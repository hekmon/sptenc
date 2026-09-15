package ffmpeg

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"github.com/hekmon/processpriority"
)

const (
	// HEVCLossless is a special QP value accepted by HEVC encode configs to request lossless mode.
	// Rejected by encoders that do not support it (hevc_vaapi).
	HEVCLossless = -1
)

/*
 * Libx265
 * ffmpeg -h encoder=libx265
 * https://x265.readthedocs.io/en/latest/cli.html
 */

// Libx265EncodingPreset represents the encoding preset for the libx265 HEVC encoder.
// Each preset trades encoding speed for compression efficiency.
type Libx265EncodingPreset string

const (
	// HEVCEncoderLibx265 is the FFmpeg encoder name for libx265 (HEVC software encoding).
	HEVCEncoderLibx265 Encoder = "libx265"

	// Libx265PresetUltrafast is the fastest preset with the lowest compression efficiency.
	Libx265PresetUltrafast Libx265EncodingPreset = "ultrafast"
	// Libx265PresetSuperfast offers very fast encoding at the cost of file size.
	Libx265PresetSuperfast Libx265EncodingPreset = "superfast"
	// Libx265PresetVeryfast provides fast encoding with moderate compression.
	Libx265PresetVeryfast Libx265EncodingPreset = "veryfast"
	// Libx265PresetFaster is faster than the default with slightly reduced compression.
	Libx265PresetFaster Libx265EncodingPreset = "faster"
	// Libx265PresetFast balances speed and compression better than faster presets.
	Libx265PresetFast Libx265EncodingPreset = "fast"
	// Libx265PresetMedium is the default preset balancing speed and compression.
	Libx265PresetMedium Libx265EncodingPreset = "medium"
	// Libx265PresetSlow is recommended for final encode, offering better compression at the cost of speed.
	Libx265PresetSlow Libx265EncodingPreset = "slow"
	// Libx265PresetSlower provides significantly better compression than slow.
	Libx265PresetSlower Libx265EncodingPreset = "slower"
	// Libx265PresetVeryslow offers very high compression efficiency but is very slow.
	Libx265PresetVeryslow Libx265EncodingPreset = "veryslow"
	// Libx265PresetPlacebo is the slowest preset with marginal gains over veryslow.
	Libx265PresetPlacebo Libx265EncodingPreset = "placebo"

	// HEVCLibx265QPMin is the minimum Quantization Parameter (QP) value for HEVC encoders.
	HEVCLibx265QPMin = 0
	// HEVCLibx265QPMax is the maximum Quantization Parameter (QP) value for HEVC encoders.
	HEVCLibx265QPMax = 51

	libx265AQMode = 3 // AQ enabled with auto-variance and bias to dark scenes - https://x265.readthedocs.io/en/latest/cli.html#cmdoption-aq-mode
)

// HEVCLibx265EncodeQPConfig holds the configuration for HEVC encoding using libx265.
type HEVCLibx265EncodeQPConfig struct {
	// Input
	Input string
	// Output
	Preset       Libx265EncodingPreset // if unset it will be set automatically to Libx265PresetMedium
	Quantization int
	Output       string // .mkv (Matroska) file recommended: the most permissive container for stream copy
	// Reporting
	Debug             func(msg string)
	RuntimeError      func(err error) // stderr error output, stats output will be send in FFMPEGStatsReport
	FFMPEGStatsReport func(stats ProgressStats)
}

