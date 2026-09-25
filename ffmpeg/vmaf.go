package ffmpeg

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hekmon/processpriority"
	"github.com/olekukonko/tablewriter"
	"github.com/olekukonko/tablewriter/renderer"
	"github.com/olekukonko/tablewriter/tw"
)

/*
 * Models
 */

// VMAFModel names a VMAF model built into libvmaf. sptenc selects between two VMAF v1 models
// (libvmaf 3.2.0 or newer, see VMAFModels) and scores with any other one libvmaf knows when the
// user forces it: whether libvmaf knows a name is for VMAFProbe to tell, so a model released
// after sptenc can be used without waiting for an update. The v1 models fuse ADM (with an
// additive impairment term for blockiness), motion, CAMBI (banding) and a chroma feature; the
// enhancement gain is clamped in all of them (what used to be the NEG variants of v0), and VIF
// is gone. See resource/doc/models_v1.md in the libvmaf repository.
type VMAFModel string

const (
	// VMAFModelFHD predicts the viewing condition of a 1080p display watched from 3 picture
	// heights: the v1 successor of vmaf_v0.6.1.
	VMAFModelFHD VMAFModel = "vmaf_v1.0.16_3d0h"
	// VMAFModelUHD predicts the viewing condition of a 2160p display watched from 1.5 picture
	// heights, the distance at which 4K is worth it: the v1 successor of vmaf_4k_v0.6.1.
	VMAFModelUHD VMAFModel = "vmaf_v1.0.16_1d5h_2160"
	// The other v1 models libvmaf builds in (all of them since 3.2.0), never selected: named
	// for the messages of a run that forces one (see VMAFForcedModels).
	VMAFModelPhone     VMAFModel = "vmaf_v1.0.16_5d0h"
	VMAFModelUHDFar    VMAFModel = "vmaf_v1.0.16_3d0h_2160"
	VMAFModelFHDHFR    VMAFModel = "vmaf_v1.0.16_hfr_3d0h"
	VMAFModelUHDHFR    VMAFModel = "vmaf_v1.0.16_hfr_1d5h_2160"
	VMAFModelPhoneHFR  VMAFModel = "vmaf_v1.0.16_hfr_5d0h"
	VMAFModelUHDFarHFR VMAFModel = "vmaf_v1.0.16_hfr_3d0h_2160"
	// The v0 models sptenc v0.1.0 selected, never selected anymore: named for the messages of
	// a run that forces one (see VMAFV0Models).
	VMAFModelV0FHD    VMAFModel = "vmaf_v0.6.1"
	VMAFModelV0FHDNEG VMAFModel = "vmaf_v0.6.1neg"
	VMAFModelV0UHD    VMAFModel = "vmaf_4k_v0.6.1"
	VMAFModelV0UHDNEG VMAFModel = "vmaf_4k_v0.6.1neg"
)

// VMAFModels lists the models sptenc selects, from the height of the source (see
// SelectVMAFModel), and the ones it checks and lists: the viewing conditions where the small
// artifacts a quality floor is about are seen, at the frame rates the models are made for.
var VMAFModels = []VMAFModel{VMAFModelFHD, VMAFModelUHD}

// VMAFForcedModels lists the other v1 models libvmaf builds in, which sptenc never selects:
// the phone (5 picture heights) and the 4K at 3 picture heights viewing conditions are
// lenient, small artifacts are not seen from there (the latter also scores up to 110, above
// what a threshold can ask for), and the high frame rate variants, made for ~50/60 fps, are in
// Netflix's words "an area of active improvement". Forcing one is the user's call.
var VMAFForcedModels = []VMAFModel{VMAFModelPhone, VMAFModelUHDFar,
	VMAFModelFHDHFR, VMAFModelUHDHFR, VMAFModelPhoneHFR, VMAFModelUHDFarHFR}

