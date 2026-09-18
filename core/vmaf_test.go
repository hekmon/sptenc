package core

import (
	"strings"
	"testing"
)

func TestNewVMAFChecker_AllOff(t *testing.T) {
	_, err := NewVMAFChecker(VMAFOffValue, VMAFOffValue, VMAFOffValue, VMAFOffValue,
		VMAFOffValue, VMAFOffValue, VMAFOffValue, VMAFOffValue)
	if err == nil {
		t.Fatal("expected error when all thresholds are off, got nil")
	}
}

func TestNewVMAFChecker_OutOfRange(t *testing.T) {
	tests := []struct {
		name  string
		min   float64
		p1    float64
		p5    float64
		p10   float64
		p25   float64
		med   float64
		mean  float64
		hmean float64
	}{
		{"min below range", -2, VMAFOffValue, VMAFOffValue, VMAFOffValue, VMAFOffValue, VMAFOffValue, VMAFOffValue, VMAFOffValue},
		{"min above range", 101, VMAFOffValue, VMAFOffValue, VMAFOffValue, VMAFOffValue, VMAFOffValue, VMAFOffValue, VMAFOffValue},
		{"mean below range", VMAFOffValue, VMAFOffValue, VMAFOffValue, VMAFOffValue, VMAFOffValue, VMAFOffValue, -0.5, VMAFOffValue},
		{"mean above range", VMAFOffValue, VMAFOffValue, VMAFOffValue, VMAFOffValue, VMAFOffValue, VMAFOffValue, 100.1, VMAFOffValue},
		{"p1 out of range", VMAFOffValue, -0.1, VMAFOffValue, VMAFOffValue, VMAFOffValue, VMAFOffValue, VMAFOffValue, VMAFOffValue},
		{"median out of range", VMAFOffValue, VMAFOffValue, VMAFOffValue, VMAFOffValue, VMAFOffValue, 101, VMAFOffValue, VMAFOffValue},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewVMAFChecker(tt.min, tt.p1, tt.p5, tt.p10, tt.p25, tt.med, tt.mean, tt.hmean)
			if err == nil {
				t.Fatal("expected error for out-of-range value, got nil")
			}
		})
	}
}

func TestNewVMAFChecker_Valid(t *testing.T) {
	_, err := NewVMAFChecker(0, 10, 20, 30, 40, 50, 60, 70)
	if err != nil {
		t.Fatalf("expected no error for valid thresholds, got %v", err)
	}

	// Single active threshold should work
	_, err = NewVMAFChecker(VMAFOffValue, VMAFOffValue, VMAFOffValue, VMAFOffValue,
		VMAFOffValue, VMAFOffValue, 80, VMAFOffValue)
	if err != nil {
		t.Fatalf("expected no error for single active threshold, got %v", err)
	}
}

func TestVMAFChecker_Validate(t *testing.T) {
	vc, err := NewVMAFChecker(50, VMAFOffValue, VMAFOffValue, VMAFOffValue,
		VMAFOffValue, VMAFOffValue, 80, VMAFOffValue)
	if err != nil {
		t.Fatalf("failed to create checker: %v", err)
	}

	tests := []struct {
		name   string
		stats  VMAFStats
		wantOK bool
	}{
		{"all above thresholds", VMAFStats{Minimum: 55, Mean: 85}, true},
		{"min exactly at threshold", VMAFStats{Minimum: 50, Mean: 85}, true},
		{"mean exactly at threshold", VMAFStats{Minimum: 55, Mean: 80}, true},
		{"min below threshold", VMAFStats{Minimum: 49, Mean: 85}, false},
		{"mean below threshold", VMAFStats{Minimum: 55, Mean: 79}, false},
		{"both below", VMAFStats{Minimum: 40, Mean: 70}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := vc.Validate(tt.stats)
			if got != tt.wantOK {
				t.Errorf("Validate() = %v, want %v", got, tt.wantOK)
			}
		})
	}
}

func TestVMAFChecker_Validate_OnlySomeActive(t *testing.T) {
	// Only median active
	vc, err := NewVMAFChecker(VMAFOffValue, VMAFOffValue, VMAFOffValue, VMAFOffValue,
		VMAFOffValue, 75, VMAFOffValue, VMAFOffValue)
	if err != nil {
		t.Fatalf("failed to create checker: %v", err)
	}

	if !vc.Validate(VMAFStats{Median: 75, Minimum: 0, Mean: 0}) {
		t.Error("expected validation to pass when only median is checked and meets threshold")
	}
	if vc.Validate(VMAFStats{Median: 74, Minimum: 100, Mean: 100}) {
		t.Error("expected validation to fail when median is below threshold")
	}
}

