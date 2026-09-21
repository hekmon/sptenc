package ffmpeg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hekmon/processpriority"
	"github.com/olekukonko/tablewriter"
	"github.com/olekukonko/tablewriter/renderer"
	"github.com/olekukonko/tablewriter/tw"
)

// Predefined VMAF model names.
const (
	VMAFModelRegularName    = "vmaf_v0.6.1"
	VMAFModelRegularNEGName = "vmaf_v0.6.1neg"
	VMAFModelUltraHDName    = "vmaf_4k_v0.6.1"
	VMAFModelUltraHDNEGName = "vmaf_4k_v0.6.1neg"
)

// VMAFModel returns the VMAF model name to use based on the desired resolution
// and whether the No-Enhancement-Gain (NEG) variant is requested.
func VMAFModel(ultraHD, neg bool) string {
	if ultraHD {
		if neg {
			return VMAFModelUltraHDNEGName
		}
		return VMAFModelUltraHDName
	}
	if neg {
		return VMAFModelRegularNEGName
	}
	return VMAFModelRegularName
}

// VMAFComputeConfig holds the parameters for a VMAF computation.
type VMAFComputeConfig struct {
	// Input
	ReferencePath  string // Path to the reference (original) video.
	DistortedPath  string // Path to the distorted (encoded) video.
	InputFrameRate string // Frame rate of the input videos (e.g. "24" or "24000/1001").
	// VMAF generation
	ReportPath        string // Path where the JSON VMAF report will be written.
	UltraHD           bool   // Use the Ultra-HD (4K) VMAF model.
	NoEnhancementGain bool   // Use the NEG (No Enhancement Gain) model variant.
	VMAFCuda          bool   // Enable CUDA-accelerated VMAF computation. NVDEC hardware decoding is automatically used for input codecs that support it.
	GPUID             *int   // Optional CUDA GPU device ID for libvmaf_cuda (used only when VMAFCuda is true).
	// Hardware decode for the software libvmaf path (ignored when VMAFCuda is true).
	HWDecoderConfig
	// Reporting
	Debug             func(msg string)          // Optional debug logger.
	RuntimeError      func(err error)           // Optional callback for non-fatal runtime errors.
	FFMPEGStatsReport func(stats ProgressStats) // Optional callback for FFmpeg progress updates.
}

