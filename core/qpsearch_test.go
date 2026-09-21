package core

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// mockEncoder is a test double that returns pre-computed VMAF results without calling ffmpeg.
type mockEncoder struct {
	name          string
	qpMin         int
	qpMax         int
	vmafResults   map[int]VMAFStats
	vmafBySegment map[int]map[int]VMAFStats
	encodeCalls   []mockEncodeCall
	vmafCalls     []mockVMAFCall
	encodeErr     error
	vmafErr       error
	probeErr      error
	probeErrors   []error
	probeResults  []VideoStream
	probeCallIdx  int
	validateQP    bool
}

type mockEncodeCall struct {
	input  string
	output string
	qp     int
}

type mockVMAFCall struct {
	reference string
	distorted string
}

func (m *mockEncoder) Name() string { return m.name }

func (m *mockEncoder) QPRange() (min, max int, found bool) {
	return m.qpMin, m.qpMax, true
}

func (m *mockEncoder) Encode(_ context.Context, input, output string, qp int, _ VideoStream,
	_ func(ProgressStats), _ func(string), _ func(error)) error {
	if m.encodeErr != nil {
		return m.encodeErr
	}
	if m.validateQP && (qp < m.qpMin || qp > m.qpMax) {
		return fmt.Errorf("QP %d out of range [%d, %d]", qp, m.qpMin, m.qpMax)
	}
	m.encodeCalls = append(m.encodeCalls, mockEncodeCall{input, output, qp})
	// Create a dummy file so getFileSize succeeds for the selected QP.
	return os.WriteFile(output, []byte("dummy"), 0644)
}

func (m *mockEncoder) ComputeVMAF(_ context.Context, reference, distorted string, _ VideoStream,
	_ func(ProgressStats), _ func(string), _ func(error)) (VMAFStats, error) {
	if m.vmafErr != nil {
		return VMAFStats{}, m.vmafErr
	}
	m.vmafCalls = append(m.vmafCalls, mockVMAFCall{reference, distorted})
	seg, qp, err := extractSegmentAndQP(distorted)
	if err != nil {
		return VMAFStats{}, err
	}
	if m.vmafBySegment != nil {
		if segMap, ok := m.vmafBySegment[seg]; ok {
			if stats, ok := segMap[qp]; ok {
				return stats, nil
			}
			return VMAFStats{}, fmt.Errorf("no mock VMAF for segment %d QP %d", seg, qp)
		}
		return VMAFStats{}, fmt.Errorf("no mock VMAF for segment %d", seg)
	}
	stats, ok := m.vmafResults[qp]
	if !ok {
		return VMAFStats{}, fmt.Errorf("no mock VMAF for QP %d", qp)
	}
	return stats, nil
}

func (m *mockEncoder) ProbeStream(_ context.Context, _ string, _ func(int64),
	_ func(string), _ func(error)) (VideoStream, error) {
	if m.probeErr != nil {
		return VideoStream{}, m.probeErr
	}
	if m.probeCallIdx < len(m.probeResults) {
		res := m.probeResults[m.probeCallIdx]
		m.probeCallIdx++
		return res, nil
	}
	if m.probeCallIdx < len(m.probeErrors) && m.probeErrors[m.probeCallIdx] != nil {
		err := m.probeErrors[m.probeCallIdx]
		m.probeCallIdx++
		return VideoStream{}, err
	}
	m.probeCallIdx++
	return VideoStream{NbFrames: 1000, NbReadFrames: 1000, RFrameRate: "24/1", Height: 1080, Duration: time.Minute}, nil
}

func extractSegmentAndQP(path string) (segment, qp int, err error) {
	base := filepath.Base(path)
	if !strings.HasPrefix(base, "seg_") {
		return 0, 0, fmt.Errorf("invalid segment filename: %s", base)
	}
	segStr := base[4:10]
	segment, err = strconv.Atoi(segStr)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid segment index in %s: %w", base, err)
	}
	qpIdx := strings.LastIndex(base, "_qp")
	if qpIdx == -1 {
		return 0, 0, fmt.Errorf("no qp in path %s", base)
	}
	qp, err = strconv.Atoi(base[qpIdx+3 : qpIdx+6])
	if err != nil {
		return 0, 0, fmt.Errorf("invalid qp in %s: %w", base, err)
	}
	return
}

// linearVMAF generates a monotonic VMAF curve: mean = 100 - 1.5*qp.
func linearVMAF(qp int) VMAFStats {
	mean := 100.0 - 1.5*float64(qp)
	return VMAFStats{
		Mean:         mean,
		Minimum:      mean - 5,
		Percentile1:  mean - 4,
		Percentile5:  mean - 3,
		Percentile10: mean - 2,
		Percentile25: mean - 1,
		Median:       mean,
		HarmonicMean: mean - 0.5,
		Maximum:      mean + 2,
	}
}

// mockStatsCache is an in-memory StatsCache for tests.
type mockStatsCache struct {
	mean   int
	stddev int
}

func (m *mockStatsCache) GetMeanStdDev() (mean, stddev int) {
	return m.mean, m.stddev
}

func (m *mockStatsCache) Snapshot() (mean, stddev float64, weight int, ok bool) {
	return float64(m.mean), float64(m.stddev), 1, true
}

// mockCallbacks satisfies QPSearchCallbacks for tests.
type mockCallbacks struct{}

func (m *mockCallbacks) Debug(workerID int, format string, a ...any)                   {}
func (m *mockCallbacks) Warning(workerID int, format string, a ...any)                 {}
func (m *mockCallbacks) Error(workerID int, err error)                                 {}
func (m *mockCallbacks) OnSegmentStart(workerID, segmentIndex int, segmentPath string) {}
func (m *mockCallbacks) OnSegmentDone(workerID, segmentFinalQP, segmentFrames, segmentNbAttempts int, currentTotalDuration time.Duration, currentTotalSize int64) {
}
func (m *mockCallbacks) OnSegmentNewCandidate(workerID, qpCandidate int)           {}
func (m *mockCallbacks) OnSegmentAnalysisStart(workerID int, fileSize int64)       {}
func (m *mockCallbacks) OnSegmentAnalysisProgress(workerID int, read int64)        {}
func (m *mockCallbacks) OnSegmentAnalysisStop(workerID int)                        {}
func (m *mockCallbacks) OnSegmentEncodeStart(workerID int, totalFrames int)        {}
func (m *mockCallbacks) OnSegmentEncodeProgress(workerID int, stats ProgressStats) {}
func (m *mockCallbacks) OnSegmentEncodeStop(workerID int)                          {}
func (m *mockCallbacks) OnSegmentVMAFStart(workerID int, totalFrames int)          {}
func (m *mockCallbacks) OnSegmentVMAFProgress(workerID int, stats ProgressStats)   {}
func (m *mockCallbacks) OnSegmentVMAFStop(workerID int)                            {}

