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
	qps := make([]float64, len(existingResults))
	mins := make([]float64, len(existingResults))
	p1s := make([]float64, len(existingResults))
	p5s := make([]float64, len(existingResults))
	p10s := make([]float64, len(existingResults))
	p25s := make([]float64, len(existingResults))
	medians := make([]float64, len(existingResults))
	hmeans := make([]float64, len(existingResults))
	means := make([]float64, len(existingResults))
	index := 0
	for qp := ffmpegutils.QPMinimum; qp <= ffmpegutils.QPMaximum; qp++ {
		result, ok := existingResults[qp]
		if ok {
			qps[index] = float64(qp)
			mins[index] = result.Minimum
			p1s[index] = result.Percentile1
			p5s[index] = result.Percentile5
			p10s[index] = result.Percentile10
			p25s[index] = result.Percentile25
			medians[index] = result.Median
			hmeans[index] = result.HarmonicMean
			means[index] = result.Mean
			index++
		}
	}
	if *debug {
		fmt.Fprintf(liveprogress.Bypass(), "Initializing FritschButland predicator with %d points: %+v\n",
			len(existingResults), qps)
	}
	// Init with known points
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic encountered while initializing predicator with %d points: %+v",
				len(existingResults), r)
		}
	}()
	_ = p.minInterpolator.Fit(qps, mins)
	_ = p.p1Interpolator.Fit(qps, p1s)
	_ = p.p5Interpolator.Fit(qps, p5s)
	_ = p.p10Interpolator.Fit(qps, p10s)
	_ = p.p25Interpolator.Fit(qps, p25s)
	_ = p.mediansInterpolator.Fit(qps, medians)
	_ = p.hmeanInterpolator.Fit(qps, hmeans)
	_ = p.meanInterpolator.Fit(qps, means)
	return
}

type Predicator struct {
	minInterpolator     interp.FittablePredictor
	p1Interpolator      interp.FittablePredictor
	p5Interpolator      interp.FittablePredictor
	p10Interpolator     interp.FittablePredictor
	p25Interpolator     interp.FittablePredictor
	mediansInterpolator interp.FittablePredictor
	hmeanInterpolator   interp.FittablePredictor
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
	return
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