// VMAFCompute runs FFmpeg with libvmaf to compare a distorted video against its
// reference and returns the parsed VMAF report. The context can be used to cancel
// the long-running ffmpeg process.
func VMAFCompute(ctx context.Context, config VMAFComputeConfig) (stats VMAFReport, err error) {
	// Validate inputs
	if config.ReferencePath == "" {
		err = errors.New("reference path cannot be empty")
		return
	}
	if config.DistortedPath == "" {
		err = errors.New("distorted path cannot be empty")
		return
	}
	if config.ReportPath == "" {
		err = errors.New("report path cannot be empty")
		return
	}
	if config.InputFrameRate == "" {
		err = errors.New("input frame rate must be set")
		return
	}
	version := VMAFModel(config.UltraHD, config.NoEnhancementGain)
	// Apply defaults
	if config.NVDevice == 0 {
		config.NVDevice = CUDADefaultDevice
	}
	if config.VAAPIDevice == "" {
		config.VAAPIDevice = VAAPIDefaultDevice
	}
	if config.D3D12Device == 0 {
		config.D3D12Device = D3D12VADefaultDevice
	}
	// Build up ffmpeg args
	args := []string{
		"-loglevel", "error", "-nostats", "-progress", "pipe:1", "-stats_period",
		strconv.FormatFloat(float64(StatsPeriod)/float64(time.Second), 'f', -1, 64),
	}
	if config.VMAFCuda {
		// CUDA VMAF path: auto-detect NVDEC compatibility
		nvdecDistorted := false
		nvdecReference := false
		if stats, err := GetStreamsInfos(ctx, GetStreamsInfosConfig{Path: config.DistortedPath}); err == nil {
			if video := stats.VideoTrack(); video != nil {
				nvdecDistorted = IsNVDecCompatible(video.CodecName)
			}
		} else if config.RuntimeError != nil {
			config.RuntimeError(fmt.Errorf("failed to probe distorted file for NVDEC auto-detection: %w, falling back to software decode", err))
		}
		if stats, err := GetStreamsInfos(ctx, GetStreamsInfosConfig{Path: config.ReferencePath}); err == nil {
			if video := stats.VideoTrack(); video != nil {
				nvdecReference = IsNVDecCompatible(video.CodecName)
			}
		} else if config.RuntimeError != nil {
			config.RuntimeError(fmt.Errorf("failed to probe reference file for NVDEC auto-detection: %w, falling back to software decode", err))
		}
		if (nvdecDistorted || nvdecReference) && config.GPUID != nil {
			args = append(args, "-init_hw_device", fmt.Sprintf("cuda=nvc:%d", *config.GPUID))
		}
		//// distorted file first
		if nvdecDistorted {
			args = append(args, "-hwaccel", "cuda")
			if config.GPUID != nil {
				args = append(args, "-hwaccel_device", "nvc")
			}
			args = append(args, "-hwaccel_output_format", "cuda")
		}
		args = append(args,
			"-r", config.InputFrameRate,
			"-i", config.DistortedPath,
		)
		//// ref file
		if nvdecReference {
			args = append(args, "-hwaccel", "cuda")
			if config.GPUID != nil {
				args = append(args, "-hwaccel_device", "nvc")
			}
			args = append(args, "-hwaccel_output_format", "cuda")
		}
		args = append(args,
			"-r", config.InputFrameRate,
			"-i", config.ReferencePath,
		)
		//// vmaf filter
		if config.GPUID != nil {
			args = append(args, "-filter_hw_device", "nvc")
		}
		var scaleDist, scaleRef string
		if nvdecDistorted {
			scaleDist = "[syncdist]scale_cuda=format=yuv420p[dist]"
		} else {
			scaleDist = "[syncdist]hwupload,scale_cuda=format=yuv420p[dist]"
		}
		if nvdecReference {
			scaleRef = "[syncref]scale_cuda=format=yuv420p[ref]"
		} else {
			scaleRef = "[syncref]hwupload,scale_cuda=format=yuv420p[ref]"
		}
		args = append(args,
			"-filter_complex",
			fmt.Sprintf(
				"[0:v]setpts=PTS-STARTPTS[syncdist];%s;[1:v]setpts=PTS-STARTPTS[syncref];%s;[dist][ref]libvmaf_cuda=model=version=%s:log_fmt=json:log_path=%s",
				scaleDist, scaleRef, version, adaptVMAFPath(config.ReportPath),
			),
		)
	} else {
		// Software libvmaf path, optionally with hardware-accelerated decoding
		distortedHW := HWDecoderConfig{}
		referenceHW := HWDecoderConfig{}
		if config.NVDec || config.VAAPIDec || config.D3D12Dec || config.VideoToolboxDec {
			distortedHW = SelectCompatibleDecoders(ctx, config.DistortedPath,
				config.NVDec, config.VAAPIDec, config.D3D12Dec, config.VideoToolboxDec,
				config.NVDevice, config.VAAPIDevice, config.D3D12Device,
			)
			referenceHW = SelectCompatibleDecoders(ctx, config.ReferencePath,
				config.NVDec, config.VAAPIDec, config.D3D12Dec, config.VideoToolboxDec,
				config.NVDevice, config.VAAPIDevice, config.D3D12Device,
			)
		}
		//// distorted file first
		args = appendHWAccelArgs(args, distortedHW)
		args = append(args,
			"-r", config.InputFrameRate,
			"-i", config.DistortedPath,
		)
		//// ref file
		args = appendHWAccelArgs(args, referenceHW)
		args = append(args,
			"-r", config.InputFrameRate,
			"-i", config.ReferencePath,
		)
		//// vmaf filter
		args = append(args,
			"-filter_complex",
			fmt.Sprintf(
				"[0:v]setpts=PTS-STARTPTS[distorted];[1:v]setpts=PTS-STARTPTS[reference];[distorted][reference]libvmaf=model=version=%s:log_fmt=json:log_path=%s:n_threads=%d",
				version, adaptVMAFPath(config.ReportPath), NbThreadsToUse,
			),
		)
	}
	//// no ffmpeg output
	args = append(args, "-f", "null", "-")
	// Prepare command
	if config.Debug != nil {
		config.Debug(fmt.Sprintf("Compute VMAF with: %s", getPrintableCMDLine(FFMPEGBinary, args)))
	}
	cmd := exec.CommandContext(ctx, FFMPEGBinary, args...)
	//// Prepare output handling
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		err = fmt.Errorf("error setting up stdout pipe: %w", err)
		return
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		err = fmt.Errorf("error setting up stderr pipe: %w", err)
		return
	}
	// Start program
	if err = cmd.Start(); err != nil {
		err = fmt.Errorf("error starting %s: %w\n%s", FFMPEGBinary, err, getPrintableCMDLine(FFMPEGBinary, args))
		return
	}
	// Start progress monitoring (after cmd.Start to avoid goroutine leak on error)
	progressDone := make(chan struct{})
	go func() {
		defer close(progressDone)
		standardProgress(stdoutPipe, config.FFMPEGStatsReport, config.RuntimeError)
	}()
	stderrDone := make(chan struct{})
	go func() {
		stderrForwarder(stderrPipe, config.RuntimeError)
		close(stderrDone)
	}()
	if err = processpriority.Set(cmd.Process.Pid, ProcessPriority); err != nil && config.RuntimeError != nil {
		config.RuntimeError(fmt.Errorf("Failed to lower %s process priority: %w", FFMPEGBinary, err))
	}
	<-progressDone
	<-stderrDone
	if err = cmd.Wait(); err != nil {
		err = fmt.Errorf("error during %s execution: %w\n%s", FFMPEGBinary, err, getPrintableCMDLine(FFMPEGBinary, args))
		return
	}
	// Parse report
	reportFd, err := os.Open(config.ReportPath)
	if err != nil {
		err = fmt.Errorf("failed to open VMAF report file: %w", err)
		return
	}
	defer reportFd.Close()
	if err = json.NewDecoder(reportFd).Decode(&stats); err != nil {
		err = fmt.Errorf("error parsing VMAF JSON output: %w", err)
		return
	}
	return
}