func TestFindAllSegmentsQP_Convergence(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	// Build monotonic VMAF results for QP 0-51.
	vmafResults := make(map[int]VMAFStats, 52)
	for qp := 0; qp <= 51; qp++ {
		vmafResults[qp] = linearVMAF(qp)
	}

	encoder := &mockEncoder{
		name:        "mock",
		qpMin:       0,
		qpMax:       51,
		vmafResults: vmafResults,
	}

	// Require mean >= 80 → optimal QP is 13 (mean=80.5 at qp=13, mean=79 at qp=14).
	auditor, err := NewVMAFChecker(VMAFOffValue, VMAFOffValue, VMAFOffValue, VMAFOffValue,
		VMAFOffValue, VMAFOffValue, VMAFOffValue, 80)
	if err != nil {
		t.Fatalf("failed to create auditor: %v", err)
	}

	// Empty cache → defaults to mean=26, stddev=13 (same heuristic as real empty cache for QP range 0-51).
	statsCache := &mockStatsCache{mean: 26, stddev: 13}

	config := QPSearchConfig{
		SegmentsPaths: []string{filepath.Join(tmpDir, "segment.mkv")},
		Auditor:       auditor,
		WorkingDir:    tmpDir,
		StatsCache:    statsCache,
		Encoder:       encoder,
	}

	// Create a dummy source segment so ProbeStream has a file to stat.
	if err := os.WriteFile(config.SegmentsPaths[0], []byte("source"), 0644); err != nil {
		t.Fatalf("failed to create source segment: %v", err)
	}

	results, err := FindAllSegmentsQP(ctx, &mockCallbacks{}, config)
	if err != nil {
		t.Fatalf("FindAllSegmentsQP failed: %v", err)
	}

	// With default cache (mean=26, stddev=13), expect convergence to QP 13
	// in well under a linear scan (52 attempts).
	if results.TotalNbAttempts >= 20 {
		t.Errorf("expected fast convergence (<20 attempts), got %d", results.TotalNbAttempts)
	}
	if len(results.QPs) != 1 || results.QPs[0] != 13 {
		t.Errorf("expected QP 13, got %v", results.QPs)
	}
	if results.NbBestEfforts != 0 {
		t.Errorf("expected no best-effort segments, got %d", results.NbBestEfforts)
	}
}

func TestFindAllSegmentsQP_BestEffort(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	// Even at QP 0 the mean is only 70, which is below the threshold.
	vmafResults := make(map[int]VMAFStats, 52)
	for qp := 0; qp <= 51; qp++ {
		vmafResults[qp] = linearVMAF(qp + 20) // shift down so max mean is 70
	}

	encoder := &mockEncoder{
		name:        "mock",
		qpMin:       0,
		qpMax:       51,
		vmafResults: vmafResults,
	}

	auditor, err := NewVMAFChecker(VMAFOffValue, VMAFOffValue, VMAFOffValue, VMAFOffValue,
		VMAFOffValue, VMAFOffValue, VMAFOffValue, 80)
	if err != nil {
		t.Fatalf("failed to create auditor: %v", err)
	}

	statsCache := &mockStatsCache{mean: 26, stddev: 13}

	config := QPSearchConfig{
		SegmentsPaths: []string{filepath.Join(tmpDir, "segment.mkv")},
		Auditor:       auditor,
		WorkingDir:    tmpDir,
		StatsCache:    statsCache,
		Encoder:       encoder,
	}

	if err := os.WriteFile(config.SegmentsPaths[0], []byte("source"), 0644); err != nil {
		t.Fatalf("failed to create source segment: %v", err)
	}

	results, err := FindAllSegmentsQP(ctx, &mockCallbacks{}, config)
	if err != nil {
		t.Fatalf("FindAllSegmentsQP failed: %v", err)
	}

	if results.NbBestEfforts != 1 {
		t.Errorf("expected 1 best-effort segment, got %d", results.NbBestEfforts)
	}
	if len(results.QPs) != 1 || results.QPs[0] != 0 {
		t.Errorf("expected QP 0 for best effort, got %v", results.QPs)
	}
}

func TestFindAllSegmentsQP_CacheGuidesSearch(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	vmafResults := make(map[int]VMAFStats, 52)
	for qp := 0; qp <= 51; qp++ {
		vmafResults[qp] = linearVMAF(qp)
	}

	encoder := &mockEncoder{
		name:        "mock",
		qpMin:       0,
		qpMax:       51,
		vmafResults: vmafResults,
	}

	auditor, err := NewVMAFChecker(VMAFOffValue, VMAFOffValue, VMAFOffValue, VMAFOffValue,
		VMAFOffValue, VMAFOffValue, VMAFOffValue, 80)
	if err != nil {
		t.Fatalf("failed to create auditor: %v", err)
	}

	// Seed cache with mean=13, stddev=2 — the optimal QP.
	statsCache := &mockStatsCache{mean: 13, stddev: 2}

	config := QPSearchConfig{
		SegmentsPaths: []string{filepath.Join(tmpDir, "segment.mkv")},
		Auditor:       auditor,
		WorkingDir:    tmpDir,
		StatsCache:    statsCache,
		Encoder:       encoder,
	}

	if err := os.WriteFile(config.SegmentsPaths[0], []byte("source"), 0644); err != nil {
		t.Fatalf("failed to create source segment: %v", err)
	}

	results, err := FindAllSegmentsQP(ctx, &mockCallbacks{}, config)
	if err != nil {
		t.Fatalf("FindAllSegmentsQP failed: %v", err)
	}

	// With cache mean=13, stddev=2, the first candidate is 13 (the optimum).
	// The search should converge in very few attempts.
	if results.TotalNbAttempts > 5 {
		t.Errorf("expected cache-guided fast convergence (<=5 attempts), got %d", results.TotalNbAttempts)
	}
	if len(results.QPs) != 1 || results.QPs[0] != 13 {
		t.Errorf("expected QP 13, got %v", results.QPs)
	}
}

