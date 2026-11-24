package ffmpeg

import (
	"context"
	"io"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Real output of ffmpeg n9.0.2 (setpts, scdet then metadata filters), other lines included.
const scdetOutput = `Input #0, mov,mp4,m4a,3gp,3g2,mj2, from 'src.mp4':
  Stream #0:0[0x1](und): Video: h264 (High 4:4:4 Predictive), yuv420p, 160x90, 23.98 fps
[Parsed_scdet_1 @ 0x7eec58003240] lavfi.scd.score: 41.160, lavfi.scd.time: 4.045708
[Parsed_metadata_2 @ 0x7eec58003500] frame:97   pts:97097   pts_time:4.045708
[Parsed_metadata_2 @ 0x7eec58003500] lavfi.scd.time=4.045708
[Parsed_scdet_1 @ 0x7eec58003240] lavfi.scd.score: 40.408, lavfi.scd.time: 8.091417
[Parsed_metadata_2 @ 0x7eec58003500] frame:194  pts:194194  pts_time:8.091417
[Parsed_metadata_2 @ 0x7eec58003500] lavfi.scd.time=8.091417
[Parsed_scdet_1 @ 0x7eec58003240] lavfi.scd.score: 14.003, lavfi.scd.time: 1011.427083
[Parsed_metadata_2 @ 0x7eec58003500] frame:24250 pts:24274250 pts_time:1011.427083
[Parsed_metadata_2 @ 0x7eec58003500] lavfi.scd.time=1011.427083
[out#0/null @ 0x5581] video:618KiB audio:0KiB
`

func TestScdetProgress(t *testing.T) {
	scenes, err := scdetProgress(io.NopCloser(strings.NewReader(scdetOutput)), nil, func(err error) {
		t.Errorf("unexpected runtime error: %v", err)
	})
	if err != nil {
		t.Fatalf("scdetProgress failed: %v", err)
	}
	expected := []Scene{
		{Frame: 97, Start: 4046 * time.Millisecond, Score: 41.16},
		{Frame: 194, Start: 8091 * time.Millisecond, Score: 40.408},
		{Frame: 24250, Start: 1011427 * time.Millisecond, Score: 14.003}, // no space left after the frame index
	}
	if len(scenes) != len(expected) {
		t.Fatalf("expected %d scenes, got %v", len(expected), scenes)
	}
	for i := range expected {
		if scenes[i] != expected[i] {
			t.Errorf("scene %d: expected %+v, got %+v", i, expected[i], scenes[i])
		}
	}
}

// A scene which can not get its frame index must fail the detection: it can not be cut, and
// dropping it would silently change the scenes (see scdetProgress).
func TestScdetProgress_MissingFrameIndex(t *testing.T) {
	for name, output := range map[string]string{
		"followed by another scene": "[Parsed_scdet_1 @ 0x1] lavfi.scd.score: 41.160, lavfi.scd.time: 4.045708\n" +
			"[Parsed_scdet_1 @ 0x1] lavfi.scd.score: 40.408, lavfi.scd.time: 8.091417\n" +
			"[Parsed_metadata_2 @ 0x2] frame:194  pts:194194  pts_time:8.091417\n",
		"last line": "[Parsed_scdet_1 @ 0x1] lavfi.scd.score: 41.160, lavfi.scd.time: 4.045708\n",
		"unparsable": "[Parsed_scdet_1 @ 0x1] lavfi.scd.score: 41.160, lavfi.scd.time: 4.045708\n" +
			"[Parsed_metadata_2 @ 0x2] frame:abc  pts:194194  pts_time:8.091417\n",
		"frame 0": "[Parsed_scdet_1 @ 0x1] lavfi.scd.score: 41.160, lavfi.scd.time: 0\n" +
			"[Parsed_metadata_2 @ 0x2] frame:0    pts:0       pts_time:0\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := scdetProgress(io.NopCloser(strings.NewReader(output)), nil, nil); err == nil {
				t.Error("expected an error, got nil")
			}
		})
	}
}

