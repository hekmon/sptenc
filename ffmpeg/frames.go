package ffmpeg

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/hekmon/processpriority"
)

// FeedFramesConfig holds the configuration for FeedFrames.
type FeedFramesConfig struct {
	// Input
	Path string
	// Output: every frame scaled to Width x Height, in RGB24 (fixed: 3 bytes per pixel, R then G
	// then B, rows top to bottom, no padding), written to Output one whole frame per Write
	Width  int
	Height int
	Output io.Writer
	// Hardware decode (caller decides based on codec compatibility, the decoders incompatible with
	// the input codec are ignored: software decode)
	NVDec           bool   // use NVDEC for hardware-accelerated decoding
	NVDevice        int    // NVIDIA GPU index, see CUDADefaultDevice
	VAAPIDec        bool   // use VA-API for hardware-accelerated decoding
	VAAPIDevice     string // DRM render node, see VAAPIDefaultDevice
	D3D12Dec        bool   // use D3D12VA for hardware-accelerated decoding
	D3D12Device     int    // Direct3D 12 adapter index, see D3D12VADefaultDevice
	VideoToolboxDec bool   // use VideoToolbox for hardware-accelerated decoding
	// Reporting
	Debug        func(msg string)
	RuntimeError func(err error) // non fatal errors
	// FramesReport is given the number of whole frames written to Output so far, at most once per
	// StatsPeriod, and the total once ffmpeg is done: see WHY THE BYTES AND NOT FFMPEG'S COUNTER
	FramesReport func(frames int)
}

