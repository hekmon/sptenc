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
	// SceneScoreResolution is the resolution of the scores reported by the scdet filter: they are
	// read from its log lines, where ffmpeg prints them rounded to 3 decimals. The filter itself
	// compares its threshold with the real (unrounded) score, see pipeline.ReusableThreshold.
	SceneScoreResolution = 0.001
)

// Scene represents a detected scene boundary: where the new scene starts and its detection score.
//
// # WHY A FRAME INDEX ON TOP OF A TIME
//
// A boundary used to be a time only, and the video was cut at that time. It is wrong as soon as
// detection and cut do not run on the same file, which is the case of encode and batchsearch:
// scenes are detected on the original file (to benefit from hardware decoding) while the cut is
// done on the lossless master. These two files do not share the same timeline:
//   - the master only holds the video stream, so ffmpeg makes it start at 0. In the original file
//     the video stream can start after the audio (usual with MP4 or TS sources): every cut was
//     then late by that offset, a constant number of frames.
//   - the master is a Matroska file, its timestamps are rounded to the millisecond. Times
//     reported by the detection are rounded to the millisecond too, but not from the same value
//     when the original file is not a Matroska one: both rounding disagree often enough to push
//     a cut to the next frame (about one cut out of two on a 23.976 fps MP4 source).
//
// Nothing fails when it happens, frame counts still match: a segment simply ends with the first
// frames of the next scene, which pays the scene change twice (once within the segment, once as
// the first frame of the next one) and encodes these frames with the QP of the wrong scene.
//
// Aligning the two timelines (time of the first video frame subtracted from the detection) was
// tried: the offset goes away, not the rounding. A tolerance on the cut time would hide it but
// has to be tuned against the frame rate. Frames do not have any of these issues: the Nth frame
// out of the decoder is the Nth frame of the master, whatever the containers and their timestamps.
// So Frame is what cuts (see Segment) and what the minimum segment length is counted on (see
// core.FilterShortScenes, which met the same rounding), Start is only there to be printed.
type Scene struct {
	Frame int           // index (from 0) of the first frame of the new scene, which is also the number of frames before it
	Start time.Duration // time of that frame since the first frame of the video stream
	Score float64
}

