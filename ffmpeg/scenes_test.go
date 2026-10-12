package ffmpeg

import (
	"context"
	"io"
	"os/exec"
	"path/filepath"
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
