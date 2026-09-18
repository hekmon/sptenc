package core

import "time"

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
type Scene struct {
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