// ScenesDetectionConfig holds the configuration for ScenesDetection.
type ScenesDetectionConfig struct {
	// Config
	Path      string
	Threshold float64 // https://ffmpeg.org/ffmpeg-filters.html#scdet-1
	// RGBToYUV is the matrix an RGB input is converted with (see YUVMatrix), empty for a YUV one:
	// scdet then reads the luma of the master, and scores the scene changes as it does on the
	// master (on a test clip, the implicit conversion scored a cut 22.275 where the master scores
	// it 24.775).
	RGBToYUV YUVMatrix
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
	if config.RGBToYUV != "" && !config.RGBToYUV.Valid() {
		err = fmt.Errorf("invalid matrix %q to convert RGB to YUV with", config.RGBToYUV)
		return
	}
	// Ignore the hardware decoders incompatible with the input codec
	dec := HWDecoderConfig{NVDec: config.NVDec, VAAPIDec: config.VAAPIDec, D3D12Dec: config.D3D12Dec, VideoToolboxDec: config.VideoToolboxDec}.
		compatibleWith(ctx, config.Path, "", config.RuntimeError)
	config.NVDec, config.VAAPIDec, config.D3D12Dec, config.VideoToolboxDec = dec.NVDec, dec.VAAPIDec, dec.D3D12Dec, dec.VideoToolboxDec
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
		"-y",
		"-hide_banner",
		"-nostats",
		"-progress", "pipe:1",
		"-stats_period",
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
	var filters []string
	if config.RGBToYUV != "" {
		filters = append(filters, RGBToYUVFilter(config.RGBToYUV))
	}
	filters = append(filters,
		// make times relative to the first video frame: the video stream does not always
		// start at 0 within its container, and a first segment longer than it really is
		// would mislead the minimum segment length
		"setpts=PTS-STARTPTS",
		"scdet=t="+strconv.FormatFloat(config.Threshold, 'f', -1, float64Precision),
		// scdet does not report frame indexes: have the frames it flagged printed by the
		// metadata filter, which does (see scdetProgress)
		"metadata=mode=print:key="+scdetTimeKey,
	)
	args = append(args,
		"-i", config.Path,
		"-vf", strings.Join(filters, ","),
		"-f", "null", "-",
	)
	if config.Debug != nil {
		config.Debug(fmt.Sprintf("Detect scenes with: %s", getPrintableCMDLine(FFMPEGBinary, args)))
	}
	cmd := exec.CommandContext(ctx, FFMPEGBinary, args...)
	// Prepare output handling: stdout for progress, stderr for scdet
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
	// Start monitoring goroutines (after cmd.Start to avoid goroutine leak on error)
	progressDone := make(chan struct{})
	go func() {
		standardProgress(stdoutPipe, config.FFMPEGStatsReport, config.RuntimeError)
		close(progressDone)
	}()
	scenesDone := make(chan struct{})
	var scenesErr error
	go func() {
		scenes, scenesErr = scdetProgress(stderrPipe, config.Debug, config.RuntimeError)
		close(scenesDone)
	}()
	if err = processpriority.Set(cmd.Process.Pid, ProcessPriority); err != nil && config.RuntimeError != nil {
		config.RuntimeError(fmt.Errorf("Failed to lower %s process priority: %w", FFMPEGBinary, err))
	}
	<-progressDone
	<-scenesDone
	if err = cmd.Wait(); err != nil {
		err = fmt.Errorf("error during %s execution: %w\n%s", FFMPEGBinary, err, getPrintableCMDLine(FFMPEGBinary, args))
		return
	}
	if scenesErr != nil {
		err = fmt.Errorf("error reading scenes from %s output: %w", FFMPEGBinary, scenesErr)
		scenes = nil
	}
	return
}

const (
	scdetScoreMarker = "lavfi.scd.score:"
	scdetTimeKey     = "lavfi.scd.time"
	metadataMarker   = "Parsed_metadata_"
	metadataFrameKey = "frame:"
)

// scdetProgress reads the scenes out of the ffmpeg logs. A scene comes as two lines:
//
//	[Parsed_scdet_1 @ 0x7eec58003240] lavfi.scd.score: 41.160, lavfi.scd.time: 4.087417
//	[Parsed_metadata_2 @ 0x7eec58003500] frame:97   pts:98098   pts_time:4.087417
//
// The first one is the scdet filter reporting a scene change (score and time), the second one
// is the metadata filter printing the frame scdet just flagged, which is the only way to get
// its index: see Scene for why it is needed. That index is the counter ffmpeg keeps for each
// filter input, the one the n of the select and showinfo filters is documented from ("starting
// from 0").
//
// # WHY A SCENE WITHOUT ITS FRAME INDEX IS A FATAL ERROR
//
// Such a scene can not be cut. Dropping it quietly would give other scenes than the ones
// detected, and what a threshold produces must not depend on a parsing accident. Better to stop
// right away: detection runs first, nothing has been encoded yet.
func scdetProgress(ffmpegOutput io.ReadCloser, debug func(string), runtimeError func(error)) (scenes []Scene, err error) {
	defer ffmpegOutput.Close()
	scanner := bufio.NewScanner(ffmpegOutput)
	var pending *Scene // scene reported by scdet, waiting for its frame index
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.Contains(line, scdetScoreMarker):
			if pending != nil && err == nil {
				err = fmt.Errorf("no frame index reported for the scene detected at %v", pending.Start)
			}
			pending = nil
			if scene, parseErr := parseScdet(line); parseErr != nil {
				if runtimeError != nil {
					runtimeError(fmt.Errorf("error while parsing scdet informations: %w", parseErr))
				}
			} else {
				pending = &scene
			}
		case pending != nil && strings.Contains(line, metadataMarker) && strings.Contains(line, metadataFrameKey):
			frame, parseErr := parseMetadataFrame(line)
			if parseErr != nil {
				if err == nil {
					err = fmt.Errorf("scene detected at %v: %w", pending.Start, parseErr)
				}
				pending = nil
				continue
			}
			pending.Frame = frame
			scenes = append(scenes, *pending)
			pending = nil
			if debug != nil {
				debug(fmt.Sprintf("Scene %d detected at frame %d (%v) with score %s",
					len(scenes)+1, frame, scenes[len(scenes)-1].Start,
					strconv.FormatFloat(scenes[len(scenes)-1].Score, 'f', -1, float64Precision)))
			}
		}
	}
	if pending != nil && err == nil {
		err = fmt.Errorf("no frame index reported for the scene detected at %v", pending.Start)
	}
	if scanErr := scanner.Err(); scanErr != nil && runtimeError != nil {
		runtimeError(fmt.Errorf("error reading ffmpeg stderr: %w", scanErr))
	}
	return
}

