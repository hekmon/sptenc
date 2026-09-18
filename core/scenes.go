package core

import (
	"sort"

	"github.com/hekmon/sptenc/ffmpeg"
)

// GetOptimalMinDrop finds the smallest minDrop that produces at most maxCandidates candidates.
// It returns the candidate list and the effective minDrop used.
// A nil candidates slice indicates the request could not be satisfied
// (empty scenes or maxCandidates < 1).
func GetOptimalMinDrop(scenes []ffmpeg.Scene, maxCandidates int) (candidates []float64, minDrop int) {
	if len(scenes) == 0 || maxCandidates < 1 {
		return
	}
	for minDrop = 1; ; minDrop++ {
		if candidates = GetSearchThresholdCandidates(scenes, minDrop); len(candidates) <= maxCandidates {
			return
		}
	}
}

// GetSearchThresholdCandidates builds candidate thresholds using pure scene-drop logic.
// Each candidate (after the first) eliminates at least minDrop more scenes than the previous candidate.
// A nil candidates slice indicates empty input scenes.
func GetSearchThresholdCandidates(scenes []ffmpeg.Scene, minDrop int) (candidates []float64) {
	if len(scenes) == 0 {
		return
	}
	// Collect unique scores and sort them ascending
	uniqueScores := make([]float64, 0, len(scenes))
	seen := make(map[float64]bool, len(scenes))
	for _, scene := range scenes {
		if !seen[scene.Score] {
			seen[scene.Score] = true
			uniqueScores = append(uniqueScores, scene.Score)
		}
	}
	sort.Float64s(uniqueScores)
	// First candidate is always the lowest score (baseline)
	candidates = make([]float64, 1, len(scenes))
	candidates[0] = uniqueScores[0]
	// Search for candidates that drop at least minDrop between each others
	lastEliminated := 0
	for i := 1; i < len(uniqueScores); i++ {
		eliminated := 0
		for _, scene := range scenes {
			if scene.Score < uniqueScores[i] {
				eliminated++
			}
		}
		if eliminated-lastEliminated >= minDrop {
			candidates = append(candidates, uniqueScores[i])
			lastEliminated = eliminated
		}
	}
	return
}