func TestFindAllSegmentsQP_MultiSegmentDifferentQPs(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	// Segment 0 converges at QP 13 (mean>=80)
	// Segment 1 converges at QP 20 (mean>=70, shifted curve)
	vmafBySegment := map[int]map[int]VMAFStats{
		0: {},
		1: {},
	}
	for qp := 0; qp <= 51; qp++ {
		vmafBySegment[0][qp] = linearVMAF(qp)
		vmafBySegment[1][qp] = linearVMAF(qp + 7) // shifted: at qp=20, mean=100-1.5*27=59.5... wait that's wrong
	}
	// Let me recalculate: linearVMAF(qp) mean = 100 - 1.5*qp
	// For segment 1, I want optimal QP to be 20 with threshold mean>=70.
	// If mean = 100 - 1.5*(qp+7), then at qp=20, mean = 100 - 1.5*27 = 59.5. Too low.
	// Instead, use a flatter curve for segment 1.
	for qp := 0; qp <= 51; qp++ {
		vmafBySegment[0][qp] = linearVMAF(qp)
		mean := 100.0 - 1.0*float64(qp) // flatter: qp=30 -> mean=70
		vmafBySegment[1][qp] = VMAFStats{
			Mean:         mean,
			Minimum:      mean - 5,
			Percentile1:  mean - 4,
			Percentile5:  mean - 3,
			Percentile10: mean - 2,
			Percentile25: mean - 1,
			Median:       mean,
			HarmonicMean: mean - 0.5,
			Maximum:      mean + 2,
		}
	}

	encoder := &mockEncoder{
		name:          "mock",
		qpMin:         0,
		qpMax:         51,
		vmafBySegment: vmafBySegment,
	}

	auditor, err := NewVMAFChecker(VMAFOffValue, VMAFOffValue, VMAFOffValue, VMAFOffValue,
		VMAFOffValue, VMAFOffValue, VMAFOffValue, 80)
	if err != nil {
		t.Fatalf("failed to create auditor: %v", err)
	}

	statsCache := &mockStatsCache{mean: 26, stddev: 13}

	segments := []string{
		filepath.Join(tmpDir, "segment0.mkv"),
		filepath.Join(tmpDir, "segment1.mkv"),
	}
	config := QPSearchConfig{
		SegmentsPaths: segments,
		Auditor:       auditor,
		WorkingDir:    tmpDir,
		StatsCache:    statsCache,
		Encoder:       encoder,
	}

	for _, seg := range segments {
		if err := os.WriteFile(seg, []byte("source"), 0644); err != nil {
			t.Fatalf("failed to create source segment: %v", err)
		}
	}

	results, err := FindAllSegmentsQP(ctx, &mockCallbacks{}, config)
	if err != nil {
		t.Fatalf("FindAllSegmentsQP failed: %v", err)
	}

	if len(results.QPs) != 2 {
		t.Fatalf("expected 2 QPs, got %v", results.QPs)
	}
	if results.QPs[0] != 13 {
		t.Errorf("expected segment 0 QP 13, got %d", results.QPs[0])
	}
	if results.QPs[1] != 20 {
		t.Errorf("expected segment 1 QP 20, got %d", results.QPs[1])
	}
	if results.NbBestEfforts != 0 {
		t.Errorf("expected no best-effort segments, got %d", results.NbBestEfforts)
	}

	// Verify call tracking: each segment should have at least 1 encode and 1 vmaf call.
	if len(encoder.encodeCalls) < 2 {
		t.Errorf("expected at least 2 encode calls, got %d", len(encoder.encodeCalls))
	}
	if len(encoder.vmafCalls) < 2 {
		t.Errorf("expected at least 2 vmaf calls, got %d", len(encoder.vmafCalls))
	}

	// Verify GlobalWeightedQP: both segments have 1000 frames.
	// (13*1000 + 20*1000) / 2000 = 16.5
	expectedWeighted := 16.5
	if results.GlobalWeightedQP != expectedWeighted {
		t.Errorf("expected GlobalWeightedQP %v, got %v", expectedWeighted, results.GlobalWeightedQP)
	}
	if results.TotalSegmentsFrames != 2000 {
		t.Errorf("expected TotalSegmentsFrames 2000, got %d", results.TotalSegmentsFrames)
	}
	// Each segment has 1000 frames, so TotalEncodedFrames should be 1000 * TotalNbAttempts
	expectedTotalEncoded := 1000 * results.TotalNbAttempts
	if results.TotalEncodedFrames != expectedTotalEncoded {
		t.Errorf("expected TotalEncodedFrames %d, got %d", expectedTotalEncoded, results.TotalEncodedFrames)
	}
}

func TestFindAllSegmentsQP_KeepInvalidQP(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	vmafResults := make(map[int]VMAFStats, 52)
	for qp := 0; qp <= 51; qp++ {
		vmafResults[qp] = linearVMAF(qp)
	}

	encoder := &mockEncoder{
		name:        "mock",
		qpMin:       0,
		qpMax:       51,
		vmafResults: vmafResults,
	}

	auditor, err := NewVMAFChecker(VMAFOffValue, VMAFOffValue, VMAFOffValue, VMAFOffValue,
		VMAFOffValue, VMAFOffValue, VMAFOffValue, 80)
	if err != nil {
		t.Fatalf("failed to create auditor: %v", err)
	}

	statsCache := &mockStatsCache{mean: 26, stddev: 13}

	config := QPSearchConfig{
		SegmentsPaths: []string{filepath.Join(tmpDir, "segment.mkv")},
		Auditor:       auditor,
		WorkingDir:    tmpDir,
		StatsCache:    statsCache,
		Encoder:       encoder,
		KeepInvalidQP: false,
	}

	if err := os.WriteFile(config.SegmentsPaths[0], []byte("source"), 0644); err != nil {
		t.Fatalf("failed to create source segment: %v", err)
	}

	results, err := FindAllSegmentsQP(ctx, &mockCallbacks{}, config)
	if err != nil {
		t.Fatalf("FindAllSegmentsQP failed: %v", err)
	}

	// Count how many temporary encoded files exist in working dir
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		t.Fatalf("failed to read dir: %v", err)
	}
	// There should be exactly 1 encoded file (the source segment + the final encoded segment)
	encodedCount := 0
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "seg_") {
			encodedCount++
		}
	}
	if encodedCount != 1 {
		t.Errorf("expected 1 final encoded file with KeepInvalidQP=false, found %d", encodedCount)
	}

	// Now run again with KeepInvalidQP=true
	tmpDir2 := t.TempDir()
	config.WorkingDir = tmpDir2
	config.KeepInvalidQP = true
	config.SegmentsPaths = []string{filepath.Join(tmpDir2, "segment.mkv")}
	if err := os.WriteFile(config.SegmentsPaths[0], []byte("source"), 0644); err != nil {
		t.Fatalf("failed to create source segment: %v", err)
	}

	results, err = FindAllSegmentsQP(ctx, &mockCallbacks{}, config)
	if err != nil {
		t.Fatalf("FindAllSegmentsQP failed: %v", err)
	}

	entries, err = os.ReadDir(tmpDir2)
	if err != nil {
		t.Fatalf("failed to read dir: %v", err)
	}
	encodedCount = 0
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "seg_") {
			encodedCount++
		}
	}
	if encodedCount != results.TotalNbAttempts {
		t.Errorf("expected %d encoded files with KeepInvalidQP=true, found %d", results.TotalNbAttempts, encodedCount)
	}
}

func TestFindAllSegmentsQP_EncodeError(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	encoder := &mockEncoder{
		name:      "mock",
		qpMin:     0,
		qpMax:     51,
		encodeErr: fmt.Errorf("encode exploded"),
	}

	auditor, err := NewVMAFChecker(VMAFOffValue, VMAFOffValue, VMAFOffValue, VMAFOffValue,
		VMAFOffValue, VMAFOffValue, VMAFOffValue, 80)
	if err != nil {
		t.Fatalf("failed to create auditor: %v", err)
	}

	config := QPSearchConfig{
		SegmentsPaths: []string{filepath.Join(tmpDir, "segment.mkv")},
		Auditor:       auditor,
		WorkingDir:    tmpDir,
		StatsCache:    &mockStatsCache{mean: 26, stddev: 13},
		Encoder:       encoder,
	}

	if err := os.WriteFile(config.SegmentsPaths[0], []byte("source"), 0644); err != nil {
		t.Fatalf("failed to create source segment: %v", err)
	}

	_, err = FindAllSegmentsQP(ctx, &mockCallbacks{}, config)
	if err == nil {
		t.Fatal("expected error when Encode fails, got nil")
	}
	if !strings.Contains(err.Error(), "encode exploded") {
		t.Errorf("expected error to contain 'encode exploded', got %v", err)
	}
}

