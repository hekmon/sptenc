package ffmpeg

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// StatsPeriod controls how often ffmpeg emits progress blocks.
// Callers may override it at init time (e.g. 200 * time.Millisecond).
var StatsPeriod = 100 * time.Millisecond

// ProgressStats holds the parsed progress information from an ffmpeg encode or extraction.
type ProgressStats struct {
	CurrentFrame int
	FPS          float64
	Dup          int // images extract only
	Drop         int // images extract only
	Time         time.Duration
	Bitrate      string // encode only
	Speed        float64
}

func standardProgress(ffmpegOutput io.ReadCloser, progress func(stats ProgressStats), runtimeError func(error)) {
	defer ffmpegOutput.Close()
	scanner := bufio.NewScanner(ffmpegOutput)
	var (
		stats        ProgressStats
		key, value   string
		ok           bool
		microSeconds int64
		err          error
	)
	for scanner.Scan() {
		if key, value, ok = strings.Cut(scanner.Text(), "="); !ok {
			runtimeError(fmt.Errorf("error reading ffmpeg progress: uncuttable progress line: %s", scanner.Text()))
			continue
		}
		switch key {
		case "frame":
			if stats.CurrentFrame, err = strconv.Atoi(value); err != nil {
				runtimeError(fmt.Errorf("error reading ffmpeg progress: error parsing frame: %s", value))
			}
		case "fps":
			if stats.FPS, err = strconv.ParseFloat(value, 64); err != nil {
				runtimeError(fmt.Errorf("error reading ffmpeg progress: error parsing fps: %s", value))
			}
		case "dup_frames":
			if stats.Dup, err = strconv.Atoi(value); err != nil {
				runtimeError(fmt.Errorf("error reading ffmpeg progress: error parsing dup_frames: %s", value))
			}
		case "drop_frames":
			if stats.Drop, err = strconv.Atoi(value); err != nil {
				runtimeError(fmt.Errorf("error reading ffmpeg progress: error parsing drop_frames: %s", value))
			}
		case "out_time_us":
			if microSeconds, err = strconv.ParseInt(value, 10, 64); err != nil {
				runtimeError(fmt.Errorf("error reading ffmpeg progress: error parsing out_time: %s", value))
			} else {
				stats.Time = time.Duration(microSeconds) * time.Microsecond
			}
		case "bitrate":
			stats.Bitrate = value
		case "speed":
			if stats.Speed, err = strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(value), "x"), 64); err != nil {
				runtimeError(fmt.Errorf("error reading ffmpeg progress: error parsing speed: %s", value))
			}
		case "progress":
			switch value {
			case "continue":
				if progress != nil {
					progress(stats)
				}
				stats = ProgressStats{}
			case "end":
				return
			default:
				runtimeError(fmt.Errorf("error reading ffmpeg progress: unexpected progress value: %s", value))
			}
		}
	}
	if err := scanner.Err(); err != nil && runtimeError != nil {
		runtimeError(fmt.Errorf("error reading ffmpeg progress: %w", err))
	} else {
		runtimeError(errors.New("error reading ffmpeg progress: unexpected end"))
	}
}

func stderrForwarder(stderr io.ReadCloser, runtimeError func(error)) {
	defer stderr.Close()
	if runtimeError == nil {
		return
	}
	scanner := bufio.NewScanner(stderr)
	for scanner.Scan() {
		runtimeError(errors.New(scanner.Text()))
	}
	if err := scanner.Err(); err != nil {
		runtimeError(fmt.Errorf("error reading ffmpeg stderr: %w", err))
	}
}
