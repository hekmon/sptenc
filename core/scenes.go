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

// Candidate is a threshold along with the scene boundaries it produces.
// See GetCandidates for what a candidate is (and is not).
type Candidate struct {
	Threshold float64
	Scenes    []Scene
}

// SelectScenes returns the scene boundaries a given threshold produces: boundaries scoring
// below the threshold are dropped first, then the ones creating segments shorter than
// minDuration are merged (see FilterShortScenes).
//
// Every command must get its scenes this way, in this order: the threshold picks the cuts,
// the minimum duration is a guardrail applied on the cuts that were picked. The input is
// never modified, every call starts from the full list: GetCandidates explains why both
// points are load-bearing.
func SelectScenes(scenes []Scene, threshold float64, totalDuration, minDuration time.Duration) []Scene {
	selected := make([]Scene, 0, len(scenes))
	for _, scene := range scenes {
		if scene.Score >= threshold {
			selected = append(selected, scene)
		}
	}
	return FilterShortScenes(selected, totalDuration, minDuration)
}

// GetCandidates returns every threshold worth testing up to maxThreshold, in ascending order,
// each one along with the scenes it produces. It is the entry point of a threshold search
// (batchsearch, thresholds); GetOptimalMinDrop then thins the list out.
//
// # A CANDIDATE IS NOT A SCENE MARKER
//
// This is the usual source of confusion. A scene marker is a time and a score. A candidate
// is a threshold: a bare score used as a cut-off line across the whole list of markers.
// Testing a candidate does not select "its" marker, it selects every marker scoring at or
// above it:
//
//	markers (time/score):  8s/40   10s/15   40s/35
//
//	candidate 15 -> keeps 8s, 10s, 40s
//	candidate 35 -> keeps 8s, 40s
//	candidate 40 -> keeps 8s
//
// Candidates are taken from the markers scores only because the set of kept markers changes
// when the line crosses a score, and only then: any threshold between 15 and 35 gives exactly
// what 35 gives. Scores are the only thresholds worth testing.
//
// # WHY SHORT SEGMENTS ARE MERGED PER CANDIDATE, NOT ONCE ON THE FULL LIST
//
// Merging once upfront then applying each threshold is simpler and was the original design.
// It is wrong. Which segments are too short depends on which markers are kept, so it
// depends on the candidate (5s minimum in this example):
//
//	candidate 15: 8s, 10s, 40s -> the 8s-10s segment is too short -> the merge removes the 8s marker
//	candidate 35: 8s, 40s      -> nothing is too short            -> no merge at all
//
// Merging is not a cleanup of the markers list, it is a repair of one particular set of
// markers. The upfront merge is the repair candidate 15 needs. Reused for every candidate, it
// removes the 8s marker for 35 and 40 too, where it was never an issue: a weak marker (10s/15)
// deletes a strong one (8s/40) before being dropped itself by the threshold. Consequences:
//   - the search measures file sizes on scenes poorer than they should be (on a simulated
//     95 min film, about a hundred markers scoring 40+ were lost this way at each candidate);
//   - the scenes of a candidate depend on the minimum threshold used for detection, which is
//     only supposed to bound the search;
//   - the best threshold does not give the same scenes once handed to encode, which only ever
//     sees the markers at or above its own threshold. Finding a threshold on one episode to
//     encode the rest of the season is the main use of a search.
//
// # WHY EACH CANDIDATE STARTS FROM THE FULL LIST, NOT FROM THE PREVIOUS CANDIDATE RESULT
//
// Chaining (dropping the next scores from the already merged list of the previous candidate)
// looks equivalent. It is not: it has the same flaw as the upfront merge, one step at a
// time, and adds another one: the scenes of a candidate would depend on which candidates were
// computed before it, so on minDrop and maxCandidates.
//
// Starting from the full list, the scenes of a candidate depend on two things only: the
// threshold and the minimum duration. This is exactly what encode computes (SelectScenes), so
// a candidate is reproducible by construction. TestGetCandidates enforces it.
//
// # WHY EVERY UNIQUE SCORE IS COMPUTED BEFORE THINNING OUT, NOT AFTER
//
// Thinning out first (scene drop counted on raw scores) then computing the scenes of the few
// survivors was tried. The drop was then counted on markers that mostly get merged away: a
// minDrop of 3 meant "3 raw markers", not "3 scenes", and survivors could end up with identical
// scenes, each one wasting a complete search pass (days of encoding). Thinning out must see
// real scenes, so it must come last.
//
// # THE ORDER OF OPERATIONS
//
//  1. scenes detection                                   (ffmpeg, applies the minimum threshold)
//  2. candidates are capped to maxThreshold              (here)
//  3. raw candidates: the unique scores in that range    (here)
//  4. scenes of each raw candidate, from the full list   (here, with SelectScenes)
//  5. candidates with the same scenes are dropped        (here)
//  6. candidates are thinned out by scene drop to fit    (GetOptimalMinDrop, FilterCandidatesByDrop)
//     the maximum number of candidates
//
// # EDGE CASES
//
//   - maxThreshold caps the candidates, not the markers: a marker scoring above maxThreshold
//     is kept by every candidate, as it would be by encode. Removing it from the list would
//     break reproducibility.
//   - Several thresholds producing the same scenes: the lowest one is kept. Any of them would
//     do for this input; there is no principled choice for another input.
//   - Candidates are compared by boundaries times only: two lists with the same times are the
//     same scenes whatever the scores.
//   - minDuration == 0: no merge, every unique score within range is a distinct candidate.
//   - No marker within range: returns no candidates.
func GetCandidates(scenes []Scene, maxThreshold float64, totalDuration, minDuration time.Duration) (candidates []Candidate) {
	// Collect unique scores within range and sort them ascending
	thresholds := make([]float64, 0, len(scenes))
	seen := make(map[float64]bool, len(scenes))
	for _, scene := range scenes {
		if scene.Score <= maxThreshold && !seen[scene.Score] {
			seen[scene.Score] = true
			thresholds = append(thresholds, scene.Score)
		}
	}
	sort.Float64s(thresholds)
	// Compute the scenes of each one, always from the full list
	candidates = make([]Candidate, 0, len(thresholds))
	for _, threshold := range thresholds {
		selected := SelectScenes(scenes, threshold, totalDuration, minDuration)
		if len(candidates) > 0 && sameBoundaries(candidates[len(candidates)-1].Scenes, selected) {
			continue
		}
		candidates = append(candidates, Candidate{Threshold: threshold, Scenes: selected})
	}
	return
}

