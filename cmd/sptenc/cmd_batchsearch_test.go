package main

import "testing"

// TestBatchStatusRender follows the candidates line of batchsearch through a search, with the marks
// of TestSegmentCandidatesRender: ~struck~ for a candidate whose file was not smaller than every
// file before it, _underlined_ for one whose file was, *bold* for the best so far, and ^faint^ for
// the candidates not tested yet.
func TestBatchStatusRender(t *testing.T) {
	nonImproving := func(s string) string { return "~" + s + "~" }
	improving := func(s string) string { return "_" + s + "_" }
	best := func(s string) string { return "*" + s + "*" }
	future := func(s string) string { return "^" + s + "^" }
	bs := batchStatus{candidates: []float64{8, 9, 10.5, 12, 14, 16}}
	bs.sizes = make([]int64, len(bs.candidates))
	expect := func(step, want string) {
		t.Helper()
		if got := bs.render(nonImproving, improving, best, future); got != want {
			t.Errorf("%s: want %q, got %q", step, want, got)
		}
	}
	// done is what the search does once a candidate is encoded: its size, the best, the next one
	done := func(size int64) {
		bs.sizes[bs.currentCandidateIndex] = size
		bs.ComputeBest()
		bs.currentCandidateIndex++
	}
	expect("the first candidate", "8 ^9^ ^10.5^ ^12^ ^14^ ^16^")
	done(1000)
	expect("the first one done", "*_8_* 9 ^10.5^ ^12^ ^14^ ^16^")
	done(1100)
	expect("a strike", "*_8_* ~9~ 10.5 ^12^ ^14^ ^16^")
	done(1050)
	expect("smaller than the one before, not than the best", "*_8_* ~9~ ~10.5~ 12 ^14^ ^16^")
	done(900)
	expect("a new best", "_8_ ~9~ ~10.5~ *_12_* 14 ^16^")
	done(900)
	expect("as small as the best, which stays the first one", "_8_ ~9~ ~10.5~ *_12_* ~14~ 16")
	done(800)
	expect("all done", "_8_ ~9~ ~10.5~ _12_ ~14~ *_16_*")
}
