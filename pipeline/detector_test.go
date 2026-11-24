package pipeline

import (
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hekmon/sptenc/core"
	"github.com/hekmon/sptenc/transnetv2"
)

func TestParseSceneDetector(t *testing.T) {
	for _, tc := range []struct {
		name     string
		expected SceneDetector
		valid    bool
	}{
		{"scdet", SceneDetectorScdet, true},
		{"transnetv2", SceneDetectorTransNetV2, true},
		{"", "", false},
		{"SCDET", "", false},
		{"TransNetV2", "", false},
	} {
		got, err := ParseSceneDetector(tc.name)
		if (err == nil) != tc.valid || got != tc.expected {
			t.Errorf("%q: expected %q (valid %t), got %q (error %v)", tc.name, tc.expected, tc.valid, got, err)
		}
	}
	if names := SceneDetectorNames(); !slices.Equal(names, []string{"scdet", "transnetv2"}) {
		t.Errorf("unexpected detectors %v", names)
	}
	if SceneDetectors[0] != SceneDetectorScdet {
		t.Errorf("the default detector must come first, got %q", SceneDetectors[0])
	}
}

// Every listed detector has the expected properties: one without a row fails here.
func TestSceneDetectorProperties(t *testing.T) {
	tested := make(map[SceneDetector]bool, len(SceneDetectors))
	defer func() {
		for _, d := range SceneDetectors {
			if !tested[d] {
				t.Errorf("%s: no expected properties", d)
			}
		}
	}()
	for _, tc := range []struct {
		detector                   SceneDetector
		rangeMin, rangeMax         float64
		threshold                  float64
		searchMin, searchMax       float64
		formattedThreshold, score  string // of 24.2765 and 24.277
		reusable, reusableAtSearch float64
	}{
		{SceneDetectorScdet, 0, 100, 10, 8, 40, "24.2765", "24.28", 24.2765, 8},
		// scores rounded and compared rounded by sptenc: the score is its own reusable threshold
		{SceneDetectorTransNetV2, 0.3, 1, 0.5, 0.3, 0.9, "24.2765", "24.2770", 24.277, 0.3},
	} {
		d := tc.detector
		tested[d] = true
		if min, max := d.ThresholdRange(); min != tc.rangeMin || max != tc.rangeMax {
			t.Errorf("%s: expected the range %v-%v, got %v-%v", d, tc.rangeMin, tc.rangeMax, min, max)
		}
		if got := d.DefaultThreshold(); got != tc.threshold {
			t.Errorf("%s: expected the default threshold %v, got %v", d, tc.threshold, got)
		}
		if min, max := d.DefaultSearchRange(); min != tc.searchMin || max != tc.searchMax {
			t.Errorf("%s: expected the search range %v-%v, got %v-%v", d, tc.searchMin, tc.searchMax, min, max)
		}
		// the defaults must be valid thresholds, and the search range a range
		for _, v := range []float64{tc.threshold, tc.searchMin, tc.searchMax} {
			if err := d.ValidateThreshold(v); err != nil {
				t.Errorf("%s: default %v: %s", d, v, err)
			}
		}
		if tc.searchMin >= tc.searchMax {
			t.Errorf("%s: empty default search range", d)
		}
		// the bounds are in the range, beyond them is not
		for v, valid := range map[float64]bool{tc.rangeMin: true, tc.rangeMax: true, tc.rangeMin - 0.001: false, tc.rangeMax + 0.001: false} {
			if err := d.ValidateThreshold(v); (err == nil) != valid {
				t.Errorf("%s: threshold %v: expected valid %t, got %v", d, v, valid, err)
			}
		}
		if got := d.FormatThreshold(24.2765); got != tc.formattedThreshold {
			t.Errorf("%s: expected the threshold formatted %q, got %q", d, tc.formattedThreshold, got)
		}
		if got := d.FormatScore(24.277); got != tc.score {
			t.Errorf("%s: expected the score formatted %q, got %q", d, tc.score, got)
		}
		if got := d.ReusableThreshold(24.277, tc.searchMin); got != tc.reusable {
			t.Errorf("%s: expected the reusable threshold %v, got %v", d, tc.reusable, got)
		}
		if got := d.ReusableThreshold(tc.searchMin, tc.searchMin); got != tc.reusableAtSearch {
			t.Errorf("%s: expected the reusable threshold %v at the detection threshold, got %v", d, tc.reusableAtSearch, got)
		}
	}
}

