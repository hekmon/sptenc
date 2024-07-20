package main

import (
	"fmt"

	"github.com/hekmon/ffmpegutils"
)

const (
	VMAFOffValue = -1
	VMAFMinValue = 0
	VMAFMaxValue = 100
)

func NewVMAFChecker(min, p1, hmean, mean float64) (vc VMAFChecker, err error) {
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
	min, p1, hmean, mean float64
}

func (vc VMAFChecker) Validate(stats ffmpegutils.VMAFStats) bool {
	if vc.min != VMAFOffValue && stats.Minimum < vc.min {
		return false
	}
	if vc.p1 != VMAFOffValue && stats.Percentile1 < vc.p1 {
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
