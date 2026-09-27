package core

import (
	"context"
	"fmt"
	"math"
)

// CAMBIOffValue disables a CAMBI threshold (see NewCAMBIChecker).
const CAMBIOffValue = -1

// CAMBIChecker holds the thresholds gating the banding the encoder added to a segment (see
// BandingStats). They are ceilings where the VMAF thresholds are floors: the added banding must
// be at most the mean threshold on average over the frames, and at most the max threshold on the
// worst frame. The zero value is off: no banding is measured (see searchSegmentCAMBI).
//
// # WHY THE MEAN, NOT THE HARMONIC MEAN
//
// The harmonic mean libvmaf pools, n / Σ 1/(x+1) − 1, leans toward the lowest values. For a VMAF
// score they are the worst frames, which makes it stricter than the mean, and the default VMAF
// gate. For the added banding they are the frames with nothing added, most of them on real
// content: the harmonic mean hides the banded ones. 90 frames at 0 and 10 at 5 average 0.5, their
// harmonic mean is 0.09; 99 frames at 0 and one at 24, the highest CAMBI libvmaf's documentation
// reports ("unwatchable"), average 0.24 against 0.01. The mean sets the level and the worst frame
// bounds the tail: a pooling stricter than the mean would have to weigh the high values, as a
// percentile does.
type CAMBIChecker struct {
	mean float64
	max  float64
	on   bool
}

// NewCAMBIChecker creates the checker of the CAMBI thresholds: the banding the encoder added,
// averaged over the frames of a segment (mean) and on its worst frame (max). CAMBIOffValue
// disables a threshold, and both turn the gate off, which is no error: that is how it is turned
// off. 0 is a threshold, no added banding at all. Any other value must be positive and finite:
// CAMBI has no fixed ceiling ("the maximum CAMBI observed in a sequence is 24", libvmaf's CAMBI
// documentation).
func NewCAMBIChecker(mean, max float64) (cc CAMBIChecker, err error) {
	for _, threshold := range []struct {
		name  string
		value float64
	}{{"mean", mean}, {"max", max}} {
		if threshold.value != CAMBIOffValue &&
			(threshold.value < 0 || math.IsNaN(threshold.value) || math.IsInf(threshold.value, 0)) {
			err = fmt.Errorf("CAMBI %s threshold %v is invalid: a positive number, or %d to disable it",
				threshold.name, threshold.value, CAMBIOffValue)
			return
		}
	}
	return CAMBIChecker{mean: mean, max: max, on: mean != CAMBIOffValue || max != CAMBIOffValue}, nil
}

// Enabled reports whether at least one threshold is active: the CAMBI gate is on.
func (cc CAMBIChecker) Enabled() bool {
	return cc.on
}

// Validate checks the banding a segment's encode added against every active threshold.
func (cc CAMBIChecker) Validate(bs BandingStats) bool {
	return cc.ValidateMean(bs) && (cc.max == CAMBIOffValue || bs.AddedMax <= cc.max)
}

// ValidateMean checks the banding a segment's encode added against the mean threshold alone
// (passing when it is disabled): what a CAMBI best effort keeps (see searchSegmentCAMBI).
func (cc CAMBIChecker) ValidateMean(bs BandingStats) bool {
	return cc.mean == CAMBIOffValue || bs.AddedMean <= cc.mean
}

// Thresholds returns the active thresholds by name, "mean" and "max": none when the gate is off.
func (cc CAMBIChecker) Thresholds() map[string]float64 {
	thresholds := make(map[string]float64, 2)
	if !cc.on {
		return thresholds
	}
	if cc.mean != CAMBIOffValue {
		thresholds["mean"] = cc.mean
	}
	if cc.max != CAMBIOffValue {
		thresholds["max"] = cc.max
	}
	return thresholds
}

// CAMBIBestEffort tells which CAMBI threshold a segment gave up, when no QP passed the VMAF and
// the CAMBI thresholds together (see searchSegmentCAMBI).
type CAMBIBestEffort int

