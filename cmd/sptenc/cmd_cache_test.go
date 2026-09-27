package main

import (
	"fmt"
	"io/fs"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hekmon/sptenc/core"
)

func mustVMAFChecker(t *testing.T, hmean, mean float64) core.VMAFChecker {
	t.Helper()
	off := float64(core.VMAFOffValue)
	vc, err := core.NewVMAFChecker(off, off, off, off, off, off, hmean, mean)
	if err != nil {
		t.Fatalf("NewVMAFChecker: %v", err)
	}
	return vc
}

// writeCacheFile creates a cache file the way an encode does, so its name is the real one.
func writeCacheFile(t *testing.T, dir, encoder, vmafModel string, originalScore bool, cacheProfile string, profile core.VMAFChecker) string {
	t.Helper()
	sch, err := core.NewStatsCacheHistory(dir, encoder, 0, 51, vmafModel, originalScore, profile, cacheProfile)
	if err != nil {
		t.Fatalf("NewStatsCacheHistory: %v", err)
	}
	if _, _, err = sch.AddRun([]int{20, 24, 28}); err != nil {
		t.Fatalf("AddRun: %v", err)
	}
	return sch.GetPath()
}

// entryKeys describes the entries in listing order, index included.
func entryKeys(entries []cacheEntry) []string {
	keys := make([]string, len(entries))
	for i, e := range entries {
		keys[i] = fmt.Sprintf("#%d %s unreadable=%t", e.index, describeCacheIdentity(e.identity), e.readErr != nil)
	}
	return keys
}

