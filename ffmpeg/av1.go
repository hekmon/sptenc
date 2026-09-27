package ffmpeg

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"time"

	"github.com/hekmon/processpriority"
)

const (
	// AV1Lossless is the QP value requesting lossless mode, mirroring HEVCLossless for caller-API
	// uniformity. No AV1 encoder in this package has verified bit-exact lossless (libaom -crf 0
	// only produces lossless keyframes): all functions reject it with an explicit error.
	AV1Lossless = -1
)

/*
 * Libaom (AV1)
 * ffmpeg -h encoder=libaom-av1
 * https://ffmpeg.org/ffmpeg-codecs.html#libaom_002dav1
 */

const (
	// AV1EncoderLibaom is the FFmpeg encoder name for libaom-av1 (AV1 software encoding).
	AV1EncoderLibaom Encoder = "libaom-av1"

	// AV1LibaomQPMin is the minimum quality value for libaom-av1, applied via -crf.
	AV1LibaomQPMin = 0
	// AV1LibaomQPMax is the maximum quality value for libaom-av1, applied via -crf.
	AV1LibaomQPMax = 63

	// AV1LibaomCPUUsedMin is the slowest requestable speed setting (best compression).
	// The encoder accepts 0, but 0 is reserved as the "unset" zero value (auto-defaulted
	// to AV1LibaomCPUUsedDefault), so 1 is the lowest actually requestable.
	AV1LibaomCPUUsedMin = 1
	// AV1LibaomCPUUsedMax is the fastest and least efficient speed setting.
	AV1LibaomCPUUsedMax = 8
	// AV1LibaomCPUUsedDefault corresponds to the libx265 "slow" preset, recommended for final encoding.
	AV1LibaomCPUUsedDefault = 2
)

// AV1LibaomEncodeQPConfig holds the configuration for AV1 encoding using libaom.
type AV1LibaomEncodeQPConfig struct {
	// Input
	Input string
	// Output
	CPUUsed      int    // libaom speed/quality tradeoff: 1 (slowest/best) to 8 (fastest/worst). Not thread count.
	Quantization int    // AV1Lossless is rejected: no verified bit-exact AV1 lossless encoder
	Output       string // .mkv (Matroska) file recommended: the most permissive container for stream copy
	// Reporting
	Debug             func(msg string)
	RuntimeError      func(err error) // stderr error output, stats output will be send in FFMPEGStatsReport
	FFMPEGStatsReport func(stats ProgressStats)
}

