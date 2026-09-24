package main

import (
	"strings"
	"testing"

	"github.com/hekmon/sptenc/ffmpeg"
)

// concatTestStats returns the probe of a file whose video is at frameRate and starts at
// videoStart, the file itself starting at fileStart ("" for no format section).
func concatTestStats(frameRate, videoStart, fileStart string) ffmpeg.FFProbeStats {
	stats := ffmpeg.FFProbeStats{
		Streams: []*ffmpeg.FFProbeBinaryStream{
			{CodecType: "audio", StartTime: fileStart},
			{CodecType: "video", RFrameRate: frameRate, StartTime: videoStart},
		},
	}
	if fileStart != "" {
		stats.Format = &ffmpeg.FFProbeFormat{StartTime: fileStart}
	}
	return stats
}

func TestConcatSnapFrameRate(t *testing.T) {
	tests := []struct {
		name          string
		stats         []ffmpeg.FFProbeStats
		original      string // frame rate of the original file, if given
		wantFrameRate string
		wantWhyNot    []string // parts of the reason, when no snap is expected
	}{
		{
			name: "same frame rate, video starting with each file",
			stats: []ffmpeg.FFProbeStats{
				concatTestStats("24000/1001", "0.000000", "0.000000"),
				concatTestStats("24000/1001", "0.000000", "0.000000"),
			},
			wantFrameRate: "24000/1001",
		},
		{
			name:          "single file",
			stats:         []ffmpeg.FFProbeStats{concatTestStats("25/1", "0.000000", "0.000000")},
			wantFrameRate: "25/1",
		},
		{
			name: "different frame rates",
			stats: []ffmpeg.FFProbeStats{
				concatTestStats("24000/1001", "0.000000", "0.000000"),
				concatTestStats("25/1", "0.000000", "0.000000"),
			},
			wantWhyNot: []string{"b.mkv", "24000/1001", "25/1"},
		},
		{
			name: "video starting after the audio",
			stats: []ffmpeg.FFProbeStats{
				concatTestStats("24000/1001", "0.000000", "0.000000"),
				concatTestStats("24000/1001", "0.022000", "0.000000"),
			},
			wantWhyNot: []string{"b.mkv", "0.022000", "0.000000"},
		},
		{
			name: "start times unknown",
			stats: []ffmpeg.FFProbeStats{
				concatTestStats("24000/1001", "N/A", "N/A"),
				concatTestStats("24000/1001", "N/A", "N/A"),
			},
			wantWhyNot: []string{"a.mkv", "N/A"},
		},
		{
			name: "no format section",
			stats: []ffmpeg.FFProbeStats{
				concatTestStats("24000/1001", "0.000000", ""),
				concatTestStats("24000/1001", "0.000000", ""),
			},
			wantWhyNot: []string{"a.mkv"},
		},
		{
			name: "original file at the exact rate of the files' Matroska approximation",
			stats: []ffmpeg.FFProbeStats{
				concatTestStats("19001/317", "0.000000", "0.000000"),
				concatTestStats("19001/317", "0.000000", "0.000000"),
			},
			original:      "60000/1001",
			wantFrameRate: "60000/1001",
		},
		{
			// the frame rates were checked against the original's while counting the frames
			name: "original file, files declaring its rate in two ways",
			stats: []ffmpeg.FFProbeStats{
				concatTestStats("60000/1001", "0.000000", "0.000000"),
				concatTestStats("19001/317", "0.000000", "0.000000"),
			},
			original:      "60000/1001",
			wantFrameRate: "60000/1001",
		},
		{
			name: "original file, video starting after the audio",
			stats: []ffmpeg.FFProbeStats{
				concatTestStats("19001/317", "0.000000", "0.000000"),
				concatTestStats("19001/317", "0.022000", "0.000000"),
			},
			original:   "60000/1001",
			wantWhyNot: []string{"b.mkv", "0.022000"},
		},
		{
			name: "no video stream",
			stats: []ffmpeg.FFProbeStats{
				concatTestStats("24000/1001", "0.000000", "0.000000"),
				{Format: &ffmpeg.FFProbeFormat{StartTime: "0.000000"}},
			},
			wantWhyNot: []string{"b.mkv", "no video"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			paths := []string{"/segments/a.mkv", "/segments/b.mkv"}[:len(tt.stats)]
			frameRate, whyNot := concatSnapFrameRate(paths, tt.stats, tt.original)
			if frameRate != tt.wantFrameRate {
				t.Errorf("frame rate %q, want %q (reason: %q)", frameRate, tt.wantFrameRate, whyNot)
			}
			if len(tt.wantWhyNot) == 0 {
				if whyNot != "" {
					t.Errorf("unexpected reason not to snap: %q", whyNot)
				}
				return
			}
			for _, part := range tt.wantWhyNot {
				if !strings.Contains(whyNot, part) {
					t.Errorf("reason %q does not mention %q", whyNot, part)
				}
			}
		})
	}
}

func TestMatroskaApproximatedRate(t *testing.T) {
	tests := []struct {
		frameRate string
		usualRate string
		found     bool
	}{
		// what Matroska gives back for 59.94, 119.88 and 47.952 fps
		{"19001/317", "60000/1001", true},
		{"29011/242", "120000/1001", true},
		{"7001/146", "48000/1001", true},
		// usual rates, exact in Matroska: nothing to tell
		{"24000/1001", "", false},
		{"30000/1001", "", false},
		{"25/1", "", false},
		{"60/1", "", false},
		{"31/1", "", false},
		// near no usual rate (23.98 fps: 2e-4 from 23.976, 8e-4 from 24)
		{"10000/417", "", false},
		{"abc", "", false},
		{"0/0", "", false},
	}
	for _, tt := range tests {
		usualRate, found := matroskaApproximatedRate(tt.frameRate)
		if found != tt.found || (found && usualRate != tt.usualRate) {
			t.Errorf("matroskaApproximatedRate(%q) = %q, %t, want %q, %t", tt.frameRate, usualRate, found, tt.usualRate, tt.found)
		}
	}
}
