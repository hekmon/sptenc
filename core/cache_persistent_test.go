package core

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/gofrs/flock"
)

// cacheMockEncoder provides the name and QP range used by StatsCacheHistory tests.
type cacheMockEncoder struct {
	name  string
	qpMin int
	qpMax int
}

func TestNewStatsCacheHistory(t *testing.T) {
	tmpDir := t.TempDir()
	encoder := &cacheMockEncoder{name: "mockenc", qpMin: 0, qpMax: 51}
	profile, err := NewVMAFChecker(VMAFOffValue, VMAFOffValue, VMAFOffValue, VMAFOffValue,
		VMAFOffValue, VMAFOffValue, VMAFOffValue, 80)
	if err != nil {
		t.Fatalf("failed to create profile: %v", err)
	}

	sch, err := NewStatsCacheHistory(tmpDir, encoder.name, encoder.qpMin, encoder.qpMax, "", false, profile, "")
	if err != nil {
		t.Fatalf("NewStatsCacheHistory failed: %v", err)
	}
	if sch == nil {
		t.Fatal("expected non-nil StatsCacheHistory")
	}
	if sch.qpMin != 0 || sch.qpMax != 51 {
		t.Errorf("expected qpMin=0 qpMax=51, got %d %d", sch.qpMin, sch.qpMax)
	}
	if sch.GetPath() == "" {
		t.Error("expected non-empty path")
	}
	if len(sch.stats) != 0 {
		t.Errorf("expected empty stats initially, got %d", len(sch.stats))
	}
}

func TestStatsCacheHistory_AddRunAndDedup(t *testing.T) {
	tmpDir := t.TempDir()
	encoder := &cacheMockEncoder{name: "mockenc", qpMin: 0, qpMax: 51}
	profile, err := NewVMAFChecker(VMAFOffValue, VMAFOffValue, VMAFOffValue, VMAFOffValue,
		VMAFOffValue, VMAFOffValue, VMAFOffValue, 80)
	if err != nil {
		t.Fatalf("failed to create profile: %v", err)
	}

	sch, err := NewStatsCacheHistory(tmpDir, encoder.name, encoder.qpMin, encoder.qpMax, "", false, profile, "")
	if err != nil {
		t.Fatalf("NewStatsCacheHistory failed: %v", err)
	}

	qps := []int{10, 12, 14, 16}
	mean1, stddev1, err := sch.AddRun(qps)
	if err != nil {
		t.Fatalf("AddRun failed: %v", err)
	}
	if mean1 == 0 && stddev1 == 0 {
		t.Error("expected non-zero mean/stddev")
	}
	if len(sch.stats) != 1 {
		t.Fatalf("expected 1 stat entry, got %d", len(sch.stats))
	}

	// Add the exact same run again — should be deduplicated
	mean2, stddev2, err := sch.AddRun(qps)
	if err != nil {
		t.Fatalf("AddRun second time failed: %v", err)
	}
	if mean1 != mean2 || stddev1 != stddev2 {
		t.Errorf("expected same mean/stddev for duplicate run, got %v/%v vs %v/%v", mean1, stddev1, mean2, stddev2)
	}
	if len(sch.stats) != 1 {
		t.Errorf("expected still 1 stat entry after dedup, got %d", len(sch.stats))
	}
}

func TestStatsCacheHistory_AddRunConcurrent(t *testing.T) {
	tmpDir := t.TempDir()
	encoder := &cacheMockEncoder{name: "mockenc", qpMin: 0, qpMax: 51}
	profile, err := NewVMAFChecker(VMAFOffValue, VMAFOffValue, VMAFOffValue, VMAFOffValue,
		VMAFOffValue, VMAFOffValue, VMAFOffValue, 80)
	if err != nil {
		t.Fatalf("failed to create profile: %v", err)
	}

	sch, err := NewStatsCacheHistory(tmpDir, encoder.name, encoder.qpMin, encoder.qpMax, "", false, profile, "")
	if err != nil {
		t.Fatalf("NewStatsCacheHistory failed: %v", err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			qps := []int{idx, idx + 2, idx + 4}
			_, _, err := sch.AddRun(qps)
			if err != nil {
				t.Errorf("AddRun goroutine %d failed: %v", idx, err)
			}
		}(i)
	}
	wg.Wait()

	if len(sch.stats) != 10 {
		t.Errorf("expected 10 unique stat entries, got %d", len(sch.stats))
	}
}

