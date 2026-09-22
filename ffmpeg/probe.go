package ffmpeg

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// GetStreamsInfosConfig holds the configuration for GetStreamsInfos.
type GetStreamsInfosConfig struct {
	// Input
	Path string
	// CountPackets reads every packet of the file to fill NbReadPackets on each stream
	// (no decoding, the whole file is read).
	CountPackets bool
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
	if config.CountPackets {
		args = append(args, "-count_packets")
	}
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
	Index              int       `json:"index"`
	CodecName          CodecName `json:"codec_name"`
	CodecLongName      string    `json:"codec_long_name"`
	Profile            string    `json:"profile,omitempty"`
	CodecType          string    `json:"codec_type"`
	CodecTagString     string    `json:"codec_tag_string"`
	CodecTag           string    `json:"codec_tag"`
	Width              int       `json:"width,omitempty"`
	Height             int       `json:"height,omitempty"`
	CodedWidth         int       `json:"coded_width,omitempty"`  // need -count_frames to be populated
	CodedHeight        int       `json:"coded_height,omitempty"` // need -count_frames to be populated
	ClosedCaptions     int       `json:"closed_captions,omitempty"`
	FilmGrain          int       `json:"film_grain,omitempty"`
	HasBFrames         int       `json:"has_b_frames,omitempty"`
	SampleAspectRatio  string    `json:"sample_aspect_ratio,omitempty"`
	DisplayAspectRatio string    `json:"display_aspect_ratio,omitempty"`
	PixFmt             string    `json:"pix_fmt,omitempty"`
	Level              int       `json:"level,omitempty"`
	ColorRange         string    `json:"color_range,omitempty"`
	ColorSpace         string    `json:"color_space,omitempty"`
	ColorTransfer      string    `json:"color_transfer,omitempty"`
	ColorPrimaries     string    `json:"color_primaries,omitempty"`
	ChromaLocation     string    `json:"chroma_location,omitempty"`
	FieldOrder         string    `json:"field_order,omitempty"`
	Refs               int       `json:"refs,omitempty"`
	RFrameRate         string    `json:"r_frame_rate"`
	AvgFrameRate       string    `json:"avg_frame_rate"`
	TimeBase           string    `json:"time_base"`
	StartPts           int       `json:"start_pts"`
	StartTime          string    `json:"start_time"`
	BitRate            string    `json:"bit_rate,omitempty"`
	MaxBitRate         string    `json:"max_bit_rate,omitempty"` // need -count_frames to appear (only on video stream)
	NbFrames           int       `json:"nb_frames"`
	NbReadPackets      int       `json:"-"` // need -count_packets (GetStreamsInfosConfig.CountPackets) to be populated
	// Measured by CountFrames and set by SetReadFrames (not part of the ffprobe report), see IsConstantFrameRate
	NbReadFrames          int                            `json:"-"` // exact number of frames
	NbFrameDurations      int                            `json:"-"` // number of durations measured (time between two consecutive frames)
	ShortestFrameDuration time.Duration                  `json:"-"`
	LongestFrameDuration  time.Duration                  `json:"-"`
	ExtradataSize         int                            `json:"extradata_size,omitempty"`
	Disposition           FFProbeBinaryStreamDisposition `json:"disposition"`
	Tags                  map[string]string              `json:"tags"`
	SideDataList          []FFProbeBinaryDataType        `json:"side_data_list,omitempty"`
	SampleFmt             string                         `json:"sample_fmt,omitempty"`
	SampleRate            string                         `json:"sample_rate,omitempty"`
	Channels              int                            `json:"channels,omitempty"`
	ChannelLayout         string                         `json:"channel_layout,omitempty"`
	BitsPerSample         int                            `json:"bits_per_sample,omitempty"`
	DmixMode              string                         `json:"dmix_mode,omitempty"`
	LtrtCmixlev           string                         `json:"ltrt_cmixlev,omitempty"`
	LtrtSurmixlev         string                         `json:"ltrt_surmixlev,omitempty"`
	LoroCmixlev           string                         `json:"loro_cmixlev,omitempty"`
	LoroSurmixlev         string                         `json:"loro_surmixlev,omitempty"`
	DurationTs            int                            `json:"duration_ts,omitempty"`
	Duration              string                         `json:"duration,omitempty"`
	InitialPadding        int                            `json:"initial_padding,omitempty"`
}

