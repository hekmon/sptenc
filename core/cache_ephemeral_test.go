package core

import (
	"math"
	"sync"
	"testing"
)

// weightedMockStatsCache is a StatsCache with explicit snapshot weight.
type weightedMockStatsCache struct {
	mean   int
	stddev int
	weight int
}

func (m *weightedMockStatsCache) GetMeanStdDev() (mean, stddev int) {
	return m.mean, m.stddev
}

func (m *weightedMockStatsCache) Snapshot() (mean, stddev float64, weight int, ok bool) {
	return float64(m.mean), float64(m.stddev), m.weight, true
}

func TestEphemeralStatsCache_EmptyBase(t *testing.T) {
	base := &mockStatsCache{mean: 26, stddev: 13} // empty persistent cache behavior
	e := newEphemeralStatsCache(base, 0, 51)

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

func TestEphemeralStatsCache_WithSolidBase(t *testing.T) {
	// Simulate a solid persistent cache: mean=15, stddev=3, weight=100
	base := &weightedMockStatsCache{mean: 15, stddev: 3, weight: 100}
	e := newEphemeralStatsCache(base, 0, 51)

	mean, stddev := e.GetMeanStdDev()
	if mean != 15 {
		t.Errorf("expected initial mean 15 from base, got %d", mean)
	}
	if stddev != 3 {
		t.Errorf("expected initial stddev 3 from base, got %d", stddev)
	}

	// Add one QP — base still dominates (100 vs 1)
	e.addQP(20)
	mean, stddev = e.GetMeanStdDev()
	// (15*100 + 20) / 101 = 15.05 → 15
	if mean != 15 {
		t.Errorf("expected mean still 15 (100 vs 1 weight), got %d", mean)
	}
	// stddev stays at base because len(qps)==1
	if stddev != 3 {
		t.Errorf("expected stddev still 3, got %d", stddev)
	}

	// Add several more QPs so accumulated weight becomes significant
	for _, qp := range []int{18, 22, 19, 21} {
		e.addQP(qp)
	}
	mean, stddev = e.GetMeanStdDev()
	// Weighted mean: (15*100 + 20+18+22+19+21) / 105 = (1500 + 100) / 105 ≈ 15.2 → 15
	if mean != 15 {
		t.Errorf("expected mean ~15 with solid base, got %d", mean)
	}
	// Stddev should still be close to 3 (base dominates)
	if stddev < 2 || stddev > 4 {
		t.Errorf("expected stddev near 3 with solid base, got %d", stddev)
	}
}

func TestEphemeralStatsCache_ConcurrentAccess(t *testing.T) {
	base := &mockStatsCache{mean: 26, stddev: 13}
	e := newEphemeralStatsCache(base, 0, 51)

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
	base := &weightedMockStatsCache{mean: 10, stddev: 2, weight: 50}
	e := newEphemeralStatsCache(base, 0, 51)

	// Snapshot before adding QPs should reflect the base
	mean, stddev, weight, ok := e.Snapshot()
	if !ok {
		t.Fatal("expected ok=true from Snapshot")
	}
	if weight != 50 {
		t.Errorf("expected weight 50, got %d", weight)
	}
	if mean != 10.0 {
		t.Errorf("expected mean 10.0, got %v", mean)
	}
	if stddev != 2.0 {
		t.Errorf("expected stddev 2.0, got %v", stddev)
	}

	// Add QPs and verify Snapshot reflects combined data
	e.addQP(12)
	e.addQP(14)
	mean, stddev, weight, ok = e.Snapshot()
	if !ok {
		t.Fatal("expected ok=true from Snapshot after addQP")
	}
	if weight != 52 {
		t.Errorf("expected weight 52, got %d", weight)
	}
	// Weighted mean: (10*50 + 12 + 14) / 52 = 536/52 ≈ 10.3077
	expectedMean := (10.0*50 + 12 + 14) / 52
	if math.Abs(mean-expectedMean) > 1e-9 {
		t.Errorf("expected mean %v, got %v", expectedMean, mean)
	}
}

func TestEphemeralStatsCache_WrapsAnotherEphemeral(t *testing.T) {
	// An ephemeral cache is a StatsCache like any other: it can seed another one, which then
	// starts from everything the first one knows (its own base and the QPs it accumulated).
	// Nothing does it today: FindAllSegmentsQP wraps the cache it is given for the duration of
	// one search, and batchsearch gives the same persistent cache to every candidate, so each
	// one starts from the history only, not from what the previous candidates found. This test
	// keeps the wrapping valid for a caller willing to carry that knowledge from a search to
	// the next.
	persistent := &weightedMockStatsCache{mean: 20, stddev: 5, weight: 10}
	inner := newEphemeralStatsCache(persistent, 0, 51)
	inner.addQP(22)
	inner.addQP(24) // what a first search found

	// Outer ephemeral seeds from inner, as FindAllSegmentsQP would if it was given inner
	outer := newEphemeralStatsCache(inner, 0, 51)
	outer.addQP(23) // one segment of a second search

	mean, stddev := outer.GetMeanStdDev()
	// Total weight: 10 (persistent) + 2 (first search) + 1 (second search) = 13
	// Weighted mean: (20*10 + 22 + 24 + 23) / 13 = 269/13 ≈ 20.69 → 21
	if mean != 21 {
		t.Errorf("expected mean 21, got %d", mean)
	}
	// Stddev should be positive
	if stddev < 1 {
		t.Errorf("expected stddev >= 1, got %d", stddev)
	}
}
