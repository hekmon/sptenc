package core

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
