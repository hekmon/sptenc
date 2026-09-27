package core

import (
	"context"
	"fmt"
	"maps"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

func TestNewCAMBIChecker(t *testing.T) {
	// anything but a positive finite number or the off value is refused
	for _, tc := range []struct{ mean, max float64 }{
		{-2, CAMBIOffValue},
		{-0.5, 1},
		{math.NaN(), CAMBIOffValue},
		{1, math.Inf(1)},
		{CAMBIOffValue, math.Inf(-1)},
	} {
		if _, err := NewCAMBIChecker(tc.mean, tc.max); err == nil {
			t.Errorf("mean %v max %v should be refused", tc.mean, tc.max)
		}
	}
	// both off is the gate off, as is the zero value
	off, err := NewCAMBIChecker(CAMBIOffValue, CAMBIOffValue)
	if err != nil {
		t.Fatal(err)
	}
	if off.Enabled() || (CAMBIChecker{}).Enabled() {
		t.Error("the gate should be off")
	}
	if len(off.Thresholds()) != 0 || len((CAMBIChecker{}).Thresholds()) != 0 {
		t.Error("an off gate has no threshold")
	}
	if both, _ := NewCAMBIChecker(0, 2.5); !maps.Equal(both.Thresholds(), map[string]float64{"mean": 0, "max": 2.5}) {
		t.Errorf("unexpected thresholds: %v", both.Thresholds())
	}
	if maxOnly, _ := NewCAMBIChecker(CAMBIOffValue, 3); !maps.Equal(maxOnly.Thresholds(), map[string]float64{"max": 3}) {
		t.Errorf("unexpected thresholds: %v", maxOnly.Thresholds())
	}
	for _, tc := range []struct {
		name            string
		mean, max       float64
		banding         BandingStats
		valid, meanOnly bool
	}{
		{"at the thresholds", 1, 2, BandingStats{AddedMean: 1, AddedMax: 2}, true, true},
		{"mean above", 1, 2, BandingStats{AddedMean: 1.01, AddedMax: 1}, false, false},
		{"worst frame above", 1, 2, BandingStats{AddedMean: 0.5, AddedMax: 2.01}, false, true},
		{"mean alone", 1, CAMBIOffValue, BandingStats{AddedMean: 0.9, AddedMax: 24}, true, true},
		{"worst frame alone", CAMBIOffValue, 3, BandingStats{AddedMean: 20, AddedMax: 3.5}, false, true},
		// 0 is a threshold: no added banding at all
		{"zero, nothing added", 0, 0, BandingStats{}, true, true},
		{"zero, some added", 0, CAMBIOffValue, BandingStats{AddedMean: 0.001, AddedMax: 0.1}, false, false},
	} {
		cc, err := NewCAMBIChecker(tc.mean, tc.max)
		if err != nil {
			t.Fatalf("%s: %s", tc.name, err)
		}
		if !cc.Enabled() {
			t.Errorf("%s: the gate should be on", tc.name)
		}
		if got := cc.Validate(tc.banding); got != tc.valid {
			t.Errorf("%s: Validate = %t, want %t", tc.name, got, tc.valid)
		}
		if got := cc.ValidateMean(tc.banding); got != tc.meanOnly {
			t.Errorf("%s: ValidateMean = %t, want %t", tc.name, got, tc.meanOnly)
		}
	}
}

/*
 * The CAMBI stage, with the mock encoder of qpsearch_test.go: a linear VMAF curve gated on a
 * mean of 80 puts the VMAF search QP at 13, the banding of each QP is the test's.
 */

// addedBanding returns the banding statistics of an encode adding mean on average over the
// frames and max on its worst frame, to a source rated 3.
func addedBanding(mean, max float64) BandingStats {
	return BandingStats{AddedMean: mean, AddedMax: max, SourceMean: 3, EncodeMean: 3 + mean}
}

// bandingCurve returns the banding of every QP from 0 to 51: nothing added, but for the listed QPs.
func bandingCurve(listed map[int]BandingStats) map[int]BandingStats {
	curve := make(map[int]BandingStats, 52)
	for qp := 0; qp <= 51; qp++ {
		if banding, ok := listed[qp]; ok {
			curve[qp] = banding
		} else {
			curve[qp] = addedBanding(0, 0)
		}
	}
	return curve
}

// linearCurve returns linearVMAF for every QP from 0 to 51: mean 100 - 1.5 * QP.
func linearCurve() map[int]VMAFStats {
	curve := make(map[int]VMAFStats, 52)
	for qp := 0; qp <= 51; qp++ {
		curve[qp] = linearVMAF(qp)
	}
	return curve
}

// warningCallbacks records the warnings of a search.
type warningCallbacks struct {
	mockCallbacks
	mu       sync.Mutex
	warnings []string
}

func (w *warningCallbacks) Warning(workerID int, format string, a ...any) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.warnings = append(w.warnings, fmt.Sprintf(format, a...))
}

