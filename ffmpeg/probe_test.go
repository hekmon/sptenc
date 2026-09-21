package ffmpeg

import (
	"strings"
	"testing"
	"time"
)

// Real (shortened) ffprobe n9.0.2 report. The first frame comes with a side data list nobody
// asked for, positions are not strictly increasing and one is not available.
const probeWithFramesReport = `{
    "frames": [
        { "pkt_pos": "715",
            "side_data_list": [
                {  }
            ] },
        { "pkt_pos": "8251" },
        { "pkt_pos": "14418" },
        { "pkt_pos": "11112" },
        { "pkt_pos": "N/A" },
        { "pkt_pos": "17334" }
    ],
    "programs": [
    ],
    "streams": [
        { "index": 0, "codec_name": "h264", "codec_type": "video", "width": 160, "height": 90, "r_frame_rate": "24000/1001", "avg_frame_rate": "24000/1001", "nb_frames": "1439", "nb_read_frames": "1439", "disposition": { "default": 1 } }
    ],
    "format": { "filename": "src.mp4", "nb_streams": 2, "format_name": "mov,mp4,m4a,3gp,3g2,mj2", "duration": "60.059000", "size": "4645425" }
}`

func TestParseProbeWithFrames(t *testing.T) {
	var reports []int
	stats, err := parseProbeWithFrames(strings.NewReader(probeWithFramesReport), func(n int) {
		reports = append(reports, n)
	})
	if err != nil {
		t.Fatalf("parseProbeWithFrames failed: %v", err)
	}
	// Progress: only a new highest position counts, and the sum is the highest position
	expectedReports := []int{715, 7536, 6167, 2916}
	if len(reports) != len(expectedReports) {
		t.Fatalf("expected reports %v, got %v", expectedReports, reports)
	}
	for i := range expectedReports {
		if reports[i] != expectedReports[i] {
			t.Errorf("report %d: expected %d, got %d", i, expectedReports[i], reports[i])
		}
	}
	// The usual sections must be parsed as they are without the frames
	video := stats.VideoTrack()
	if video == nil {
		t.Fatal("expected a video track")
	}
	if video.NbReadFrames != 1439 || video.CodecName != CodecVideoAVC || video.RFrameRate != "24000/1001" {
		t.Errorf("unexpected video track: %+v", video)
	}
	if stats.Format == nil || stats.Format.Duration != 60059*time.Millisecond {
		t.Errorf("unexpected format: %+v", stats.Format)
	}
	// No progress callback must not be an issue
	if _, err = parseProbeWithFrames(strings.NewReader(probeWithFramesReport), nil); err != nil {
		t.Errorf("parseProbeWithFrames failed without progress callback: %v", err)
	}
}

func TestParseProbeWithFrames_Invalid(t *testing.T) {
	for name, report := range map[string]string{
		"empty":     ``,
		"truncated": `{ "frames": [ { "pkt_pos": "715" }, `,
		"not json":  `[mov,mp4 @ 0x55] stream 1, offset 0x30: partial file`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseProbeWithFrames(strings.NewReader(report), nil); err == nil {
				t.Error("expected an error, got nil")
			}
		})
	}
}

// TestIsConstantFrameRate verifies the numeric CFR heuristic.
//
// The test matrix is designed around two risks:
//  1. False rejection — legitimate CFR files where ffprobe expresses the same
//     rate differently (rational vs. decimal). These MUST return true.
//  2. False acceptance — genuinely different rates or corrupt metadata.
//     These MUST return false.
//
// We do NOT test pathological VFR that happens to average to a standard rate
// within epsilon; that is a limitation of the metadata heuristic itself, not
// of the parser.
func TestIsConstantFrameRate(t *testing.T) {
	tests := []struct {
		name    string
		r       string // r_frame_rate from ffprobe
		avg     string // avg_frame_rate from ffprobe
		wantCFR bool   // expected result: true if we should treat as CFR
	}{
		// Case 1: identical rationals — the easy path.
		{"identical rational", "24000/1001", "24000/1001", true},

		// Case 2: rational vs. float representation of the same rate.
		// This is the exact bug that motivated the numeric comparison.
		{"rational vs float representation", "24000/1001", "23.976024", true},

		// Case 3: integer written as rational vs. bare integer.
		{"integer vs rational", "30/1", "30", true},

		// Case 4: identical decimal strings.
		{"identical floats", "23.976024", "23.976024", true},

		// Case 5: genuinely different standards (30 fps vs. 25 fps).
		{"different standards", "30/1", "25/1", false},

		// Case 6: NTSC (~29.97) vs. integer 30 — close but not equal.
		{"NTSC vs integer", "30/1", "29.970030", false},

		// Case 7-10: malformed or missing metadata must fail safe (return false).
		{"empty r_frame_rate", "", "30/1", false},
		{"empty avg_frame_rate", "30/1", "", false},
		{"invalid r_frame_rate", "invalid", "30/1", false},
		{"invalid avg_frame_rate", "30/1", "invalid", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &FFProbeBinaryStream{
				RFrameRate:   tt.r,
				AvgFrameRate: tt.avg,
			}
			if got := s.IsConstantFrameRate(); got != tt.wantCFR {
				t.Errorf("IsConstantFrameRate() = %v, want %v", got, tt.wantCFR)
			}
		})
	}
}

func TestIsInterlaced(t *testing.T) {
	for fieldOrder, expected := range map[string]bool{
		"progressive": false,
		"unknown":     false, // ffprobe could not tell
		"":            false, // not a (probed) video stream
		"tt":          true,
		"bb":          true,
		"tb":          true,
		"bt":          true,
	} {
		if got := (&FFProbeBinaryStream{FieldOrder: fieldOrder}).IsInterlaced(); got != expected {
			t.Errorf("field order %q: expected %t, got %t", fieldOrder, expected, got)
		}
	}
}