// appendHWAccelArgs appends the appropriate -hwaccel flags for the given decoder config.
func appendHWAccelArgs(args []string, dec HWDecoderConfig) []string {
	if dec.NVDec {
		args = append(args, "-hwaccel", "cuda")
		if dec.NVDevice >= 0 {
			args = append(args, "-hwaccel_device", strconv.Itoa(dec.NVDevice))
		}
	} else if dec.VAAPIDec {
		args = append(args, "-hwaccel", "vaapi")
		if dec.VAAPIDevice != "" {
			args = append(args, "-vaapi_device", dec.VAAPIDevice)
		}
	} else if dec.D3D12Dec {
		args = append(args, "-hwaccel", "d3d12va")
		if dec.D3D12Device >= 0 {
			args = append(args, "-hwaccel_device", strconv.Itoa(dec.D3D12Device))
		}
	} else if dec.VideoToolboxDec {
		args = append(args, "-hwaccel", "videotoolbox")
	}
	return args
}

// VMAFReport is the top-level structure of the JSON report produced by libvmaf.
type VMAFReport struct {
	Version          string            `json:"version"`
	FPS              float64           `json:"fps"`
	Frames           VMAFFrames        `json:"frames"`
	PooledMetrics    VMAFPooledMetrics `json:"pooled_metrics"`
	AggregateMetrics struct{}          `json:"aggregate_metrics"`
}

// VMAF percentile constants for statistics calculation.
const (
	vmafPercentile1  = 1
	vmafPercentile5  = 5
	vmafPercentile10 = 10
	vmafPercentile25 = 25
	vmafPercentile50 = 50 // Median
)