func TestStatsCacheHistory_GetMeanStdDev_Empty(t *testing.T) {
	tmpDir := t.TempDir()
	encoder := &cacheMockEncoder{name: "mockenc", qpMin: 0, qpMax: 51}
	profile, err := NewVMAFChecker(VMAFOffValue, VMAFOffValue, VMAFOffValue, VMAFOffValue,
		VMAFOffValue, VMAFOffValue, VMAFOffValue, 80)
	if err != nil {
		t.Fatalf("failed to create profile: %v", err)
	}

	sch, err := NewStatsCacheHistory(tmpDir, encoder.name, encoder.qpMin, encoder.qpMax, "", false, profile, "")
	if err != nil {
		t.Fatalf("NewStatsCacheHistory failed: %v", err)
	}

	mean, stddev := sch.GetMeanStdDev()
	expectedMean := 26   // middle of [0,51] is 25.5, rounded
	expectedStddev := 13 // a quarter of the 52 QPs
	if mean != expectedMean {
		t.Errorf("expected mean %d for empty cache, got %d", expectedMean, mean)
	}
	if stddev != expectedStddev {
		t.Errorf("expected stddev %d for empty cache, got %d", expectedStddev, stddev)
	}
}

func TestStatsCacheHistory_GetMeanStdDev_Weighted(t *testing.T) {
	tmpDir := t.TempDir()
	encoder := &cacheMockEncoder{name: "mockenc", qpMin: 0, qpMax: 51}
	profile, err := NewVMAFChecker(VMAFOffValue, VMAFOffValue, VMAFOffValue, VMAFOffValue,
		VMAFOffValue, VMAFOffValue, VMAFOffValue, 80)
	if err != nil {
		t.Fatalf("failed to create profile: %v", err)
	}

	sch, err := NewStatsCacheHistory(tmpDir, encoder.name, encoder.qpMin, encoder.qpMax, "", false, profile, "")
	if err != nil {
		t.Fatalf("NewStatsCacheHistory failed: %v", err)
	}

	// Run 1: [10, 10, 20] → weight 3
	_, _, err = sch.AddRun([]int{10, 10, 20})
	if err != nil {
		t.Fatalf("AddRun 1 failed: %v", err)
	}
	// Run 2: [20, 20] → weight 2
	_, _, err = sch.AddRun([]int{20, 20})
	if err != nil {
		t.Fatalf("AddRun 2 failed: %v", err)
	}

	mean, stddev := sch.GetMeanStdDev()
	// Weighted mean of means:
	// run1 mean = 40/3 ≈ 13.333, run2 mean = 20
	// weighted mean = (13.333*3 + 20*2) / 5 = (40 + 40) / 5 = 16
	if mean != 16 {
		t.Errorf("expected weighted mean 16, got %d", mean)
	}
	// stddev should be > 0
	if stddev < 1 {
		t.Errorf("expected stddev >= 1, got %d", stddev)
	}
}

func TestStatsCacheHistory_Snapshot_Empty(t *testing.T) {
	tmpDir := t.TempDir()
	encoder := &cacheMockEncoder{name: "mockenc", qpMin: 0, qpMax: 51}
	profile, err := NewVMAFChecker(VMAFOffValue, VMAFOffValue, VMAFOffValue, VMAFOffValue,
		VMAFOffValue, VMAFOffValue, VMAFOffValue, 80)
	if err != nil {
		t.Fatalf("failed to create profile: %v", err)
	}

	sch, err := NewStatsCacheHistory(tmpDir, encoder.name, encoder.qpMin, encoder.qpMax, "", false, profile, "")
	if err != nil {
		t.Fatalf("NewStatsCacheHistory failed: %v", err)
	}

	mean, stddev, ok := sch.Snapshot()
	if ok {
		t.Error("expected Snapshot to return ok=false for empty cache")
	}
	if mean != 0 || stddev != 0 {
		t.Errorf("expected zero values for empty snapshot, got %v %v", mean, stddev)
	}
}

