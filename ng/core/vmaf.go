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
	Min    float64
	P1     float64
	P5     float64
	P10    float64
	P25    float64
	Median float64
	HMean  float64
	Mean   float64
}