// FeedFrames decodes the first video stream of a file and writes its frames to config.Output,
// scaled to config.Width x config.Height in RGB24: the input of a scene detector reading pictures
// rather than ffmpeg's scdet. It returns the number of frames written. On error, it is the number
// written before it, which do not make the video and must not be used as such.
//
// It fails when ffmpeg does, when the bytes do not make a whole number of frames, when a frame
// changes its picture size, pixel format, range or matrix mid-stream (pictureChange: such a source
// fails exactly as it does with scdet, see scdetProgress), and when Output returns an error, which
// is then the cause of the returned one (errors.Is). Each of these, and the cancellation of ctx,
// kills ffmpeg at once, and it is waited for before FeedFrames returns. A Write that never returns
// blocks FeedFrames with it: Output must give up by itself when its reader is gone.
//
// # WHY THE FIRST VIDEO STREAM ALONE
//
// For the reasons ScenesDetection gives (-map 0:v:0): the frames must be those of the stream the
// master is made of, and no audio must be decoded to be thrown away.
//
// # WHY -fps_mode passthrough
//
// The rawvideo muxer is not a container: without passthrough, ffmpeg fills the timeline of the
// output at a constant rate, duplicating frames where timestamps leave a hole, and the frame N
// written is not the frame N of the master. Measured with ffmpeg 9.0.1 on H.264 clips of 240 frames
// at 24 fps: an MP4 whose video starts 0.5 s after its audio gave 252 frames (the first one written
// 13 times), a Matroska file missing 12 frames in its middle (228 frames, timestamps left as they
// were) gave 240. With passthrough, each frame is written once: 240 and 228, the counts of
// CountFrames and ffprobe -count_frames.
//
// # WHY NO RELABELLING
//
// Unlike ScenesDetection (frameAsDecoded), the frames reach the scaler with their own colour
// description: the conversion to RGB is the one each frame declares, its matrix and its range,
// which is what the pictures look like and what the detector was validated on. Relabelled, the
// bytes of a tagged source change: on the 72 first frames of a 1280x720 BT.709 H.264 clip, 268,803
// bytes of 279,936, up to 34 codes off.
//
// # WHY ONE FILTER GRAPH FOR EVERY FRAME
//
// -reinit_filter 0 does not change the bytes: on H.264 clips of 38 frames whose second part (from
// frame 14) declares a colour description, a full range, another matrix, another picture size or
// another pixel format, the bytes with and without it are those of each part fed on its own. What
// it changes is the log: with the graph kept, ffmpeg logs at the warning level the first frame of
// each change, against the properties of the graph's first frame, in the form pictureChange reads;
// with the graph rebuilt, it logs nothing at that level, and a change could not be refused. So the
// detection of such a source fails as it does with scdet, whose graph is kept for its own reasons
// (see ScenesDetection).
//
// # WHY THE BYTES AND NOT FFMPEG'S COUNTER
//
// ffmpeg's -progress is written to its standard output or a file, and the standard output carries
// the frames: progress on another descriptor would need os/exec's ExtraFiles, which Windows does
// not support. The bytes read are the frames anyway, and only whole frames are counted: what the
// progress reports has been written to Output.
//
// # WHY THE WARNING LEVEL
//
// The change lines are warnings, so ffmpeg runs at -loglevel level+warning, each line prefixed with
// its level (see feedFramesLogs). Measured with ffmpeg 9.0.1, software and VideoToolbox decoding:
// H.264 MP4 (video starting with or after its audio), Matroska and TS, MPEG-2 TS and PS, HEVC 10
// bits BT.2020, H.264 4:4:4, FFV1 RGB, PNG and ProRes print nothing at that level. Full range
// sources decoded in software (H.264 full range, MJPEG) print one warning of the scaler,
// "deprecated pixel format used, make sure you did set range correctly", and their bytes are still
// those of the extraction the detector was validated on. A change prints two warnings, the line
// pictureChange reads and "Changing video frame properties on the fly is not supported by all
// filters". The warnings other than the change lines go to Debug, so that a normal run stays
// silent; errors, and any line without a level, go to RuntimeError as stderrForwarder does.
//
// # WHY FFMPEG IS KILLED
//
// When the writing stops (Output failed, a change was refused, ctx was cancelled), ffmpeg must not
// be left behind: when the reader of its standard output stops reading, it blocks on its next
// write, forever. Closing the pipe instead makes it exit with status 224, but only after logging
// seven errors (Broken pipe), which would reach RuntimeError for a failure already returned.
// Killed, it logs nothing, and FeedFrames waits for it as for a normal end.
func FeedFrames(ctx context.Context, config FeedFramesConfig) (frames int, err error) {
	// Validate inputs
	if config.Path == "" {
		err = errors.New("input path cannot be empty")
		return
	}
	if config.Width <= 0 || config.Height <= 0 {
		err = fmt.Errorf("invalid frame size %dx%d", config.Width, config.Height)
		return
	}
	if config.Output == nil {
		err = errors.New("output writer cannot be nil")
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
		"-hide_banner",
		"-nostats",
		"-loglevel", "level+warning", // the change lines are warnings: see WHY THE WARNING LEVEL
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
		"-reinit_filter", "0", // the changes must be logged: see WHY ONE FILTER GRAPH FOR EVERY FRAME
		"-i", config.Path,
		"-map", "0:v:0", // the video the master is made of, and nothing else: see WHY THE FIRST VIDEO STREAM ALONE
		"-fps_mode", "passthrough", // each frame written once: see WHY -fps_mode passthrough
		// no filter of our own, the frames converted as they declare: see WHY NO RELABELLING
		"-f", "rawvideo",
		"-pix_fmt", "rgb24",
		"-s", strconv.Itoa(config.Width)+"x"+strconv.Itoa(config.Height),
		"pipe:1",
	)
	if config.Debug != nil {
		config.Debug(fmt.Sprintf("Feed frames with: %s", getPrintableCMDLine(FFMPEGBinary, args)))
	}
	// ffmpeg is killed when ctx is done, and when the call stops it for a cause of its own: see WHY
	// FFMPEG IS KILLED
	cmdCtx, stop := context.WithCancelCause(ctx)
	defer stop(nil)
	cmd := exec.CommandContext(cmdCtx, FFMPEGBinary, args...)
	// Prepare output handling: stdout for the frames, stderr for the changes and the errors
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
	// Start monitoring goroutine (after cmd.Start to avoid goroutine leak on error)
	logsDone := make(chan struct{})
	var changeErr error
	go func() {
		defer close(logsDone)
		if changeErr = feedFramesLogs(stderrPipe, config.Debug, config.RuntimeError); changeErr != nil {
			stop(changeErr) // the source is refused: no need to decode the rest of it
		}
	}()
	if prioErr := processpriority.Set(cmd.Process.Pid, ProcessPriority); prioErr != nil && config.RuntimeError != nil {
		config.RuntimeError(fmt.Errorf("Failed to lower %s process priority: %w", FFMPEGBinary, prioErr))
	}
	// Read the frames
	frameSize := config.Width * config.Height * 3
	reader := bufio.NewReaderSize(stdoutPipe, frameSize*16)
	frame := make([]byte, frameSize)
	var (
		readErr      error
		partial      int
		writeErr     error
		lastReport   time.Time
		lastReported = -1
	)
	for cmdCtx.Err() == nil { // nothing more is written once stopped, buffered frames included
		var n int
		if n, readErr = io.ReadFull(reader, frame); readErr != nil {
			partial = n
			break
		}
		if _, writeErr = config.Output.Write(frame); writeErr != nil {
			stop(writeErr)
			break
		}
		frames++
		if config.FramesReport != nil && time.Since(lastReport) >= StatsPeriod {
			config.FramesReport(frames)
			lastReport, lastReported = time.Now(), frames
		}
	}
	<-logsDone
	runErr := cmd.Wait()
	// Interpret
	switch cause := context.Cause(cmdCtx); {
	case writeErr != nil:
		err = fmt.Errorf("error writing frame %d: %w", frames, writeErr)
	case changeErr != nil:
		err = changeErr
	case cause != nil:
		err = fmt.Errorf("frames feed of %s interrupted after %d frames: %w", config.Path, frames, cause)
	case runErr != nil:
		err = fmt.Errorf("error during %s execution: %w\n%s", FFMPEGBinary, runErr, getPrintableCMDLine(FFMPEGBinary, args))
	case readErr != io.EOF && readErr != io.ErrUnexpectedEOF:
		err = fmt.Errorf("error reading the frames from %s: %w", FFMPEGBinary, readErr)
	case partial != 0:
		err = fmt.Errorf("%s output does not make whole frames: %d frames of %d bytes, then %d bytes",
			FFMPEGBinary, frames, frameSize, partial)
	default:
		if config.FramesReport != nil && lastReported != frames {
			config.FramesReport(frames)
		}
	}
	return
}