// VMAFV0Models lists the v0 models, which sptenc v0.1.0 selected and libvmaf still builds in
// (the 4K NEG one since 3.0). sptenc never selects them: they measure luma only and do not see
// banding (their NEG variants barely do). Forcing one scores like the published VMAF anchors
// were measured, or like a v0.1.0 encode.
var VMAFV0Models = []VMAFModel{VMAFModelV0FHD, VMAFModelV0FHDNEG, VMAFModelV0UHD, VMAFModelV0UHDNEG}

// IsV0 reports whether the model is one of the v0 models (see VMAFV0Models).
func (m VMAFModel) IsV0() bool {
	for _, candidate := range VMAFV0Models {
		if m == candidate {
			return true
		}
	}
	return false
}

// vmafModelNameRe is what a model name may be made of: it is written into an ffmpeg filter
// graph, where ':', '=', ',' or ';' would be read as options or filters.
var vmafModelNameRe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// String returns the model name as libvmaf knows it.
func (m VMAFModel) String() string {
	return string(m)
}

// Valid reports whether the name can be handed to libvmaf. Whether libvmaf knows the model is
// for VMAFProbe to tell: any model it knows is accepted, a future one included.
func (m VMAFModel) Valid() bool {
	return vmafModelNameRe.MatchString(string(m))
}

// Description returns the viewing condition the model predicts, empty for a model sptenc does
// not know.
func (m VMAFModel) Description() string {
	switch m {
	case VMAFModelFHD:
		return "1080p display at 3 picture heights"
	case VMAFModelUHD:
		return "2160p display at 1.5 picture heights"
	case VMAFModelPhone:
		return "phone (1080p) at 5 picture heights"
	case VMAFModelUHDFar:
		return "2160p display at 3 picture heights, scored up to 110"
	case VMAFModelFHDHFR:
		return "1080p display at 3 picture heights, ~50/60 fps"
	case VMAFModelUHDHFR:
		return "2160p display at 1.5 picture heights, ~50/60 fps"
	case VMAFModelPhoneHFR:
		return "phone (1080p) at 5 picture heights, ~50/60 fps"
	case VMAFModelUHDFarHFR:
		return "2160p display at 3 picture heights, ~50/60 fps, scored up to 110"
	case VMAFModelV0FHD:
		return "1080p display at 3 picture heights"
	case VMAFModelV0FHDNEG:
		return "1080p display at 3 picture heights, no enhancement gain"
	case VMAFModelV0UHD:
		return "2160p display at 1.5 picture heights"
	case VMAFModelV0UHDNEG:
		return "2160p display at 1.5 picture heights, no enhancement gain"
	default:
		return ""
	}
}

// displayHeight returns the height of the display the model predicts: 2160 for the 4K models,
// 1080 for the others (the phone ones included), 0 for a model sptenc does not know.
func (m VMAFModel) displayHeight() int {
	switch m {
	case VMAFModelUHD, VMAFModelUHDFar, VMAFModelUHDHFR, VMAFModelUHDFarHFR,
		VMAFModelV0UHD, VMAFModelV0UHDNEG:
		return Height4K
	case VMAFModelFHD, VMAFModelPhone, VMAFModelFHDHFR, VMAFModelPhoneHFR,
		VMAFModelV0FHD, VMAFModelV0FHDNEG:
		return 1080
	default:
		return 0
	}
}

// SelectVMAFModel returns the model matching the resolution of a source: the 4K model from
// 2160 lines up, the 1080p model below.
func SelectVMAFModel(height int) VMAFModel {
	if height >= Height4K {
		return VMAFModelUHD
	}
	return VMAFModelFHD
}

