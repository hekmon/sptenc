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
	Bitrate      string
	TotalSize    int
	Time         time.Duration
	Dup          int
	Drop         int
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
			runtimeError(fmt.Errorf("error reading ffmpeg progress: uncuttable progress line: %q", scanner.Text()))
			continue
		}
		switch key {
		case "frame":
			if stats.CurrentFrame, err = strconv.Atoi(value); err != nil {
				runtimeError(fmt.Errorf("error reading ffmpeg progress: error parsing frame: %q", value))
			}
		case "fps":
			if stats.FPS, err = strconv.ParseFloat(value, 64); err != nil {
				runtimeError(fmt.Errorf("error reading ffmpeg progress: error parsing fps: %q", value))
			}
		case "bitrate":
			stats.Bitrate = value
		case "total_size":
			if value == "N/A" {
				continue
			}
			if stats.TotalSize, err = strconv.Atoi(value); err != nil {
				runtimeError(fmt.Errorf("error reading ffmpeg progress: error parsing total_size: %q", value))
			}
		case "out_time_ms":
			// old field misnamed for ms for microseconds, see out_time_us
			continue
		case "out_time_us":
			if value == "N/A" {
				continue
			}
			if microSeconds, err = strconv.ParseInt(value, 10, 64); err != nil {
				runtimeError(fmt.Errorf("error reading ffmpeg progress: error parsing out_time_us: %q", value))
			} else {
				stats.Time = time.Duration(microSeconds) * time.Microsecond
			}
		case "out_time":
			// human representation of time, we don't care we parse out_time_us
			continue
		case "dup_frames":
			if stats.Dup, err = strconv.Atoi(value); err != nil {
				runtimeError(fmt.Errorf("error reading ffmpeg progress: error parsing dup_frames: %q", value))
			}
		case "drop_frames":
			if stats.Drop, err = strconv.Atoi(value); err != nil {
				runtimeError(fmt.Errorf("error reading ffmpeg progress: error parsing drop_frames: %q", value))
			}
		case "speed":
			if value == "N/A" {
				continue
			}
			if stats.Speed, err = strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(value), "x"), 64); err != nil {
				runtimeError(fmt.Errorf("error reading ffmpeg progress: error parsing speed: %q", value))
			}
		case "progress":
			switch value {
			case "continue":
				if progress != nil {
					progress(stats)
				}
				stats = ProgressStats{}
			case "end":
				// the last block is the final state (the last frame written, the total time):
				// report it too, callers are not told otherwise that a bar reached its end
				if progress != nil {
					progress(stats)
				}
				return
			default:
				runtimeError(fmt.Errorf("error reading ffmpeg progress: unexpected progress value: %s", value))
			}
		default:
			if strings.HasPrefix(key, "stream_") {
				continue
			}
			runtimeError(fmt.Errorf("unknown progress field: %q=%q", key, value))
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
