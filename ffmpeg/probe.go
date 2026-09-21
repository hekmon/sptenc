package ffmpeg

import (
	"bytes"
	"context"
	"encoding/json"
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

// GetStreamsInfosConfig holds the configuration for GetStreamsInfos.
type GetStreamsInfosConfig struct {
	// Input
	Path string
	// Reporting
	Debug        func(msg string)
	RuntimeError func(err error) // non fatal errors
}

// GetStreamsInfos runs ffprobe to extract stream and format metadata from the given file.
func GetStreamsInfos(ctx context.Context, config GetStreamsInfosConfig) (stats FFProbeStats, err error) {
	// Validate inputs
	if config.Path == "" {
		err = errors.New("input path cannot be empty")
		return
	}
	// Build up args
	args := []string{"-loglevel", "error", "-print_format", "json", "-show_format", "-show_streams"}
	args = append(args, config.Path)
	// Prepare command
	if config.Debug != nil {
		config.Debug(fmt.Sprintf("Extract input file metadata: %s", getPrintableCMDLine(FFProbeBinary, args)))
	}
	cmd := exec.CommandContext(ctx, FFProbeBinary, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	// Exec program
	if err = cmd.Start(); err != nil {
		err = fmt.Errorf("error starting %s: %w\n%s", FFProbeBinary, err, getPrintableCMDLine(FFProbeBinary, args))
		return
	}
	if err = cmd.Wait(); err != nil {
		err = fmt.Errorf("error during %s execution: %w\n%s\n%s", FFProbeBinary, err, stderr.String(), getPrintableCMDLine(FFProbeBinary, args))
		return
	}
	// Extract report infos
	if err = json.Unmarshal(stdout.Bytes(), &stats); err != nil {
		err = fmt.Errorf("error parsing %s output: %w", FFProbeBinary, err)
		return
	}
	return
}

// GetStreamsInfosCFConfig holds the configuration for GetStreamsInfosCF.
type GetStreamsInfosCFConfig struct {
	// Same as light version
	GetStreamsInfosConfig
	// But with read report for long processing
	ReadBytesReport func(n int)
}

// GetStreamsInfosCF runs ffprobe with -count_frames to get the exact number of frames of the
// first video stream: it is decoded entirely, which can be long. Only that stream is returned
// (along with the format): it is the one the master is made of, and the only one callers need.
//
// # WHY FFPROBE IS GIVEN THE PATH AND NOT THE FILE CONTENT ON ITS STANDARD INPUT
//
// The file used to be opened here and sent to ffprobe through its standard input, as counting
// the bytes going through was a cheap way to report progress. But a pipe can not be seeked, and
// some files can not be read without seeking: a MP4 file with its index at the end, which is what
// ffmpeg and most cameras write by default, is one of them. ffprobe then complains on its
// standard error output but still exits with a success and no frames count at all. It was read
// as a count of 0, nothing noticed until the frames count verification before the final VMAF,
// once everything had been encoded. Given the path, ffprobe reads the file the way ffmpeg will.
//
// Progress now comes from ffprobe itself: it is asked for the position in the file of each
// frame it decodes, and prints them as it goes. The report stays expressed in bytes read.
//
// # WHY A COUNT OF 0 IS AN ERROR
//
// No caller can do anything with a video stream without frames, and this is how a file ffprobe
// can not read properly looks like. Every reason for it can not be known in advance: better to
// stop here, before any encoding, whatever the reason is.
func GetStreamsInfosCF(ctx context.Context, config GetStreamsInfosCFConfig) (stats FFProbeStats, err error) {
	// Validate inputs
	if config.Path == "" {
		err = errors.New("input path cannot be empty")
		return
	}
	// Build up args
	args := []string{
		"-loglevel", "error", "-print_format", "json=c=1",
		"-show_format", "-show_streams", "-count_frames",
		"-select_streams", "v:0",
		"-show_entries", "frame=pkt_pos", // for progress, see parseProbeWithFrames
		"-threads", "auto",
		config.Path,
	}
	// Prepare command
	if config.Debug != nil {
		config.Debug(fmt.Sprintf("Extract input file complete metadata: %s", getPrintableCMDLine(FFProbeBinary, args)))
	}
	cmd := exec.CommandContext(ctx, FFProbeBinary, args...)
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		err = fmt.Errorf("error setting up stdout pipe: %w", err)
		return
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	// Exec program
	if err = cmd.Start(); err != nil {
		err = fmt.Errorf("error starting %s: %w\n%s", FFProbeBinary, err, getPrintableCMDLine(FFProbeBinary, args))
		return
	}
	if err = processpriority.Set(cmd.Process.Pid, ProcessPriority); err != nil && config.RuntimeError != nil {
		config.RuntimeError(fmt.Errorf("Failed to lower %s process priority: %w", FFProbeBinary, err))
	}
	// Read the report as it comes (must be done before waiting for the program to end)
	stats, parseErr := parseProbeWithFrames(stdoutPipe, config.ReadBytesReport)
	if parseErr != nil {
		// do not let ffprobe blocked on a standard output nobody reads anymore
		_, _ = io.Copy(io.Discard, stdoutPipe)
	}
	if err = cmd.Wait(); err != nil {
		err = fmt.Errorf("error during %s execution: %w\n%s\n%s", FFProbeBinary, err, stderr.String(), getPrintableCMDLine(FFProbeBinary, args))
		return
	}
	// With this log level, anything ffprobe wrote is an error, even if it ended with a success
	if config.RuntimeError != nil {
		for line := range strings.SplitSeq(strings.TrimSpace(stderr.String()), "\n") {
			if line != "" {
				config.RuntimeError(fmt.Errorf("%s: %s", FFProbeBinary, line))
			}
		}
	}
	if parseErr != nil {
		err = fmt.Errorf("error parsing %s output: %w", FFProbeBinary, parseErr)
		return
	}
	if video := stats.VideoTrack(); video != nil && video.NbReadFrames <= 0 {
		err = fmt.Errorf("%s did not count any frame in the video stream: the file can not be read properly\n%s",
			FFProbeBinary, getPrintableCMDLine(FFProbeBinary, args))
		return
	}
	return
}

// parseProbeWithFrames reads a ffprobe JSON report holding a frames section on top of the usual
// streams and format ones. Frames come first and are printed while the file is decoded: they are
// consumed one by one to report progress, not stored (there is one entry per frame of the video).
//
// Progress is the position of the frame within the file (pkt_pos), reported as the number of
// bytes gained since the last report. Positions do not strictly increase (streams are
// interleaved, frames reordered): only a new highest position counts. Some formats do not
// provide it: there is no progress then, the count itself is not affected.
func parseProbeWithFrames(report io.Reader, readBytesReport func(n int)) (stats FFProbeStats, err error) {
	decoder := json.NewDecoder(report)
	expectDelim := func(expected json.Delim) error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		if delim, ok := token.(json.Delim); !ok || delim != expected {
			return fmt.Errorf("unexpected token %v, expected %v", token, expected)
		}
		return nil
	}
	if err = expectDelim('{'); err != nil {
		return
	}
	var highestPosition int64
	for decoder.More() {
		var key json.Token
		if key, err = decoder.Token(); err != nil {
			return
		}
		switch key {
		case "frames":
			if err = expectDelim('['); err != nil {
				return
			}
			for decoder.More() {
				var frame struct {
					PktPos string `json:"pkt_pos"`
				}
				if err = decoder.Decode(&frame); err != nil {
					return
				}
				// missing or "N/A" when the format does not provide it
				if position, parseErr := strconv.ParseInt(frame.PktPos, 10, 64); parseErr == nil && position > highestPosition {
					if readBytesReport != nil {
						readBytesReport(int(position - highestPosition))
					}
					highestPosition = position
				}
			}
			if err = expectDelim(']'); err != nil {
				return
			}
		case "streams":
			if err = decoder.Decode(&stats.Streams); err != nil {
				return
			}
		case "format":
			if err = decoder.Decode(&stats.Format); err != nil {
				return
			}
		default:
			var ignored json.RawMessage
			if err = decoder.Decode(&ignored); err != nil {
				return
			}
		}
	}
	err = expectDelim('}')
	return
}

// FFProbeStats represents the top-level output of an ffprobe -show_format -show_streams run.
type FFProbeStats struct {
	Format  *FFProbeFormat         `json:"format"`
	Streams []*FFProbeBinaryStream `json:"streams"`
}

// VideoTrack returns the first video stream from the probe results, or nil if none is found.
func (stats *FFProbeStats) VideoTrack() (videoStream *FFProbeBinaryStream) {
	for _, stream := range stats.Streams {
		if stream.CodecType == "video" {
			videoStream = stream
			break
		}
	}
	return
}

// AudioTrack returns the first audio stream from the probe results, or nil if none is found.
func (stats *FFProbeStats) AudioTrack() (audioStream *FFProbeBinaryStream) {
	for _, stream := range stats.Streams {
		if stream.CodecType == "audio" {
			audioStream = stream
			break
		}
	}
	return
}

// FFProbeFormat holds the container-level metadata for a probed file.
type FFProbeFormat struct {
	Filename       string            `json:"filename"`
	NbStreams      int               `json:"nb_streams"`
	NbPrograms     int               `json:"nb_programs"`
	NbStreamGroups int               `json:"nb_stream_groups"`
	Name           FormatName        `json:"format_name"`
	LongName       string            `json:"format_long_name"`
	StartTime      string            `json:"start_time"`
	Duration       time.Duration     `json:"duration"`
	Size           string            `json:"size"`
	BitRate        string            `json:"bit_rate"`
	ProbeScore     int               `json:"probe_score"`
	Tags           map[string]string `json:"tags"`
}

// UnmarshalJSON implements json.Unmarshaler to parse the Duration field as seconds.
func (format *FFProbeFormat) UnmarshalJSON(data []byte) (err error) {
	type Mask FFProbeFormat
	tmp := &struct {
		Duration string `json:"duration"`
		*Mask
	}{
		Mask: (*Mask)(format),
	}
	if err = json.Unmarshal(data, &tmp); err != nil {
		return err
	}
	seconds, err := strconv.ParseFloat(tmp.Duration, float64Precision)
	if err != nil {
		return fmt.Errorf("failed to parse duration %q as float: %w", tmp.Duration, err)
	}
	format.Duration = time.Duration(seconds * float64(time.Second))
	return nil
}

// FormatName is the name of a container format as reported by ffprobe.
type FormatName string

// CodecName is the name of an audio or video codec as reported by ffprobe.
type CodecName string

const (
	// Containers
	FormatMatroska  FormatName = "matroska,webm"           // LongName: Matroska / WebM
	FormatQuickTime FormatName = "mov,mp4,m4a,3gp,3g2,mj2" // LongName: QuickTime / MOV
	FormatAVI       FormatName = "avi"                     // LongName: AVI (Audio Video Interleaved)
	// Audio
	CodecAudioPCM    CodecName = "pcm_s16le" // LongName: PCM signed 16-bit little-endian
	CodecAudioPCM24b CodecName = "pcm_s24le" // LongName: PCM signed 24-bit little-endian
	// Video
	CodecVideoMPEG1 CodecName = "mpeg1video" // unverified
	CodecVideoMPEG2 CodecName = "mpeg2video" // unverified
	CodecVideoMPEG4 CodecName = "mpeg4"      // unverified
	CodecVideoVC1   CodecName = "vc1"
	CodecVideoAVC   CodecName = "h264"
	CodecVideoHEVC  CodecName = "hevc"
	CodecVideoVP8   CodecName = "vp8"   // unverified
	CodecVideoVP9   CodecName = "vp9"   // unverified
	CodecVideoAV1   CodecName = "av1"   // unverified
	CodecVideoMJPEG CodecName = "mjpeg" // unverified
)

// FFProbeBinaryStream holds metadata for a single stream within a media file.
type FFProbeBinaryStream struct {
	Index              int                            `json:"index"`
	CodecName          CodecName                      `json:"codec_name"`
	CodecLongName      string                         `json:"codec_long_name"`
	Profile            string                         `json:"profile,omitempty"`
	CodecType          string                         `json:"codec_type"`
	CodecTagString     string                         `json:"codec_tag_string"`
	CodecTag           string                         `json:"codec_tag"`
	Width              int                            `json:"width,omitempty"`
	Height             int                            `json:"height,omitempty"`
	CodedWidth         int                            `json:"coded_width,omitempty"`  // need -count_frames to be populated
	CodedHeight        int                            `json:"coded_height,omitempty"` // need -count_frames to be populated
	ClosedCaptions     int                            `json:"closed_captions,omitempty"`
	FilmGrain          int                            `json:"film_grain,omitempty"`
	HasBFrames         int                            `json:"has_b_frames,omitempty"`
	SampleAspectRatio  string                         `json:"sample_aspect_ratio,omitempty"`
	DisplayAspectRatio string                         `json:"display_aspect_ratio,omitempty"`
	PixFmt             string                         `json:"pix_fmt,omitempty"`
	Level              int                            `json:"level,omitempty"`
	ColorRange         string                         `json:"color_range,omitempty"`
	ColorSpace         string                         `json:"color_space,omitempty"`
	ColorTransfer      string                         `json:"color_transfer,omitempty"`
	ColorPrimaries     string                         `json:"color_primaries,omitempty"`
	ChromaLocation     string                         `json:"chroma_location,omitempty"`
	FieldOrder         string                         `json:"field_order,omitempty"`
	Refs               int                            `json:"refs,omitempty"`
	RFrameRate         string                         `json:"r_frame_rate"`
	AvgFrameRate       string                         `json:"avg_frame_rate"`
	TimeBase           string                         `json:"time_base"`
	StartPts           int                            `json:"start_pts"`
	StartTime          string                         `json:"start_time"`
	BitRate            string                         `json:"bit_rate,omitempty"`
	MaxBitRate         string                         `json:"max_bit_rate,omitempty"` // need -count_frames to appear (only on video stream)
	NbFrames           int                            `json:"nb_frames"`
	NbReadFrames       int                            `json:"-"` // need -count_frames to appear
	ExtradataSize      int                            `json:"extradata_size,omitempty"`
	Disposition        FFProbeBinaryStreamDisposition `json:"disposition"`
	Tags               map[string]string              `json:"tags"`
	SideDataList       []FFProbeBinaryDataType        `json:"side_data_list,omitempty"`
	SampleFmt          string                         `json:"sample_fmt,omitempty"`
	SampleRate         string                         `json:"sample_rate,omitempty"`
	Channels           int                            `json:"channels,omitempty"`
	ChannelLayout      string                         `json:"channel_layout,omitempty"`
	BitsPerSample      int                            `json:"bits_per_sample,omitempty"`
	DmixMode           string                         `json:"dmix_mode,omitempty"`
	LtrtCmixlev        string                         `json:"ltrt_cmixlev,omitempty"`
	LtrtSurmixlev      string                         `json:"ltrt_surmixlev,omitempty"`
	LoroCmixlev        string                         `json:"loro_cmixlev,omitempty"`
	LoroSurmixlev      string                         `json:"loro_surmixlev,omitempty"`
	DurationTs         int                            `json:"duration_ts,omitempty"`
	Duration           string                         `json:"duration,omitempty"`
	InitialPadding     int                            `json:"initial_padding,omitempty"`
}

// UnmarshalJSON implements json.Unmarshaler to parse frame counts as integers.
func (ffpbs *FFProbeBinaryStream) UnmarshalJSON(data []byte) (err error) {
	type Mask FFProbeBinaryStream
	tmp := struct {
		*Mask
		NbFrames     string `json:"nb_frames"`
		NbReadFrames string `json:"nb_read_frames"` // need -count_frames to appear
	}{
		Mask: (*Mask)(ffpbs),
	}
	if err = json.Unmarshal(data, &tmp); err != nil {
		return
	}
	if tmp.NbFrames != "" {
		if ffpbs.NbFrames, err = strconv.Atoi(tmp.NbFrames); err != nil {
			err = fmt.Errorf("failed to parse nb_frames: %w", err)
			return
		}
	}
	if tmp.NbReadFrames != "" {
		if ffpbs.NbReadFrames, err = strconv.Atoi(tmp.NbReadFrames); err != nil {
			err = fmt.Errorf("failed to parse nb_read_frames: %w", err)
			return
		}
	}
	return
}

// IsInterlaced reports whether the stream uses interlaced frames.
func (s *FFProbeBinaryStream) IsInterlaced() bool {
	return s.FieldOrder != "progressive" && s.FieldOrder != "unknown"
}

// parseFrameRate converts an ffprobe frame-rate string into a float64.
//
// ffprobe reports the same rate in multiple formats depending on the container
// and encoder (e.g. "24000/1001", "23.976024", "30/1", or "30"). String
// comparison therefore rejects legitimate CFR content. This helper normalises
// both rational and decimal forms so callers can compare numerically.
func parseFrameRate(s string) (float64, error) {
	if s == "" {
		return 0, errors.New("empty frame rate")
	}
	if strings.Contains(s, "/") {
		parts := strings.SplitN(s, "/", 2)
		num, err := strconv.ParseFloat(parts[0], 64)
		if err != nil {
			return 0, fmt.Errorf("invalid frame rate numerator %q: %w", parts[0], err)
		}
		den, err := strconv.ParseFloat(parts[1], 64)
		if err != nil {
			return 0, fmt.Errorf("invalid frame rate denominator %q: %w", parts[1], err)
		}
		if den == 0 {
			return 0, errors.New("frame rate denominator is zero")
		}
		return num / den, nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid frame rate %q: %w", s, err)
	}
	return v, nil
}

// IsConstantFrameRate reports whether the stream appears to have a constant frame rate.
//
// It compares r_frame_rate and avg_frame_rate from ffprobe. Both values are
// metadata-level estimates (not ground-truth measurements), so the check is
// intentionally conservative: unparseable or missing values are treated as VFR.
//
// The old implementation used string equality, which produced false rejections
// when the same rate was expressed differently (e.g. "24000/1001" vs "23.976024").
// We now parse both fields as float64 and allow a small tolerance.
//
// Epsilon choice: the smallest gap between common *different* standard rates is
// ~0.024 fps (24 vs 23.976). 1e-3 is 24× smaller than that gap, so it cannot
// conflate two genuine standards, while being ~40 000× larger than the float
// representation noise we are trying to absorb.
func (s *FFProbeBinaryStream) IsConstantFrameRate() bool {
	r, err := parseFrameRate(s.RFrameRate)
	if err != nil {
		// Fail-safe: if we cannot parse the declared rate, assume VFR.
		return false
	}
	avg, err := parseFrameRate(s.AvgFrameRate)
	if err != nil {
		return false
	}
	const epsilon = 1e-3
	return math.Abs(r-avg) <= epsilon
}

// FFProbeBinaryStreamDisposition describes the role and properties of a stream.
type FFProbeBinaryStreamDisposition struct {
	Default         int `json:"default"`
	Dub             int `json:"dub"`
	Original        int `json:"original"`
	Comment         int `json:"comment"`
	Lyrics          int `json:"lyrics"`
	Karaoke         int `json:"karaoke"`
	Forced          int `json:"forced"`
	HearingImpaired int `json:"hearing_impaired"`
	VisualImpaired  int `json:"visual_impaired"`
	CleanEffects    int `json:"clean_effects"`
	AttachedPic     int `json:"attached_pic"`
	TimedThumbnails int `json:"timed_thumbnails"`
	NonDiegetic     int `json:"non_diegetic"`
	Captions        int `json:"captions"`
	Descriptions    int `json:"descriptions"`
	Metadata        int `json:"metadata"`
	Dependent       int `json:"dependent"`
	StillImage      int `json:"still_image"`
}

// FFProbeBinaryDataType carries side-data information for a stream.
type FFProbeBinaryDataType struct {
	SideDataType string `json:"side_data_type"`
	MaxBitrate   int    `json:"max_bitrate"`
	MinBitrate   int    `json:"min_bitrate"`
	AvgBitrate   int    `json:"avg_bitrate"`
	BufferSize   int    `json:"buffer_size"`
	VbvDelay     int    `json:"vbv_delay"`
}