// An unknown detector, the zero value included, never crashes: it fails its validation, naming
// itself as unknown, and its values print as plain numbers.
func TestSceneDetectorUnknown(t *testing.T) {
	for _, d := range []SceneDetector{"", "unknown"} {
		for _, threshold := range []float64{0, 10, 100} {
			err := d.ValidateThreshold(threshold)
			if err == nil || !strings.Contains(err.Error(), "unknown scene detector") {
				t.Errorf("%q: threshold %v: expected an unknown detector error, got %v", d, threshold, err)
			}
		}
		for _, tc := range []struct{ got, want string }{
			{d.FormatThreshold(24.2765), "24.2765"},
			{d.FormatThreshold(10), "10"},
			{d.FormatScore(24.277), "24.277"},
		} {
			if tc.got != tc.want {
				t.Errorf("%q: expected %q, got %q", d, tc.want, tc.got)
			}
		}
		if got := d.ReusableThreshold(24.277, 8); got != 24.277 {
			t.Errorf("%q: expected the score itself as reusable threshold, got %v", d, got)
		}
		if got := d.ReusableThreshold(5, 8); got != 8 {
			t.Errorf("%q: expected the detection threshold as reusable threshold, got %v", d, got)
		}
	}
}

// Every listed detector has a complete entry: known, a range, defaults inside it, a resolution.
func TestSceneDetectorEntriesComplete(t *testing.T) {
	for _, d := range SceneDetectors {
		p := detectorPropertiesOf(d)
		if !p.known || p.thresholdMin >= p.thresholdMax || p.scoreResolution <= 0 ||
			p.defaultSearchMin >= p.defaultSearchMax || p.defaultThreshold <= p.thresholdMin {
			t.Errorf("%s: incomplete properties %+v", d, p)
		}
		if p.scoreDecimals < 0 || p.thresholdDecimals < -1 {
			t.Errorf("%s: invalid decimals %+v", d, p)
		}
	}
}

// The helper and the frames it reads are properties of the detector: TransNetV2's are the
// protocol's, scdet has none, nor has an unknown detector.
func TestSceneDetectorHelper(t *testing.T) {
	for _, tc := range []struct {
		detector      SceneDetector
		helper        bool
		width, height int
	}{
		{SceneDetectorScdet, false, 0, 0},
		{SceneDetectorTransNetV2, true, transnetv2.FrameWidth, transnetv2.FrameHeight},
		{"", false, 0, 0},
		{"unknown", false, 0, 0},
	} {
		w, h := tc.detector.HelperFrameSize()
		if got := tc.detector.HasHelper(); got != tc.helper || w != tc.width || h != tc.height {
			t.Errorf("%q: expected helper %t reading %dx%d, got %t reading %dx%d", tc.detector, tc.helper, tc.width, tc.height, got, w, h)
		}
	}
	if transnetv2.FrameWidth != 48 || transnetv2.FrameHeight != 27 {
		t.Errorf("the protocol's frames are 48x27, got %dx%d", transnetv2.FrameWidth, transnetv2.FrameHeight)
	}
}

// TransNetV2's reusable threshold is the score itself, never below the detection threshold, with
// no floating point noise: the scores have 4 decimals and are printed with all of them.
func TestReusableThreshold_TransNetV2(t *testing.T) {
	for _, tc := range []struct {
		score, minThreshold, expected float64
	}{
		{0.7, 0.3, 0.7},
		{0.3, 0.3, 0.3},
		{0.3001, 0.3, 0.3001},
		{0.4999, 0.5, 0.5}, // never below the detection threshold
		{1, 0.3, 1},
	} {
		if got := SceneDetectorTransNetV2.ReusableThreshold(tc.score, tc.minThreshold); got != tc.expected {
			t.Errorf("score %v (min %v): expected %v, got %v", tc.score, tc.minThreshold, tc.expected, got)
		}
	}
	for _, tc := range []struct {
		value            float64
		threshold, score string
	}{
		{0.7, "0.7", "0.7000"},
		{0.3001, "0.3001", "0.3001"},
		{1, "1", "1.0000"},
	} {
		if got := SceneDetectorTransNetV2.FormatThreshold(tc.value); got != tc.threshold {
			t.Errorf("threshold %v: expected %q, got %q", tc.value, tc.threshold, got)
		}
		if got := SceneDetectorTransNetV2.FormatScore(tc.value); got != tc.score {
			t.Errorf("score %v: expected %q, got %q", tc.value, tc.score, got)
		}
	}
}

// The resolution field gives scdet the arithmetic of its former constant-based function, on every
// printable score of its range.
func TestReusableThreshold_ScdetArithmetic(t *testing.T) {
	former := func(score, minThreshold float64) float64 {
		const precision = 10 / 0.001
		threshold := math.Round((score-0.001/2)*precision) / precision
		return math.Max(threshold, minThreshold)
	}
	for i := 0; i <= 100000; i++ {
		score := float64(i) / 1000
		if got, want := SceneDetectorScdet.ReusableThreshold(score, 0), former(score, 0); got != want {
			t.Fatalf("score %v: expected %v, got %v", score, want, got)
		}
	}
}

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
		if got := SceneDetectorScdet.ReusableThreshold(tc.score, tc.minThreshold); got != tc.expected {
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
		fromThreshold, err := core.SelectScenes(scenes, SceneDetectorScdet.ReusableThreshold(scene.Score, 1), "25", time.Minute, 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(fromScore) != len(fromThreshold) {
			t.Errorf("score %v: %d scenes from the score but %d from the reusable threshold",
				scene.Score, len(fromScore), len(fromThreshold))
		}
	}
}