// feedFramesLogs reads the logs of FeedFrames' ffmpeg (-loglevel level+warning) and returns the
// first change of the frames properties pictureChange refuses, after reading every line. The other
// warnings go to debug, the errors and the lines without a level to runtimeError: see WHY THE
// WARNING LEVEL in FeedFrames.
func feedFramesLogs(ffmpegOutput io.ReadCloser, debug func(string), runtimeError func(error)) (err error) {
	defer ffmpegOutput.Close()
	scanner := bufio.NewScanner(ffmpegOutput)
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.Contains(line, graphPropertiesMarker) && strings.Contains(line, framePropertiesMarker):
			if changeErr := pictureChange(line); changeErr != nil && err == nil {
				err = changeErr
			}
		case ffmpegLogLevel(line) == "warning":
			if debug != nil {
				debug(line)
			}
		default:
			if runtimeError != nil {
				runtimeError(errors.New(line))
			}
		}
	}
	if scanErr := scanner.Err(); scanErr != nil && runtimeError != nil {
		runtimeError(fmt.Errorf("error reading ffmpeg stderr: %w", scanErr))
	}
	return
}

// ffmpegLogLevel returns the level of a line ffmpeg logged with -loglevel level+..., empty when it
// has none. The level comes after the context, when there is one:
//
//	[swscaler @ 0x75f2ad4000] [warning] deprecated pixel format used, make sure you did set range correctly
//	[error] Unrecognized option 'hwaccel videotoolbox'.
func ffmpegLogLevel(line string) string {
	if strings.HasPrefix(line, "[") {
		if context, rest, found := strings.Cut(line, "] "); found && strings.Contains(context, " @ ") {
			line = rest
		}
	}
	if !strings.HasPrefix(line, "[") {
		return ""
	}
	level, _, found := strings.Cut(line[1:], "] ")
	if !found || strings.ContainsAny(level, " []") {
		return ""
	}
	return level
}