const (
	// CAMBIBestEffortNone: the QP kept passes every threshold.
	CAMBIBestEffortNone CAMBIBestEffort = iota
	// CAMBIBestEffortMax: the worst frame threshold was given up. The QP kept passes the mean one
	// (or has no mean one to pass).
	CAMBIBestEffortMax
	// CAMBIBestEffortMean: no QP passes the mean threshold either. The QP of the VMAF search is
	// kept, failing the mean threshold, and the worst frame one when it is active.
	CAMBIBestEffortMean
)

// cambiOutcome is what the CAMBI stage did to a segment (see searchSegmentCAMBI).
type cambiOutcome struct {
	qp         int             // the QP kept
	walked     bool            // the QP of the VMAF search failed the CAMBI thresholds: the QPs below it were tried
	walk       []int           // the QPs the walk measured, in order (the ones skipped for failing VMAF left out)
	encodes    int             // the QPs the walk encoded, the others were encoded by the VMAF search
	bestEffort CAMBIBestEffort // the threshold given up, when no QP passed them all
}

// searchSegmentCAMBI gates the banding the encoder added to a segment, once its VMAF search found
// vmafQP: it keeps the highest QP at or below vmafQP passing both the VMAF and the CAMBI
// thresholds. results holds the VMAF statistics of the QPs encoded so far (the encodes of the walk
// are added), testedQPs their list (the encodes of the walk are appended: the files follow
// KeepInvalidQP like the others).
//
// # WHY A STAGE OF ITS OWN
//
// The VMAF thresholds gate fidelity, the score of a v1 model with its CAMBI term clipped (see
// AGENTS.md, "VMAF fidelity and added banding"). Fidelity sees coarse steps, not the fine
// staircases an encoder leaves on smooth gradients: on synthetic 10-bit gradients, it stayed at
// 93 or above at QPs where the encoder added 2 to 3.3 of banding on average over the frames.
// CAMBI in full-reference mode rates them, and only what the encode adds to its source, not the
// banding the source already has.
//
// # WHY ONCE, AT THE QP OF THE VMAF SEARCH
//
// The VMAF search runs as if this stage did not exist and measures no banding. The banding is
// measured at the QP it found, and when the thresholds pass, which was the case of every segment
// of the two real contents measured (BENCHMARKS.md), nothing more is encoded. Measured in every
// attempt of the VMAF search, the banding would give the walk nothing to use: the banding an
// encoder adds is not monotonic in QP (the worst frame of a segment of the episode adds 0.78 at
// QP 16, 4.89 at 20 to 22, 1.52 at 28, 0.17 at 30), so a value measured lower down lets the walk
// skip no QP.
//
// # WHY ONE QP AT A TIME, NOT A JUMP NOR A PREDICTION
//
// That same non-monotony: a jump can land below the highest QP that passes, and every QP too low
// is size for nothing. There is no cache for this stage either: a cache gives a search its start
// and its step, and the walk has neither to learn, it starts right below vmafQP and steps by one.
//
// # THE WALK
//
// From vmafQP-1 down to qpMin. A QP the VMAF search encoded costs a banding pass on its file (the
// search keeps them until the segment ends), and is skipped if it failed the VMAF thresholds. A new
// one costs an encode and a single pass measuring both. The first QP passing both is kept.
//
// # BEST EFFORT
//
// No QP passes both down to qpMin: the worst frame threshold is given up, and the highest QP
// passing the VMAF thresholds and the mean one is kept, vmafQP when none passes the mean either. A
// best effort must not buy size for a threshold nothing meets: the QPs below are no better on it,
// only larger. Knowing that nothing passes takes the walk down to qpMin: a non-monotonic curve
// offers no early exit.
//
// # EDGE CASES
//
//   - vmafQP is a VMAF best effort (qpMin, failing the VMAF thresholds): its banding is measured
//     all the same, there is nothing below it to walk, and it is kept, as a CAMBI best effort too
//     when it fails the CAMBI thresholds.
//   - A QP below vmafQP can fail the VMAF thresholds (fidelity rises with the QP on smooth
//     synthetic gradients, which cost the encoders a few hundred bytes per frame): it can not be
//     kept, not even as a best effort. Encoded by the VMAF search, its banding is not measured;
//     encoded by the walk, it is measured in the same pass and ignored.
//   - The mean threshold disabled: every QP passes it, a best effort keeps vmafQP.
func searchSegmentCAMBI(ctx context.Context, scb QPSearchCallbacks, config QPSearchConfig,
	workerID, segment int, segmentPath string, videoTrack VideoStream, vmafQP int, results map[int]VMAFStats,
	testedQPs *[]int) (outcome cambiOutcome, err error) {
	qpMin, _, found := config.Encoder.QPRange()
	if !found {
		err = fmt.Errorf("failed to get QP range for encoder %s", config.Encoder.Name())
		return
	}
	// The QP of the VMAF search
	scb.OnSegmentCAMBIStart(workerID, vmafQP)
	banding := make(map[int]BandingStats)
	if banding[vmafQP], err = segmentBanding(ctx, scb, config, segmentPath, workerID, segment, vmafQP, videoTrack); err != nil {
		err = fmt.Errorf("failed to measure the banding of QP %d: %w", vmafQP, err)
		return
	}
	outcome.qp = vmafQP
	if config.CAMBIAuditor.Validate(banding[vmafQP]) {
		return
	}
	// The walk
	outcome.walked = true
	defer func() {
		scb.Debug(workerID, "Segment %d: CAMBI walk from QP %d: QP %d kept, %d QPs encoded", segment, vmafQP, outcome.qp, outcome.encodes)
	}()
	for qp := vmafQP - 1; qp >= qpMin; qp-- {
		if vmafStats, encoded := results[qp]; encoded {
			if !config.Auditor.Validate(vmafStats) {
				scb.Debug(workerID, "Segment %d: CAMBI walk: QP %d failed the VMAF thresholds, skipped", segment, qp)
				continue
			}
			scb.OnSegmentCAMBICandidate(workerID, qp)
			outcome.walk = append(outcome.walk, qp)
			if banding[qp], err = segmentBanding(ctx, scb, config, segmentPath, workerID, segment, qp, videoTrack); err != nil {
				err = fmt.Errorf("failed to measure the banding of QP %d: %w", qp, err)
				return
			}
		} else {
			scb.OnSegmentCAMBICandidate(workerID, qp)
			outcome.walk = append(outcome.walk, qp)
			var qpBanding BandingStats
			if vmafStats, qpBanding, err = segmentQP(ctx, scb, config, segmentPath, workerID, segment, qp, videoTrack,
				VMAFMeasures{Score: true, Banding: true}); err != nil {
				err = fmt.Errorf("failed to produce QP %d: %w", qp, err)
				return
			}
			outcome.encodes++
			results[qp] = vmafStats
			*testedQPs = append(*testedQPs, qp)
			if !config.Auditor.Validate(vmafStats) {
				scb.Debug(workerID, "Segment %d: CAMBI walk: QP %d failed the VMAF thresholds", segment, qp)
				scb.OnSegmentCAMBICandidateDone(workerID, qp, false)
				continue
			}
			banding[qp] = qpBanding
		}
		passed := config.CAMBIAuditor.Validate(banding[qp])
		scb.OnSegmentCAMBICandidateDone(workerID, qp, passed)
		if passed {
			outcome.qp = qp
			return
		}
	}
	// Best effort: the highest QP passing the VMAF thresholds and the mean one, vmafQP otherwise.
	// Below vmafQP, banding only holds QPs passing the VMAF thresholds; vmafQP fails them when it
	// is a VMAF best effort, qpMin, which is kept either way.
	for qp := vmafQP; qp >= qpMin; qp-- {
		if qpBanding, measured := banding[qp]; measured && config.CAMBIAuditor.ValidateMean(qpBanding) {
			outcome.qp = qp
			break
		}
	}
	if config.CAMBIAuditor.ValidateMean(banding[outcome.qp]) {
		outcome.bestEffort = CAMBIBestEffortMax
	} else {
		outcome.bestEffort = CAMBIBestEffortMean
	}
	return
}
