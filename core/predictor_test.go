package core

import (
	"math"
	"testing"
)

func TestPredictor_Convergence(t *testing.T) {
	// Build a monotonic QP→VMAF map.
	results := make(map[int]VMAFStats)
	for qp := 10; qp <= 40; qp += 10 {
		results[qp] = linearVMAF(qp)
	}

	p, err := NewPredictor(results, 0, 51, nil)
	if err != nil {
		t.Fatalf("NewPredictor failed: %v", err)
	}

	// Predicted value at QP 15 should lie between QP 10 and QP 20.
	predicted := p.Predict(15)
	expectedMin := linearVMAF(20).Mean
	expectedMax := linearVMAF(10).Mean
	if predicted.Mean <= expectedMin || predicted.Mean >= expectedMax {
		t.Errorf("predicted mean %f should be between %f and %f", predicted.Mean, expectedMin, expectedMax)
	}
}

func TestPredictor_ExtrapolationClamped(t *testing.T) {
	results := map[int]VMAFStats{
		20: {Mean: 80, Minimum: 70, Median: 80},
		30: {Mean: 60, Minimum: 50, Median: 60},
	}

	p, err := NewPredictor(results, 0, 51, nil)
	if err != nil {
		t.Fatalf("NewPredictor failed: %v", err)
	}

	// Extrapolate above known range; clamping should keep it valid.
	predicted := p.Predict(40)
	if predicted.Mean > 100 || predicted.Mean < 0 {
		t.Errorf("predicted mean %f out of valid VMAF range", predicted.Mean)
	}
}

func TestPredictor_AdaptCeiling(t *testing.T) {
	// When both neighbours are at VMAFMaxValue, the interpolator would keep
	// predicting 100 for interior points. The adapter should only lower the
	// value after the midpoint to avoid a one-by-one search.
	results := map[int]VMAFStats{
		10: {Mean: 100, Minimum: 100, Median: 100},
		20: {Mean: 100, Minimum: 100, Median: 100},
		30: {Mean: 90, Minimum: 85, Median: 90},
	}

	p, err := NewPredictor(results, 0, 51, nil)
	if err != nil {
		t.Fatalf("NewPredictor failed: %v", err)
	}

	// QP 15 is in the first half between 10 and 20; should stay at 100.
	predicted := p.Predict(15)
	if predicted.Mean != 100 {
		t.Errorf("expected adapted ceiling value 100 for first half, got %f", predicted.Mean)
	}

	// QP 25 is exactly at the midpoint between 20 and 30; adaptation returns the pre value.
	predicted = p.Predict(25)
	if predicted.Mean != 100 {
		t.Errorf("expected adapted ceiling value 100 at midpoint, got %f", predicted.Mean)
	}

	// QP 26 is in the second half; should be re-interpolated.
	predicted = p.Predict(26)
	if predicted.Mean >= 100 || predicted.Mean <= 85 {
		t.Errorf("expected interpolated value between 85 and 100 for second half, got %f", predicted.Mean)
	}
}

func TestPredictor_InsufficientPoints(t *testing.T) {
	_, err := NewPredictor(map[int]VMAFStats{}, 0, 51, nil)
	if err == nil {
		t.Fatal("expected error for insufficient points, got nil")
	}

	_, err = NewPredictor(map[int]VMAFStats{10: {Mean: 80}}, 0, 51, nil)
	if err == nil {
		t.Fatal("expected error for single point, got nil")
	}
}

func TestPredictor_Monotonicity(t *testing.T) {
	results := make(map[int]VMAFStats)
	for qp := 0; qp <= 50; qp += 5 {
		results[qp] = linearVMAF(qp)
	}

	p, err := NewPredictor(results, 0, 51, nil)
	if err != nil {
		t.Fatalf("NewPredictor failed: %v", err)
	}

	var lastMean float64 = math.MaxFloat64
	for qp := 0; qp <= 51; qp++ {
		predicted := p.Predict(qp)
		if predicted.Mean > lastMean+1e-9 {
			t.Errorf("non-monotonic prediction at QP %d: %f > %f", qp, predicted.Mean, lastMean)
		}
		lastMean = predicted.Mean
	}
}

