package core

import (
	"math"
	"testing"
	"time"
)

// testFrameRate is the frame rate the scenes of these tests are placed on: at 25 fps, a second is
// 25 frames and the durations of the tests read as they are.
const testFrameRate = "25"

// at returns a scene boundary at a whole second of the testFrameRate grid.
func at(second int) Scene {
	return Scene{Frame: second * 25, Start: time.Duration(second) * time.Second}
}

// atScore returns a scene boundary at a whole second of the testFrameRate grid, with its score.
func atScore(second int, score float64) Scene {
	scene := at(second)
	scene.Score = score
	return scene
}

func mustFilter(t *testing.T, scenes []Scene, totalDuration, minDuration time.Duration) []Scene {
	t.Helper()
	filtered, err := FilterShortScenes(scenes, testFrameRate, totalDuration, minDuration)
	if err != nil {
		t.Fatal(err)
	}
	return filtered
}

func mustSelect(t *testing.T, scenes []Scene, threshold float64, totalDuration, minDuration time.Duration) []Scene {
	t.Helper()
	selected, err := SelectScenes(scenes, threshold, testFrameRate, totalDuration, minDuration)
	if err != nil {
		t.Fatal(err)
	}
	return selected
}

func mustCandidates(t *testing.T, scenes []Scene, maxThreshold float64, totalDuration, minDuration time.Duration) []Candidate {
	t.Helper()
	candidates, err := GetCandidates(scenes, maxThreshold, testFrameRate, totalDuration, minDuration)
	if err != nil {
		t.Fatal(err)
	}
	return candidates
}

// dropCandidates runs the complete candidates logic without minimum duration nor maximum
// threshold and returns the thresholds kept for a given minDrop.
func dropCandidates(t *testing.T, scenes []Scene, minDrop int) (thresholds []float64) {
	t.Helper()
	for _, candidate := range FilterCandidatesByDrop(mustCandidates(t, scenes, math.Inf(1), 0, 0), minDrop) {
		thresholds = append(thresholds, candidate.Threshold)
	}
	return
}

func TestFilterCandidatesByDrop_Empty(t *testing.T) {
	candidates := dropCandidates(t, nil, 1)
	if candidates != nil {
		t.Errorf("expected nil for empty scenes, got %v", candidates)
	}
}

func TestFilterCandidatesByDrop_SingleScene(t *testing.T) {
	scenes := []Scene{{Score: 0.5}}
	candidates := dropCandidates(t, scenes, 1)
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
		candidates := dropCandidates(t, scenes, tt.minDrop)
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
	candidates := dropCandidates(t, scenes, 10)
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
	candidates := dropCandidates(t, scenes, 1)
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
	candidates, minDrop := GetOptimalMinDrop(mustCandidates(t, scenes, math.Inf(1), 0, 0), 0)
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
		candidates, minDrop := GetOptimalMinDrop(mustCandidates(t, scenes, math.Inf(1), 0, 0), tt.maxCandidates)
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
	candidates, minDrop := GetOptimalMinDrop(mustCandidates(t, scenes, math.Inf(1), 0, 0), 1)
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

	candidates := dropCandidates(t, scenes, 3)
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
	scenes := []Scene{at(1), at(2)}
	got := mustFilter(t, scenes, 10*time.Second, 0)
	if len(got) != 2 {
		t.Fatalf("expected 2 scenes for minDuration=0, got %d", len(got))
	}
}

func TestFilterShortScenes_NoShortSegments(t *testing.T) {
	// All segments are 5s, minDuration is 3s → no change.
	scenes := []Scene{
		at(5),
		at(10),
		at(15),
	}
	got := mustFilter(t, scenes, 20*time.Second, 3*time.Second)
	if len(got) != 3 {
		t.Fatalf("expected 3 scenes, got %d", len(got))
	}
}

func TestFilterShortScenes_SingleShortAtStart(t *testing.T) {
	// Segments: 1s | 9s. Min 4s. Shortest is first segment → merge right.
	scenes := []Scene{at(1)}
	got := mustFilter(t, scenes, 10*time.Second, 4*time.Second)
	if len(got) != 0 {
		t.Fatalf("expected 0 scenes (single 10s segment), got %d", len(got))
	}
}

func TestFilterShortScenes_SingleShortAtEnd(t *testing.T) {
	// Segments: 9s | 1s. Min 4s. Shortest is last segment → merge left.
	scenes := []Scene{at(9)}
	got := mustFilter(t, scenes, 10*time.Second, 4*time.Second)
	if len(got) != 0 {
		t.Fatalf("expected 0 scenes (single 10s segment), got %d", len(got))
	}
}

