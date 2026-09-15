package ffmpeg

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/hekmon/processpriority"
)

const (
	// SegmentOutputFormat is the filename pattern used when splitting a video into segments.
	SegmentOutputFormat = "seg_%d.mkv"
)

// SegmentConfig holds the configuration for Segment.
type SegmentConfig struct {
	// Input
	Input string
	// Output
	ScenesMarkers []time.Duration // if empty, will cut at each I frame
	OutputDir     string
	// Reporting
	Debug             func(msg string)
	RuntimeError      func(err error) // non fatal errors
	FFMPEGStatsReport func(stats ProgressStats)
}

// Segment splits a video into segments at the provided scene markers using ffmpeg's segment muxer.
func Segment(ctx context.Context, config SegmentConfig) (err error) {
	// Validate inputs
	if config.Input == "" {
		return errors.New("input path cannot be empty")
	}
	if config.OutputDir == "" {
		return errors.New("output directory cannot be empty")
	}
	if len(config.ScenesMarkers) == 0 {
		return errors.New("no scene markers")
	}
	// Prepare command
	args := []string{
		"-y", "-loglevel", "error", "-nostats", "-progress", "pipe:1", "-stats_period",
		strconv.FormatFloat(float64(StatsPeriod)/float64(time.Second), 'f', -1, 64),
		"-i", config.Input,
		"-map", "0:v:0",
		"-c:v", "copy",
		"-f", "segment",
		"-segment_times", formatSliceMarkers(config.ScenesMarkers),
		"-reset_timestamps", "1",
		filepath.Join(config.OutputDir, SegmentOutputFormat),
	}
	if config.Debug != nil {
		config.Debug(fmt.Sprintf("Scene splitting with: %s", getPrintableCMDLine(FFMPEGBinary, args)))
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

func formatSliceMarkers(markers []time.Duration) (formatted string) {
	strForm := make([]string, len(markers))
	for i := range markers {
		strForm[i] = strconv.FormatFloat(markers[i].Seconds(), 'f', -1, float64Precision)
	}
	return strings.Join(strForm, ",")
}