func TestStatsCacheHistory_Snapshot_Weighted(t *testing.T) {
	tmpDir := t.TempDir()
	encoder := &cacheMockEncoder{name: "mockenc", qpMin: 0, qpMax: 51}
	profile, err := NewVMAFChecker(VMAFOffValue, VMAFOffValue, VMAFOffValue, VMAFOffValue,
		VMAFOffValue, VMAFOffValue, VMAFOffValue, 80)
	if err != nil {
		t.Fatalf("failed to create profile: %v", err)
	}

	sch, err := NewStatsCacheHistory(tmpDir, encoder.name, encoder.qpMin, encoder.qpMax, "", false, profile, "")
	if err != nil {
		t.Fatalf("NewStatsCacheHistory failed: %v", err)
	}

	// Run 1: [10, 10, 20] → weight 3, mean 40/3 ≈ 13.333
	_, _, err = sch.AddRun([]int{10, 10, 20})
	if err != nil {
		t.Fatalf("AddRun 1 failed: %v", err)
	}
	// Run 2: [20, 20] → weight 2, mean 20
	_, _, err = sch.AddRun([]int{20, 20})
	if err != nil {
		t.Fatalf("AddRun 2 failed: %v", err)
	}

	mean, stddev, ok := sch.Snapshot()
	if !ok {
		t.Fatal("expected Snapshot to return ok=true")
	}
	// Weighted mean of means: (13.333*3 + 20*2) / 5 = 16
	if math.Round(mean) != 16 {
		t.Errorf("expected snapshot mean ~16, got %v", mean)
	}
	if stddev < 0 {
		t.Errorf("expected non-negative stddev, got %v", stddev)
	}
}

func TestStatsCacheHistory_SaveLoadRoundtrip(t *testing.T) {
	tmpDir := t.TempDir()
	encoder := &cacheMockEncoder{name: "mockenc", qpMin: 0, qpMax: 51}
	profile, err := NewVMAFChecker(VMAFOffValue, VMAFOffValue, VMAFOffValue, VMAFOffValue,
		VMAFOffValue, VMAFOffValue, VMAFOffValue, 80)
	if err != nil {
		t.Fatalf("failed to create profile: %v", err)
	}

	sch1, err := NewStatsCacheHistory(tmpDir, encoder.name, encoder.qpMin, encoder.qpMax, "", false, profile, "")
	if err != nil {
		t.Fatalf("NewStatsCacheHistory failed: %v", err)
	}

	qps := []int{10, 15, 20, 25}
	mean1, stddev1, err := sch1.AddRun(qps)
	if err != nil {
		t.Fatalf("AddRun failed: %v", err)
	}

	// Create a new cache pointing to the same file
	sch2, err := NewStatsCacheHistory(tmpDir, encoder.name, encoder.qpMin, encoder.qpMax, "", false, profile, "")
	if err != nil {
		t.Fatalf("NewStatsCacheHistory second instance failed: %v", err)
	}
	if len(sch2.stats) != 1 {
		t.Fatalf("expected 1 stat entry after load, got %d", len(sch2.stats))
	}
	mean2, stddev2 := sch2.GetMeanStdDev()
	if mean2 != int(math.Round(mean1)) {
		t.Errorf("loaded mean %d does not match saved mean %v", mean2, mean1)
	}
	if stddev2 != max(1, int(math.Round(stddev1))) {
		t.Errorf("loaded stddev %d does not match saved stddev %v", stddev2, stddev1)
	}
}

