package core

import (
	"math"
	"testing"
	"time"
)

// dropCandidates runs the complete candidates logic without minimum duration nor maximum
// threshold and returns the thresholds kept for a given minDrop.
func dropCandidates(scenes []Scene, minDrop int) (thresholds []float64) {
	for _, candidate := range FilterCandidatesByDrop(GetCandidates(scenes, math.Inf(1), 0, 0), minDrop) {
		thresholds = append(thresholds, candidate.Threshold)
	}
	return
}

func TestFilterCandidatesByDrop_Empty(t *testing.T) {
	candidates := dropCandidates(nil, 1)
	if candidates != nil {
		t.Errorf("expected nil for empty scenes, got %v", candidates)
	}
}

func TestFilterCandidatesByDrop_SingleScene(t *testing.T) {
	scenes := []Scene{{Score: 0.5}}
	candidates := dropCandidates(scenes, 1)
	if len(candidates) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(candidates))
	}
	if candidates[0] != 0.5 {
		t.Errorf("expected candidate 0.5, got %v", candidates[0])
	}
}

func TestFilterCandidatesByDrop_ManyScenes(t *testing.T) {
	scenes := []Scene{
		{Score: 0.3},
		{Score: 0.3},
		{Score: 0.5},
		{Score: 0.7},
		{Score: 0.9},
	}

	tests := []struct {
		minDrop  int
		expected []float64
	}{
		{1, []float64{0.3, 0.5, 0.7, 0.9}},
		{2, []float64{0.3, 0.5, 0.9}},
		{3, []float64{0.3, 0.7}},
		{4, []float64{0.3, 0.9}},
		{5, []float64{0.3}},
	}

	for _, tt := range tests {
		candidates := dropCandidates(scenes, tt.minDrop)
		if len(candidates) != len(tt.expected) {
			t.Errorf("minDrop=%d: expected %v, got %v", tt.minDrop, tt.expected, candidates)
			continue
		}
		for i := range candidates {
			if candidates[i] != tt.expected[i] {
				t.Errorf("minDrop=%d: candidate[%d] expected %v, got %v", tt.minDrop, i, tt.expected[i], candidates[i])
			}
		}
	}
}

func TestFilterCandidatesByDrop_MinDropLargerThanScenes(t *testing.T) {
	scenes := []Scene{
		{Score: 0.1},
		{Score: 0.2},
	}
	// minDrop=10 should only return the first candidate
	candidates := dropCandidates(scenes, 10)
	if len(candidates) != 1 || candidates[0] != 0.1 {
		t.Errorf("expected [0.1], got %v", candidates)
	}
}

func TestFilterCandidatesByDrop_DedupesScores(t *testing.T) {
	scenes := []Scene{
		{Score: 0.4},
		{Score: 0.4},
		{Score: 0.4},
		{Score: 0.4},
	}
	// All same score → only one unique candidate
	candidates := dropCandidates(scenes, 1)
	if len(candidates) != 1 || candidates[0] != 0.4 {
		t.Errorf("expected [0.4], got %v", candidates)
	}
}

func TestGetOptimalMinDrop_EmptyScenes(t *testing.T) {
	candidates, minDrop := GetOptimalMinDrop(nil, 5)
	if candidates != nil {
		t.Errorf("expected nil candidates for empty scenes, got %v", candidates)
	}
	if minDrop != 0 {
		t.Errorf("expected minDrop=0 for empty scenes, got %d", minDrop)
	}
}

func TestGetOptimalMinDrop_MaxCandidatesZero(t *testing.T) {
	scenes := []Scene{
		{Score: 0.1},
		{Score: 0.2},
	}
	candidates, minDrop := GetOptimalMinDrop(GetCandidates(scenes, math.Inf(1), 0, 0), 0)
	if candidates != nil {
		t.Errorf("expected nil candidates for maxCandidates=0, got %v", candidates)
	}
	if minDrop != 0 {
		t.Errorf("expected minDrop=0 for maxCandidates=0, got %d", minDrop)
	}
}

