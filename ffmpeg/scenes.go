package ffmpeg

import (
	"bufio"
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
	// Hardware decode (caller decides based on codec compatibility)
	NVDec           bool   // use NVDEC for hardware-accelerated decoding
	NVDevice        int    // NVIDIA GPU index, see CUDADefaultDevice
	VAAPIDec        bool   // use VA-API for hardware-accelerated decoding
	VAAPIDevice     string // DRM render node, see VAAPIDefaultDevice
	D3D12Dec        bool   // use D3D12VA for hardware-accelerated decoding
	D3D12Device     int    // Direct3D 12 adapter index, see D3D12VADefaultDevice
	VideoToolboxDec bool   // use VideoToolbox for hardware-accelerated decoding
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
	// Auto-detect hardware decode compatibility if flags are set
	if config.NVDec || config.VAAPIDec || config.D3D12Dec || config.VideoToolboxDec {
		if stats, probeErr := GetStreamsInfos(ctx, GetStreamsInfosConfig{Path: config.Path}); probeErr == nil {
			if video := stats.VideoTrack(); video != nil {
				if config.NVDec && !IsNVDecCompatible(video.CodecName) {
					config.NVDec = false
				}
				if config.VAAPIDec && !IsVAAPIDecCompatible(video.CodecName) {
					config.VAAPIDec = false
				}
				if config.D3D12Dec && !IsD3D12DecCompatible(video.CodecName) {
					config.D3D12Dec = false
				}
				if config.VideoToolboxDec && !IsVideoToolboxDecCompatible(video.CodecName) {
					config.VideoToolboxDec = false
				}
			}
		} else if config.RuntimeError != nil {
			config.RuntimeError(fmt.Errorf("failed to probe input for hardware decode auto-detection: %w, falling back to software decode", probeErr))
		}
	}
	// Apply defaults
	if config.NVDevice == 0 {
		config.NVDevice = CUDADefaultDevice
	}
	if config.VAAPIDevice == "" {
		config.VAAPIDevice = VAAPIDefaultDevice
	}
	if config.D3D12Device == 0 {
		config.D3D12Device = D3D12VADefaultDevice
	}
	// Prepare command
	args := []string{
		"-y", "-loglevel", "error", "-nostats", "-progress", "pipe:2", "-stats_period",
		strconv.FormatFloat(float64(StatsPeriod)/float64(time.Second), 'f', -1, 64),
	}
	// Hardware decode paths
	if config.NVDec {
		args = append(args, "-hwaccel", "cuda")
		if config.NVDevice >= 0 {
			args = append(args, "-hwaccel_device", strconv.Itoa(config.NVDevice))
		}
	} else if config.VAAPIDec {
		args = append(args, "-hwaccel", "vaapi")
		if config.VAAPIDevice != "" {
			args = append(args, "-vaapi_device", config.VAAPIDevice)
		}
	} else if config.D3D12Dec {
		args = append(args, "-hwaccel", "d3d12va")
		if config.D3D12Device >= 0 {
			args = append(args, "-hwaccel_device", strconv.Itoa(config.D3D12Device))
		}
	} else if config.VideoToolboxDec {
		args = append(args, "-hwaccel", "videotoolbox")
	}
	args = append(args,
		"-i", config.Path,
		"-vf", "scdet=t="+strconv.FormatFloat(config.Threshold, 'f', -1, float64Precision),
		"-f", "null", "-",
	)
	if config.Debug != nil {
		config.Debug(fmt.Sprintf("Detect scenes with: %s", getPrintableCMDLine(FFMPEGBinary, args)))
	}
	cmd := exec.CommandContext(ctx, FFMPEGBinary, args...)
	// Prepare output handling
	outputPipe, err := cmd.StderrPipe()
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
	defer ffmpegOutput.Close()
	scanner := bufio.NewScanner(ffmpegOutput)
	var stats ProgressStats
	for scanner.Scan() {
		line := scanner.Text()
		if strings.Contains(line, "[scdet") {
			var scene Scene
			var err error
			if scene, err = parseScdet(line); err != nil {
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
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			if runtimeError != nil && line != "" {
				runtimeError(errors.New(line))
			}
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
		runtimeError(fmt.Errorf("error reading ffmpeg output: %w", err))
	}
	return
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
