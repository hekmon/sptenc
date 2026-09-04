package ffmpeg

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"

	"github.com/hekmon/processpriority"
)

const (
	// HEVCQPMin is the minimum Quantization Parameter (QP) value for HEVC encoders.
	HEVCQPMin = 0
	// HEVCQPMax is the maximum Quantization Parameter (QP) value for HEVC encoders.
	HEVCQPMax = 51
	// HEVCLossless is a special QP value to switch from QP encoding to lossless HEVC encoding mode.
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

	libx265AQMode = 3 // AQ enabled with auto-variance and bias to dark scenes - https://x265.readthedocs.io/en/latest/cli.html#cmdoption-aq-mode
)

// HEVCLibx265EncodeConfig holds the configuration for HEVC encoding using libx265.
type HEVCLibx265EncodeConfig struct {
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

// HEVCLibx265Encode encodes a video file to HEVC/H.265 using the libx265 encoder via FFmpeg.
// This is a CPU-based software encoder; it is slower than GPU encoding (e.g. NVENC or VA-API)
// but produces significantly smaller files, making it recommended for final encoding.
func HEVCLibx265Encode(ctx context.Context, config HEVCLibx265EncodeConfig) (err error) {
	// Validate inputs
	if config.Input == "" {
		return errors.New("input path cannot be empty")
	}
	if config.Output == "" {
		return errors.New("output file path cannot be empty")
	}
	if (config.Quantization < HEVCQPMin || config.Quantization > HEVCQPMax) && config.Quantization != HEVCLossless {
		return fmt.Errorf("quantization must be %d-%d or %d for lossless, got %d", HEVCQPMin, HEVCQPMax, HEVCLossless, config.Quantization)
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
		err = fmt.Errorf("error setting up stdout pipe: %w", err)
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
)

/*
 * HEVC VA API
 * ffmpeg -h encoder=hevc_vaapi
 */

const (
	// HEVCEncoderVAAPI is the FFmpeg encoder name for VA-API HEVC hardware encoding.
	HEVCEncoderVAAPI Encoder = "hevc_vaapi"
)