func TestComputeCacheStatsFileName_Stability(t *testing.T) {
	profile1, err := NewVMAFChecker(VMAFOffValue, VMAFOffValue, VMAFOffValue, VMAFOffValue,
		VMAFOffValue, VMAFOffValue, VMAFOffValue, 80)
	if err != nil {
		t.Fatalf("failed to create profile: %v", err)
	}
	profile2, err := NewVMAFChecker(VMAFOffValue, VMAFOffValue, VMAFOffValue, VMAFOffValue,
		VMAFOffValue, VMAFOffValue, VMAFOffValue, 80.1)
	if err != nil {
		t.Fatalf("failed to create profile: %v", err)
	}

	name1 := computeCacheStatsFileName("mock", "", false, profile1, "")
	name1again := computeCacheStatsFileName("mock", "", false, profile1, "")
	if name1 != name1again {
		t.Errorf("same inputs produced different filenames: %s vs %s", name1, name1again)
	}

	name2 := computeCacheStatsFileName("mock", "", false, profile2, "")
	if name1 == name2 {
		t.Error("different profiles produced the same filename")
	}

	name3 := computeCacheStatsFileName("mock", "", false, profile1, "profileA")
	if name1 == name3 {
		t.Error("different cache profiles produced the same filename")
	}

	// Model should affect filename
	name4 := computeCacheStatsFileName("mock", "vmaf_v0.6.1neg", false, profile1, "")
	if name1 == name4 {
		t.Error("different vmaf models produced the same filename")
	}

	// So should the score gated
	name5 := computeCacheStatsFileName("mock", "vmaf_v1.0.16_3d0h", true, profile1, "")
	if name5 == computeCacheStatsFileName("mock", "vmaf_v1.0.16_3d0h", false, profile1, "") {
		t.Error("different scores produced the same filename")
	}
}

// TestComputeCacheStatsFileName_Golden pins the names: fidelity has no marker, so the files
// sptenc v0.1.0 wrote (v0 models, whose single score is their fidelity score) keep theirs.
func TestComputeCacheStatsFileName_Golden(t *testing.T) {
	hmean93, err := NewVMAFChecker(VMAFOffValue, VMAFOffValue, VMAFOffValue, VMAFOffValue,
		VMAFOffValue, VMAFOffValue, 93, VMAFOffValue)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		model         string
		originalScore bool
		cacheProfile  string
		want          string
	}{
		{"vmaf_v0.6.1", false, "", "qphistory_libx265.model~vmaf_v0.6.1_vmaf-LTF8LTF8LTF8LTF8LTF8LTF8OTN8LTE.json"},
		{"vmaf_v1.0.16_3d0h", false, "", "qphistory_libx265.model~vmaf_v1.0.16_3d0h_vmaf-LTF8LTF8LTF8LTF8LTF8LTF8OTN8LTE.json"},
		{"vmaf_v1.0.16_3d0h", true, "", "qphistory_libx265.model~vmaf_v1.0.16_3d0h~original_vmaf-LTF8LTF8LTF8LTF8LTF8LTF8OTN8LTE.json"},
		{"vmaf_v1.0.16_3d0h", true, "anime", "qphistory_libx265.model~vmaf_v1.0.16_3d0h~original_vmaf-LTF8LTF8LTF8LTF8LTF8LTF8OTN8LTE_YW5pbWU.json"},
	} {
		if got := computeCacheStatsFileName("libx265", tc.model, tc.originalScore, hmean93, tc.cacheProfile); got != tc.want {
			t.Errorf("want %s, got %s", tc.want, got)
		}
	}
}

func TestStatsCacheHistory_AddRunInvalidDir(t *testing.T) {
	// Use a non-existent directory path so that os.Create inside saveStats fails
	tmpDir := filepath.Join(t.TempDir(), "nonexistent", "subdir")

	encoder := &cacheMockEncoder{name: "mockenc", qpMin: 0, qpMax: 51}
	profile, err := NewVMAFChecker(VMAFOffValue, VMAFOffValue, VMAFOffValue, VMAFOffValue,
		VMAFOffValue, VMAFOffValue, VMAFOffValue, 80)
	if err != nil {
		t.Fatalf("failed to create profile: %v", err)
	}

	sch, err := NewStatsCacheHistory(tmpDir, encoder.name, encoder.qpMin, encoder.qpMax, "", false, profile, "")
	if err != nil {
		t.Fatalf("NewStatsCacheHistory failed: %v", err)
	}

	_, _, err = sch.AddRun([]int{10, 20})
	if err == nil {
		t.Fatal("expected error when saving to invalid directory, got nil")
	}
}

