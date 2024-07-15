package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/hekmon/scenc/ffmpegutils"

	"github.com/hekmon/liveprogress/v2"
	"github.com/hekmon/processpriority"
)

func getScenes(path string, totalDuration time.Duration, threshold int, cuvid bool, gpusList []int) (scenes []*Scene, err error) {
	var args []string
	// Input
	if cuvid {
		args = append(args, "-hwaccel", "cuda")
		if len(gpusList) > 0 {
			args = append(args, "-hwaccel_device", strconv.Itoa(gpusList[0]))
		}
	}
	args = append(args, "-i", path)
	// Scene detection filter
	args = append(args, "-vf", fmt.Sprintf("scdet=t=%d", threshold))
	// No output file
	args = append(args, "-f", "null", "-")
	// Prepare command
	bypass := liveprogress.Bypass()
	if *debug {
		fmt.Fprintf(bypass, "Extract input file frames with: %s \"%s\"\n", FFMPEG, strings.Join(args, "\" \""))
	}
	cmd := exec.Command(FFMPEG, args...)
	// Prepare output handling
	outputPipe, err := cmd.StderrPipe()
	if err != nil {
		err = fmt.Errorf("error setting up stdout pipe: %w", err)
		return
	}
	progressDone := make(chan struct{})
	go func() {
		scenes = extractSceneProgress(outputPipe, totalDuration)
		close(progressDone)
	}()
	// Start program
	start := time.Now()
	if err = cmd.Start(); err != nil {
		err = fmt.Errorf("error starting ffmpeg: %w", err)
		return
	}
	children.Add(cmd.Process)
	defer children.Remove(cmd.Process)
	if err = processpriority.Set(cmd.Process.Pid, processpriority.BelowNormal); err != nil {
		fmt.Fprintf(bypass, "Failed to lower extractor process priority: %s\n", err)
	}
	if err = cmd.Wait(); err != nil {
		err = fmt.Errorf("error during ffmpeg execution: %w", err)
		return
	}
	duration := time.Since(start)
	children.Remove(cmd.Process) // remove early (do not wait for defer safety)
	<-progressDone
	// Successful execution
	fmt.Fprintf(bypass, "Detected %d scenes in %v\n", len(scenes), duration.Round(time.Second))
	return
}

func extractSceneProgress(ffmpegOutput io.ReadCloser, totalDuration time.Duration) (scenes []*Scene) {
	output := bufio.NewReader(ffmpegOutput)
	var (
		err         error
		r           rune
		currentLine string
		stats       ffmpegutils.ProgressStats
		bar         *liveprogress.Bar
	)
	bypass := liveprogress.Bypass()
	// Prepare progress bar
	bar = liveprogress.AddBar(
		liveprogress.WithTotal(uint64(totalDuration)),
		liveprogress.WithLineFillRunes(),
		// liveprogress.WithWidth(barsWidth),
		liveprogress.WithPrependDecorator(func(bar *liveprogress.Bar) string {
			return "  Scene detections | "
		}),
		liveprogress.WithPrependTimeElapsed(liveprogress.BaseStyle()),
		liveprogress.WithAppendPercent(liveprogress.BaseStyle()),
		liveprogress.WithAppendTimeRemaining(liveprogress.BaseStyle()),
		liveprogress.WithAppendDecorator(func(bar *liveprogress.Bar) string {
			var build strings.Builder
			build.WriteString(fmt.Sprintf(" remaining | %d frames processed (%0.0f fps, speed: %0.2fx)",
				stats.CurrentFrame, stats.FPS, stats.Speed,
			))
			return build.String()
		}),
	)
	defer liveprogress.RemoveBar(bar)
	// Read rune by rune until EOF
	lineBuffer := bytes.NewBuffer(nil)
	for {
		// Read a rune a write it to our buffer
		if r, _, err = output.ReadRune(); err != nil {
			if !errors.Is(err, io.EOF) {
				fmt.Fprintf(bypass, "error while reading rune from ffmpeg output: %s\n", err)
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
					fmt.Fprintf(bypass, "error while parsing scdet informations: %s\n", err)
				} else {
					scenes = append(scenes, &scene)
					if *debug {
						fmt.Fprintf(bypass, "Scene %d detected at %v with score %s\n",
							len(scenes), scene.Start, strconv.FormatFloat(scene.Score, 'f', -1, 64))
					}
				}
			}
			lineBuffer.Reset()
		case 'x':
			currentLine = lineBuffer.String()
			if !strings.Contains(currentLine, "speed=") {
				continue
			}
			// We are near the end of a line (speed=XX.Xx) before line clear
			if stats, err = ffmpegutils.ParseProgressStats(currentLine); err != nil {
				fmt.Fprintf(bypass, "Error parsing ffmpeg progress line: %s\n", err)
				continue
			}
			bar.CurrentSet(uint64(stats.Time))
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
	// multiply by 1000 to avoid float rounding error: [scdet @ 0x55b4790b5dc0] lavfi.scd.score: 22.189, lavfi.scd.time: 514.723 --> Scene detected at 8m34.722999999s with score 22.189
	scene.Start = time.Duration(math.Round(start*1000*float64(time.Second)) / 1000)
	return
}

type Scene struct {
	Start        time.Duration
	Score        float64
	OriginalPath string
	Quantization map[int]SceneQuantized
}

type SceneQuantized struct {
	VMAF string
	File string
}
