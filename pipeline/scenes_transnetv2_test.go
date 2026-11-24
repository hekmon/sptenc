package pipeline

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hekmon/sptenc/core"
	"github.com/hekmon/sptenc/ffmpeg"
	"github.com/hekmon/sptenc/transnetv2"
)

// The test binary is its own fake TransNetV2 helper: run with fakeHelperModeEnv set, TestMain runs
// fakeHelper instead of the tests, in the mode it names (the pattern of transnetv2/detect_test.go).
const (
	fakeHelperModeEnv = "SPTENC_PIPELINE_TRANSNETV2_FAKE"
	fakeHelperWindow  = 100 // the fake answers 100 frames late, as the model may
	fakeHelperSHA256  = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
)

func TestMain(m *testing.M) {
	if mode := os.Getenv(fakeHelperModeEnv); mode != "" {
		os.Exit(fakeHelper(mode, os.Args[1:]))
	}
	os.Exit(m.Run())
}

// fakeHelperProbability is the probability the fake gives a frame: its brightness, the mean of its
// bytes over 255. It is derived from the frame received, so that a test can tell which frame of
// the source each probability belongs to.
func fakeHelperProbability(frame []byte) float32 {
	sum := 0
	for _, b := range frame {
		sum += int(b)
	}
	return float32(float64(sum) / float64(len(frame)) / 255)
}

// fakeHelper behaves as the helper in the given mode and returns its exit status: "brightness"
// answers fakeHelperProbability for every frame, "fail" dies after reading 5 frames.
func fakeHelper(mode string, args []string) int {
	if len(args) != 1 {
		fmt.Fprintf(os.Stderr, "fake: unexpected arguments %q\n", args)
		return 126
	}
	switch args[0] {
	case "version":
		fmt.Printf(`{"protocol":1,"detector":"transnetv2","version":"0.1.0","model":"transnetv2.onnx","model_sha256":"%s"}`+"\n", fakeHelperSHA256)
		return 0
	case "detect":
		in := bufio.NewReader(os.Stdin)
		out := bufio.NewWriter(os.Stdout)
		defer out.Flush()
		frame := make([]byte, transnetv2.FrameSize)
		var pending []string
		for read := 1; ; read++ {
			if _, err := io.ReadFull(in, frame); err != nil {
				break
			}
			if mode == "fail" && read == 5 {
				out.Flush()
				fmt.Fprintln(os.Stderr, "fake: out of memory")
				return 3
			}
			pending = append(pending, strconv.FormatFloat(float64(fakeHelperProbability(frame)), 'g', -1, 32))
			if len(pending) > fakeHelperWindow {
				fmt.Fprintln(out, pending[0])
				pending = pending[1:]
			}
		}
		for _, line := range pending {
			fmt.Fprintln(out, line)
		}
		return 0
	default:
		fmt.Fprintf(os.Stderr, "fake: unknown command %q\n", args[0])
		return 126
	}
}

// useFakeHelper makes the test binary the helper, in the given mode.
func useFakeHelper(t *testing.T, mode string) {
	t.Helper()
	previous := transnetv2.Helper
	t.Cleanup(func() { transnetv2.Helper = previous })
	transnetv2.Helper = os.Args[0]
	t.Setenv(fakeHelperModeEnv, mode)
	// under -race, a Go program exiting with status 0 sleeps a second first
	t.Setenv("GORACE", "atexit_sleep_ms=0")
}

// markersClip writes an FFV1 clip of n grey frames at 24 fps, 64x36: dark ones (a pattern of
// each scene, under 50 of 255, far below the floor once fed) and, at the indexes of markers, a
// flat frame of the level given. A marker is the last frame of its shot, as TransNetV2 scores it:
// the cut is on the frame after it.
func markersClip(t *testing.T, n int, markers map[int]byte) string {
	t.Helper()
	if _, err := exec.LookPath(ffmpeg.FFMPEGBinary); err != nil {
		t.Skipf("%s not found: %s", ffmpeg.FFMPEGBinary, err)
	}
	const width, height = 64, 36
	var raw bytes.Buffer
	scene := 0
	for i := range n {
		level, marker := markers[i]
		for y := range height {
			for x := range width {
				if marker {
					raw.WriteByte(level)
				} else {
					raw.WriteByte(byte((x*3 + y*5 + scene*17) % 50))
				}
			}
		}
		if marker {
			scene++
		}
	}
	path := filepath.Join(t.TempDir(), "markers.mkv")
	cmd := exec.CommandContext(t.Context(), ffmpeg.FFMPEGBinary, "-loglevel", "error", "-nostdin", "-y",
		"-f", "rawvideo", "-pix_fmt", "gray", "-s", fmt.Sprintf("%dx%d", width, height), "-r", "24", "-i", "pipe:0",
		"-c:v", "ffv1", "-pix_fmt", "yuv420p", path)
	cmd.Stdin = &raw
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to make the clip: %s\n%s", err, output)
	}
	return path
}