// ResolutionMismatch explains why the model is not made for the display of a source of the
// given height (see SelectVMAFModel), an empty string when it is, or when sptenc does not know
// the model. Only the display is compared: the viewing distance and the frame rate of a forced
// model are the user's choice.
func (m VMAFModel) ResolutionMismatch(height int) string {
	expected := SelectVMAFModel(height)
	if display := m.displayHeight(); display == 0 || display == expected.displayHeight() {
		return ""
	}
	return fmt.Sprintf("%s (%s) is made for another display than this %dp source, %s is the model of this resolution",
		m, m.Description(), height, expected)
}

/*
 * Filter
 */

// vmafFilter returns the libvmaf filter of a computation, to be fed the distorted stream then
// the reference one.
func vmafFilter(model VMAFModel, reportPath string, threads int) string {
	return fmt.Sprintf("libvmaf=model=version=%s:log_fmt=json:log_path=%s:n_threads=%d",
		model, adaptVMAFPath(reportPath), threads)
}

/*
 * Probe
 */

// ErrVMAFModelUnavailable is returned by VMAFProbe when the libvmaf of ffmpeg does not know
// the model (the v1 models are built into libvmaf 3.2.0 and newer).
var ErrVMAFModelUnavailable = errors.New("libvmaf does not know this model")

// Probe pictures: 1080p unless told otherwise, the resolution the models are made for (smaller
// pictures are not scored by every model, see VMAFProbeConfig), and the fewest frames a report
// can be built from.
const (
	vmafProbeWidth    = 1920
	vmafProbeHeight   = 1080
	vmafProbeFrames   = 2
	vmafProbeFileMask = "sptenc-vmaf-probe-*.json"
)

// vmafModelUnavailableMarker is what ffmpeg prints when libvmaf can not load a model by its
// version name.
const vmafModelUnavailableMarker = "could not load libvmaf model with version"

// VMAFProbeConfig holds the parameters of a libvmaf probe.
type VMAFProbeConfig struct {
	Model VMAFModel // Model to load.
	// Size of the probe pictures, 1920x1080 when zero. Below a minimum size libvmaf crashes, or
	// writes no report, and that minimum depends on the model and on the aspect ratio, which a
	// fixed floor can not follow. Measured with libvmaf f85a8536 (a September 2026 build): the
	// 1080p and 4K models of VMAFModels score 216x160 but not 1920x160 nor 3840x180, the phone
	// models need 480x270 and the 4K at 3 picture heights ones 568x320 at 16:9. Probing with the size
	// of the source tells for sure, whatever the model and the libvmaf version.
	Width, Height int
	ReportDir     string           // Directory the JSON report of the probe is written to (and removed from).
	Debug         func(msg string) // Optional debug logger.
}