func TestFindAllSegmentsQP_VMAFError(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	encoder := &mockEncoder{
		name:    "mock",
		qpMin:   0,
		qpMax:   51,
		vmafErr: fmt.Errorf("vmaf exploded"),
	}

	auditor, err := NewVMAFChecker(VMAFOffValue, VMAFOffValue, VMAFOffValue, VMAFOffValue,
		VMAFOffValue, VMAFOffValue, VMAFOffValue, 80)
	if err != nil {
		t.Fatalf("failed to create auditor: %v", err)
	}

	config := QPSearchConfig{
		SegmentsPaths: []string{filepath.Join(tmpDir, "segment.mkv")},
		Auditor:       auditor,
		WorkingDir:    tmpDir,
		StatsCache:    &mockStatsCache{mean: 26, stddev: 13},
		Encoder:       encoder,
	}

	if err := os.WriteFile(config.SegmentsPaths[0], []byte("source"), 0644); err != nil {
		t.Fatalf("failed to create source segment: %v", err)
	}

	_, err = FindAllSegmentsQP(ctx, &mockCallbacks{}, config)
	if err == nil {
		t.Fatal("expected error when ComputeVMAF fails, got nil")
	}
	if !strings.Contains(err.Error(), "vmaf exploded") {
		t.Errorf("expected error to contain 'vmaf exploded', got %v", err)
	}
}

func TestFindAllSegmentsQP_ProbeStreamError(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	encoder := &mockEncoder{
		name:     "mock",
		qpMin:    0,
		qpMax:    51,
		probeErr: fmt.Errorf("probe failed"),
	}

	auditor, err := NewVMAFChecker(VMAFOffValue, VMAFOffValue, VMAFOffValue, VMAFOffValue,
		VMAFOffValue, VMAFOffValue, VMAFOffValue, 80)
	if err != nil {
		t.Fatalf("failed to create auditor: %v", err)
	}

	config := QPSearchConfig{
		SegmentsPaths: []string{filepath.Join(tmpDir, "segment.mkv")},
		Auditor:       auditor,
		WorkingDir:    tmpDir,
		StatsCache:    &mockStatsCache{mean: 26, stddev: 13},
		Encoder:       encoder,
	}

	if err := os.WriteFile(config.SegmentsPaths[0], []byte("source"), 0644); err != nil {
		t.Fatalf("failed to create source segment: %v", err)
	}

	_, err = FindAllSegmentsQP(ctx, &mockCallbacks{}, config)
	if err == nil {
		t.Fatal("expected error when ProbeStream fails, got nil")
	}
	if !strings.Contains(err.Error(), "probe failed") {
		t.Errorf("expected error to contain 'probe failed', got %v", err)
	}
}

func TestFindAllSegmentsQP_FinalSegmentFrameMismatch(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	vmafResults := make(map[int]VMAFStats, 52)
	for qp := 0; qp <= 51; qp++ {
		vmafResults[qp] = linearVMAF(qp)
	}

	// First ProbeStream call returns 1000 frames (source).
	// Second ProbeStream call (on final encoded segment) returns 999 frames.
	encoder := &mockEncoder{
		name:        "mock",
		qpMin:       0,
		qpMax:       51,
		vmafResults: vmafResults,
		probeResults: []VideoStream{
			{NbFrames: 1000, NbReadFrames: 1000, RFrameRate: "24/1", Height: 1080, Duration: time.Minute},
			{NbFrames: 999, NbReadFrames: 999, RFrameRate: "24/1", Height: 1080, Duration: time.Minute},
		},
	}

	auditor, err := NewVMAFChecker(VMAFOffValue, VMAFOffValue, VMAFOffValue, VMAFOffValue,
		VMAFOffValue, VMAFOffValue, VMAFOffValue, 80)
	if err != nil {
		t.Fatalf("failed to create auditor: %v", err)
	}

	config := QPSearchConfig{
		SegmentsPaths: []string{filepath.Join(tmpDir, "segment.mkv")},
		Auditor:       auditor,
		WorkingDir:    tmpDir,
		StatsCache:    &mockStatsCache{mean: 26, stddev: 13},
		Encoder:       encoder,
	}

	if err := os.WriteFile(config.SegmentsPaths[0], []byte("source"), 0644); err != nil {
		t.Fatalf("failed to create source segment: %v", err)
	}

	_, err = FindAllSegmentsQP(ctx, &mockCallbacks{}, config)
	if err == nil {
		t.Fatal("expected error when final segment frame count mismatches, got nil")
	}
	if !strings.Contains(err.Error(), "999 frames instead of 1000") {
		t.Errorf("expected frame mismatch error, got %v", err)
	}
}

func TestFindAllSegmentsQP_BestEffortMultipleSegments(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	// Segment 0: can not meet threshold even at QP 0 (best effort)
	// Segment 1: converges at QP 13
	vmafBySegment := map[int]map[int]VMAFStats{
		0: {},
		1: {},
	}
	for qp := 0; qp <= 51; qp++ {
		vmafBySegment[0][qp] = linearVMAF(qp + 20) // max mean = 70
		vmafBySegment[1][qp] = linearVMAF(qp)
	}

	encoder := &mockEncoder{
		name:          "mock",
		qpMin:         0,
		qpMax:         51,
		vmafBySegment: vmafBySegment,
	}

	auditor, err := NewVMAFChecker(VMAFOffValue, VMAFOffValue, VMAFOffValue, VMAFOffValue,
		VMAFOffValue, VMAFOffValue, VMAFOffValue, 80)
	if err != nil {
		t.Fatalf("failed to create auditor: %v", err)
	}

	segments := []string{
		filepath.Join(tmpDir, "segment0.mkv"),
		filepath.Join(tmpDir, "segment1.mkv"),
	}
	config := QPSearchConfig{
		SegmentsPaths: segments,
		Auditor:       auditor,
		WorkingDir:    tmpDir,
		StatsCache:    &mockStatsCache{mean: 26, stddev: 13},
		Encoder:       encoder,
	}

	for _, seg := range segments {
		if err := os.WriteFile(seg, []byte("source"), 0644); err != nil {
			t.Fatalf("failed to create source segment: %v", err)
		}
	}

	results, err := FindAllSegmentsQP(ctx, &mockCallbacks{}, config)
	if err != nil {
		t.Fatalf("FindAllSegmentsQP failed: %v", err)
	}

	if results.NbBestEfforts != 1 {
		t.Errorf("expected 1 best-effort segment, got %d", results.NbBestEfforts)
	}
	if len(results.QPs) != 2 {
		t.Fatalf("expected 2 QPs, got %v", results.QPs)
	}
	if results.QPs[0] != 0 {
		t.Errorf("expected segment 0 QP 0 (best effort), got %d", results.QPs[0])
	}
	if results.QPs[1] != 13 {
		t.Errorf("expected segment 1 QP 13, got %d", results.QPs[1])
	}
}

