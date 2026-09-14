package ffmpeg

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"

	"github.com/hekmon/processpriority"
)

// FFMEGTags is a list of FFMPEG tags (cli flag + value)
type FFMEGTags []string

// FLAC encoding constants
const (
	FLACExactRiceParams = 1  // Use exact Rice parameters for lossless
	FLACCompressionMin  = 0  // Minimum compression level
	FLACCompressionMax  = 12 // Maximum compression level
)

// RemuxSwapVideoConfig holds the parameters for a video swap remux operation.
type RemuxSwapVideoConfig struct {
	// Input
	OriginalFile string
	NewVideoFile string
	// Output
	// OutputFilePath must use the .mkv extension. Matroska is required because
	// it is the most permissive container for stream copy and it carries HDR
	// container metadata (Colour elements, MaxCLL, MaxFALL) that other containers
	// lack or encode differently.
	OutputFilePath string
	EncodeToFLAC   bool
	Tags           FFMEGTags // tags to add, can be nil
	// Reporting
	Debug             func(msg string)
	RuntimeError      func(err error) // non fatal errors
	FFMPEGStatsReport func(stats ProgressStats)
}

// RemuxSwapVideo replaces the video stream of OriginalFile with the video from
// NewVideoFile while copying all other streams (audio, subtitles, data,
// attachments). If EncodeToFLAC is true, audio is re-encoded to FLAC with
// maximum compression; otherwise it is copied as-is.
func RemuxSwapVideo(ctx context.Context, config RemuxSwapVideoConfig) (err error) {
	// Validate inputs
	if config.OriginalFile == "" {
		return errors.New("original file path cannot be empty")
	}
	if config.NewVideoFile == "" {
		return errors.New("new video file path cannot be empty")
	}
	if config.OutputFilePath == "" {
		return errors.New("output file path cannot be empty")
	}
	// Matroska is required because:
	// - It is the most permissive container for stream copy (handles arbitrary codecs, attachments, subtitles).
	// - It carries HDR container metadata (Colour elements, MaxCLL, MaxFALL) that other containers lack or encode differently.
	if filepath.Ext(config.OutputFilePath) != ".mkv" {
		return errors.New("output file path must have a .mkv extension")
	}
	// Extract container-level color metadata from the original file so we can
	// propagate it onto the copied video stream. With -c:v copy ffmpeg copies
	// encoded packets unchanged, but the output stream's codecpar (which the
	// muxer uses to write container-level color tags / Matroska Colour elements)
	// is inherited from the stream actually mapped. The mapped stream may have
	// left these codecpar fields empty or wrong even when its bitstream SEIs are
	// correct, because encoders write SEIs into the encoded frames while muxers
	// write container tags from codecpar, and the two are independent. By probing
	// the original file and passing output-side -color_* flags we override the
	// mapped stream's codecpar with the original's authoritative values. This does
	// not affect or create bitstream-level SEIs.
	var (
		colorRange     string
		colorSpace     string
		colorTransfer  string
		colorPrimaries string
	)
	if originalStats, err := GetStreamsInfos(ctx, GetStreamsInfosConfig{
		Path:         config.OriginalFile,
		Debug:        config.Debug,
		RuntimeError: config.RuntimeError,
	}); err == nil {
		if video := originalStats.VideoTrack(); video != nil {
			colorRange = video.ColorRange
			colorSpace = video.ColorSpace
			colorTransfer = video.ColorTransfer
			colorPrimaries = video.ColorPrimaries
		}
	} else if config.RuntimeError != nil {
		config.RuntimeError(fmt.Errorf("failed to probe original file for color metadata: %w", err))
	}
	// Prepare
	args := []string{
		"-y",
		"-loglevel", "error", "-stats",
	}
	// Inputs
	args = append(args,
		"-i", config.OriginalFile,
		"-i", config.NewVideoFile,
	)
	//// stream mapping: video from the encoded merge, everything else from the original
	args = append(args,
		"-map", "1:v", // video from encoded merge
		"-map", "0", // all streams from original...
		"-map", "-0:v", // ...except its video streams
	)
	//// codecs
	args = append(args,
		"-c:v", "copy",
		"-c:s", "copy",
		"-c:d", "copy",
		"-c:t", "copy",
	)
	if config.EncodeToFLAC {
		args = append(args, "-c:a", "flac", "-compression_level", strconv.Itoa(FLACCompressionMax), "-exact_rice_parameters", strconv.Itoa(FLACExactRiceParams))
	} else {
		args = append(args, "-c:a", "copy")
	}
	//// container-level options (muxer behavior, metadata, color signaling)
	if colorRange != "" {
		args = append(args, "-color_range:v:0", colorRange)
	}
	if colorSpace != "" {
		args = append(args, "-colorspace:v:0", colorSpace)
	}
	if colorTransfer != "" {
		args = append(args, "-color_trc:v:0", colorTransfer)
	}
	if colorPrimaries != "" {
		args = append(args, "-color_primaries:v:0", colorPrimaries)
	}
	args = append(args,
		"-max_interleave_delta", "0", // avoid '[matroska] Starting new cluster due to timestamp'
	)
	args = append(args, config.Tags...)
	//// end with output
	args = append(args, config.OutputFilePath)
	// Prepare command
	if config.Debug != nil {
		config.Debug(fmt.Sprintf("Remux with: %s", getPrintableCMDLine(FFMPEGBinary, args)))
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
