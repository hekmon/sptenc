package core

import (
	"fmt"

	"gonum.org/v1/gonum/interp"
)

// NewPredictor builds an interpolator from known QP→VMAF data points.
// It validates inputs before calling gonum because FritschButland.Fit panics
// on insufficient points, mismatched lengths, or non-strictly-increasing x values.
func NewPredictor(existingResults map[int]VMAFStats, qpMin, qpMax int, debug func(format string, a ...any)) (p Predictor, err error) {
	if len(existingResults) < 2 {
		return p, fmt.Errorf("need at least 2 data points for interpolation, got %d", len(existingResults))
	}
	// Spawn the interpolators
	p.minInterpolator = new(interp.FritschButland)
	p.p1Interpolator = new(interp.FritschButland)
	p.p5Interpolator = new(interp.FritschButland)
	p.p10Interpolator = new(interp.FritschButland)
	p.p25Interpolator = new(interp.FritschButland)
	p.mediansInterpolator = new(interp.FritschButland)
	p.hmeanInterpolator = new(interp.FritschButland)
	p.meanInterpolator = new(interp.FritschButland)
	// Prepare the data sets
	p.qps = make([]float64, 0, len(existingResults))
	p.mins = make([]float64, 0, len(existingResults))
	p.p1s = make([]float64, 0, len(existingResults))
	p.p5s = make([]float64, 0, len(existingResults))
	p.p10s = make([]float64, 0, len(existingResults))
	p.p25s = make([]float64, 0, len(existingResults))
	p.medians = make([]float64, 0, len(existingResults))
	p.hmeans = make([]float64, 0, len(existingResults))
	p.means = make([]float64, 0, len(existingResults))
	for qp := qpMin; qp <= qpMax; qp++ {
		result, ok := existingResults[qp]
		if !ok {
			continue
		}
		p.qps = append(p.qps, float64(qp))
		p.mins = append(p.mins, result.Minimum)
		p.p1s = append(p.p1s, result.Percentile1)
		p.p5s = append(p.p5s, result.Percentile5)
		p.p10s = append(p.p10s, result.Percentile10)
		p.p25s = append(p.p25s, result.Percentile25)
		p.medians = append(p.medians, result.Median)
		p.hmeans = append(p.hmeans, result.HarmonicMean)
		p.means = append(p.means, result.Mean)
	}
	if len(p.qps) < 2 {
		return p, fmt.Errorf("need at least 2 data points within QP range [%d, %d] for interpolation, got %d", qpMin, qpMax, len(p.qps))
	}
	for i := 1; i < len(p.qps); i++ {
		if p.qps[i] <= p.qps[i-1] {
			return p, fmt.Errorf("QP values must be strictly increasing for interpolation, got %v", p.qps)
		}
	}
	p.debug = debug
	if p.debug != nil {
		p.debug("Initializing FritschButland predictor with %d points: %+v",
			len(p.qps), p.qps)
	}
	_ = p.minInterpolator.Fit(p.qps, p.mins)
	_ = p.p1Interpolator.Fit(p.qps, p.p1s)
	_ = p.p5Interpolator.Fit(p.qps, p.p5s)
	_ = p.p10Interpolator.Fit(p.qps, p.p10s)
	_ = p.p25Interpolator.Fit(p.qps, p.p25s)
	_ = p.mediansInterpolator.Fit(p.qps, p.medians)
	_ = p.hmeanInterpolator.Fit(p.qps, p.hmeans)
	_ = p.meanInterpolator.Fit(p.qps, p.means)
	return
}

