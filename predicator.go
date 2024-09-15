package main

import (
	"fmt"

	"github.com/hekmon/ffmpegutils"
	"gonum.org/v1/gonum/interp"
)

func NewPredicator(existingResults map[int]ffmpegutils.VMAFStats) (p Predicator, err error) {
	// Spawn the interpolators
	p.minInterpolator = new(interp.AkimaSpline)
	p.p1Interpolator = new(interp.AkimaSpline)
	p.p5Interpolator = new(interp.AkimaSpline)
	p.p10Interpolator = new(interp.AkimaSpline)
	p.p25Interpolator = new(interp.AkimaSpline)
	p.mediansInterpolator = new(interp.AkimaSpline)
	p.hmeanInterpolator = new(interp.AkimaSpline)
	p.meanInterpolator = new(interp.AkimaSpline)
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
	// Init with known points
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic encountered: %+v", r)
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
	// // Test them
	// for qp := ffmpegutils.QPMinimum; qp <= ffmpegutils.QPMaximum; qp++ {
	// 	result, ok := existingResults[qp]
	// 	if !ok {
	// 		continue
	// 	}
	// 	if p.minInterpolator.Predict(float64(qp)) != result.Minimum {
	// 		err = fmt.Errorf("minInterpolator.Predict(%d) = %f, want %f", qp, p.minInterpolator.Predict(float64(qp)), result.Minimum)
	// 		return
	// 	}
	// 	if p.p1Interpolator.Predict(float64(qp)) != result.Percentile1 {
	// 		err = fmt.Errorf("p1Interpolator.Predict(%d) = %f, want %f", qp, p.p1Interpolator.Predict(float64(qp)), result.Percentile1)
	// 		return
	// 	}
	// 	if p.p5Interpolator.Predict(float64(qp)) != result.Percentile5 {
	// 		err = fmt.Errorf("p5Interpolator.Predict(%d) = %f, want %f", qp, p.p5Interpolator.Predict(float64(qp)), result.Percentile5)
	// 		return
	// 	}
	// 	if p.p10Interpolator.Predict(float64(qp)) != result.Percentile10 {
	// 		err = fmt.Errorf("p10Interpolator.Predict(%d) = %f, want %f", qp, p.p10Interpolator.Predict(float64(qp)), result.Percentile10)
	// 		return
	// 	}
	// 	if p.p25Interpolator.Predict(float64(qp)) != result.Percentile25 {
	// 		err = fmt.Errorf("p25Interpolator.Predict(%d) = %f, want %f", qp, p.p25Interpolator.Predict(float64(qp)), result.Percentile25)
	// 		return
	// 	}
	// 	if p.mediansInterpolator.Predict(float64(qp)) != result.Median {
	// 		err = fmt.Errorf("mediansInterpolator.Predict(%d) = %f, want %f", qp, p.mediansInterpolator.Predict(float64(qp)), result.Median)
	// 		return
	// 	}
	// 	if p.hmeanInterpolator.Predict(float64(qp)) != result.HarmonicMean {
	// 		err = fmt.Errorf("hmeanInterpolator.Predict(%d) = %f, want %f", qp, p.hmeanInterpolator.Predict(float64(qp)), result.HarmonicMean)
	// 		return
	// 	}
	// 	if p.meanInterpolator.Predict(float64(qp)) != result.Mean {
	// 		err = fmt.Errorf("meanInterpolator.Predict(%d) = %f, want %f", qp, p.meanInterpolator.Predict(float64(qp)), result.Mean)
	// 		return
	// 	}
	// }
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