func TestStatsCacheHistory_FileCorruption(t *testing.T) {
	tmpDir := t.TempDir()
	encoder := &cacheMockEncoder{name: "mockenc", qpMin: 0, qpMax: 51}
	profile, err := NewVMAFChecker(VMAFOffValue, VMAFOffValue, VMAFOffValue, VMAFOffValue,
		VMAFOffValue, VMAFOffValue, VMAFOffValue, 80)
	if err != nil {
		t.Fatalf("failed to create profile: %v", err)
	}

	sch, err := NewStatsCacheHistory(tmpDir, encoder.name, encoder.qpMin, encoder.qpMax, "", false, profile, "")
	if err != nil {
		t.Fatalf("NewStatsCacheHistory failed: %v", err)
	}

	// Corrupt the file by writing invalid JSON directly
	if err := os.WriteFile(sch.GetPath(), []byte("not json"), 0644); err != nil {
		t.Fatalf("failed to write corrupt file: %v", err)
	}

	// NewStatsCacheHistory should fail to load corrupted stats
	_, err = NewStatsCacheHistory(tmpDir, encoder.name, encoder.qpMin, encoder.qpMax, "", false, profile, "")
	if err == nil {
		t.Fatal("expected error loading corrupted cache file, got nil")
	}
}

// TestLoadRunStats_AgreesWithEncode checks that LoadRunStats, which the cache command uses to tell
// the user which cache files an encode can not use, reads every file the way an encode does.
func TestLoadRunStats_AgreesWithEncode(t *testing.T) {
	profile, err := NewVMAFChecker(VMAFOffValue, VMAFOffValue, VMAFOffValue, VMAFOffValue,
		VMAFOffValue, VMAFOffValue, 93, VMAFOffValue)
	if err != nil {
		t.Fatalf("failed to create profile: %v", err)
	}
	tests := []struct {
		name     string
		content  string
		wantErr  string // empty when both must load it
		wantRuns int
	}{
		{name: "valid", content: `[{"mean":30,"stddev":4,"weight":10}]`, wantRuns: 1},
		// A save interrupted by another writer, before saves went through a temporary file
		{name: "trailing data", content: `[{"mean":30,"stddev":4,"weight":10}]` + "\n" + `,"weight":12}]`, wantRuns: 1},
		{name: "null", content: `null`},
		{name: "empty", content: ``, wantErr: "empty cache file"},
		{name: "whitespace only", content: "\n", wantErr: "empty cache file"},
		{name: "truncated", content: `[{"mean": 30,`, wantErr: "unexpected EOF"},
		{name: "not json", content: `not json`, wantErr: "invalid character"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			path := filepath.Join(tmpDir, computeCacheStatsFileName("mockenc", "", false, profile, ""))
			if err := os.WriteFile(path, []byte(tt.content), 0644); err != nil {
				t.Fatalf("failed to write cache file: %v", err)
			}
			runs, listErr := LoadRunStats(path)
			sch, encodeErr := NewStatsCacheHistory(tmpDir, "mockenc", 0, 51, "", false, profile, "")
			for name, err := range map[string]error{"LoadRunStats": listErr, "NewStatsCacheHistory": encodeErr} {
				if tt.wantErr == "" && err != nil {
					t.Errorf("%s: unexpected error: %v", name, err)
				}
				if tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
					t.Errorf("%s: error = %v, want one containing %q", name, err, tt.wantErr)
				}
			}
			if tt.wantErr == "" {
				if len(runs) != tt.wantRuns || len(sch.stats) != tt.wantRuns {
					t.Errorf("LoadRunStats read %d runs, NewStatsCacheHistory %d, want %d", len(runs), len(sch.stats), tt.wantRuns)
				}
			}
		})
	}
}