func TestLoadCacheEntriesOrderIgnoresModificationTime(t *testing.T) {
	dir := t.TempDir()
	const v0 = "vmaf_v0.6.1"
	for _, f := range []struct {
		encoder, vmafModel, cacheProfile string
		originalScore                    bool
		hmean, mean                      float64
		unreadable                       bool
	}{
		{encoder: "libx265", vmafModel: v0, cacheProfile: "grainy_90s", hmean: 95, mean: -1},
		{encoder: "libx265", vmafModel: v0, hmean: 93, mean: 80},
		{encoder: "libx265", vmafModel: v0, hmean: 100, mean: -1},
		{encoder: "libx265", vmafModel: v0, hmean: 93, mean: -1},
		// Named between readable ones, so the sort meets it on both sides of a comparison
		{encoder: "libx265", vmafModel: v0, hmean: 90, mean: -1, unreadable: true},
		// Only the model tells these apart from the v0 ones: without it they would sort by threshold
		{encoder: "libx265", vmafModel: "vmaf_4k_v0.6.1", hmean: 95, mean: -1},
		{encoder: "libx265", vmafModel: "", hmean: 97, mean: -1}, // files from before the model was in the name
		{encoder: "hevc_videotoolbox", vmafModel: v0, hmean: 93, mean: -1},
		// The original score of a model comes after its fidelity, whatever the thresholds
		{encoder: "hevc_videotoolbox", vmafModel: "vmaf_v1.0.16_3d0h", originalScore: true, hmean: 90, mean: -1},
		{encoder: "hevc_videotoolbox", vmafModel: "vmaf_v1.0.16_3d0h", hmean: 95, mean: -1},
		{encoder: "hevc_nvenc", vmafModel: v0, hmean: 93, mean: -1, unreadable: true},
	} {
		path := writeCacheFile(t, dir, f.encoder, f.vmafModel, f.originalScore, f.cacheProfile, mustVMAFChecker(t, f.hmean, f.mean))
		if f.unreadable {
			// Truncated, as a crash in the middle of a write would leave it
			if err := os.WriteFile(path, []byte(`[{"mean": 30,`), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	want := []string{
		"#1 hevc_videotoolbox / vmaf_v0.6.1 / hmean=93 unreadable=false",
		"#2 hevc_videotoolbox / vmaf_v1.0.16_3d0h / hmean=95 unreadable=false",
		"#3 hevc_videotoolbox / vmaf_v1.0.16_3d0h / original score / hmean=90 unreadable=false",
		"#4 libx265 / hmean=97 unreadable=false",
		"#5 libx265 / vmaf_4k_v0.6.1 / hmean=95 unreadable=false",
		"#6 libx265 / vmaf_v0.6.1 / hmean=93 unreadable=false",
		// Numeric, not lexical: 100 comes after 93
		"#7 libx265 / vmaf_v0.6.1 / hmean=100 unreadable=false",
		// An inactive threshold comes before any value
		"#8 libx265 / vmaf_v0.6.1 / mean=80, hmean=93 unreadable=false",
		"#9 libx265 / vmaf_v0.6.1 / profile \"grainy_90s\" / hmean=95 unreadable=false",
		// Unreadable ones last, whatever their identity, sorted the same way among themselves
		"#10 hevc_nvenc / vmaf_v0.6.1 / hmean=93 unreadable=true",
		"#11 libx265 / vmaf_v0.6.1 / hmean=90 unreadable=true",
	}

	entries, err := loadCacheEntries(dir)
	if err != nil {
		t.Fatalf("loadCacheEntries: %v", err)
	}
	if got := entryKeys(entries); !slices.Equal(got, want) {
		t.Fatalf("first listing:\n got %q\nwant %q", got, want)
	}

	// An encode adding a run to a cache file rewrites it: its modification time must not move
	// any index. Make the entries listed last the most recently written, the opposite of what a
	// newest first order would show.
	base := time.Now().Add(-time.Hour)
	for i, e := range entries {
		mtime := base.Add(time.Duration(i) * time.Minute)
		if err := os.Chtimes(e.filename, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
	entries, err = loadCacheEntries(dir)
	if err != nil {
		t.Fatalf("loadCacheEntries: %v", err)
	}
	if got := entryKeys(entries); !slices.Equal(got, want) {
		t.Fatalf("listing after the modification times changed:\n got %q\nwant %q", got, want)
	}
	for _, e := range entries[9:] {
		if e.readErr == nil || strings.Contains(errorWithoutPath(e.readErr), dir) {
			t.Fatalf("unreadable entry #%d error = %v, want a JSON error without the path", e.index, e.readErr)
		}
	}
}

// TestLoadCacheEntriesLargeCache checks the order past 12 entries, where slices.SortFunc switches
// from insertion sort to pdqsort: a comparator that is not a consistent order can get through the
// first and not the second.
func TestLoadCacheEntriesLargeCache(t *testing.T) {
	dir := t.TempDir()
	var readable, unreadable []string
	for hmean := 60; hmean < 100; hmean++ {
		path := writeCacheFile(t, dir, "libx265", "vmaf_v0.6.1", false, "", mustVMAFChecker(t, float64(hmean), -1))
		key := fmt.Sprintf("libx265 / vmaf_v0.6.1 / hmean=%d unreadable=%t", hmean, hmean%7 == 0)
		if hmean%7 != 0 {
			readable = append(readable, key)
			continue
		}
		unreadable = append(unreadable, key)
		if err := os.WriteFile(path, []byte(`not json`), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var want []string
	for i, key := range append(readable, unreadable...) {
		want = append(want, fmt.Sprintf("#%d %s", i+1, key))
	}
	entries, err := loadCacheEntries(dir)
	if err != nil {
		t.Fatalf("loadCacheEntries: %v", err)
	}
	if got := entryKeys(entries); !slices.Equal(got, want) {
		t.Fatalf("listing:\n got %q\nwant %q", got, want)
	}
}

func TestLoadCacheEntriesMissingDir(t *testing.T) {
	entries, err := loadCacheEntries(t.TempDir() + "/missing")
	if err != nil || len(entries) != 0 {
		t.Fatalf("loadCacheEntries on a missing directory = %v, %v; want no entry and no error", entries, err)
	}
}

func TestSelectCacheEntries(t *testing.T) {
	entries := make([]cacheEntry, 4)
	for i := range entries {
		entries[i].index = i + 1
	}
	tests := []struct {
		name    string
		entries []cacheEntry
		indexes []string
		want    []int
		wantErr string
	}{
		{name: "no index selects all", entries: entries, want: []int{1, 2, 3, 4}},
		{name: "single index", entries: entries, indexes: []string{"2"}, want: []int{2}},
		{name: "listing order, each once", entries: entries, indexes: []string{"4", "1", "4"}, want: []int{1, 4}},
		{name: "zero", entries: entries, indexes: []string{"0"}, wantErr: "no cache entry #0: indexes go from 1 to 4"},
		{name: "past the end", entries: entries, indexes: []string{"5"}, wantErr: "no cache entry #5: indexes go from 1 to 4"},
		{name: "negative", entries: entries, indexes: []string{"-1"}, wantErr: "no cache entry #-1"},
		{name: "not a number", entries: entries, indexes: []string{"libx265"}, wantErr: `invalid cache index "libx265"`},
		{name: "one invalid among valid ones selects nothing", entries: entries, indexes: []string{"1", "9", "2"}, wantErr: "no cache entry #9"},
		{name: "empty cache", entries: nil, indexes: []string{"1"}, wantErr: "the cache is empty"},
		{name: "empty cache without index", entries: nil, want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := selectCacheEntries(tt.entries, tt.indexes)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want one containing %q", err, tt.wantErr)
				}
				if got != nil {
					t.Fatalf("got %d entries along with the error, want none", len(got))
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			var gotIndexes []int
			for _, e := range got {
				gotIndexes = append(gotIndexes, e.index)
			}
			if !slices.Equal(gotIndexes, tt.want) {
				t.Fatalf("selected %v, want %v", gotIndexes, tt.want)
			}
		})
	}
}

func TestErrorWithoutPath(t *testing.T) {
	path := "/cache/qphistory_libx265.model~vmaf_v0.6.1_vmaf-LTF8LTF8LTF8LTF8LTF8LTF8OTN8LTE.json.lock"
	err := fmt.Errorf("failed to lock cache file: %w", &fs.PathError{Op: "open", Path: path, Err: fs.ErrPermission})
	if got, want := errorWithoutPath(err), "failed to lock cache file: open: permission denied"; got != want {
		t.Fatalf("errorWithoutPath = %q, want %q", got, want)
	}
	if got, want := errorWithoutPath(core.ErrCacheFileBusy), core.ErrCacheFileBusy.Error(); got != want {
		t.Fatalf("errorWithoutPath = %q, want %q", got, want)
	}
}

func TestCacheScore(t *testing.T) {
	for _, tc := range []struct {
		identity core.CacheFileIdentity
		want     string
	}{
		{core.CacheFileIdentity{VMAFModel: "vmaf_v1.0.16_3d0h"}, "fidelity"},
		{core.CacheFileIdentity{VMAFModel: "vmaf_v1.0.16_3d0h", OriginalScore: true}, "original"},
		// a v0 model has a single score, its fidelity score
		{core.CacheFileIdentity{VMAFModel: "vmaf_v0.6.1"}, "fidelity"},
		// named before the model was: nothing to tell
		{core.CacheFileIdentity{}, "-"},
	} {
		if got := cacheScore(tc.identity); got != tc.want {
			t.Errorf("%+v: want %q, got %q", tc.identity, tc.want, got)
		}
	}
}
