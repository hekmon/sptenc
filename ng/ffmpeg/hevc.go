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
	// HEVCLossless is a special QP value indicating lossless HEVC encoding mode.
	HEVCLossless = -1
)

/*
 * Libx265
 * ffmpeg -h encoder=libx265
 * https://x265.readthedocs.io/en/latest/cli.html
 */

type Libx265EncodingPreset string

const (
	// HEVCEncoderLibx265 is the FFmpeg encoder name for libx265 (HEVC software encoding).
	HEVCEncoderLibx265 Encoder = "libx265"

	Libx265PresetUltrafast Libx265EncodingPreset = "ultrafast"
	Libx265PresetSuperfast Libx265EncodingPreset = "superfast"
	Libx265PresetVeryfast  Libx265EncodingPreset = "veryfast"
	Libx265PresetFaster    Libx265EncodingPreset = "faster"
	Libx265PresetFast      Libx265EncodingPreset = "fast"
	Libx265PresetMedium    Libx265EncodingPreset = "medium" // default
	Libx265PresetSlow      Libx265EncodingPreset = "slow"   // recommended for final encode
	Libx265PresetSlower    Libx265EncodingPreset = "slower"
	Libx265PresetVeryslow  Libx265EncodingPreset = "veryslow"
	Libx265PresetPlacebo   Libx265EncodingPreset = "placebo"

	libx265HEVCAQ = 1 // Enable HEVC AQ - https://x265.readthedocs.io/en/latest/cli.html#cmdoption-hevc-aq
	libx265AQMode = 3 // AQ enabled with auto-variance and bias to dark scenes - https://x265.readthedocs.io/en/latest/cli.html#cmdoption-aq-mode
)

type HEVCLibx265EncodeConfig struct {
	// Input
	Input string
	// Output
	Preset       Libx265EncodingPreset // if unset it will be set automatically to slow for x265
	Quantization int
	Output       string
	// Reporting
	Debug             func(msg string)
	RuntimeError      func(err error) // non fatal errors
	FFMPEGStatsReport func(stats ProgressStats)
}

func HEVCLibx265Encode(ctx context.Context, config HEVCLibx265EncodeConfig) (err error) {
	// Validate inputs
	if config.Input == "" {
		return errors.New("input path cannot be empty")
	}
	if config.OutputFilePath == "" {
		return errors.New("output file path cannot be empty")
	}
	if config.Quantization < HEVCQPMin || (config.Quantization > HEVCQPMax && config.Quantization != HEVCLossless) {
		return fmt.Errorf("quantization must be %d-%d or %d for lossless, got %d", HEVCQPMin, HEVCQPMax, HEVCLossless, config.Quantization)
	}
	if config.Preset == "" {
		config.Preset = Libx265PresetSlow
	}
	// Prepare
	args := []string{
		"-y",
		"-loglevel", "error", "-stats",
		"-i", config.Input,
		// copy anything that is not a video
		"-c:a", "copy", // copy (not convert) if audio
		"-c:s", "copy", // copy (not convert) if subtitles
		"-c:d", "copy", // copy (not convert) if data
		"-c:t", "copy", // copy (not convert) if attachments
		// encode video
		"-c:v", "libx265",
		"-profile:v", "main10",
		"-preset", string(config.Preset), // preset defines lookahead
	}
	//// libx265 params
	if config.Quantization == HEVCLossless {
		args = append(args,
			"-x265-params", "lossless=1",
		)
	} else {
		args = append(args,
			"-qp", strconv.Itoa(config.Quantization),
			"-x265-params", fmt.Sprintf("hevc-aq=%d:aq-mode=%d", libx265HEVCAQ, libx265AQMode),
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
 */

const (
	// HEVCEncoderNVENC is the FFmpeg encoder name for NVIDIA NVENC HEVC hardware encoding.
	HEVCEncoderNVENC Encoder = "hevc_nvenc"
)

/*
 * HEVC VA API
 */

const (
	// HEVCEncoderVAAPI is the FFmpeg encoder name for VA-API HEVC hardware encoding.
	HEVCEncoderVAAPI Encoder = "hevc_vaapi"
)