// HEVCLibx265EncodeQP encodes a video file to HEVC/H.265 using the libx265 encoder via FFmpeg.
// This is a CPU-based software encoder; it is slower than GPU encoding (e.g. NVENC or VA-API)
// but produces significantly smaller files, making it recommended for final encoding.
func HEVCLibx265EncodeQP(ctx context.Context, config HEVCLibx265EncodeQPConfig) (err error) {
	// Validate inputs
	if config.Input == "" {
		return errors.New("input path cannot be empty")
	}
	if config.Output == "" {
		return errors.New("output file path cannot be empty")
	}
	if (config.Quantization < HEVCLibx265QPMin || config.Quantization > HEVCLibx265QPMax) && config.Quantization != HEVCLossless {
		return fmt.Errorf("quantization must be %d-%d or %d for lossless, got %d",
			HEVCLibx265QPMin, HEVCLibx265QPMax, HEVCLossless, config.Quantization,
		)
	}
	switch config.Preset {
	case Libx265PresetUltrafast, Libx265PresetSuperfast, Libx265PresetVeryfast,
		Libx265PresetFaster, Libx265PresetFast, Libx265PresetMedium,
		Libx265PresetSlow, Libx265PresetSlower, Libx265PresetVeryslow, Libx265PresetPlacebo:
		// valid, continue
	case "":
		config.Preset = Libx265PresetMedium
	default:
		return fmt.Errorf("invalid preset: %q", config.Preset)
	}
	// Prepare
	args := []string{
		"-y",
		"-loglevel", "error", "-stats",
		"-i", config.Input,
		"-map", "0", // "-map", "-0:d?",
		"-c", "copy",
		"-c:v", "libx265",
		"-profile:v", "main10",
		"-pix_fmt", "yuv420p10le",
		"-preset", string(config.Preset),
	}
	//// quality
	if config.Quantization == HEVCLossless {
		args = append(args,
			"-x265-params", "lossless=1",
		)
	} else {
		args = append(args,
			"-qp", strconv.Itoa(config.Quantization),
			"-x265-params", fmt.Sprintf("aq-mode=%d", libx265AQMode),
		)
	}
	//// end with output
	args = append(args,
		"-max_interleave_delta", "0", // disable interleave buffering limit to avoid issues with large lossless packets
		config.Output,
	)
	// Prepare command
	if config.Debug != nil {
		config.Debug(fmt.Sprintf("Encode with: %s", getPrintableCMDLine(FFMPEGBinary, args)))
	}
	cmd := exec.CommandContext(ctx, FFMPEGBinary, args...)
	// Prepare output handling
	outputPipe, err := cmd.StderrPipe()
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
		standardProgress(outputPipe, config.FFMPEGStatsReport, config.RuntimeError)
		close(progressDone)
	}()
	if err = processpriority.Set(cmd.Process.Pid, ProcessPriority); err != nil && config.RuntimeError != nil {
		config.RuntimeError(fmt.Errorf("Failed to lower %s process priority: %w", FFMPEGBinary, err))
	}
	<-progressDone
	if err = cmd.Wait(); err != nil {
		err = fmt.Errorf("error during %s execution: %w\n%s", FFMPEGBinary, err, getPrintableCMDLine(FFMPEGBinary, args))
		return
	}
	return
}

/*
 * HEVC NVEnc
 * ffmpeg -h encoder=hevc_nvenc
 */

const (
	// HEVCEncoderNVEnc is the FFmpeg encoder name for NVIDIA NVENC HEVC hardware encoding.
	HEVCEncoderNVEnc Encoder = "hevc_nvenc"

	// HEVCNVEncQPMin is the minimum Quantization Parameter (QP) value for HEVC encoders.
	HEVCNVEncQPMin = 0
	// HEVCNVEncQPMax is the maximum Quantization Parameter (QP) value for HEVC encoders.
	HEVCNVEncQPMax = 51
)

// HEVCNVEncEncodeQPConfig holds the configuration for HEVC encoding using NVIDIA NVENC.
type HEVCNVEncEncodeQPConfig struct {
	// Input
	Input  string
	Device int // NVIDIA GPU index, see CUDADefaultDevice
	// Output
	Preset       NVEncEncodingPreset // if unset it will be set automatically to NVEncPresetP4
	Quantization int
	Output       string // .mkv (Matroska) file recommended: the most permissive container for stream copy
	// Reporting
	Debug             func(msg string)
	RuntimeError      func(err error) // stderr error output, stats output will be send in FFMPEGStatsReport
	FFMPEGStatsReport func(stats ProgressStats)
}

