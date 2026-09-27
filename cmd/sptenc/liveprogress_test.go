package main

import (
	"testing"

	"github.com/hekmon/sptenc/core"
)

// TestSegmentCandidatesRender follows the live line of a segment through its search, with marks
// in place of the terminal styles: ~struck~ for a failed QP, *bold* for the QP of the VMAF search.
func TestSegmentCandidatesRender(t *testing.T) {
	failed := func(s string) string { return "~" + s + "~" }
	vmafQP := func(s string) string { return "*" + s + "*" }
	var candidates segmentCandidates
	candidates.reset()
	expect := func(step, want string) {
		t.Helper()
		if got := candidates.render(failed, vmafQP); got != want {
			t.Errorf("%s: want %q, got %q", step, want, got)
		}
	}
	expect("counting the frames", "")
	search := func(qp int, passed bool) {
		candidates.search = append(candidates.search, liveCandidate{qp: qp})
		expect("encoding", candidates.render(failed, vmafQP)) // being measured: plain
		markCandidate(candidates.search, qp, passed)
	}
	search(26, false)
	search(13, true)
	search(14, false)
	expect("the VMAF search", "~26~ 13 ~14~")
	// the CAMBI stage measures the banding of the QP found
	candidates.vmafQP = 13
	expect("the banding of the QP found", "~26~ *13* ~14~ · CAMBI")
	candidates.walk = append(candidates.walk, liveCandidate{qp: 12})
	expect("the walk", "~26~ *13* ~14~ · CAMBI 12")
	markCandidate(candidates.walk, 12, false)
	candidates.walk = append(candidates.walk, liveCandidate{qp: 11})
	markCandidate(candidates.walk, 11, true)
	expect("the walk done", "~26~ *13* ~14~ · CAMBI ~12~ 11")
	// a VMAF best effort failed, and is the QP found all the same
	candidates.reset()
	candidates.search = append(candidates.search, liveCandidate{qp: 0, done: true})
	candidates.vmafQP = 0
	expect("a VMAF best effort", "*~0~* · CAMBI")
	// the next segment starts from nothing
	candidates.reset()
	expect("reset", "")
}

func TestSegmentDoneLine(t *testing.T) {
	for _, tc := range []struct {
		segment     core.SegmentResult
		concurrency int
		want        string
	}{
		{core.SegmentResult{QP: 13, VMAFQP: 13, Frames: 575, Attempts: 3}, 1,
			"\tSegment 5: QP 13 selected for this segment of 575 frames (3 attempts)"},
		// the walk is told when it moved the QP
		{core.SegmentResult{QP: 11, VMAFQP: 13, Frames: 575, Attempts: 5, CAMBIWalk: []int{12, 11}}, 1,
			"\tSegment 5: QP 11 selected for this segment of 575 frames (5 attempts), lowered from QP 13 by the CAMBI gate (walk: 12 11)"},
		// a CAMBI best effort keeping the QP of the VMAF search: the warning tells, not this line
		{core.SegmentResult{QP: 13, VMAFQP: 13, Frames: 575, Attempts: 16, CAMBIWalk: []int{12, 11, 10}}, 3,
			"\tSegment 5: QP 13 selected for this segment of 575 frames (16 attempts) [worker #2]"},
	} {
		if got := segmentDoneLine(4, tc.segment, 2, tc.concurrency); got != tc.want {
			t.Errorf("want %q, got %q", tc.want, got)
		}
	}
}
