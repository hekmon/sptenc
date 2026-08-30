package core

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/olekukonko/tablewriter"
	"github.com/olekukonko/tablewriter/renderer"
	"github.com/olekukonko/tablewriter/tw"
)

const (
	// VMAFOffValue disables VMAF processing.
	VMAFOffValue = -1
	// VMAFMinValue is the minimum valid VMAF score.
	VMAFMinValue = 0
	// VMAFMaxValue is the maximum valid VMAF score.
	VMAFMaxValue = 100
)

// VMAFStats holds the statistical distribution of VMAF scores for a given video comparison.
type VMAFStats struct {
	Version      string
	Minimum      float64 `json:"min"`
	Percentile1  float64 `json:"p1"`
	Percentile5  float64 `json:"p5"`
	Percentile10 float64 `json:"p10"`
	Percentile25 float64 `json:"p25"`
	Median       float64 `json:"median"`
	HarmonicMean float64 `json:"harmonic_mean"`
	Mean         float64 `json:"mean"`
	Maximum      float64 `json:"max"`
}

// NewVMAFChecker creates a new VMAFChecker with the given thresholds.
// Each threshold can be set to VMAFOffValue to disable that specific check.
// Returns an error if all values are off or if any value is out of the valid VMAF range.
func NewVMAFChecker(min, p1, p5, p10, p25, median, hmean, mean float64) (vc VMAFChecker, err error) {
	if min == VMAFOffValue && p1 == VMAFOffValue && p5 == VMAFOffValue && p10 == VMAFOffValue &&
		p25 == VMAFOffValue && median == VMAFOffValue && hmean == VMAFOffValue && mean == VMAFOffValue {
		err = errors.New("all values are off")
		return
	}
	if (min < VMAFMinValue || min > VMAFMaxValue) && min != VMAFOffValue {
		err = fmt.Errorf("min value %f is out of range [%d, %d]", min, VMAFMinValue, VMAFMaxValue)
		return
	}
	vc.min = min
	if (p1 < VMAFMinValue || p1 > VMAFMaxValue) && p1 != VMAFOffValue {
		err = fmt.Errorf("p1 value %f is out of range [%d, %d]", p1, VMAFMinValue, VMAFMaxValue)
		return
	}
	vc.p1 = p1
	if (p5 < VMAFMinValue || p5 > VMAFMaxValue) && p5 != VMAFOffValue {
		err = fmt.Errorf("p5 value %f is out of range [%d, %d]", p5, VMAFMinValue, VMAFMaxValue)
		return
	}
	vc.p5 = p5
	if (p10 < VMAFMinValue || p10 > VMAFMaxValue) && p10 != VMAFOffValue {
		err = fmt.Errorf("p10 value %f is out of range [%d, %d]", p10, VMAFMinValue, VMAFMaxValue)
		return
	}
	vc.p10 = p10
	if (p25 < VMAFMinValue || p25 > VMAFMaxValue) && p25 != VMAFOffValue {
		err = fmt.Errorf("p25 value %f is out of range [%d, %d]", p25, VMAFMinValue, VMAFMaxValue)
		return
	}
	vc.p25 = p25
	if (median < VMAFMinValue || median > VMAFMaxValue) && median != VMAFOffValue {
		err = fmt.Errorf("median value %f is out of range [%d, %d]", median, VMAFMinValue, VMAFMaxValue)
		return
	}
	vc.median = median
	if (hmean < VMAFMinValue || hmean > VMAFMaxValue) && hmean != VMAFOffValue {
		err = fmt.Errorf("hmean value %f is out of range [%d, %d]", hmean, VMAFMinValue, VMAFMaxValue)
		return
	}
	vc.hmean = hmean
	if (mean < VMAFMinValue || mean > VMAFMaxValue) && mean != VMAFOffValue {
		err = fmt.Errorf("mean value %f is out of range [%d, %d]", mean, VMAFMinValue, VMAFMaxValue)
		return
	}
	vc.mean = mean
	return
}

// VMAFChecker holds VMAF score thresholds used to validate video quality.
type VMAFChecker struct {
	min    float64
	p1     float64
	p5     float64
	p10    float64
	p25    float64
	median float64
	hmean  float64
	mean   float64
}

