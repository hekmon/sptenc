package transnetv2

import (
	"math"
	"reflect"
	"strings"
	"testing"
)

func TestCuts(t *testing.T) {
	below := math.Nextafter32(0.3, 0) // float32's largest value under 0.3: 0.29999998
	above := math.Nextafter32(0.3, 1) // the float32 after float32's 0.3: 0.30000004
	for _, tc := range []struct {
		name          string
		probabilities []float32
		want          []Cut
	}{
		{"empty", nil, nil},
		{"all under the floor", []float32{0, 0.1, 0.25, below, 0.01}, nil},
		{"one spike", []float32{0, 0.9, 0}, []Cut{{2, 0.9}}},
		{"the new scene starts after the peak", []float32{0, 0.4, 0.7, 0.5, 0}, []Cut{{3, 0.7}}},
		{"first of equal maxima", []float32{0, 0.5, 0.5, 0}, []Cut{{2, 0.5}}},
		{"first of equal maxima after a lower one", []float32{0, 0.4, 0.6, 0.5, 0.6, 0}, []Cut{{3, 0.6}}},
		{"run touching the start", []float32{0.8, 0.4, 0}, []Cut{{1, 0.8}}},
		{"run touching the end, peak before the last frame", []float32{0, 0.7, 0.4}, []Cut{{2, 0.7}}},
		{"run touching the end, peak on the last frame", []float32{0, 0.4, 0.7}, nil},
		{"one frame, at the floor", []float32{0.5}, nil},
		{"run touching both ends", []float32{0.3, 0.9, 0.4}, []Cut{{2, 0.9}}},
		{"run touching both ends, plateau to the end", []float32{0.3, 1, 1}, []Cut{{2, 1}}},
		{"two runs one frame apart", []float32{0.3, 0.05, 0.4, 0}, []Cut{{1, 0.3}, {3, 0.4}}},
		{"a dip staying at the floor keeps one run", []float32{0, 0.9, 0.3, 0.8, 0}, []Cut{{2, 0.9}}},
		{"a dip under the floor makes two runs", []float32{0, 0.9, below, 0.8, 0}, []Cut{{2, 0.9}, {4, 0.8}}},
		// a floor of 0.1 made one run of it, one cut at frame 2 at every threshold
		{"a dip to 0.2 makes two runs", []float32{0, 0.9, 0.2, 0.8, 0}, []Cut{{2, 0.9}, {4, 0.8}}},
		{"under the floor, 0.1 is nothing", []float32{0, 0.1, 0.2, 0.1, 0}, nil},
		{"float32's 0.3 reaches the floor", []float32{0, 0.3, 0}, []Cut{{2, 0.3}}},
		{"the float32 after 0.3 reaches it", []float32{0, above, 0}, []Cut{{2, 0.3}}},
		{"the float32 under 0.3 does not", []float32{0, below, 0}, nil},
		{"the float32 under 0.3 splits a run", []float32{0, 0.3, below, above, 0}, []Cut{{2, 0.3}, {4, 0.3}}},
		{"score rounded to 4 decimals", []float32{0, 0.523456, 0}, []Cut{{2, 0.5235}}},
		{"score of float32's 0.7, rounded up", []float32{0, 0.7, 0}, []Cut{{2, 0.7}}},
		// k/32 are exact in float32 and end in 5 at the fifth decimal
		{"exact tie rounded half to even, down", []float32{0, 0.40625, 0}, []Cut{{2, 0.4062}}},
		{"exact tie rounded half to even, up", []float32{0, 0.34375, 0}, []Cut{{2, 0.3438}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Cuts(tc.probabilities)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("expected %v, got %v", tc.want, got)
			}
		})
	}
}

func TestCutsRefused(t *testing.T) {
	for _, tc := range []struct {
		name          string
		probabilities []float32
		want          string
	}{
		{"NaN", []float32{0, float32(math.NaN()), 0}, "probability NaN of frame 1"},
		{"negative", []float32{0, 0, -0.1}, "probability -0.1 of frame 2"},
		{"over 1", []float32{1.5}, "probability 1.5 of frame 0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cuts, err := Cuts(tc.probabilities)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("expected an error containing %q, got %v", tc.want, err)
			}
			if cuts != nil {
				t.Errorf("expected no cuts with an error, got %v", cuts)
			}
		})
	}
}
