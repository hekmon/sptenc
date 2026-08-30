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

	"github.com/hekmon/sptenc/ng/ffmpeg"

	"gonum.org/v1/gonum/stat"
)

// NewStatsCacheHistory initializes a stats cache for the given encoder and VMAF profile.
// It loads any existing cache from disk or starts with an empty history.
func NewStatsCacheHistory(dir string, encoder ffmpeg.Encoder, profile VMAFChecker) (sch *StatsCacheHistory, err error) {
	sch = &StatsCacheHistory{
		path: filepath.Join(dir, computeCacheStatsFileName(encoder, profile)),
	}
	var found bool
	if sch.qpMin, sch.qpMax, found = ffmpeg.GetEncoderQPRange(encoder); !found {
		err = fmt.Errorf("unsupported encoder %s", encoder)
		return
	}
	err = sch.LoadStats()
	return
}

// StatsCacheHistory persists and aggregates QP statistics across encoding runs
// for a specific encoder and VMAF profile.
type StatsCacheHistory struct {
	path  string
	stats []runStats
	qpMin int
	qpMax int
}

// runStats represents the statistical summary of a single encoding run.
type runStats struct {
	Mean   float64 `json:"mean"`
	StdDev float64 `json:"stddev"`
	Weight int     `json:"weight"`
}

// GetPath returns the file path used to persist the cache.
func (sch *StatsCacheHistory) GetPath() string {
	return sch.path
}

// LoadStats reads the cache from disk. A missing file is treated as an empty cache.
func (sch *StatsCacheHistory) LoadStats() (err error) {
	fd, err := os.Open(sch.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			sch.stats = make([]runStats, 0, 1)
			err = nil
		}
		return
	}
	defer fd.Close()
	return json.NewDecoder(fd).Decode(&sch.stats)
}

// SaveStats writes the current cache to disk as indented JSON.
func (sch *StatsCacheHistory) SaveStats() error {
	// Create or truncate file
	fd, err := os.Create(sch.path)
	if err != nil {
		return err
	}
	defer fd.Close()
	// Make it human readable
	enc := json.NewEncoder(fd)
	enc.SetIndent("", "  ")
	// Dump data
	return enc.Encode(sch.stats)
}

// AddRun records a new set of QP values and returns their mean and standard deviation.
// Duplicate runs (same mean, stddev, and weight) are ignored.
func (sch *StatsCacheHistory) AddRun(qps []int) (mean, stddev float64) {
	// Convert to float64
	qpf := make([]float64, len(qps))
	for i, q := range qps {
		qpf[i] = float64(q)
	}
	// Prepare the stats object
	mean, stddev = stat.MeanStdDev(qpf, nil)
	rs := runStats{
		Mean:   mean,
		StdDev: stddev,
		Weight: len(qps),
	}
	// Avoid the encoding done multiple times to impact the stats
	if slices.Contains(sch.stats, rs) {
		return
	}
	// Add it to the list
	sch.stats = append(sch.stats, rs)
	return
}

// GetMeanStdDev returns the weighted mean and standard deviation of all recorded runs.
// If no runs have been recorded, it returns a heuristic estimate based on the encoder's QP range.
func (sch *StatsCacheHistory) GetMeanStdDev() (mean, stddev int) {
	// If we do not have stats yet, set data like a quick sort/search
	if len(sch.stats) == 0 {
		mean = (sch.qpMax - sch.qpMin + 1) / 2
		stddev = mean / 2
		return
	}
	// Extract data
	means := make([]float64, len(sch.stats))
	stddevs := make([]float64, len(sch.stats))
	weights := make([]float64, len(sch.stats))
	for index, rs := range sch.stats {
		means[index] = rs.Mean
		stddevs[index] = rs.StdDev
		weights[index] = float64(rs.Weight)
	}
	// Compute averages
	meanf := stat.Mean(means, weights)
	stddevf := stat.Mean(stddevs, weights)
	// Round to nearest integers - trust the statistics
	mean = int(math.Round(meanf))
	stddev = max(1, int(math.Round(stddevf)))
	return
}

func computeCacheStatsFileName(encoder ffmpeg.Encoder, profile VMAFChecker) string {
	var builder bytes.Buffer
	builder.WriteString(string(encoder))
	builder.WriteString(strconv.FormatFloat(profile.min, 'f', -1, 64))
	builder.WriteString(strconv.FormatFloat(profile.p1, 'f', -1, 64))
	builder.WriteString(strconv.FormatFloat(profile.p5, 'f', -1, 64))
	builder.WriteString(strconv.FormatFloat(profile.p10, 'f', -1, 64))
	builder.WriteString(strconv.FormatFloat(profile.p25, 'f', -1, 64))
	builder.WriteString(strconv.FormatFloat(profile.median, 'f', -1, 64))
	builder.WriteString(strconv.FormatFloat(profile.hmean, 'f', -1, 64))
	builder.WriteString(strconv.FormatFloat(profile.mean, 'f', -1, 64))
	return fmt.Sprintf("sptenc_qp_history_%s.json", base64.RawURLEncoding.EncodeToString(builder.Bytes()))
}