// Validate checks whether the provided VMAFStats meet all configured thresholds.
// It returns false if any active threshold is not met, otherwise true.
func (vc VMAFChecker) Validate(stats VMAFStats) bool {
	if vc.min != VMAFOffValue && stats.Minimum < vc.min {
		return false
	}
	if vc.p1 != VMAFOffValue && stats.Percentile1 < vc.p1 {
		return false
	}
	if vc.p5 != VMAFOffValue && stats.Percentile5 < vc.p5 {
		return false
	}
	if vc.p10 != VMAFOffValue && stats.Percentile10 < vc.p10 {
		return false
	}
	if vc.p25 != VMAFOffValue && stats.Percentile25 < vc.p25 {
		return false
	}
	if vc.median != VMAFOffValue && stats.Median < vc.median {
		return false
	}
	if vc.hmean != VMAFOffValue && stats.HarmonicMean < vc.hmean {
		return false
	}
	if vc.mean != VMAFOffValue && stats.Mean < vc.mean {
		return false
	}
	return true
}

// String returns a formatted table representation of the active VMAF thresholds.
func (vc VMAFChecker) String() string {
	var tableBuffer strings.Builder
	// Prepare alignment config for all columns
	alignments := make([]tw.Align, 0, 9)
	if vc.min != VMAFOffValue {
		alignments = append(alignments, tw.AlignCenter)
	}
	if vc.p1 != VMAFOffValue {
		alignments = append(alignments, tw.AlignCenter)
	}
	if vc.p5 != VMAFOffValue {
		alignments = append(alignments, tw.AlignCenter)
	}
	if vc.p10 != VMAFOffValue {
		alignments = append(alignments, tw.AlignCenter)
	}
	if vc.p25 != VMAFOffValue {
		alignments = append(alignments, tw.AlignCenter)
	}
	if vc.median != VMAFOffValue {
		alignments = append(alignments, tw.AlignCenter)
	}
	if vc.hmean != VMAFOffValue {
		alignments = append(alignments, tw.AlignCenter)
	}
	if vc.mean != VMAFOffValue {
		alignments = append(alignments, tw.AlignCenter)
	}
	// Create table with custom symbols and alignment
	symbols := tw.NewSymbolCustom("custom").
		WithCenter("┼").
		WithRow("─").
		WithColumn("│")
	table := tablewriter.NewTable(&tableBuffer,
		tablewriter.WithRenderer(renderer.NewBlueprint(tw.Rendition{
			Borders:  tw.BorderNone,
			Symbols:  symbols,
			Settings: tw.Settings{Separators: tw.Separators{BetweenColumns: tw.On}},
		})),
		tablewriter.WithConfig(tablewriter.Config{
			Header: tw.CellConfig{
				Alignment: tw.CellAlignment{PerColumn: alignments},
			},
			Row: tw.CellConfig{
				Alignment: tw.CellAlignment{PerColumn: alignments},
			},
		}),
	)
	// headers
	headers := make([]string, 0, 9)
	if vc.min != VMAFOffValue {
		headers = append(headers, "Min")
	}
	if vc.p1 != VMAFOffValue {
		headers = append(headers, "P1")
	}
	if vc.p5 != VMAFOffValue {
		headers = append(headers, "P5")
	}
	if vc.p10 != VMAFOffValue {
		headers = append(headers, "P10")
	}
	if vc.p25 != VMAFOffValue {
		headers = append(headers, "P25")
	}
	if vc.median != VMAFOffValue {
		headers = append(headers, "Median")
	}
	if vc.hmean != VMAFOffValue {
		headers = append(headers, "Harmonic Mean")
	}
	if vc.mean != VMAFOffValue {
		headers = append(headers, "Mean")
	}
	table.Header(headers)
	// body
	body := make([]string, 0, 9)
	if vc.min != VMAFOffValue {
		body = append(body, strconv.FormatFloat(vc.min, 'f', -1, 64))
	}
	if vc.p1 != VMAFOffValue {
		body = append(body, strconv.FormatFloat(vc.p1, 'f', -1, 64))
	}
	if vc.p5 != VMAFOffValue {
		body = append(body, strconv.FormatFloat(vc.p5, 'f', -1, 64))
	}
	if vc.p10 != VMAFOffValue {
		body = append(body, strconv.FormatFloat(vc.p10, 'f', -1, 64))
	}
	if vc.p25 != VMAFOffValue {
		body = append(body, strconv.FormatFloat(vc.p25, 'f', -1, 64))
	}
	if vc.median != VMAFOffValue {
		body = append(body, strconv.FormatFloat(vc.median, 'f', -1, 64))
	}
	if vc.hmean != VMAFOffValue {
		body = append(body, strconv.FormatFloat(vc.hmean, 'f', -1, 64))
	}
	if vc.mean != VMAFOffValue {
		body = append(body, strconv.FormatFloat(vc.mean, 'f', -1, 64))
	}
	table.Append(body)
	// Build table
	table.Render()
	return tableBuffer.String()
}
