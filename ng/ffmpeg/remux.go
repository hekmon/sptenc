package ffmpeg

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
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
	//// copy only the video stream from the concat script
	args = append(args,
		"-map", "1:v",
		"-c:v", "copy",
	)
	//// copy everything from original file except its video track
	args = append(args,
		"-map", "0", // map all streams from original file...
		"-map", "-0:v", // ...except video streams
		"-c:s", "copy", // copy (not convert) if subtitles
		"-c:d", "copy", // copy (not convert) if data
		"-c:t", "copy", // copy (not convert) if attachments
	)
	if config.EncodeToFLAC {
		args = append(args, "-c:a", "flac", "-compression_level", strconv.Itoa(FLACCompressionMax), "-exact_rice_parameters", strconv.Itoa(FLACExactRiceParams))
	} else {
		args = append(args, "-c:a", "copy")
	}
	//// avoid '[matroska] Starting new cluster due to timestamp' especially with lossless video stream and secondary low bitrate tracks,
	//// see https://ffmpeg.org/ffmpeg-formats.html#toc-Format-Options
	args = append(args,
		"-max_interleave_delta", "0",
	)
	//// add tags
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
