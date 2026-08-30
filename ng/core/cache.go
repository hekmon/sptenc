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

func NewStatsCacheHistory(dir string, encoder ffmpeg.Encoder, profile VMAFStats) (sch *StatsCacheHistory, err error) {
	sch = &StatsCacheHistory{
		path: filepath.Join(dir, computeCacheStatsFileName(encoder, profile)),
	}
	switch encoder {
	case ffmpeg.HEVCEncoderLibx265, ffmpeg.HEVCEncoderNVENC, ffmpeg.HEVCEncoderVAAPI:
		sch.qpMin = ffmpeg.HEVCQPMin
		sch.qpMax = ffmpeg.HEVCQPMax
	case ffmpeg.AV1EncoderLibaom:
		sch.qpMin = ffmpeg.AV1LibaomQPMin
		sch.qpMax = ffmpeg.AV1LibaomQPMax
	case ffmpeg.AV1EncoderNVENC:
		sch.qpMin = ffmpeg.AV1NVENCQPMin
		sch.qpMax = ffmpeg.AV1NVENCQPMax
	case ffmpeg.AV1EncoderVAAPI:
		sch.qpMin = ffmpeg.AV1VAAPIQPMin
		sch.qpMax = ffmpeg.AV1VAAPIQPMax
	default:
		err = fmt.Errorf("unsupported encoder %s", encoder)
		return
	}
	err = sch.LoadStats()
	return
}

type StatsCacheHistory struct {
	path  string
	stats []runStats
	qpMin int
	qpMax int
}

type runStats struct {
	Mean   float64 `json:"mean"`
	StdDev float64 `json:"stddev"`
	Weight int     `json:"weight"`
}

func (sch *StatsCacheHistory) GetPath() string {
	return sch.path
}

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

func computeCacheStatsFileName(encoder ffmpeg.Encoder, profile VMAFStats) string {
	var builder bytes.Buffer
	builder.WriteString(string(encoder))
	builder.WriteString(strconv.FormatFloat(profile.Min, 'f', -1, 64))
	builder.WriteString(strconv.FormatFloat(profile.P1, 'f', -1, 64))
	builder.WriteString(strconv.FormatFloat(profile.P5, 'f', -1, 64))
	builder.WriteString(strconv.FormatFloat(profile.P10, 'f', -1, 64))
	builder.WriteString(strconv.FormatFloat(profile.P25, 'f', -1, 64))
	builder.WriteString(strconv.FormatFloat(profile.Median, 'f', -1, 64))
	builder.WriteString(strconv.FormatFloat(profile.HMean, 'f', -1, 64))
	builder.WriteString(strconv.FormatFloat(profile.Mean, 'f', -1, 64))
	return fmt.Sprintf("sptenc_qp_history_%s.json", base64.RawURLEncoding.EncodeToString(builder.Bytes()))
}