func TestFilterShortScenes_SingleShortInMiddle(t *testing.T) {
	// Segments: 5s | 1s | 4s. Min 2s. Shortest is middle (1s).
	// Left neighbour 5s, right neighbour 4s. Right is shorter → merge right.
	// Expected boundaries: [5s] → segments 5s | 5s.
	scenes := []Scene{at(5), at(6)}
	got := mustFilter(t, scenes, 10*time.Second, 2*time.Second)
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
	scenes := []Scene{at(6), at(7)}
	got := mustFilter(t, scenes, 10*time.Second, 2*time.Second)
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
	scenes := []Scene{at(3), at(4)}
	got := mustFilter(t, scenes, 7*time.Second, 2*time.Second)
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
	scenes := []Scene{at(1), at(2), at(3), at(8)}
	got := mustFilter(t, scenes, 9*time.Second, 2*time.Second)
	if len(got) != 1 {
		t.Fatalf("expected 1 scene, got %d", len(got))
	}
	if got[0].Start != 3*time.Second {
		t.Errorf("expected boundary at 3s, got %v", got[0].Start)
	}
}

func TestFilterShortScenes_TotalDurationBelowMin(t *testing.T) {
	// Total 5s, min 10s → single segment.
	scenes := []Scene{at(2), at(4)}
	got := mustFilter(t, scenes, 5*time.Second, 10*time.Second)
	if len(got) != 0 {
		t.Fatalf("expected 0 scenes (whole video is one segment), got %d", len(got))
	}
}

