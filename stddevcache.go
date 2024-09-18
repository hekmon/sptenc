package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"strconv"

	"github.com/hekmon/ffmpegutils"
	"gonum.org/v1/gonum/stat"
)

type RunHistory []RunStats

type RunStats struct {
	Mean   float64
	StdDev float64
	Weight int
}

func (rh *RunHistory) AddRun(qps []int) {
	// Convert to float64
	qpf := make([]float64, len(qps))
	for i, q := range qps {
		qpf[i] = float64(q)
	}
	// Compute mean and stddev
	rs := RunStats{
		Weight: len(qps),
	}
	rs.Mean, rs.StdDev = stat.MeanStdDev(qpf, nil)
	// Add it to the list
	*rh = append(*rh, rs)
}

func (rh *RunHistory) GetMeanStdDev() (mean, stddev int) {
	// If we do not have stats yet, set data like a quick sort/search
	if len(*rh) == 0 {
		mean = (ffmpegutils.QPMaximum - ffmpegutils.QPMinimum + 1) / 2
		stddev = mean / 2
		return
	}
	// Extract data
	means := make([]float64, len(*rh))
	stddevs := make([]float64, len(*rh))
	weights := make([]float64, len(*rh))
	for index, rs := range *rh {
		means[index] = rs.Mean
		stddevs[index] = rs.StdDev
		weights[index] = float64(rs.Weight)
	}
	// Compute averages
	mean = int(math.Round(stat.Mean(means, weights)))
	stddev = int(math.Ceil(stat.Mean(stddevs, weights)))
	return
}

const (
	qpstatsFormat = "stpenc_qp_history_%s.json"
)

var (
	previousRuns RunHistory
)

func computePreviousRunsStatsFile() string {
	var builder bytes.Buffer
	if *nvenc {
		builder.WriteString("nvenc_hevc")
	} else {
		builder.WriteString("libx265")
	}
	builder.WriteString(strconv.FormatFloat(*vmafLimitMin, 'f', -1, 64))
	builder.WriteString(strconv.FormatFloat(*vmafLimitP1, 'f', -1, 64))
	builder.WriteString(strconv.FormatFloat(*vmafLimitP5, 'f', -1, 64))
	builder.WriteString(strconv.FormatFloat(*vmafLimitP10, 'f', -1, 64))
	builder.WriteString(strconv.FormatFloat(*vmafLimitP25, 'f', -1, 64))
	builder.WriteString(strconv.FormatFloat(*vmafLimitMedian, 'f', -1, 64))
	builder.WriteString(strconv.FormatFloat(*vmafLimitHMean, 'f', -1, 64))
	builder.WriteString(strconv.FormatFloat(*vmafLimitMean, 'f', -1, 64))
	return fmt.Sprintf(qpstatsFormat, base64.RawStdEncoding.EncodeToString(builder.Bytes()))
}

func loadStats() (err error) {
	fd, err := os.Open(computePreviousRunsStatsFile())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			previousRuns = make(RunHistory, 0, 1)
			err = nil
		}
		return
	}
	defer fd.Close()
	return json.NewDecoder(fd).Decode(&previousRuns)
}

func saveStats() error {
	// Create or truncate file
	fd, err := os.Create(computePreviousRunsStatsFile())
	if err != nil {
		return err
	}
	defer fd.Close()
	// Make it human readable
	enc := json.NewEncoder(fd)
	enc.SetIndent("", "  ")
	// Dump data
	return enc.Encode(previousRuns)
}
