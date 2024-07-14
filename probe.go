package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/hekmon/liveprogress/v2"
)

func isVideoFile(inputFile string) bool {
	stats, err := getStreamsInfos(inputFile)
	if err != nil {
		fmt.Fprintf(liveprogress.Bypass(), "Verifying streams from %q failed: %s\n", inputFile, err)
		return false
	}
	fmt.Fprintf(liveprogress.Bypass(), "Video stream found in %q\n", inputFile)
	return stats.VideoTrack() != nil
}

func getStreamsInfos(inputFile string) (stats FFProbeStats, err error) {
	// Build up args
	args := []string{"-loglevel", "error", "-print_format", "json", "-show_format", "-show_streams"}
	args = append(args, inputFile)
	// Prepare command
	bypass := liveprogress.Bypass()
	if *debug {
		fmt.Fprintf(bypass, "Extract input file metadata: %s \"%s\"\n", FFProbe, strings.Join(args, "\" \""))
	}
	cmd := exec.Command(FFProbe, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	// Exec program
	if err = cmd.Start(); err != nil {
		err = fmt.Errorf("error starting %s: %w", FFProbe, err)
		return
	}
	children.Add(cmd.Process)
	defer children.Remove(cmd.Process)
	// if err = processpriority.Set(cmd.Process.Pid, processpriority.BelowNormal); err != nil {
	// 	fmt.Fprintf(bypass, "Failed to lower probbing process priority: %s\n", err)
	// }
	if err = cmd.Wait(); err != nil {
		err = fmt.Errorf("error during %s execution: %w\n%s", FFProbe, err, stderr.String())
		return
	}
	children.Remove(cmd.Process) // remove early (do not wait for defer safety)
	// Extract video infos
	if err = json.Unmarshal(stdout.Bytes(), &stats); err != nil {
		err = fmt.Errorf("error parsing %s output: %w", FFProbe, err)
		return
	}
	// Extract video track infos
	videoTrack := stats.VideoTrack()
	if videoTrack == nil {
		err = errors.New("no video track found in input file")
		return
	}
	return
}

type FFProbeStats struct {
	Format  *FFProbeFormat   `json:"format"`
	Streams []*FFProbeStream `json:"streams"`
}

func (stats *FFProbeStats) VideoTrack() (videoTrack *FFProbeStream) {
	for _, stream := range stats.Streams {
		if stream.CodecType == "video" {
			videoTrack = stream
			break
		}
	}
	return
}

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
	seconds, err := strconv.ParseFloat(tmp.Duration, 64)
	if err != nil {
		return fmt.Errorf("failed to parse duration %q as float: %w", tmp.Duration, err)
	}
	format.Duration = time.Duration(seconds * float64(time.Second))
	return nil
}

type FormatName string

const (
	FormatMatroska  FormatName = "matroska,webm"           // LongName: Matroska / WebM
	FormatQuickTime FormatName = "mov,mp4,m4a,3gp,3g2,mj2" // LongName: QuickTime / MOV
	FormatAVI       FormatName = "avi"                     // LongName: AVI (Audio Video Interleaved)
)

type FFProbeStream struct {
	Index              int                      `json:"index"`
	CodecName          string                   `json:"codec_name"`
	CodecLongName      string                   `json:"codec_long_name"`
	Profile            string                   `json:"profile,omitempty"`
	CodecType          string                   `json:"codec_type"`
	CodecTagString     string                   `json:"codec_tag_string"`
	CodecTag           string                   `json:"codec_tag"`
	Width              int                      `json:"width,omitempty"`
	Height             int                      `json:"height,omitempty"`
	CodedWidth         int                      `json:"coded_width,omitempty"`  // need -count_frames to be populated
	CodedHeight        int                      `json:"coded_height,omitempty"` // need -count_frames to be populated
	ClosedCaptions     int                      `json:"closed_captions,omitempty"`
	FilmGrain          int                      `json:"film_grain,omitempty"`
	HasBFrames         int                      `json:"has_b_frames,omitempty"`
	SampleAspectRatio  string                   `json:"sample_aspect_ratio,omitempty"`
	DisplayAspectRatio string                   `json:"display_aspect_ratio,omitempty"`
	PixFmt             string                   `json:"pix_fmt,omitempty"`
	Level              int                      `json:"level,omitempty"`
	ColorRange         string                   `json:"color_range,omitempty"`
	ColorSpace         string                   `json:"color_space,omitempty"`
	ColorTransfer      string                   `json:"color_transfer,omitempty"`
	ColorPrimaries     string                   `json:"color_primaries,omitempty"`
	ChromaLocation     string                   `json:"chroma_location,omitempty"`
	FieldOrder         string                   `json:"field_order,omitempty"`
	Refs               int                      `json:"refs,omitempty"`
	RFrameRate         string                   `json:"r_frame_rate"`
	AvgFrameRate       string                   `json:"avg_frame_rate"`
	TimeBase           string                   `json:"time_base"`
	StartPts           int                      `json:"start_pts"`
	StartTime          string                   `json:"start_time"`
	BitRate            string                   `json:"bit_rate,omitempty"`
	MaxBitRate         string                   `json:"max_bit_rate,omitempty"` // need -count_frames to appear (only on video stream)
	NbReadFrames       string                   `json:"nb_read_frames"`         // need -count_frames to appear
	ExtradataSize      int                      `json:"extradata_size,omitempty"`
	Disposition        FFProbeStreamDisposition `json:"disposition"`
	Tags               map[string]string        `json:"tags"`
	SideDataList       []FFProbeDataType        `json:"side_data_list,omitempty"`
	SampleFmt          string                   `json:"sample_fmt,omitempty"`
	SampleRate         string                   `json:"sample_rate,omitempty"`
	Channels           int                      `json:"channels,omitempty"`
	ChannelLayout      string                   `json:"channel_layout,omitempty"`
	BitsPerSample      int                      `json:"bits_per_sample,omitempty"`
	DmixMode           string                   `json:"dmix_mode,omitempty"`
	LtrtCmixlev        string                   `json:"ltrt_cmixlev,omitempty"`
	LtrtSurmixlev      string                   `json:"ltrt_surmixlev,omitempty"`
	LoroCmixlev        string                   `json:"loro_cmixlev,omitempty"`
	LoroSurmixlev      string                   `json:"loro_surmixlev,omitempty"`
	DurationTs         int                      `json:"duration_ts,omitempty"`
	Duration           string                   `json:"duration,omitempty"`
	InitialPadding     int                      `json:"initial_padding,omitempty"`
}

func (s *FFProbeStream) IsInterlaced() bool {
	return s.FieldOrder != "progressive" && s.FieldOrder != "unknown"
}

type FFProbeStreamDisposition struct {
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

type FFProbeDataType struct {
	SideDataType string `json:"side_data_type"`
	MaxBitrate   int    `json:"max_bitrate"`
	MinBitrate   int    `json:"min_bitrate"`
	AvgBitrate   int    `json:"avg_bitrate"`
	BufferSize   int    `json:"buffer_size"`
	VbvDelay     int    `json:"vbv_delay"`
}
