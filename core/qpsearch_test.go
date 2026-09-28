package core

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// mockEncoder is a test double that returns pre-computed VMAF results without calling ffmpeg.
// It is safe for concurrent use: FindAllSegmentsQP calls it from several workers at once.
type mockEncoder struct {
	mu            sync.Mutex
	encodeDelay   time.Duration // makes encodes of different workers overlap
	activeEncodes int
	maxActive     int // highest number of encodes seen running at the same time
	name          string
	qpMin         int
	qpMax         int
	vmafResults   map[int]VMAFStats
	vmafBySegment map[int]map[int]VMAFStats
	// banding added by the encode of each QP (see bandingOf), for the passes measuring it
	bandingResults   map[int]BandingStats
	bandingBySegment map[int]map[int]BandingStats
	encodeCalls      []mockEncodeCall
	vmafCalls        []mockVMAFCall
	encodeErr        error
	vmafErr          error
	probeErr         error
	probeErrors      []error
	probeResults     []VideoStream
	probeCallIdx     int
	countErr         error
	counts           map[string]int // what CountFrames returns for each file: recorded by ProbeStream for the sources, by Encode for the encodes
	// encodeFrames makes encodes short: the frames of the successive encodes of a file (by name),
	// before they hold the stream's (see Encode)
	encodeFrames map[string][]int
	probePaths   []string // the files ProbeStream was called on, in order
	countPaths   []string // the files CountFrames was called on, in order
	frameRate    string   // declared by the default probe result (24/1 when empty)
	validateQP   bool
}

type mockEncodeCall struct {
	input  string
	output string
	qp     int
}

type mockVMAFCall struct {
	reference string
	distorted string
	measures  VMAFMeasures
}

func (m *mockEncoder) Name() string { return m.name }

func (m *mockEncoder) QPRange() (min, max int, found bool) {
	return m.qpMin, m.qpMax, true
}

func (m *mockEncoder) Encode(_ context.Context, input, output string, qp int, stream VideoStream,
	_ func(ProgressStats), _ func(string), _ func(error)) error {
	if m.encodeErr != nil {
		return m.encodeErr
	}
	if m.validateQP && (qp < m.qpMin || qp > m.qpMax) {
		return fmt.Errorf("QP %d out of range [%d, %d]", qp, m.qpMin, m.qpMax)
	}
	m.mu.Lock()
	m.activeEncodes++
	m.maxActive = max(m.maxActive, m.activeEncodes)
	m.mu.Unlock()
	time.Sleep(m.encodeDelay)
	m.mu.Lock()
	m.activeEncodes--
	m.encodeCalls = append(m.encodeCalls, mockEncodeCall{input, output, qp})
	// What CountFrames will find in the encode: the frames of the stream it was given, unless the
	// test made it short
	if m.counts == nil {
		m.counts = make(map[string]int)
	}
	frames := stream.NbReadFrames
	if short := m.encodeFrames[filepath.Base(output)]; len(short) > 0 {
		frames, m.encodeFrames[filepath.Base(output)] = short[0], short[1:]
	}
	m.counts[output] = frames
	m.mu.Unlock()
	// Create a dummy file so getFileSize succeeds for the selected QP.
	return os.WriteFile(output, []byte("dummy"), 0644)
}

func (m *mockEncoder) ComputeVMAF(_ context.Context, reference, distorted string, _ VideoStream, measures VMAFMeasures,
	_ func(ProgressStats), _ func(string), _ func(error)) (stats VMAFStats, banding BandingStats, err error) {
	if m.vmafErr != nil {
		return VMAFStats{}, BandingStats{}, m.vmafErr
	}
	m.mu.Lock()
	m.vmafCalls = append(m.vmafCalls, mockVMAFCall{reference, distorted, measures})
	m.mu.Unlock()
	if !measures.Score && !measures.Banding {
		return VMAFStats{}, BandingStats{}, fmt.Errorf("nothing to measure")
	}
	seg, qp, err := extractSegmentAndQP(distorted)
	if err != nil {
		return VMAFStats{}, BandingStats{}, err
	}
	// the file must have been encoded: a banding pass reuses the one of the VMAF search
	if _, err = os.Stat(distorted); err != nil {
		return VMAFStats{}, BandingStats{}, fmt.Errorf("no encode to measure: %w", err)
	}
	if measures.Score {
		if stats, err = m.vmafOf(seg, qp); err != nil {
			return
		}
	}
	if measures.Banding {
		banding, err = m.bandingOf(seg, qp)
	}
	return
}