// Predictor forecasts, for a QP that was not encoded, the VMAF stats record the auditor would
// see: one monotone curve per statistic, fitted on the QPs already encoded for this segment.
//
// # WHY THIS EXISTS
//
// Once the search has bracketed the threshold (one QP known to pass, a higher one known to
// fail), it has to pick which QP inside the bracket to encode next. Without a forecast that
// is a blind walk. With one, interpolateCandidate asks for every untested QP of the bracket,
// from the failing side down, and encodes the first one forecast to pass every enabled
// threshold.
//
// The gain over bisecting the bracket is modest and content-dependent: the 2024 benchmark
// below measured 30% fewer attempts on one clip, 2% to 6% fewer on two others, and 18% more
// on a fourth searched from a cold start. It was kept because it won on the clips searched
// with a cache, which is the production case.
//
// # WHAT A FORECAST IS NOT
//
// A forecast is a candidate, never a result. The QP it names is encoded and measured like
// any other, and it is only kept once the next higher QP has been encoded and found failing.
// No QP reaches the output on a prediction: the per-segment floor sptenc promises is always
// a measured one. Do not "trust" a forecast to skip an encode.
//
// # WHY ONE CURVE PER STATISTIC
//
// The auditor validates a full record (min, percentiles, median, harmonic mean, mean) against
// whichever thresholds the profile enables. Each statistic has its own curve against QP, so
// each gets its own interpolator, and the forecast is validated as a whole by the same
// auditor as a real result. No distance or score is computed: every enabled threshold must
// pass on its own.
//
// # EDGE CASES
//
//   - Fewer than two encoded QPs: no curve can be fitted, NewPredictor fails and the search
//     keeps walking in stddev steps instead.
//   - Values are clamped to [0, 100] after interpolation, then adapted for the VMAF ceiling:
//     when a known point sits at 100 the curve must not forecast the same for a lower QP as a
//     mere plateau, see adaptCeilingValues.
type Predictor struct {
	qps                 []float64
	mins                []float64
	minInterpolator     interp.FittablePredictor
	p1s                 []float64
	p1Interpolator      interp.FittablePredictor
	p5s                 []float64
	p5Interpolator      interp.FittablePredictor
	p10s                []float64
	p10Interpolator     interp.FittablePredictor
	p25s                []float64
	p25Interpolator     interp.FittablePredictor
	medians             []float64
	mediansInterpolator interp.FittablePredictor
	hmeans              []float64
	hmeanInterpolator   interp.FittablePredictor
	means               []float64
	meanInterpolator    interp.FittablePredictor
	debug               func(format string, a ...any)
}

// Predict estimates VMAF stats for a given QP using the fitted interpolators.
// It does not return an error because gonum's Predict handles out-of-range
// values gracefully via extrapolation; clamping and adaptation follow.
func (p *Predictor) Predict(qp int) (stats VMAFStats) {
	qpf := float64(qp)
	stats.Minimum = p.minInterpolator.Predict(qpf)
	stats.Percentile1 = p.p1Interpolator.Predict(qpf)
	stats.Percentile5 = p.p5Interpolator.Predict(qpf)
	stats.Percentile10 = p.p10Interpolator.Predict(qpf)
	stats.Percentile25 = p.p25Interpolator.Predict(qpf)
	stats.Median = p.mediansInterpolator.Predict(qpf)
	stats.HarmonicMean = p.hmeanInterpolator.Predict(qpf)
	stats.Mean = p.meanInterpolator.Predict(qpf)
	// Clamp all values to valid VMAF range [0, 100]
	stats.Minimum = clampVMAF(stats.Minimum)
	stats.Percentile1 = clampVMAF(stats.Percentile1)
	stats.Percentile5 = clampVMAF(stats.Percentile5)
	stats.Percentile10 = clampVMAF(stats.Percentile10)
	stats.Percentile25 = clampVMAF(stats.Percentile25)
	stats.Median = clampVMAF(stats.Median)
	stats.HarmonicMean = clampVMAF(stats.HarmonicMean)
	stats.Mean = clampVMAF(stats.Mean)
	stats = p.adapt(qp, stats)
	return
}

func (p *Predictor) adapt(qp int, stats VMAFStats) (adapted VMAFStats) {
	adapted = stats
	var preIndex, postIndex int
	// Find known values indexes sourrounding qp
	for index, fqp := range p.qps {
		if fqp == float64(qp) {
			// this is not a predicted value, this is a known value
			return
		}
		if fqp > float64(qp) {
			postIndex = index
			preIndex = index - 1
			break
		}
	}
	if preIndex == postIndex {
		// search loop unsucessfull
		return
	}
	// Adapt values if needed
	adapted.Minimum = p.adaptCeilingValues(preIndex, postIndex, qp, stats.Minimum, p.mins)
	adapted.Percentile1 = p.adaptCeilingValues(preIndex, postIndex, qp, stats.Percentile1, p.p1s)
	adapted.Percentile5 = p.adaptCeilingValues(preIndex, postIndex, qp, stats.Percentile5, p.p5s)
	adapted.Percentile10 = p.adaptCeilingValues(preIndex, postIndex, qp, stats.Percentile10, p.p10s)
	adapted.Percentile25 = p.adaptCeilingValues(preIndex, postIndex, qp, stats.Percentile25, p.p25s)
	adapted.Median = p.adaptCeilingValues(preIndex, postIndex, qp, stats.Median, p.medians)
	adapted.HarmonicMean = p.adaptCeilingValues(preIndex, postIndex, qp, stats.HarmonicMean, p.hmeans)
	adapted.Mean = p.adaptCeilingValues(preIndex, postIndex, qp, stats.Mean, p.means)
	return
}