// HEVCNVEncEncodeQP encodes a video file to HEVC/H.265 using the NVIDIA NVENC hardware encoder via FFmpeg.
func HEVCNVEncEncodeQP(ctx context.Context, config HEVCNVEncEncodeQPConfig) (err error) {
	// Validate inputs
	if config.Input == "" {
		return errors.New("input path cannot be empty")
	}
	if config.Output == "" {
		return errors.New("output file path cannot be empty")
	}
	if (config.Quantization < HEVCNVEncQPMin || config.Quantization > HEVCNVEncQPMax) && config.Quantization != HEVCLossless {
		return fmt.Errorf("quantization must be %d-%d or %d for lossless, got %d",
			HEVCNVEncQPMin, HEVCNVEncQPMax, HEVCLossless, config.Quantization,
		)
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
		"-loglevel", "error", "-stats",
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
		"-c:v", "hevc_nvenc",
		"-profile:v", "main10",
		"-preset", string(preset),
	)
	//// quality
	if config.Quantization == HEVCLossless {
		args = append(args,
			"-tune", "lossless",
		)
	} else {
		args = append(args,
			"-tune", "hq",
			"-rc", "constqp",
			"-qp", strconv.Itoa(config.Quantization),
			// AQ flags are believed inert under constqp: NVIDIA documents constqp as
			// "the entire frame is encoded using QP specified in constQP", leaving no
			// room for per-block QP modulation (unlike x265, where AQ is an offset layer
			// on the base QP and stays active in CQP). FFmpeg forwards the flags without
			// warning. Kept as best-effort: no cost if ignored, gain if the driver honors them.
			"-spatial_aq", strconv.Itoa(nvEncSpatialAQ),
			"-temporal_aq", strconv.Itoa(nvEncTemporalAQ),
		)
	}
	args = append(args, "-rc-lookahead", strconv.Itoa(nvEncMaxLookahead))
	//// end with output
	args = append(args,
		"-max_interleave_delta", "0",
		config.Output,
	)
	// Prepare command
	if config.Debug != nil {
		config.Debug(fmt.Sprintf("Encode with: %s", getPrintableCMDLine(FFMPEGBinary, args)))
	}
	cmd := exec.CommandContext(ctx, FFMPEGBinary, args...)
	// Prepare output handling
	outputPipe, err := cmd.StderrPipe()
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
		standardProgress(outputPipe, config.FFMPEGStatsReport, config.RuntimeError)
		close(progressDone)
	}()
	if err = processpriority.Set(cmd.Process.Pid, ProcessPriority); err != nil && config.RuntimeError != nil {
		config.RuntimeError(fmt.Errorf("Failed to lower %s process priority: %w", FFMPEGBinary, err))
	}
	<-progressDone
	if err = cmd.Wait(); err != nil {
		err = fmt.Errorf("error during %s execution: %w\n%s", FFMPEGBinary, err, getPrintableCMDLine(FFMPEGBinary, args))
		return
	}
	return
}

/*
 * HEVC VA-API
 * Linux only, vendor-agnostic (Intel/AMD).
 * ffmpeg -h encoder=hevc_vaapi
 * https://ffmpeg.org/ffmpeg-codecs.html#VAAPI-encoders
 */

const (
	// HEVCEncoderVAAPI is the FFmpeg encoder name for VA-API HEVC hardware encoding.
	HEVCEncoderVAAPI Encoder = "hevc_vaapi"

	// HEVCVAAPIQPMin is the minimum QP value usable with hevc_vaapi.
	// 0 is FFmpeg's "unset" sentinel: explicit_qp is only set when qp > 0
	// (vaapi_encode_h265.c), so 0 cannot actually be requested.
	HEVCVAAPIQPMin = 1
	// HEVCVAAPIQPMax is the maximum Quantization Parameter (QP) value for HEVC encoders.
	HEVCVAAPIQPMax = 52

	// VAAPIDefaultDevice is the default DRM render node (first GPU)
	VAAPIDefaultDevice = "/dev/dri/renderD128"
)

