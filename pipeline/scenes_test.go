package pipeline

import (
	"testing"
	"time"

	"github.com/hekmon/sptenc/core"
)

func TestReusableThreshold(t *testing.T) {
	for _, tc := range []struct {
		score, minThreshold, expected float64
	}{
		{24.277, 14, 24.2765},
		{14.003, 14, 14.0025},
		{14, 14, 14},         // never below the detection threshold
		{14.0004, 14, 14},    // same
		{100, 14, 99.9995},   // top of the range
		{0.001, 0, 0.0005},   // bottom of the range
		{30.462, 1, 30.4615}, // no floating point noise
		{1.008, 1, 1.0075},
	} {
		if got := ReusableThreshold(tc.score, tc.minThreshold); got != tc.expected {
			t.Errorf("score %v (min %v): expected %v, got %v", tc.score, tc.minThreshold, tc.expected, got)
		}
	}
}

// A reusable threshold must select the same scenes as the printed score it comes from,
// neighbours one resolution step away included.
func TestReusableThreshold_SameSelection(t *testing.T) {
	scenes := []core.Scene{
		{Start: 10 * time.Second, Score: 24.276},
		{Start: 20 * time.Second, Score: 24.277},
		{Start: 30 * time.Second, Score: 24.278},
	}
	for _, scene := range scenes {
		fromScore, err := core.SelectScenes(scenes, scene.Score, "25", time.Minute, 0)
		if err != nil {
			t.Fatal(err)
		}
		fromThreshold, err := core.SelectScenes(scenes, ReusableThreshold(scene.Score, 1), "25", time.Minute, 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(fromScore) != len(fromThreshold) {
			t.Errorf("score %v: %d scenes from the score but %d from the reusable threshold",
				scene.Score, len(fromScore), len(fromThreshold))
		}
	}
}
