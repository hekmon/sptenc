package core

import (
	"fmt"
	"time"
)

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
	// Banding diagnostic: the CAMBI feature the model fused into the score (0 is no banding,
	// around 5 is where it starts to be slightly annoying, the models cap it at 17). Not a
	// threshold: it tells a segment losing points to banding from one losing them to compression.
	CAMBIMean float64
	CAMBIMax  float64
}

// String renders the VMAF statistics on a single line, for the debug logs of the QP search.
// Without it, formatting the struct with %s prints each float as %!s(float64=...).
// The table rendering lives with the ffmpeg type: core does not import ffmpeg.
func (vs VMAFStats) String() string {
	return fmt.Sprintf("min=%v p1=%v p5=%v p10=%v p25=%v median=%v hmean=%v mean=%v max=%v cambi_mean=%v cambi_max=%v (libvmaf %s)",
		vs.Minimum, vs.Percentile1, vs.Percentile5, vs.Percentile10, vs.Percentile25,
		vs.Median, vs.HarmonicMean, vs.Mean, vs.Maximum, vs.CAMBIMean, vs.CAMBIMax, vs.Version,
	)
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
