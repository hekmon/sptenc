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
			frameRate, whyNot := concatSnapFrameRate(paths, tt.stats)
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