func scenesFramesOf(scenes []ffmpeg.Scene) []int {
	frames := make([]int, len(scenes))
	for i, scene := range scenes {
		frames[i] = scene.Frame
	}
	return frames
}

// TestDetectScenesTransNetV2 runs the real ffmpeg feed into the fake helper on a clip whose cuts
// are known: the fake derives each probability from the frame it receives, so the cuts coming
// out on the frame after each marker proves that frame N of the source is frame N of the cut list,
// through a helper answering 100 frames late. The threshold keeps the scenes whose score reaches
// it, and each reported score, handed back as a threshold, gives the same scenes again.
func TestDetectScenesTransNetV2(t *testing.T) {
	useFakeHelper(t, "brightness")
	const n = 180
	path := markersClip(t, n, map[int]byte{23: 255, 61: 150, 100: 200, 151: 120})
	var fed, scored atomic.Int64
	detect := func(threshold float64) []ffmpeg.Scene {
		t.Helper()
		scenes, err := DetectScenesTransNetV2(t.Context(), TransNetV2ScenesConfig{
			Path:                path,
			Threshold:           threshold,
			FrameRate:           "24",
			RuntimeError:        func(err error) { t.Errorf("unexpected runtime error: %s", err) },
			FramesFedReport:     func(frames int) { fed.Store(int64(frames)) },
			ProbabilitiesReport: func(probabilities int) { scored.Store(int64(probabilities)) },
		})
		if err != nil {
			t.Fatalf("threshold %v: %s", threshold, err)
		}
		return scenes
	}
	all := detect(transnetv2.ProbabilityFloor)
	if frames := scenesFramesOf(all); !slices.Equal(frames, []int{24, 62, 101, 152}) {
		t.Fatalf("expected the cuts at frames [24 62 101 152], got %v (%+v)", frames, all)
	}
	if fed.Load() != n || scored.Load() != n {
		t.Errorf("expected the last reports to be %d frames fed and %d scored, got %d and %d", n, n, fed.Load(), scored.Load())
	}
	for _, scene := range all {
		start, err := core.FramesDuration(scene.Frame, "24")
		if err != nil {
			t.Fatal(err)
		}
		if scene.Start != start {
			t.Errorf("frame %d: expected the start %v, got %v", scene.Frame, start, scene.Start)
		}
		if rounded := math.Round(scene.Score*1e4) / 1e4; scene.Score != rounded || scene.Score < transnetv2.ProbabilityFloor || scene.Score > 1 {
			t.Errorf("frame %d: score %v is not a probability rounded to 4 decimals", scene.Frame, scene.Score)
		}
	}
	// the brighter the marker, the higher the score
	if !(all[0].Score > all[2].Score && all[2].Score > all[1].Score && all[1].Score > all[3].Score) {
		t.Errorf("scores out of the markers' order: %+v", all)
	}
	// filtered by score: the 150 marker's score keeps it and the brighter ones
	if frames := scenesFramesOf(detect(all[1].Score)); !slices.Equal(frames, []int{24, 62, 101}) {
		t.Errorf("at the score of frame 62, expected the cuts [24 62 101], got %v", frames)
	}
	if frames := scenesFramesOf(detect(all[1].Score + 0.0001)); !slices.Equal(frames, []int{24, 101}) {
		t.Errorf("just above the score of frame 62, expected the cuts [24 101], got %v", frames)
	}
	// detecting again with the threshold reported for a scene, as printed, gives the same scenes
	for _, scene := range all {
		reusable := SceneDetectorTransNetV2.ReusableThreshold(scene.Score, transnetv2.ProbabilityFloor)
		printed, err := strconv.ParseFloat(SceneDetectorTransNetV2.FormatThreshold(reusable), 64)
		if err != nil {
			t.Fatal(err)
		}
		var want []ffmpeg.Scene
		for _, other := range all {
			if other.Score >= scene.Score {
				want = append(want, other)
			}
		}
		if got := detect(printed); !slices.Equal(got, want) {
			t.Errorf("threshold %v reported for frame %d: expected %+v, got %+v", printed, scene.Frame, want, got)
		}
	}
}

