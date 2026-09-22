package core

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/gofrs/flock"
	"gonum.org/v1/gonum/stat"
)

// StatsCache provides statistical guidance for the QP search start point.
type StatsCache interface {
	GetMeanStdDev() (mean, stddev int)
	// Snapshot returns the historical aggregate as one mean and stddev, for another cache to
	// seed from (see NewEphemeralStatsCache). ok=false when no history exists (empty
	// persistent cache).
	Snapshot() (mean, stddev float64, ok bool)
}

// NewStatsCacheHistory initializes a stats cache for the given encoder name, QP range, VMAF model,
// VMAF profile, and optional user-provided cache profile. It loads any existing cache from disk or
// starts with an empty history.
func NewStatsCacheHistory(dir string, encoderName string, qpMin, qpMax int, vmafModel string, profile VMAFChecker, cacheProfile string) (
	sch *StatsCacheHistory, err error) {
	sch = &StatsCacheHistory{
		path:  filepath.Join(dir, computeCacheStatsFileName(encoderName, vmafModel, profile, cacheProfile)),
		qpMin: qpMin,
		qpMax: qpMax,
	}
	err = sch.loadStats()
	return
}

// StatsCacheHistory persists and aggregates QP statistics across encoding runs
// for a specific encoder and VMAF profile.
type StatsCacheHistory struct {
	path   string
	stats  []RunStats
	qpMin  int
	qpMax  int
	access sync.RWMutex
}

// RunStats represents the statistical summary of a single encoding run.
type RunStats struct {
	Mean   float64 `json:"mean"`
	StdDev float64 `json:"stddev"`
	Weight int     `json:"weight"`
}

// GetMeanStdDev returns the weighted mean and standard deviation of all recorded runs.
// If no runs have been recorded, it returns a heuristic estimate based on the encoder's QP range.
func (sch *StatsCacheHistory) GetMeanStdDev() (mean, stddev int) {
	sch.access.RLock()
	defer sch.access.RUnlock()
	// If we do not have stats yet, set data like a quick sort/search
	if len(sch.stats) == 0 {
		meanf, stddevf := coldStartStats(sch.qpMin, sch.qpMax)
		return int(math.Round(meanf)), max(1, int(math.Round(stddevf)))
	}
	meanf, stddevf, _, _ := sch.computeWeightedAggregate()
	// Round to nearest integers - trust the statistics
	mean = int(math.Round(meanf))
	stddev = max(1, int(math.Round(stddevf)))
	return
}

// Snapshot returns the historical aggregate (runs weighted by their number of segments) as
// one mean and stddev. ok=false when no history exists.
func (sch *StatsCacheHistory) Snapshot() (mean, stddev float64, ok bool) {
	sch.access.RLock()
	defer sch.access.RUnlock()
	if len(sch.stats) == 0 {
		return 0, 0, false
	}
	mean, stddev, _, _ = sch.computeWeightedAggregate()
	return mean, stddev, true
}

// computeWeightedAggregate computes the weighted mean and stddev of all recorded runs
// and returns the total weight. Caller must hold sch.access.RLock.
func (sch *StatsCacheHistory) computeWeightedAggregate() (mean, stddev float64, weight int, totalWeight float64) {
	means := make([]float64, len(sch.stats))
	stddevs := make([]float64, len(sch.stats))
	weights := make([]float64, len(sch.stats))
	for index, rs := range sch.stats {
		means[index] = rs.Mean
		stddevs[index] = rs.StdDev
		weights[index] = float64(rs.Weight)
		weight += rs.Weight
	}
	mean = stat.Mean(means, weights)
	stddev = stat.Mean(stddevs, weights)
	totalWeight = float64(weight)
	return
}

// GetPath returns the file path used to persist the cache.
func (sch *StatsCacheHistory) GetPath() string {
	return sch.path
}

