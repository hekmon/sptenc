package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"

	"github.com/hekmon/ffmpegutils"
)

type mediansQPs []int

func (mqp *mediansQPs) AddIdealQPs(qps []int) {
	// Copy the results list before sorting it
	results := make([]int, len(qps))
	copy(results, qps)
	sort.Ints(results)
	// Extract the median
	median := results[int(math.Round(float64(len(results))/2))]
	// Add it to the list
	*mqp = append(*mqp, median)
}

func (mqp mediansQPs) GetIdealSplitQP() int {
	if len(mqp) == 0 {
		return (ffmpegutils.QPMaximum - ffmpegutils.QPMinimum + 1) / 2
	}
	sum := 0
	for _, qp := range mqp {
		sum += qp
	}
	return int(math.Round(float64(sum) / float64(len(mqp))))
}

const (
	qpstatsFormat = "gop_qp_medians_%s.json"
)

var (
	previousMediansQPs mediansQPs
)

func computeSplitQPFile() string {
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

func loadIdealQPs() (err error) {
	fd, err := os.Open(computeSplitQPFile())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			previousMediansQPs = make(mediansQPs, 0, 1)
			err = nil
		}
		return
	}
	defer fd.Close()
	return json.NewDecoder(fd).Decode(&previousMediansQPs)
}

func saveIdealQPs() error {
	// Create or truncate file
	fd, err := os.Create(computeSplitQPFile())
	if err != nil {
		return err
	}
	defer fd.Close()
	// Make it human readable
	enc := json.NewEncoder(fd)
	enc.SetIndent("", "  ")
	// Dump data
	return enc.Encode(previousMediansQPs)
}
