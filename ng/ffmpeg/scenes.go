package ffmpeg

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/hekmon/processpriority"
)

const (
	// SceneThresholdMin is the minimum valid value for the scene detection threshold.
	SceneThresholdMin = 0
	// SceneThresholdMax is the maximum valid value for the scene detection threshold.
	SceneThresholdMax = 100
)

// Scene represents a detected scene boundary with its start time and detection score.
type Scene struct {
	Start time.Duration
	Score float64
}

// ScenesDetectionConfig holds the configuration for ScenesDetection.
type ScenesDetectionConfig struct {
	// Config
	Path      string
	Threshold float64 // https://ffmpeg.org/ffmpeg-filters.html#scdet-1
	// Reporting
	Debug             func(msg string)
	RuntimeError      func(err error) // non fatal errors
	FFMPEGStatsReport func(stats ProgressStats)
}

// ScenesDetection runs ffmpeg with the scdet filter to detect scene changes in a video.
// It returns a slice of Scene values representing the detected boundaries.
func ScenesDetection(ctx context.Context, config ScenesDetectionConfig) (scenes []Scene, err error) {
	// Validate inputs
	if config.Path == "" {
		err = errors.New("input path cannot be empty")
		return
	}
	if config.Threshold < SceneThresholdMin || config.Threshold > SceneThresholdMax {
		err = fmt.Errorf("scene detection threshold must be between %d and %d", SceneThresholdMin, SceneThresholdMax)
		return
	}
	// Prepare command
	args := []string{
		"-i", config.Path,
		"-vf", "scdet=t=" + strconv.FormatFloat(config.Threshold, 'f', -1, float64Precision),
		"-f", "null", "-",
	}
	if config.Debug != nil {
		config.Debug(fmt.Sprintf("Detect scenes with: %s", getPrintableCMDLine(FFMPEGBinary, args)))
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
		scenes = scdetProgress(outputPipe, config.FFMPEGStatsReport, config.Debug, config.RuntimeError)
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

func scdetProgress(ffmpegOutput io.ReadCloser, progress func(stats ProgressStats), debug func(string), runtimeError func(error)) (scenes []Scene) {
	output := bufio.NewReader(ffmpegOutput)
	var (
		err         error
		r           rune
		currentLine string
		stats       ProgressStats
	)
	// Read rune by rune until EOF
	lineBuffer := bytes.NewBuffer(nil)
	for {
		// Read a rune a write it to our buffer
		if r, _, err = output.ReadRune(); err != nil {
			if !errors.Is(err, io.EOF) && runtimeError != nil {
				runtimeError(fmt.Errorf("error while reading rune from ffmpeg output: %w", err))
			}
			return
		}
		lineBuffer.WriteRune(r)
		// Is this a complete line ?
		switch r {
		case '\n':
			currentLine = lineBuffer.String()
			if strings.Contains(currentLine, "[scdet") {
				var scene Scene
				if scene, err = parseScdet(currentLine); err != nil {
					if runtimeError != nil {
						runtimeError(fmt.Errorf("error while parsing scdet informations: %w", err))
					}
				} else {
					scenes = append(scenes, scene)
					if debug != nil {
						debug(fmt.Sprintf("Scene %d detected at %v with score %s",
							len(scenes), scene.Start, strconv.FormatFloat(scene.Score, 'f', -1, float64Precision)))
					}
				}
			}
			lineBuffer.Reset()
		case 'x':
			currentLine = lineBuffer.String()
			if !strings.Contains(currentLine, "speed=") {
				continue
			}
			// We are near the end of a line (speed=00.0x) before line clear
			if stats, err = parseProgressStats(currentLine); err != nil {
				if runtimeError != nil {
					runtimeError(fmt.Errorf("error while parsing ffmpeg progress line: %w", err))
				}
				continue
			}
			if progress != nil {
				progress(stats)
			}
			lineBuffer.Reset()
		}
	}
}

// Scene detection constants
const (
	scdetLineFields      = 7 // Expected fields in scdet output line
	scdetScoreFieldIndex = 4 // Field index for score
	scdetTimeFieldIndex  = 6 // Field index for timestamp
)

func parseScdet(line string) (scene Scene, err error) {
	// fmt.Fprintln(liveprogress.Bypass(), strings.TrimSuffix(line, "\n"))
	var found bool
	if _, line, found = strings.Cut(line, "[scdet"); !found {
		err = errors.New("line does not contains scdet separator")
		return
	}
	fields := strings.Split(line, " ")
	if len(fields) != scdetLineFields {
		err = fmt.Errorf("line does not contains %d fields: %d", scdetLineFields, len(fields))
		return
	}
	if scene.Score, err = strconv.ParseFloat(strings.TrimSuffix(fields[scdetScoreFieldIndex], ","), float64Precision); err != nil {
		err = fmt.Errorf("error parsing score: %w", err)
		return
	}
	start, err := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSuffix(fields[scdetTimeFieldIndex], "\n"), "\r"), float64Precision)
	if err != nil {
		err = fmt.Errorf("error parsing start: %w", err)
		return
	}
	// Round to nearest millisecond to avoid sub-ms float noise like 8m34.722999999s.
	scene.Start = time.Duration(math.Round(start*1000)) * time.Millisecond
	return
}
