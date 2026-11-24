package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hekmon/sptenc/ffmpeg"
	"github.com/hekmon/sptenc/pipeline"
	"github.com/hekmon/sptenc/transnetv2"

	"github.com/urfave/cli/v3"
)

// The test binary is its own fake TransNetV2 helper: run with fakeHelperModeEnv set, TestMain runs
// fakeHelper instead of the tests (the pattern of transnetv2/detect_test.go). Only the version
// command is needed here: the detection itself is tested in pipeline.
const (
	fakeHelperModeEnv = "SPTENC_CMD_TRANSNETV2_FAKE"
	fakeHelperSHA256  = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
)

func TestMain(m *testing.M) {
	if mode := os.Getenv(fakeHelperModeEnv); mode != "" {
		os.Exit(fakeHelper(mode, os.Args[1:]))
	}
	os.Exit(m.Run())
}

func fakeHelper(mode string, args []string) int {
	if len(args) != 1 || args[0] != "version" {
		fmt.Fprintf(os.Stderr, "fake: unexpected arguments %q\n", args)
		return 126
	}
	protocol := 1
	if mode == "protocol2" {
		protocol = 2
	}
	fmt.Printf(`{"protocol":%d,"detector":"transnetv2","version":"0.1.0","model":"transnetv2.onnx","model_sha256":"%s"}`+"\n",
		protocol, fakeHelperSHA256)
	return 0
}

// useHelper points transnetv2.Helper at the fake in the given mode, or at a file that does not
// exist for "missing".
func useHelper(t *testing.T, mode string) {
	t.Helper()
	previous := transnetv2.Helper
	t.Cleanup(func() { transnetv2.Helper = previous })
	if mode == "missing" {
		transnetv2.Helper = filepath.Join(t.TempDir(), "sptenc-detector-transnetv2")
		return
	}
	transnetv2.Helper = os.Args[0]
	t.Setenv(fakeHelperModeEnv, mode)
	t.Setenv("GORACE", "atexit_sleep_ms=0") // under -race, a Go program exiting with status 0 sleeps a second first
}

// TestCheckSceneDetector: nothing is run for scdet, whatever the helper; transnetv2 needs a helper
// answering its version command in this protocol, or fails with the cause and the pointer to
// sptenc check.
func TestCheckSceneDetector(t *testing.T) {
	for _, tc := range []struct {
		mode     string
		detector pipeline.SceneDetector
		wantErr  []string // empty: no error
	}{
		{"missing", pipeline.SceneDetectorScdet, nil},
		{"protocol2", pipeline.SceneDetectorScdet, nil},
		{"ok", pipeline.SceneDetectorTransNetV2, nil},
		{"missing", pipeline.SceneDetectorTransNetV2, []string{"the transnetv2 scene detector needs its helper", "run 'sptenc check'", "--transnetv2-path", "error starting"}},
		{"protocol2", pipeline.SceneDetectorTransNetV2, []string{"run 'sptenc check'", "protocol 2 stated, this sptenc speaks protocol 1"}},
		{"ok", "unknown", []string{"unsupported scene detector"}},
	} {
		useHelper(t, tc.mode)
		helper, err := checkSceneDetector(t.Context(), tc.detector)
		name := string(tc.detector) + " with a helper " + tc.mode
		if len(tc.wantErr) == 0 {
			if err != nil {
				t.Errorf("%s: %s", name, err)
			}
			if tc.detector.HasHelper() != (helper != nil) {
				t.Errorf("%s: expected a version line %t, got %+v", name, tc.detector.HasHelper(), helper)
			}
			if helper != nil && (helper.Version != "0.1.0" || helper.ModelSHA256 != fakeHelperSHA256) {
				t.Errorf("%s: unexpected version line %+v", name, helper)
			}
			continue
		}
		if err == nil {
			t.Errorf("%s: expected an error", name)
			continue
		}
		for _, want := range tc.wantErr {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%s: expected %q in the error, got %s", name, want, err)
			}
		}
	}
}

