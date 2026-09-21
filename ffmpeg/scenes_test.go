package ffmpeg

import (
	"io"
	"strings"
	"testing"
	"time"
)

// Real output of ffmpeg n9.0.2 (setpts, scdet then metadata filters), other lines included.
const scdetOutput = `Input #0, mov,mp4,m4a,3gp,3g2,mj2, from 'src.mp4':
  Stream #0:0[0x1](und): Video: h264 (High 4:4:4 Predictive), yuv420p, 160x90, 23.98 fps
[Parsed_scdet_1 @ 0x7eec58003240] lavfi.scd.score: 41.160, lavfi.scd.time: 4.045708
[Parsed_metadata_2 @ 0x7eec58003500] frame:97   pts:97097   pts_time:4.045708
[Parsed_metadata_2 @ 0x7eec58003500] lavfi.scd.time=4.045708
[Parsed_scdet_1 @ 0x7eec58003240] lavfi.scd.score: 40.408, lavfi.scd.time: 8.091417
[Parsed_metadata_2 @ 0x7eec58003500] frame:194  pts:194194  pts_time:8.091417
[Parsed_metadata_2 @ 0x7eec58003500] lavfi.scd.time=8.091417
[Parsed_scdet_1 @ 0x7eec58003240] lavfi.scd.score: 14.003, lavfi.scd.time: 1011.427083
[Parsed_metadata_2 @ 0x7eec58003500] frame:24250 pts:24274250 pts_time:1011.427083
[Parsed_metadata_2 @ 0x7eec58003500] lavfi.scd.time=1011.427083
[out#0/null @ 0x5581] video:618KiB audio:0KiB
`

func TestScdetProgress(t *testing.T) {
	scenes, err := scdetProgress(io.NopCloser(strings.NewReader(scdetOutput)), nil, func(err error) {
		t.Errorf("unexpected runtime error: %v", err)
	})
	if err != nil {
		t.Fatalf("scdetProgress failed: %v", err)
	}
	expected := []Scene{
		{Frame: 97, Start: 4046 * time.Millisecond, Score: 41.16},
		{Frame: 194, Start: 8091 * time.Millisecond, Score: 40.408},
		{Frame: 24250, Start: 1011427 * time.Millisecond, Score: 14.003}, // no space left after the frame index
	}
	if len(scenes) != len(expected) {
		t.Fatalf("expected %d scenes, got %v", len(expected), scenes)
	}
	for i := range expected {
		if scenes[i] != expected[i] {
			t.Errorf("scene %d: expected %+v, got %+v", i, expected[i], scenes[i])
		}
	}
}

// A scene which can not get its frame index must fail the detection: it can not be cut, and
// dropping it would silently change the scenes (see scdetProgress).
func TestScdetProgress_MissingFrameIndex(t *testing.T) {
	for name, output := range map[string]string{
		"followed by another scene": "[Parsed_scdet_1 @ 0x1] lavfi.scd.score: 41.160, lavfi.scd.time: 4.045708\n" +
			"[Parsed_scdet_1 @ 0x1] lavfi.scd.score: 40.408, lavfi.scd.time: 8.091417\n" +
			"[Parsed_metadata_2 @ 0x2] frame:194  pts:194194  pts_time:8.091417\n",
		"last line": "[Parsed_scdet_1 @ 0x1] lavfi.scd.score: 41.160, lavfi.scd.time: 4.045708\n",
		"unparsable": "[Parsed_scdet_1 @ 0x1] lavfi.scd.score: 41.160, lavfi.scd.time: 4.045708\n" +
			"[Parsed_metadata_2 @ 0x2] frame:abc  pts:194194  pts_time:8.091417\n",
		"frame 0": "[Parsed_scdet_1 @ 0x1] lavfi.scd.score: 41.160, lavfi.scd.time: 0\n" +
			"[Parsed_metadata_2 @ 0x2] frame:0    pts:0       pts_time:0\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := scdetProgress(io.NopCloser(strings.NewReader(output)), nil, nil); err == nil {
				t.Error("expected an error, got nil")
			}
		})
	}
}

func TestSegment_InvalidFrames(t *testing.T) {
	for name, frames := range map[string][]int{
		"missing frame index": {0},
		"not increasing":      {97, 97},
		"decreasing":          {194, 97},
	} {
		t.Run(name, func(t *testing.T) {
			err := Segment(t.Context(), SegmentConfig{Input: "in.mkv", OutputDir: t.TempDir(), ScenesFrames: frames})
			if err == nil || !strings.Contains(err.Error(), "strictly increasing") {
				t.Errorf("expected a frames validation error, got %v", err)
			}
		})
	}
}