func TestVMAFChecker_String(t *testing.T) {
	vc, err := NewVMAFChecker(50, VMAFOffValue, VMAFOffValue, 60, VMAFOffValue, 70, 80, VMAFOffValue)
	if err != nil {
		t.Fatalf("failed to create checker: %v", err)
	}

	s := vc.String()
	if s == "" {
		t.Fatal("expected non-empty string output")
	}
	// Should contain the active threshold values
	if !strings.Contains(s, "50") {
		t.Error("expected String() to contain '50'")
	}
	if !strings.Contains(s, "60") {
		t.Error("expected String() to contain '60'")
	}
	if !strings.Contains(s, "70") {
		t.Error("expected String() to contain '70'")
	}
	if !strings.Contains(s, "80") {
		t.Error("expected String() to contain '80'")
	}

	// Inactive threshold values should not appear (no 90 in this checker)
	if strings.Contains(s, "90") {
		t.Error("expected String() NOT to contain inactive value '90'")
	}
}

func TestVMAFChecker_Thresholds(t *testing.T) {
	vc, err := NewVMAFChecker(50, VMAFOffValue, 60, VMAFOffValue, 70, VMAFOffValue, 80, 90)
	if err != nil {
		t.Fatalf("failed to create checker: %v", err)
	}

	m := vc.Thresholds()
	if len(m) != 5 {
		t.Errorf("expected 5 active thresholds, got %d", len(m))
	}

	expected := map[string]float64{
		"min":   50,
		"p5":    60,
		"p25":   70,
		"mean":  80,
		"hmean": 90,
	}
	for k, v := range expected {
		got, ok := m[k]
		if !ok {
			t.Errorf("expected threshold %s to be present", k)
			continue
		}
		if got != v {
			t.Errorf("expected %s=%v, got %v", k, v, got)
		}
	}

	inactive := []string{"p1", "p10", "median"}
	for _, k := range inactive {
		if _, ok := m[k]; ok {
			t.Errorf("expected threshold %s to be absent", k)
		}
	}
}

func TestVMAFChecker_Validate_AllThresholds(t *testing.T) {
	tests := []struct {
		name     string
		setValue func(*VMAFStats, float64)
		getValue func(VMAFStats) float64
	}{
		{"min", func(s *VMAFStats, v float64) { s.Minimum = v }, func(s VMAFStats) float64 { return s.Minimum }},
		{"p1", func(s *VMAFStats, v float64) { s.Percentile1 = v }, func(s VMAFStats) float64 { return s.Percentile1 }},
		{"p5", func(s *VMAFStats, v float64) { s.Percentile5 = v }, func(s VMAFStats) float64 { return s.Percentile5 }},
		{"p10", func(s *VMAFStats, v float64) { s.Percentile10 = v }, func(s VMAFStats) float64 { return s.Percentile10 }},
		{"p25", func(s *VMAFStats, v float64) { s.Percentile25 = v }, func(s VMAFStats) float64 { return s.Percentile25 }},
		{"median", func(s *VMAFStats, v float64) { s.Median = v }, func(s VMAFStats) float64 { return s.Median }},
		{"mean", func(s *VMAFStats, v float64) { s.Mean = v }, func(s VMAFStats) float64 { return s.Mean }},
		{"hmean", func(s *VMAFStats, v float64) { s.HarmonicMean = v }, func(s VMAFStats) float64 { return s.HarmonicMean }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Build a checker with only this threshold active at value 50.
			args := [8]float64{VMAFOffValue, VMAFOffValue, VMAFOffValue, VMAFOffValue, VMAFOffValue, VMAFOffValue, VMAFOffValue, VMAFOffValue}
			switch tt.name {
			case "min":
				args[0] = 50
			case "p1":
				args[1] = 50
			case "p5":
				args[2] = 50
			case "p10":
				args[3] = 50
			case "p25":
				args[4] = 50
			case "median":
				args[5] = 50
			case "mean":
				args[6] = 50
			case "hmean":
				args[7] = 50
			}
			vc, err := NewVMAFChecker(args[0], args[1], args[2], args[3], args[4], args[5], args[6], args[7])
			if err != nil {
				t.Fatalf("failed to create checker: %v", err)
			}

			// Exactly at threshold should pass.
			stats := VMAFStats{Minimum: 100, Percentile1: 100, Percentile5: 100, Percentile10: 100, Percentile25: 100, Median: 100, Mean: 100, HarmonicMean: 100}
			tt.setValue(&stats, 50)
			if !vc.Validate(stats) {
				t.Error("expected validation to pass at exact threshold")
			}

			// Just below threshold should fail.
			tt.setValue(&stats, 49.999)
			if vc.Validate(stats) {
				t.Error("expected validation to fail just below threshold")
			}
		})
	}
}

func TestVMAFChecker_String_AllOffExceptOne(t *testing.T) {
	vc, err := NewVMAFChecker(VMAFOffValue, VMAFOffValue, VMAFOffValue, VMAFOffValue,
		VMAFOffValue, VMAFOffValue, 90, VMAFOffValue)
	if err != nil {
		t.Fatalf("failed to create checker: %v", err)
	}

	s := vc.String()
	if !strings.Contains(s, "90") {
		t.Error("expected String() to contain active value '90'")
	}
}
