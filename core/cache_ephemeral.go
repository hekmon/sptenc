package core

import (
	"math"
	"sync"

	"gonum.org/v1/gonum/stat"
)

// EphemeralStatsCache wraps a StatsCache with in-memory accumulation of segment QPs.
// It is a StatsCache itself, so it can wrap another EphemeralStatsCache: FindAllSegmentsQP
// wraps whatever cache it is given for the duration of one search, and a caller running
// several searches on the same content (batchsearch) can wrap the persistent cache once,
// give that to every search and feed it their results with AddRun, so that each search
// starts from what the previous ones found. It is never written to disk: the persistent
// cache is updated only once at the end by the caller, with the run worth keeping.
type EphemeralStatsCache struct {
	mu         sync.RWMutex
	baseMean   float64
	baseStddev float64
	baseWeight int
	qps        []int
}

// NewEphemeralStatsCache creates an ephemeral cache seeded from base (or a cold start
// heuristic on the given QP range if base has no history).
func NewEphemeralStatsCache(base StatsCache, qpMin, qpMax int) *EphemeralStatsCache {
	e := &EphemeralStatsCache{}
	if mean, stddev, weight, ok := base.Snapshot(); ok {
		e.baseMean = mean
		e.baseStddev = stddev
		e.baseWeight = weight
	} else {
		e.baseMean, e.baseStddev = coldStartStats(qpMin, qpMax)
		e.baseWeight = 1
	}
	return e
}

// AddRun records the final QPs of a whole search so the next searches seeded from this
// cache benefit from it. Unlike the persistent cache, runs are not deduplicated: an
// ephemeral cache only lives for one process, on one content, and every run on it is
// wanted data.
func (e *EphemeralStatsCache) AddRun(qps []int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.qps = append(e.qps, qps...)
}

// addQP records a segment's final QP so subsequent segments can benefit.
func (e *EphemeralStatsCache) addQP(qp int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.qps = append(e.qps, qp)
}

// GetMeanStdDev returns the combined mean and stddev of the base and all accumulated
// segment QPs.
func (e *EphemeralStatsCache) GetMeanStdDev() (mean, stddev int) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	meanf, stddevf, _ := e.compute()
	mean = int(math.Round(meanf))
	stddev = max(1, int(math.Round(stddevf)))
	return
}

// Snapshot returns the combined historical aggregate as a single weighted run.
func (e *EphemeralStatsCache) Snapshot() (mean, stddev float64, weight int, ok bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	mean, stddev, weight = e.compute()
	return mean, stddev, weight, true
}

// compute returns the raw weighted mean, stddev, and total weight.
// Caller must hold at least e.mu.RLock.
func (e *EphemeralStatsCache) compute() (mean, stddev float64, weight int) {
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