func TestStatsCacheHistory_QPRangeBoundary(t *testing.T) {
	tmpDir := t.TempDir()
	encoder := &cacheMockEncoder{name: "mockenc", qpMin: 10, qpMax: 30}
	profile, err := NewVMAFChecker(VMAFOffValue, VMAFOffValue, VMAFOffValue, VMAFOffValue,
		VMAFOffValue, VMAFOffValue, VMAFOffValue, 80)
	if err != nil {
		t.Fatalf("failed to create profile: %v", err)
	}

	sch, err := NewStatsCacheHistory(tmpDir, encoder.name, encoder.qpMin, encoder.qpMax, "", false, profile, "")
	if err != nil {
		t.Fatalf("NewStatsCacheHistory failed: %v", err)
	}

	mean, stddev := sch.GetMeanStdDev()
	expectedMean := 20  // middle of [10,30]: the range offset must be taken into account
	expectedStddev := 5 // a quarter of the 21 QPs, rounded
	if mean != expectedMean {
		t.Errorf("expected mean %d for QP range [10,30], got %d", expectedMean, mean)
	}
	if stddev != expectedStddev {
		t.Errorf("expected stddev %d for QP range [10,30], got %d", expectedStddev, stddev)
	}
}

func TestParseCacheFilename_Roundtrip(t *testing.T) {
	tests := []struct {
		name          string
		encoder       string
		model         string
		min           float64
		p1            float64
		p5            float64
		p10           float64
		p25           float64
		median        float64
		mean          float64
		hmean         float64
		cacheProfile  string
		originalScore bool
	}{
		{
			name:    "no profile",
			encoder: "libx265",
			mean:    93,
			hmean:   93,
		},
		{
			name:          "original score",
			encoder:       "hevc_nvenc",
			model:         "vmaf_v1.0.16_3d0h",
			hmean:         93,
			originalScore: true,
		},
		{
			name:          "original score and profile",
			encoder:       "libx265",
			model:         "vmaf_v1.0.16_1d5h_2160",
			hmean:         95,
			cacheProfile:  "grainy_90s",
			originalScore: true,
		},
		{
			// no model in the name: the marker follows the encoder
			name:          "original score without model",
			encoder:       "hevc_nvenc",
			hmean:         93,
			originalScore: true,
		},
		{
			name:         "with profile",
			encoder:      "libsvtav1",
			mean:         95,
			hmean:        93,
			cacheProfile: "grainy_90s",
		},
		{
			name:    "all thresholds active",
			encoder: "hevc_nvenc",
			min:     70, p1: 75, p5: 80, p10: 82, p25: 85,
			median: 88, mean: 90, hmean: 93,
		},
		{
			name:    "mixed off values",
			encoder: "libaom-av1",
			min:     VMAFOffValue, p1: 75, p5: VMAFOffValue, p10: 82,
			p25: VMAFOffValue, median: 88, mean: VMAFOffValue, hmean: 93,
		},
		{
			name:    "with model",
			encoder: "libx265",
			model:   "vmaf_v0.6.1neg",
			mean:    93,
			hmean:   93,
		},
		{
			name:         "with model and profile",
			encoder:      "libsvtav1",
			model:        "vmaf_4k_v0.6.1",
			mean:         95,
			hmean:        93,
			cacheProfile: "grainy_90s",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			profile, err := NewVMAFChecker(tt.min, tt.p1, tt.p5, tt.p10, tt.p25, tt.median, tt.hmean, tt.mean)
			if err != nil {
				t.Fatalf("failed to create profile: %v", err)
			}

			filename := computeCacheStatsFileName(tt.encoder, tt.model, tt.originalScore, profile, tt.cacheProfile)
			identity, ok := ParseCacheFilename(filename)
			if !ok {
				t.Fatalf("ParseCacheFilename failed for %s", filename)
			}

			if identity.Encoder != tt.encoder {
				t.Errorf("encoder: want %q, got %q", tt.encoder, identity.Encoder)
			}
			if identity.VMAFModel != tt.model {
				t.Errorf("vmafModel: want %q, got %q", tt.model, identity.VMAFModel)
			}
			if identity.OriginalScore != tt.originalScore {
				t.Errorf("originalScore: want %t, got %t", tt.originalScore, identity.OriginalScore)
			}
			if identity.CacheProfile != tt.cacheProfile {
				t.Errorf("cacheProfile: want %q, got %q", tt.cacheProfile, identity.CacheProfile)
			}

			// Compare thresholds via Thresholds() map
			wantMap := profile.Thresholds()
			gotMap := identity.Profile.Thresholds()
			if len(gotMap) != len(wantMap) {
				t.Errorf("threshold count: want %d, got %d", len(wantMap), len(gotMap))
			}
			for k, wantV := range wantMap {
				gotV, exists := gotMap[k]
				if !exists {
					t.Errorf("missing threshold %s", k)
					continue
				}
				if gotV != wantV {
					t.Errorf("threshold %s: want %v, got %v", k, wantV, gotV)
				}
			}
		})
	}
}