// A frame of another picture size, pixel format or range than the graph's stops the detection, and
// so does one of another matrix where the graph's first frame declares one; a colour description
// appearing does not, nor a switch between BT.601's two labels (see scdetProgress). Lines of ffmpeg
// n9.0.1, decoded in software (fmt: 0) and by VideoToolbox (fmt: 23), and two lines not captured
// from a real ffmpeg: one without the colour description, which can not rule a change out, and an
// unreadable one.
func TestScdetProgress_PictureChange(t *testing.T) {
	for name, tc := range map[string]struct {
		line    string
		wantErr bool
	}{
		"colour description":                 {"[graph -1 input from stream 0:0 @ 0x1] filter context - w: 320 h: 240 fmt: 0 csp: unknown range: unknown alpha: unspecified, incoming frame - w: 320 h: 240 fmt: 0 csp: bt709 range: tv alpha: unspecified pts_time: 0.583333", false},
		"colour description, VideoToolbox":   {"[graph -1 input from stream 0:0 @ 0x1] filter context - w: 320 h: 240 fmt: 23 csp: unknown range: unknown alpha: unspecified, incoming frame - w: 320 h: 240 fmt: 23 csp: bt709 range: tv alpha: unspecified pts_time: 0.583333", false},
		"BT.601's two labels":                {"[graph -1 input from stream 0:0 @ 0x1] filter context - w: 320 h: 240 fmt: 0 csp: smpte170m range: tv alpha: unspecified, incoming frame - w: 320 h: 240 fmt: 0 csp: bt470bg range: tv alpha: unspecified pts_time: 0.583333", false},
		"BT.601's two labels, the other way": {"[graph -1 input from stream 0:0 @ 0x1] filter context - w: 320 h: 240 fmt: 23 csp: bt470bg range: tv alpha: unspecified, incoming frame - w: 320 h: 240 fmt: 23 csp: smpte170m range: tv alpha: unspecified pts_time: 0.583333", false},
		"size":                               {"[graph -1 input from stream 0:0 @ 0x1] filter context - w: 320 h: 240 fmt: 0 csp: unknown range: unknown alpha: unspecified, incoming frame - w: 160 h: 120 fmt: 0 csp: bt709 range: tv alpha: unspecified pts_time: 0.583333", true},
		"pixel format":                       {"[graph -1 input from stream 0:0 @ 0x1] filter context - w: 320 h: 240 fmt: 0 csp: unknown range: unknown alpha: unspecified, incoming frame - w: 320 h: 240 fmt: 64 csp: unknown range: unknown alpha: unspecified pts_time: 0.583333", true},
		"range":                              {"[graph -1 input from stream 0:0 @ 0x1] filter context - w: 320 h: 240 fmt: 0 csp: unknown range: unknown alpha: unspecified, incoming frame - w: 320 h: 240 fmt: 0 csp: unknown range: pc alpha: unspecified pts_time: 0.583333", true},
		"range, VideoToolbox":                {"[graph -1 input from stream 0:0 @ 0x1] filter context - w: 320 h: 240 fmt: 23 csp: unknown range: unknown alpha: unspecified, incoming frame - w: 320 h: 240 fmt: 23 csp: unknown range: pc alpha: unspecified pts_time: 0.583333", true},
		"matrix":                             {"[graph -1 input from stream 0:0 @ 0x1] filter context - w: 320 h: 240 fmt: 0 csp: smpte170m range: tv alpha: unspecified, incoming frame - w: 320 h: 240 fmt: 0 csp: bt709 range: tv alpha: unspecified pts_time: 0.583333", true},
		"matrix, VideoToolbox":               {"[graph -1 input from stream 0:0 @ 0x1] filter context - w: 320 h: 240 fmt: 23 csp: smpte170m range: tv alpha: unspecified, incoming frame - w: 320 h: 240 fmt: 23 csp: bt709 range: tv alpha: unspecified pts_time: 0.583333", true},
		"no colour description":              {"[graph -1 input from stream 0:0 @ 0x1] filter context - w: 320 h: 240 fmt: 0, incoming frame - w: 320 h: 240 fmt: 0 pts_time: 0.583333", true},
		"unreadable":                         {"[graph -1 input from stream 0:0 @ 0x1] filter context - 320x240, incoming frame - 352x288", true},
	} {
		t.Run(name, func(t *testing.T) {
			output := "[Parsed_scdet_1 @ 0x1] lavfi.scd.score: 41.160, lavfi.scd.time: 0.291667\n" +
				"[Parsed_metadata_2 @ 0x2] frame:7    pts:26250   pts_time:0.291667\n" + tc.line + "\n"
			scenes, err := scdetProgress(io.NopCloser(strings.NewReader(output)), nil, nil)
			if tc.wantErr && err == nil {
				t.Errorf("expected an error, got the scenes %+v", scenes)
			}
			if !tc.wantErr && (err != nil || len(scenes) != 1) {
				t.Errorf("expected the scene alone, got %+v (%v)", scenes, err)
			}
		})
	}
}

