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
	var stats ProgressStats
	for scanner.Scan() {
		key, value, ok := strings.Cut(scanner.Text(), "=")
		if !ok {
			continue
		}
		switch key {
		case "frame":
			stats.CurrentFrame, _ = strconv.Atoi(value)
		case "fps":
			stats.FPS, _ = strconv.ParseFloat(value, 64)
		case "dup_frames":
			stats.Dup, _ = strconv.Atoi(value)
		case "drop_frames":
			stats.Drop, _ = strconv.Atoi(value)
		case "out_time_us":
			if us, err := strconv.ParseInt(value, 10, 64); err == nil {
				stats.Time = time.Duration(us) * time.Microsecond
			}
		case "bitrate":
			stats.Bitrate = value
		case "speed":
			stats.Speed, _ = strconv.ParseFloat(strings.TrimSuffix(value, "x"), 64)
		case "progress":
			if progress != nil {
				progress(stats)
			}
			if value == "end" {
				return
			}
		}
	}
	if err := scanner.Err(); err != nil && runtimeError != nil {
		runtimeError(fmt.Errorf("error reading ffmpeg progress: %w", err))
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