// HEVCVAAPIEncodeQPConfig holds the configuration for HEVC encoding using VA-API.
type HEVCVAAPIEncodeQPConfig struct {
	// Input
	Input  string
	Device string // DRM render node, if unset it will be set automatically to VAAPIDefaultDevice
	// Output
	Quantization int    // HEVCLossless is not supported by hevc_vaapi and will return an error
	Output       string // .mkv (Matroska) file recommended: the most permissive container for stream copy
	// Reporting
	Debug             func(msg string)
	RuntimeError      func(err error) // stderr error output, stats output will be send in FFMPEGStatsReport
	FFMPEGStatsReport func(stats ProgressStats)
}

// HEVCVAAPIEncodeQP encodes a video file to HEVC/H.265 using the VA-API hardware encoder via FFmpeg.
// WARNING: currently untested
func HEVCVAAPIEncodeQP(ctx context.Context, config HEVCVAAPIEncodeQPConfig) (err error) {
	// Validate inputs
	if config.Input == "" {
		return errors.New("input path cannot be empty")
	}
	if config.Output == "" {
		return errors.New("output file path cannot be empty")
	}
	if config.Quantization == HEVCLossless {
		// VA-API HEVC has no lossless mode; qp 0 is not even guaranteed to be honored by drivers
		return errors.New("hevc_vaapi does not support lossless encoding")
	}
	if config.Quantization < HEVCVAAPIQPMin || config.Quantization > HEVCVAAPIQPMax {
		return fmt.Errorf("quantization must be %d-%d, got %d", HEVCVAAPIQPMin, HEVCVAAPIQPMax, config.Quantization)
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
		"-loglevel", "error", "-stats",
	}
	//// vaapi decoding ?
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
	//// flux selection
	args = append(args,
		"-map", "0",
		"-c", "copy",
	)
	//// vaapi
	args = append(args,
		"-c:v", "hevc_vaapi",
		"-profile:v", "main10",
	)
	//// quality
	args = append(args,
		"-rc_mode", "CQP", // constant QP: the QP asked is the QP applied, deterministic for the per-scene quality loop
		"-qp", strconv.Itoa(config.Quantization),
	)
	//// end with output
	args = append(args,
		"-max_interleave_delta", "0",
		config.Output,
	)
	// Prepare command
	if config.Debug != nil {
		config.Debug(fmt.Sprintf("Encode with: %s", getPrintableCMDLine(FFMPEGBinary, args)))
	}
	cmd := exec.CommandContext(ctx, FFMPEGBinary, args...)
	// Prepare output handling
	outputPipe, err := cmd.StderrPipe()
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
		standardProgress(outputPipe, config.FFMPEGStatsReport, config.RuntimeError)
		close(progressDone)
	}()
	if err = processpriority.Set(cmd.Process.Pid, ProcessPriority); err != nil && config.RuntimeError != nil {
		config.RuntimeError(fmt.Errorf("Failed to lower %s process priority: %w", FFMPEGBinary, err))
	}
	<-progressDone
	if err = cmd.Wait(); err != nil {
		err = fmt.Errorf("error during %s execution: %w\n%s", FFMPEGBinary, err, getPrintableCMDLine(FFMPEGBinary, args))
		return
	}
	return
}

/*
 * HEVC D3D12VA
 * ffmpeg -h encoder=hevc_d3d12va
 * Windows only, vendor-agnostic (Intel/AMD/NVIDIA).
 * No GPU scaling filter exists for D3D12 (scale_d3d12 does not exist in FFmpeg),
 * so 8-bit sources require a CPU round-trip (hwdownload,format=p010le,hwupload).
 */