// TestDetectScenesTransNetV2Failures: a helper failure surfaces with its cause, a refused
// mid-stream change fails the detection as it does with scdet, and the inputs are checked.
func TestDetectScenesTransNetV2Failures(t *testing.T) {
	t.Run("helper failure", func(t *testing.T) {
		useFakeHelper(t, "fail")
		path := markersClip(t, 60, map[int]byte{23: 255})
		_, err := DetectScenesTransNetV2(t.Context(), TransNetV2ScenesConfig{Path: path, Threshold: 0.5, FrameRate: "24"})
		if err == nil || !strings.Contains(err.Error(), "fake: out of memory") || !strings.Contains(err.Error(), "exit status 3") {
			t.Errorf("expected the helper's exit status and stderr, got %v", err)
		}
	})
	t.Run("helper missing", func(t *testing.T) {
		previous := transnetv2.Helper
		defer func() { transnetv2.Helper = previous }()
		transnetv2.Helper = filepath.Join(t.TempDir(), "sptenc-detector-transnetv2")
		path := markersClip(t, 30, nil)
		if _, err := DetectScenesTransNetV2(t.Context(), TransNetV2ScenesConfig{Path: path, Threshold: 0.5, FrameRate: "24"}); err == nil ||
			!strings.Contains(err.Error(), "error starting "+transnetv2.Helper) {
			t.Errorf("expected an error starting the helper, got %v", err)
		}
	})
	t.Run("picture size change", func(t *testing.T) {
		useFakeHelper(t, "brightness")
		if _, err := exec.LookPath(ffmpeg.FFMPEGBinary); err != nil {
			t.Skipf("%s not found: %s", ffmpeg.FFMPEGBinary, err)
		}
		dir := t.TempDir()
		var parts []string
		for i, size := range []string{"320x240", "160x120"} {
			part := filepath.Join(dir, "part"+strconv.Itoa(i)+".ts")
			if output, err := exec.CommandContext(t.Context(), ffmpeg.FFMPEGBinary, "-loglevel", "error", "-nostdin", "-y",
				"-f", "lavfi", "-i", "testsrc=size="+size+":rate=24:duration=1", "-vf", "format=yuv420p,setsar=1",
				"-c:v", "mpeg2video", "-q:v", "2", "-g", "12", "-bf", "0", part).CombinedOutput(); err != nil {
				t.Fatalf("failed to make %s: %s\n%s", part, err, output)
			}
			parts = append(parts, part)
		}
		path := filepath.Join(dir, "sizechange.ts")
		if output, err := exec.CommandContext(t.Context(), ffmpeg.FFMPEGBinary, "-loglevel", "error", "-nostdin", "-y",
			"-i", "concat:"+strings.Join(parts, "|"), "-map", "0:v:0", "-c", "copy", path).CombinedOutput(); err != nil {
			t.Fatalf("failed to make %s: %s\n%s", path, err, output)
		}
		scenes, err := DetectScenesTransNetV2(t.Context(), TransNetV2ScenesConfig{Path: path, Threshold: 0.5, FrameRate: "24"})
		if err == nil {
			t.Errorf("expected an error, got the scenes %+v", scenes)
		} else if !strings.Contains(err.Error(), "changes its picture size or pixel format mid-stream (320x240 to 160x120)") {
			t.Errorf("expected the change reported, got %s", err)
		}
	})
	t.Run("inputs", func(t *testing.T) {
		for _, tc := range []struct {
			name   string
			config TransNetV2ScenesConfig
			want   string
		}{
			{"no path", TransNetV2ScenesConfig{Threshold: 0.5, FrameRate: "24"}, "input path cannot be empty"},
			{"under the floor", TransNetV2ScenesConfig{Path: "x.mkv", Threshold: 0.2, FrameRate: "24"}, "must be between 0.3 and 1"},
			{"above 1", TransNetV2ScenesConfig{Path: "x.mkv", Threshold: 1.5, FrameRate: "24"}, "must be between 0.3 and 1"},
			{"no frame rate", TransNetV2ScenesConfig{Path: "x.mkv", Threshold: 0.5}, "invalid frame rate"},
		} {
			if _, err := DetectScenesTransNetV2(t.Context(), tc.config); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("%s: expected an error containing %q, got %v", tc.name, tc.want, err)
			}
		}
	})
}

