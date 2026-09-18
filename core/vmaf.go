package core

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/hekmon/sptenc/ffmpeg"

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

// setThreshold validates a single threshold value and assigns it to target.
// VMAFOffValue is accepted as a sentinel meaning "disabled".
func setThreshold(name string, value float64, target *float64) error {
	if value == VMAFOffValue {
		*target = value
		return nil
	}
	if value < VMAFMinValue || value > VMAFMaxValue {
		return fmt.Errorf("%s value %f is out of range [%d, %d]", name, value, VMAFMinValue, VMAFMaxValue)
	}
	*target = value
	return nil
}

// NewVMAFChecker creates a new VMAFChecker with the given thresholds.
// Each threshold can be set to VMAFOffValue to disable that specific check.
// Returns an error if all values are off or if any value is out of the valid VMAF range.
func NewVMAFChecker(min, p1, p5, p10, p25, median, mean, hmean float64) (vc VMAFChecker, err error) {
	if min == VMAFOffValue && p1 == VMAFOffValue && p5 == VMAFOffValue && p10 == VMAFOffValue &&
		p25 == VMAFOffValue && median == VMAFOffValue && mean == VMAFOffValue && hmean == VMAFOffValue {
		err = errors.New("all values are off")
		return
	}
	if err = setThreshold("min", min, &vc.min); err != nil {
		return
	}
	if err = setThreshold("p1", p1, &vc.p1); err != nil {
		return
	}
	if err = setThreshold("p5", p5, &vc.p5); err != nil {
		return
	}
	if err = setThreshold("p10", p10, &vc.p10); err != nil {
		return
	}
	if err = setThreshold("p25", p25, &vc.p25); err != nil {
		return
	}
	if err = setThreshold("median", median, &vc.median); err != nil {
		return
	}
	if err = setThreshold("mean", mean, &vc.mean); err != nil {
		return
	}
	if err = setThreshold("hmean", hmean, &vc.hmean); err != nil {
		return
	}
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
	mean   float64
	hmean  float64
}

// Validate checks whether the provided VMAFStats meet all configured thresholds.
// It returns false if any active threshold is not met, otherwise true.
func (vc VMAFChecker) Validate(stats ffmpeg.VMAFStats) bool {
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