func TestFindAllSegmentsQP_MockEncoderTracksCalls(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	vmafResults := make(map[int]VMAFStats, 52)
	for qp := 0; qp <= 51; qp++ {
		vmafResults[qp] = linearVMAF(qp)
	}

	encoder := &mockEncoder{
		name:        "mock",
		qpMin:       0,
		qpMax:       51,
		vmafResults: vmafResults,
	}

	auditor, err := NewVMAFChecker(VMAFOffValue, VMAFOffValue, VMAFOffValue, VMAFOffValue,
		VMAFOffValue, VMAFOffValue, VMAFOffValue, 80)
	if err != nil {
		t.Fatalf("failed to create auditor: %v", err)
	}

	config := QPSearchConfig{
		SegmentsPaths: []string{filepath.Join(tmpDir, "segment.mkv")},
		Auditor:       auditor,
		WorkingDir:    tmpDir,
		StatsCache:    &mockStatsCache{mean: 26, stddev: 13},
		Encoder:       encoder,
	}

	if err := os.WriteFile(config.SegmentsPaths[0], []byte("source"), 0644); err != nil {
		t.Fatalf("failed to create source segment: %v", err)
	}

	_, err = FindAllSegmentsQP(ctx, &mockCallbacks{}, config)
	if err != nil {
		t.Fatalf("FindAllSegmentsQP failed: %v", err)
	}

	if len(encoder.encodeCalls) == 0 {
		t.Error("expected at least one encode call")
	}
	if len(encoder.vmafCalls) == 0 {
		t.Error("expected at least one vmaf call")
	}
	if len(encoder.encodeCalls) != len(encoder.vmafCalls) {
		t.Errorf("expected equal encode and vmaf calls, got %d encode and %d vmaf",
			len(encoder.encodeCalls), len(encoder.vmafCalls))
	}
	// Each encode call should match the QP extracted from the output path
	for _, call := range encoder.encodeCalls {
		_, qp, err := extractSegmentAndQP(call.output)
		if err != nil {
			t.Errorf("failed to extract QP from output %s: %v", call.output, err)
			continue
		}
		if call.qp != qp {
			t.Errorf("encode call QP %d does not match output path QP %d", call.qp, qp)
		}
	}
}

func TestFindAllSegmentsQP_NilEncoder(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	auditor, err := NewVMAFChecker(VMAFOffValue, VMAFOffValue, VMAFOffValue, VMAFOffValue,
		VMAFOffValue, VMAFOffValue, VMAFOffValue, 80)
	if err != nil {
		t.Fatalf("failed to create auditor: %v", err)
	}

	config := QPSearchConfig{
		SegmentsPaths: []string{filepath.Join(tmpDir, "segment.mkv")},
		Auditor:       auditor,
		WorkingDir:    tmpDir,
		StatsCache:    &mockStatsCache{mean: 26, stddev: 13},
		Encoder:       nil,
	}

	if err := os.WriteFile(config.SegmentsPaths[0], []byte("source"), 0644); err != nil {
		t.Fatalf("failed to create source segment: %v", err)
	}

	_, err = FindAllSegmentsQP(ctx, &mockCallbacks{}, config)
	if err == nil {
		t.Fatal("expected error with nil Encoder, but got nil")
	}
}

func TestFindAllSegmentsQP_NilCallbacks(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	vmafResults := make(map[int]VMAFStats, 52)
	for qp := 0; qp <= 51; qp++ {
		vmafResults[qp] = linearVMAF(qp)
	}

	encoder := &mockEncoder{
		name:        "mock",
		qpMin:       0,
		qpMax:       51,
		vmafResults: vmafResults,
	}

	auditor, err := NewVMAFChecker(VMAFOffValue, VMAFOffValue, VMAFOffValue, VMAFOffValue,
		VMAFOffValue, VMAFOffValue, VMAFOffValue, 80)
	if err != nil {
		t.Fatalf("failed to create auditor: %v", err)
	}

	config := QPSearchConfig{
		SegmentsPaths: []string{filepath.Join(tmpDir, "segment.mkv")},
		Auditor:       auditor,
		WorkingDir:    tmpDir,
		StatsCache:    &mockStatsCache{mean: 26, stddev: 13},
		Encoder:       encoder,
	}

	if err := os.WriteFile(config.SegmentsPaths[0], []byte("source"), 0644); err != nil {
		t.Fatalf("failed to create source segment: %v", err)
	}

	_, err = FindAllSegmentsQP(ctx, nil, config)
	if err == nil {
		t.Fatal("expected error with nil callbacks, but got nil")
	}
}

func TestQPSearchResults_GetMinMaxQPs(t *testing.T) {
	tests := []struct {
		name    string
		qps     []int
		wantMin int
		wantMax int
	}{
		{"empty", []int{}, -1, -1},
		{"single", []int{42}, 42, 42},
		{"two", []int{10, 20}, 10, 20},
		{"unordered", []int{30, 10, 20}, 10, 30},
		{"all same", []int{5, 5, 5}, 5, 5},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			results := QPSearchResults{QPs: tt.qps}
			min, max := results.GetMinMaxQPs()
			if min != tt.wantMin {
				t.Errorf("GetMinMaxQPs() min = %d, want %d", min, tt.wantMin)
			}
			if max != tt.wantMax {
				t.Errorf("GetMinMaxQPs() max = %d, want %d", max, tt.wantMax)
			}
		})
	}
}

func TestQPSearchResults_GetMeanStdDev(t *testing.T) {
	tests := []struct {
		name       string
		qps        []int
		wantMean   float64
		wantStddev float64
	}{
		{"empty", []int{}, 0, 0},
		{"single", []int{42}, 42, 0},
		{"two", []int{10, 20}, 15, 7.0710678118654755},
		{"three", []int{10, 20, 30}, 20, 10},
		{"all same", []int{5, 5, 5}, 5, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			results := QPSearchResults{QPs: tt.qps}
			mean, stddev := results.GetMeanStdDev()
			if mean != tt.wantMean {
				t.Errorf("GetMeanStdDev() mean = %v, want %v", mean, tt.wantMean)
			}
			if stddev != tt.wantStddev {
				t.Errorf("GetMeanStdDev() stddev = %v, want %v", stddev, tt.wantStddev)
			}
		})
	}
}

func TestFindAllSegmentsQP_CacheMeanOutOfRange(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	vmafResults := make(map[int]VMAFStats, 52)
	for qp := 0; qp <= 51; qp++ {
		vmafResults[qp] = linearVMAF(qp)
	}

	encoder := &mockEncoder{
		name:        "mock",
		qpMin:       0,
		qpMax:       51,
		vmafResults: vmafResults,
		validateQP:  true,
	}

	auditor, err := NewVMAFChecker(VMAFOffValue, VMAFOffValue, VMAFOffValue, VMAFOffValue,
		VMAFOffValue, VMAFOffValue, VMAFOffValue, 80)
	if err != nil {
		t.Fatalf("failed to create auditor: %v", err)
	}

	// Cache mean is outside the valid QP range [0, 51]
	config := QPSearchConfig{
		SegmentsPaths: []string{filepath.Join(tmpDir, "segment.mkv")},
		Auditor:       auditor,
		WorkingDir:    tmpDir,
		StatsCache:    &mockStatsCache{mean: 100, stddev: 10},
		Encoder:       encoder,
	}

	if err := os.WriteFile(config.SegmentsPaths[0], []byte("source"), 0644); err != nil {
		t.Fatalf("failed to create source segment: %v", err)
	}

	_, err = FindAllSegmentsQP(ctx, &mockCallbacks{}, config)
	if err == nil {
		t.Fatal("expected error when initial cache mean is outside valid QP range, got nil")
	}
	if !strings.Contains(err.Error(), "out of range") {
		t.Errorf("expected error to mention out of range, got %v", err)
	}
}

