package main

import (
	"context"
	"errors"
	"io"
	"maps"
	"os/exec"
	"strings"
	"testing"

	"github.com/hekmon/sptenc/core"
	"github.com/hekmon/sptenc/ffmpeg"

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
		{1, core.CAMBIOffValue, "at most +1 on average over the frames of a segment"},
		{0.5, 2, "at most +0.5 on average over the frames of a segment, and +2 on its worst frame"},
		{core.CAMBIOffValue, 3, "at most +3 on the worst frame of a segment"},
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

// TestSetupVMAF runs the real thing, the probes included (skipped without ffmpeg): the score each
// model and flag lead to, the gate, the cache marker, and what the run says.
func TestSetupVMAF(t *testing.T) {
	if _, err := exec.LookPath(ffmpeg.FFMPEGBinary); err != nil {
		t.Skipf("%s not found: %s", ffmpeg.FFMPEGBinary, err)
	}
	dir := t.TempDir()
	stream := &ffmpeg.FFProbeBinaryStream{Width: 320, Height: 180}
	for _, tc := range []struct {
		name                              string
		model                             ffmpeg.VMAFModel
		gate                              bool
		args                              []string
		score                             ffmpeg.VMAFScore
		modelCAMBI, cambi, cachesOriginal bool
		says, saysNot                     []string
	}{
		{"v1", ffmpeg.VMAFModelFHD, true, nil, ffmpeg.VMAFScoreFidelity, true, true, false,
			[]string{"The VMAF thresholds gate fidelity", "gated too"}, []string{"original"}},
		{"v1, original score", ffmpeg.VMAFModelFHD, true, []string{"--vmaf-original"}, ffmpeg.VMAFScoreOriginal, true, true, true,
			[]string{"The VMAF thresholds gate the original score", "counts against every encode", "gated too"}, []string{"fidelity"}},
		{"v1, original score reported", ffmpeg.VMAFModelFHD, false, []string{"--vmaf-original"}, ffmpeg.VMAFScoreOriginal, true, false, true,
			[]string{"Scoring with the original score", "counts against the distorted video"}, []string{"fidelity", "gate", "every encode"}},
		{"v1, fidelity reported", ffmpeg.VMAFModelFHD, false, nil, ffmpeg.VMAFScoreFidelity, true, false, false,
			[]string{"Scoring fidelity"}, []string{"gate"}},
		// a model without CAMBI: nothing about banding, unless a threshold asks for the gate
		{"v0", ffmpeg.VMAFModelV0FHD, true, nil, ffmpeg.VMAFScoreFidelity, false, false, false,
			nil, []string{"CAMBI", "fidelity"}},
		{"v0, original score", ffmpeg.VMAFModelV0FHD, true, []string{"--vmaf-original"}, ffmpeg.VMAFScoreFidelity, false, false, false,
			[]string{"WARNING: --vmaf-original has no effect with vmaf_v0.6.1: this model has no CAMBI term, so the score sptenc gates on is already its original one"},
			[]string{"gated too"}},
		{"v0, original score reported", ffmpeg.VMAFModelV0FHD, false, []string{"--vmaf-original"}, ffmpeg.VMAFScoreFidelity, false, false, false,
			[]string{"so the score sptenc reports is already its original one"}, nil},
		{"v0, CAMBI threshold", ffmpeg.VMAFModelV0FHD, true, []string{"--cambi-max", "2"}, ffmpeg.VMAFScoreFidelity, false, true, false,
			[]string{"gated too, frame by frame how much higher the encode rates than its source on CAMBI's scale (0 = no banding, ~5 = slightly annoying): at most +1 on average over the frames of a segment, and +2 on its worst frame"},
			[]string{"fidelity"}},
	} {
		flags := []cli.Flag{vmafOriginalFlag("VMAF", false)}
		if tc.gate {
			flags = VMAFFlags()
		}
		flags = append(flags, &cli.StringFlag{Name: tmpDirFlagName, Value: dir})
		var (
			setup vmafSetup
			out   strings.Builder
		)
		cmd := &cli.Command{
			Name:  "test",
			Flags: flags,
			Action: func(ctx context.Context, cmd *cli.Command) (err error) {
				setup, err = setupVMAF(ctx, cmd, &out, tc.model, stream, tc.gate)
				return
			},
		}
		if err := cmd.Run(context.Background(), append([]string{"test"}, tc.args...)); err != nil {
			if errors.Is(err, ffmpeg.ErrVMAFModelUnavailable) {
				t.Skipf("the libvmaf of %s does not know %s: %s", ffmpeg.FFMPEGBinary, tc.model, err)
			}
			t.Fatalf("%s: %s", tc.name, err)
		}
		if setup.score != tc.score || setup.modelCAMBI != tc.modelCAMBI || setup.cambi.Enabled() != tc.cambi ||
			setup.cachesOriginalScore() != tc.cachesOriginal {
			t.Errorf("%s: expected score %s, CAMBI %t, gate %t, original cache %t, got %+v (original cache %t)",
				tc.name, tc.score, tc.modelCAMBI, tc.cambi, tc.cachesOriginal, setup, setup.cachesOriginalScore())
		}
		for _, says := range tc.says {
			if !strings.Contains(out.String(), says) {
				t.Errorf("%s: expected %q in:\n%s", tc.name, says, out.String())
			}
		}
		for _, saysNot := range tc.saysNot {
			if strings.Contains(out.String(), saysNot) {
				t.Errorf("%s: unexpected %q in:\n%s", tc.name, saysNot, out.String())
			}
		}
	}
}