func TestFilterShortScenes_EmptyScenes(t *testing.T) {
	got := mustFilter(t, nil, 10*time.Second, 2*time.Second)
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
	scenes := []Scene{at(3), at(5), at(7)}
	got := mustFilter(t, scenes, 10*time.Second, 3*time.Second)
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

func TestFilterShortScenes_ExactlyMinDuration(t *testing.T) {
	// Segments: 5s | 5s. Min 5s: a segment lasting exactly the minimum is long enough.
	if got := mustFilter(t, []Scene{at(5)}, 10*time.Second, 5*time.Second); len(got) != 1 {
		t.Fatalf("expected the boundary to be kept, got %v", got)
	}
}

// The same boundaries must merge the same way whatever the muxer rounded their timestamps with.
// mkvmerge rounds half a millisecond down and ffmpeg (the master) up: at 23.976 fps, frame 156 is
// at 6506.5 ms, 6506 ms in a source muxed by mkvmerge and 6507 ms in its master. By timestamps,
// the 10-frame segment 156-166 had two neighbours of 156 frames each, the left one shorter on the
// source and longer on the master: merged left on one, right on the other. In frames the
// neighbours are equal, and a tie merges left on both.
func TestFilterShortScenes_ContainerRounding(t *testing.T) {
	const frameRate = "24000/1001"
	exact := func(frame int) float64 { return float64(frame) * 1001 / 24 } // in ms
	total := time.Duration(322) * 1001 * time.Second / 24000
	for _, muxer := range []struct {
		name  string
		round func(frame int) time.Duration
	}{
		{"mkvmerge", func(frame int) time.Duration { return time.Duration(math.Ceil(exact(frame)-0.5)) * time.Millisecond }},
		{"ffmpeg", func(frame int) time.Duration { return time.Duration(math.Floor(exact(frame)+0.5)) * time.Millisecond }},
	} {
		scenes := []Scene{{Frame: 156, Start: muxer.round(156)}, {Frame: 166, Start: muxer.round(166)}}
		got, err := FilterShortScenes(scenes, frameRate, total, 5*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].Frame != 166 {
			t.Errorf("%s timestamps: expected the 10-frame segment merged into its left neighbour (the boundary at frame 166 kept), got %+v",
				muxer.name, got)
		}
	}
}

func TestFilterShortScenes_InvalidFrameRate(t *testing.T) {
	scenes := []Scene{at(1), at(2)}
	for _, frameRate := range []string{"", "0/0", "24000/0", "abc"} {
		if _, err := FilterShortScenes(scenes, frameRate, 10*time.Second, 2*time.Second); err == nil {
			t.Errorf("frame rate %q: expected an error", frameRate)
		}
	}
	// Without minimum duration there is nothing to count: the frame rate is not read.
	if got, err := FilterShortScenes(scenes, "", 10*time.Second, 0); err != nil || len(got) != 2 {
		t.Errorf("without minimum duration: expected the 2 scenes and no error, got %v, %v", got, err)
	}
}

func TestDurationToFrames(t *testing.T) {
	for _, tc := range []struct {
		d        time.Duration
		num, den int64
		ceil     bool
		want     int
	}{
		{5 * time.Second, 25, 1, true, 125},                // exactly 125 frames: 125 is not short
		{5 * time.Second, 24000, 1001, true, 120},          // 119.88 frames: 119 is short, 120 is not
		{5 * time.Second, 30000, 1001, true, 150},          // 149.85
		{5 * time.Second, 24000, 1001, false, 120},         // nearest
		{4990 * time.Millisecond, 24000, 1001, false, 120}, // 119.64
		{4970 * time.Millisecond, 24000, 1001, false, 119}, // 119.16
		{0, 25, 1, true, 0},
		{48 * time.Hour, 120000, 1001, false, 20715285}, // 20715284.7: d times num overflows an int64
	} {
		if got := durationToFrames(tc.d, tc.num, tc.den, tc.ceil); got != tc.want {
			t.Errorf("%v at %d/%d (ceil %t): expected %d frames, got %d", tc.d, tc.num, tc.den, tc.ceil, tc.want, got)
		}
	}
}

func TestSelectScenes_ThresholdBeforeMerge(t *testing.T) {
	total := 60 * time.Second
	minDuration := 5 * time.Second
	// The 2s segment between 8s and 10s is too short. Its left neighbour is the shorter one,
	// so a merge removes the boundary at 8s: the strong one.
	scenes := []Scene{
		atScore(8, 40),
		atScore(10, 15),
		atScore(40, 35),
	}

	// Threshold 14 keeps everything: the merge has to happen, the strong cut is lost.
	got := mustSelect(t, scenes, 14, total, minDuration)
	want := []Scene{atScore(10, 15), atScore(40, 35)}
	if !sameBoundaries(got, want) {
		t.Errorf("threshold 14: expected %v, got %v", want, got)
	}

	// Threshold 20 drops the weak boundary first: nothing is short anymore, the strong cut survives.
	got = mustSelect(t, scenes, 20, total, minDuration)
	want = []Scene{atScore(8, 40), atScore(40, 35)}
	if !sameBoundaries(got, want) {
		t.Errorf("threshold 20: expected %v, got %v", want, got)
	}

	// The result must not depend on weaker boundaries present in the input list
	// (ie on the threshold used for detection).
	withWeaker := append([]Scene{atScore(7, 5)}, scenes...)
	if got2 := mustSelect(t, withWeaker, 20, total, minDuration); !sameBoundaries(got, got2) {
		t.Errorf("weaker boundaries changed the result: %v vs %v", got, got2)
	}

	// No minimum duration: threshold only.
	if got = mustSelect(t, scenes, 14, total, 0); len(got) != 3 {
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
	got, err := SelectScenes(scenes, 20, "24", 60*time.Second, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
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
		atScore(20, 30),
		atScore(21, 15), // always merged away while 20s is there
		atScore(22, 18), // same
		atScore(40, 50),
		atScore(50, 90), // above the max threshold: never a candidate, always a marker
	}
	// 15, 18 and 30 all end up with boundaries at 20s, 40s and 50s: only the lowest is kept.
	// 50 drops the 20s boundary. 90 is above the maximum threshold.
	candidates := mustCandidates(t, scenes, 60, total, minDuration)
	if len(candidates) != 2 || candidates[0].Threshold != 15 || candidates[1].Threshold != 50 {
		t.Fatalf("expected candidates 15 and 50, got %v", candidates)
	}
	if !sameBoundaries(candidates[0].Scenes, []Scene{at(20), at(40), at(50)}) {
		t.Errorf("candidate 15: unexpected scenes %v", candidates[0].Scenes)
	}
	if !sameBoundaries(candidates[1].Scenes, []Scene{at(40), at(50)}) {
		t.Errorf("candidate 50: unexpected scenes %v", candidates[1].Scenes)
	}

	// Without minimum duration every unique score within range is a distinct candidate.
	if candidates = mustCandidates(t, scenes, 60, total, 0); len(candidates) != 4 {
		t.Errorf("expected 4 candidates without minimum duration, got %v", candidates)
	}

	// Each candidate must be what SelectScenes (so encode) gives for its threshold.
	for _, candidate := range mustCandidates(t, scenes, 100, total, minDuration) {
		if !sameBoundaries(candidate.Scenes, mustSelect(t, scenes, candidate.Threshold, total, minDuration)) {
			t.Errorf("candidate %v does not match SelectScenes", candidate.Threshold)
		}
	}
}

// The scene drop must be counted on real scenes (once merged), not on raw markers.
func TestFilterCandidatesByDrop_CountsMergedScenes(t *testing.T) {
	total := 100 * time.Second
	minDuration := 5 * time.Second
	scenes := []Scene{
		atScore(10, 20),
		atScore(11, 21), // merged away
		atScore(12, 22), // merged away
		atScore(13, 23), // merged away
		atScore(50, 30),
		atScore(80, 40),
	}
	candidates := mustCandidates(t, scenes, 100, total, minDuration)
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
