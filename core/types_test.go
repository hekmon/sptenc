package core

import (
	"fmt"
	"testing"
)

func TestVMAFStats_String(t *testing.T) {
	stats := VMAFStats{
		Version: "v3.0.0", Minimum: 88.5, Percentile1: 89, Percentile5: 90.25, Percentile10: 91,
		Percentile25: 92, Median: 93.5, HarmonicMean: 93.2, Mean: 93.25, Maximum: 99.75,
		CAMBIMean: 1.25, CAMBIMax: 4.5,
	}
	expected := "min=88.5 p1=89 p5=90.25 p10=91 p25=92 median=93.5 hmean=93.2 mean=93.25 max=99.75 cambi_mean=1.25 cambi_max=4.5 (libvmaf v3.0.0)"
	// %s is what the debug logs of the QP search use
	if got := fmt.Sprintf("%s", stats); got != expected {
		t.Errorf("expected %q, got %q", expected, got)
	}
}
