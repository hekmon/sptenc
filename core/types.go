package core

import (
	"fmt"
	"time"
)

// VMAFMeasures tells SegmentEncoder.ComputeVMAF what to measure in a pass.
type VMAFMeasures struct {
	Score   bool // The VMAF score the thresholds gate (see VMAFStats).
	Banding bool // The banding the encode added to its source (see BandingStats).
}

// VMAFStats is a user-friendly summary of VMAF results, including computed percentiles.
type VMAFStats struct {
	Version      string
	Minimum      float64
	Percentile1  float64
	Percentile5  float64
	Percentile10 float64
	Percentile25 float64
	Median       float64
	HarmonicMean float64
	Mean         float64
	Maximum      float64
}

// String renders the VMAF statistics on a single line, for the debug logs of the QP search.
// Without it, formatting the struct with %s prints each float as %!s(float64=...).
// The table rendering lives with the ffmpeg type: core does not import ffmpeg.
func (vs VMAFStats) String() string {
	return fmt.Sprintf("min=%v p1=%v p5=%v p10=%v p25=%v median=%v hmean=%v mean=%v max=%v (libvmaf %s)",
		vs.Minimum, vs.Percentile1, vs.Percentile5, vs.Percentile10, vs.Percentile25,
		vs.Median, vs.HarmonicMean, vs.Mean, vs.Maximum, vs.Version,
	)
}

// BandingStats is the banding the encode of a segment added to its source, over the frames of
// the segment. CAMBI rates the banding of a picture from 0 (none) up, "around 5 is where banding
// starts to become slightly annoying" (libvmaf's CAMBI documentation). It rates each frame of the
// encode and the same frame of the source: the encode adds what it rates above the source, none
// where it rates below (an encoder smooths the steps of a banded source).
type BandingStats struct {
	AddedMean  float64 // The banding the encode added, averaged over the frames.
	AddedMax   float64 // The banding the encode added to its worst frame.
	SourceMean float64 // The banding of the source, averaged over the frames.
	EncodeMean float64 // The banding of the encode, averaged over the frames.
}

// String renders the banding statistics on a single line, for the debug logs of the QP search.
func (bs BandingStats) String() string {
	return fmt.Sprintf("added_mean=%v added_max=%v source_mean=%v encode_mean=%v",
		bs.AddedMean, bs.AddedMax, bs.SourceMean, bs.EncodeMean)
}

// ProgressStats holds parsed progress information from an ffmpeg encode or extraction.
type ProgressStats struct {
	CurrentFrame int
	FPS          float64
	Bitrate      string
	TotalSize    int
	Time         time.Duration
	Dup          int
	Drop         int
	Speed        float64
}

// Scene represents a detected scene boundary with its start time and detection score.
// Frame is not used by core, which only reasons on durations and scores. It is carried along
// because it is what the video is cut at in the end (see ffmpeg.Scene): it must come out of
// the selection untouched.
type Scene struct {
	Frame int
	Start time.Duration
	Score float64
}

// VideoStream holds the video stream properties required by the QP search loop.
type VideoStream struct {
	NbFrames     int
	NbReadFrames int
	RFrameRate   string
	Height       int
	Duration     time.Duration
}