func TestParseCacheFilename_Invalid(t *testing.T) {
	invalidNames := []string{
		"",
		"qphistory_libx265_vmaf-abc.json",
		"qphistory_libx265.json",
		"qphistory_libx265_vmaf-.json",
		"qphistory__vmaf-abc123.json",
		"libx265_vmaf-abc123.json",
		"qphistory_libx265_vmaf-abc123_extra.json",
		"not_a_cache_file.json",
	}

	for _, name := range invalidNames {
		t.Run(name, func(t *testing.T) {
			_, ok := ParseCacheFilename(name)
			if ok {
				t.Errorf("expected ParseCacheFilename(%q) to return ok=false", name)
			}
		})
	}
}

func TestStatsCacheHistory_CacheProfileInFilename(t *testing.T) {
	tmpDir := t.TempDir()
	encoder := &cacheMockEncoder{name: "mockenc", qpMin: 0, qpMax: 51}
	profile, err := NewVMAFChecker(VMAFOffValue, VMAFOffValue, VMAFOffValue, VMAFOffValue,
		VMAFOffValue, VMAFOffValue, VMAFOffValue, 80)
	if err != nil {
		t.Fatalf("failed to create profile: %v", err)
	}

	sch1, err := NewStatsCacheHistory(tmpDir, encoder.name, encoder.qpMin, encoder.qpMax, "", false, profile, "profileA")
	if err != nil {
		t.Fatalf("NewStatsCacheHistory failed: %v", err)
	}
	_, _, err = sch1.AddRun([]int{10, 20})
	if err != nil {
		t.Fatalf("AddRun failed: %v", err)
	}

	sch2, err := NewStatsCacheHistory(tmpDir, encoder.name, encoder.qpMin, encoder.qpMax, "", false, profile, "profileB")
	if err != nil {
		t.Fatalf("NewStatsCacheHistory failed: %v", err)
	}
	_, _, err = sch2.AddRun([]int{30, 40})
	if err != nil {
		t.Fatalf("AddRun failed: %v", err)
	}

	// The two caches should be isolated
	mean1, _ := sch1.GetMeanStdDev()
	mean2, _ := sch2.GetMeanStdDev()
	if mean1 == mean2 {
		t.Errorf("expected different means for different profiles, got %d and %d", mean1, mean2)
	}

	// Verify both files exist
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		t.Fatalf("failed to read dir: %v", err)
	}
	fileCount := 0
	for _, entry := range entries {
		if !entry.IsDir() && filepath.Ext(entry.Name()) == ".json" {
			fileCount++
		}
	}
	if fileCount != 2 {
		t.Errorf("expected 2 cache files, got %d", fileCount)
	}
}

func TestParseCacheFilename_BackwardCompatibility(t *testing.T) {
	// Old cache files without a model should still parse correctly
	profile, err := NewVMAFChecker(VMAFOffValue, VMAFOffValue, VMAFOffValue, VMAFOffValue,
		VMAFOffValue, VMAFOffValue, VMAFOffValue, 80)
	if err != nil {
		t.Fatalf("failed to create profile: %v", err)
	}

	tests := []struct {
		name         string
		encoder      string
		cacheProfile string
	}{
		{"no profile", "libx265", ""},
		{"with profile", "libsvtav1", "grainy_90s"},
		{"encoder with underscore", "hevc_nvenc", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			filename := computeCacheStatsFileName(tt.encoder, "", false, profile, tt.cacheProfile)
			identity, ok := ParseCacheFilename(filename)
			if !ok {
				t.Fatalf("ParseCacheFilename failed for %s", filename)
			}
			if identity.Encoder != tt.encoder {
				t.Errorf("encoder: want %q, got %q", tt.encoder, identity.Encoder)
			}
			if identity.VMAFModel != "" {
				t.Errorf("vmafModel: want empty, got %q", identity.VMAFModel)
			}
			if identity.CacheProfile != tt.cacheProfile {
				t.Errorf("cacheProfile: want %q, got %q", tt.cacheProfile, identity.CacheProfile)
			}
		})
	}
}