func TestGetOptimalMinDrop_FindsSmallestMinDrop(t *testing.T) {
	scenes := []Scene{
		{Score: 0.1},
		{Score: 0.2},
		{Score: 0.3},
		{Score: 0.4},
		{Score: 0.5},
	}

	tests := []struct {
		maxCandidates int
		wantMinDrop   int
		wantLen       int
	}{
		{5, 1, 5},
		{4, 2, 3}, // minDrop=1 gives 5>4, minDrop=2 gives 3<=4
		{3, 2, 3}, // minDrop=2 gives 3<=3
		{2, 3, 2}, // minDrop=2 gives 3>2, minDrop=3 gives 2<=2
		{1, 5, 1}, // minDrop=3 gives 2>1, minDrop=4 gives 2>1, minDrop=5 gives 1<=1
	}

	for _, tt := range tests {
		candidates, minDrop := GetOptimalMinDrop(GetCandidates(scenes, math.Inf(1), 0, 0), tt.maxCandidates)
		if minDrop != tt.wantMinDrop {
			t.Errorf("maxCandidates=%d: expected minDrop %d, got %d", tt.maxCandidates, tt.wantMinDrop, minDrop)
		}
		if len(candidates) != tt.wantLen {
			t.Errorf("maxCandidates=%d: expected %d candidates, got %v", tt.maxCandidates, tt.wantLen, candidates)
		}
	}
}

func TestGetOptimalMinDrop_SingleScene(t *testing.T) {
	scenes := []Scene{{Score: 0.5}}
	candidates, minDrop := GetOptimalMinDrop(GetCandidates(scenes, math.Inf(1), 0, 0), 1)
	if len(candidates) != 1 || candidates[0].Threshold != 0.5 {
		t.Errorf("expected [0.5], got %v", candidates)
	}
	if minDrop != 1 {
		t.Errorf("expected minDrop=1, got %d", minDrop)
	}
}

func TestFilterCandidatesByDrop_MinDropRespectedBetweenCandidates(t *testing.T) {
	// 10 scenes with distinct scores
	scenes := make([]Scene, 10)
	for i := range scenes {
		scenes[i] = Scene{Score: float64(i) * 0.1}
	}

	candidates := dropCandidates(scenes, 3)
	// First candidate is always 0.0.
	// Each subsequent candidate must drop at least 3 more scenes than the previous.
	// 0.0 → 0 scenes eliminated (score < 0.0 is none)
	// 0.3 → 3 scenes eliminated (0.0, 0.1, 0.2). diff = 3-0 = 3 >= 3 → add
	// 0.6 → 6 scenes eliminated. diff = 6-3 = 3 >= 3 → add
	// 0.9 → 9 scenes eliminated. diff = 9-6 = 3 >= 3 → add
	expected := []float64{0.0, 0.3, 0.6, 0.9}
	if len(candidates) != len(expected) {
		t.Fatalf("expected %v, got %v", expected, candidates)
	}
	for i := range candidates {
		if math.Abs(candidates[i]-expected[i]) > 1e-9 {
			t.Errorf("candidate[%d] expected %v, got %v", i, expected[i], candidates[i])
		}
	}
}

func TestFilterShortScenes_ZeroMinDuration(t *testing.T) {
	scenes := []Scene{{Start: 1 * time.Second}, {Start: 2 * time.Second}}
	got := FilterShortScenes(scenes, 10*time.Second, 0)
	if len(got) != 2 {
		t.Fatalf("expected 2 scenes for minDuration=0, got %d", len(got))
	}
}

func TestFilterShortScenes_NoShortSegments(t *testing.T) {
	// All segments are 5s, minDuration is 3s → no change.
	scenes := []Scene{
		{Start: 5 * time.Second},
		{Start: 10 * time.Second},
		{Start: 15 * time.Second},
	}
	got := FilterShortScenes(scenes, 20*time.Second, 3*time.Second)
	if len(got) != 3 {
		t.Fatalf("expected 3 scenes, got %d", len(got))
	}
}

func TestFilterShortScenes_SingleShortAtStart(t *testing.T) {
	// Segments: 1s | 9s. Min 4s. Shortest is first segment → merge right.
	scenes := []Scene{{Start: 1 * time.Second}}
	got := FilterShortScenes(scenes, 10*time.Second, 4*time.Second)
	if len(got) != 0 {
		t.Fatalf("expected 0 scenes (single 10s segment), got %d", len(got))
	}
}

