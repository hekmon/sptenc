package ffmpegutils

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"

	"github.com/hekmon/processpriority"
	"github.com/olekukonko/tablewriter"
)

const (
	VMAFModelRegularName = "vmaf_v0.6.1"
	VMAFModelUltraHDName = "vmaf_4k_v0.6.1"
	UltraHDHeight        = 2160
)

type VMAFComputeConfig struct {
	// Input
	ReferencePath string
	DistortedPath string
	// VMAF generation
	ProcessPriority processpriority.ProcessPriority // BelowNormal recommended
	ReportPath      string
	UltraHD         bool
	VideoCuda       bool
	VMAFCuda        bool
	GPUs            []int
	// Reporting
	Debug               func(msg string)
	RuntimeError        func(err error)                          // non fatal errors
	ProcessRegistration func(process *os.Process, register bool) // true to register, false to unregister. Must be idempotent.
	FFMPEGStatsReport   func(stats ProgressStats)
}

func VMAFCompute(config VMAFComputeConfig) (stats VMAFReport, err error) {
	// Prepare
	var version string
	if config.UltraHD {
		version = VMAFModelUltraHDName
	} else {
		version = VMAFModelRegularName
	}
	// Build up ffmpeg args
	args := []string{"-loglevel", "error", "-stats"}
	//// distorted file first
	if config.VideoCuda || config.VMAFCuda {
		args = append(args, "-hwaccel", "cuda")
		if len(config.GPUs) > 0 {
			args = append(args, "-hwaccel_device", strconv.Itoa(config.GPUs[0]))
		}
		if config.VMAFCuda {
			args = append(args, "-hwaccel_output_format", "cuda")
		}
	}
	args = append(args, "-i", config.DistortedPath)
	//// ref file
	if config.VideoCuda || config.VMAFCuda {
		args = append(args, "-hwaccel", "cuda")
		if len(config.GPUs) > 0 {
			args = append(args, "-hwaccel_device", strconv.Itoa(config.GPUs[0]))
		}
		if config.VMAFCuda {
			args = append(args, "-hwaccel_output_format", "cuda")
		}
	}
	args = append(args, "-i", config.ReferencePath)
	//// vmaf filter
	if config.VMAFCuda {
		args = append(args,
			"-filter_complex",
			fmt.Sprintf(
				"[0:v]scale_cuda=format=yuv420p[dist];[1:v]scale_cuda=format=yuv420p[ref];[dist][ref]libvmaf_cuda=model=version=%s:log_fmt=json:log_path=%s",
				version, AdaptVMAFPath(config.ReportPath),
			),
		)
	} else {
		args = append(args,
			"-filter_complex",
			fmt.Sprintf(
				"[0:v]setpts=PTS-STARTPTS[distorted];[1:v]setpts=PTS-STARTPTS[reference];[distorted][reference]libvmaf=model=version=%s:log_fmt=json:log_path=%s:n_threads=%d",
				version, AdaptVMAFPath(config.ReportPath), NbThreadsToUse,
			),
		)
	}
	//// no ffmpeg output
	args = append(args, "-f", "null", "-")
	// Prepare command
	if config.Debug != nil {
		config.Debug(fmt.Sprintf("Compute VMAF with: %s \"%s\"", FFMPEGBinary, strings.Join(args, "\" \"")))
	}
	cmd := exec.Command(FFMPEGBinary, args...)
	cmd.Stdout = nil
	//// Prepare output handling
	outputPipe, err := cmd.StderrPipe()
	if err != nil {
		err = fmt.Errorf("error setting up stdout pipe: %w", err)
		return
	}
	progressDone := make(chan struct{})
	go func() {
		defer close(progressDone)
		vmafProgress(outputPipe, config.FFMPEGStatsReport, config.RuntimeError)
	}()
	// Start program
	if err = cmd.Start(); err != nil {
		err = fmt.Errorf("error starting %s: %w", FFMPEGBinary, err)
		return
	}
	if config.ProcessRegistration != nil {
		config.ProcessRegistration(cmd.Process, true)
		defer config.ProcessRegistration(cmd.Process, false)
	}
	if err = processpriority.Set(cmd.Process.Pid, config.ProcessPriority); err != nil && config.RuntimeError != nil {
		config.RuntimeError(fmt.Errorf("Failed to lower probbing process priority: %w", err))
	}
	if err = cmd.Wait(); err != nil {
		err = fmt.Errorf("error during %s execution: %w", FFMPEGBinary, err)
		return
	}
	if config.ProcessRegistration != nil {
		// remove early (do not wait for defer safety)
		config.ProcessRegistration(cmd.Process, false)
	}
	<-progressDone
	// Parse report
	reportFd, err := os.Open(config.ReportPath)
	if err != nil {
		err = fmt.Errorf("failed to open VMAF report file %q: %w", config.ReportPath, err)
		return
	}
	defer reportFd.Close()
	if err = json.NewDecoder(reportFd).Decode(&stats); err != nil {
		err = fmt.Errorf("error parsing VMAF JSON output: %w", err)
		return
	}
	return
}

