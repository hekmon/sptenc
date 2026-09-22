package core

import (
	"math"
	"sync"
	"testing"
)

func TestEphemeralStatsCache_EmptyBase(t *testing.T) {
	base := &mockStatsCache{mean: 26, stddev: 13} // empty persistent cache behavior
	e := NewEphemeralStatsCache(base, 0, 51)

	mean, stddev := e.GetMeanStdDev()
	if mean != 26 {
		t.Errorf("expected initial mean 26, got %d", mean)
	}
	if stddev != 13 {
		t.Errorf("expected initial stddev 13, got %d", stddev)
	}

	// Add one QP — mean shifts, stddev stays at base (no spread info yet)
	e.addQP(20)
	mean, stddev = e.GetMeanStdDev()
	if mean != 23 {
		t.Errorf("expected mean 23 after one QP, got %d", mean)
	}
	if stddev != 13 {
		t.Errorf("expected stddev unchanged at 13 after one QP, got %d", stddev)
	}

	// Add second QP — stddev now computed from sample
	e.addQP(22)
	mean, stddev = e.GetMeanStdDev()
	// (26*1 + 20 + 22) / 3 = 22.67 → 23
	if mean != 23 {
		t.Errorf("expected mean 23 after two QPs, got %d", mean)
	}
	// sample stddev of [20,22] is 1.0; combined: (13*1 + 1.0*2) / 3 = 5.0 → 5
	if stddev != 5 {
		t.Errorf("expected stddev 5 after two QPs, got %d", stddev)
	}
}

func TestEphemeralStatsCache_BaseWeighsOneSegment(t *testing.T) {
	// The base is a starting point only: whatever history it aggregates, it weighs one
	// segment, so the segments of the run take over as soon as they exist (see
	// NewEphemeralStatsCache). A base mean far from this content must be overtaken
	// within a handful of segments.
	base := &mockStatsCache{mean: 40, stddev: 3}
	e := NewEphemeralStatsCache(base, 0, 51)

	mean, stddev := e.GetMeanStdDev()
	if mean != 40 {
		t.Errorf("expected initial mean 40 from base, got %d", mean)
	}
	if stddev != 3 {
		t.Errorf("expected initial stddev 3 from base, got %d", stddev)
	}

	// One segment: halfway there, spread kept from the base
	e.addQP(20)
	mean, stddev = e.GetMeanStdDev()
	// (40 + 20) / 2 = 30
	if mean != 30 {
		t.Errorf("expected mean 30 after one QP, got %d", mean)
	}
	if stddev != 3 {
		t.Errorf("expected stddev still 3 after one QP, got %d", stddev)
	}

	// Three segments: the base is already a minority
	e.addQP(21)
	e.addQP(22)
	mean, _ = e.GetMeanStdDev()
	// (40 + 20 + 21 + 22) / 4 = 25.75 → 26
	if mean != 26 {
		t.Errorf("expected mean 26 after three QPs, got %d", mean)
	}

	// Nine segments: the base is noise
	for _, qp := range []int{20, 21, 22, 20, 21, 22} {
		e.addQP(qp)
	}
	mean, stddev = e.GetMeanStdDev()
	// (40 + 3*(20+21+22)) / 10 = 22.9 → 23
	if mean != 23 {
		t.Errorf("expected mean 23 after nine QPs, got %d", mean)
	}
	// sample stddev of three times [20,21,22] is 0.866; combined: (3 + 0.866*9) / 10 = 1.08 → 1
	if stddev != 1 {
		t.Errorf("expected stddev 1 after nine QPs, got %d", stddev)
	}
}

func TestEphemeralStatsCache_ConcurrentAccess(t *testing.T) {
	base := &mockStatsCache{mean: 26, stddev: 13}
	e := NewEphemeralStatsCache(base, 0, 51)

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(qp int) {
			defer wg.Done()
			e.addQP(qp)
			_, _ = e.GetMeanStdDev()
		}(i % 52)
	}
	wg.Wait()

	if len(e.qps) != 100 {
		t.Errorf("expected 100 accumulated QPs, got %d", len(e.qps))
	}
}

