package core

import (
	"sort"
	"time"
)

// FilterShortScenes removes scene boundaries that would create segments shorter
// than minDuration. It is applied after scene detection and threshold filtering
// but before splitting, so the QP search and VMAF evaluation operate on
// segments that are long enough to be encoder-meaningful and to yield
// statistically valid percentile metrics (p1 needs ≥100 frames, p5 needs ≥20).
//
// # WHY THIS EXISTS
//
// Scene detection thresholds alone cannot eliminate very short segments on
// fast-cut content (e.g. anime) without destroying legitimate scene structure.
// Empirical data on a typical anime episode at threshold 14 shows 413 scenes
// with 189 sub-second segments (45.8 %). Raising the threshold to 31 reduces
// that to 49 scenes but still leaves 9 sub-second segments (18.4 %). The only
// way to eliminate all micro-segments via threshold is to raise it so high
// that the video collapses to ~20 chunks — losing most legitimate boundaries.
//
// Segments under ~1 second are harmful for three reasons:
//  1. I-frame overhead: each boundary forces a keyframe; sub-10-frame runs
//     never let B/P-frame referencing amortize the intra cost.
//  2. VMAF percentiles become meaningless: p1 needs ≥100 frames to be
//     statistically useful, p5 needs ≥20. A 3-frame segment has no reliable
//     percentile metrics.
//  3. QP search convergence is unstable: the cache records statistics for
//     segments whose VMAF variance is dominated by sample-size noise.
//
// # WHY DURATION-BASED, NOT FRAME-BASED
//
// Scene boundaries are already expressed as timestamps (time.Duration). Using
// frames would require a reliable frame rate, introduce rounding issues, and
// is less intuitive for users ("4 seconds" vs "96 frames at 24 fps").
//
// # WHY GREEDY SHORTEST-FIRST
//
// The algorithm repeatedly finds the single shortest segment and merges it.
// This is predictable, handles clusters of micro-scenes naturally (a common
// pattern in anime: 3-frame, 4-frame, 5-frame bursts), and converges in a
// single pass without backtracking.
//
// # WHY MERGE INTO THE SHORTER NEIGHBOUR, NOT THE WEAKER BOUNDARY
//
// Merging across the weaker boundary (lower scdet score) was considered, as it
// respects visual coherence. The problem is drowning: a 3-frame segment merged
// into a 300-frame neighbour becomes 1 % of the merged segment. If those 3
// frames contain a quality problem (compression artifact, source defect), the
// VMAF p1/p5 of the 303-frame segment will almost certainly not detect it.
// The segment passes thresholds it should have failed.
//
// For a tool whose promise is provable per-segment quality floors, creating
// invisible blind spots is worse than splitting a visually coherent unit.
// Merging into the shorter neighbour limits the blast radius: a 3-frame
// segment merged into a 30-frame neighbour is still 10 % of the total —
// detectable by percentile metrics.
//
// A duration-ratio tiebreaker (merge across weaker boundary when neighbours
// are within N×) was considered but rejected because no empirically justified
// N was available. Shorter-neighbour is the only heuristic defensible without
// a magic number.
//
// EDGE CASES
//
//   - minDuration == 0: returns scenes unchanged (fast path).
//   - Total video shorter than minDuration: returns empty (single segment).
//   - Single scene input + minDuration > 0: still single segment.
//   - Cluster of consecutive short segments: handled iteratively; each merge
//     may create a new segment that is still short, which gets merged again.
func FilterShortScenes(scenes []Scene, totalDuration, minDuration time.Duration) (filtered []Scene) {
	if minDuration <= 0 || len(scenes) == 0 {
		return scenes
	}
	filtered = make([]Scene, len(scenes))
	copy(filtered, scenes)
	// Build segment durations from boundaries.
	// segments[i] is the duration of the segment starting at boundary i-1
	// (or time zero) and ending at boundary i (or totalDuration).
	durations := make([]time.Duration, 0, len(filtered)+1)
	durations = append(durations, filtered[0].Start)
	for i := 1; i < len(filtered); i++ {
		durations = append(durations, filtered[i].Start-filtered[i-1].Start)
	}
	durations = append(durations, totalDuration-filtered[len(filtered)-1].Start)
	// Greedy loop: repeatedly find the shortest sub-minimum segment and merge
	// it into its shorter neighbour until all segments satisfy the floor.
	for {
		// Find the shortest segment
		shortestIdx := -1
		for i, d := range durations {
			if d < minDuration {
				if shortestIdx == -1 || d < durations[shortestIdx] {
					shortestIdx = i
				}
			}
		}
		if shortestIdx == -1 {
			break // all segments are long enough, greedy loop done
		}
		// If a segment too short has been found
		switch shortestIdx {
		case 0:
			// First segment is too short: merge right by removing the first boundary.
			durations[1] += durations[0]
			durations = durations[1:]
			filtered = filtered[1:]
		case len(durations) - 1:
			// Last segment is too short: merge left by removing the last boundary.
			durations[len(durations)-2] += durations[len(durations)-1]
			durations = durations[:len(durations)-1]
			filtered = filtered[:len(filtered)-1]
		default:
			// Middle segment: merge into the shorter neighbour. In case of a tie
			// the left neighbour is preferred for determinism.
			leftDur := durations[shortestIdx-1]
			rightDur := durations[shortestIdx+1]
			if leftDur <= rightDur {
				// Merge left: remove boundary shortestIdx-1.
				durations[shortestIdx-1] += durations[shortestIdx]
				durations = append(durations[:shortestIdx], durations[shortestIdx+1:]...)
				filtered = append(filtered[:shortestIdx-1], filtered[shortestIdx:]...)
			} else {
				// Merge right: remove boundary shortestIdx.
				durations[shortestIdx] += durations[shortestIdx+1]
				durations = append(durations[:shortestIdx+1], durations[shortestIdx+2:]...)
				filtered = append(filtered[:shortestIdx], filtered[shortestIdx+1:]...)
			}
		}
		// merging is done...
		if len(filtered) == 0 {
			break // ... but now entire video is a single segment, nothing to do left
		}
		// ... some scenes remain, continue checking for too short segments
	}
	return
}

// GetOptimalMinDrop finds the smallest minDrop that produces at most maxCandidates candidates.
// It returns the candidate list and the effective minDrop used.
// A nil candidates slice indicates the request could not be satisfied
// (empty scenes or maxCandidates < 1).
func GetOptimalMinDrop(scenes []Scene, maxCandidates int) (candidates []float64, minDrop int) {
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
func GetSearchThresholdCandidates(scenes []Scene, minDrop int) (candidates []float64) {
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
