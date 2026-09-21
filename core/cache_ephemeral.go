package core

import (
	"math"
	"sync"

	"gonum.org/v1/gonum/stat"
)

// ephemeralStatsCache wraps a persistent StatsCache with in-memory accumulation
// of segment QPs within a single encode. It is discarded after the search; the
// persistent cache is updated only once at the end by the caller.
type ephemeralStatsCache struct {
	mu         sync.RWMutex
	baseMean   float64
	baseStddev float64
	baseWeight int
	qps        []int
}

// newEphemeralStatsCache creates an ephemeral cache seeded from the persistent
// cache (or a fallback heuristic if the persistent cache is empty).
func newEphemeralStatsCache(base StatsCache, qpMin, qpMax int) *ephemeralStatsCache {
	e := &ephemeralStatsCache{}
	if mean, stddev, weight, ok := base.Snapshot(); ok {
		e.baseMean = mean
		e.baseStddev = stddev
		e.baseWeight = weight
	} else {
		e.baseMean = float64(qpMax-qpMin+1) / 2
		e.baseStddev = e.baseMean / 2
		e.baseWeight = 1
	}
	return e
}

// addQP records a segment's final QP so subsequent segments can benefit.
func (e *ephemeralStatsCache) addQP(qp int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.qps = append(e.qps, qp)
}

// GetMeanStdDev returns the combined mean and stddev of the persistent base
// and all accumulated segment QPs.
func (e *ephemeralStatsCache) GetMeanStdDev() (mean, stddev int) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	meanf, stddevf, _ := e.compute()
	mean = int(math.Round(meanf))
	stddev = max(1, int(math.Round(stddevf)))
	return
}

// Snapshot returns the combined historical aggregate as a single weighted run.
func (e *ephemeralStatsCache) Snapshot() (mean, stddev float64, weight int, ok bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	mean, stddev, weight = e.compute()
	return mean, stddev, weight, true
}

// compute returns the raw weighted mean, stddev, and total weight.
// Caller must hold at least e.mu.RLock.
func (e *ephemeralStatsCache) compute() (mean, stddev float64, weight int) {
	weight = e.baseWeight + len(e.qps)

	// Mean
	sumQPs := 0
	for _, qp := range e.qps {
		sumQPs += qp
	}
	mean = (e.baseMean*float64(e.baseWeight) + float64(sumQPs)) / float64(weight)

	// Stddev
	switch {
	case len(e.qps) == 0:
		stddev = e.baseStddev
	case len(e.qps) == 1:
		// One segment tells us the center shifted but gives no spread information;
		// keep the base stddev to avoid premature tightening.
		stddev = e.baseStddev
	default:
		qpf := make([]float64, len(e.qps))
		for i, qp := range e.qps {
			qpf[i] = float64(qp)
		}
		_, accStddev := stat.MeanStdDev(qpf, nil)
		stddev = (e.baseStddev*float64(e.baseWeight) + accStddev*float64(len(e.qps))) / float64(weight)
	}

	return
}