// AddRun records a new set of QP values and returns their mean and standard deviation.
// Duplicate runs (same mean, stddev, and weight) are ignored.
func (sch *StatsCacheHistory) AddRun(qps []int) (mean, stddev float64, err error) {
	sch.access.Lock()
	defer sch.access.Unlock()
	// Cross-process lock: block until available so concurrent sptenc instances
	// don't lose updates. If the OS/filesystem doesn't support locking, return
	// the error so the user is informed the cache is unprotected.
	fileLock := flock.New(sch.path + cacheLockExtension)
	if err = fileLock.Lock(); err != nil {
		err = fmt.Errorf("failed to lock cache file: %w", err)
		return
	}
	defer fileLock.Unlock()
	// Load last version of the cached file (another encode may have added a run since)
	if err = sch.loadStats(); err != nil {
		err = fmt.Errorf("failed to reload stats from disk: %w", err)
		return
	}
	// Convert to float64
	qpf := make([]float64, len(qps))
	for i, q := range qps {
		qpf[i] = float64(q)
	}
	// Prepare the stats object
	if len(qpf) == 1 {
		// The sample standard deviation of a single value is NaN, which can not be serialized
		// to JSON. A single segment tells where the center is but nothing about the spread:
		// record the same step a cold start would use (see coldStartStats). Recording 0 would
		// make the next search crawl one QP at a time if this run is alone in the cache.
		mean = qpf[0]
		_, stddev = coldStartStats(sch.qpMin, sch.qpMax)
	} else {
		mean, stddev = stat.MeanStdDev(qpf, nil)
	}
	rs := RunStats{
		Mean:   mean,
		StdDev: stddev,
		Weight: len(qps),
	}
	// Avoid the encoding done multiple times to impact the stats.
	// Float equality is intentional here: identical inputs produce identical IEEE-754
	// results from stat.MeanStdDev in pure Go, so exact match reliably catches re-runs
	// of the same encode without risking false positives from near-duplicates.
	if slices.Contains(sch.stats, rs) {
		return
	}
	// Add it to the list
	sch.stats = append(sch.stats, rs)
	// Save it to disk
	if err = sch.saveStats(); err != nil {
		err = fmt.Errorf("failed to save stats to disk: %w", err)
	}
	return
}

// LoadRunStats reads a cache file from disk and returns the decoded run statistics.
func LoadRunStats(path string) ([]RunStats, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var runs []RunStats
	if err := json.Unmarshal(data, &runs); err != nil {
		return nil, err
	}
	return runs, nil
}

// LoadStats reads the cache from disk. A missing file is treated as an empty cache.
func (sch *StatsCacheHistory) loadStats() (err error) {
	fd, err := os.Open(sch.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			sch.stats = make([]RunStats, 0, 1)
			err = nil
		}
		return
	}
	defer fd.Close()
	return json.NewDecoder(fd).Decode(&sch.stats)
}