// UnmarshalJSON implements json.Unmarshaler to parse frame counts as integers.
func (ffpbs *FFProbeBinaryStream) UnmarshalJSON(data []byte) (err error) {
	type Mask FFProbeBinaryStream
	tmp := struct {
		*Mask
		NbFrames      string `json:"nb_frames"`
		NbReadPackets string `json:"nb_read_packets"`
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
	if tmp.NbReadPackets != "" {
		if ffpbs.NbReadPackets, err = strconv.Atoi(tmp.NbReadPackets); err != nil {
			err = fmt.Errorf("failed to parse nb_read_packets: %w", err)
			return
		}
	}
	return
}

// SetReadFrames stores on the stream what CountFrames measured by decoding it: its exact number
// of frames and how long they last, which IsConstantFrameRate relies on from then on.
func (ffpbs *FFProbeBinaryStream) SetReadFrames(frames ReadFrames) {
	ffpbs.NbReadFrames = frames.Nb
	ffpbs.NbFrameDurations = frames.NbDurations
	ffpbs.ShortestFrameDuration = frames.ShortestDuration
	ffpbs.LongestFrameDuration = frames.LongestDuration
}

// IsInterlaced reports whether the stream is declared as interlaced: a field order which is
// neither "progressive" nor "unknown". It reads what ffprobe reports for the stream, no frame
// is analyzed: it has been checked against interlaced and progressive samples (H.264, MPEG-2
// and FFV1 interlaced, top and bottom field first; H.264, HEVC, AV1, VP9, MPEG-2 and FFV1
// progressive; Matroska, MP4, MPEG-TS and VOB), the stream value always matched the frames.
//
// # EDGE CASES
//
//   - "unknown" is not interlaced: ffprobe could not tell, and rejecting every file it can not
//     tell anything about would reject valid progressive ones.
//   - A missing value is not interlaced either: ffprobe always prints one for a video stream, so
//     this is not a video stream (or not a probed one).
//   - Progressive content stored in an interlaced stream (PsF) is reported as interlaced, as the
//     stream says so: only analyzing the pictures could tell, this is not done here.
func (s *FFProbeBinaryStream) IsInterlaced() bool {
	switch s.FieldOrder {
	case "progressive", "unknown", "":
		return false
	default:
		return true
	}
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

// FrameDurationTolerance is how much the time between two consecutive frames can vary within a
// constant frame rate stream, see IsConstantFrameRate.
const FrameDurationTolerance = time.Millisecond

// IsConstantFrameRate reports whether the stream has a constant frame rate. It relies on the
// frame durations measured by CountFrames when they have been set (SetReadFrames), on the frame
// rates declared by the container otherwise (no frame read).
//
// # WHY THE DECLARED FRAME RATES ARE NOT ENOUGH
//
// Comparing r_frame_rate with avg_frame_rate was the only check for a long time. It works
// when the container computes its average rate out of the frames (MP4: a file with 3 seconds
// at 24 fps then 3 seconds at 30 fps declares 120/1 and 27/1). Matroska does not: its average
// rate comes from a header field, the very same file remuxed to Matroska declares 24/1 for
// both and was accepted as constant. It is not harmless: the encode went fine, VMAF too (frames
// are aligned by their index), but the output had its 30 fps part retimed to 24 fps and
// duplicated timestamps, so a video not lasting what its audio does. Matroska is what sptenc is
// given most of the time, and mixing 24 and 30 fps is a classic of anime DVD sources.
//
// So frame durations are measured, while frames are being counted (no additional pass): a
// stream is constant when its shortest and longest durations are within FrameDurationTolerance.
//
// # WHY ONE MILLISECOND
//
// Durations of a constant frame rate stream are not all equal: Matroska rounds timestamps to
// the millisecond, 23.976 fps gives a mix of 41 and 42 ms. This rounding can not move a duration
// by more than 1 ms, and it is the coarsest of the usual containers: on constant frame rate
// samples (7 codecs, Matroska, MP4, MPEG-TS and VOB, from 23.976 to 120 fps, with B-frames,
// interlaced ones included) the gap between the shortest and the longest duration was 1 ms at
// most, against 9 ms for the 24/30 fps mix. A larger tolerance was rejected: at 2 ms, a mix of
// 24 and 25 fps (41.67 and 40 ms) goes through.
//
// # EDGE CASES
//
//   - A constant frame rate stream with a hole (a frame dropped by a capture device: one
//     duration twice as long as the others) is reported as variable. It is on purpose: nothing
//     here knows how to keep that hole, every frame after it would be shifted.
//   - Not enough durations measured (no CountFrames run, a single frame, a format without frame
//     timestamps such as AVI): the declared frame rates are compared, as before. Both values
//     are metadata-level estimates, so unparseable or missing ones are treated as variable.
//     They are compared as numbers, the same rate being written in different ways ("24000/1001"
//     and "23.976024"), with a tolerance 24 times smaller than the gap between the two closest
//     standard rates (24 and 23.976 fps).
func (s *FFProbeBinaryStream) IsConstantFrameRate() bool {
	if s.NbFrameDurations > 0 {
		return s.LongestFrameDuration-s.ShortestFrameDuration <= FrameDurationTolerance
	}
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
