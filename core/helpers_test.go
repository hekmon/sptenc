package core

import (
	"strings"
	"testing"
)

func TestGetFileSize_Error(t *testing.T) {
	_, err := getFileSize("/nonexistent/path/that/does/not/exist")
	if err == nil {
		t.Fatal("expected error for non-existent path, got nil")
	}
	if !strings.Contains(err.Error(), "failed to stat path") {
		t.Errorf("expected 'failed to stat path' in error, got %v", err)
	}
}

func TestColdStartStats(t *testing.T) {
	for _, tc := range []struct {
		qpMin, qpMax int
		mean, stddev float64
	}{
		{0, 51, 25.5, 13},   // libx265, hevc_nvenc
		{1, 52, 26.5, 13},   // hevc_vaapi, hevc_d3d12va
		{1, 63, 32, 15.75},  // libsvtav1
		{0, 255, 127.5, 64}, // av1_nvenc
		{1, 100, 50.5, 25},  // hevc_videotoolbox
		{20, 40, 30, 5.25},  // a range far from 0: the offset must be taken into account
	} {
		if mean, stddev := coldStartStats(tc.qpMin, tc.qpMax); mean != tc.mean || stddev != tc.stddev {
			t.Errorf("range [%d,%d]: expected mean %v and stddev %v, got %v and %v",
				tc.qpMin, tc.qpMax, tc.mean, tc.stddev, mean, stddev)
		}
	}
	// Both caches must agree on an empty history
	persistent := &StatsCacheHistory{qpMin: 20, qpMax: 40}
	pMean, pStddev := persistent.GetMeanStdDev()
	eMean, eStddev := NewEphemeralStatsCache(persistent, 20, 40).GetMeanStdDev()
	if pMean != 30 || pStddev != 5 || eMean != pMean || eStddev != pStddev {
		t.Errorf("expected 30/5 from both caches, got %d/%d (persistent) and %d/%d (ephemeral)", pMean, pStddev, eMean, eStddev)
	}
}
