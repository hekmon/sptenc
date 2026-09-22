package core

import (
	"math"
	"sync"

	"gonum.org/v1/gonum/stat"
)

// seedWeight is the weight of the base's snapshot in an EphemeralStatsCache: one ghost
// segment, whatever the base holds. See NewEphemeralStatsCache.
const seedWeight = 1

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
	qps        []int
}

// NewEphemeralStatsCache creates an ephemeral cache seeded from base (or a cold start
// heuristic on the given QP range if base has no history).
//
// # WHY THE BASE WEIGHS ONE SEGMENT
//
// The base is a starting point for the first segments, not knowledge about this content:
// the persistent cache aggregates other files, whose mean QP is theirs, not this one's.
// The segments of the run are the only measurements of this content, so they must take over
// as soon as they exist: after one segment the mean is halfway to it, after a handful the
// base is noise. That is what a single ghost segment does.
//
// # WHY NOT THE BASE'S OWN WEIGHT
//
// Seeding with the number of segments behind the base (a previous design) made a large cache
// unmovable: hundreds of real QPs from this file could not shift a mean built on thousands
// from other files, and every segment kept starting from the wrong place and walking from
// there. Measured with FindAllSegmentsQP on synthetic segments, cache mean 6 QP away from the
// file's: +0.7 attempt per segment with the base's weight, +0.02 with a weight of one. When
// the cache mean matches the file, the base's weight gains 0.07 attempt per segment at
// most. A bad prior costs far more than a good one saves, so the prior must be light.
//
// The same rule applies when the base is another EphemeralStatsCache on the same content
// (batchsearch candidates): its full weight gains nothing measurable there either, since the
// candidate's own segments describe the content as well as the previous candidate's did.
//
// # EDGE CASES
//
//   - No history: the cold start heuristic is the ghost segment (see coldStartStats).
//   - One accumulated QP: the mean moves, the spread is kept from the base (one value has no
//     spread, and a spread of zero would make the search step one QP at a time).
//   - Two accumulated QPs at the same value: their spread is zero and weighs two against the
//     base's one; the third segment repairs it. Measured as harmless (see above).
func NewEphemeralStatsCache(base StatsCache, qpMin, qpMax int) *EphemeralStatsCache {
	e := &EphemeralStatsCache{}
	if mean, stddev, ok := base.Snapshot(); ok {
		e.baseMean = mean
		e.baseStddev = stddev
	} else {
		e.baseMean, e.baseStddev = coldStartStats(qpMin, qpMax)
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

	meanf, stddevf := e.compute()
	mean = int(math.Round(meanf))
	stddev = max(1, int(math.Round(stddevf)))
	return
}

// Snapshot returns the combined mean and stddev of the base and all accumulated segment QPs.
func (e *EphemeralStatsCache) Snapshot() (mean, stddev float64, ok bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	mean, stddev = e.compute()
	return mean, stddev, true
}

// compute returns the raw mean and stddev of the base (weighing seedWeight) and the
// accumulated QPs (weighing one each).
// Caller must hold at least e.mu.RLock.
func (e *EphemeralStatsCache) compute() (mean, stddev float64) {
	weight := seedWeight + len(e.qps)

	// Mean
	sumQPs := 0
	for _, qp := range e.qps {
		sumQPs += qp
	}
	mean = (e.baseMean*seedWeight + float64(sumQPs)) / float64(weight)

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
		stddev = (e.baseStddev*seedWeight + accStddev*float64(len(e.qps))) / float64(weight)
	}

	return
}
