package main

import (
	"fmt"

	"github.com/hekmon/ffmpegutils"
	"gonum.org/v1/gonum/interp"
)

func NewPredicator(existingResults map[int]ffmpegutils.VMAFStats) (p Predicator, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic encountered: %+v", r)
		}
	}()
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
		if !ok {
			continue
		}
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
	// Spawn the interpolators
	p.minInterpolator = new(interp.PiecewiseLinear)
	p.p1Interpolator = new(interp.PiecewiseLinear)
	p.p5Interpolator = new(interp.PiecewiseLinear)
	p.p10Interpolator = new(interp.PiecewiseLinear)
	p.p25Interpolator = new(interp.PiecewiseLinear)
	p.mediansInterpolator = new(interp.PiecewiseLinear)
	p.hmeanInterpolator = new(interp.PiecewiseLinear)
	p.meanInterpolator = new(interp.PiecewiseLinear)
	// Init with known points
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

func (p *Predicator) Predict(qp int) (stats ffmpegutils.VMAFStats) {
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