// AV1LibaomEncodeQP encodes a video file to AV1 using the libaom encoder via FFmpeg.
// This is a CPU-based software encoder; it is slower than GPU encoding (e.g. NVENC or VA-API)
// but produces significantly smaller files, making it recommended for final encoding.
func AV1LibaomEncodeQP(ctx context.Context, config AV1LibaomEncodeQPConfig) (err error) {
	// Validate inputs
	if config.Input == "" {
		return errors.New("input path cannot be empty")
	}
	if config.Output == "" {
		return errors.New("output file path cannot be empty")
	}
	if config.Quantization == AV1Lossless {
		return errors.New("libaom-av1 does not support lossless encoding: -crf 0 only produces lossless keyframes, use the HEVC functions for lossless intermediates")
	}
	if config.Quantization < AV1LibaomQPMin || config.Quantization > AV1LibaomQPMax {
		return fmt.Errorf("quantization must be %d-%d, got %d", AV1LibaomQPMin, AV1LibaomQPMax, config.Quantization)
	}
	if config.CPUUsed == 0 { // zero value: apply default
		config.CPUUsed = AV1LibaomCPUUsedDefault
	}
	if config.CPUUsed < AV1LibaomCPUUsedMin || config.CPUUsed > AV1LibaomCPUUsedMax {
		return fmt.Errorf("cpu-used must be %d-%d, got %d", AV1LibaomCPUUsedMin, AV1LibaomCPUUsedMax, config.CPUUsed)
	}
	// Probe input resolution to drive tiling decisions (required by policy: tiling
	// must be keyed on resolution, not core count).
	probeStats, err := GetStreamsInfos(ctx, GetStreamsInfosConfig{
		Path:         config.Input,
		Debug:        config.Debug,
		RuntimeError: config.RuntimeError,
	})
	if err != nil {
		return fmt.Errorf("failed to probe input resolution: %w", err)
	}
	videoStream := probeStats.VideoTrack()
	if videoStream == nil {
		return errors.New("no video stream found in input")
	}
	width := videoStream.Width
	height := videoStream.Height

	// Prepare
	args := []string{
		"-y", "-loglevel", "error", "-nostats", "-progress", "pipe:1", "-stats_period",
		strconv.FormatFloat(float64(StatsPeriod)/float64(time.Second), 'f', -1, 64),
		"-i", config.Input,
		"-map", "0",
		"-c", "copy",
		"-c:v", "libaom-av1",
		"-pix_fmt", "yuv420p10le", // force 10-bit output (AV1 main profile covers 8/10bit, unlike HEVC main10)
		"-cpu-used", strconv.Itoa(config.CPUUsed),
		"-threads", strconv.Itoa(NbThreadsToUse),
		"-row-mt", "1",
	}
	// Auto-tile based on probed resolution. Tiers:
	//   4K-class (>=3840x2160) -> 2x2 tiles
	//   1080p-class (>=1920x1080) -> 2x1 tiles
	//   below -> libaom default (1x1)
	// Axis convention: both width AND height must meet the tier threshold.
	// Guard: libaom EINVAL if frame < 128 px on an axis with tiles enabled.
	if width >= 128 && height >= 128 {
		switch {
		case width >= Width4K && height >= Height4K:
			args = append(args, "-tile-columns", "1", "-tile-rows", "1") // 2x2 tiles
		case width >= WidthFHD && height >= HeightFHD:
			args = append(args, "-tile-columns", "1", "-tile-rows", "0") // 2x1 tiles
		}
	}
	// Larger lookahead improves rate-distortion decisions for archival VOD.
	args = append(args, "-lag-in-frames", "32")
	// // quality
	// libaom has no -qp: -crf N -b:v 0 triggers aom's Q mode (--end-usage=q --cq-level=N),
	// a fixed-qindex mode with no bitrate target, deterministic for the per-scene quality loop.
	args = append(args,
		"-crf", strconv.Itoa(config.Quantization),
		"-b:v", "0",
	)
	// // end with output
	args = append(args,
		"-fps_mode", "passthrough", // preserve original timestamps, prevent frame drop/duplicate
		"-max_interleave_delta", "0",
		config.Output,
	)
	// Prepare command
	if config.Debug != nil {
		config.Debug(fmt.Sprintf("Encode with: %s", getPrintableCMDLine(FFMPEGBinary, args)))
	}
	cmd := exec.CommandContext(ctx, FFMPEGBinary, args...)
	// Prepare output handling
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
		standardProgress(stdoutPipe, config.FFMPEGStatsReport, config.RuntimeError)
		close(progressDone)
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
	return
}

/*
 * AV1 SVT-AV1
 * CPU encoder, recommended for production.
 * ffmpeg -h encoder=libsvtav1
 * Much faster than libaom at comparable compression: the iteration/production
 * counterpart to libaom's final-encode role.
 */

const (
	// AV1EncoderSVTAV1 is the FFmpeg encoder name for libsvtav1 (AV1 software encoding).
	AV1EncoderSVTAV1 Encoder = "libsvtav1"

	// AV1SVTAV1QPMin is the minimum QP value usable with libsvtav1.
	// 0 is the wrapper's "unset" sentinel: qp is only forwarded when > 0 (libsvtav1.c),
	// so 1 is the lowest actually requestable.
	AV1SVTAV1QPMin = 1
	// AV1SVTAV1QPMax is the maximum QP value for libsvtav1.
	AV1SVTAV1QPMax = 63

	// AV1SVTAV1PresetMin is the slowest requestable preset (best compression).
	// 0 is reserved as the "unset" zero value (auto-defaulted to AV1SVTAV1PresetDefault).
	AV1SVTAV1PresetMin = 1
	// AV1SVTAV1PresetMax is the fastest preset.
	AV1SVTAV1PresetMax = 13
	// AV1SVTAV1PresetDefault is the preset sptenc encodes with: a compromise between compression
	// and the time of a search that encodes every segment several times, as libx265's "slow" is.
	AV1SVTAV1PresetDefault = 3
)