// TestStatsCacheHistory_AddRunSingleQP ensures a single segment run (single scene
// input) can be saved: the sample standard deviation of one value is NaN, which
// JSON can not encode. The cold start step (a quarter of the QP range) is recorded instead.
func TestStatsCacheHistory_AddRunSingleQP(t *testing.T) {
	tmpDir := t.TempDir()
	encoder := &cacheMockEncoder{name: "mockenc", qpMin: 0, qpMax: 51}
	profile, err := NewVMAFChecker(VMAFOffValue, VMAFOffValue, VMAFOffValue, VMAFOffValue,
		VMAFOffValue, VMAFOffValue, VMAFOffValue, 80)
	if err != nil {
		t.Fatalf("failed to create profile: %v", err)
	}

	sch, err := NewStatsCacheHistory(tmpDir, encoder.name, encoder.qpMin, encoder.qpMax, "", false, profile, "")
	if err != nil {
		t.Fatalf("NewStatsCacheHistory failed: %v", err)
	}

	mean, stddev, err := sch.AddRun([]int{36})
	if err != nil {
		t.Fatalf("AddRun failed: %v", err)
	}
	if mean != 36 || stddev != 13 {
		t.Errorf("expected mean 36 and stddev 13, got %v/%v", mean, stddev)
	}

	// The saved file must be loadable
	runs, err := LoadRunStats(sch.GetPath())
	if err != nil {
		t.Fatalf("LoadRunStats failed: %v", err)
	}
	if len(runs) != 1 || runs[0].Mean != 36 || runs[0].StdDev != 13 || runs[0].Weight != 1 {
		t.Errorf("unexpected saved runs: %+v", runs)
	}
}

// TestRemoveCacheFile ensures the cache file is removed along with its lock file,
// and that a cache file locked by someone else is left untouched.
func TestRemoveCacheFile(t *testing.T) {
	tmpDir := t.TempDir()
	encoder := &cacheMockEncoder{name: "mockenc", qpMin: 0, qpMax: 51}
	profile, err := NewVMAFChecker(VMAFOffValue, VMAFOffValue, VMAFOffValue, VMAFOffValue,
		VMAFOffValue, VMAFOffValue, VMAFOffValue, 80)
	if err != nil {
		t.Fatalf("failed to create profile: %v", err)
	}
	sch, err := NewStatsCacheHistory(tmpDir, encoder.name, encoder.qpMin, encoder.qpMax, "", false, profile, "")
	if err != nil {
		t.Fatalf("NewStatsCacheHistory failed: %v", err)
	}
	if _, _, err = sch.AddRun([]int{10, 12, 14, 16}); err != nil {
		t.Fatalf("AddRun failed: %v", err)
	}
	lockPath := sch.GetPath() + cacheLockExtension
	if _, err = os.Stat(lockPath); err != nil {
		t.Fatalf("expected a lock file after AddRun: %v", err)
	}

	// Locked by someone else: nothing must be removed
	otherLock := flock.New(lockPath)
	if locked, err := otherLock.TryLock(); err != nil || !locked {
		t.Fatalf("failed to take the lock: locked=%t err=%v", locked, err)
	}
	if err = RemoveCacheFile(sch.GetPath()); !errors.Is(err, ErrCacheFileBusy) {
		t.Errorf("expected ErrCacheFileBusy, got %v", err)
	}
	if _, err = os.Stat(sch.GetPath()); err != nil {
		t.Errorf("cache file should still exist: %v", err)
	}
	if err = otherLock.Unlock(); err != nil {
		t.Fatalf("failed to release the lock: %v", err)
	}

	// Free: both files must go
	if err = RemoveCacheFile(sch.GetPath()); err != nil {
		t.Fatalf("RemoveCacheFile failed: %v", err)
	}
	for _, path := range []string{sch.GetPath(), lockPath} {
		if _, err = os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s should have been removed (stat err: %v)", filepath.Base(path), err)
		}
	}
}
