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

// CountFramesConfig holds the configuration for CountFrames.
type CountFramesConfig struct {
	// Input
	Path string
	// Hardware decode: decoders incompatible with the input codec are ignored (software decode)
	HWDecoderConfig
	// Reporting
	Debug             func(msg string)
	RuntimeError      func(err error) // non fatal errors
	FFMPEGStatsReport func(stats ProgressStats)
}

// ReadFrames is what can only be known by decoding a whole video stream: its exact number of
// frames and how long they last, see CountFrames.
type ReadFrames struct {
	Nb int // exact number of frames
	// Time between two consecutive frames, see FFProbeBinaryStream.IsConstantFrameRate
	NbDurations      int // number of durations measured
	ShortestDuration time.Duration
	LongestDuration  time.Duration
}

// CountFrames decodes the first video stream of the file entirely to count its frames exactly
// and to measure how long each of them lasts. The count is the number of frames ffmpeg reports
// in its last progress block, the durations come from the timestamps of the decoded frames.
//
// # WHY FFMPEG AND NOT FFPROBE
//
// ffprobe -count_frames does the same job, but decodes in software only. ffmpeg decodes with
// the same hardware decoders the rest of the pipeline uses, which leaves the CPU to the encodes
// and the VMAF computations running at the same time (see QPSearchConfig.NbConcurrentSegments).
// The metadata of the streams is not read here anymore: GetStreamsInfos does it, in a fraction
// of a second, and callers need it before counting anyway (the duration for the progress).
//
// # WHY A COUNT OF 0 IS AN ERROR
//
// No caller can do anything with a video stream without frames, and this is how a file ffmpeg
// can not read properly looks like. Every reason for it can not be known in advance: better to
// stop here, before any encoding, whatever the reason is.
func CountFrames(ctx context.Context, config CountFramesConfig) (frames ReadFrames, err error) {
	// Validate inputs
	if config.Path == "" {
		err = errors.New("input path cannot be empty")
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
	// Prepare arguments
	args := []string{
		"-hide_banner",
		// the frames timestamps are printed by a filter at the info level: errors are told apart
		// by the level prefix, see countFramesLogs
		"-loglevel", "level+info",
		"-nostats", "-progress", "pipe:1", "-stats_period",
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
		"-map", "0:v:0", // only the first video stream is decoded
		"-vf", strings.Join([]string{
			// express the timestamps in microseconds whatever the container time base: the
			// metadata filter prints them as integers of its input time base (pts_time is
			// printed with 6 significant digits only, useless after a few minutes)
			"settb=AVTB",
			// the metadata filter only prints frames holding some metadata: give one to each
			"metadata=mode=add:key=" + countFramesMetadataKey + ":value=1",
			"metadata=mode=print:key=" + countFramesMetadataKey,
		}, ","),
		"-fps_mode", "passthrough", // no frame duplicated or dropped: every decoded frame is counted
		"-f", "null", "-",
	)
	// Prepare command
	if config.Debug != nil {
		config.Debug(fmt.Sprintf("Count frames with: %s", getPrintableCMDLine(FFMPEGBinary, args)))
	}
	cmd := exec.CommandContext(ctx, FFMPEGBinary, args...)
	// Prepare output handling: stdout for progress, stderr for the frames timestamps
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
		// the last progress block is the final state: its frame is the count
		standardProgress(stdoutPipe, func(stats ProgressStats) {
			frames.Nb = stats.CurrentFrame
			if config.FFMPEGStatsReport != nil {
				config.FFMPEGStatsReport(stats)
			}
		}, config.RuntimeError)
		close(progressDone)
	}()
	logsDone := make(chan struct{})
	go func() {
		frames.NbDurations, frames.ShortestDuration, frames.LongestDuration = countFramesLogs(stderrPipe, config.RuntimeError)
		close(logsDone)
	}()
	if err = processpriority.Set(cmd.Process.Pid, ProcessPriority); err != nil && config.RuntimeError != nil {
		config.RuntimeError(fmt.Errorf("Failed to lower %s process priority: %w", FFMPEGBinary, err))
	}
	<-progressDone
	<-logsDone
	if err = cmd.Wait(); err != nil {
		err = fmt.Errorf("error during %s execution: %w\n%s", FFMPEGBinary, err, getPrintableCMDLine(FFMPEGBinary, args))
		return
	}
	if frames.Nb <= 0 {
		err = fmt.Errorf("%s did not count any frame in the video stream: the file can not be read properly\n%s",
			FFMPEGBinary, getPrintableCMDLine(FFMPEGBinary, args))
		return
	}
	return
}