// GetStats extracts summary statistics from the VMAF report, including
// percentiles that are not present in the raw pooled metrics.
func (vr VMAFReport) GetStats() (vs VMAFStats) {
	vs.Version = vr.Version
	// Copy existing metrics
	vs.Minimum = vr.PooledMetrics.VMAF.Min
	vs.HarmonicMean = vr.PooledMetrics.VMAF.HarmonicMean
	vs.Mean = vr.PooledMetrics.VMAF.Mean
	vs.Maximum = vr.PooledMetrics.VMAF.Max
	// Compute the missing ones
	sort.Sort(vr.Frames)
	vs.Percentile1 = vr.Frames.VMAFPercentile(vmafPercentile1)
	vs.Percentile5 = vr.Frames.VMAFPercentile(vmafPercentile5)
	vs.Percentile10 = vr.Frames.VMAFPercentile(vmafPercentile10)
	vs.Percentile25 = vr.Frames.VMAFPercentile(vmafPercentile25)
	vs.Median = vr.Frames.VMAFPercentile(vmafPercentile50)
	return
}

// VMAFFrames is a slice of per-frame VMAF measurements.
type VMAFFrames []*VMAFFrame

// Len returns the number of frames. It implements sort.Interface.
func (vf VMAFFrames) Len() int {
	return len(vf)
}

// Less reports whether frame i should sort before frame j based on VMAF score.
// It implements sort.Interface.
func (vf VMAFFrames) Less(i, j int) bool {
	if vf[i] == nil {
		return true
	}
	if vf[j] == nil {
		return false
	}
	return vf[i].Metrics.VMAF < vf[j].Metrics.VMAF
}

// Swap exchanges frames i and j. It implements sort.Interface.
func (vf VMAFFrames) Swap(i, j int) {
	vf[i], vf[j] = vf[j], vf[i]
}

// VMAFPercentile returns the VMAF value at the given percentile.
// The receiver must be sorted by VMAF score before calling this method.
func (vf VMAFFrames) VMAFPercentile(p float64) float64 {
	// vf must have been sorted !
	index := int(math.Round(float64(len(vf))/100*p)) - 1
	if index == -1 {
		index = 0
	}
	if index < 0 || index >= len(vf) {
		return 0
	}
	return vf[index].Metrics.VMAF
}

// VMAFFrame represents the VMAF metrics for a single frame.
type VMAFFrame struct {
	FrameNum int              `json:"frameNum"`
	Metrics  VMAFFrameMetrics `json:"metrics"`
}

// VMAFFrameMetrics holds the individual metric values reported by libvmaf for one frame.
type VMAFFrameMetrics struct {
	IntegerAdm2      float64 `json:"integer_adm2"`
	IntegerAdmScale0 float64 `json:"integer_adm_scale0"`
	IntegerAdmScale1 float64 `json:"integer_adm_scale1"`
	IntegerAdmScale2 float64 `json:"integer_adm_scale2"`
	IntegerAdmScale3 float64 `json:"integer_adm_scale3"`
	IntegerMotion2   float64 `json:"integer_motion2"`
	IntegerMotion    float64 `json:"integer_motion"`
	IntegerVifScale0 float64 `json:"integer_vif_scale0"`
	IntegerVifScale1 float64 `json:"integer_vif_scale1"`
	IntegerVifScale2 float64 `json:"integer_vif_scale2"`
	IntegerVifScale3 float64 `json:"integer_vif_scale3"`
	VMAF             float64 `json:"vmaf"`
}

// VMAFPooledMetrics aggregates metrics over the entire sequence.
type VMAFPooledMetrics struct {
	IntegerAdm2      VMAFPooledMetric `json:"integer_adm2"`
	IntegerAdmScale0 VMAFPooledMetric `json:"integer_adm_scale0"`
	IntegerAdmScale1 VMAFPooledMetric `json:"integer_adm_scale1"`
	IntegerAdmScale2 VMAFPooledMetric `json:"integer_adm_scale2"`
	IntegerAdmScale3 VMAFPooledMetric `json:"integer_adm_scale3"`
	IntegerMotion2   VMAFPooledMetric `json:"integer_motion2"`
	IntegerMotion    VMAFPooledMetric `json:"integer_motion"`
	IntegerVifScale0 VMAFPooledMetric `json:"integer_vif_scale0"`
	IntegerVifScale1 VMAFPooledMetric `json:"integer_vif_scale1"`
	IntegerVifScale2 VMAFPooledMetric `json:"integer_vif_scale2"`
	IntegerVifScale3 VMAFPooledMetric `json:"integer_vif_scale3"`
	VMAF             VMAFPooledMetric `json:"vmaf"`
}