func sameBoundaries(a, b []Scene) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Start != b[i].Start {
			return false
		}
	}
	return true
}

// GetOptimalMinDrop finds the smallest minDrop that thins candidates out to at most maxCandidates
// (step 6 of the section comment above). It returns the kept candidates and the minDrop used.
// A nil slice indicates the request could not be satisfied (no candidates or maxCandidates < 1).
func GetOptimalMinDrop(candidates []Candidate, maxCandidates int) (kept []Candidate, minDrop int) {
	if len(candidates) == 0 || maxCandidates < 1 {
		return
	}
	for minDrop = 1; ; minDrop++ {
		if kept = FilterCandidatesByDrop(candidates, minDrop); len(kept) <= maxCandidates {
			return
		}
	}
}

// FilterCandidatesByDrop thins out candidates (as returned by GetCandidates) using pure scene-drop
// logic: the first candidate is always kept as the baseline, then a candidate is only kept if it has
// at least minDrop scenes less than the previous kept one.
//
// # WHY SCENES ARE COUNTED ONCE MERGED
//
// The drop is expressed in real scenes, the ones that will be encoded: this is what makes two
// candidates worth a search pass each. Counting raw markers instead was tried and rejected, see
// GetCandidates.
//
// # EDGE CASES
//
//   - The number of scenes almost always decreases as the threshold rises, but this is not
//     guaranteed: dropping a weak marker can avoid a merge that was removing a strong one. A
//     candidate with as many or more scenes than the previous kept one is simply skipped.
//   - Two candidates with the same number of scenes but different boundaries: the second one is
//     skipped whatever minDrop is (a drop of 0 never satisfies minDrop >= 1). Same count, slightly
//     shifted cuts: not worth a search pass.
func FilterCandidatesByDrop(candidates []Candidate, minDrop int) (kept []Candidate) {
	if len(candidates) == 0 {
		return
	}
	kept = make([]Candidate, 1, len(candidates))
	kept[0] = candidates[0]
	for _, candidate := range candidates[1:] {
		if len(kept[len(kept)-1].Scenes)-len(candidate.Scenes) >= minDrop {
			kept = append(kept, candidate)
		}
	}
	return
}