// AV1SVTAV1EncodeQPConfig holds the configuration for AV1 encoding using SVT-AV1.
type AV1SVTAV1EncodeQPConfig struct {
	// Input
	Input string
	// Output
	Preset       int    // 1 (slowest, best compression) to 13 (fastest), if unset it will be set automatically to AV1SVTAV1PresetDefault
	Quantization int    // AV1Lossless is rejected: SVT-AV1 has no lossless mode
	Output       string // .mkv (Matroska) file recommended: the most permissive container for stream copy
	// Reporting
	Debug             func(msg string)
	RuntimeError      func(err error) // stderr error output, stats output will be send in FFMPEGStatsReport
	FFMPEGStatsReport func(stats ProgressStats)
}

// AV1SVTAV1EncodeQP encodes a video file to AV1 using the SVT-AV1 encoder via FFmpeg.
// This is a CPU-based software encoder, dramatically faster than libaom at comparable
// compression: recommended for production and per-scene quality loop iteration.
func AV1SVTAV1EncodeQP(ctx context.Context, config AV1SVTAV1EncodeQPConfig) (err error) {
	// Validate inputs
	if config.Input == "" {
		return errors.New("input path cannot be empty")
	}
	if config.Output == "" {
		return errors.New("output file path cannot be empty")
	}
	if config.Quantization == AV1Lossless {
		return errors.New("libsvtav1 does not support lossless encoding, use the HEVC functions for lossless intermediates")
	}
	if config.Quantization < AV1SVTAV1QPMin || config.Quantization > AV1SVTAV1QPMax {
		return fmt.Errorf("quantization must be %d-%d, got %d", AV1SVTAV1QPMin, AV1SVTAV1QPMax, config.Quantization)
	}
	if config.Preset == 0 { // zero value: apply default
		config.Preset = AV1SVTAV1PresetDefault
	}
	if config.Preset < AV1SVTAV1PresetMin || config.Preset > AV1SVTAV1PresetMax {
		return fmt.Errorf("preset must be %d-%d, got %d", AV1SVTAV1PresetMin, AV1SVTAV1PresetMax, config.Preset)
	}
	// Prepare
	args := []string{
		"-y", "-loglevel", "error", "-nostats", "-progress", "pipe:1", "-stats_period",
		strconv.FormatFloat(float64(StatsPeriod)/float64(time.Second), 'f', -1, 64),
		"-i", config.Input,
		"-map", "0",
		"-c", "copy",
		"-c:v", "libsvtav1",
		"-pix_fmt", "yuv420p10le", // force 10-bit output (wrapper only supports yuv420p/yuv420p10le anyway)
		"-preset", strconv.Itoa(config.Preset),
	}
	// // quality
	// -qp in the FFmpeg wrapper is true CQP: rate_control_mode 0 with aq_mode explicitly
	// disabled (libsvtav1.c). The QP asked is the QP applied, deterministic for the
	// per-scene quality loop. (-crf would keep SVT-AV1's default aq-mode 2, "deltaq pred
	// efficiency"; the variance based AQ is mode 1.)
	args = append(args,
		"-qp", strconv.Itoa(config.Quantization),
	)
	// // end with output
	args = append(args,
		"-fps_mode", "passthrough", // preserve original timestamps, prevent frame drop/duplicate
		"-max_interleave_delta", "0",
		config.Output,
	)
	// Prepare command
	if config.Debug != nil {
		config.Debug(fmt.Sprintf("Encode with: %s", getPrintableCMDLine(FFMPEGBinary, args)))
	}
	cmd := exec.CommandContext(ctx, FFMPEGBinary, args...)
	// The SVT-AV1 library writes its own logs straight to stderr, ignoring ffmpeg's -loglevel:
	// its information banner (about 20 lines) would be reported as errors for every single
	// encode. Unlike libx265 (log-level within -x265-params) there is no encoder parameter for
	// this, only an environment variable read by the library itself.
	// SVT_LOG levels: 0 fatal, 1 error, 2 warning, 3 info (default), 4 debug.
	cmd.Env = append(os.Environ(), "SVT_LOG=1")
	// Prepare output handling
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
		standardProgress(stdoutPipe, config.FFMPEGStatsReport, config.RuntimeError)
		close(progressDone)
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
	return
}