// VMAFPooledMetric holds aggregate values (min, max, mean, harmonic mean) for a single metric.
type VMAFPooledMetric struct {
	Min          float64 `json:"min"`
	Max          float64 `json:"max"`
	Mean         float64 `json:"mean"`
	HarmonicMean float64 `json:"harmonic_mean"`
}

// VMAFStats is a user-friendly summary of VMAF results, including computed percentiles.
type VMAFStats struct {
	Version      string
	Minimum      float64 `json:"min"`
	Percentile1  float64 `json:"p1"`
	Percentile5  float64 `json:"p5"`
	Percentile10 float64 `json:"p10"`
	Percentile25 float64 `json:"p25"`
	Median       float64 `json:"median"`
	HarmonicMean float64 `json:"harmonic_mean"`
	Mean         float64 `json:"mean"`
	Maximum      float64 `json:"max"`
}

// String renders the VMAF statistics as an aligned plain-text table.
func (vs VMAFStats) String() string {
	var tableBuffer strings.Builder
	table := tablewriter.NewTable(&tableBuffer,
		tablewriter.WithRenderer(renderer.NewBlueprint(tw.Rendition{
			Borders: tw.BorderNone,
			Symbols: tw.NewSymbolCustom("box").
				WithRow("─").
				WithColumn("│").
				WithCenter("┼"),
		})),
		tablewriter.WithConfig(tablewriter.Config{
			Header: tw.CellConfig{
				Formatting: tw.CellFormatting{
					AutoFormat: tw.Off,
				},
			},
			Row: tw.CellConfig{
				Alignment: tw.CellAlignment{Global: tw.AlignCenter},
			},
		}),
	)
	table.Header([]string{
		"Min",
		"P1",
		"P5",
		"P10",
		"P25",
		"Median",
		"Harmonic Mean",
		"Mean",
		"Max",
	})
	table.Append([]string{
		strconv.FormatFloat(vs.Minimum, 'f', -1, float64Precision),
		strconv.FormatFloat(vs.Percentile1, 'f', -1, float64Precision),
		strconv.FormatFloat(vs.Percentile5, 'f', -1, float64Precision),
		strconv.FormatFloat(vs.Percentile10, 'f', -1, float64Precision),
		strconv.FormatFloat(vs.Percentile25, 'f', -1, float64Precision),
		strconv.FormatFloat(vs.Median, 'f', -1, float64Precision),
		strconv.FormatFloat(vs.HarmonicMean, 'f', -1, float64Precision),
		strconv.FormatFloat(vs.Mean, 'f', -1, float64Precision),
		strconv.FormatFloat(vs.Maximum, 'f', -1, float64Precision),
	})
	table.Render()
	return tableBuffer.String()
}

// vmafPathEscaper applies the two levels of escaping needed by a filter option value
// embedded in a filtergraph description (without any quoting):
//   - 1st level, the filter option value: \ ' and : are special
//   - 2nd level, the filtergraph description: \ ' [ ] , and ; are special
//
// Each replacement below is the 1st level escaping of the character, escaped again for the 2nd level.
// https://ffmpeg.org/ffmpeg-filters.html#Notes-on-filtergraph-escaping
//
// The same escaping works on every platform: Windows paths do not need their backslashes
// converted, only escaped (checked against a Windows ffmpeg build, drive colon included).
var vmafPathEscaper = strings.NewReplacer(
	`\`, `\\\\`,
	`'`, `\\\'`,
	`:`, `\\:`,
	`[`, `\[`,
	`]`, `\]`,
	`,`, `\,`,
	`;`, `\;`,
)

// adaptVMAFPath escapes a file path to be used as the libvmaf log_path within a filtergraph description.
func adaptVMAFPath(path string) string {
	return vmafPathEscaper.Replace(path)
}