// TestNewSceneCuts: the helper's version and model go into the cuts with a helper, nothing without.
func TestNewSceneCuts(t *testing.T) {
	info := &transnetv2.VersionInfo{Protocol: 1, Detector: "transnetv2", Version: "0.1.0", Model: "m.onnx", ModelSHA256: fakeHelperSHA256}
	cuts := newSceneCuts(pipeline.SceneDetectorTransNetV2, 0.5, 5e9, info)
	if cuts.Detector != pipeline.SceneDetectorTransNetV2 || cuts.Threshold != 0.5 || cuts.MinSegmentLength != 5e9 ||
		cuts.DetectorVersion != "0.1.0" || cuts.DetectorModelSHA256 != fakeHelperSHA256 {
		t.Errorf("unexpected cuts %+v", cuts)
	}
	if cuts = newSceneCuts(pipeline.SceneDetectorScdet, 10, 5e9, nil); cuts.DetectorVersion != "" || cuts.DetectorModelSHA256 != "" {
		t.Errorf("scdet: unexpected helper in %+v", cuts)
	}
}

// TestCommandsCheckTheHelper runs the Before of split and thresholds for real, their Action
// replaced: with --detector transnetv2 they fail at start-up without a usable helper, and pass
// with one; with scdet nothing is run, a missing helper changes nothing. Needs ffmpeg and ffprobe,
// which they check first.
func TestCommandsCheckTheHelper(t *testing.T) {
	for _, tool := range []string{ffmpeg.FFMPEGBinary, ffmpeg.FFProbeBinary} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not found: %s", tool, err)
		}
	}
	input := filepath.Join(t.TempDir(), "input.mkv") // Before only checks it is a regular file
	if err := os.WriteFile(input, []byte("not a video"), 0o600); err != nil {
		t.Fatal(err)
	}
	commands := []struct {
		name   string
		flags  func() []cli.Flag
		before cli.BeforeFunc
		args   []string
	}{
		{"split", func() []cli.Flag {
			return []cli.Flag{detectorFlag(""), singleThresholdFlag("Scene detection threshold", ""),
				&cli.BoolFlag{Name: listScenesFlagName}}
		}, splitCommand.Before, []string{"--" + listScenesFlagName, input}},
		{"thresholds", func() []cli.Flag {
			return append([]cli.Flag{detectorFlag(thresholdCategoryName)}, thresholdSearchFlags()...)
		}, thresholdsCommand.Before, []string{input}},
	}
	for _, command := range commands {
		for _, tc := range []struct {
			detector, mode string
			wantErr        string // empty: no error
		}{
			{"scdet", "missing", ""},
			{"scdet", "protocol2", ""},
			{"transnetv2", "ok", ""},
			{"transnetv2", "missing", "run 'sptenc check'"},
			{"transnetv2", "protocol2", "protocol 2 stated"},
		} {
			useHelper(t, tc.mode)
			acted := false
			cmd := &cli.Command{
				Name:      command.name,
				Flags:     command.flags(),
				Arguments: []cli.Argument{&cli.StringArg{Name: "inputfile"}},
				Writer:    io.Discard,
				ErrWriter: io.Discard,
				Before:    command.before,
				Action: func(ctx context.Context, cmd *cli.Command) error {
					acted = true
					return nil
				},
			}
			err := cmd.Run(t.Context(), append([]string{command.name, "--" + detectorFlagName, tc.detector}, command.args...))
			name := command.name + " --detector " + tc.detector + " with a helper " + tc.mode
			switch {
			case tc.wantErr == "" && err != nil:
				t.Errorf("%s: %s", name, err)
			case tc.wantErr == "" && !acted:
				t.Errorf("%s: the action did not run", name)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Errorf("%s: expected an error containing %q, got %v", name, tc.wantErr, err)
			case tc.wantErr != "" && acted:
				t.Errorf("%s: the action ran", name)
			}
		}
	}
}