func TestEphemeralStatsCache_Snapshot(t *testing.T) {
	base := &mockStatsCache{mean: 10, stddev: 2}
	e := NewEphemeralStatsCache(base, 0, 51)

	// Snapshot before adding QPs should reflect the base
	mean, stddev, ok := e.Snapshot()
	if !ok {
		t.Fatal("expected ok=true from Snapshot")
	}
	if mean != 10.0 {
		t.Errorf("expected mean 10.0, got %v", mean)
	}
	if stddev != 2.0 {
		t.Errorf("expected stddev 2.0, got %v", stddev)
	}

	// Add QPs and verify Snapshot reflects combined data, base weighing one segment
	e.addQP(12)
	e.addQP(14)
	mean, stddev, ok = e.Snapshot()
	if !ok {
		t.Fatal("expected ok=true from Snapshot after addQP")
	}
	// (10 + 12 + 14) / 3 = 12
	if math.Abs(mean-12) > 1e-9 {
		t.Errorf("expected mean 12, got %v", mean)
	}
	// sample stddev of [12,14] is 1.414; combined: (2 + 1.414*2) / 3 = 1.609
	expectedStddev := (2 + math.Sqrt2*2) / 3
	if math.Abs(stddev-expectedStddev) > 1e-9 {
		t.Errorf("expected stddev %v, got %v", expectedStddev, stddev)
	}
}

func TestEphemeralStatsCache_WrapsAnotherEphemeral(t *testing.T) {
	// An ephemeral cache is a StatsCache like any other: it can seed another one, which then
	// starts from everything the first one knows (its own base and the QPs it accumulated),
	// as one ghost segment like any other base. FindAllSegmentsQP wraps the cache it is given
	// for the duration of one search, and batchsearch gives every candidate the same ephemeral
	// cache fed with the previous candidates (see TestEphemeralStatsCache_AddRun), so each
	// search starts from what the previous ones found.
	persistent := &mockStatsCache{mean: 20, stddev: 5}
	inner := NewEphemeralStatsCache(persistent, 0, 51)
	inner.addQP(22)
	inner.addQP(24) // what a first search found

	// Outer ephemeral seeds from inner, as FindAllSegmentsQP would if it was given inner
	outer := NewEphemeralStatsCache(inner, 0, 51)
	mean, stddev := outer.GetMeanStdDev()
	// inner: (20 + 22 + 24) / 3 = 22, stddev (5 + 1.414*2) / 3 = 2.61 → 3
	if mean != 22 {
		t.Errorf("expected mean 22 before any segment, got %d", mean)
	}
	if stddev != 3 {
		t.Errorf("expected stddev 3 before any segment, got %d", stddev)
	}

	outer.addQP(30) // one segment of a second search
	mean, stddev = outer.GetMeanStdDev()
	// (22 + 30) / 2 = 26: inner weighs one segment, not its base's and its two QPs
	if mean != 26 {
		t.Errorf("expected mean 26, got %d", mean)
	}
	if stddev != 3 {
		t.Errorf("expected stddev kept at 3 after one segment, got %d", stddev)
	}
}

func TestEphemeralStatsCache_AddRun(t *testing.T) {
	// batchsearch: one ephemeral cache over the persistent one for the whole run, every
	// candidate search wraps it and, once done, feeds its QPs back so the next candidate
	// starts from them.
	persistent := &mockStatsCache{mean: 20, stddev: 5}
	run := NewEphemeralStatsCache(persistent, 0, 51)

	// First candidate: what FindAllSegmentsQP sees before any result
	first := NewEphemeralStatsCache(run, 0, 51)
	if mean, _ := first.GetMeanStdDev(); mean != 20 {
		t.Fatalf("first candidate should start from the persistent mean 20, got %d", mean)
	}
	run.AddRun([]int{30, 32, 34, 36})

	// Second candidate starts from persistent + first candidate
	second := NewEphemeralStatsCache(run, 0, 51)
	mean, stddev := second.GetMeanStdDev()
	// (20 + 30+32+34+36) / 5 = 30.4 → 30
	if mean != 30 {
		t.Errorf("expected mean 30, got %d", mean)
	}
	// sample stddev of [30,32,34,36] is 2.58; combined: (5 + 2.58*4) / 5 = 3.06 → 3
	if stddev != 3 {
		t.Errorf("expected stddev 3, got %d", stddev)
	}
	// Runs are not deduplicated: the same QPs again weigh twice
	run.AddRun([]int{30, 32, 34, 36})
	snapMean, _, _ := run.Snapshot()
	// (20 + 2*132) / 9 = 31.56
	if math.Abs(snapMean-284.0/9) > 1e-9 {
		t.Errorf("expected mean %v after two identical runs, got %v", 284.0/9, snapMean)
	}
	// An empty run changes nothing
	run.AddRun(nil)
	if again, _, _ := run.Snapshot(); again != snapMean {
		t.Errorf("expected mean unchanged at %v after an empty run, got %v", snapMean, again)
	}
}