/*
 * AV1 NVEnc
 * ffmpeg -h encoder=av1_nvenc
 * Requires an Ada Lovelace (RTX 40 series) or newer GPU: AV1 has no NVENC before that.
 * Presets, AQ/lookahead constants and device handling are shared with HEVC NVENC (see encoders.go).
 */

const (
	// AV1EncoderNVEnc is the FFmpeg encoder name for NVIDIA NVENC AV1 hardware encoding.
	AV1EncoderNVEnc Encoder = "av1_nvenc"

	// AV1NVEncQPMin is the minimum Quantization Parameter (QP) value for av1_nvenc.
	// 0 is requestable: nvenc uses -1 as its "unset" sentinel, unlike VAAPI/D3D12VA.
	AV1NVEncQPMin = 0
	// AV1NVEncQPMax is the maximum Quantization Parameter (QP) value for av1_nvenc (AV1 qindex range).
	AV1NVEncQPMax = 255
)

// AV1NVEncEncodeQPConfig holds the configuration for AV1 encoding using NVIDIA NVENC.
type AV1NVEncEncodeQPConfig struct {
	// Input
	Input  string
	Device int // NVIDIA GPU index, see CUDADefaultDevice
	// Output
	Preset       NVEncEncodingPreset // shared with HEVC NVENC, if unset it will be set automatically to NVEncPresetP4
	Quantization int                 // AV1Lossless is rejected: NVENC AV1 lossless bit-exactness unverified
	Output       string              // .mkv (Matroska) file recommended: the most permissive container for stream copy
	// Reporting
	Debug             func(msg string)
	RuntimeError      func(err error) // stderr error output, stats output will be send in FFMPEGStatsReport
	FFMPEGStatsReport func(stats ProgressStats)
}

// AV1NVEncEncodeQP encodes a video file to AV1 using the NVIDIA NVENC hardware encoder via FFmpeg.
func AV1NVEncEncodeQP(ctx context.Context, config AV1NVEncEncodeQPConfig) (err error) {
	// Validate inputs
	if config.Input == "" {
		return errors.New("input path cannot be empty")
	}
	if config.Output == "" {
		return errors.New("output file path cannot be empty")
	}
	if config.Quantization == AV1Lossless {
		return errors.New("av1_nvenc does not support lossless encoding: NVENC AV1 lossless bit-exactness is unverified, use the HEVC functions for lossless intermediates")
	}
	if config.Quantization < AV1NVEncQPMin || config.Quantization > AV1NVEncQPMax {
		return fmt.Errorf("quantization must be %d-%d, got %d", AV1NVEncQPMin, AV1NVEncQPMax, config.Quantization)
	}
	// Preset validation and default
	preset := config.Preset
	switch preset {
	case NVEncPresetP1, NVEncPresetP2, NVEncPresetP3, NVEncPresetP4,
		NVEncPresetP5, NVEncPresetP6, NVEncPresetP7:
	// valid, continue
	case "":
		preset = NVEncPresetP4
	default:
		return fmt.Errorf("invalid preset: %q", preset)
	}
	// Auto-detect NVDEC compatibility from input codec
	nvdec := false
	if stats, err := GetStreamsInfos(ctx, GetStreamsInfosConfig{Path: config.Input}); err == nil {
		if video := stats.VideoTrack(); video != nil {
			nvdec = IsNVDecCompatible(video.CodecName)
		}
	} else if config.RuntimeError != nil {
		config.RuntimeError(fmt.Errorf("failed to probe input for NVDEC auto-detection: %w, falling back to software decode", err))
	}
	// Prepare
	args := []string{
		"-y",
		"-loglevel", "error", "-nostats", "-progress", "pipe:1", "-stats_period",
		strconv.FormatFloat(float64(StatsPeriod)/float64(time.Second), 'f', -1, 64),
		"-init_hw_device", fmt.Sprintf("cuda=nv:%d", config.Device),
		"-filter_hw_device", "nv",
	}
	//// nvdec ?
	if nvdec {
		args = append(args,
			"-hwaccel", "cuda",
			"-hwaccel_output_format", "cuda",
			"-hwaccel_device", "nv",
		)
	}
	args = append(args, "-i", config.Input)
	if nvdec {
		args = append(args, "-vf", "scale_cuda=format=p010le") // convert to 10bits if necessary while staying on CUDA device between nvdec and nvenc
	} else {
		args = append(args, "-vf", "hwupload,scale_cuda=format=p010le") // perform the 10bits conversion in CUDA for performance (as we are going to use nvenc)
	}
	//// flux selection
	args = append(args,
		"-map", "0",
		"-c", "copy",
	)
	//// nvenc
	args = append(args,
		"-c:v", "av1_nvenc",
		// no -profile: AV1 has a single profile (main) covering 8/10bit
		"-preset", string(preset),
	)
	//// quality
	args = append(args,
		"-tune", "hq",
		"-rc", "constqp",
		"-qp", strconv.Itoa(config.Quantization),
		// Same as HEVC NVENC: honored under constqp, identical for every tested QP (see hevc.go).
		"-spatial-aq", strconv.Itoa(nvEncSpatialAQ),
		"-temporal-aq", strconv.Itoa(nvEncTemporalAQ),
	)
	args = append(args, "-rc-lookahead", strconv.Itoa(nvEncMaxLookahead))
	// // end with output
	args = append(args,
		"-fps_mode", "passthrough", // preserve original timestamps, prevent frame drop/duplicate
		"-max_interleave_delta", "0",
		config.Output,
	)
	// Prepare command
	if config.Debug != nil {
		config.Debug(fmt.Sprintf("Encode with: %s", getPrintableCMDLine(FFMPEGBinary, args)))
	}
	cmd := exec.CommandContext(ctx, FFMPEGBinary, args...)
	// Prepare output handling
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
		standardProgress(stdoutPipe, config.FFMPEGStatsReport, config.RuntimeError)
		close(progressDone)
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
	return
}

