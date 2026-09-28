package main

import (
	"strings"
	"testing"

	"github.com/hekmon/sptenc/ffmpeg"
)

// TestVerdict checks the verdict of every case against what sptenc assumes of the encoder.
func TestVerdict(t *testing.T) {
	lone := encodeResult{size: 994014, pictures: "3e4d0500167d", frames: 270, keyframes: 2}
	extra := encodeResult{size: 1087591, pictures: "157d6f224a48", frames: 270, keyframes: 3}
	for _, tc := range []struct {
		name        string
		encoder     ffmpeg.Encoder
		alone       []encodeResult
		together    []encodeResult
		contradicts bool
		says        string
	}{
		{"VideoToolbox disturbed, as assumed", ffmpeg.HEVCEncoderVideoToolbox,
			[]encodeResult{lone, lone}, []encodeResult{lone, extra}, false, "1 of the 2 encodes"},
		{"VideoToolbox not disturbed", ffmpeg.HEVCEncoderVideoToolbox,
			[]encodeResult{lone, lone}, []encodeResult{lone, lone}, true, "does not need"},
		{"NVENC not disturbed, as assumed", ffmpeg.HEVCEncoderNVEnc,
			[]encodeResult{lone, lone}, []encodeResult{lone, lone}, false, "only changes the time taken"},
		{"NVENC disturbed", ffmpeg.HEVCEncoderNVEnc,
			[]encodeResult{lone, lone}, []encodeResult{extra, lone}, true, "does not expect"},
		{"lone encodes differing", ffmpeg.HEVCEncoderLibx265,
			[]encodeResult{lone, extra}, []encodeResult{lone, lone}, true, "not all the same"},
	} {
		summary, contradicts := verdict(tc.encoder, tc.alone, tc.together)
		if contradicts != tc.contradicts || !strings.Contains(summary, tc.says) {
			t.Errorf("%s: got %t, %q", tc.name, contradicts, summary)
		}
	}
}
