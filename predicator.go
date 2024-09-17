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
	adapted.Minimum = adaptCeilingValues(int(p.qps[preIndex]), qp, int(p.qps[postIndex]), p.mins[preIndex], stats.Minimum, p.mins[postIndex])
	adapted.Percentile1 = adaptCeilingValues(int(p.qps[preIndex]), qp, int(p.qps[postIndex]), p.p1s[preIndex], stats.Percentile1, p.p1s[postIndex])
	adapted.Percentile5 = adaptCeilingValues(int(p.qps[preIndex]), qp, int(p.qps[postIndex]), p.p5s[preIndex], stats.Percentile5, p.p5s[postIndex])
	adapted.Percentile10 = adaptCeilingValues(int(p.qps[preIndex]), qp, int(p.qps[postIndex]), p.p10s[preIndex], stats.Percentile10, p.p10s[postIndex])
	adapted.Percentile25 = adaptCeilingValues(int(p.qps[preIndex]), qp, int(p.qps[postIndex]), p.p25s[preIndex], stats.Percentile25, p.p25s[postIndex])
	adapted.Median = adaptCeilingValues(int(p.qps[preIndex]), qp, int(p.qps[postIndex]), p.medians[preIndex], stats.Median, p.medians[postIndex])
	adapted.HarmonicMean = adaptCeilingValues(int(p.qps[preIndex]), qp, int(p.qps[postIndex]), p.hmeans[preIndex], stats.HarmonicMean, p.hmeans[postIndex])
	adapted.Mean = adaptCeilingValues(int(p.qps[preIndex]), qp, int(p.qps[postIndex]), p.means[preIndex], stats.Mean, p.means[postIndex])
	return
}

func adaptCeilingValues(pre, predicted, post int, preValue, predicatedValue, postValue float64) (adaptedValue float64) {
	if predicted <= pre || predicted >= post {
		if *debug {
			fmt.Fprintf(liveprogress.Bypass(), "Not adapting predicted value. pre: %d, predicted: %d, post: %d, preValue: %f, predictedValue: %f, postValue: %f\n",
				pre, predicted, post, preValue, predicatedValue, postValue)
		}
		return predicatedValue
	}
	if preValue == 100 && postValue < 100 {
		if *debug {
			fmt.Fprintf(liveprogress.Bypass(), "Adapting predicted value. pre: %d, predicted: %d, post: %d, preValue: %f, predictedValue: %f, postValue: %f\n",
				pre, predicted, post, preValue, predicatedValue, postValue)
		}
		// Interpolation will decrease value as expected, but as VMAF 100 is a ceilling value, it could stay at 100 for a few more QP values.
		// But if user is expecting a 100 value for its auditor, lowering value right after pre, will force him to check qp incrementally one
		// by one defeating the purpose of interpolation. The idea here is to lower the value only after the second half between pre and post
		// to force the QP search using the predicator to have a quick search like search and make him compute the middle value between pre
		// and post by hoping the actual computed value won't be 100.
		if predicted <= pre+(post-pre)/2 {
			return preValue
		}
		return postValue
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



	Method quick_search: 1313 tries
	Method quick_interpolation_FritschButland: 915 tries
	Method quick_interpolation_FritschButland-adaptative: 1165 tries
	Method quick_search: 62722 frames
	Method quick_interpolation_FritschButland: 44103 frames
	Method quick_interpolation_FritschButland-adaptative: 55638 frames
	Best method for tries is quick_interpolation_FritschButland with 915 tries.
	Best method for frames is quick_interpolation_FritschButland with 44103 frames.

	Manual start QP Ideal QP for lowest tries is 13 with 2223 tries.
	Manual start QP Ideal QP for lowest frames is 13 with 105695 frames.
*/