/*
 * AV1 VA-API
 * Linux only, vendor-agnostic (Intel/AMD).
 * ffmpeg -h encoder=av1_vaapi
 * Device handling is shared with HEVC VA-API (see hevc.go).
 * Requires a GPU with an AV1 encoder: Intel DG2 (Arc Alchemist), Meteor Lake and newer, AMD VCN 4.0
 * (RDNA 3) and newer. Intel Tiger Lake to Raptor Lake (Alder Lake-N included), DG1 and AMD VCN 3
 * (RDNA 2) can not encode AV1. Where they decode it, the driver lists VAProfileAV1Profile0 without
 * an encode entrypoint, and ffmpeg fails to open the encoder with "No usable encoding entrypoint
 * found for profile VAProfileAV1Profile0".
 * https://github.com/intel/media-driver#decodingencoding-features
 * https://github.com/GPUOpen-LibrariesAndSDKs/AMF/wiki/GPU%20and%20APU%20HW%20Features%20and%20Support
 */

const (
	// AV1EncoderVAAPI is the FFmpeg encoder name for VA-API AV1 hardware encoding.
	AV1EncoderVAAPI Encoder = "av1_vaapi"

	// AV1VAAPIQPMin is the minimum quality value usable with av1_vaapi.
	// Applied via -global_quality (no -qp exists for av1_vaapi): 0 is silently
	// replaced by the encoder default (25) via the default_quality fallback,
	// so 1 is the lowest value actually requestable.
	AV1VAAPIQPMin = 1
	// AV1VAAPIQPMax is the maximum quality value for av1_vaapi (AV1_MAX_QUANT, clipped by the encoder).
	AV1VAAPIQPMax = 255
)

// AV1VAAPIEncodeQPConfig holds the configuration for AV1 encoding using VA-API.
type AV1VAAPIEncodeQPConfig struct {
	// Input
	Input  string
	Device string // DRM render node, if unset it will be set automatically to VAAPIDefaultDevice
	// Output
	Quantization int    // AV1Lossless is rejected: av1_vaapi has no lossless mode
	Output       string // .mkv (Matroska) file recommended: the most permissive container for stream copy
	// Reporting
	Debug             func(msg string)
	RuntimeError      func(err error) // stderr error output, stats output will be send in FFMPEGStatsReport
	FFMPEGStatsReport func(stats ProgressStats)
}