// VMAFProbe checks that the libvmaf of ffmpeg can score pictures of a given size with a model,
// and returns the libvmaf version. It runs libvmaf for real on two synthetic frames generated
// by ffmpeg itself (no file needed), scored against themselves. Success is a report on disk:
// the exit code of ffmpeg is not to be trusted, libvmaf errors have been seen leaving it at
// zero with no report written. A model libvmaf does not know is reported with
// ErrVMAFModelUnavailable.
func VMAFProbe(ctx context.Context, config VMAFProbeConfig) (libvmafVersion string, err error) {
	if !config.Model.Valid() {
		err = fmt.Errorf("invalid VMAF model name %q", config.Model)
		return
	}
	if config.Width == 0 || config.Height == 0 {
		config.Width, config.Height = vmafProbeWidth, vmafProbeHeight
	}
	// Report file, unique in case of concurrent probes
	reportFd, err := os.CreateTemp(config.ReportDir, vmafProbeFileMask)
	if err != nil {
		err = fmt.Errorf("failed to create the probe report file: %w", err)
		return
	}
	reportPath := reportFd.Name()
	reportFd.Close()
	defer os.Remove(reportPath)
	// One generated stream split in two: the distorted side is the reference itself
	args := []string{
		"-loglevel", "error", "-nostats", "-nostdin", "-y",
		"-f", "lavfi", "-i", fmt.Sprintf("testsrc2=size=%dx%d:rate=%d:duration=1", config.Width, config.Height, vmafProbeFrames),
		"-filter_complex", fmt.Sprintf("[0:v]format=yuv420p10le,split[distorted][reference];[distorted][reference]%s",
			vmafFilter(config.Model, reportPath, 1)),
		"-f", "null", "-",
	}
	if config.Debug != nil {
		config.Debug(fmt.Sprintf("Probe libvmaf with: %s", getPrintableCMDLine(FFMPEGBinary, args)))
	}
	cmd := exec.CommandContext(ctx, FFMPEGBinary, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	output := strings.TrimSpace(stderr.String())
	if strings.Contains(output, vmafModelUnavailableMarker) {
		err = fmt.Errorf("%w: %s", ErrVMAFModelUnavailable, output)
		return
	}
	if runErr != nil {
		err = fmt.Errorf("error during %s execution: %w\n%s\n%s", FFMPEGBinary, runErr, output, getPrintableCMDLine(FFMPEGBinary, args))
		return
	}
	// Parse the report
	report, err := readVMAFReport(reportPath)
	if err != nil {
		err = fmt.Errorf("libvmaf did not produce a usable report with model %s: %w\n%s", config.Model, err, output)
		return
	}
	if len(report.Frames) != vmafProbeFrames {
		err = fmt.Errorf("libvmaf scored %d frames out of %d with model %s\n%s", len(report.Frames), vmafProbeFrames, config.Model, output)
		return
	}
	if libvmafVersion = report.Version; libvmafVersion == "" {
		libvmafVersion = "unknown"
	}
	return
}

/*
 * Compute
 */

// VMAFComputeConfig holds the parameters for a VMAF computation.
type VMAFComputeConfig struct {
	// Input
	ReferencePath  string // Path to the reference (original) video.
	DistortedPath  string // Path to the distorted (encoded) video.
	InputFrameRate string // Frame rate of the input videos (e.g. "24" or "24000/1001").
	// VMAF generation
	ReportPath string    // Path where the JSON VMAF report will be written.
	Model      VMAFModel // Model to score with (see SelectVMAFModel).
	// Hardware decode
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
	if !config.Model.Valid() {
		err = fmt.Errorf("invalid VMAF model name %q", config.Model)
		return
	}
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
	// Hardware-accelerated decoding, when requested and when the codec allows it
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
		"[0:v]setpts=PTS-STARTPTS[distorted];[1:v]setpts=PTS-STARTPTS[reference];[distorted][reference]"+
			vmafFilter(config.Model, config.ReportPath, NbThreadsToUse),
	)
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
	return readVMAFReport(config.ReportPath)
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

// readVMAFReport reads and parses the JSON report written by libvmaf.
func readVMAFReport(path string) (report VMAFReport, err error) {
	reportFd, err := os.Open(path)
	if err != nil {
		err = fmt.Errorf("failed to open VMAF report file: %w", err)
		return
	}
	defer reportFd.Close()
	if err = json.NewDecoder(reportFd).Decode(&report); err != nil {
		err = fmt.Errorf("error parsing VMAF JSON output: %w", err)
		return
	}
	return
}

/*
 * Report
 */

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
	vs.CAMBIMean = vr.PooledMetrics.CAMBI.Mean
	vs.CAMBIMax = vr.PooledMetrics.CAMBI.Max
	vs.HasCAMBI = vr.PooledMetrics.HasCAMBI
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

// Metric keys of a libvmaf report. The score is always "vmaf". The features are keyed by a
// name templated from their options (cambi_hrs_1080_cmxv_17_vlt_0.06 for the CAMBI of the
// v1.0.16 models): a model retrained with other options changes the key, hence a prefix match.
const (
	vmafMetricKey     = "vmaf"
	cambiMetricPrefix = "cambi"
)

// findMetricKeys returns the keys of the score and of the CAMBI feature among the metrics
// of a report, empty when absent. Keys are scanned in order for a deterministic pick.
func findMetricKeys(keys []string) (vmafKey, cambiKey string) {
	sort.Strings(keys)
	for _, key := range keys {
		switch {
		case key == vmafMetricKey:
			vmafKey = key
		case cambiKey == "" && strings.HasPrefix(key, cambiMetricPrefix):
			cambiKey = key
		}
	}
	return
}

// VMAFFrameMetrics holds the metrics of one frame sptenc reads out of the ones libvmaf reports:
// the score, and the CAMBI banding feature the v1 models are fed with (0 is no banding, around
// 5 is where it starts to be slightly annoying, the v1 models cap it at 17).
type VMAFFrameMetrics struct {
	VMAF  float64
	CAMBI float64
}

// UnmarshalJSON picks the score and the CAMBI feature among the metrics of a frame.
func (m *VMAFFrameMetrics) UnmarshalJSON(data []byte) error {
	var raw map[string]float64
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	keys := make([]string, 0, len(raw))
	for key := range raw {
		keys = append(keys, key)
	}
	vmafKey, cambiKey := findMetricKeys(keys)
	if vmafKey == "" {
		return fmt.Errorf("no %q metric in frame metrics", vmafMetricKey)
	}
	m.VMAF = raw[vmafKey]
	m.CAMBI = raw[cambiKey] // zero when absent
	return nil
}

// VMAFPooledMetrics aggregates over the entire sequence the metrics sptenc reads (see
// VMAFFrameMetrics).
type VMAFPooledMetrics struct {
	VMAF  VMAFPooledMetric
	CAMBI VMAFPooledMetric
	// HasCAMBI reports whether the model has CAMBI among its features: the v1 models do, a v0
	// one forced with the model flag does not, and its zero would read as "no banding".
	HasCAMBI bool
}

// UnmarshalJSON picks the score and the CAMBI feature among the pooled metrics of a report.
func (m *VMAFPooledMetrics) UnmarshalJSON(data []byte) error {
	var raw map[string]VMAFPooledMetric
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	keys := make([]string, 0, len(raw))
	for key := range raw {
		keys = append(keys, key)
	}
	vmafKey, cambiKey := findMetricKeys(keys)
	if vmafKey == "" {
		return fmt.Errorf("no %q metric in pooled metrics", vmafMetricKey)
	}
	m.VMAF = raw[vmafKey]
	m.CAMBI = raw[cambiKey] // zero when absent
	m.HasCAMBI = cambiKey != ""
	return nil
}

// VMAFPooledMetric holds aggregate values (min, max, mean, harmonic mean) for a single metric.
type VMAFPooledMetric struct {
	Min          float64 `json:"min"`
	Max          float64 `json:"max"`
	Mean         float64 `json:"mean"`
	HarmonicMean float64 `json:"harmonic_mean"`
}

// VMAFStats is a user-friendly summary of VMAF results, including computed percentiles and
// the banding diagnostic (see VMAFFrameMetrics).
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
	CAMBIMean    float64 `json:"cambi_mean"`
	CAMBIMax     float64 `json:"cambi_max"`
	HasCAMBI     bool    `json:"-"` // see VMAFPooledMetrics
}

// String renders the VMAF statistics as an aligned plain-text table, followed by the banding
// diagnostic when the model has one. CAMBI is already part of the score: it is shown for
// diagnosis, never gated. It rates the banding of the encoded picture, the source's included
// (see core.VMAFStats).
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
	if vs.HasCAMBI {
		fmt.Fprintf(&tableBuffer, "\nBanding (CAMBI, 0 = none, ~5 = slightly annoying, 17 = ceiling): mean %s, max %s\n",
			strconv.FormatFloat(vs.CAMBIMean, 'f', -1, float64Precision),
			strconv.FormatFloat(vs.CAMBIMax, 'f', -1, float64Precision),
		)
	}
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