const (
	// HEVCEncoderD3D12VA is the FFmpeg encoder name for D3D12VA HEVC hardware encoding.
	HEVCEncoderD3D12VA Encoder = "hevc_d3d12va"

	// HEVCD3D12VAQPMin is the minimum QP value usable with hevc_d3d12va.
	// 0 is FFmpeg's "unset" sentinel (same explicit_qp semantics as VAAPI),
	// so 1 is the lowest value actually requestable.
	HEVCD3D12VAQPMin = 1
	// HEVCD3D12VAQPMax is the maximum QP value accepted by hevc_d3d12va.
	HEVCD3D12VAQPMax = 52

	// D3D12VADefaultDevice is the default Direct3D 12 adapter index (first GPU).
	D3D12VADefaultDevice = 0
)

// HEVCD3D12VAEncodeQPConfig holds the configuration for HEVC encoding using D3D12VA.
type HEVCD3D12VAEncodeQPConfig struct {
	// Input
	Input  string
	Device int // Direct3D 12 adapter index, see D3D12VADefaultDevice
	// Output
	Quantization int    // HEVCLossless is not supported by hevc_d3d12va and will return an error
	Output       string // .mkv (Matroska) file recommended: the most permissive container for stream copy
	// Reporting
	Debug             func(msg string)
	RuntimeError      func(err error) // stderr error output, stats output will be send in FFMPEGStatsReport
	FFMPEGStatsReport func(stats ProgressStats)
}

// HEVCD3D12VAEncodeQP encodes a video file to HEVC/H.265 using the D3D12VA hardware encoder via FFmpeg.
// The encoder only accepts hardware frames ("Supported pixel formats: d3d12"), so hwupload is
// mandatory on the software decode path. 4:2:2 and 4:4:4 chroma subsampling are not supported.
// WARNING: currently untested
func HEVCD3D12VAEncodeQP(ctx context.Context, config HEVCD3D12VAEncodeQPConfig) (err error) {
	// Validate inputs
	if config.Input == "" {
		return errors.New("input path cannot be empty")
	}
	if config.Output == "" {
		return errors.New("output file path cannot be empty")
	}
	if config.Quantization == HEVCLossless {
		// D3D12VA HEVC has no lossless mode
		return errors.New("hevc_d3d12va does not support lossless encoding")
	}
	if config.Quantization < HEVCD3D12VAQPMin || config.Quantization > HEVCD3D12VAQPMax {
		return fmt.Errorf("quantization must be %d-%d, got %d", HEVCD3D12VAQPMin, HEVCD3D12VAQPMax, config.Quantization)
	}
	// Auto-detect D3D12VA decode compatibility and pixel format from input
	d3d12dec := false
	pixFmt := ""
	if stats, err := GetStreamsInfos(ctx, GetStreamsInfosConfig{Path: config.Input}); err == nil {
		if video := stats.VideoTrack(); video != nil {
			d3d12dec = IsD3D12DecCompatible(video.CodecName)
			pixFmt = video.PixFmt
		}
	} else if config.RuntimeError != nil {
		config.RuntimeError(fmt.Errorf("failed to probe input for D3D12VA decode auto-detection: %w, falling back to software decode", err))
	}
	// Reject chroma formats that D3D12VA does not support
	if strings.Contains(pixFmt, "422") || strings.Contains(pixFmt, "444") {
		return errors.New("d3d12va does not support 4:2:2 or 4:4:4 chroma subsampling")
	}
	is10Bit := strings.Contains(pixFmt, "p10") || strings.Contains(pixFmt, "10le")
	// Prepare
	args := []string{
		"-y",
		"-loglevel", "error", "-stats",
		// named device shared by decoder, filters and encoder: same GPU guaranteed
		"-init_hw_device", "d3d12va=d12:" + strconv.Itoa(config.Device),
		"-filter_hw_device", "d12",
	}
	//// d3d12 decoding ?
	if d3d12dec {
		args = append(args,
			"-hwaccel", "d3d12va",
			"-hwaccel_output_format", "d3d12",
			"-hwaccel_device", "d12", // reuse the named device
		)
	}
	args = append(args, "-i", config.Input)
	//// filter chain: stay on GPU or convert to p010le when necessary
	if d3d12dec {
		if !is10Bit {
			// 8-bit source: round-trip through CPU for 10-bit conversion (no scale_d3d12 filter exists)
			args = append(args, "-vf", "hwdownload,format=p010le,hwupload")
		}
		// 10-bit source: d3d12 frames pass straight through, no filter needed
	} else {
		if is10Bit {
			args = append(args, "-vf", "hwupload") // 10-bit software frames → d3d12
		} else {
			args = append(args, "-vf", "format=p010le,hwupload") // 8-bit → 10-bit conversion, then upload
		}
	}
	//// flux selection
	args = append(args,
		"-map", "0",
		"-c", "copy",
	)
	//// d3d12va
	args = append(args,
		"-c:v", "hevc_d3d12va",
		"-profile:v", "main10",
	)
	//// quality
	args = append(args,
		"-rc_mode", "CQP", // constant QP: the QP asked is the QP applied, deterministic for the per-scene quality loop
		"-qp", strconv.Itoa(config.Quantization),
	)
	//// end with output
	args = append(args,
		"-max_interleave_delta", "0",
		config.Output,
	)
	// Prepare command
	if config.Debug != nil {
		config.Debug(fmt.Sprintf("Encode with: %s", getPrintableCMDLine(FFMPEGBinary, args)))
	}
	cmd := exec.CommandContext(ctx, FFMPEGBinary, args...)
	// Prepare output handling
	outputPipe, err := cmd.StderrPipe()
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
		standardProgress(outputPipe, config.FFMPEGStatsReport, config.RuntimeError)
		close(progressDone)
	}()
	if err = processpriority.Set(cmd.Process.Pid, ProcessPriority); err != nil && config.RuntimeError != nil {
		config.RuntimeError(fmt.Errorf("Failed to lower %s process priority: %w", FFMPEGBinary, err))
	}
	<-progressDone
	if err = cmd.Wait(); err != nil {
		err = fmt.Errorf("error during %s execution: %w\n%s", FFMPEGBinary, err, getPrintableCMDLine(FFMPEGBinary, args))
		return
	}
	return
}

