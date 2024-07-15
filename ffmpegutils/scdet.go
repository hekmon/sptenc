package ffmpegutils

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/hekmon/processpriority"
)

type SceneDetectionConfig struct {
	// Input
	Path string
	// scdet
	Threshold       int                             // https://ffmpeg.org/ffmpeg-filters.html#scdet-1
	ProcessPriority processpriority.ProcessPriority // BelowNormal recommended
	VideoCuda       bool
	GPUID           *int
	// Reporting
	Debug               func(msg string)
	RuntimeError        func(err error)                          // non fatal errors
	ProcessRegistration func(process *os.Process, register bool) // true to register, false to unregister. Must be idempotent.
	FFMPEGStatsReport   func(stats ProgressStats)
}

func SceneDetection(config SceneDetectionConfig) (scenes []*Scene, err error) {
	var args []string
	// Input
	if config.VideoCuda {
		args = append(args, "-hwaccel", "cuda")
		if config.GPUID != nil {
			args = append(args, "-hwaccel_device", strconv.Itoa(*config.GPUID))
		}
	}
	args = append(args, "-i", config.Path)
	// Scene detection filter
	args = append(args, "-vf", fmt.Sprintf("scdet=t=%d", config.Threshold))
	// No output file
	args = append(args, "-f", "null", "-")
	// Prepare command
	if config.Debug != nil {
		config.Debug(fmt.Sprintf("Detect scenes with: %s \"%s\"\n", FFMPEGBinary, strings.Join(args, "\" \"")))
	}
	cmd := exec.Command(FFMPEGBinary, args...)
	// Prepare output handling
	outputPipe, err := cmd.StderrPipe()
	if err != nil {
		err = fmt.Errorf("error setting up stdout pipe: %w", err)
		return
	}
	progressDone := make(chan struct{})
	go func() {
		scenes = scdetProgress(outputPipe, config.FFMPEGStatsReport, config.Debug, config.RuntimeError)
		close(progressDone)
	}()
	// Start program
	if err = cmd.Start(); err != nil {
		err = fmt.Errorf("error starting ffmpeg: %w", err)
		return
	}
	if config.ProcessRegistration != nil {
		config.ProcessRegistration(cmd.Process, true)
		defer config.ProcessRegistration(cmd.Process, false)
	}
	if err = processpriority.Set(cmd.Process.Pid, config.ProcessPriority); err != nil && config.RuntimeError != nil {
		config.RuntimeError(fmt.Errorf("Failed to lower probbing process priority: %w", err))
	}
	if err = cmd.Wait(); err != nil {
		err = fmt.Errorf("error during %s execution: %w", FFMPEGBinary, err)
		return
	}
	config.ProcessRegistration(cmd.Process, false) // remove early (do not wait for defer safety)
	<-progressDone
	return
}

func scdetProgress(ffmpegOutput io.ReadCloser, progress func(stats ProgressStats), debug func(string), runtimeError func(error)) (scenes []*Scene) {
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
					scenes = append(scenes, &scene)
					if debug != nil {
						debug(fmt.Sprintf("Scene %d detected at %v with score %s\n",
							len(scenes), scene.Start, strconv.FormatFloat(scene.Score, 'f', -1, 64)))
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
			if stats, err = ParseProgressStats(currentLine); err != nil {
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

func parseScdet(line string) (scene Scene, err error) {
	// fmt.Fprintln(liveprogress.Bypass(), strings.TrimSuffix(line, "\n"))
	var found bool
	if _, line, found = strings.Cut(line, "[scdet"); !found {
		err = errors.New("line does not contains scdet separator")
		return
	}
	fields := strings.Split(line, " ")
	if len(fields) != 7 {
		err = fmt.Errorf("line does not contains 7 fields: %d", len(fields))
		return
	}
	if scene.Score, err = strconv.ParseFloat(strings.TrimSuffix(fields[4], ","), 64); err != nil {
		err = fmt.Errorf("error parsing score: %w", err)
		return
	}
	start, err := strconv.ParseFloat(strings.TrimSuffix(fields[6], "\n"), 64)
	if err != nil {
		err = fmt.Errorf("error parsing start: %w", err)
		return
	}
	// multiply by 1000 to get milliseconds and round to before switching back to seconds to avoid float rounding error:
	// [scdet @ 0x55b4790b5dc0] lavfi.scd.score: 22.189, lavfi.scd.time: 514.723 --> Scene detected at 8m34.722999999s with score 22.189
	scene.Start = time.Duration(math.Round(start*1000*float64(time.Second)) / 1000)
	return
}

type Scene struct {
	Start time.Duration
	Score float64
}
