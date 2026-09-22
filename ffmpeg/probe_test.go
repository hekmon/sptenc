package ffmpeg

import (
	"testing"
	"time"
)

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