// parseMetadataFrame extracts the frame index from a line printed by the metadata filter:
//
//	[Parsed_metadata_2 @ 0x7eec58003500] frame:97   pts:98098   pts_time:4.087417
func parseMetadataFrame(line string) (frame int, err error) {
	_, after, found := strings.Cut(line, metadataFrameKey)
	if !found {
		return 0, fmt.Errorf("line missing the frame index: %s", line)
	}
	fields := strings.Fields(after)
	if len(fields) == 0 {
		return 0, fmt.Errorf("line missing the frame index: %s", line)
	}
	if frame, err = strconv.Atoi(fields[0]); err != nil {
		return 0, fmt.Errorf("error parsing frame index: %w", err)
	}
	if frame < 1 {
		// frame 0 can not be a scene change: there is nothing before it to change from
		return 0, fmt.Errorf("invalid frame index %d", frame)
	}
	return
}

func parseScdet(line string) (scene Scene, err error) {
	if !strings.Contains(line, "lavfi.scd.score:") {
		err = errors.New("line does not contain scdet marker")
		return
	}

	// Extract lavfi.scd.score and lavfi.scd.time from lines like:
	// [Parsed_scdet_0 @ 0x88f047180] lavfi.scd.score: 19.626, lavfi.scd.time: 16.808
	scoreIdx := strings.Index(line, "lavfi.scd.score:")
	timeIdx := strings.Index(line, "lavfi.scd.time:")
	if scoreIdx == -1 || timeIdx == -1 {
		err = fmt.Errorf("line missing expected scdet fields: %s", line)
		return
	}

	// Parse score
	scoreStr := strings.TrimSpace(line[scoreIdx+len("lavfi.scd.score:"):])
	if commaIdx := strings.Index(scoreStr, ","); commaIdx != -1 {
		scoreStr = scoreStr[:commaIdx]
	}
	if scene.Score, err = strconv.ParseFloat(scoreStr, 64); err != nil {
		err = fmt.Errorf("error parsing score: %w", err)
		return
	}

	// Parse time
	timeStr := strings.TrimSpace(line[timeIdx+len("lavfi.scd.time:"):])
	if commaIdx := strings.Index(timeStr, ","); commaIdx != -1 {
		timeStr = timeStr[:commaIdx]
	}
	start, parseErr := strconv.ParseFloat(timeStr, 64)
	if parseErr != nil {
		err = fmt.Errorf("error parsing time: %w", parseErr)
		return
	}
	// Round to nearest millisecond to avoid sub-ms float noise like 8m34.722999999s.
	scene.Start = time.Duration(math.Round(start*1000)) * time.Millisecond
	return
}