func TestFilterShortScenes_SingleShortAtEnd(t *testing.T) {
	// Segments: 9s | 1s. Min 4s. Shortest is last segment → merge left.
	scenes := []Scene{{Start: 9 * time.Second}}
	got := FilterShortScenes(scenes, 10*time.Second, 4*time.Second)
	if len(got) != 0 {
		t.Fatalf("expected 0 scenes (single 10s segment), got %d", len(got))
	}
}

func TestFilterShortScenes_SingleShortInMiddle(t *testing.T) {
	// Segments: 5s | 1s | 4s. Min 2s. Shortest is middle (1s).
	// Left neighbour 5s, right neighbour 4s. Right is shorter → merge right.
	// Expected boundaries: [5s] → segments 5s | 5s.
	scenes := []Scene{{Start: 5 * time.Second}, {Start: 6 * time.Second}}
	got := FilterShortScenes(scenes, 10*time.Second, 2*time.Second)
	if len(got) != 1 {
		t.Fatalf("expected 1 scene, got %d", len(got))
	}
	if got[0].Start != 5*time.Second {
		t.Errorf("expected boundary at 5s, got %v", got[0].Start)
	}
}

func TestFilterShortScenes_MergeIntoShorterNeighbour(t *testing.T) {
	// Segments: 6s | 1s | 3s. Min 2s. Shortest is middle (1s).
	// Left neighbour 6s, right neighbour 3s. Right is shorter → merge right.
	// Expected boundaries: [6s] → segments 6s | 4s.
	scenes := []Scene{{Start: 6 * time.Second}, {Start: 7 * time.Second}}
	got := FilterShortScenes(scenes, 10*time.Second, 2*time.Second)
	if len(got) != 1 {
		t.Fatalf("expected 1 scene, got %d", len(got))
	}
	if got[0].Start != 6*time.Second {
		t.Errorf("expected boundary at 6s, got %v", got[0].Start)
	}
}

func TestFilterShortScenes_EqualNeighboursPreferLeft(t *testing.T) {
	// Segments: 3s | 1s | 3s. Min 2s. Shortest is middle (1s).
	// Left and right are equal (3s). leftDur <= rightDur → merge left.
	// Expected boundaries: [4s] → segments 4s | 3s.
	scenes := []Scene{{Start: 3 * time.Second}, {Start: 4 * time.Second}}
	got := FilterShortScenes(scenes, 7*time.Second, 2*time.Second)
	if len(got) != 1 {
		t.Fatalf("expected 1 scene, got %d", len(got))
	}
	if got[0].Start != 4*time.Second {
		t.Errorf("expected boundary at 4s, got %v", got[0].Start)
	}
}

func TestFilterShortScenes_ClusterOfShortSegments(t *testing.T) {
	// Segments: 5s | 1s | 1s | 1s | 7s. Min 2s.
	// Iteration 1: shortest is 1s (index 1). Left 5s, right 1s. Right shorter → merge right.
	//   Boundaries: [5s, 2s, 3s, 8s] → durations 5s | 1s | 1s | 7s... wait.
	//
	// Let me trace carefully:
	// Initial scenes: [1s, 2s, 3s, 8s], total=9s
	// Initial durations: 1s, 1s, 1s, 5s, 1s
	//
	// Iter 1: shortest idx=0 (1s). First segment → merge right.
	//   durations[1] += 1s → 2s. durations = [2s, 1s, 5s, 1s]. scenes = [2s, 3s, 8s]
	//
	// Iter 2: shortest idx=1 (1s). Left 2s, right 5s. Left shorter → merge left.
	//   durations[0] += 1s → 3s. durations = [3s, 5s, 1s]. scenes = [3s, 8s]
	//
	// Iter 3: shortest idx=2 (1s). Last segment → merge left.
	//   durations[1] += 1s → 6s. durations = [3s, 6s]. scenes = [3s]
	//
	// Result: segments 3s | 6s.
	scenes := []Scene{{Start: 1 * time.Second}, {Start: 2 * time.Second}, {Start: 3 * time.Second}, {Start: 8 * time.Second}}
	got := FilterShortScenes(scenes, 9*time.Second, 2*time.Second)
	if len(got) != 1 {
		t.Fatalf("expected 1 scene, got %d", len(got))
	}
	if got[0].Start != 3*time.Second {
		t.Errorf("expected boundary at 3s, got %v", got[0].Start)
	}
}