func (p *Predictor) adaptCeilingValues(preIndex, postIndex, predictedForQP int, predictedValue float64, ys []float64) (adaptedValue float64) {
	if predictedForQP <= int(p.qps[preIndex]) || predictedForQP >= int(p.qps[postIndex]) {
		// safety
		return predictedValue
	}
	if ys[preIndex] == VMAFMaxValue && ys[postIndex] < VMAFMaxValue {
		// Interpolation will decrease value as expected, but as VMAF 100 is a ceilling value, it could stay at 100 for a few more QP values.
		// But if user is expecting a 100 value for its auditor, lowering value right after pre, will force him to check qp incrementally one
		// by one defeating the purpose of interpolation. The idea here is to lower the value only after the second half between pre and post
		// to force the QP search using the predictor to have a quick search like search and make him compute the middle value between pre
		// and post by hoping the actual computed value won't be 100 for the next interpolation and avoid a one by one search.
		middleQP := int(p.qps[preIndex]) + (int(p.qps[postIndex])-int(p.qps[preIndex]))/2
		if predictedForQP <= middleQP {
			// For the first half, we return the pre value (VMAFMaxValue)
			adaptedValue = ys[preIndex]
			if p.debug != nil {
				p.debug("Adapting predicted value for first half. pre: %d, predicted: %d, post: %d, preValue: %f, predictedValue: %f, adaptedValue: %f, postValue: %f",
					int(p.qps[preIndex]), predictedForQP, int(p.qps[postIndex]), ys[preIndex], predictedValue, adaptedValue, ys[postIndex],
				)
			}
			return
		}
		// For the second half, redo an interpolation between middle (as 100) and post
		newqps := make([]float64, 0, len(p.qps)+1)
		for index, qp := range p.qps {
			if index == preIndex {
				newqps = append(newqps, qp)
				// add the new false data point just after
				newqps = append(newqps, float64(middleQP))
			} else {
				newqps = append(newqps, qp)
			}
		}
		newValues := make([]float64, 0, len(ys)+1)
		for index, y := range ys {
			if index == preIndex {
				newValues = append(newValues, y)
				// add the new false data point just after
				newValues = append(newValues, VMAFMaxValue)
			} else {
				newValues = append(newValues, y)
			}
		}
		predictor := new(interp.FritschButland)
		if err := predictor.Fit(newqps, newValues); err != nil {
			// Fallback to the raw predicted value if the secondary interpolation fails.
			return predictedValue
		}
		adaptedValue = predictor.Predict(float64(predictedForQP))
		if p.debug != nil {
			p.debug("Adapting predicted value for second half. pre: %d, predicted: %d, post: %d, preValue: %f, predictedValue: %f, adaptedValue: %f, postValue: %f",
				int(p.qps[preIndex]), predictedForQP, int(p.qps[postIndex]), ys[preIndex], predictedValue, adaptedValue, ys[postIndex],
			)
		}
		return
	}
	return predictedValue
}

// clamp restricts a value to the VAMF value range
func clampVMAF(value float64) float64 {
	if value < VMAFMinValue {
		return VMAFMinValue
	}
	if value > VMAFMaxValue {
		return VMAFMaxValue
	}
	return value
}

