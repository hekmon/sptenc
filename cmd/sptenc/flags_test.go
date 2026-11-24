package main

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/hekmon/sptenc/pipeline"

	"github.com/urfave/cli/v3"
)

// runSceneFlags parses the args with the flags given, validated as the commands do in their Before,
// and returns the detector and what action read from the command.
func runSceneFlags(t *testing.T, flags []cli.Flag, validate func(*cli.Command) error, args []string,
	action func(*cli.Command, pipeline.SceneDetector)) (detector pipeline.SceneDetector, err error) {
	t.Helper()
	cmd := &cli.Command{
		Name:      "test",
		Flags:     flags,
		Writer:    io.Discard,
		ErrWriter: io.Discard,
		Before: func(ctx context.Context, cmd *cli.Command) (context.Context, error) {
			return ctx, validate(cmd)
		},
		Action: func(ctx context.Context, cmd *cli.Command) (err error) {
			if detector, err = sceneDetector(cmd); err != nil {
				return
			}
			action(cmd, detector)
			return
		},
		OnUsageError: func(ctx context.Context, cmd *cli.Command, err error, isSubcommand bool) error {
			return err
		},
	}
	err = cmd.Run(context.Background(), append([]string{"test"}, args...))
	return
}

// TestSingleSceneThreshold parses the flags of encode and split for real: the threshold not given
// is the detector's default, a threshold given is checked against the detector's range.
func TestSingleSceneThreshold(t *testing.T) {
	for _, tc := range []struct {
		name      string
		args      []string
		threshold float64
		valid     bool
	}{
		{"no flag", nil, 10, true},
		{"detector given", []string{"--detector", "scdet"}, 10, true},
		{"threshold given", []string{"--min-threshold", "24.2765"}, 24.2765, true},
		{"alias", []string{"--detector", "scdet", "-T", "12.5"}, 12.5, true},
		{"bottom of the range", []string{"-T", "0"}, 0, true},
		{"top of the range", []string{"-T", "100"}, 100, true},
		{"above the range", []string{"-T", "100.5"}, 0, false},
		{"below the range", []string{"-T", "-1"}, 0, false},
		{"unknown detector", []string{"--detector", "none"}, 0, false},
	} {
		var threshold float64
		detector, err := runSceneFlags(t, []cli.Flag{detectorFlag(""), singleThresholdFlag("Scene detection threshold", "")},
			func(cmd *cli.Command) error { return validateSceneThresholds(cmd, minThresholdFlagName) },
			tc.args, func(cmd *cli.Command, d pipeline.SceneDetector) { threshold = singleSceneThreshold(cmd, d) })
		if (err == nil) != tc.valid {
			t.Errorf("%s: expected valid %t, got error %v", tc.name, tc.valid, err)
			continue
		}
		if !tc.valid {
			continue
		}
		if detector != pipeline.SceneDetectorScdet || threshold != tc.threshold {
			t.Errorf("%s: expected %s at %v, got %s at %v", tc.name, pipeline.SceneDetectorScdet, tc.threshold, detector, threshold)
		}
	}
}

// TestSearchSceneThresholds parses the flags of batchsearch and thresholds for real: each bound
// not given is the detector's default, both are checked against the detector's range and each other.
func TestSearchSceneThresholds(t *testing.T) {
	for _, tc := range []struct {
		name     string
		args     []string
		min, max float64
		valid    bool
	}{
		{"no flag", nil, 8, 40, true},
		{"detector given", []string{"--detector", "scdet"}, 8, 40, true},
		{"floor given", []string{"-T", "20"}, 20, 40, true},
		{"ceiling given", []string{"--max-threshold", "30"}, 8, 30, true},
		{"both given", []string{"--min-threshold", "0", "-M", "100"}, 0, 100, true},
		{"floor above the default ceiling", []string{"-T", "45"}, 0, 0, false},
		{"empty range", []string{"-T", "30", "-M", "30"}, 0, 0, false},
		{"ceiling above the range", []string{"-M", "101"}, 0, 0, false},
		{"floor below the range", []string{"-T", "-0.5"}, 0, 0, false},
		{"unknown detector", []string{"--detector", "none"}, 0, 0, false},
	} {
		var min, max float64
		flags := append([]cli.Flag{detectorFlag(thresholdCategoryName)}, thresholdSearchFlags()...)
		_, err := runSceneFlags(t, flags, validateSearchSceneThresholds, tc.args,
			func(cmd *cli.Command, d pipeline.SceneDetector) { min, max = searchSceneThresholds(cmd, d) })
		if (err == nil) != tc.valid {
			t.Errorf("%s: expected valid %t, got error %v", tc.name, tc.valid, err)
			continue
		}
		if tc.valid && (min != tc.min || max != tc.max) {
			t.Errorf("%s: expected %v-%v, got %v-%v", tc.name, tc.min, tc.max, min, max)
		}
	}
}