func TestSegment_InvalidFrames(t *testing.T) {
	for name, frames := range map[string][]int{
		"missing frame index": {0},
		"not increasing":      {97, 97},
		"decreasing":          {194, 97},
	} {
		t.Run(name, func(t *testing.T) {
			err := Segment(t.Context(), SegmentConfig{Input: "in.mkv", OutputDir: t.TempDir(), ScenesFrames: frames})
			if err == nil || !strings.Contains(err.Error(), "strictly increasing") {
				t.Errorf("expected a frames validation error, got %v", err)
			}
		})
	}
}

// TestScenesDetectionFirstVideoAlone detects for real the scenes of files holding more than
// their video: the scenes are those of the first video stream, the one the master is made of,
// whatever stream ffmpeg would pick by itself, and no audio is decoded, a source whose audio can
// not be decoded having its scenes detected like any other. Both failed before: the scene of the
// first case was not found, and the detection of the second ended in an error.
func TestScenesDetectionFirstVideoAlone(t *testing.T) {
	for _, bin := range []string{FFMPEGBinary, FFProbeBinary} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not found: %s", bin, err)
		}
	}
	ctx := context.Background()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		if output, err := exec.CommandContext(ctx, FFMPEGBinary, append([]string{"-loglevel", "error", "-nostdin", "-y"}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("ffmpeg %v: %s\n%s", args, err, output)
		}
	}
	// two scenes of 12 frames, with AC-3 audio
	twoScenes := filepath.Join(dir, "twoscenes.mkv")
	run("-f", "lavfi", "-i", "testsrc2=size=320x180:rate=24:duration=0.5", "-f", "lavfi", "-i", "smptebars=size=320x180:rate=24:duration=0.5",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration=1",
		"-filter_complex", "[0:v]format=yuv420p,setsar=1[a];[1:v]format=yuv420p,setsar=1[b];[a][b]concat=n=2:v=1:a=0[v]",
		"-map", "[v]", "-map", "2:a", "-c:v", "mpeg4", "-q:v", "5", "-c:a", "ac3", twoScenes)
	// the same next to a larger video of a single scene, flagged as the default one
	twoVideos := filepath.Join(dir, "twovideos.mkv")
	run("-i", twoScenes, "-f", "lavfi", "-i", "testsrc2=size=640x360:rate=24:duration=1", "-map", "0:v:0", "-map", "1:v:0", "-map", "0:a:0",
		"-c:v:0", "copy", "-c:v:1", "mpeg4", "-q:v:1", "5", "-c:a", "copy", "-disposition:v:0", "0", "-disposition:v:1", "default", twoVideos)
	// the two scenes with every byte of their audio packets damaged
	audioDamaged := filepath.Join(dir, "audiodamaged.mkv")
	run("-i", twoScenes, "-map", "0", "-c", "copy", "-bsf:a", "noise=amount=1", audioDamaged)
	for _, tc := range []struct{ name, path string }{
		{"video and audio", twoScenes},
		{"a second video stream, the default one", twoVideos},
		{"audio that can not be decoded", audioDamaged},
	} {
		scenes, err := ScenesDetection(ctx, ScenesDetectionConfig{Path: tc.path, Threshold: 10})
		if err != nil {
			t.Errorf("%s: %s", tc.name, err)
			continue
		}
		if len(scenes) != 1 || scenes[0].Frame != 12 {
			t.Errorf("%s: want the scene change at frame 12 alone, got %+v", tc.name, scenes)
		}
	}
	// what ffmpeg picks by itself in the second file: its second video, which holds no scene change
	alone := filepath.Join(dir, "second.mkv")
	run("-i", twoVideos, "-map", "0:v:1", "-c", "copy", alone)
	if scenes, err := ScenesDetection(ctx, ScenesDetectionConfig{Path: alone, Threshold: 10}); err != nil || len(scenes) != 0 {
		t.Fatalf("the second video was expected without a scene change, got %+v (%v)", scenes, err)
	}
}

