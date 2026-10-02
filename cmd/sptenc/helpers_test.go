package main

import (
	"context"
	"errors"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

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

// TestSourceYUVMatrix parses the RGB matrix flag for real: the matrix an RGB source is converted
// with, from its primaries or the flag, the refusal of primaries calling for none, and nothing for a
// source that is not RGB.
func TestSourceYUVMatrix(t *testing.T) {
	for _, tc := range []struct {
		name, pixFmt, primaries string
		args                    []string
		want                    ffmpeg.YUVMatrix
		refused                 bool
		says                    string
	}{
		{"YUV", "yuv420p10le", "bt709", nil, "", false, ""},
		{"YUV, flag set", "yuv420p", "bt709", []string{"--rgb-matrix", "bt2020nc"}, "", false,
			"WARNING: --rgb-matrix has no effect: the source is not RGB (yuv420p)"},
		{"RGB, BT.709 primaries", "gbrp16le", "bt709", nil, ffmpeg.YUVMatrixBT709, false,
			"RGB source (gbrp16le): converted to YUV with the bt709 matrix of its bt709 primaries"},
		{"RGB, BT.2020 primaries", "gbrp10le", "bt2020", nil, ffmpeg.YUVMatrixBT2020NC, false,
			"with the bt2020nc matrix of its bt2020 primaries"},
		{"RGB, no primaries", "rgb48le", "", nil, "", true, ""},
		{"RGB, unknown primaries", "bgr0", "unknown", nil, "", true, ""},
		{"RGB, SD primaries", "gbrp", "smpte170m", nil, "", true, ""},
		{"RGB, no primaries, flag set", "gbrp", "", []string{"--rgb-matrix", "bt470bg"}, ffmpeg.YUVMatrixBT601, false,
			"with the bt470bg matrix forced by --rgb-matrix"},
		{"RGB, flag over the primaries", "gbrp16le", "bt709", []string{"--rgb-matrix", "bt2020nc"}, ffmpeg.YUVMatrixBT2020NC, false,
			"with the bt2020nc matrix forced by --rgb-matrix"},
	} {
		var (
			matrix ffmpeg.YUVMatrix
			out    strings.Builder
		)
		cmd := &cli.Command{
			Name:  "test",
			Flags: []cli.Flag{rgbMatrixFlag("")},
			Action: func(ctx context.Context, cmd *cli.Command) (err error) {
				matrix, err = sourceYUVMatrix(cmd, &out, &ffmpeg.FFProbeBinaryStream{PixFmt: tc.pixFmt, ColorPrimaries: tc.primaries})
				return
			},
		}
		err := cmd.Run(context.Background(), append([]string{"test"}, tc.args...))
		switch {
		case tc.refused && err == nil:
			t.Errorf("%s: should be refused, got the %q matrix", tc.name, matrix)
		case tc.refused && !strings.Contains(err.Error(), "--rgb-matrix"):
			t.Errorf("%s: the refusal should point to --rgb-matrix: %s", tc.name, err)
		case !tc.refused && err != nil:
			t.Errorf("%s: %s", tc.name, err)
		case matrix != tc.want:
			t.Errorf("%s: want the %q matrix, got %q", tc.name, tc.want, matrix)
		case tc.says == "" && out.Len() > 0:
			t.Errorf("%s: unexpected output %q", tc.name, out.String())
		case !strings.Contains(out.String(), tc.says):
			t.Errorf("%s: expected %q in %q", tc.name, tc.says, out.String())
		}
	}
	// a value the flag does not take
	cmd := &cli.Command{Name: "test", Flags: []cli.Flag{rgbMatrixFlag("")}, Writer: io.Discard, ErrWriter: io.Discard,
		Action: func(context.Context, *cli.Command) error { return nil }}
	if err := cmd.Run(context.Background(), []string{"test", "--rgb-matrix", "gbr"}); err == nil {
		t.Error("--rgb-matrix gbr should be refused")
	}
}

// TestVMAFRGBMatrices parses the RGB matrix flag of the vmaf command for real: each RGB input gets
// the matrix encode would give it, a YUV one none.
func TestVMAFRGBMatrices(t *testing.T) {
	rgb := &ffmpeg.FFProbeBinaryStream{PixFmt: "gbrp16le", ColorPrimaries: "bt709"}
	rgb2020 := &ffmpeg.FFProbeBinaryStream{PixFmt: "gbrp10le", ColorPrimaries: "bt2020"}
	rgbUnknown := &ffmpeg.FFProbeBinaryStream{PixFmt: "rgb48le"}
	yuv := &ffmpeg.FFProbeBinaryStream{PixFmt: "yuv420p10le", ColorPrimaries: "bt709", ColorSpace: "bt709"}
	for _, tc := range []struct {
		name                 string
		reference, distorted *ffmpeg.FFProbeBinaryStream
		args                 []string
		wantReference        ffmpeg.YUVMatrix
		wantDistorted        ffmpeg.YUVMatrix
		refusal, says        string
	}{
		{"YUV inputs", yuv, yuv, nil, "", "", "", ""},
		{"YUV inputs, flag set", yuv, yuv, []string{"--rgb-matrix", "bt709"}, "", "", "",
			"WARNING: --rgb-matrix has no effect: neither the reference (yuv420p10le) nor the distorted video (yuv420p10le) is RGB"},
		{"RGB reference", rgb, yuv, nil, ffmpeg.YUVMatrixBT709, "", "",
			"RGB reference (gbrp16le): converted to YUV with the bt709 matrix of its bt709 primaries"},
		{"RGB distorted video", yuv, rgb2020, nil, "", ffmpeg.YUVMatrixBT2020NC, "",
			"RGB distorted video (gbrp10le): converted to YUV with the bt2020nc matrix of its bt2020 primaries"},
		{"RGB inputs", rgb, rgb, nil, ffmpeg.YUVMatrixBT709, ffmpeg.YUVMatrixBT709, "", ""},
		{"RGB reference without primaries", rgbUnknown, yuv, nil, "", "", "the reference is RGB (rgb48le) with unknown primaries", ""},
		{"RGB distorted video without primaries", yuv, rgbUnknown, nil, "", "", "the distorted video is RGB (rgb48le) with unknown primaries", ""},
		{"RGB inputs, flag set", rgbUnknown, rgb, []string{"--rgb-matrix", "bt470bg"}, ffmpeg.YUVMatrixBT601, ffmpeg.YUVMatrixBT601, "",
			"with the bt470bg matrix forced by --rgb-matrix"},
	} {
		var (
			referenceMatrix, distortedMatrix ffmpeg.YUVMatrix
			out                              strings.Builder
		)
		cmd := &cli.Command{
			Name:  "test",
			Flags: []cli.Flag{rgbMatrixFlag("")},
			Action: func(ctx context.Context, cmd *cli.Command) (err error) {
				referenceMatrix, distortedMatrix, err = vmafRGBMatrices(cmd, &out, tc.reference, tc.distorted)
				return
			},
		}
		err := cmd.Run(context.Background(), append([]string{"test"}, tc.args...))
		switch {
		case tc.refusal != "" && (err == nil || !strings.Contains(err.Error(), tc.refusal)):
			t.Errorf("%s: want the refusal %q, got %v", tc.name, tc.refusal, err)
		case tc.refusal == "" && err != nil:
			t.Errorf("%s: %s", tc.name, err)
		case referenceMatrix != tc.wantReference || distortedMatrix != tc.wantDistorted:
			t.Errorf("%s: want the matrices %q and %q, got %q and %q", tc.name, tc.wantReference, tc.wantDistorted, referenceMatrix, distortedMatrix)
		case !strings.Contains(out.String(), tc.says):
			t.Errorf("%s: expected %q in %q", tc.name, tc.says, out.String())
		}
	}
}

// TestVMAFColorWarnings checks what the vmaf command says about the colors its inputs declare: the
// differences, an RGB input counting as converted, and nothing when they agree.
func TestVMAFColorWarnings(t *testing.T) {
	stream := func(pixFmt, matrix, colorRange, primaries, transfer string) *ffmpeg.FFProbeBinaryStream {
		return &ffmpeg.FFProbeBinaryStream{PixFmt: pixFmt, ColorSpace: matrix, ColorRange: colorRange,
			ColorPrimaries: primaries, ColorTransfer: transfer}
	}
	yuv709 := stream("yuv420p10le", "bt709", "tv", "bt709", "bt709")
	rgb709 := stream("gbrp16le", "gbr", "pc", "bt709", "bt709")
	for _, tc := range []struct {
		name                                 string
		reference, distorted                 *ffmpeg.FFProbeBinaryStream
		referenceRGBToYUV, distortedRGBToYUV ffmpeg.YUVMatrix
		want                                 []string
	}{
		{"same colors", yuv709, yuv709, "", "", nil},
		{"RGB reference converted with the matrix of the distorted video", rgb709, yuv709, ffmpeg.YUVMatrixBT709, "", nil},
		{"both undeclared", stream("yuv420p", "", "", "", ""), stream("yuv420p", "unknown", "unknown", "unknown", "unknown"), "", "", nil},
		{"limited range and none", yuv709, stream("yuv420p10le", "bt709", "", "bt709", "bt709"), "", "", nil},
		{"matrices", yuv709, stream("yuv420p10le", "smpte170m", "tv", "bt709", "bt709"), "", "", []string{
			"the reference declares the bt709 matrix, the distorted video declares the smpte170m matrix: VMAF compares the pictures as they are, neither converted into the matrix of the other (see MANUAL.md, What VMAF sees)"}},
		{"RGB reference, another matrix", rgb709, stream("yuv420p10le", "smpte170m", "tv", "bt709", "bt709"), ffmpeg.YUVMatrixBT709, "", []string{
			"the reference is converted from RGB with the bt709 matrix, the distorted video declares the smpte170m matrix: VMAF compares the pictures as they are, neither converted into the matrix of the other (see MANUAL.md, What VMAF sees)"}},
		{"ranges", stream("yuv444p10le", "bt709", "pc", "bt709", "bt709"), yuv709, "", "", []string{
			"the reference declares full range (pc), the distorted video declares limited range (tv): ffmpeg converts one into the range of the other before VMAF compares them, which is right only if both declare it truly"}},
		{"full range and none", yuv709, stream("yuv420p10le", "bt709", "", "bt709", "bt709"), "", "", nil},
		{"none and full range", stream("yuv420p10le", "bt709", "unknown", "bt709", "bt709"), stream("yuv420p10le", "bt709", "pc", "bt709", "bt709"), "", "", []string{
			"the reference declares no range, which ffmpeg reads as limited, the distorted video declares full range (pc): ffmpeg converts one into the range of the other before VMAF compares them, which is right only if both declare it truly"}},
		{"RGB distorted video, full range reference", stream("yuv444p10le", "bt709", "pc", "bt709", "bt709"), rgb709, "", ffmpeg.YUVMatrixBT709, []string{
			"the reference declares full range (pc), the distorted video is converted from RGB to limited range: ffmpeg converts one into the range of the other before VMAF compares them, which is right only if both declare it truly"}},
		{"primaries and transfers", yuv709, stream("yuv420p10le", "bt709", "tv", "bt2020", "smpte2084"), "", "", []string{
			"the reference declares bt709 primaries, the distorted video bt2020 ones: VMAF compares the pictures as they are, whatever colors each is meant to show",
			"the reference declares the bt709 transfer, the distorted video the smpte2084 one: VMAF compares the pictures as they are, whatever light each is meant to show"}},
		{"distorted video declaring nothing", yuv709, stream("yuv420p10le", "", "", "", ""), "", "", []string{
			"the distorted video declares no matrix, primaries or transfer, where the reference declares bt709, bt709 and bt709: VMAF compares the pictures as they are, as if it declared the same"}},
		{"reference without matrix", stream("yuv420p10le", "", "tv", "bt709", "bt709"), yuv709, "", "", []string{
			"the reference declares no matrix, where the distorted video declares bt709: VMAF compares the pictures as they are, as if it declared the same"}},
	} {
		if got := vmafColorWarnings(tc.reference, tc.distorted, tc.referenceRGBToYUV, tc.distortedRGBToYUV); !slices.Equal(got, tc.want) {
			t.Errorf("%s:\n got %q\nwant %q", tc.name, got, tc.want)
		}
	}
}

// TestProbePreSplitSegments probes real segments: their durations, and a directory mixing RGB and
// YUV segments refused, whichever comes first.
func TestProbePreSplitSegments(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	yuv1, yuv2 := testSegment(t, ctx, dir, "seg_01.mkv", "yuv420p10le"), testSegment(t, ctx, dir, "seg_02.mkv", "yuv420p")
	rgb1, rgb2 := testSegment(t, ctx, dir, "seg_03.mkv", "gbrp10le"), testSegment(t, ctx, dir, "seg_04.mkv", "gbrp16le")
	for _, segments := range [][]string{{yuv1, yuv2}, {rgb1, rgb2}} {
		durations, err := probePreSplitSegments(ctx, segments, false)
		if err != nil {
			t.Fatalf("%v: %s", segments, err)
		}
		if want := []time.Duration{500 * time.Millisecond, 500 * time.Millisecond}; !slices.Equal(durations, want) {
			t.Errorf("%v: want the durations %v, got %v", segments, want, durations)
		}
	}
	for _, tc := range []struct {
		segments []string
		refusal  string
	}{
		{[]string{yuv1, rgb1}, "segment seg_03.mkv holds RGB pictures where seg_01.mkv holds YUV ones"},
		{[]string{rgb1, rgb2, yuv2}, "segment seg_02.mkv holds YUV pictures where seg_03.mkv holds RGB ones"},
	} {
		if _, err := probePreSplitSegments(ctx, tc.segments, false); err == nil || !strings.Contains(err.Error(), tc.refusal) {
			t.Errorf("want %q, got %v", tc.refusal, err)
		}
	}
}

// TestConvertRGBSegments converts real RGB segments: their masters are named as the segments of a
// source, hold their frames, and declare the matrix they were converted with.
func TestConvertRGBSegments(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	segments := []string{testSegment(t, ctx, dir, "upscaled_a.mkv", "gbrp16le"), testSegment(t, ctx, dir, "upscaled_b.mkv", "gbrp10le")}
	out := filepath.Join(dir, "work")
	if err := os.Mkdir(out, 0o755); err != nil {
		t.Fatal(err)
	}
	masters, err := convertRGBSegments(ctx, segments, []time.Duration{500 * time.Millisecond, 500 * time.Millisecond}, out,
		ffmpeg.YUVMatrixBT709, false)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{filepath.Join(out, "seg_000000.mkv"), filepath.Join(out, "seg_000001.mkv")}; !slices.Equal(masters, want) {
		t.Fatalf("want the masters %v, got %v", want, masters)
	}
	for _, master := range masters {
		stats, err := ffmpeg.GetStreamsInfos(ctx, ffmpeg.GetStreamsInfosConfig{Path: master, CountPackets: true})
		if err != nil {
			t.Fatal(err)
		}
		video := stats.VideoTrack()
		if got, want := [4]string{video.PixFmt, video.ColorSpace, video.ColorRange, video.ChromaLocation},
			[4]string{"yuv420p10le", "bt709", "tv", "left"}; got != want || video.NbReadPackets != 12 {
			t.Errorf("%s: want %v and 12 frames, got %v and %d", master, want, got, video.NbReadPackets)
		}
	}
}

// testSegment writes a segment of 12 frames (0.5 s at 24 fps) in the pixel format given, declaring
// BT.709 primaries, as an upscaler of a BT.709 source does.
func testSegment(t *testing.T, ctx context.Context, dir, name, pixFmt string) string {
	t.Helper()
	for _, bin := range []string{ffmpeg.FFMPEGBinary, ffmpeg.FFProbeBinary} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not found: %s", bin, err)
		}
	}
	path := filepath.Join(dir, name)
	if output, err := exec.CommandContext(ctx, ffmpeg.FFMPEGBinary, "-loglevel", "error", "-nostdin", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=64x64:rate=24:duration=0.5", "-pix_fmt", pixFmt,
		"-color_primaries", "bt709", "-color_trc", "bt709", "-c:v", "ffv1", path).CombinedOutput(); err != nil {
		t.Fatalf("failed to create %s: %s\n%s", name, err, output)
	}
	return path
}
