package ffmpeg

import (
	"strings"
	"testing"
	"time"
)

// Real (shortened) ffprobe n9.0.2 report. The first frame comes with a side data list nobody
// asked for, positions are not strictly increasing and one frame has neither position nor time.
// Times are the ones of a 24 fps Matroska file: rounded to the millisecond, 41 or 42 ms apart.
const probeWithFramesReport = `{
    "frames": [
        { "pkt_pos": "715", "pts_time": "0.042000",
            "side_data_list": [
                {  }
            ] },
        { "pkt_pos": "8251", "pts_time": "0.083000" },
        { "pkt_pos": "14418", "pts_time": "0.125000" },
        { "pkt_pos": "11112", "pts_time": "0.167000" },
        { "pkt_pos": "N/A", "pts_time": "N/A" },
        { "pkt_pos": "17334", "pts_time": "0.250000" },
        { "pkt_pos": "20256", "pts_time": "0.292000" }
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
	expectedReports := []int{715, 7536, 6167, 2916, 2922}
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
	// Frame durations: 41, 42, 42 then 42 ms. The frame without a time breaks the chain: the
	// 83 ms between the frames around it are not a duration.
	if video.NbFrameDurations != 4 || video.ShortestFrameDuration != 41*time.Millisecond || video.LongestFrameDuration != 42*time.Millisecond {
		t.Errorf("unexpected frame durations: %d measured, from %v to %v",
			video.NbFrameDurations, video.ShortestFrameDuration, video.LongestFrameDuration)
	}
	if !video.IsConstantFrameRate() {
		t.Error("a 24 fps Matroska stream must be seen as a constant frame rate one")
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

// Measured frame durations must prevail over the declared frame rates, which can not be trusted
// with every container (see IsConstantFrameRate).
func TestIsConstantFrameRate_Measured(t *testing.T) {
	ms := time.Millisecond
	for name, tc := range map[string]struct {
		r, avg            string
		nb                int
		shortest, longest time.Duration
		expected          bool
	}{
		"exact durations (MP4, MPEG-TS)":             {"24000/1001", "24000/1001", 1438, 41708 * time.Microsecond, 41709 * time.Microsecond, true},
		"rounded to the millisecond (Matroska)":      {"24000/1001", "24000/1001", 1438, 41 * ms, 42 * ms, true},
		"120 fps rounded to the millisecond":         {"120/1", "120/1", 239, 8 * ms, 9 * ms, true},
		"24 and 30 fps mix declared as 24 fps (MKV)": {"24/1", "24/1", 161, 33 * ms, 42 * ms, false},
		"24 and 25 fps mix declared as 24 fps (MKV)": {"24/1", "24/1", 161, 40 * ms, 42 * ms, false},
		"constant with a dropped frame":              {"24/1", "24/1", 94, 41 * ms, 83 * ms, false},
		"same timestamp twice":                       {"24/1", "24/1", 94, 0, 42 * ms, false},
		"constant but declared rates disagree (MP4)": {"120/1", "27/1", 161, 41708 * time.Microsecond, 41709 * time.Microsecond, true},
		"nothing measured: declared rates, constant": {"24/1", "24/1", 0, 0, 0, true},
		"nothing measured: declared rates, variable": {"120/1", "27/1", 0, 0, 0, false},
	} {
		t.Run(name, func(t *testing.T) {
			stream := &FFProbeBinaryStream{RFrameRate: tc.r, AvgFrameRate: tc.avg,
				NbFrameDurations: tc.nb, ShortestFrameDuration: tc.shortest, LongestFrameDuration: tc.longest}
			if got := stream.IsConstantFrameRate(); got != tc.expected {
				t.Errorf("expected %t, got %t", tc.expected, got)
			}
		})
	}
}
