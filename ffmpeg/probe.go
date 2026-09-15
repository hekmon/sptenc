package ffmpeg

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
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

// GetStreamsInfosCF runs ffprobe with -count_frames to extract complete stream metadata,
// including per-frame counts, reading the file through the provided path.
func GetStreamsInfosCF(ctx context.Context, config GetStreamsInfosCFConfig) (stats FFProbeStats, err error) {
	// Validate inputs
	if config.Path == "" {
		err = errors.New("input path cannot be empty")
		return
	}
	// Build up args
	args := []string{
		"-loglevel", "error", "-print_format", "json",
		"-show_format", "-show_streams", "-count_frames",
		"-threads", "auto",
		"-", // stdin
	}
	// Prepare command
	if config.Debug != nil {
		// lets print a usable command line
		usableCMDLine := make([]string, len(args))
		copy(usableCMDLine, args)
		usableCMDLine[len(usableCMDLine)-1] = config.Path
		config.Debug(fmt.Sprintf("Extract input file complete metadata: %s", getPrintableCMDLine(FFProbeBinary, usableCMDLine)))
	}
	// Open file ourself to keep track of bytes read to generate progress
	file, err := os.Open(config.Path)
	if err != nil {
		err = fmt.Errorf("Failed to open %q: %w", config.Path, err)
		return
	}
	defer file.Close()
	readerWrapper := &readerCounter{
		wrapped: file,
		updater: config.ReadBytesReport,
	}
	// Exec FFProbeBinary
	cmd := exec.CommandContext(ctx, FFProbeBinary, args...)
	cmd.Stdin = readerWrapper
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	// Exec program
	if err = cmd.Start(); err != nil {
		err = fmt.Errorf("error starting %s: %w\n%s", FFProbeBinary, err, getPrintableCMDLine(FFProbeBinary, args))
		return
	}
	if err = processpriority.Set(cmd.Process.Pid, ProcessPriority); err != nil && config.RuntimeError != nil {
		config.RuntimeError(fmt.Errorf("Failed to lower %s process priority: %w", FFProbeBinary, err))
	}
	if err = cmd.Wait(); err != nil {
		err = fmt.Errorf("error during %s execution: %w\n%s\n%s\n%s", FFProbeBinary, err, stdout.String(), stderr.String(), getPrintableCMDLine(FFProbeBinary, args))
		return
	}
	// Extract video infos
	if err = json.Unmarshal(stdout.Bytes(), &stats); err != nil {
		err = fmt.Errorf("error parsing %s output: %w", FFProbeBinary, err)
		return
	}
	return
}

type readerCounter struct {
	wrapped io.Reader
	updater func(bytesRead int)
}

func (rc *readerCounter) Read(p []byte) (n int, err error) {
	if rc.wrapped == nil {
		err = io.EOF
		return
	}
	n, err = rc.wrapped.Read(p)
	if rc.updater != nil {
		rc.updater(n)
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

// IsConstantFrameRate reports whether the stream appears to have a constant frame rate.
// It compares r_frame_rate and avg_frame_rate from ffprobe. If they differ, the stream
// is likely variable frame rate (VFR), which can cause frame misalignment in VMAF comparisons.
func (s *FFProbeBinaryStream) IsConstantFrameRate() bool {
	return s.RFrameRate == s.AvgFrameRate
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