func TestFilterShortScenes_TotalDurationBelowMin(t *testing.T) {
	// Total 5s, min 10s → single segment.
	scenes := []Scene{{Start: 2 * time.Second}, {Start: 4 * time.Second}}
	got := FilterShortScenes(scenes, 5*time.Second, 10*time.Second)
	if len(got) != 0 {
		t.Fatalf("expected 0 scenes (whole video is one segment), got %d", len(got))
	}
}

func TestFilterShortScenes_EmptyScenes(t *testing.T) {
	got := FilterShortScenes(nil, 10*time.Second, 2*time.Second)
	if got != nil {
		t.Fatalf("expected nil for empty scenes, got %v", got)
	}
}

func TestFilterShortScenes_MergeCreatesNewShortSegment(t *testing.T) {
	// Segments: 3s | 2s | 2s | 3s. Min 3s.
	// Shortest are indices 1 and 2 (both 2s). Pick index 1 (first encountered).
	// Left 3s, right 2s. Right shorter → merge right.
	// New durations: 3s | 4s | 3s. All >= 3s → stop.
	// Result: boundaries [3s, 7s] → 2 scenes.
	scenes := []Scene{{Start: 3 * time.Second}, {Start: 5 * time.Second}, {Start: 7 * time.Second}}
	got := FilterShortScenes(scenes, 10*time.Second, 3*time.Second)
	if len(got) != 2 {
		t.Fatalf("expected 2 scenes, got %d", len(got))
	}
	if got[0].Start != 3*time.Second {
		t.Errorf("expected first boundary at 3s, got %v", got[0].Start)
	}
	if got[1].Start != 7*time.Second {
		t.Errorf("expected second boundary at 7s, got %v", got[1].Start)
	}
}

func TestSelectScenes_ThresholdBeforeMerge(t *testing.T) {
	total := 60 * time.Second
	minDuration := 5 * time.Second
	// The 2s segment between 8s and 10s is too short. Its left neighbour is the shorter one,
	// so a merge removes the boundary at 8s: the strong one.
	scenes := []Scene{
		{Start: 8 * time.Second, Score: 40},
		{Start: 10 * time.Second, Score: 15},
		{Start: 40 * time.Second, Score: 35},
	}

	// Threshold 14 keeps everything: the merge has to happen, the strong cut is lost.
	got := SelectScenes(scenes, 14, total, minDuration)
	want := []Scene{{Start: 10 * time.Second, Score: 15}, {Start: 40 * time.Second, Score: 35}}
	if !sameBoundaries(got, want) {
		t.Errorf("threshold 14: expected %v, got %v", want, got)
	}

	// Threshold 20 drops the weak boundary first: nothing is short anymore, the strong cut survives.
	got = SelectScenes(scenes, 20, total, minDuration)
	want = []Scene{{Start: 8 * time.Second, Score: 40}, {Start: 40 * time.Second, Score: 35}}
	if !sameBoundaries(got, want) {
		t.Errorf("threshold 20: expected %v, got %v", want, got)
	}

	// The result must not depend on weaker boundaries present in the input list
	// (ie on the threshold used for detection).
	withWeaker := append([]Scene{{Start: 7 * time.Second, Score: 5}}, scenes...)
	if got2 := SelectScenes(withWeaker, 20, total, minDuration); !sameBoundaries(got, got2) {
		t.Errorf("weaker boundaries changed the result: %v vs %v", got, got2)
	}

	// No minimum duration: threshold only.
	if got = SelectScenes(scenes, 14, total, 0); len(got) != 3 {
		t.Errorf("expected the 3 boundaries without minimum duration, got %v", got)
	}

	// The input must be left untouched.
	if scenes[0].Start != 8*time.Second || scenes[1].Start != 10*time.Second || scenes[2].Start != 40*time.Second {
		t.Errorf("input scenes have been modified: %v", scenes)
	}
}

