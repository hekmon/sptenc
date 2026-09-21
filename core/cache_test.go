package core

import (
	"math"
	"os"
	"path/filepath"
	"sync"
	"testing"
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

	sch, err := NewStatsCacheHistory(tmpDir, encoder.name, encoder.qpMin, encoder.qpMax, "", profile, "")
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

	sch, err := NewStatsCacheHistory(tmpDir, encoder.name, encoder.qpMin, encoder.qpMax, "", profile, "")
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

	sch, err := NewStatsCacheHistory(tmpDir, encoder.name, encoder.qpMin, encoder.qpMax, "", profile, "")
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

	sch, err := NewStatsCacheHistory(tmpDir, encoder.name, encoder.qpMin, encoder.qpMax, "", profile, "")
	if err != nil {
		t.Fatalf("NewStatsCacheHistory failed: %v", err)
	}

	mean, stddev := sch.GetMeanStdDev()
	expectedMean := (51 - 0 + 1) / 2   // 26
	expectedStddev := expectedMean / 2 // 13
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

	sch, err := NewStatsCacheHistory(tmpDir, encoder.name, encoder.qpMin, encoder.qpMax, "", profile, "")
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

func TestStatsCacheHistory_SaveLoadRoundtrip(t *testing.T) {
	tmpDir := t.TempDir()
	encoder := &cacheMockEncoder{name: "mockenc", qpMin: 0, qpMax: 51}
	profile, err := NewVMAFChecker(VMAFOffValue, VMAFOffValue, VMAFOffValue, VMAFOffValue,
		VMAFOffValue, VMAFOffValue, VMAFOffValue, 80)
	if err != nil {
		t.Fatalf("failed to create profile: %v", err)
	}

	sch1, err := NewStatsCacheHistory(tmpDir, encoder.name, encoder.qpMin, encoder.qpMax, "", profile, "")
	if err != nil {
		t.Fatalf("NewStatsCacheHistory failed: %v", err)
	}

	qps := []int{10, 15, 20, 25}
	mean1, stddev1, err := sch1.AddRun(qps)
	if err != nil {
		t.Fatalf("AddRun failed: %v", err)
	}

	// Create a new cache pointing to the same file
	sch2, err := NewStatsCacheHistory(tmpDir, encoder.name, encoder.qpMin, encoder.qpMax, "", profile, "")
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

	name1 := computeCacheStatsFileName("mock", "", profile1, "")
	name1again := computeCacheStatsFileName("mock", "", profile1, "")
	if name1 != name1again {
		t.Errorf("same inputs produced different filenames: %s vs %s", name1, name1again)
	}

	name2 := computeCacheStatsFileName("mock", "", profile2, "")
	if name1 == name2 {
		t.Error("different profiles produced the same filename")
	}

	name3 := computeCacheStatsFileName("mock", "", profile1, "profileA")
	if name1 == name3 {
		t.Error("different cache profiles produced the same filename")
	}

	// Model should affect filename
	name4 := computeCacheStatsFileName("mock", "vmaf_v0.6.1neg", profile1, "")
	if name1 == name4 {
		t.Error("different vmaf models produced the same filename")
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

	sch, err := NewStatsCacheHistory(tmpDir, encoder.name, encoder.qpMin, encoder.qpMax, "", profile, "")
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

	sch, err := NewStatsCacheHistory(tmpDir, encoder.name, encoder.qpMin, encoder.qpMax, "", profile, "")
	if err != nil {
		t.Fatalf("NewStatsCacheHistory failed: %v", err)
	}

	// Corrupt the file by writing invalid JSON directly
	if err := os.WriteFile(sch.GetPath(), []byte("not json"), 0644); err != nil {
		t.Fatalf("failed to write corrupt file: %v", err)
	}

	// NewStatsCacheHistory should fail to load corrupted stats
	_, err = NewStatsCacheHistory(tmpDir, encoder.name, encoder.qpMin, encoder.qpMax, "", profile, "")
	if err == nil {
		t.Fatal("expected error loading corrupted cache file, got nil")
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

	sch, err := NewStatsCacheHistory(tmpDir, encoder.name, encoder.qpMin, encoder.qpMax, "", profile, "")
	if err != nil {
		t.Fatalf("NewStatsCacheHistory failed: %v", err)
	}

	mean, stddev := sch.GetMeanStdDev()
	expectedMean := (30 - 10 + 1) / 2  // 10
	expectedStddev := expectedMean / 2 // 5
	if mean != expectedMean {
		t.Errorf("expected mean %d for QP range [10,30], got %d", expectedMean, mean)
	}
	if stddev != expectedStddev {
		t.Errorf("expected stddev %d for QP range [10,30], got %d", expectedStddev, stddev)
	}
}

func TestParseCacheFilename_Roundtrip(t *testing.T) {
	tests := []struct {
		name         string
		encoder      string
		model        string
		min          float64
		p1           float64
		p5           float64
		p10          float64
		p25          float64
		median       float64
		mean         float64
		hmean        float64
		cacheProfile string
	}{
		{
			name:    "no profile",
			encoder: "libx265",
			mean:    93,
			hmean:   93,
		},
		{
			name:         "with profile",
			encoder:      "svtav1",
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
			encoder:      "svtav1",
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

			filename := computeCacheStatsFileName(tt.encoder, tt.model, profile, tt.cacheProfile)
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

	sch1, err := NewStatsCacheHistory(tmpDir, encoder.name, encoder.qpMin, encoder.qpMax, "", profile, "profileA")
	if err != nil {
		t.Fatalf("NewStatsCacheHistory failed: %v", err)
	}
	_, _, err = sch1.AddRun([]int{10, 20})
	if err != nil {
		t.Fatalf("AddRun failed: %v", err)
	}

	sch2, err := NewStatsCacheHistory(tmpDir, encoder.name, encoder.qpMin, encoder.qpMax, "", profile, "profileB")
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
		{"with profile", "svtav1", "grainy_90s"},
		{"encoder with underscore", "hevc_nvenc", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			filename := computeCacheStatsFileName(tt.encoder, "", profile, tt.cacheProfile)
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
