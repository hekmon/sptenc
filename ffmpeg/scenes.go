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
//
// # WHY THE FIRST VIDEO STREAM ALONE
//
// The command names the stream it reads (-map 0:v:0): the one the master is made of (see
// FFV1VideoMaster), and nothing else. Without a -map, ffmpeg picks the streams of an output by
// itself, and picked two it should not have:
//   - The video it ranks first, which is not always the first one: in a file whose second video
//     stream is flagged as the default one, the scenes were detected on that stream and the
//     master made of the first. A clip of five scenes next to a larger video of a single one was
//     found to hold one scene, and split in one segment.
//   - An audio stream, since the null output takes one: decoded, and thrown away. A source whose
//     audio can not be decoded (every packet of its AC-3 damaged) failed here, the audio decoder
//     giving up and ffmpeg with it: split and encode stopped at their first step, on a stream
//     they only ever copy. The same source now encodes, its audio copied as it is.
//
// # WHY ONE FILTER GRAPH FOR EVERY FRAME
//
// When the properties of the decoded frames change mid-stream (a colour description appearing
// after the first frames, as on DVDs), ffmpeg rebuilds the filter graph by default. The rebuilt
// graph starts over: the metadata filter counts its frames from 0 again, setpts takes a new
// STARTPTS and scdet has no previous frame. Every cut after the change was then numbered from the
// change instead of from the first frame, and nothing failed (ffmpeg 9.0.1, MPEG-2 and H.264 clips
// whose second part declares BT.709 and the first nothing, cuts at frames 7, 14 and 26):
//   - MPEG-2, graph rebuilt at frame 13: the cuts came out at 7, 1 and 13;
//   - H.264, graph rebuilt at frame 14, the cut itself: 7 and 12, the cut at 14 lost (the first
//     frame of a graph has nothing to be compared with).
//
// Every segment after the change was cut 13 or 14 frames early. -reinit_filter 0 keeps the graph
// the first frame built for every frame: the cuts come out at 7, 14 and 26 on both, and on the
// MPEG-2 clip every frame scores as before but the two at the rebuild (the cut after it 35.886
// instead of 35.900, scored this time against a previous frame).
//
// One graph has a cost of its own: a conversion in it (the decoder's nv12 or p010 to a pixel
// format scdet takes, auto-inserted, or RGBToYUV) is set up once, for the colour description of
// the first frame, and converts every frame declaring another one into it. With VideoToolbox
// decoding the H.264 clip, the luma of the 24 frames after the change came out different from a
// software decode, and the cuts at 14 and 26 scored 32.682 and 25.772 instead of 35.898 and
// 30.140. So every frame is relabelled first, without a colour description at all (frameAsDecoded):
// a conversion has then nothing to convert but the pixel format, and the scores are the software
// decode's on every frame. Without a change, the scores are those of the graph without the
// relabelling, frame by frame (MPEG-2, H.264 BT.709, H.264 full range, HEVC 10 bits BT.2020, RGB
// FFV1 and PNG; software and VideoToolbox decoding).
//
// A single graph can not absorb a change of picture size or pixel format: scdet keeps the size it
// was configured with. On the H.264 clip with its second part at 352x288, the cut at the change
// was missed and the next one scored 30.104 instead of 30.024; with its second part at 160x120,
// 6 of its 24 frames came out as false cuts (scores 11.1 to 18.5) and the true one at 26 scored
// 17.447. A rebuilt graph misses the cut at the change too and misnumbers the others, and the
// master is rescaled to the first size anyway: such a source fails the detection instead, with
// the change ffmpeg reports (see scdetProgress).
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
	// relabelled before any conversion: see WHY ONE FILTER GRAPH FOR EVERY FRAME
	filters := []string{frameAsDecoded}
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
		"-reinit_filter", "0", // the frame counter must not restart: see WHY ONE FILTER GRAPH FOR EVERY FRAME
		"-i", config.Path,
		"-map", "0:v:0", // the video the master is made of, and nothing else: see WHY THE FIRST VIDEO STREAM ALONE
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
	// frameAsDecoded relabels every frame without a colour description, so that no conversion of
	// the graph converts one: see WHY ONE FILTER GRAPH FOR EVERY FRAME in ScenesDetection
	frameAsDecoded = "setparams=range=unknown:color_primaries=unknown:color_trc=unknown:colorspace=unknown"
	// what ffmpeg logs when a frame does not have the properties the graph was built with
	// (-reinit_filter 0), the graph's first then the frame's:
	//	[graph -1 input from stream 0:0] filter context - w: 320 h: 240 fmt: 0 csp: unknown range: unknown alpha: unspecified, incoming frame - w: 160 h: 120 fmt: 0 csp: bt709 range: tv alpha: unspecified pts_time: 0.583333
	graphPropertiesMarker = "filter context - "
	framePropertiesMarker = "incoming frame - "
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
// from 0"). It counts from the first frame only because the graph is never rebuilt, see WHY ONE
// FILTER GRAPH FOR EVERY FRAME in ScenesDetection.
//
// # WHY A SCENE WITHOUT ITS FRAME INDEX IS A FATAL ERROR
//
// Such a scene can not be cut. Dropping it quietly would give other scenes than the ones
// detected, and what a threshold produces must not depend on a parsing accident. Better to stop
// right away: detection runs first, nothing has been encoded yet.
//
// # WHY A CHANGE OF PICTURE SIZE OR PIXEL FORMAT IS A FATAL ERROR
//
// The detection runs one filter graph for every frame (see ScenesDetection), which scdet can not
// read a frame of another size or pixel format with: such scores are wrong, false cuts included,
// and nothing fails. ffmpeg logs every frame that does not have the properties of the graph; a
// change of picture size or pixel format stops the detection for the same reason as above.
//
// # WHY A CHANGE OF RANGE OR MATRIX IS A FATAL ERROR TOO
//
// Scene detection itself reads such frames fine: they are relabelled before anything converts
// them. The rest of the run does not. The master converts the frames after the change into the
// range and matrix of the first one, and the final VMAF of encode and batchsearch refuses a source
// whose range changes (a missing range read as limited), or whose matrix changes where the first
// frame declares one: see WHY A CHANGE OF SIZE, PIXEL FORMAT, RANGE OR MATRIX IS AN ERROR in
// VMAFCompute for the measurements. That refusal comes after every segment has been encoded, hours later;
// detection is the first pass over the source, so it refuses it with the same rule, before anything
// is encoded. A line that does not give the range and the matrix can not rule a change out, and
// fails like an unreadable one.
//
// Measured with ffmpeg 9.0.1 on H.264 clips of 38 frames whose second part, from frame 14, declares
// other properties, decoded in software and by VideoToolbox: ffmpeg logs the change once, with the
// source's own matrix and range (the relabelling comes after the graph's input), for a range
// appearing as full, a matrix changing from BT.601 to BT.709, and a colour description appearing.
// A file without a change logs no such line: H.264 BT.709, H.264 full range and HEVC 10 bits
// BT.2020, whose first frames have the same matrix and range with either decoding.
//
// BT.601's two labels, smpte170m and bt470bg, name the same coefficients, and a source switching
// between them is plausible on DVDs: MPEG-2, 8-bit and 10-bit H.264 clips switching from one to
// the other, mastered by FFV1VideoMaster, gave a master holding the pictures of each part decoded
// on its own, on every frame (MPEG-2 from smpte170m to bt470bg, H.264 either way), and the final
// VMAF, its refusal bypassed, scored the source as its master on every frame (either way), decoded
// in software and (8-bit H.264) by VideoToolbox. Such a switch is accepted, as VMAFCompute
// accepts it.
func scdetProgress(ffmpegOutput io.ReadCloser, debug func(string), runtimeError func(error)) (scenes []Scene, err error) {
	defer ffmpegOutput.Close()
	scanner := bufio.NewScanner(ffmpegOutput)
	var pending *Scene // scene reported by scdet, waiting for its frame index
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.Contains(line, graphPropertiesMarker) && strings.Contains(line, framePropertiesMarker):
			if changeErr := pictureChange(line); changeErr != nil && err == nil {
				err = changeErr
			}
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

// pictureChange returns an error when a line of ffmpeg reporting a frame whose properties are
// not the graph's (see graphPropertiesMarker) gives it another picture size or pixel format, or
// another range, or another matrix where the graph's first frame declared one, or when it can not
// be read: see WHY A CHANGE OF PICTURE SIZE OR PIXEL FORMAT IS A FATAL ERROR and WHY A CHANGE OF
// RANGE OR MATRIX IS A FATAL ERROR TOO. A matrix appearing where the first frame declared none is
// not one, nor a change between BT.601's two labels.
func pictureChange(line string) error {
	_, after, _ := strings.Cut(line, graphPropertiesMarker)
	graphProperties, frameProperties, _ := strings.Cut(after, framePropertiesMarker)
	type properties struct{ size, format, matrix, colorRange string }
	read := func(text string) (p properties, ok bool) {
		var w, h string
		fields := strings.Fields(text)
		for i := 0; i+1 < len(fields); i++ {
			value := strings.TrimSuffix(fields[i+1], ",")
			switch fields[i] {
			case "w:":
				w = value
			case "h:":
				h = value
			case "fmt:":
				p.format = value
			case "csp:":
				p.matrix = value
			case "range:":
				p.colorRange = value
			}
		}
		p.size = w + "x" + h
		// ffmpeg reads a missing range as limited (see declaredRange)
		if p.colorRange == "unknown" {
			p.colorRange = "tv"
		}
		return p, w != "" && h != "" && p.format != "" && p.matrix != "" && p.colorRange != ""
	}
	graph, graphOK := read(graphProperties)
	frame, frameOK := read(frameProperties)
	if !graphOK || !frameOK {
		return fmt.Errorf("can not tell what changes mid-stream in the video: %s", line)
	}
	if graph.size != frame.size || graph.format != frame.format {
		return fmt.Errorf("the video changes its picture size or pixel format mid-stream (%s to %s), which scene detection can not compare: %s",
			graph.size, frame.size, line)
	}
	bt601 := func(matrix string) bool { return matrix == "smpte170m" || matrix == "bt470bg" }
	var change string
	switch {
	case graph.colorRange != frame.colorRange:
		change = "range (" + graph.colorRange + " to " + frame.colorRange + ")"
	case graph.matrix != "unknown" && graph.matrix != frame.matrix && !(bt601(graph.matrix) && bt601(frame.matrix)):
		change = "matrix (" + graph.matrix + " to " + frame.matrix + ")"
	default:
		return nil
	}
	return fmt.Errorf("the video changes its %s mid-stream: scene detection reads it, but the master converts such frames and the final VMAF refuses the source, which it would do only once every segment is encoded: %s",
		change, line)
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