// TestScenesDetectionPropertiesChange detects for real the scenes of an MPEG-2 stream whose
// second part declares a colour description the first one does not, which made ffmpeg rebuild
// the filter graph (see WHY ONE FILTER GRAPH FOR EVERY FRAME in ScenesDetection): the cuts after
// the change are numbered from the first frame. They came out at 7, 1 and 13 before. A second
// part of another picture size fails the detection. With libx264, so do H.264 sources whose range
// or matrix changes mid-stream, which the final VMAF refuses (see scdetProgress), and a switch
// between BT.601's two labels does not; both changes went through before.
func TestScenesDetectionPropertiesChange(t *testing.T) {
	if _, err := exec.LookPath(FFMPEGBinary); err != nil {
		t.Skipf("%s not found: %s", FFMPEGBinary, err)
	}
	ctx := context.Background()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		if output, err := exec.CommandContext(ctx, FFMPEGBinary, append([]string{"-loglevel", "error", "-nostdin", "-y"}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("ffmpeg %v: %s\n%s", args, err, output)
		}
	}
	mpeg2 := []string{"-c:v", "mpeg2video", "-q:v", "2", "-g", "12", "-bf", "0"}
	bt709 := []string{"-color_primaries", "bt709", "-color_trc", "bt709", "-colorspace", "bt709", "-color_range", "tv"}
	twoScenes := func(scale string, frames int) string {
		return "[0:v]trim=end_frame=" + strconv.Itoa(frames) + ",format=yuv420p" + scale + ",setsar=1[a];[1:v]trim=end_frame=" + strconv.Itoa(frames) +
			",format=yuv420p" + scale + ",setsar=1[b];[a][b]concat=n=2:v=1:a=0[v]"
	}
	// frames 0 to 13, no colour description, a cut at 7
	first := filepath.Join(dir, "first.ts")
	run(append([]string{"-f", "lavfi", "-i", "testsrc2=size=320x240:rate=24:duration=1", "-f", "lavfi", "-i", "smptebars=size=320x240:rate=24:duration=1",
		"-filter_complex", twoScenes("", 7), "-map", "[v]"}, append(mpeg2, first)...)...)
	// frames 14 to 37, BT.709, cuts at 14 and 26
	second := filepath.Join(dir, "second.ts")
	run(append(append([]string{"-f", "lavfi", "-i", "testsrc=size=320x240:rate=24:duration=1", "-f", "lavfi", "-i", "mandelbrot=size=320x240:rate=24",
		"-filter_complex", twoScenes("", 12), "-map", "[v]"}, mpeg2...), append(bt709, second)...)...)
	// the same at another picture size
	smaller := filepath.Join(dir, "smaller.ts")
	run(append(append([]string{"-f", "lavfi", "-i", "testsrc=size=320x240:rate=24:duration=1", "-f", "lavfi", "-i", "mandelbrot=size=320x240:rate=24",
		"-filter_complex", twoScenes(",scale=160:120", 12), "-map", "[v]"}, mpeg2...), append(bt709, smaller)...)...)
	colourChange := filepath.Join(dir, "colourchange.ts")
	run("-i", "concat:"+first+"|"+second, "-map", "0:v:0", "-c", "copy", colourChange)
	sizeChange := filepath.Join(dir, "sizechange.ts")
	run("-i", "concat:"+first+"|"+smaller, "-map", "0:v:0", "-c", "copy", sizeChange)
	scenes, err := ScenesDetection(ctx, ScenesDetectionConfig{Path: colourChange, Threshold: 10})
	if err != nil {
		t.Fatalf("colour description change: %s", err)
	}
	var frames []int
	for _, scene := range scenes {
		frames = append(frames, scene.Frame)
	}
	if !slices.Equal(frames, []int{7, 14, 26}) {
		t.Errorf("colour description change: want the scene changes at frames [7 14 26], got %v (%+v)", frames, scenes)
	}
	if scenes, err = ScenesDetection(ctx, ScenesDetectionConfig{Path: sizeChange, Threshold: 10}); err == nil {
		t.Errorf("picture size change: expected an error, got the scenes %+v", scenes)
	} else if !strings.Contains(err.Error(), "changes its picture size or pixel format mid-stream") {
		t.Errorf("picture size change: expected the change reported, got %s", err)
	}
	// H.264 (MPEG-2 has no range to declare): the same pictures in two parts, frames 0 to 13 and 14
	// to 37, their colour description written into the bitstream alone (a metadata bitstream filter)
	x264 := []string{"-c:v", "libx264", "-qp", "10", "-g", "12", "-bf", "0"}
	h264TwoParts := func(name, firstBSF, secondBSF string) (string, bool) {
		var parts []string
		for i, part := range []struct{ source, bsf string }{{"testsrc2=size=320x240:rate=24", firstBSF}, {"testsrc=size=320x240:rate=24", secondBSF}} {
			parts = append(parts, filepath.Join(dir, name+"_"+strconv.Itoa(i)+".ts"))
			args := append([]string{"-f", "lavfi", "-i", part.source, "-frames:v", strconv.Itoa(14 + 10*i), "-vf", "format=yuv420p,setsar=1"}, x264...)
			if part.bsf != "" {
				args = append(args, "-bsf:v", part.bsf)
			}
			if output, err := exec.CommandContext(ctx, FFMPEGBinary, append(append([]string{"-loglevel", "error", "-nostdin", "-y"}, args...), parts[i])...).CombinedOutput(); err != nil {
				t.Logf("can not make the H.264 clips (libx264 needed): %s\n%s", err, output)
				return "", false
			}
		}
		path := filepath.Join(dir, name+".ts")
		run("-i", "concat:"+strings.Join(parts, "|"), "-map", "0:v:0", "-c", "copy", path)
		return path, true
	}
	const (
		h264BT601   = "h264_metadata=colour_primaries=6:transfer_characteristics=6:matrix_coefficients=6"
		h264BT470BG = "h264_metadata=colour_primaries=5:transfer_characteristics=6:matrix_coefficients=5"
		h264BT709   = "h264_metadata=colour_primaries=1:transfer_characteristics=1:matrix_coefficients=1"
	)
	for _, tc := range []struct{ name, firstBSF, secondBSF, want string }{
		{"range change", "", "h264_metadata=video_full_range_flag=1", "changes its range (tv to pc) mid-stream"},
		{"matrix change", h264BT601, h264BT709, "changes its matrix (smpte170m to bt709) mid-stream"},
		{"BT.601's two labels", h264BT601, h264BT470BG, ""},
	} {
		source, ok := h264TwoParts(strings.ReplaceAll(tc.name, " ", "_"), tc.firstBSF, tc.secondBSF)
		if !ok {
			t.Skip("libx264 is needed for the range and matrix changes")
		}
		scenes, err := ScenesDetection(ctx, ScenesDetectionConfig{Path: source, Threshold: 10})
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("%s: %s", tc.name, err)
		case tc.want == "" && (len(scenes) != 1 || scenes[0].Frame != 14):
			t.Errorf("%s: want the scene change at frame 14 alone, got %+v", tc.name, scenes)
		case tc.want != "" && err == nil:
			t.Errorf("%s: expected an error, got the scenes %+v", tc.name, scenes)
		case tc.want != "" && !strings.Contains(err.Error(), tc.want):
			t.Errorf("%s: expected %q in the error, got %s", tc.name, tc.want, err)
		}
	}
}
