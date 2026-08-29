package ffmpeg

import (
	"context"
	"errors"
	"fmt"
	"os/exec"

	"github.com/hekmon/processpriority"
)

// FFV1VideoMasterConfig holds the configuration for FFV1VideoMaster().
type FFV1VideoMasterConfig struct {
	// Input
	InputFilePath string
	// Output
	OutputFilePath string
	// Reporting
	Debug        func(msg string)
	RuntimeError func(err error) // non fatal errors
	StatsReport  func(stats ProgressStats)
}

// FFV1VideoMaster encodes the input video file to FFV1 with intra frames and 10-bit YUV420P.
// Only the first video stream is kept; all other streams are dropped.
func FFV1VideoMaster(ctx context.Context, config FFV1VideoMasterConfig) (err error) {
	// Validate inputs
	if config.InputFilePath == "" {
		return errors.New("input path cannot be empty")
	}
	if config.OutputFilePath == "" {
		return errors.New("output file path cannot be empty")
	}
	// Prepare arguments
	args := []string{
		"-y",
		"-loglevel", "error", "-stats",
		"-i", config.InputFilePath,
		"-map", "0:v:0", // we only want the video stream
		"-c:v", "ffv1", // encoded as ffv1
		"-g", "1", // with every frame self contained
		"-pix_fmt", "yuv420p10le", // in 10bits output
		config.OutputFilePath,
	}
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
		standardProgress(outputPipe, config.StatsReport, config.RuntimeError)
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
