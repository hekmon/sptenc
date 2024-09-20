package main

import (
	"fmt"

	"github.com/hekmon/ffmpegutils"
	"github.com/hekmon/liveprogress/v2"
	"gonum.org/v1/gonum/interp"
)

func NewPredicator(existingResults map[int]ffmpegutils.VMAFStats) (p Predicator, err error) {
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
	p.qps = make([]float64, len(existingResults))
	p.mins = make([]float64, len(existingResults))
	p.p1s = make([]float64, len(existingResults))
	p.p5s = make([]float64, len(existingResults))
	p.p10s = make([]float64, len(existingResults))
	p.p25s = make([]float64, len(existingResults))
	p.medians = make([]float64, len(existingResults))
	p.hmeans = make([]float64, len(existingResults))
	p.means = make([]float64, len(existingResults))
	index := 0
	for qp := ffmpegutils.QPMinimum; qp <= ffmpegutils.QPMaximum; qp++ {
		result, ok := existingResults[qp]
		if ok {
			p.qps[index] = float64(qp)
			p.mins[index] = result.Minimum
			p.p1s[index] = result.Percentile1
			p.p5s[index] = result.Percentile5
			p.p10s[index] = result.Percentile10
			p.p25s[index] = result.Percentile25
			p.medians[index] = result.Median
			p.hmeans[index] = result.HarmonicMean
			p.means[index] = result.Mean
			index++
		}
	}
	if *debug {
		fmt.Fprintf(liveprogress.Bypass(), "Initializing FritschButland predicator with %d points: %+v\n",
			len(existingResults), p.qps)
	}
	// Init with known points
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic encountered while initializing predicator with %d points: %+v",
				len(existingResults), r)
		}
	}()
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

type Predicator struct {
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
}

func (p *Predicator) Predict(qp int) (stats ffmpegutils.VMAFStats, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic encountered: %+v", r)
		}
	}()
	qpf := float64(qp)
	stats.Minimum = p.minInterpolator.Predict(qpf)
	stats.Percentile1 = p.p1Interpolator.Predict(qpf)
	stats.Percentile5 = p.p5Interpolator.Predict(qpf)
	stats.Percentile10 = p.p10Interpolator.Predict(qpf)
	stats.Percentile25 = p.p25Interpolator.Predict(qpf)
	stats.Median = p.mediansInterpolator.Predict(qpf)
	stats.HarmonicMean = p.hmeanInterpolator.Predict(qpf)
	stats.Mean = p.meanInterpolator.Predict(qpf)
	stats = p.adapt(qp, stats)
	return
}

func (p *Predicator) adapt(qp int, stats ffmpegutils.VMAFStats) (adapted ffmpegutils.VMAFStats) {
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

func (p *Predicator) adaptCeilingValues(preIndex, postIndex, predictedForQP int, predicatedValue float64, ys []float64) (adaptedValue float64) {
	if predictedForQP <= int(p.qps[preIndex]) || predictedForQP >= int(p.qps[postIndex]) {
		// safety
		return predicatedValue
	}
	if ys[preIndex] == 100 && ys[postIndex] < 100 {
		// Interpolation will decrease value as expected, but as VMAF 100 is a ceilling value, it could stay at 100 for a few more QP values.
		// But if user is expecting a 100 value for its auditor, lowering value right after pre, will force him to check qp incrementally one
		// by one defeating the purpose of interpolation. The idea here is to lower the value only after the second half between pre and post
		// to force the QP search using the predicator to have a quick search like search and make him compute the middle value between pre
		// and post by hoping the actual computed value won't be 100 for the next interpolation and avoid a one by one search.
		middleQP := int(p.qps[preIndex]) + (int(p.qps[postIndex])-int(p.qps[preIndex]))/2
		if predictedForQP <= middleQP {
			// For the first half, we return the pre value (100)
			adaptedValue = ys[preIndex]
			if *debug {
				fmt.Fprintf(liveprogress.Bypass(), "Adapting predicted value for first half. pre: %d, predicted: %d, post: %d, preValue: %f, predictedValue: %f, adaptedValue: %f, postValue: %f\n",
					int(p.qps[preIndex]), predictedForQP, int(p.qps[postIndex]), ys[preIndex], predicatedValue, adaptedValue, ys[postIndex])
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
				newValues = append(newValues, 100)
			} else {
				newValues = append(newValues, y)
			}
		}
		predicator := new(interp.FritschButland)
		_ = predicator.Fit(newqps, newValues)
		adaptedValue = predicator.Predict(float64(predictedForQP))
		if *debug {
			fmt.Fprintf(liveprogress.Bypass(), "Adapting predicted value for second half. pre: %d, predicted: %d, post: %d, preValue: %f, predictedValue: %f, adaptedValue: %f, postValue: %f\n",
				int(p.qps[preIndex]), predictedForQP, int(p.qps[postIndex]), ys[preIndex], predicatedValue, adaptedValue, ys[postIndex])
		}
		return
	}
	return predicatedValue
}

/*
	Method quick_search: 1313 tries
	Method full_interpolation_AkimaSpline: 1365 tries
	Method full_interpolation_ClampedCubic: 1546 tries
	Method full_interpolation_FritschButland: 1175 tries
	Method full_interpolation_NaturalCubic: 1345 tries
	Method quick_interpolation_AkimaSpline: 977 tries
	Method quick_interpolation_ClampedCubic: 1102 tries
	Method quick_interpolation_FritschButland: 915 tries
	Method quick_interpolation_NaturalCubic: 968 tries

	Best method for tries is quick_interpolation_FritschButland with 915 tries.


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


	Manual start QP Ideal QP for lowest tries is 13 with 2223 tries.
	Manual start QP Ideal QP for lowest frames is 13 with 105695 frames.

	-------------------------------------------------------------------------------
	-------------------------------------------------------------------------------

	The Dawn I (no data for stddev)

	Method quicksearch: 530 tries
	Method split_interpolation: 603 tries
	Method quick_interpolation: 519 tries
	Method stddev_quick: 459 tries
	Method stddev_interpolation: 543 tries
		Best method for tries is stddev_quick with 459 tries.

	Method quicksearch: 75369 frames
	Method split_interpolation: 85604 frames
	Method quick_interpolation: 73779 frames
	Method stddev_quick: 66344 frames
	Method stddev_interpolation: 76839 frames
		Best method for frames is stddev_quick with 66344 frames.

	Manual start QP Ideal QP for lowest tries is 18 with 494 tries.
	Manual start QP Ideal QP for lowest frames is 18 with 69465 frames.

	-------------------------------------------------------------------------------

	The Dawn II (data from The Dawn I for stddev)

	Method quicksearch: 568 tries
	Method split_interpolation: 630 tries
	Method quick_interpolation: 543 tries
	Method stddev_quick: 502 tries
	Method stddev_interpolation: 492 tries
		Best method for tries is stddev_interpolation with 492 tries.

	Method quicksearch: 62514 frames
	Method split_interpolation: 69019 frames
	Method quick_interpolation: 58914 frames
	Method stddev_quick: 55575 frames
	Method stddev_interpolation: 54229 frames
		Best method for frames is stddev_interpolation with 54229 frames.

	Manual start QP Ideal QP for lowest tries is 21 with 583 tries.
	Manual start QP Ideal QP for lowest frames is 21 with 63732 frames.

	-------------------------------------------------------------------------------

	The Dawn III (data from The Dawn I & II for stddev)
*/
