package main

import (
	"errors"
	"fmt"

	"github.com/hekmon/ffmpegutils"
)

const (
	VMAFOffValue = -1
	VMAFMinValue = 0
	VMAFMaxValue = 100
)

func NewVMAFChecker(min, p1, p5, p10, p25, median, hmean, mean float64) (vc VMAFChecker, err error) {
	if min == VMAFOffValue &&
		p1 == VMAFOffValue && p5 == VMAFOffValue && p10 == VMAFOffValue && p25 == VMAFOffValue &&
		median == VMAFOffValue && hmean == VMAFOffValue && mean == VMAFOffValue {
		err = errors.New("all values are off")
		return
	}
	if (min < VMAFMinValue || min > VMAFMaxValue) && min != VMAFOffValue {
		err = fmt.Errorf("min value %f is out of range [%d, %d]", min, VMAFMinValue, VMAFMaxValue)
		return
	}
	vc.min = min
	if (p1 < VMAFMinValue || p1 > VMAFMaxValue) && p1 != VMAFOffValue {
		err = fmt.Errorf("p1 value %f is out of range [%d, %d]", p1, VMAFMinValue, VMAFMaxValue)
		return
	}
	vc.p1 = p1
	if (p5 < VMAFMinValue || p5 > VMAFMaxValue) && p5 != VMAFOffValue {
		err = fmt.Errorf("p5 value %f is out of range [%d, %d]", p5, VMAFMinValue, VMAFMaxValue)
		return
	}
	vc.p5 = p5
	if (p10 < VMAFMinValue || p10 > VMAFMaxValue) && p10 != VMAFOffValue {
		err = fmt.Errorf("p10 value %f is out of range [%d, %d]", p10, VMAFMinValue, VMAFMaxValue)
		return
	}
	vc.p10 = p10
	if (p25 < VMAFMinValue || p25 > VMAFMaxValue) && p25 != VMAFOffValue {
		err = fmt.Errorf("p25 value %f is out of range [%d, %d]", p25, VMAFMinValue, VMAFMaxValue)
		return
	}
	vc.p25 = p25
	if (median < VMAFMinValue || median > VMAFMaxValue) && median != VMAFOffValue {
		err = fmt.Errorf("median value %f is out of range [%d, %d]", median, VMAFMinValue, VMAFMaxValue)
		return
	}
	vc.median = median
	if (hmean < VMAFMinValue || hmean > VMAFMaxValue) && hmean != VMAFOffValue {
		err = fmt.Errorf("hmean value %f is out of range [%d, %d]", hmean, VMAFMinValue, VMAFMaxValue)
		return
	}
	vc.hmean = hmean
	if (mean < VMAFMinValue || mean > VMAFMaxValue) && mean != VMAFOffValue {
		err = fmt.Errorf("mean value %f is out of range [%d, %d]", mean, VMAFMinValue, VMAFMaxValue)
		return
	}
	vc.mean = mean
	return
}

type VMAFChecker struct {
	min    float64
	p1     float64
	p5     float64
	p10    float64
	p25    float64
	median float64
	hmean  float64
	mean   float64
}

func (vc VMAFChecker) Validate(stats ffmpegutils.VMAFStats) bool {
	if vc.min != VMAFOffValue && stats.Minimum < vc.min {
		return false
	}
	if vc.p1 != VMAFOffValue && stats.Percentile1 < vc.p1 {
		return false
	}
	if vc.p5 != VMAFOffValue && stats.Percentile5 < vc.p5 {
		return false
	}
	if vc.p10 != VMAFOffValue && stats.Percentile10 < vc.p10 {
		return false
	}
	if vc.p25 != VMAFOffValue && stats.Percentile25 < vc.p25 {
		return false
	}
	if vc.median != VMAFOffValue && stats.Median < vc.median {
		return false
	}
	if vc.hmean != VMAFOffValue && stats.HarmonicMean < vc.hmean {
		return false
	}
	if vc.mean != VMAFOffValue && stats.Mean < vc.mean {
		return false
	}
	return true
}