/*
 * HEVC VideoToolbox
 * macOS only, Apple Silicon recommended for constant quality (-q:v).
 * ffmpeg -h encoder=hevc_videotoolbox
 */

const (
	// HEVCEncoderVideoToolbox is the FFmpeg encoder name for VideoToolbox HEVC hardware encoding.
	HEVCEncoderVideoToolbox Encoder = "hevc_videotoolbox"

	// HEVCVideoToolboxQPMin is the minimum QP value usable with hevc_videotoolbox.
	// VideoToolbox uses -q:v with a 1-100 scale where 100 is best quality.
	// sptenc internally inverts this so lower QP = higher quality (1 -> 100).
	HEVCVideoToolboxQPMin = 1
	// HEVCVideoToolboxQPMax is the maximum QP value usable with hevc_videotoolbox.
	// Internal QP 100 maps to VideoToolbox quality 1 (worst).
	HEVCVideoToolboxQPMax = 100
)

// HEVCVideoToolboxEncodeQPConfig holds the configuration for HEVC encoding using VideoToolbox.
type HEVCVideoToolboxEncodeQPConfig struct {
	// Input
	Input string
	// Output
	Quantization int    // HEVCLossless is not supported by hevc_videotoolbox and will return an error
	Output       string // .mkv (Matroska) file recommended: the most permissive container for stream copy
	// Reporting
	Debug             func(msg string)
	RuntimeError      func(err error) // stderr error output, stats output will be send in FFMPEGStatsReport
	FFMPEGStatsReport func(stats ProgressStats)
}