func TestPredictor_ExactKnownQP(t *testing.T) {
	results := map[int]VMAFStats{
		10: {Mean: 90, Minimum: 85, Median: 90},
		20: {Mean: 80, Minimum: 75, Median: 80},
		30: {Mean: 70, Minimum: 65, Median: 70},
	}

	p, err := NewPredictor(results, 0, 51, nil)
	if err != nil {
		t.Fatalf("NewPredictor failed: %v", err)
	}

	// Predicting at an exact known QP should return the exact stored values.
	predicted := p.Predict(20)
	if predicted.Mean != 80 {
		t.Errorf("expected exact mean 80 at QP 20, got %f", predicted.Mean)
	}
	if predicted.Minimum != 75 {
		t.Errorf("expected exact minimum 75 at QP 20, got %f", predicted.Minimum)
	}
	if predicted.Median != 80 {
		t.Errorf("expected exact median 80 at QP 20, got %f", predicted.Median)
	}
}

func TestPredictor_GapInQPData(t *testing.T) {
	results := map[int]VMAFStats{
		10: {Mean: 90, Minimum: 85},
		30: {Mean: 70, Minimum: 65},
	}

	p, err := NewPredictor(results, 0, 51, nil)
	if err != nil {
		t.Fatalf("NewPredictor failed: %v", err)
	}

	// Predict at QP 20, halfway between known points.
	predicted := p.Predict(20)
	if predicted.Mean <= 70 || predicted.Mean >= 90 {
		t.Errorf("predicted mean %f should be between 70 and 90", predicted.Mean)
	}
	if predicted.Minimum <= 65 || predicted.Minimum >= 85 {
		t.Errorf("predicted minimum %f should be between 65 and 85", predicted.Minimum)
	}
}

func TestPredictor_ReverseCeiling(t *testing.T) {
	// Reverse ceiling: pre < 100, post == 100. There is no special handling for this case,
	// so the predictor should return the raw interpolated value without panic.
	results := map[int]VMAFStats{
		10: {Mean: 90, Minimum: 85, Median: 90},
		20: {Mean: 100, Minimum: 100, Median: 100},
	}

	p, err := NewPredictor(results, 0, 51, nil)
	if err != nil {
		t.Fatalf("NewPredictor failed: %v", err)
	}

	predicted := p.Predict(15)
	if predicted.Mean < 90 || predicted.Mean > 100 {
		t.Errorf("predicted mean %f should be between 90 and 100", predicted.Mean)
	}
	if predicted.Minimum < 85 || predicted.Minimum > 100 {
		t.Errorf("predicted minimum %f should be between 85 and 100", predicted.Minimum)
	}
}

func TestPredictor_SecondaryInterpolationSecondHalf(t *testing.T) {
	// This test verifies the second-half interpolation path in adaptCeilingValues.
	results := map[int]VMAFStats{
		10: {Mean: 100, Minimum: 100, Median: 100},
		20: {Mean: 100, Minimum: 100, Median: 100},
		30: {Mean: 90, Minimum: 85, Median: 90},
	}

	p, err := NewPredictor(results, 0, 51, nil)
	if err != nil {
		t.Fatalf("NewPredictor failed: %v", err)
	}

	// QP 26 is in the second half between 20 and 30. The ceiling adaptation
	// should re-interpolate between a synthetic middle point (25, 100) and post (30, 90).
	predicted := p.Predict(26)
	if predicted.Mean >= 100 || predicted.Mean <= 90 {
		t.Errorf("expected interpolated mean between 90 and 100 for second half, got %f", predicted.Mean)
	}
}

func TestClampVMAF_Boundaries(t *testing.T) {
	tests := []struct {
		input    float64
		expected float64
	}{
		{-10, 0},
		{-0.1, 0},
		{0, 0},
		{50, 50},
		{100, 100},
		{100.1, 100},
		{150, 100},
	}

	for _, tt := range tests {
		got := clampVMAF(tt.input)
		if got != tt.expected {
			t.Errorf("clampVMAF(%f) = %f, want %f", tt.input, got, tt.expected)
		}
	}
}

// TestPredictor_NonMonotonicQP is intentionally omitted. NewPredictor builds p.qps
// by iterating qpMin..qpMax in ascending order, so non-monotonic input is impossible
// through the public API. The defensive check inside NewPredictor is dead code under
// current construction rules and can only be reached if the construction logic changes.
