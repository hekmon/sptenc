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
	SegmentOutputFormat = "seg_%06d.mkv"
)

// SegmentConfig holds the configuration for Segment.
type SegmentConfig struct {
	// Input
	Input string
	// Output
	// ScenesFrames holds, for each scene but the first, the index (from 0) of its first frame:
	// see Scene for why cuts are frames and not times. Must be strictly increasing.
	// If empty, the whole input becomes the first (and only) segment.
	ScenesFrames []int
	OutputDir    string
	// Reporting
	Debug             func(msg string)
	RuntimeError      func(err error) // non fatal errors
	FFMPEGStatsReport func(stats ProgressStats)
}

// Segment splits a video into segments at the provided frames using ffmpeg's segment muxer.
// A cut can only happen on a keyframe (the muxer waits for the next one otherwise): cuts are
// exact on an all-intra master, where every frame is a keyframe.
func Segment(ctx context.Context, config SegmentConfig) (err error) {
	// Validate inputs
	if config.Input == "" {
		return errors.New("input path cannot be empty")
	}
	if config.OutputDir == "" {
		return errors.New("output directory cannot be empty")
	}
	previous := 0
	for _, frame := range config.ScenesFrames {
		// A frame not above the previous one would make ffmpeg produce less segments than
		// expected. It also catches scenes which did not get their frame index (0).
		if frame <= previous {
			return fmt.Errorf("scenes frames must be strictly increasing and above 0, got %d after %d", frame, previous)
		}
		previous = frame
	}
	// Prepare command
	args := []string{
		"-y", "-loglevel", "error", "-nostats", "-progress", "pipe:1", "-stats_period",
		strconv.FormatFloat(float64(StatsPeriod)/float64(time.Second), 'f', -1, 64),
		"-i", config.Input,
		"-map", "0:v:0",
		"-c:v", "copy",
	}
	if len(config.ScenesFrames) == 0 {
		// Single scene input (or every boundary has been filtered out): nothing to cut.
		// The segment muxer can not be used without cut points (it would fall back to
		// its default fixed segment time), so copy the video track as the only segment.
		args = append(args,
			filepath.Join(config.OutputDir, fmt.Sprintf(SegmentOutputFormat, 0)),
		)
	} else {
		args = append(args,
			"-f", "segment",
			"-segment_frames", formatScenesFrames(config.ScenesFrames),
			"-reset_timestamps", "1",
			filepath.Join(config.OutputDir, SegmentOutputFormat),
		)
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

func formatScenesFrames(frames []int) (formatted string) {
	strForm := make([]string, len(frames))
	for i := range frames {
		strForm[i] = strconv.Itoa(frames[i])
	}
	return strings.Join(strForm, ",")
}