func vmafProgress(ffmpegOutput io.ReadCloser, progress func(stats ProgressStats), runtimeError func(error)) {
	output := bufio.NewReader(ffmpegOutput)
	var (
		err         error
		r           rune
		currentLine string
		stats       ProgressStats
	)
	// Read rune by rune until EOF
	lineBuffer := bytes.NewBuffer(nil)
	for {
		// Read a rune a write it to our buffer
		if r, _, err = output.ReadRune(); err != nil {
			if !errors.Is(err, io.EOF) && runtimeError != nil {
				runtimeError(fmt.Errorf("error while reading rune from ffmpeg output: %w", err))
			}
			return
		}
		lineBuffer.WriteRune(r)
		// Is this a complete line ?
		switch r {
		case '\n':
			lineBuffer.Reset()
		case 'x':
			currentLine = lineBuffer.String()
			if !strings.Contains(currentLine, "speed=") {
				continue
			}
			// We are near the end of a line (speed=XX.Xx) before line clear
			if stats, err = ParseProgressStats(currentLine); err != nil {
				if runtimeError != nil {
					runtimeError(fmt.Errorf("error parsing ffmpeg progress line: %e", err))
				}
			} else {
				if progress != nil {
					progress(stats)
				}
			}
			lineBuffer.Reset()
		}
	}
}

type VMAFReport struct {
	Version          string            `json:"version"`
	FPS              float64           `json:"fps"`
	Frames           VMAFFrames        `json:"frames"`
	PooledMetrics    VMAFPooledMetrics `json:"pooled_metrics"`
	AggregateMetrics struct{}          `json:"aggregate_metrics"`
}

func (vr VMAFReport) GetStats() (vs VMAFStats) {
	// Copy existing metrics
	vs.Minimum = vr.PooledMetrics.VMAF.Min
	vs.HarmonicMean = vr.PooledMetrics.VMAF.HarmonicMean
	vs.Mean = vr.PooledMetrics.VMAF.Mean
	vs.Maximum = vr.PooledMetrics.VMAF.Max
	// Compute the missing ones
	sort.Sort(vr.Frames)
	vs.Percentile1 = vr.Frames.VMAFPercentile(1)
	vs.Percentile5 = vr.Frames.VMAFPercentile(5)
	vs.Percentile10 = vr.Frames.VMAFPercentile(10)
	vs.Percentile25 = vr.Frames.VMAFPercentile(25)
	vs.Median = vr.Frames.VMAFPercentile(50)
	return
}

type VMAFFrames []*VMAFFrame

func (vf VMAFFrames) Len() int {
	return len(vf)
}

func (vf VMAFFrames) Less(i, j int) bool {
	if vf[i] == nil {
		return true
	}
	if vf[j] == nil {
		return false
	}
	return vf[i].Metrics.VMAF < vf[j].Metrics.VMAF
}

func (vf VMAFFrames) Swap(i, j int) {
	vf[i], vf[j] = vf[j], vf[i]
}

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

type VMAFFrame struct {
	FrameNum int              `json:"frameNum"`
	Metrics  VMAFFrameMetrics `json:"metrics"`
}

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

type VMAFPooledMetric struct {
	Min          float64 `json:"min"`
	Max          float64 `json:"max"`
	Mean         float64 `json:"mean"`
	HarmonicMean float64 `json:"harmonic_mean"`
}

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

func (vs VMAFStats) String() string {
	var tableBuffer strings.Builder
	table := tablewriter.NewWriter(&tableBuffer)
	table.SetHeader([]string{
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
		strconv.FormatFloat(vs.Minimum, 'f', -1, 64),
		strconv.FormatFloat(vs.Percentile1, 'f', -1, 64),
		strconv.FormatFloat(vs.Percentile5, 'f', -1, 64),
		strconv.FormatFloat(vs.Percentile10, 'f', -1, 64),
		strconv.FormatFloat(vs.Percentile25, 'f', -1, 64),
		strconv.FormatFloat(vs.Median, 'f', -1, 64),
		strconv.FormatFloat(vs.HarmonicMean, 'f', -1, 64),
		strconv.FormatFloat(vs.Mean, 'f', -1, 64),
		strconv.FormatFloat(vs.Maximum, 'f', -1, 64),
	})
	table.SetColumnAlignment([]int{
		tablewriter.ALIGN_CENTER,
		tablewriter.ALIGN_CENTER,
		tablewriter.ALIGN_CENTER,
		tablewriter.ALIGN_CENTER,
		tablewriter.ALIGN_CENTER,
		tablewriter.ALIGN_CENTER,
		tablewriter.ALIGN_CENTER,
		tablewriter.ALIGN_CENTER,
		tablewriter.ALIGN_CENTER,
	})
	table.SetCenterSeparator("┼")
	table.SetRowSeparator("─")
	table.SetColumnSeparator("│")
	table.SetBorder(false)
	table.Render()
	return tableBuffer.String()
}