func TestFindAllSegmentsQP_TotalEncodedFrames(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	vmafResults := make(map[int]VMAFStats, 52)
	for qp := 0; qp <= 51; qp++ {
		vmafResults[qp] = linearVMAF(qp)
	}

	encoder := &mockEncoder{
		name:        "mock",
		qpMin:       0,
		qpMax:       51,
		vmafResults: vmafResults,
	}

	auditor, err := NewVMAFChecker(VMAFOffValue, VMAFOffValue, VMAFOffValue, VMAFOffValue,
		VMAFOffValue, VMAFOffValue, VMAFOffValue, 80)
	if err != nil {
		t.Fatalf("failed to create auditor: %v", err)
	}

	config := QPSearchConfig{
		SegmentsPaths: []string{filepath.Join(tmpDir, "segment.mkv")},
		Auditor:       auditor,
		WorkingDir:    tmpDir,
		StatsCache:    &mockStatsCache{mean: 26, stddev: 13},
		Encoder:       encoder,
	}

	if err := os.WriteFile(config.SegmentsPaths[0], []byte("source"), 0644); err != nil {
		t.Fatalf("failed to create source segment: %v", err)
	}

	results, err := FindAllSegmentsQP(ctx, &mockCallbacks{}, config)
	if err != nil {
		t.Fatalf("FindAllSegmentsQP failed: %v", err)
	}

	// TotalEncodedFrames should equal segmentFrames * totalAttempts
	// segmentFrames = 1000 (from ProbeStream)
	expectedTotalEncoded := 1000 * results.TotalNbAttempts
	if results.TotalEncodedFrames != expectedTotalEncoded {
		t.Errorf("expected TotalEncodedFrames %d, got %d", expectedTotalEncoded, results.TotalEncodedFrames)
	}
}

// TestFindAllSegmentsQP_QPMaxIsOptimal verifies the path where the optimal QP
// is the encoder maximum and interpolation returns it directly, hitting the
// candidateQP == qpMax fast-return in searchSegmentQP.
func TestFindAllSegmentsQP_QPMaxIsOptimal(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	// With threshold 10, every QP is valid. The optimal QP is qpMax=51.
	vmafResults := make(map[int]VMAFStats, 52)
	for qp := 0; qp <= 51; qp++ {
		vmafResults[qp] = linearVMAF(qp)
	}

	encoder := &mockEncoder{
		name:        "mock",
		qpMin:       0,
		qpMax:       51,
		vmafResults: vmafResults,
	}

	auditor, err := NewVMAFChecker(VMAFOffValue, VMAFOffValue, VMAFOffValue, VMAFOffValue,
		VMAFOffValue, VMAFOffValue, VMAFOffValue, 10)
	if err != nil {
		t.Fatalf("failed to create auditor: %v", err)
	}

	config := QPSearchConfig{
		SegmentsPaths: []string{filepath.Join(tmpDir, "segment.mkv")},
		Auditor:       auditor,
		WorkingDir:    tmpDir,
		StatsCache:    &mockStatsCache{mean: 26, stddev: 13},
		Encoder:       encoder,
	}

	if err := os.WriteFile(config.SegmentsPaths[0], []byte("source"), 0644); err != nil {
		t.Fatalf("failed to create source segment: %v", err)
	}

	results, err := FindAllSegmentsQP(ctx, &mockCallbacks{}, config)
	if err != nil {
		t.Fatalf("FindAllSegmentsQP failed: %v", err)
	}

	if len(results.QPs) != 1 || results.QPs[0] != 51 {
		t.Errorf("expected QP 51, got %v", results.QPs)
	}
	if results.TotalNbAttempts != 3 {
		// Expected: 26 (start), 39 (step up), 51 (clamped step up), then immediate return.
		t.Errorf("expected 3 attempts for qpMax-optimal curve, got %d", results.TotalNbAttempts)
	}

	expectedQPs := []int{26, 39, 51}
	for i, call := range encoder.encodeCalls {
		if i >= len(expectedQPs) {
			break
		}
		if call.qp != expectedQPs[i] {
			t.Errorf("encode call %d: expected QP %d, got %d", i, expectedQPs[i], call.qp)
		}
	}
}

func TestFindAllSegmentsQP_MeanAtQPMinValid(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	vmafResults := make(map[int]VMAFStats, 52)
	for qp := 0; qp <= 51; qp++ {
		vmafResults[qp] = linearVMAF(qp)
	}

	encoder := &mockEncoder{
		name:        "mock",
		qpMin:       0,
		qpMax:       51,
		vmafResults: vmafResults,
	}

	// Threshold 90: QPs 0-6 are valid, optimal is 6.
	// With mean=0, the first test at QP 0 is valid. The old code would hit the
	// fast-return at lines 290-293 and return QP 0 as best-effort. The fix
	// jumps to qpMax to close the bracket, then interpolation converges to QP 6.
	auditor, err := NewVMAFChecker(VMAFOffValue, VMAFOffValue, VMAFOffValue, VMAFOffValue,
		VMAFOffValue, VMAFOffValue, VMAFOffValue, 90)
	if err != nil {
		t.Fatalf("failed to create auditor: %v", err)
	}

	config := QPSearchConfig{
		SegmentsPaths: []string{filepath.Join(tmpDir, "segment.mkv")},
		Auditor:       auditor,
		WorkingDir:    tmpDir,
		StatsCache:    &mockStatsCache{mean: 0, stddev: 1},
		Encoder:       encoder,
	}

	if err := os.WriteFile(config.SegmentsPaths[0], []byte("source"), 0644); err != nil {
		t.Fatalf("failed to create source segment: %v", err)
	}

	results, err := FindAllSegmentsQP(ctx, &mockCallbacks{}, config)
	if err != nil {
		t.Fatalf("FindAllSegmentsQP failed: %v", err)
	}

	if len(results.QPs) != 1 || results.QPs[0] != 6 {
		t.Errorf("expected optimal QP 6, got %v", results.QPs)
	}
	if results.NbBestEfforts != 0 {
		t.Errorf("expected 0 best-efforts (optimal QP found), got %d", results.NbBestEfforts)
	}
}

func TestFindAllSegmentsQP_ZeroStddev(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	vmafResults := make(map[int]VMAFStats, 52)
	for qp := 0; qp <= 51; qp++ {
		vmafResults[qp] = linearVMAF(qp)
	}

	encoder := &mockEncoder{
		name:        "mock",
		qpMin:       0,
		qpMax:       51,
		vmafResults: vmafResults,
	}

	auditor, err := NewVMAFChecker(VMAFOffValue, VMAFOffValue, VMAFOffValue, VMAFOffValue,
		VMAFOffValue, VMAFOffValue, VMAFOffValue, 80)
	if err != nil {
		t.Fatalf("failed to create auditor: %v", err)
	}

	// stddev=0 would cause Phase 2 bracketing to retest the same QP forever.
	// The guard in searchSegmentQP forces stddev to 1, making the search
	// step by integer increments until the bracket is closed.
	config := QPSearchConfig{
		SegmentsPaths: []string{filepath.Join(tmpDir, "segment.mkv")},
		Auditor:       auditor,
		WorkingDir:    tmpDir,
		StatsCache:    &mockStatsCache{mean: 26, stddev: 0},
		Encoder:       encoder,
	}

	if err := os.WriteFile(config.SegmentsPaths[0], []byte("source"), 0644); err != nil {
		t.Fatalf("failed to create source segment: %v", err)
	}

	results, err := FindAllSegmentsQP(ctx, &mockCallbacks{}, config)
	if err != nil {
		t.Fatalf("FindAllSegmentsQP failed: %v", err)
	}

	if len(results.QPs) != 1 || results.QPs[0] != 13 {
		t.Errorf("expected QP 13, got %v", results.QPs)
	}
	// With mean=26 and forced stddev=1, the search steps down one by one
	// from 26 to 13, then interpolation converges. Expect ~14 attempts.
	if results.TotalNbAttempts >= 20 {
		t.Errorf("expected reasonable convergence (<20 attempts) with forced stddev=1, got %d", results.TotalNbAttempts)
	}
	if results.NbBestEfforts != 0 {
		t.Errorf("expected no best-effort segments, got %d", results.NbBestEfforts)
	}
}