// SaveStats writes the current cache to disk as indented JSON atomically.
func (sch *StatsCacheHistory) saveStats() error {
	tmpPath := sch.path + ".tmp"
	fd, err := os.Create(tmpPath)
	if err != nil {
		return err
	}
	// Make it human readable
	enc := json.NewEncoder(fd)
	enc.SetIndent("", "  ")
	// Dump data
	if err := enc.Encode(sch.stats); err != nil {
		fd.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := fd.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return os.Rename(tmpPath, sch.path)
}

// CacheFileIdentity holds the decoded components of a cache filename.
type CacheFileIdentity struct {
	Encoder      string
	VMAFModel    string
	Profile      VMAFChecker
	CacheProfile string
}

const (
	CacheFilePrefix        = "qphistory_"
	cacheFileMarker        = "_vmaf-"
	cacheFileModelMarker   = ".model~"
	cacheFileVMAFSeparator = "|"
	CacheFileExtension     = ".json"
	cacheLockExtension     = ".lock"
)

// ErrCacheFileBusy is returned by RemoveCacheFile when another process holds the cache file lock.
var ErrCacheFileBusy = errors.New("cache file is currently locked by another sptenc process")

// RemoveCacheFile deletes a cache file along with its companion lock file.
//
// # WHY NOT A PLAIN os.Remove
//
// A cache file comes with a lock file (see AddRun) that a plain removal leaves behind forever.
// Removing the lock file as well is not enough: another sptenc may be inside AddRun right now,
// holding that lock. Deleting the lock file under it breaks the mutual exclusion (the next
// process creates a new lock file and both believe they own the cache), and deleting the cache
// file under it is pointless as it is about to be written back. So the lock is taken first,
// without waiting: a busy cache file is reported with ErrCacheFileBusy and left untouched.
//
// # WHY THE LOCK IS RELEASED BEFORE REMOVING THE LOCK FILE
//
// Windows refuses to delete a file we still hold a handle on. This leaves a tiny window where
// another process can take the lock on a file about to be removed: the worst outcome is one
// lost cache update, which is acceptable for statistics.
func RemoveCacheFile(path string) (err error) {
	lockPath := path + cacheLockExtension
	fileLock := flock.New(lockPath)
	locked, err := fileLock.TryLock()
	if err != nil {
		return fmt.Errorf("failed to lock cache file: %w", err)
	}
	if !locked {
		return ErrCacheFileBusy
	}
	err = os.Remove(path)
	if unlockErr := fileLock.Unlock(); unlockErr != nil && err == nil {
		err = fmt.Errorf("failed to unlock cache file: %w", unlockErr)
	}
	if err != nil {
		return
	}
	if err = os.Remove(lockPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("failed to remove the lock file: %w", err)
	}
	return nil
}

// ParseCacheFilename parses a cache filename into its components.
// It returns ok=false if the filename does not match the expected format.
func ParseCacheFilename(name string) (identity CacheFileIdentity, ok bool) {
	// Step 1 - check and remove extension
	if !strings.HasSuffix(name, CacheFileExtension) {
		return
	}
	name = strings.TrimSuffix(name, CacheFileExtension)
	// Step 2 - check and remove prefix
	if !strings.HasPrefix(name, CacheFilePrefix) {
		return
	}
	name = strings.TrimPrefix(name, CacheFilePrefix)
	// Step 3 - check and remove marker to extract encoder and the rest
	var rest string
	beforeMarker, rest, ok := strings.Cut(name, cacheFileMarker)
	if !ok || rest == "" {
		ok = false
		return
	}
	// Extract optional model from beforeMarker
	if modelIdx := strings.LastIndex(beforeMarker, cacheFileModelMarker); modelIdx != -1 {
		identity.Encoder = beforeMarker[:modelIdx]
		identity.VMAFModel = beforeMarker[modelIdx+len(cacheFileModelMarker):]
	} else {
		identity.Encoder = beforeMarker
	}
	// Try whole remainder as vmaf blob (no profile).
	if decoded, err := base64.RawURLEncoding.DecodeString(rest); err == nil {
		if identity.Profile, ok = parseVMAFBlob(string(decoded)); ok {
			return
		}
	}
	// Possible profile: split on each underscore from right to left.
	for i := len(rest) - 1; i >= 0; i-- {
		if rest[i] == '_' {
			left := rest[:i]
			right := rest[i+1:]
			decodedLeft, err := base64.RawURLEncoding.DecodeString(left)
			if err != nil {
				continue
			}
			profile, profileOK := parseVMAFBlob(string(decodedLeft))
			if !profileOK {
				continue
			}
			decodedRight, err := base64.RawURLEncoding.DecodeString(right)
			if err != nil {
				continue
			}
			identity.Profile = profile
			identity.CacheProfile = string(decodedRight)
			ok = true
			return
		}
	}
	return
}

// parseVMAFBlob turns "min|p1|p5|p10|p25|median|hmean|mean" into a VMAFChecker.
func parseVMAFBlob(blob string) (VMAFChecker, bool) {
	parts := strings.Split(blob, cacheFileVMAFSeparator)
	if len(parts) != 8 {
		return VMAFChecker{}, false
	}
	values := make([]float64, 8)
	for i, p := range parts {
		v, err := strconv.ParseFloat(p, 64)
		if err != nil {
			return VMAFChecker{}, false
		}
		values[i] = v
	}
	vc, err := NewVMAFChecker(values[0], values[1], values[2], values[3],
		values[4], values[5], values[6], values[7])
	if err != nil {
		return VMAFChecker{}, false
	}
	return vc, true
}

func computeCacheStatsFileName(encoderName string, vmafModel string, profile VMAFChecker, cacheProfile string) (filename string) {
	filename = CacheFilePrefix + encoderName
	if vmafModel != "" {
		filename += cacheFileModelMarker + vmafModel
	}
	filename += cacheFileMarker
	// vmaf profile
	var builder bytes.Buffer
	builder.WriteString(strconv.FormatFloat(profile.min, 'f', -1, 64))
	builder.WriteString(cacheFileVMAFSeparator)
	builder.WriteString(strconv.FormatFloat(profile.p1, 'f', -1, 64))
	builder.WriteString(cacheFileVMAFSeparator)
	builder.WriteString(strconv.FormatFloat(profile.p5, 'f', -1, 64))
	builder.WriteString(cacheFileVMAFSeparator)
	builder.WriteString(strconv.FormatFloat(profile.p10, 'f', -1, 64))
	builder.WriteString(cacheFileVMAFSeparator)
	builder.WriteString(strconv.FormatFloat(profile.p25, 'f', -1, 64))
	builder.WriteString(cacheFileVMAFSeparator)
	builder.WriteString(strconv.FormatFloat(profile.median, 'f', -1, 64))
	builder.WriteString(cacheFileVMAFSeparator)
	builder.WriteString(strconv.FormatFloat(profile.hmean, 'f', -1, 64))
	builder.WriteString(cacheFileVMAFSeparator)
	builder.WriteString(strconv.FormatFloat(profile.mean, 'f', -1, 64))
	filename += base64.RawURLEncoding.EncodeToString(builder.Bytes())
	// isolated cache profile
	if cacheProfile != "" {
		filename += "_" + base64.RawURLEncoding.EncodeToString([]byte(cacheProfile))
	}
	return filename + CacheFileExtension
}