const (
	countFramesMetadataKey = "sptenc"
	metadataPTSKey         = "pts:"
	logLevelError          = "[error]"
	logLevelFatal          = "[fatal]"
	logLevelPanic          = "[panic]"
)

// countFramesLogs measures how long frames last out of the ffmpeg logs. Each decoded frame is
// printed by the metadata filter as two lines, the first one being the useful one:
//
//	[Parsed_metadata_2 @ 0x79954061c0] [info] frame:47   pts:1960000 pts_time:1.96
//	[Parsed_metadata_2 @ 0x79954061c0] [info] sptenc=1
//
// The time of a frame is its pts, in microseconds (see the settb filter in CountFrames). Frames
// come out of the decoder in presentation order: the shortest and the longest time between two
// consecutive frames are what IsConstantFrameRate relies on. A frame without a time (NOPTS,
// some formats do not provide it) breaks the chain: the next duration measured is the one
// between the two frames following it.
//
// The rest of the logs is the usual ffmpeg chatter at the info level, ignored, except for the
// lines flagged as errors by the level prefix ffmpeg was asked for: these are reported, as the
// standard error output of the other ffmpeg commands (run at the error level) is.
func countFramesLogs(ffmpegOutput io.ReadCloser, runtimeError func(error)) (nbDurations int, shortest, longest time.Duration) {
	defer ffmpegOutput.Close()
	scanner := bufio.NewScanner(ffmpegOutput)
	var (
		previousTime      time.Duration
		previousTimeKnown bool
	)
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.Contains(line, metadataMarker) && strings.Contains(line, metadataFrameKey):
			frameTime, known, parseErr := parseMetadataPTS(line)
			if parseErr != nil {
				if runtimeError != nil {
					runtimeError(fmt.Errorf("error while parsing frame timestamp: %w", parseErr))
				}
				known = false
			}
			if !known {
				previousTimeKnown = false
				continue
			}
			if previousTimeKnown {
				duration := frameTime - previousTime
				if nbDurations == 0 || duration < shortest {
					shortest = duration
				}
				if nbDurations == 0 || duration > longest {
					longest = duration
				}
				nbDurations++
			}
			previousTime, previousTimeKnown = frameTime, true
		case strings.Contains(line, logLevelError), strings.Contains(line, logLevelFatal), strings.Contains(line, logLevelPanic):
			if runtimeError != nil {
				runtimeError(errors.New(line))
			}
		}
	}
	if err := scanner.Err(); err != nil && runtimeError != nil {
		runtimeError(fmt.Errorf("error reading ffmpeg stderr: %w", err))
	}
	return
}

// parseMetadataPTS extracts the timestamp of the frame from a line printed by the metadata
// filter (see countFramesLogs), in microseconds. known is false when the frame has no
// timestamp (NOPTS).
func parseMetadataPTS(line string) (frameTime time.Duration, known bool, err error) {
	_, after, found := strings.Cut(line, metadataFrameKey)
	if !found {
		err = fmt.Errorf("line missing the frame index: %s", line)
		return
	}
	for _, field := range strings.Fields(after) {
		value, isPTS := strings.CutPrefix(field, metadataPTSKey)
		if !isPTS {
			continue
		}
		if value == "NOPTS" {
			return
		}
		microseconds, parseErr := strconv.ParseInt(value, 10, 64)
		if parseErr != nil {
			err = fmt.Errorf("error parsing frame timestamp %q: %w", value, parseErr)
			return
		}
		return time.Duration(microseconds) * time.Microsecond, true, nil
	}
	err = fmt.Errorf("line missing the frame timestamp: %s", line)
	return
}