func TestFindAllSegmentsQP_ZeroFrames(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	encoder := &mockEncoder{
		name:  "mock",
		qpMin: 0,
		qpMax: 51,
		probeResults: []VideoStream{
			{NbFrames: 0, NbReadFrames: 0, RFrameRate: "24/1", Height: 1080, Duration: time.Minute},
		},
	}

	auditor, err := NewVMAFChecker(VMAFOffValue, VMAFOffValue, VMAFOffValue, VMAFOffValue,
		VMAFOffValue, VMAFOffValue, VMAFOffValue, 80)
	if err != nil {
		t.Fatalf("failed to create auditor: %v", err)
	}

	config := QPSearchConfig{
		SegmentsPaths: []string{filepath.Join(tmpDir, "segment.mkv")},
		Auditor:       auditor,
		WorkingDir:    tmpDir,
		StatsCache:    &mockStatsCache{mean: 26, stddev: 13},
		Encoder:       encoder,
	}

	if err := os.WriteFile(config.SegmentsPaths[0], []byte("source"), 0644); err != nil {
		t.Fatalf("failed to create source segment: %v", err)
	}

	_, err = FindAllSegmentsQP(ctx, &mockCallbacks{}, config)
	if err == nil {
		t.Fatal("expected error when segment has zero frames, got nil")
	}
	if !strings.Contains(err.Error(), "frame count is 0 or negative") {
		t.Errorf("expected frame count error, got %v", err)
	}
}

func TestFindAllSegmentsQP_StatError(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	encoder := &mockEncoder{
		name:  "mock",
		qpMin: 0,
		qpMax: 51,
	}

	auditor, err := NewVMAFChecker(VMAFOffValue, VMAFOffValue, VMAFOffValue, VMAFOffValue,
		VMAFOffValue, VMAFOffValue, VMAFOffValue, 80)
	if err != nil {
		t.Fatalf("failed to create auditor: %v", err)
	}

	// Use a non-existent path so os.Stat fails inside getStreamsInfosCF.
	config := QPSearchConfig{
		SegmentsPaths: []string{filepath.Join(tmpDir, "does_not_exist.mkv")},
		Auditor:       auditor,
		WorkingDir:    tmpDir,
		StatsCache:    &mockStatsCache{mean: 26, stddev: 13},
		Encoder:       encoder,
	}

	_, err = FindAllSegmentsQP(ctx, &mockCallbacks{}, config)
	if err == nil {
		t.Fatal("expected error when segment file does not exist, got nil")
	}
	if !strings.Contains(err.Error(), "failed to stat") {
		t.Errorf("expected stat error, got %v", err)
	}
}

func TestFindAllSegmentsQP_EphemeralConvergence(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	vmafResults := make(map[int]VMAFStats, 52)
	for qp := 0; qp <= 51; qp++ {
		vmafResults[qp] = linearVMAF(qp)
	}

	encoder := &mockEncoder{
		name:        "mock",
		qpMin:       0,
		qpMax:       51,
		vmafResults: vmafResults,
	}

	auditor, err := NewVMAFChecker(VMAFOffValue, VMAFOffValue, VMAFOffValue, VMAFOffValue,
		VMAFOffValue, VMAFOffValue, VMAFOffValue, 70)
	if err != nil {
		t.Fatalf("failed to create auditor: %v", err)
	}

	segments := []string{
		filepath.Join(tmpDir, "seg_000000.mkv"),
		filepath.Join(tmpDir, "seg_000001.mkv"),
		filepath.Join(tmpDir, "seg_000002.mkv"),
	}
	for _, seg := range segments {
		if err := os.WriteFile(seg, []byte("dummy"), 0644); err != nil {
			t.Fatalf("failed to create segment file: %v", err)
		}
	}

	// Empty cache fallback: mean=26, stddev=13
	statsCache := &mockStatsCache{mean: 26, stddev: 13}

	config := QPSearchConfig{
		SegmentsPaths: segments,
		Auditor:       auditor,
		WorkingDir:    tmpDir,
		StatsCache:    statsCache,
		Encoder:       encoder,
	}

	results, err := FindAllSegmentsQP(ctx, &mockCallbacks{}, config)
	if err != nil {
		t.Fatalf("FindAllSegmentsQP failed: %v", err)
	}

	if len(results.QPs) != 3 {
		t.Fatalf("expected 3 QPs, got %d", len(results.QPs))
	}
	for i, qp := range results.QPs {
		if qp != 20 {
			t.Errorf("expected segment %d at QP 20, got %d", i, qp)
		}
	}

	// Count attempts per segment.
	segAttempts := make([]int, 3)
	for _, call := range encoder.encodeCalls {
		seg, _, _ := extractSegmentAndQP(call.output)
		if seg >= 0 && seg < 3 {
			segAttempts[seg]++
		}
	}

	// The ephemeral cache should help later segments converge in fewer or equal attempts.
	// Segment 2 should not need more attempts than segment 0.
	if segAttempts[2] > segAttempts[0] {
		t.Errorf("expected segment 2 to have <= attempts than segment 0 due to ephemeral cache, got %d vs %d", segAttempts[2], segAttempts[0])
	}
}

func TestFindAllSegmentsQP_EphemeralWithSeededBase(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	vmafResults := make(map[int]VMAFStats, 52)
	for qp := 0; qp <= 51; qp++ {
		vmafResults[qp] = linearVMAF(qp)
	}

	encoder := &mockEncoder{
		name:        "mock",
		qpMin:       0,
		qpMax:       51,
		vmafResults: vmafResults,
	}

	auditor, err := NewVMAFChecker(VMAFOffValue, VMAFOffValue, VMAFOffValue, VMAFOffValue,
		VMAFOffValue, VMAFOffValue, VMAFOffValue, 70)
	if err != nil {
		t.Fatalf("failed to create auditor: %v", err)
	}

	segments := []string{
		filepath.Join(tmpDir, "seg_000000.mkv"),
		filepath.Join(tmpDir, "seg_000001.mkv"),
	}
	for _, seg := range segments {
		if err := os.WriteFile(seg, []byte("dummy"), 0644); err != nil {
			t.Fatalf("failed to create segment file: %v", err)
		}
	}

	// Persistent cache seeded low; segments converge at QP 20, pulling mean up.
	statsCache := &mockStatsCache{mean: 10, stddev: 2}

	config := QPSearchConfig{
		SegmentsPaths: segments,
		Auditor:       auditor,
		WorkingDir:    tmpDir,
		StatsCache:    statsCache,
		Encoder:       encoder,
	}

	results, err := FindAllSegmentsQP(ctx, &mockCallbacks{}, config)
	if err != nil {
		t.Fatalf("FindAllSegmentsQP failed: %v", err)
	}

	if len(results.QPs) != 2 {
		t.Fatalf("expected 2 QPs, got %d", len(results.QPs))
	}
	if results.QPs[0] != 20 || results.QPs[1] != 20 {
		t.Errorf("expected both QPs=20, got %v", results.QPs)
	}

	// Segment 1 should need fewer attempts because ephemeral cache shifted mean toward 20.
	seg0Attempts := 0
	seg1Attempts := 0
	for _, call := range encoder.encodeCalls {
		seg, _, _ := extractSegmentAndQP(call.output)
		if seg == 0 {
			seg0Attempts++
		} else if seg == 1 {
			seg1Attempts++
		}
	}
	if seg1Attempts >= seg0Attempts {
		t.Errorf("expected segment 1 to have fewer attempts than segment 0, got %d vs %d", seg1Attempts, seg0Attempts)
	}
}

