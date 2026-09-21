package core

import (
	"fmt"
	"os"
)

func getFileSize(path string) (size int64, err error) {
	info, err := os.Stat(path)
	if err != nil {
		err = fmt.Errorf("failed to stat path: %w", err)
	} else {
		size = info.Size()
	}
	return
}

// coldStartStats returns the search parameters to use when nothing is known yet about an
// encoder and VMAF profile: start in the middle of the QP range and step by a quarter of its
// width, like a binary search would.
//
// # WHY A SINGLE FUNCTION
//
// Three places need these values (the empty persistent cache, the ephemeral cache seeded from
// it, and the spread recorded for single segment runs). They must agree, and the mean formula
// used to be duplicated as (qpMax-qpMin+1)/2: the width of the range divided by two, which is
// only its middle when qpMin is 0. Harmless with a qpMin of 0 or 1 (off by one at most), wrong
// for any encoder whose range starts higher: the middle is (qpMin+qpMax)/2.
//
// # WHY THE WIDTH COUNTS BOTH ENDS
//
// qpMin and qpMax are both valid QPs: a [0,51] range holds 52 values, its quarter is 13.
func coldStartStats(qpMin, qpMax int) (mean, stddev float64) {
	mean = float64(qpMin+qpMax) / 2
	stddev = float64(qpMax-qpMin+1) / 4
	return
}