// The frame index of a boundary is what the video is cut at in the end: whatever the selection
// does (threshold, merges in any direction), the boundaries left must still hold their own.
func TestSelectScenes_KeepsFrames(t *testing.T) {
	scenes := []Scene{
		{Frame: 24, Start: 1 * time.Second, Score: 50},    // first segment too short: merged to the right
		{Frame: 240, Start: 10 * time.Second, Score: 40},  //
		{Frame: 264, Start: 11 * time.Second, Score: 15},  // dropped by the threshold
		{Frame: 480, Start: 20 * time.Second, Score: 30},  //
		{Frame: 528, Start: 22 * time.Second, Score: 35},  // ends a 2s segment merged into its shorter neighbour, the right one: this boundary goes
		{Frame: 720, Start: 30 * time.Second, Score: 60},  //
		{Frame: 1416, Start: 59 * time.Second, Score: 45}, // last segment too short: merged to the left
	}
	got := SelectScenes(scenes, 20, 60*time.Second, 5*time.Second)
	want := []Scene{scenes[1], scenes[3], scenes[5]}
	if len(got) != len(want) {
		t.Fatalf("expected %v, got %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("boundary %d: expected %+v, got %+v", i, want[i], got[i])
		}
	}
}

func TestGetCandidates(t *testing.T) {
	total := 60 * time.Second
	minDuration := 5 * time.Second
	scenes := []Scene{
		{Start: 20 * time.Second, Score: 30},
		{Start: 21 * time.Second, Score: 15}, // always merged away while 20s is there
		{Start: 22 * time.Second, Score: 18}, // same
		{Start: 40 * time.Second, Score: 50},
		{Start: 50 * time.Second, Score: 90}, // above the max threshold: never a candidate, always a marker
	}
	// 15, 18 and 30 all end up with boundaries at 20s, 40s and 50s: only the lowest is kept.
	// 50 drops the 20s boundary. 90 is above the maximum threshold.
	candidates := GetCandidates(scenes, 60, total, minDuration)
	if len(candidates) != 2 || candidates[0].Threshold != 15 || candidates[1].Threshold != 50 {
		t.Fatalf("expected candidates 15 and 50, got %v", candidates)
	}
	if !sameBoundaries(candidates[0].Scenes, []Scene{{Start: 20 * time.Second}, {Start: 40 * time.Second}, {Start: 50 * time.Second}}) {
		t.Errorf("candidate 15: unexpected scenes %v", candidates[0].Scenes)
	}
	if !sameBoundaries(candidates[1].Scenes, []Scene{{Start: 40 * time.Second}, {Start: 50 * time.Second}}) {
		t.Errorf("candidate 50: unexpected scenes %v", candidates[1].Scenes)
	}

	// Without minimum duration every unique score within range is a distinct candidate.
	if candidates = GetCandidates(scenes, 60, total, 0); len(candidates) != 4 {
		t.Errorf("expected 4 candidates without minimum duration, got %v", candidates)
	}

	// Each candidate must be what SelectScenes (so encode) gives for its threshold.
	for _, candidate := range GetCandidates(scenes, 100, total, minDuration) {
		if !sameBoundaries(candidate.Scenes, SelectScenes(scenes, candidate.Threshold, total, minDuration)) {
			t.Errorf("candidate %v does not match SelectScenes", candidate.Threshold)
		}
	}
}

// The scene drop must be counted on real scenes (once merged), not on raw markers.
func TestFilterCandidatesByDrop_CountsMergedScenes(t *testing.T) {
	total := 100 * time.Second
	minDuration := 5 * time.Second
	scenes := []Scene{
		{Start: 10 * time.Second, Score: 20},
		{Start: 11 * time.Second, Score: 21}, // merged away
		{Start: 12 * time.Second, Score: 22}, // merged away
		{Start: 13 * time.Second, Score: 23}, // merged away
		{Start: 50 * time.Second, Score: 30},
		{Start: 80 * time.Second, Score: 40},
	}
	candidates := GetCandidates(scenes, 100, total, minDuration)
	// Raw markers: going from 20 to 30 drops 4 of them. Real scenes: it only drops 1.
	kept := FilterCandidatesByDrop(candidates, 2)
	var thresholds []float64
	for _, candidate := range kept {
		thresholds = append(thresholds, candidate.Threshold)
	}
	// 20 -> 3 boundaries (one around 10s, 50s, 80s), 30 -> 2 boundaries, 40 -> 1 boundary
	if len(thresholds) != 2 || thresholds[0] != 20 || thresholds[1] != 40 {
		t.Errorf("expected thresholds [20 40] (a drop of 2 real scenes), got %v", thresholds)
	}
}
