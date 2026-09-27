package main

import (
	"context"
	"io"
	"maps"
	"testing"

	"github.com/hekmon/sptenc/core"

	"github.com/urfave/cli/v3"
)

// TestResolveCAMBIGate parses the CAMBI flags for real: whether a threshold was set is what turns
// the gate on with a model without CAMBI.
func TestResolveCAMBIGate(t *testing.T) {
	for _, tc := range []struct {
		name       string
		modelCAMBI bool
		args       []string
		thresholds map[string]float64 // none: the gate is off
	}{
		{"default", true, nil, map[string]float64{"mean": 1}},
		{"both off", true, []string{"--cambi-mean", "-1"}, nil},
		{"worst frame alone", true, []string{"--cambi-mean", "-1", "--cambi-max", "2"}, map[string]float64{"max": 2}},
		{"no added banding at all", true, []string{"--cambi-mean", "0"}, map[string]float64{"mean": 0}},
		{"both", true, []string{"--cambi-max", "3"}, map[string]float64{"mean": 1, "max": 3}},
		// a model without CAMBI: off unless a threshold is set, then the gate with its defaults
		{"no CAMBI, default", false, nil, nil},
		{"no CAMBI, mean set to its default", false, []string{"--cambi-mean", "1"}, map[string]float64{"mean": 1}},
		{"no CAMBI, worst frame set", false, []string{"--cambi-max", "2"}, map[string]float64{"mean": 1, "max": 2}},
		{"no CAMBI, mean set off", false, []string{"--cambi-mean", "-1"}, nil},
		{"no CAMBI, worst frame alone", false, []string{"--cambi-mean", "-1", "--cambi-max", "0.5"}, map[string]float64{"max": 0.5}},
	} {
		var gate core.CAMBIChecker
		cmd := &cli.Command{
			Name:  "test",
			Flags: VMAFFlags(),
			Action: func(ctx context.Context, cmd *cli.Command) (err error) {
				gate, err = resolveCAMBIGate(cmd, tc.modelCAMBI)
				return
			},
		}
		if err := cmd.Run(context.Background(), append([]string{"test"}, tc.args...)); err != nil {
			t.Fatalf("%s: %s", tc.name, err)
		}
		if gate.Enabled() != (tc.thresholds != nil) {
			t.Errorf("%s: expected the gate on %t, got %t", tc.name, tc.thresholds != nil, gate.Enabled())
		}
		if tc.thresholds != nil && !maps.Equal(gate.Thresholds(), tc.thresholds) {
			t.Errorf("%s: expected thresholds %v, got %v", tc.name, tc.thresholds, gate.Thresholds())
		}
	}
	// what the gate refuses is refused on the command line
	for _, value := range []string{"-2", "-0.5", "NaN", "Inf"} {
		cmd := &cli.Command{Name: "test", Flags: VMAFFlags(), Writer: io.Discard, ErrWriter: io.Discard,
			Action: func(context.Context, *cli.Command) error { return nil }}
		if err := cmd.Run(context.Background(), []string{"test", "--cambi-max", value}); err == nil {
			t.Errorf("--cambi-max %s should be refused", value)
		}
	}
}

func TestDescribeCAMBIGate(t *testing.T) {
	for _, tc := range []struct {
		mean, max float64
		expected  string
	}{
		{1, core.CAMBIOffValue, "at most 1 on average over the frames of a segment"},
		{0.5, 2, "at most 0.5 on average over the frames of a segment, and 2 on its worst frame"},
		{core.CAMBIOffValue, 3, "at most 3 on the worst frame of a segment"},
		{core.CAMBIOffValue, core.CAMBIOffValue, "off"},
	} {
		gate, err := core.NewCAMBIChecker(tc.mean, tc.max)
		if err != nil {
			t.Fatal(err)
		}
		if got := describeCAMBIGate(gate); got != tc.expected {
			t.Errorf("mean %v max %v: expected %q, got %q", tc.mean, tc.max, tc.expected, got)
		}
	}
}