// HEVCVideoToolboxEncodeQP encodes a video file to HEVC/H.265 using the VideoToolbox hardware encoder via FFmpeg.
func HEVCVideoToolboxEncodeQP(ctx context.Context, config HEVCVideoToolboxEncodeQPConfig) (err error) {
	// Validate inputs
	if config.Input == "" {
		return errors.New("input path cannot be empty")
	}
	if config.Output == "" {
		return errors.New("output file path cannot be empty")
	}
	if config.Quantization == HEVCLossless {
		// VideoToolbox HEVC has no lossless mode
		return errors.New("hevc_videotoolbox does not support lossless encoding")
	}
	if config.Quantization < HEVCVideoToolboxQPMin || config.Quantization > HEVCVideoToolboxQPMax {
		return fmt.Errorf("quantization must be %d-%d, got %d", HEVCVideoToolboxQPMin, HEVCVideoToolboxQPMax, config.Quantization)
	}
	// Invert QP to VideoToolbox quality scale: internal QP 1 (best) -> 100, QP 100 (worst) -> 1
	quality := 101 - config.Quantization
	// Auto-detect VideoToolbox decode compatibility and pixel format from input
	vtdec := false
	pixFmt := ""
	if stats, err := GetStreamsInfos(ctx, GetStreamsInfosConfig{Path: config.Input}); err == nil {
		if video := stats.VideoTrack(); video != nil {
			vtdec = IsVideoToolboxDecCompatible(video.CodecName)
			pixFmt = video.PixFmt
		}
	} else if config.RuntimeError != nil {
		config.RuntimeError(fmt.Errorf("failed to probe input for VideoToolbox decode auto-detection: %w, falling back to software decode", err))
	}
	is10Bit := strings.Contains(pixFmt, "p10") || strings.Contains(pixFmt, "10le")
	// Prepare
	args := []string{
		"-y",
		"-loglevel", "error", "-stats",
	}
	//// videotoolbox decoding ?
	if vtdec {
		args = append(args,
			"-hwaccel", "videotoolbox",
		)
	}
	args = append(args, "-i", config.Input)
	//// filter chain: convert to 10-bit when necessary (scale_vt does not support bit-depth conversion)
	if !is10Bit {
		if vtdec {
			// 8-bit hw decode: round-trip through CPU for 10-bit conversion
			args = append(args, "-vf", "hwdownload,format=p010le,hwupload")
		} else {
			// 8-bit software decode: convert to 10-bit software frames
			args = append(args, "-vf", "format=p010le")
		}
	}
	//// flux selection
	args = append(args,
		"-map", "0",
		"-c", "copy",
	)
	//// videotoolbox
	args = append(args,
		"-c:v", "hevc_videotoolbox",
		"-allow_sw", "1", // allow software fallback if hardware is unavailable
		"-profile:v", "main10",
	)
	//// quality
	args = append(args,
		"-q:v", strconv.Itoa(quality),
	)
	//// end with output
	args = append(args,
		"-max_interleave_delta", "0",
		config.Output,
	)
	// Prepare command
	if config.Debug != nil {
		config.Debug(fmt.Sprintf("Encode with: %s", getPrintableCMDLine(FFMPEGBinary, args)))
	}
	cmd := exec.CommandContext(ctx, FFMPEGBinary, args...)
	// Prepare output handling
	outputPipe, err := cmd.StderrPipe()
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
		standardProgress(outputPipe, config.FFMPEGStatsReport, config.RuntimeError)
		close(progressDone)
	}()
	if err = processpriority.Set(cmd.Process.Pid, ProcessPriority); err != nil && config.RuntimeError != nil {
		config.RuntimeError(fmt.Errorf("Failed to lower %s process priority: %w", FFMPEGBinary, err))
	}
	<-progressDone
	if err = cmd.Wait(); err != nil {
		err = fmt.Errorf("error during %s execution: %w\n%s", FFMPEGBinary, err, getPrintableCMDLine(FFMPEGBinary, args))
		return
	}
	return
}
