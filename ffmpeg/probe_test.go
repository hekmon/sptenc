package ffmpeg

import "testing"

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