func (m *mockEncoder) vmafOf(seg, qp int) (VMAFStats, error) {
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

func (m *mockEncoder) bandingOf(seg, qp int) (BandingStats, error) {
	if m.bandingBySegment != nil {
		if segMap, ok := m.bandingBySegment[seg]; ok {
			if banding, ok := segMap[qp]; ok {
				return banding, nil
			}
			return BandingStats{}, fmt.Errorf("no mock banding for segment %d QP %d", seg, qp)
		}
		return BandingStats{}, fmt.Errorf("no mock banding for segment %d", seg)
	}
	banding, ok := m.bandingResults[qp]
	if !ok {
		return BandingStats{}, fmt.Errorf("no mock banding for QP %d", qp)
	}
	return banding, nil
}

// ProbeStream returns the next probeResults entry (or the default 1000 frames stream). Its
// NbReadFrames is what the mock will count for that file (CountFrames), it is not returned as
// such: the real ProbeStream does not decode anything.
func (m *mockEncoder) ProbeStream(_ context.Context, path string, _ func(string), _ func(error)) (VideoStream, error) {
	if m.probeErr != nil {
		return VideoStream{}, m.probeErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	res := VideoStream{NbFrames: 1000, NbReadFrames: 1000, RFrameRate: "24/1", Height: 1080, Duration: time.Minute}
	if m.frameRate != "" {
		res.RFrameRate = m.frameRate
	}
	switch {
	case m.probeCallIdx < len(m.probeResults):
		res = m.probeResults[m.probeCallIdx]
	case m.probeCallIdx < len(m.probeErrors) && m.probeErrors[m.probeCallIdx] != nil:
		err := m.probeErrors[m.probeCallIdx]
		m.probeCallIdx++
		return VideoStream{}, err
	}
	m.probeCallIdx++
	m.probePaths = append(m.probePaths, path)
	if m.counts == nil {
		m.counts = make(map[string]int)
	}
	m.counts[path] = res.NbReadFrames
	res.NbReadFrames = 0
	return res, nil
}

func (m *mockEncoder) CountFrames(_ context.Context, path string, _ func(ProgressStats),
	_ func(string), _ func(error)) (int, error) {
	if m.countErr != nil {
		return 0, m.countErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.countPaths = append(m.countPaths, path)
	return m.counts[path], nil
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

func (m *mockStatsCache) Snapshot() (mean, stddev float64, ok bool) {
	return float64(m.mean), float64(m.stddev), true
}

// mockCallbacks satisfies QPSearchCallbacks for tests.
type mockCallbacks struct{}

func (m *mockCallbacks) Debug(workerID int, format string, a ...any)                   {}
func (m *mockCallbacks) Warning(workerID int, format string, a ...any)                 {}
func (m *mockCallbacks) Error(workerID int, err error)                                 {}
func (m *mockCallbacks) OnSegmentStart(workerID, segmentIndex int, segmentPath string) {}
func (m *mockCallbacks) OnSegmentDone(workerID int, segment SegmentResult, currentTotalDuration time.Duration, currentTotalSize int64) {
}
func (m *mockCallbacks) OnSegmentNewCandidate(workerID, qpCandidate int)                    {}
func (m *mockCallbacks) OnSegmentCandidateDone(workerID, qpCandidate int, passed bool)      {}
func (m *mockCallbacks) OnSegmentCAMBIStart(workerID, vmafQP int)                           {}
func (m *mockCallbacks) OnSegmentCAMBICandidate(workerID, qpCandidate int)                  {}
func (m *mockCallbacks) OnSegmentCAMBICandidateDone(workerID, qpCandidate int, passed bool) {}
func (m *mockCallbacks) OnSegmentAnalysisStart(workerID int, duration time.Duration)        {}
func (m *mockCallbacks) OnSegmentAnalysisProgress(workerID int, stats ProgressStats)        {}
func (m *mockCallbacks) OnSegmentAnalysisStop(workerID int)                                 {}
func (m *mockCallbacks) OnSegmentEncodeStart(workerID int, totalFrames int)                 {}
func (m *mockCallbacks) OnSegmentEncodeProgress(workerID int, stats ProgressStats)          {}
func (m *mockCallbacks) OnSegmentEncodeStop(workerID int)                                   {}
func (m *mockCallbacks) OnSegmentVMAFStart(workerID int, totalFrames int)                   {}
func (m *mockCallbacks) OnSegmentVMAFProgress(workerID int, stats ProgressStats)            {}
func (m *mockCallbacks) OnSegmentVMAFStop(workerID int)                                     {}

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

func TestFindAllSegmentsQP_CountFramesError(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	encoder := &mockEncoder{
		name:     "mock",
		qpMin:    0,
		qpMax:    51,
		countErr: fmt.Errorf("count failed"),
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
		t.Fatal("expected error when CountFrames fails, got nil")
	}
	if !strings.Contains(err.Error(), "count failed") {
		t.Errorf("expected error to contain 'count failed', got %v", err)
	}
	if len(encoder.encodeCalls) != 0 {
		t.Errorf("expected no encode without a frame count, got %d", len(encoder.encodeCalls))
	}
}

// TestFindAllSegmentsQP_EncodeFrameCount checks that every encode is counted right after it is
// made, before it is measured (see segmentQP): one short of the segment's frames is encoded again,
// with a warning, and counted as an attempt; short twice, the segment fails without measuring it.
func TestFindAllSegmentsQP_EncodeFrameCount(t *testing.T) {
	search := func(encodeFrames map[string][]int) (*mockEncoder, *warningCallbacks, QPSearchResults, error) {
		t.Helper()
		// a linear curve gated on a mean of 80, from 26 ± 13: QPs 26, 13 and 14 encoded, 13 kept
		encoder := &mockEncoder{name: "mock", qpMax: 51, vmafResults: linearCurve(), encodeFrames: encodeFrames}
		auditor, err := NewVMAFChecker(VMAFOffValue, VMAFOffValue, VMAFOffValue, VMAFOffValue,
			VMAFOffValue, VMAFOffValue, VMAFOffValue, 80)
		if err != nil {
			t.Fatal(err)
		}
		workingDir := t.TempDir()
		source := filepath.Join(workingDir, "segment.mkv")
		if err = os.WriteFile(source, []byte("source"), 0644); err != nil {
			t.Fatal(err)
		}
		callbacks := &warningCallbacks{}
		results, err := FindAllSegmentsQP(context.Background(), callbacks, QPSearchConfig{
			SegmentsPaths: []string{source},
			Auditor:       auditor,
			WorkingDir:    workingDir,
			StatsCache:    &mockStatsCache{mean: 26, stddev: 13},
			Encoder:       encoder,
		})
		return encoder, callbacks, results, err
	}
	encodesOf := func(encoder *mockEncoder, qp int) (n int) {
		for _, call := range encoder.encodeCalls {
			if call.qp == qp {
				n++
			}
		}
		return
	}
	measuresOf := func(encoder *mockEncoder, qp int) (n int) {
		for _, call := range encoder.vmafCalls {
			if _, callQP, err := extractSegmentAndQP(call.distorted); err == nil && callQP == qp {
				n++
			}
		}
		return
	}
	// Every encode whole: the source counted, then each encode right after it was made
	encoder, callbacks, reference, err := search(nil)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(reference.QPs, []int{13}) || len(callbacks.warnings) != 0 {
		t.Fatalf("expected QP 13 without a warning, got %v and %v", reference.QPs, callbacks.warnings)
	}
	if len(encoder.countPaths) != 1+len(encoder.encodeCalls) {
		t.Fatalf("expected the source and every encode counted, got %v", encoder.countPaths)
	}
	for i, call := range encoder.encodeCalls {
		if encoder.countPaths[1+i] != call.output {
			t.Errorf("encode %d (%s) not counted right after it was made: %v", i, call.output, encoder.countPaths)
		}
	}
	// The encode of QP 13 short once: encoded again, the search and its result unchanged
	encoder, callbacks, results, err := search(map[string][]int{"seg_000000_qp013.mkv": {999}})
	if err != nil {
		t.Fatalf("a short encode should be encoded again, got %v", err)
	}
	if !slices.Equal(results.QPs, reference.QPs) || !slices.Equal(results.SegmentsFrames, []int{1000}) {
		t.Errorf("expected QP 13 and its 1000 frames, got %v and %v", results.QPs, results.SegmentsFrames)
	}
	if encodesOf(encoder, 13) != 2 || measuresOf(encoder, 13) != 1 {
		t.Errorf("expected QP 13 encoded twice and measured once, got %d encodes and %d measures",
			encodesOf(encoder, 13), measuresOf(encoder, 13))
	}
	if results.TotalNbAttempts != reference.TotalNbAttempts+1 {
		t.Errorf("expected the retry counted as an attempt: %d, got %d", reference.TotalNbAttempts+1, results.TotalNbAttempts)
	}
	if len(callbacks.warnings) != 1 || !strings.Contains(callbacks.warnings[0], "QP 13 has 999 frames instead of 1000") {
		t.Errorf("expected a warning about the short encode of QP 13, got %v", callbacks.warnings)
	}
	// Short twice: the segment fails, the short encodes never measured
	encoder, _, _, err = search(map[string][]int{"seg_000000_qp013.mkv": {999, 999}})
	if err == nil || !strings.Contains(err.Error(), "999 frames instead of 1000, twice") {
		t.Errorf("expected an encode short twice to fail the segment, got %v", err)
	}
	if encodesOf(encoder, 13) != 2 || measuresOf(encoder, 13) != 0 {
		t.Errorf("expected QP 13 encoded twice and never measured, got %d encodes and %d measures",
			encodesOf(encoder, 13), measuresOf(encoder, 13))
	}
}

// TestFindAllSegmentsQP_ErrorNamesSegment checks that a failure numbers its segment from 1, like
// the warnings and the log lines, and names its file, whose name counts from 0 (see job).
func TestFindAllSegmentsQP_ErrorNamesSegment(t *testing.T) {
	// the second segment's encode of QP 13, the one its search keeps, short twice
	encoder := &mockEncoder{name: "mock", qpMax: 51, vmafResults: linearCurve(),
		encodeFrames: map[string][]int{"seg_000001_qp013.mkv": {999, 999}}}
	auditor, err := NewVMAFChecker(VMAFOffValue, VMAFOffValue, VMAFOffValue, VMAFOffValue,
		VMAFOffValue, VMAFOffValue, VMAFOffValue, 80)
	if err != nil {
		t.Fatal(err)
	}
	workingDir := t.TempDir()
	segments := make([]string, 3)
	for segment := range segments {
		segments[segment] = filepath.Join(workingDir, fmt.Sprintf("seg_%06d.mkv", segment))
		if err = os.WriteFile(segments[segment], []byte("source"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	callbacks := &warningCallbacks{}
	_, err = FindAllSegmentsQP(context.Background(), callbacks, QPSearchConfig{
		SegmentsPaths: segments,
		Auditor:       auditor,
		WorkingDir:    workingDir,
		StatsCache:    &mockStatsCache{mean: 26, stddev: 13},
		Encoder:       encoder,
	})
	if err == nil || !strings.Contains(err.Error(), "for segment 2 (seg_000001.mkv): ") {
		t.Errorf("expected the error to name segment 2 and its file, got %v", err)
	}
	if len(callbacks.warnings) != 1 || !strings.HasPrefix(callbacks.warnings[0], "Segment 2: ") {
		t.Errorf("expected the warning to name segment 2 too, got %v", callbacks.warnings)
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

// TestFindAllSegmentsQP_QPMaxMinusOneIsOptimal covers the case where qpMax has been tested
// and rejected during bracketing, and the highest valid QP is its direct neighbour. Once
// qpMax-1 validates, the search looks up and finds qpMax already computed: it must conclude
// on qpMax-1, not on qpMax because it "can not go higher". This is not a best effort either:
// best effort only exists at qpMin.
func TestFindAllSegmentsQP_QPMaxMinusOneIsOptimal(t *testing.T) {
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

	// Threshold 25: QP 50 validates (mean 25), QP 51 does not (mean 23.5).
	auditor, err := NewVMAFChecker(VMAFOffValue, VMAFOffValue, VMAFOffValue, VMAFOffValue,
		VMAFOffValue, VMAFOffValue, VMAFOffValue, 25)
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

	if len(results.QPs) != 1 || results.QPs[0] != 50 {
		t.Fatalf("expected QP 50, got %v", results.QPs)
	}
	if !auditor.Validate(vmafResults[results.QPs[0]]) {
		t.Errorf("selected QP %d does not validate the VMAF profile", results.QPs[0])
	}
	if results.NbBestEfforts != 0 {
		t.Errorf("expected no best-effort segments, got %d", results.NbBestEfforts)
	}
	// qpMax must have been tested (and rejected) for qpMax-1 to be proven optimal
	var qpMaxTested bool
	for _, call := range encoder.encodeCalls {
		if call.qp == 51 {
			qpMaxTested = true
		}
	}
	if !qpMaxTested {
		t.Error("expected qpMax to be tested before concluding on qpMax-1")
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

// recordingCallbacks records what FindAllSegmentsQP reports, from any number of workers.
type recordingCallbacks struct {
	mockCallbacks
	mu            sync.Mutex
	workerIDs     map[int]bool
	doneCalls     int
	lastDuration  time.Duration // highest total duration reported
	lastTotalSize int64         // highest total size reported
	// events are the candidate events of the search, in order, as "new 26", "done 26 false",
	// "cambi 13", "walk 12", "walk done 12 true"; results the segments as reported done
	events  []string
	results []SegmentResult
}

func (r *recordingCallbacks) OnSegmentStart(workerID, segmentIndex int, segmentPath string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.workerIDs == nil {
		r.workerIDs = make(map[int]bool)
	}
	r.workerIDs[workerID] = true
}

func (r *recordingCallbacks) OnSegmentDone(workerID int, segment SegmentResult,
	currentTotalDuration time.Duration, currentTotalSize int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.doneCalls++
	r.results = append(r.results, segment)
	r.lastDuration = max(r.lastDuration, currentTotalDuration)
	r.lastTotalSize = max(r.lastTotalSize, currentTotalSize)
}

func (r *recordingCallbacks) event(format string, a ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, fmt.Sprintf(format, a...))
}

func (r *recordingCallbacks) OnSegmentNewCandidate(workerID, qpCandidate int) {
	r.event("new %d", qpCandidate)
}

func (r *recordingCallbacks) OnSegmentCandidateDone(workerID, qpCandidate int, passed bool) {
	r.event("done %d %t", qpCandidate, passed)
}

func (r *recordingCallbacks) OnSegmentCAMBIStart(workerID, vmafQP int) {
	r.event("cambi %d", vmafQP)
}

func (r *recordingCallbacks) OnSegmentCAMBICandidate(workerID, qpCandidate int) {
	r.event("walk %d", qpCandidate)
}

func (r *recordingCallbacks) OnSegmentCAMBICandidateDone(workerID, qpCandidate int, passed bool) {
	r.event("walk done %d %t", qpCandidate, passed)
}

// TestFindAllSegmentsQP_Concurrent runs the search with several workers. Along with the race
// detector (go test -race), it is what covers the concurrent mode: GPU encoders only accept a
// limited number of sessions, NbConcurrentSegments must never be exceeded.
func TestFindAllSegmentsQP_Concurrent(t *testing.T) {
	const (
		nbSegments = 12
		nbWorkers  = 4
	)
	ctx := context.Background()
	tmpDir := t.TempDir()

	// Each segment has its own response, so its own optimal QP: results must not get mixed up
	// between workers. With mean >= 70: slope 1 -> QP 30, slope 1.5 -> QP 20, slope 2 -> QP 15.
	slopes := []float64{1, 1.5, 2}
	expectedQPs := []int{30, 20, 15}
	vmafBySegment := make(map[int]map[int]VMAFStats, nbSegments)
	segments := make([]string, nbSegments)
	for segment := range nbSegments {
		vmafBySegment[segment] = make(map[int]VMAFStats, 52)
		for qp := 0; qp <= 51; qp++ {
			mean := 100 - slopes[segment%len(slopes)]*float64(qp)
			vmafBySegment[segment][qp] = VMAFStats{
				Mean: mean, Minimum: mean - 5, Percentile1: mean - 4, Percentile5: mean - 3, Percentile10: mean - 2,
				Percentile25: mean - 1, Median: mean, HarmonicMean: mean - 0.5, Maximum: mean + 2,
			}
		}
		segments[segment] = filepath.Join(tmpDir, fmt.Sprintf("seg_%06d.mkv", segment))
		if err := os.WriteFile(segments[segment], []byte("source"), 0644); err != nil {
			t.Fatalf("failed to create source segment: %v", err)
		}
	}

	encoder := &mockEncoder{
		name:          "mock",
		qpMin:         0,
		qpMax:         51,
		vmafBySegment: vmafBySegment,
		encodeDelay:   2 * time.Millisecond,
	}
	auditor, err := NewVMAFChecker(VMAFOffValue, VMAFOffValue, VMAFOffValue, VMAFOffValue,
		VMAFOffValue, VMAFOffValue, VMAFOffValue, 70)
	if err != nil {
		t.Fatalf("failed to create auditor: %v", err)
	}
	callbacks := &recordingCallbacks{}

	results, err := FindAllSegmentsQP(ctx, callbacks, QPSearchConfig{
		SegmentsPaths:        segments,
		Auditor:              auditor,
		WorkingDir:           tmpDir,
		StatsCache:           &mockStatsCache{mean: 26, stddev: 13},
		Encoder:              encoder,
		NbConcurrentSegments: nbWorkers,
	})
	if err != nil {
		t.Fatalf("FindAllSegmentsQP failed: %v", err)
	}

	// Results are per segment, whatever the worker and the order they ended in
	for segment, qp := range results.QPs {
		if expected := expectedQPs[segment%len(expectedQPs)]; qp != expected {
			t.Errorf("segment %d: expected QP %d, got %d", segment, expected, qp)
		}
		if _, qpInPath, err := extractSegmentAndQP(results.EncodedSegmentsPaths[segment]); err != nil || qpInPath != qp {
			t.Errorf("segment %d: encoded path %q does not match QP %d", segment, results.EncodedSegmentsPaths[segment], qp)
		}
	}
	if results.TotalSegmentsFrames != nbSegments*1000 {
		t.Errorf("expected %d frames, got %d", nbSegments*1000, results.TotalSegmentsFrames)
	}
	// Per segment frame counts and the shared frame rate feed the concat durations
	if len(results.SegmentsFrames) != nbSegments {
		t.Fatalf("expected %d segments frame counts, got %d", nbSegments, len(results.SegmentsFrames))
	}
	for segment, frames := range results.SegmentsFrames {
		if frames != 1000 {
			t.Errorf("segment %d: expected 1000 frames, got %d", segment, frames)
		}
	}
	if results.FrameRate != "24/1" {
		t.Errorf("expected frame rate 24/1, got %q", results.FrameRate)
	}
	if durations, err := results.SegmentsDurations(); err != nil {
		t.Errorf("SegmentsDurations failed: %v", err)
	} else if len(durations) != nbSegments {
		t.Errorf("expected %d durations, got %d", nbSegments, len(durations))
	} else {
		// 1000 frames at 24 fps: 41.666666... s, rounded to the microsecond
		for segment, d := range durations {
			if d != 41666667*time.Microsecond {
				t.Errorf("segment %d: expected %s, got %s", segment, 41666667*time.Microsecond, d)
			}
		}
	}
	if results.TotalNbAttempts != len(encoder.encodeCalls) {
		t.Errorf("expected %d attempts (encode calls), got %d", len(encoder.encodeCalls), results.TotalNbAttempts)
	}
	// Workers: really concurrent, never more than asked, IDs usable as slice indexes
	if encoder.maxActive < 2 || encoder.maxActive > nbWorkers {
		t.Errorf("expected between 2 and %d encodes at the same time, got %d", nbWorkers, encoder.maxActive)
	}
	for workerID := range callbacks.workerIDs {
		if workerID < 0 || workerID >= nbWorkers {
			t.Errorf("worker ID %d is out of [0, %d]", workerID, nbWorkers-1)
		}
	}
	// Progress: one call per segment, the last totals being the complete ones
	if callbacks.doneCalls != nbSegments {
		t.Errorf("expected %d OnSegmentDone calls, got %d", nbSegments, callbacks.doneCalls)
	}
	if callbacks.lastDuration != nbSegments*time.Minute {
		t.Errorf("expected a total duration of %v, got %v", nbSegments*time.Minute, callbacks.lastDuration)
	}
	if expectedSize := int64(nbSegments * len("dummy")); callbacks.lastTotalSize != expectedSize {
		t.Errorf("expected a total size of %d, got %d", expectedSize, callbacks.lastTotalSize)
	}
}

func TestFramesDuration(t *testing.T) {
	tests := []struct {
		name      string
		frames    int
		frameRate string
		expected  time.Duration
		wantErr   bool
	}{
		// 886 frames at 23.976 fps last 36.953583333 s: rounded to the microsecond, not to
		// the millisecond the container would use
		{"ntsc film", 886, "24000/1001", 36953583 * time.Microsecond, false},
		{"one frame ntsc film", 1, "24000/1001", 41708 * time.Microsecond, false},
		{"pal", 125, "25/1", 5 * time.Second, false},
		{"integer rate", 48, "24", 2 * time.Second, false},
		{"zero frames", 0, "24000/1001", 0, false},
		{"negative frames", -1, "24/1", 0, true},
		{"empty rate", 10, "", 0, true},
		{"zero rate", 10, "0/1", 0, true},
		{"zero denominator", 10, "24/0", 0, true},
		{"decimal rate is not a fraction", 10, "23.976", 0, true},
		{"garbage", 10, "abc/def", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := FramesDuration(tt.frames, tt.frameRate)
			if (err != nil) != tt.wantErr {
				t.Fatalf("FramesDuration(%d, %q) error = %v, wantErr %t", tt.frames, tt.frameRate, err, tt.wantErr)
			}
			if got != tt.expected {
				t.Errorf("FramesDuration(%d, %q) = %s, expected %s", tt.frames, tt.frameRate, got, tt.expected)
			}
		})
	}
}

func TestSegmentsDurations(t *testing.T) {
	results := QPSearchResults{
		SegmentsFrames: []int{403, 167, 886},
		FrameRate:      "24000/1001",
	}
	durations, err := results.SegmentsDurations()
	if err != nil {
		t.Fatalf("SegmentsDurations failed: %v", err)
	}
	expected := []time.Duration{16808458 * time.Microsecond, 6965292 * time.Microsecond, 36953583 * time.Microsecond}
	if len(durations) != len(expected) {
		t.Fatalf("expected %d durations, got %d", len(expected), len(durations))
	}
	for i := range expected {
		if durations[i] != expected[i] {
			t.Errorf("segment %d: expected %s, got %s", i, expected[i], durations[i])
		}
	}
	// A bad frame rate fails the whole set
	results.FrameRate = "vfr"
	if _, err = results.SegmentsDurations(); err == nil {
		t.Error("expected an error with an invalid frame rate")
	}
	// An empty result has no durations and no error
	if durations, err = (QPSearchResults{FrameRate: "24/1"}).SegmentsDurations(); err != nil || len(durations) != 0 {
		t.Errorf("expected no durations and no error for an empty result, got %v, %v", durations, err)
	}
}

func TestFindAllSegmentsQP_FrameRateMismatch(t *testing.T) {
	tmpDir := t.TempDir()
	segments := []string{
		filepath.Join(tmpDir, "segment0.mkv"),
		filepath.Join(tmpDir, "segment1.mkv"),
	}
	for _, seg := range segments {
		if err := os.WriteFile(seg, []byte("source"), 0644); err != nil {
			t.Fatalf("failed to create source segment: %v", err)
		}
	}
	vmafResults := make(map[int]VMAFStats)
	for qp := 0; qp <= 51; qp++ {
		vmafResults[qp] = linearVMAF(qp)
	}
	encoder := &mockEncoder{
		name:        "mock",
		qpMin:       0,
		qpMax:       51,
		vmafResults: vmafResults,
		// The mock serves probe results in call order: the first call is the source probe of
		// segment 0, which sets the frame rate of the whole set; the next one, the source of
		// segment 1 (the encodes are counted, not probed), gets the mock default, 24/1.
		probeResults: []VideoStream{
			{NbFrames: 1000, NbReadFrames: 1000, RFrameRate: "25/1", Height: 1080, Duration: time.Minute},
		},
	}
	auditor, err := NewVMAFChecker(VMAFOffValue, VMAFOffValue, VMAFOffValue, VMAFOffValue,
		VMAFOffValue, VMAFOffValue, VMAFOffValue, 85)
	if err != nil {
		t.Fatalf("failed to create auditor: %v", err)
	}
	_, err = FindAllSegmentsQP(context.Background(), &mockCallbacks{}, QPSearchConfig{
		SegmentsPaths: segments,
		Auditor:       auditor,
		WorkingDir:    tmpDir,
		StatsCache:    &mockStatsCache{mean: 26, stddev: 13},
		Encoder:       encoder,
	})
	if err == nil || !strings.Contains(err.Error(), "frame rate") {
		t.Fatalf("expected a frame rate mismatch error, got %v", err)
	}
}

func TestSameFrameRate(t *testing.T) {
	tests := []struct {
		a, b    string
		same    bool
		wantErr bool
	}{
		{"24000/1001", "24000/1001", true, false},
		{"25", "25/1", true, false},
		// what Matroska gives back for 59.94 and 119.88 fps, and for 30 fps written in
		// microseconds (measured): 5e-8, 4e-7 and 1.0e-5 away
		{"60000/1001", "19001/317", true, false},
		{"120000/1001", "29011/242", true, false},
		{"1000000/33333", "30/1", true, false},
		// the worst readback found in a scan of the rates: 1.7e-5 away
		{"10000000/17123", "29785/51", true, false},
		// the closest distinct usual rates
		{"24000/1001", "24/1", false, false},
		{"30000/1001", "30/1", false, false},
		{"60000/1001", "60/1", false, false},
		{"25/1", "24/1", false, false},
		{"24000/1001", "", false, true},
		{"23.976", "24000/1001", false, true},
	}
	for _, tt := range tests {
		same, err := SameFrameRate(tt.a, tt.b)
		if (err != nil) != tt.wantErr {
			t.Errorf("SameFrameRate(%q, %q) error = %v, wantErr %t", tt.a, tt.b, err, tt.wantErr)
			continue
		}
		if same != tt.same {
			t.Errorf("SameFrameRate(%q, %q) = %t, want %t", tt.a, tt.b, same, tt.same)
		}
	}
}

// sourceFrameRateSearch runs a two segments search whose segments declare segmentsRate,
// cut from a source at sourceRate, with concurrency workers.
func sourceFrameRateSearch(t *testing.T, segmentsRate, sourceRate string, concurrency int) (*mockEncoder, QPSearchResults, error) {
	t.Helper()
	tmpDir := t.TempDir()
	segments := []string{filepath.Join(tmpDir, "segment0.mkv"), filepath.Join(tmpDir, "segment1.mkv")}
	for _, seg := range segments {
		if err := os.WriteFile(seg, []byte("source"), 0644); err != nil {
			t.Fatalf("failed to create source segment: %v", err)
		}
	}
	vmafResults := make(map[int]VMAFStats)
	for qp := 0; qp <= 51; qp++ {
		vmafResults[qp] = linearVMAF(qp)
	}
	encoder := &mockEncoder{name: "mock", qpMin: 0, qpMax: 51, vmafResults: vmafResults, frameRate: segmentsRate}
	auditor, err := NewVMAFChecker(VMAFOffValue, VMAFOffValue, VMAFOffValue, VMAFOffValue,
		VMAFOffValue, VMAFOffValue, VMAFOffValue, 85)
	if err != nil {
		t.Fatalf("failed to create auditor: %v", err)
	}
	results, err := FindAllSegmentsQP(context.Background(), &mockCallbacks{}, QPSearchConfig{
		SegmentsPaths:        segments,
		Auditor:              auditor,
		WorkingDir:           tmpDir,
		StatsCache:           &mockStatsCache{mean: 26, stddev: 13},
		Encoder:              encoder,
		NbConcurrentSegments: concurrency,
		SourceFrameRate:      sourceRate,
	})
	return encoder, results, err
}

func TestFindAllSegmentsQP_SourceFrameRate(t *testing.T) {
	// Matroska segments declaring 19001/317, cut from a 60000/1001 source: the results carry
	// the source's rate, and so do the durations built on them
	_, results, err := sourceFrameRateSearch(t, "19001/317", "60000/1001", 1)
	if err != nil {
		t.Fatalf("FindAllSegmentsQP failed: %v", err)
	}
	if results.FrameRate != "60000/1001" {
		t.Errorf("expected the source's frame rate 60000/1001, got %q", results.FrameRate)
	}
	durations, err := results.SegmentsDurations()
	if err != nil {
		t.Fatalf("SegmentsDurations failed: %v", err)
	}
	// 1000 frames at 60000/1001: 16.683333 s (at 19001/317 it would be 16.683332 s)
	for i, duration := range durations {
		if duration != 16683333*time.Microsecond {
			t.Errorf("segment %d: expected 16.683333s, got %s", i, duration)
		}
	}
	// Without a source, the rate the segments declare
	if _, results, err = sourceFrameRateSearch(t, "19001/317", "", 1); err != nil || results.FrameRate != "19001/317" {
		t.Errorf("expected the segments' frame rate 19001/317 without a source, got %q (error %v)", results.FrameRate, err)
	}
}

func TestFindAllSegmentsQP_SourceFrameRateMismatch(t *testing.T) {
	// Segments at 24/1 given a 25 fps source: refused before any encode, whatever the
	// number of workers
	for _, concurrency := range []int{1, 2} {
		encoder, _, err := sourceFrameRateSearch(t, "24/1", "25/1", concurrency)
		if err == nil || !strings.Contains(err.Error(), "24/1") || !strings.Contains(err.Error(), "25/1") {
			t.Fatalf("%d workers: expected an error naming both frame rates, got %v", concurrency, err)
		}
		if len(encoder.encodeCalls) != 0 {
			t.Errorf("%d workers: expected no encode, got %d", concurrency, len(encoder.encodeCalls))
		}
	}
	// A source frame rate that can not be read is refused before anything
	if encoder, _, err := sourceFrameRateSearch(t, "24/1", "23.976", 1); err == nil {
		t.Error("expected an error with an invalid source frame rate")
	} else if encoder.probeCallIdx != 0 {
		t.Errorf("expected no probe with an invalid source frame rate, got %d", encoder.probeCallIdx)
	}
}