// TestSceneThresholdsTransNetV2 parses the flags of the four commands with --detector transnetv2:
// its defaults and its range, a threshold valid for one detector refused for the other.
func TestSceneThresholdsTransNetV2(t *testing.T) {
	singleValidate := func(cmd *cli.Command) error { return validateSceneThresholds(cmd, minThresholdFlagName) }
	for _, tc := range []struct {
		name      string
		args      []string
		detector  pipeline.SceneDetector
		threshold float64
		valid     bool
	}{
		{"default", []string{"--detector", "transnetv2"}, pipeline.SceneDetectorTransNetV2, 0.5, true},
		{"floor", []string{"--detector", "transnetv2", "-T", "0.3"}, pipeline.SceneDetectorTransNetV2, 0.3, true},
		{"top", []string{"--detector", "transnetv2", "-T", "1"}, pipeline.SceneDetectorTransNetV2, 1, true},
		{"a score of 4 decimals", []string{"--detector", "transnetv2", "-T", "0.6123"}, pipeline.SceneDetectorTransNetV2, 0.6123, true},
		{"under the floor", []string{"--detector", "transnetv2", "-T", "0.29"}, "", 0, false},
		{"above 1", []string{"--detector", "transnetv2", "-T", "1.01"}, "", 0, false},
		// a threshold valid for one detector, refused for the other
		{"scdet's default refused", []string{"--detector", "transnetv2", "-T", "10"}, "", 0, false},
		{"a probability for scdet", []string{"--detector", "scdet", "-T", "0.5"}, pipeline.SceneDetectorScdet, 0.5, true},
		{"0.2 for scdet", []string{"-T", "0.2"}, pipeline.SceneDetectorScdet, 0.2, true},
		{"0.2 refused for transnetv2", []string{"-T", "0.2", "--detector", "transnetv2"}, "", 0, false},
	} {
		var threshold float64
		single := []cli.Flag{detectorFlag(""), singleThresholdFlag("Scene detection threshold", "")}
		detector, err := runSceneFlags(t, single, singleValidate, tc.args,
			func(cmd *cli.Command, d pipeline.SceneDetector) { threshold = singleSceneThreshold(cmd, d) })
		if (err == nil) != tc.valid {
			t.Errorf("%s: expected valid %t, got error %v", tc.name, tc.valid, err)
			continue
		}
		if tc.valid && (detector != tc.detector || threshold != tc.threshold) {
			t.Errorf("%s: expected %s at %v, got %s at %v", tc.name, tc.detector, tc.threshold, detector, threshold)
		}
		if !tc.valid && tc.name != "under the floor" && tc.name != "above 1" && !strings.Contains(err.Error(), "with the transnetv2 detector") {
			t.Errorf("%s: expected the detector named in the error, got %v", tc.name, err)
		}
	}
	for _, tc := range []struct {
		name     string
		args     []string
		min, max float64
		valid    bool
	}{
		{"defaults", []string{"--detector", "transnetv2"}, 0.3, 0.9, true},
		{"floor given", []string{"--detector", "transnetv2", "-T", "0.5"}, 0.5, 0.9, true},
		{"whole range", []string{"--detector", "transnetv2", "-T", "0.3", "-M", "1"}, 0.3, 1, true},
		{"floor above the default ceiling", []string{"--detector", "transnetv2", "-T", "0.95"}, 0, 0, false},
		{"scdet's range refused", []string{"--detector", "transnetv2", "-T", "8", "-M", "40"}, 0, 0, false},
		{"under the floor", []string{"--detector", "transnetv2", "-T", "0.1"}, 0, 0, false},
		{"scdet given a probability range", []string{"-T", "0.3", "-M", "0.9"}, 0.3, 0.9, true},
	} {
		var min, max float64
		flags := append([]cli.Flag{detectorFlag(thresholdCategoryName)}, thresholdSearchFlags()...)
		_, err := runSceneFlags(t, flags, validateSearchSceneThresholds, tc.args,
			func(cmd *cli.Command, d pipeline.SceneDetector) { min, max = searchSceneThresholds(cmd, d) })
		if (err == nil) != tc.valid {
			t.Errorf("%s: expected valid %t, got error %v", tc.name, tc.valid, err)
			continue
		}
		if tc.valid && (min != tc.min || max != tc.max) {
			t.Errorf("%s: expected %v-%v, got %v-%v", tc.name, tc.min, tc.max, min, max)
		}
	}
}

// A command without --detector gets an error, not a crash.
func TestSceneDetectorWithoutFlag(t *testing.T) {
	cmd := &cli.Command{Name: "test", Writer: io.Discard, ErrWriter: io.Discard,
		Action: func(ctx context.Context, cmd *cli.Command) error {
			if err := validateSceneThresholds(cmd, minThresholdFlagName); err == nil {
				t.Error("validateSceneThresholds: expected an error")
			}
			if err := validateSearchSceneThresholds(cmd); err == nil {
				t.Error("validateSearchSceneThresholds: expected an error")
			}
			_, err := sceneDetector(cmd)
			return err
		},
	}
	if err := cmd.Run(context.Background(), []string{"test"}); err == nil {
		t.Error("sceneDetector: expected an error")
	}
}

func TestSceneThresholdTexts(t *testing.T) {
	for _, tc := range []struct{ got, want string }{
		{reuseThresholdFlags(pipeline.SceneDetectorScdet, 24.2765), "--detector scdet --min-threshold 24.2765"},
		{reuseThresholdFlags(pipeline.SceneDetectorTransNetV2, 0.6123), "--detector transnetv2 --min-threshold 0.6123"},
		{thresholdUsage("Scene detection threshold", pipeline.SceneDetector.DefaultThreshold),
			"Scene detection threshold, on the scale of --detector (scdet: 0-100, default 10; transnetv2: 0.3-1, default 0.5)"},
		{defaultSearchRanges(), "scdet 8–40, transnetv2 0.3–0.9"},
		{detectorUsage(), "Scene detector, whose scale the thresholds are on. " +
			"scdet: ffmpeg's scdet filter, thresholds 0-100, default 10, search 8-40; " +
			"transnetv2: the TransNetV2 neural network, run by its helper sptenc-detector-transnetv2 (see --transnetv2-path), " +
			"thresholds 0.3-1, default 0.5, search 0.3-0.9, provisional defaults"},
	} {
		if tc.got != tc.want {
			t.Errorf("want %q, got %q", tc.want, tc.got)
		}
	}
}