// cambiSearch runs FindAllSegmentsQP on nbSegments segments gated on a VMAF mean of 80 and the
// given CAMBI thresholds, the cache starting at 26 ± 13 (the cold start of a 0-51 range).
func cambiSearch(t *testing.T, encoder *mockEncoder, cambiMean, cambiMax float64, nbSegments, concurrency int,
	keepInvalidQP bool, cache StatsCache) (results QPSearchResults, workingDir string, callbacks *warningCallbacks) {
	t.Helper()
	auditor, err := NewVMAFChecker(VMAFOffValue, VMAFOffValue, VMAFOffValue, VMAFOffValue,
		VMAFOffValue, VMAFOffValue, VMAFOffValue, 80)
	if err != nil {
		t.Fatal(err)
	}
	cambiAuditor, err := NewCAMBIChecker(cambiMean, cambiMax)
	if err != nil {
		t.Fatal(err)
	}
	if cache == nil {
		cache = &mockStatsCache{mean: 26, stddev: 13}
	}
	workingDir = t.TempDir()
	segments := make([]string, nbSegments)
	for segment := range segments {
		segments[segment] = filepath.Join(workingDir, fmt.Sprintf("source_%06d.mkv", segment))
		if err := os.WriteFile(segments[segment], []byte("source"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	callbacks = &warningCallbacks{}
	if results, err = FindAllSegmentsQP(context.Background(), callbacks, QPSearchConfig{
		SegmentsPaths:        segments,
		Auditor:              auditor,
		CAMBIAuditor:         cambiAuditor,
		WorkingDir:           workingDir,
		StatsCache:           cache,
		KeepInvalidQP:        keepInvalidQP,
		Encoder:              encoder,
		NbConcurrentSegments: concurrency,
	}); err != nil {
		t.Fatalf("FindAllSegmentsQP failed: %s", err)
	}
	return
}

// encodedQPs returns the QPs encoded for a segment, in order.
func encodedQPs(t *testing.T, encoder *mockEncoder, segment int) (qps []int) {
	t.Helper()
	for _, call := range encoder.encodeCalls {
		callSegment, qp, err := extractSegmentAndQP(call.output)
		if err != nil {
			t.Fatal(err)
		}
		if callSegment == segment {
			qps = append(qps, qp)
		}
	}
	return
}

// measuredQPs returns the QPs measured for a segment with exactly these measures, in order.
func measuredQPs(t *testing.T, encoder *mockEncoder, segment int, measures VMAFMeasures) (qps []int) {
	t.Helper()
	for _, call := range encoder.vmafCalls {
		callSegment, qp, err := extractSegmentAndQP(call.distorted)
		if err != nil {
			t.Fatal(err)
		}
		if callSegment == segment && call.measures == measures {
			qps = append(qps, qp)
		}
	}
	return
}

// keptFiles returns the QPs whose encode of a segment is still in the working directory.
func keptFiles(t *testing.T, workingDir string, segment int) (qps []int) {
	t.Helper()
	entries, err := os.ReadDir(workingDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if fileSegment, qp, err := extractSegmentAndQP(entry.Name()); err == nil && fileSegment == segment {
			qps = append(qps, qp)
		}
	}
	slices.Sort(qps)
	return
}

var (
	scoreOnly       = VMAFMeasures{Score: true}
	bandingOnly     = VMAFMeasures{Banding: true}
	scoreAndBanding = VMAFMeasures{Score: true, Banding: true}
)

// TestFindAllSegmentsQP_CAMBIStageOff checks that the stage off is the search as it was: every
// pass measures the score alone, one per encode, and no banding is asked for (the mock has none
// to give).
func TestFindAllSegmentsQP_CAMBIStageOff(t *testing.T) {
	encoder := &mockEncoder{name: "mock", qpMax: 51, vmafResults: linearCurve()}
	results, _, _ := cambiSearch(t, encoder, CAMBIOffValue, CAMBIOffValue, 1, 1, false, nil)
	if !slices.Equal(results.QPs, []int{13}) || !slices.Equal(results.VMAFSearchQPs, []int{13}) {
		t.Errorf("expected QP 13 for both, got %v and %v", results.QPs, results.VMAFSearchQPs)
	}
	if got := encodedQPs(t, encoder, 0); !slices.Equal(got, []int{26, 13, 14}) {
		t.Errorf("expected the encodes of the VMAF search, 26 13 14, got %v", got)
	}
	if got := measuredQPs(t, encoder, 0, scoreOnly); len(got) != len(encoder.vmafCalls) || !slices.Equal(got, []int{26, 13, 14}) {
		t.Errorf("expected one score pass per encode and nothing else, got %+v", encoder.vmafCalls)
	}
	if results.TotalNbAttempts != 3 || results.NbCAMBIWalks != 0 || results.CAMBIWalkAttempts != 0 ||
		results.NbCAMBIBestEfforts != 0 || results.NbCAMBILowered() != 0 {
		t.Errorf("unexpected results: %+v", results)
	}
}

// TestFindAllSegmentsQP_CAMBIPass checks a segment passing at the QP of the VMAF search: one
// banding pass on its file, nothing encoded beyond the VMAF search.
func TestFindAllSegmentsQP_CAMBIPass(t *testing.T) {
	encoder := &mockEncoder{name: "mock", qpMax: 51, vmafResults: linearCurve(),
		bandingResults: bandingCurve(map[int]BandingStats{13: addedBanding(1, 5)})} // at the mean threshold
	results, workingDir, callbacks := cambiSearch(t, encoder, 1, CAMBIOffValue, 1, 1, false, nil)
	if !slices.Equal(results.QPs, []int{13}) || !slices.Equal(results.VMAFSearchQPs, []int{13}) {
		t.Errorf("expected QP 13 for both, got %v and %v", results.QPs, results.VMAFSearchQPs)
	}
	if got := encodedQPs(t, encoder, 0); !slices.Equal(got, []int{26, 13, 14}) {
		t.Errorf("expected the encodes of the VMAF search alone, got %v", got)
	}
	if got := measuredQPs(t, encoder, 0, bandingOnly); !slices.Equal(got, []int{13}) {
		t.Errorf("expected a banding pass on QP 13 alone, got %v", got)
	}
	if got := measuredQPs(t, encoder, 0, scoreAndBanding); len(got) != 0 {
		t.Errorf("expected no pass measuring both, got %v", got)
	}
	if results.TotalNbAttempts != 3 || results.NbCAMBIWalks != 0 || results.NbCAMBIBestEfforts != 0 {
		t.Errorf("unexpected results: %+v", results)
	}
	if got := keptFiles(t, workingDir, 0); !slices.Equal(got, []int{13}) {
		t.Errorf("expected the encode of QP 13 alone to remain, got %v", got)
	}
	if len(callbacks.warnings) != 0 {
		t.Errorf("unexpected warnings: %v", callbacks.warnings)
	}
}

// TestFindAllSegmentsQP_CAMBIOneStepDown checks a walk encoding the QP below: one encode and one
// pass measuring both.
func TestFindAllSegmentsQP_CAMBIOneStepDown(t *testing.T) {
	encoder := &mockEncoder{name: "mock", qpMax: 51, vmafResults: linearCurve(),
		bandingResults: bandingCurve(map[int]BandingStats{13: addedBanding(1.5, 4), 12: addedBanding(0.5, 2)})}
	results, workingDir, _ := cambiSearch(t, encoder, 1, CAMBIOffValue, 1, 1, false, nil)
	if !slices.Equal(results.QPs, []int{12}) || !slices.Equal(results.VMAFSearchQPs, []int{13}) {
		t.Errorf("expected QP 12 kept, 13 found by the VMAF search, got %v and %v", results.QPs, results.VMAFSearchQPs)
	}
	if got := encodedQPs(t, encoder, 0); !slices.Equal(got, []int{26, 13, 14, 12}) {
		t.Errorf("expected QP 12 encoded after the VMAF search, got %v", got)
	}
	if got := measuredQPs(t, encoder, 0, scoreAndBanding); !slices.Equal(got, []int{12}) {
		t.Errorf("expected QP 12 measured for both in one pass, got %v", got)
	}
	if results.TotalNbAttempts != 4 || results.CAMBIWalkAttempts != 1 || results.NbCAMBIWalks != 1 ||
		results.NbCAMBILowered() != 1 || results.NbCAMBIBestEfforts != 0 {
		t.Errorf("unexpected results: %+v", results)
	}
	// every attempt counts its frames, the walk's included
	if results.TotalEncodedFrames != 4*1000 {
		t.Errorf("expected 4000 encoded frames, got %d", results.TotalEncodedFrames)
	}
	if got := keptFiles(t, workingDir, 0); !slices.Equal(got, []int{12}) {
		t.Errorf("expected the encode of QP 12 alone to remain, got %v", got)
	}
}

// TestFindAllSegmentsQP_CAMBIReuse checks a walk on a QP the VMAF search encoded: its file is
// measured again for the banding, not encoded again.
func TestFindAllSegmentsQP_CAMBIReuse(t *testing.T) {
	encoder := &mockEncoder{name: "mock", qpMax: 51, vmafResults: linearCurve(),
		bandingResults: bandingCurve(map[int]BandingStats{13: addedBanding(1.5, 4), 12: addedBanding(0.5, 2)})}
	// from 16 ± 4, the VMAF search encodes 12 on its way to 13
	results, _, _ := cambiSearch(t, encoder, 1, CAMBIOffValue, 1, 1, false, &mockStatsCache{mean: 16, stddev: 4})
	vmafSearch := []int{16, 12, 13, 14}
	if got := encodedQPs(t, encoder, 0); !slices.Equal(got, vmafSearch) {
		t.Fatalf("expected the encodes of the VMAF search alone, %v, got %v", vmafSearch, got)
	}
	if !slices.Equal(results.QPs, []int{12}) || !slices.Equal(results.VMAFSearchQPs, []int{13}) {
		t.Errorf("expected QP 12 kept, 13 found by the VMAF search, got %v and %v", results.QPs, results.VMAFSearchQPs)
	}
	if got := measuredQPs(t, encoder, 0, bandingOnly); !slices.Equal(got, []int{13, 12}) {
		t.Errorf("expected banding passes on QPs 13 then 12, got %v", got)
	}
	if results.TotalNbAttempts != len(vmafSearch) || results.CAMBIWalkAttempts != 0 || results.NbCAMBIWalks != 1 {
		t.Errorf("unexpected results: %+v", results)
	}
}

// TestFindAllSegmentsQP_CAMBIHump checks that the walk goes on past a QP adding more banding than
// the one above it: the added banding is not monotonic in QP.
func TestFindAllSegmentsQP_CAMBIHump(t *testing.T) {
	encoder := &mockEncoder{name: "mock", qpMax: 51, vmafResults: linearCurve(),
		bandingResults: bandingCurve(map[int]BandingStats{
			13: addedBanding(1.5, 4),
			12: addedBanding(2.5, 6),
			11: addedBanding(0.8, 3),
			10: addedBanding(1.2, 4), // a pass at 11 ends the walk: never measured
		})}
	results, _, _ := cambiSearch(t, encoder, 1, CAMBIOffValue, 1, 1, false, nil)
	if !slices.Equal(results.QPs, []int{11}) {
		t.Errorf("expected QP 11, got %v", results.QPs)
	}
	if got := measuredQPs(t, encoder, 0, scoreAndBanding); !slices.Equal(got, []int{12, 11}) {
		t.Errorf("expected QPs 12 then 11 measured for both, got %v", got)
	}
	if results.CAMBIWalkAttempts != 2 || results.NbCAMBIBestEfforts != 0 {
		t.Errorf("unexpected results: %+v", results)
	}
}

// TestFindAllSegmentsQP_CAMBIMaxUnreachable checks the best effort giving up the worst frame
// threshold: after walking down to qpMin, the highest QP passing the VMAF thresholds and the
// mean one is kept.
func TestFindAllSegmentsQP_CAMBIMaxUnreachable(t *testing.T) {
	listed := make(map[int]BandingStats, 52)
	for qp := 0; qp <= 51; qp++ {
		listed[qp] = addedBanding(0.4, 3) // every worst frame above 2
	}
	listed[13] = addedBanding(1.2, 3)
	listed[12] = addedBanding(0.9, 3)
	encoder := &mockEncoder{name: "mock", qpMax: 51, vmafResults: linearCurve(), bandingResults: listed}
	results, workingDir, callbacks := cambiSearch(t, encoder, 1, 2, 1, 1, false, nil)
	if !slices.Equal(results.QPs, []int{12}) || !slices.Equal(results.VMAFSearchQPs, []int{13}) {
		t.Errorf("expected QP 12 kept, 13 found by the VMAF search, got %v and %v", results.QPs, results.VMAFSearchQPs)
	}
	// nothing passing takes every QP down to qpMin
	walk := []int{12, 11, 10, 9, 8, 7, 6, 5, 4, 3, 2, 1, 0}
	if got := measuredQPs(t, encoder, 0, scoreAndBanding); !slices.Equal(got, walk) {
		t.Errorf("expected the walk down to QP 0, got %v", got)
	}
	if results.NbCAMBIBestEfforts != 1 || results.NbCAMBIWalks != 1 || results.CAMBIWalkAttempts != len(walk) ||
		results.NbCAMBILowered() != 1 || results.TotalNbAttempts != 3+len(walk) {
		t.Errorf("unexpected results: %+v", results)
	}
	if len(callbacks.warnings) != 1 || !strings.Contains(callbacks.warnings[0], "worst frame") ||
		!strings.Contains(callbacks.warnings[0], "QP 12") {
		t.Errorf("expected a warning giving up the worst frame threshold, got %v", callbacks.warnings)
	}
	if got := keptFiles(t, workingDir, 0); !slices.Equal(got, []int{12}) {
		t.Errorf("expected the encode of QP 12 alone to remain, got %v", got)
	}
}

// TestFindAllSegmentsQP_CAMBIMeanUnreachable checks the best effort when no QP passes the mean
// threshold: the QP of the VMAF search is kept, no size bought for a threshold nothing meets.
func TestFindAllSegmentsQP_CAMBIMeanUnreachable(t *testing.T) {
	listed := make(map[int]BandingStats, 52)
	for qp := 0; qp <= 51; qp++ {
		listed[qp] = addedBanding(1.5, 3)
	}
	encoder := &mockEncoder{name: "mock", qpMax: 51, vmafResults: linearCurve(), bandingResults: listed}
	results, workingDir, callbacks := cambiSearch(t, encoder, 1, CAMBIOffValue, 1, 1, false, nil)
	if !slices.Equal(results.QPs, []int{13}) {
		t.Errorf("expected QP 13 kept, got %v", results.QPs)
	}
	if got := measuredQPs(t, encoder, 0, scoreAndBanding); len(got) != 13 {
		t.Errorf("expected the walk down to QP 0, got %v", got)
	}
	if results.NbCAMBIBestEfforts != 1 || results.NbCAMBIWalks != 1 || results.NbCAMBILowered() != 0 {
		t.Errorf("unexpected results: %+v", results)
	}
	if len(callbacks.warnings) != 1 || !strings.Contains(callbacks.warnings[0], "mean threshold") {
		t.Errorf("expected a warning giving up the mean threshold, got %v", callbacks.warnings)
	}
	if got := keptFiles(t, workingDir, 0); !slices.Equal(got, []int{13}) {
		t.Errorf("expected the encode of QP 13 alone to remain, got %v", got)
	}
}

// TestFindAllSegmentsQP_CAMBIVMAFBestEffort checks that a VMAF best effort has its banding
// measured all the same, and can be a CAMBI best effort too.
func TestFindAllSegmentsQP_CAMBIVMAFBestEffort(t *testing.T) {
	vmafCurve := make(map[int]VMAFStats, 52)
	for qp := 0; qp <= 51; qp++ {
		vmafCurve[qp] = linearVMAF(qp + 20) // 70 at best: no QP passes a mean of 80
	}
	for _, tc := range []struct {
		name            string
		banding         BandingStats
		cambiBestEffort int
	}{
		{"banding passing", addedBanding(0.5, 2), 0},
		{"banding failing", addedBanding(1.5, 4), 1},
	} {
		encoder := &mockEncoder{name: "mock", qpMax: 51, vmafResults: vmafCurve,
			bandingResults: bandingCurve(map[int]BandingStats{0: tc.banding})}
		results, _, callbacks := cambiSearch(t, encoder, 1, CAMBIOffValue, 1, 1, false, nil)
		if !slices.Equal(results.QPs, []int{0}) || results.NbBestEfforts != 1 {
			t.Errorf("%s: expected a VMAF best effort at QP 0, got %v (%d)", tc.name, results.QPs, results.NbBestEfforts)
		}
		if got := measuredQPs(t, encoder, 0, bandingOnly); !slices.Equal(got, []int{0}) {
			t.Errorf("%s: expected the banding of QP 0 measured, got %v", tc.name, got)
		}
		if results.NbCAMBIBestEfforts != tc.cambiBestEffort || results.CAMBIWalkAttempts != 0 {
			t.Errorf("%s: expected %d CAMBI best effort and no walk encode, got %+v", tc.name, tc.cambiBestEffort, results)
		}
		// the VMAF warning, and the CAMBI one when it is one
		if len(callbacks.warnings) != 1+tc.cambiBestEffort {
			t.Errorf("%s: unexpected warnings: %v", tc.name, callbacks.warnings)
		}
	}
}

// TestFindAllSegmentsQP_CAMBIKeepInvalidQP checks that the encodes of the walk follow
// KeepInvalidQP like the others.
func TestFindAllSegmentsQP_CAMBIKeepInvalidQP(t *testing.T) {
	curve := bandingCurve(map[int]BandingStats{13: addedBanding(1.5, 4), 12: addedBanding(2.5, 6), 11: addedBanding(0.8, 3)})
	for _, keep := range []bool{false, true} {
		encoder := &mockEncoder{name: "mock", qpMax: 51, vmafResults: linearCurve(), bandingResults: curve}
		results, workingDir, _ := cambiSearch(t, encoder, 1, CAMBIOffValue, 1, 1, keep, nil)
		want := []int{11}
		if keep {
			want = []int{11, 12, 13, 14, 26}
		}
		if got := keptFiles(t, workingDir, 0); !slices.Equal(got, want) {
			t.Errorf("KeepInvalidQP %t: expected the encodes of %v to remain, got %v", keep, want, got)
		}
		if !slices.Equal(results.QPs, []int{11}) {
			t.Errorf("KeepInvalidQP %t: expected QP 11, got %v", keep, results.QPs)
		}
	}
}

// TestFindAllSegmentsQP_CAMBIFinalFrameCount checks that the frame count of the encode is
// checked on the QP kept, the walk's.
func TestFindAllSegmentsQP_CAMBIFinalFrameCount(t *testing.T) {
	encoder := &mockEncoder{name: "mock", qpMax: 51, vmafResults: linearCurve(),
		bandingResults: bandingCurve(map[int]BandingStats{13: addedBanding(1.5, 4)})}
	cambiSearch(t, encoder, 1, CAMBIOffValue, 1, 1, false, nil)
	if len(encoder.probePaths) != 2 {
		t.Fatalf("expected the source and the encode kept probed, got %v", encoder.probePaths)
	}
	if _, qp, err := extractSegmentAndQP(encoder.probePaths[1]); err != nil || qp != 12 {
		t.Errorf("expected the encode of QP 12 checked, got %s", encoder.probePaths[1])
	}
	// and a count off there fails the segment
	encoder = &mockEncoder{name: "mock", qpMax: 51, vmafResults: linearCurve(),
		bandingResults: bandingCurve(map[int]BandingStats{13: addedBanding(1.5, 4)}),
		probeResults: []VideoStream{
			{NbFrames: 1000, NbReadFrames: 1000, RFrameRate: "24/1", Height: 1080},
			{NbFrames: 999, NbReadFrames: 999, RFrameRate: "24/1", Height: 1080},
		}}
	auditor, _ := NewVMAFChecker(VMAFOffValue, VMAFOffValue, VMAFOffValue, VMAFOffValue,
		VMAFOffValue, VMAFOffValue, VMAFOffValue, 80)
	cambiAuditor, _ := NewCAMBIChecker(1, CAMBIOffValue)
	workingDir := t.TempDir()
	source := filepath.Join(workingDir, "source.mkv")
	if err := os.WriteFile(source, []byte("source"), 0644); err != nil {
		t.Fatal(err)
	}
	_, err := FindAllSegmentsQP(context.Background(), &mockCallbacks{}, QPSearchConfig{
		SegmentsPaths: []string{source},
		Auditor:       auditor,
		CAMBIAuditor:  cambiAuditor,
		WorkingDir:    workingDir,
		StatsCache:    &mockStatsCache{mean: 26, stddev: 13},
		Encoder:       encoder,
	})
	if err == nil || !strings.Contains(err.Error(), "999 frames instead of 1000") {
		t.Errorf("expected the frame count of the encode kept to fail, got %v", err)
	}
}

// TestFindAllSegmentsQP_CAMBICacheLearnsVMAFQP checks that the ephemeral cache learns the QP of
// the VMAF search, not the one the walk lowered it to.
func TestFindAllSegmentsQP_CAMBICacheLearnsVMAFQP(t *testing.T) {
	encoder := &mockEncoder{name: "mock", qpMax: 51, vmafResults: linearCurve(),
		bandingBySegment: map[int]map[int]BandingStats{
			0: bandingCurve(map[int]BandingStats{13: addedBanding(1.5, 4), 12: addedBanding(2, 4), 11: addedBanding(2, 4)}),
			1: bandingCurve(nil),
		}}
	results, _, _ := cambiSearch(t, encoder, 1, CAMBIOffValue, 2, 1, false, nil)
	if !slices.Equal(results.QPs, []int{10, 13}) || !slices.Equal(results.VMAFSearchQPs, []int{13, 13}) {
		t.Fatalf("expected QPs 10 and 13, found at 13 and 13, got %v and %v", results.QPs, results.VMAFSearchQPs)
	}
	// the seed of 26 weighs one segment: learning 13 starts the next search at 19.5, rounded to
	// 20, where learning the 10 kept would start it at 18
	if got := encodedQPs(t, encoder, 1); len(got) == 0 || got[0] != 20 {
		t.Errorf("expected the second segment to start at QP 20, got %v", got)
	}
}

// TestFindAllSegmentsQP_CAMBIConcurrent checks that concurrent segments get the results of a
// sequential search: the QPs of the VMAF search and the QPs kept. The attempts are not compared:
// segments searched together start from other points (see FindAllSegmentsQP), and the walk
// encodes what the VMAF search did not.
func TestFindAllSegmentsQP_CAMBIConcurrent(t *testing.T) {
	const nbSegments = 12
	// with a VMAF mean of 80: slope 1 -> QP 20, slope 1.5 -> QP 13, slope 2 -> QP 10
	slopes := []float64{1, 1.5, 2}
	profiles := []map[int]BandingStats{
		nil, // passes at the QP of the VMAF search
		{20: addedBanding(1.5, 4), 13: addedBanding(1.5, 4), 10: addedBanding(1.5, 4)}, // one step down
		{20: addedBanding(1.5, 4), 19: addedBanding(2.5, 4), 13: addedBanding(1.5, 4), 12: addedBanding(2.5, 4),
			10: addedBanding(1.5, 4), 9: addedBanding(2.5, 4)}, // two steps down
		{}, // no QP passes: filled below
	}
	unreachable := make(map[int]BandingStats, 52)
	for qp := 0; qp <= 51; qp++ {
		unreachable[qp] = addedBanding(3, 5)
	}
	profiles[3] = unreachable
	vmafBySegment := make(map[int]map[int]VMAFStats, nbSegments)
	bandingBySegment := make(map[int]map[int]BandingStats, nbSegments)
	for segment := range nbSegments {
		vmafBySegment[segment] = make(map[int]VMAFStats, 52)
		for qp := 0; qp <= 51; qp++ {
			mean := 100 - slopes[segment%len(slopes)]*float64(qp)
			vmafBySegment[segment][qp] = VMAFStats{Mean: mean, Minimum: mean - 5, Median: mean, HarmonicMean: mean - 0.5, Maximum: mean + 2}
		}
		bandingBySegment[segment] = bandingCurve(profiles[segment%len(profiles)])
	}
	var reference QPSearchResults
	for _, concurrency := range []int{1, 4} {
		encoder := &mockEncoder{name: "mock", qpMax: 51, vmafBySegment: vmafBySegment, bandingBySegment: bandingBySegment}
		results, _, _ := cambiSearch(t, encoder, 1, CAMBIOffValue, nbSegments, concurrency, false, nil)
		if concurrency == 1 {
			reference = results
			// the expected QPs, from the curves
			vmafQPs := []int{20, 13, 10}
			for segment, qp := range results.QPs {
				vmafQP := vmafQPs[segment%len(vmafQPs)]
				want := vmafQP
				switch segment % len(profiles) {
				case 1:
					want = vmafQP - 1
				case 2:
					want = vmafQP - 2
				}
				if qp != want || results.VMAFSearchQPs[segment] != vmafQP {
					t.Errorf("segment %d: expected QP %d found at %d, got %d found at %d", segment, want, vmafQP, qp, results.VMAFSearchQPs[segment])
				}
			}
			continue
		}
		if !slices.Equal(results.QPs, reference.QPs) || !slices.Equal(results.VMAFSearchQPs, reference.VMAFSearchQPs) {
			t.Errorf("-C %d: expected QPs %v found at %v, got %v found at %v", concurrency,
				reference.QPs, reference.VMAFSearchQPs, results.QPs, results.VMAFSearchQPs)
		}
		if results.NbCAMBIWalks != reference.NbCAMBIWalks || results.NbCAMBIBestEfforts != reference.NbCAMBIBestEfforts ||
			results.NbCAMBILowered() != reference.NbCAMBILowered() {
			t.Errorf("-C %d: expected %d walks, %d best efforts and %d lowered, got %d, %d and %d", concurrency,
				reference.NbCAMBIWalks, reference.NbCAMBIBestEfforts, reference.NbCAMBILowered(),
				results.NbCAMBIWalks, results.NbCAMBIBestEfforts, results.NbCAMBILowered())
		}
	}
	if reference.NbCAMBIWalks != 9 || reference.NbCAMBIBestEfforts != 3 || reference.NbCAMBILowered() != 6 {
		t.Errorf("expected 9 walks, 3 best efforts and 6 lowered, got %d, %d and %d",
			reference.NbCAMBIWalks, reference.NbCAMBIBestEfforts, reference.NbCAMBILowered())
	}
}

// TestSearchSegmentCAMBI_VMAFFailures checks the QPs below the QP of the VMAF search that fail the
// VMAF thresholds (fidelity rising with the QP, as on smooth synthetic gradients): never kept,
// not measured when the VMAF search encoded them, ignored when the walk did.
func TestSearchSegmentCAMBI_VMAFFailures(t *testing.T) {
	pass, fail := linearVMAF(10), linearVMAF(20) // mean 85 and 70, against a mean of 80
	auditor, _ := NewVMAFChecker(VMAFOffValue, VMAFOffValue, VMAFOffValue, VMAFOffValue,
		VMAFOffValue, VMAFOffValue, VMAFOffValue, 80)
	for _, tc := range []struct {
		name     string
		cambiMax float64
		searched map[int]VMAFStats // what the VMAF search encoded
		walked   map[int]VMAFStats // what the walk encodes
		banding  map[int]BandingStats
		kept     int
		measured []int // banding passes
		encoded  []int // walk encodes
	}{
		{
			name:     "encoded by the VMAF search",
			cambiMax: CAMBIOffValue,
			searched: map[int]VMAFStats{13: pass, 12: fail, 11: pass},
			banding:  map[int]BandingStats{13: addedBanding(1.5, 4), 12: addedBanding(0, 0), 11: addedBanding(0.5, 2)},
			kept:     11,
			measured: []int{13, 11},
		},
		{
			name:     "encoded by the walk",
			cambiMax: CAMBIOffValue,
			searched: map[int]VMAFStats{13: pass},
			walked:   map[int]VMAFStats{12: fail, 11: pass},
			banding:  map[int]BandingStats{13: addedBanding(1.5, 4), 12: addedBanding(0, 0), 11: addedBanding(0.5, 2)},
			kept:     11,
			measured: []int{13},
			encoded:  []int{12, 11},
		},
		{
			// the best effort can not keep it either: 11 fails the max threshold, and passes the
			// VMAF thresholds and the mean one
			name:     "best effort",
			cambiMax: 1,
			searched: map[int]VMAFStats{13: pass},
			walked:   map[int]VMAFStats{12: fail, 11: pass, 10: pass, 9: pass, 8: pass, 7: pass, 6: pass, 5: pass, 4: pass, 3: pass, 2: pass, 1: pass, 0: pass},
			banding: map[int]BandingStats{13: addedBanding(1.5, 4), 12: addedBanding(0, 0), 11: addedBanding(0.5, 2),
				10: addedBanding(1.5, 4), 9: addedBanding(1.5, 4), 8: addedBanding(1.5, 4), 7: addedBanding(1.5, 4),
				6: addedBanding(1.5, 4), 5: addedBanding(1.5, 4), 4: addedBanding(1.5, 4), 3: addedBanding(1.5, 4),
				2: addedBanding(1.5, 4), 1: addedBanding(1.5, 4), 0: addedBanding(1.5, 4)},
			kept:     11,
			measured: []int{13},
			encoded:  []int{12, 11, 10, 9, 8, 7, 6, 5, 4, 3, 2, 1, 0},
		},
	} {
		cambiAuditor, err := NewCAMBIChecker(1, tc.cambiMax)
		if err != nil {
			t.Fatal(err)
		}
		vmafResults := make(map[int]VMAFStats)
		for qp, stats := range tc.searched {
			vmafResults[qp] = stats
		}
		for qp, stats := range tc.walked {
			vmafResults[qp] = stats
		}
		encoder := &mockEncoder{name: "mock", qpMax: 51, vmafResults: vmafResults, bandingResults: tc.banding}
		workingDir := t.TempDir()
		results := make(map[int]VMAFStats)
		var testedQPs []int
		for qp, stats := range tc.searched {
			results[qp] = stats
			testedQPs = append(testedQPs, qp)
			if err := os.WriteFile(filepath.Join(workingDir, fmt.Sprintf(segEncodedOutputFormat, 0, qp)), []byte("dummy"), 0644); err != nil {
				t.Fatal(err)
			}
		}
		config := QPSearchConfig{Auditor: auditor, CAMBIAuditor: cambiAuditor, WorkingDir: workingDir, Encoder: encoder}
		outcome, err := searchSegmentCAMBI(context.Background(), &mockCallbacks{}, config, 0, 0, "source.mkv",
			VideoStream{NbReadFrames: 1000}, 13, results, &testedQPs)
		if err != nil {
			t.Fatalf("%s: %s", tc.name, err)
		}
		if outcome.qp != tc.kept {
			t.Errorf("%s: expected QP %d kept, got %d", tc.name, tc.kept, outcome.qp)
		}
		if got := measuredQPs(t, encoder, 0, bandingOnly); !slices.Equal(got, tc.measured) {
			t.Errorf("%s: expected banding passes on %v, got %v", tc.name, tc.measured, got)
		}
		if got := encodedQPs(t, encoder, 0); !slices.Equal(got, tc.encoded) || outcome.encodes != len(tc.encoded) {
			t.Errorf("%s: expected the walk to encode %v, got %v (%d)", tc.name, tc.encoded, got, outcome.encodes)
		}
		// the encodes of the walk join the tested QPs, their files follow KeepInvalidQP
		if len(testedQPs) != len(tc.searched)+len(tc.encoded) {
			t.Errorf("%s: expected %d tested QPs, got %v", tc.name, len(tc.searched)+len(tc.encoded), testedQPs)
		}
	}
}