// AV1VAAPIEncodeQP encodes a video file to AV1 using the VA-API hardware encoder via FFmpeg.
// WARNING: currently untested
func AV1VAAPIEncodeQP(ctx context.Context, config AV1VAAPIEncodeQPConfig) (err error) {
	// Validate inputs
	if config.Input == "" {
		return errors.New("input path cannot be empty")
	}
	if config.Output == "" {
		return errors.New("output file path cannot be empty")
	}
	if config.Quantization == AV1Lossless {
		return errors.New("av1_vaapi does not support lossless encoding, use the HEVC functions for lossless intermediates")
	}
	if config.Quantization < AV1VAAPIQPMin || config.Quantization > AV1VAAPIQPMax {
		return fmt.Errorf("quantization must be %d-%d, got %d", AV1VAAPIQPMin, AV1VAAPIQPMax, config.Quantization)
	}
	if config.Device == "" {
		config.Device = VAAPIDefaultDevice
	}
	// Auto-detect VA-API decode compatibility from input codec
	vaapidec := false
	if stats, err := GetStreamsInfos(ctx, GetStreamsInfosConfig{Path: config.Input}); err == nil {
		if video := stats.VideoTrack(); video != nil {
			vaapidec = IsVAAPIDecCompatible(video.CodecName)
		}
	} else if config.RuntimeError != nil {
		config.RuntimeError(fmt.Errorf("failed to probe input for VA-API decode auto-detection: %w, falling back to software decode", err))
	}
	// Prepare
	args := []string{
		"-y",
		"-loglevel", "error", "-nostats", "-progress", "pipe:1", "-stats_period",
		strconv.FormatFloat(float64(StatsPeriod)/float64(time.Second), 'f', -1, 64),
	}
	// // vaapi decoding ?
	if vaapidec {
		args = append(args,
			"-hwaccel", "vaapi",
			"-hwaccel_output_format", "vaapi",
			"-hwaccel_device", config.Device,
		)
	} else {
		args = append(args,
			"-init_hw_device", "vaapi=va:"+config.Device,
			"-filter_hw_device", "va",
		)
	}
	args = append(args, "-i", config.Input)
	if vaapidec {
		args = append(args, "-vf", "scale_vaapi=format=p010le") // convert to 10bits if necessary while staying on the GPU between decode and encode
	} else {
		args = append(args, "-vf", "hwupload,scale_vaapi=format=p010le") // perform the 10bits conversion on GPU for performance (as we are going to use vaapi encode)
	}
	// // flux selection
	args = append(args,
		"-map", "0",
		"-c", "copy",
	)
	// // vaapi
	args = append(args,
		"-c:v", "av1_vaapi",
		"-profile:v", "main", // only profile AV1 has; explicit for clarity
	)
	// // quality
	args = append(args,
		"-rc_mode", "CQP",
		// av1_vaapi has no -qp (the 2023 patch adding it was never merged): in CQP mode the
		// qindex comes from rc_quality, fed by -global_quality and clipped to 0-255 by
		// vaapi_encode_av1_configure. Same "QP asked is QP applied" determinism, different knob.
		"-global_quality", strconv.Itoa(config.Quantization),
	)
	// // end with output
	args = append(args,
		"-fps_mode", "passthrough", // preserve original timestamps, prevent frame drop/duplicate
		"-max_interleave_delta", "0",
		config.Output,
	)
	// Prepare command
	if config.Debug != nil {
		config.Debug(fmt.Sprintf("Encode with: %s", getPrintableCMDLine(FFMPEGBinary, args)))
	}
	cmd := exec.CommandContext(ctx, FFMPEGBinary, args...)
	// Prepare output handling
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
		standardProgress(stdoutPipe, config.FFMPEGStatsReport, config.RuntimeError)
		close(progressDone)
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
	return
}

/*
 * AV1 D3D12VA
 * Does not exist: FFmpeg's D3D12VA encoders cover HEVC (7.0) and H.264 (8.1) only.
 * Windows AV1 hardware encoding alternatives: av1_qsv (Intel), av1_amf (AMD).
 */