/*
	SEARCH METHOD BENCHMARK (September 2024: superseded totals, valid method choice)

	What it was: a harness (search_methods.go, removed since; see commits 7962b10, cf876d2 and
	7cad665) ran every search method on every segment of a clip, with real encodes and real
	VMAF, and summed per method the QPs tested ("attempts", one encode plus its VMAF
	computation) and the frames those encodes produced. Every method started from the same QP,
	the persistent cache mean, and differed in what it did next:
	  - quicksearch: bisect until the threshold is bracketed, then keep bisecting.
	  - split_interpolation: jump to the range extremes to bracket, then interpolate.
	  - quick_interpolation: bisect until bracketed, then interpolate.
	  - stddev_quick: walk from the mean in cache-stddev steps until bracketed, then bisect.
	  - stddev_interpolation: walk in stddev steps until bracketed, then interpolate. This is
	    the production algorithm (see findSegmentQP), with Predictor doing the interpolation.
	The first table is an earlier stage of the same harness: the interpolation algorithms
	compared with each other, under two bracketing strategies ("full" and "quick"); the clip
	and the exact strategies were not recorded.

	What was held fixed: the start point. The persistent cache was read once per clip, before
	the first segment, and every segment of the clip started from that same mean and stddev.
	"No data for stddev" means an empty cache: mean 26, stddev 13, the same cold start as
	today's coldStartStats. The per-segment learning of EphemeralStatsCache did not exist, so
	nothing moved the start point from one segment to the next.

	What to keep from it: the method. Fritsch-Butland needed the fewest attempts of the four
	interpolation algorithms. A stddev start beat bisection from the same mean on all three
	Dawn clips, cold start included. Interpolation inside the bracket beat bisection on the two
	clips searched with a cache (492 vs 502, 839 vs 893 attempts) and lost on the cold one
	(543 vs 459).

	What not to read into it: the totals. They include what a bad start point costs on every
	segment, which the ephemeral cache now removes after the first few segments of a run. The
	"Manual start QP" lines are an oracle, computed after the fact by replaying the results
	from every possible fixed start. Measured in 2026 with the ephemeral learning, the
	production pipeline takes about 3.9 attempts per segment from a cold cache (see the
	README).

	-------------------------------------------------------------------------------
	Interpolation algorithms compared (clip not recorded)
	-------------------------------------------------------------------------------

	Method quick_search: 1313 attempts
	Method full_interpolation_AkimaSpline: 1365 attempts
	Method full_interpolation_ClampedCubic: 1546 attempts
	Method full_interpolation_FritschButland: 1175 attempts
	Method full_interpolation_NaturalCubic: 1345 attempts
	Method quick_interpolation_AkimaSpline: 977 attempts
	Method quick_interpolation_ClampedCubic: 1102 attempts
	Method quick_interpolation_FritschButland: 915 attempts
	Method quick_interpolation_NaturalCubic: 968 attempts

	Best method for attempts is quick_interpolation_FritschButland with 915 attempts.


	Method quick_search: 62722 frames
	Method full_interpolation_AkimaSpline: 66084 frames
	Method full_interpolation_ClampedCubic: 74843 frames
	Method full_interpolation_FritschButland: 56807 frames
	Method full_interpolation_NaturalCubic: 65043 frames
	Method quick_interpolation_AkimaSpline: 47091 frames
	Method quick_interpolation_ClampedCubic: 53185 frames
	Method quick_interpolation_FritschButland: 44103 frames
	Method quick_interpolation_NaturalCubic: 46607 frames

	Best method for frames is quick_interpolation_FritschButland with 44103 frames.


	Manual start QP Ideal QP for lowest attempts is 13 with 2223 attempts.
	Manual start QP Ideal QP for lowest frames is 13 with 105695 frames.

	-------------------------------------------------------------------------------
	-------------------------------------------------------------------------------

	The Dawn I (no data for stddev)

	Method quicksearch: 530 attempts
	Method split_interpolation: 603 attempts
	Method quick_interpolation: 519 attempts
	Method stddev_quick: 459 attempts
	Method stddev_interpolation: 543 attempts
		Best method for attempts is stddev_quick with 459 attempts.

	Method quicksearch: 75369 frames
	Method split_interpolation: 85604 frames
	Method quick_interpolation: 73779 frames
	Method stddev_quick: 66344 frames
	Method stddev_interpolation: 76839 frames
		Best method for frames is stddev_quick with 66344 frames.

	Manual start QP Ideal QP for lowest attempts is 18 with 494 attempts.
	Manual start QP Ideal QP for lowest frames is 18 with 69465 frames.

	-------------------------------------------------------------------------------

	The Dawn II (data from The Dawn I for stddev)

	Method quicksearch: 568 attempts
	Method split_interpolation: 630 attempts
	Method quick_interpolation: 543 attempts
	Method stddev_quick: 502 attempts
	Method stddev_interpolation: 492 attempts
		Best method for attempts is stddev_interpolation with 492 attempts.

	Method quicksearch: 62514 frames
	Method split_interpolation: 69019 frames
	Method quick_interpolation: 58914 frames
	Method stddev_quick: 55575 frames
	Method stddev_interpolation: 54229 frames
		Best method for frames is stddev_interpolation with 54229 frames.

	Manual start QP Ideal QP for lowest attempts is 21 with 583 attempts.
	Manual start QP Ideal QP for lowest frames is 21 with 63732 frames.

	-------------------------------------------------------------------------------

	The Dawn III (data from The Dawn I & II for stddev)

	Method quicksearch: 993 attempts
	Method split_interpolation: 1055 attempts
	Method quick_interpolation: 913 attempts
	Method stddev_quick: 893 attempts
	Method stddev_interpolation: 839 attempts
		Best method for attempts is stddev_interpolation with 839 attempts.


	Method quicksearch: 89403 frames
	Method split_interpolation: 94104 frames
	Method quick_interpolation: 81303 frames
	Method stddev_quick: 79029 frames
	Method stddev_interpolation: 75060 frames
		Best method for frames is stddev_interpolation with 75060 frames.

	Manual start QP Ideal QP for lowest attempts is 22 with 1096 attempts.
	Manual start QP Ideal QP for lowest frames is 20 with 97403 frames.
*/