// TestTransNetV2ReusableThreshold_RoundingBoundaries: the property the reusable threshold exists
// for, on scores sitting on rounding boundaries. Each cut is one frame of the probability given
// between frames at 0; for every scene, its reusable threshold, printed and read back as the user
// pastes it, keeps it and gives exactly the scenes scoring as much or more.
func TestTransNetV2ReusableThreshold_RoundingBoundaries(t *testing.T) {
	var values []float32
	for _, v := range []float32{0.70005, 0.7, 0.69995, 0.30005, 0.3, 0.50015, 0.50025, 0.99995, 1} {
		// the value, and the float32 just under and just over it
		values = append(values, math.Nextafter32(v, 0), v, math.Nextafter32(v, 2))
	}
	var probabilities []float32
	for _, v := range values {
		if v > 1 {
			v = 1
		}
		probabilities = append(probabilities, 0, v)
	}
	probabilities = append(probabilities, 0)
	all, err := transnetv2Scenes(probabilities, transnetv2.ProbabilityFloor, "24000/1001")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) < 20 {
		t.Fatalf("expected most of the %d values to make a cut, got %d: %+v", len(values), len(all), all)
	}
	for _, scene := range all {
		reusable := SceneDetectorTransNetV2.ReusableThreshold(scene.Score, transnetv2.ProbabilityFloor)
		if reusable != scene.Score {
			t.Errorf("frame %d: expected the score %v itself as reusable threshold, got %v", scene.Frame, scene.Score, reusable)
		}
		printed, err := strconv.ParseFloat(SceneDetectorTransNetV2.FormatThreshold(reusable), 64)
		if err != nil {
			t.Fatal(err)
		}
		got, err := transnetv2Scenes(probabilities, printed, "24000/1001")
		if err != nil {
			t.Fatal(err)
		}
		var want []ffmpeg.Scene
		for _, other := range all {
			if other.Score >= scene.Score {
				want = append(want, other)
			}
		}
		if !slices.Equal(got, want) {
			t.Errorf("frame %d, score %v, threshold %v: expected %v, got %v", scene.Frame, scene.Score, printed,
				scenesFramesOf(want), scenesFramesOf(got))
		}
		if !slices.Contains(scenesFramesOf(got), scene.Frame) {
			t.Errorf("frame %d: lost by its own threshold %v", scene.Frame, printed)
		}
	}
}

// TestScdetReusableThreshold_RealDetection: the same property for scdet, for real (its threshold
// is compared by ffmpeg with an unrounded score): for every scene detected at a low threshold,
// detecting again at its reusable threshold, as printed, gives the scenes printed with a score as
// high or higher.
func TestScdetReusableThreshold_RealDetection(t *testing.T) {
	path := markersClip(t, 180, map[int]byte{23: 255, 61: 150, 100: 200, 151: 120, 160: 90})
	const minThreshold = 1
	all, err := ffmpeg.ScenesDetection(t.Context(), ffmpeg.ScenesDetectionConfig{Path: path, Threshold: minThreshold})
	if err != nil {
		t.Fatal(err)
	}
	if frames := scenesFramesOf(all); !slices.Equal(frames, []int{23, 61, 100, 151, 160}) {
		// scdet's score is the smaller of the frame's difference to the previous one and the change
		// of that difference: the frame after a marker scores low, the marker itself high
		t.Fatalf("expected a scene change on each marker, got %+v", all)
	}
	for _, scene := range all {
		reusable := SceneDetectorScdet.ReusableThreshold(scene.Score, minThreshold)
		printed, err := strconv.ParseFloat(SceneDetectorScdet.FormatThreshold(reusable), 64)
		if err != nil {
			t.Fatal(err)
		}
		got, err := ffmpeg.ScenesDetection(t.Context(), ffmpeg.ScenesDetectionConfig{Path: path, Threshold: printed})
		if err != nil {
			t.Fatal(err)
		}
		var want []int
		for _, other := range all {
			if other.Score >= scene.Score {
				want = append(want, other.Frame)
			}
		}
		if frames := scenesFramesOf(got); !slices.Equal(frames, want) {
			t.Errorf("frame %d, score %v, threshold %v: expected %v, got %v", scene.Frame, scene.Score, printed, want, frames)
		}
	}
}
