package core

import (
	"math"
	"testing"
)

func TestGetSearchThresholdCandidates_Empty(t *testing.T) {
	candidates := GetSearchThresholdCandidates(nil, 1)
	if candidates != nil {
		t.Errorf("expected nil for empty scenes, got %v", candidates)
	}
}

func TestGetSearchThresholdCandidates_SingleScene(t *testing.T) {
	scenes := []Scene{{Score: 0.5}}
	candidates := GetSearchThresholdCandidates(scenes, 1)
	if len(candidates) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(candidates))
	}
	if candidates[0] != 0.5 {
		t.Errorf("expected candidate 0.5, got %v", candidates[0])
	}
}

func TestGetSearchThresholdCandidates_ManyScenes(t *testing.T) {
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
		candidates := GetSearchThresholdCandidates(scenes, tt.minDrop)
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

func TestGetSearchThresholdCandidates_MinDropLargerThanScenes(t *testing.T) {
	scenes := []Scene{
		{Score: 0.1},
		{Score: 0.2},
	}
	// minDrop=10 should only return the first candidate
	candidates := GetSearchThresholdCandidates(scenes, 10)
	if len(candidates) != 1 || candidates[0] != 0.1 {
		t.Errorf("expected [0.1], got %v", candidates)
	}
}

func TestGetSearchThresholdCandidates_DedupesScores(t *testing.T) {
	scenes := []Scene{
		{Score: 0.4},
		{Score: 0.4},
		{Score: 0.4},
		{Score: 0.4},
	}
	// All same score → only one unique candidate
	candidates := GetSearchThresholdCandidates(scenes, 1)
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
	candidates, minDrop := GetOptimalMinDrop(scenes, 0)
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
		candidates, minDrop := GetOptimalMinDrop(scenes, tt.maxCandidates)
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
	candidates, minDrop := GetOptimalMinDrop(scenes, 1)
	if len(candidates) != 1 || candidates[0] != 0.5 {
		t.Errorf("expected [0.5], got %v", candidates)
	}
	if minDrop != 1 {
		t.Errorf("expected minDrop=1, got %d", minDrop)
	}
}

func TestGetSearchThresholdCandidates_MinDropRespectedBetweenCandidates(t *testing.T) {
	// 10 scenes with distinct scores
	scenes := make([]Scene, 10)
	for i := range scenes {
		scenes[i] = Scene{Score: float64(i) * 0.1}
	}

	candidates := GetSearchThresholdCandidates(scenes, 3)
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