func TestFindAllSegmentsQP_EphemeralBestEffort(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	// Even at QP 0 the mean is only 70, which matches threshold 70 exactly.
	// linearVMAF(qp+30): QP 0 → mean 70, QP 1 → mean 68.5, etc.
	vmafResults := make(map[int]VMAFStats, 52)
	for qp := 0; qp <= 51; qp++ {
		vmafResults[qp] = linearVMAF(qp + 30)
	}

	encoder := &mockEncoder{
		name:        "mock",
		qpMin:       0,
		qpMax:       51,
		vmafResults: vmafResults,
	}

	auditor, err := NewVMAFChecker(VMAFOffValue, VMAFOffValue, VMAFOffValue, VMAFOffValue,
		VMAFOffValue, VMAFOffValue, VMAFOffValue, 70)
	if err != nil {
		t.Fatalf("failed to create auditor: %v", err)
	}

	segments := []string{
		filepath.Join(tmpDir, "seg_000000.mkv"),
		filepath.Join(tmpDir, "seg_000001.mkv"),
	}
	for _, seg := range segments {
		if err := os.WriteFile(seg, []byte("dummy"), 0644); err != nil {
			t.Fatalf("failed to create segment file: %v", err)
		}
	}

	statsCache := &mockStatsCache{mean: 26, stddev: 13}

	config := QPSearchConfig{
		SegmentsPaths: segments,
		Auditor:       auditor,
		WorkingDir:    tmpDir,
		StatsCache:    statsCache,
		Encoder:       encoder,
	}

	results, err := FindAllSegmentsQP(ctx, &mockCallbacks{}, config)
	if err != nil {
		t.Fatalf("FindAllSegmentsQP failed: %v", err)
	}

	if results.NbBestEfforts != 2 {
		t.Errorf("expected 2 best-effort segments, got %d", results.NbBestEfforts)
	}
	if results.QPs[0] != 0 || results.QPs[1] != 0 {
		t.Errorf("expected both QPs=0 (best effort), got %v", results.QPs)
	}

	// Ephemeral cache should have learned that QP 0 is the norm.
	seg0Attempts := 0
	seg1Attempts := 0
	for _, call := range encoder.encodeCalls {
		seg, _, _ := extractSegmentAndQP(call.output)
		if seg == 0 {
			seg0Attempts++
		} else if seg == 1 {
			seg1Attempts++
		}
	}
	// Segment 1 should start lower (closer to 0) and converge faster.
	if seg1Attempts >= seg0Attempts {
		t.Errorf("expected segment 1 to have fewer attempts than segment 0, got %d vs %d", seg1Attempts, seg0Attempts)
	}
}

// TestFindAllSegmentsQP_MeanAtQPMinInvalid covers the case where the very first
// candidate is qpMin and it does not validate: there is nothing lower to try, so
// the search must end in best effort with a single attempt instead of erroring.
func TestFindAllSegmentsQP_MeanAtQPMinInvalid(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	vmafResults := make(map[int]VMAFStats, 52)
	for qp := 0; qp <= 51; qp++ {
		vmafResults[qp] = linearVMAF(qp)
	}

	encoder := &mockEncoder{
		name:        "mock",
		qpMin:       0,
		qpMax:       51,
		vmafResults: vmafResults,
		validateQP:  true,
	}

	// Even the highest quality (QP 0) stays below the mean threshold of 100.
	vmafResults[0] = linearVMAF(1)
	auditor, err := NewVMAFChecker(VMAFOffValue, VMAFOffValue, VMAFOffValue, VMAFOffValue,
		VMAFOffValue, VMAFOffValue, VMAFOffValue, 100)
	if err != nil {
		t.Fatalf("failed to create auditor: %v", err)
	}

	config := QPSearchConfig{
		SegmentsPaths: []string{filepath.Join(tmpDir, "segment.mkv")},
		Auditor:       auditor,
		WorkingDir:    tmpDir,
		StatsCache:    &mockStatsCache{mean: 0, stddev: 1},
		Encoder:       encoder,
	}

	if err := os.WriteFile(config.SegmentsPaths[0], []byte("source"), 0644); err != nil {
		t.Fatalf("failed to create source segment: %v", err)
	}

	results, err := FindAllSegmentsQP(ctx, &mockCallbacks{}, config)
	if err != nil {
		t.Fatalf("FindAllSegmentsQP failed: %v", err)
	}

	if len(results.QPs) != 1 || results.QPs[0] != 0 {
		t.Errorf("expected best effort QP 0, got %v", results.QPs)
	}
	if results.NbBestEfforts != 1 {
		t.Errorf("expected 1 best-effort, got %d", results.NbBestEfforts)
	}
	if results.TotalNbAttempts != 1 {
		t.Errorf("expected a single attempt, got %d", results.TotalNbAttempts)
	}
}

// TestFindAllSegmentsQP_MeanAtQPMaxValid covers the case where the very first
// candidate is qpMax and it validates: there is nothing higher to try, so the
// search must end right away with qpMax instead of erroring.
func TestFindAllSegmentsQP_MeanAtQPMaxValid(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	vmafResults := make(map[int]VMAFStats, 52)
	for qp := 0; qp <= 51; qp++ {
		vmafResults[qp] = linearVMAF(qp)
	}

	encoder := &mockEncoder{
		name:        "mock",
		qpMin:       0,
		qpMax:       51,
		vmafResults: vmafResults,
		validateQP:  true,
	}

	// With threshold 10, every QP is valid. The optimal QP is qpMax=51.
	auditor, err := NewVMAFChecker(VMAFOffValue, VMAFOffValue, VMAFOffValue, VMAFOffValue,
		VMAFOffValue, VMAFOffValue, VMAFOffValue, 10)
	if err != nil {
		t.Fatalf("failed to create auditor: %v", err)
	}

	config := QPSearchConfig{
		SegmentsPaths: []string{filepath.Join(tmpDir, "segment.mkv")},
		Auditor:       auditor,
		WorkingDir:    tmpDir,
		StatsCache:    &mockStatsCache{mean: 51, stddev: 1},
		Encoder:       encoder,
	}

	if err := os.WriteFile(config.SegmentsPaths[0], []byte("source"), 0644); err != nil {
		t.Fatalf("failed to create source segment: %v", err)
	}

	results, err := FindAllSegmentsQP(ctx, &mockCallbacks{}, config)
	if err != nil {
		t.Fatalf("FindAllSegmentsQP failed: %v", err)
	}

	if len(results.QPs) != 1 || results.QPs[0] != 51 {
		t.Errorf("expected QP 51, got %v", results.QPs)
	}
	if results.NbBestEfforts != 0 {
		t.Errorf("expected 0 best-efforts, got %d", results.NbBestEfforts)
	}
	if results.TotalNbAttempts != 1 {
		t.Errorf("expected a single attempt, got %d", results.TotalNbAttempts)
	}
}
