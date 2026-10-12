package ffmpeg

import (
	"context"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestGenerateConcatList(t *testing.T) {
	tmpDir := t.TempDir()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get the current directory: %v", err)
	}
	absFile := filepath.Join(tmpDir, `we'ird`, "seg_000001.mkv")

	listPath, err := GenerateConcatList(tmpDir, []string{
		filepath.Join("relative", "seg_000000.mkv"),
		absFile,
	}, nil)
	if err != nil {
		t.Fatalf("GenerateConcatList failed: %v", err)
	}
	content, err := os.ReadFile(string(listPath))
	if err != nil {
		t.Fatalf("failed to read the generated list: %v", err)
	}

	expected := concatScriptHeader + "\n" +
		// relative paths must be made absolute: ffmpeg resolves them against the list directory
		"file '" + filepath.Join(cwd, "relative", "seg_000000.mkv") + "'\n" +
		// quotes must be escaped the ffmpeg way
		"file '" + strings.ReplaceAll(absFile, `'`, `'\''`) + "'\n"
	if string(content) != expected {
		t.Errorf("unexpected concat list:\n%s\nexpected:\n%s", content, expected)
	}
}

func TestGenerateConcatList_NoFiles(t *testing.T) {
	if _, err := GenerateConcatList(t.TempDir(), nil, nil); err == nil {
		t.Error("expected an error with no files")
	}
}

func TestGenerateConcatList_Durations(t *testing.T) {
	tmpDir := t.TempDir()
	files := []string{
		filepath.Join(tmpDir, "seg_000000.mkv"),
		filepath.Join(tmpDir, "seg_000001.mkv"),
	}
	// 60 and 886 frames at 24000/1001 fps, rounded to the microsecond
	listPath, err := GenerateConcatList(tmpDir, files, []time.Duration{
		2502500 * time.Microsecond,
		36953583 * time.Microsecond,
	})
	if err != nil {
		t.Fatalf("GenerateConcatList failed: %v", err)
	}
	content, err := os.ReadFile(string(listPath))
	if err != nil {
		t.Fatalf("failed to read the generated list: %v", err)
	}
	expected := concatScriptHeader + "\n" +
		"file '" + files[0] + "'\n" +
		"duration 2.502500\n" +
		"file '" + files[1] + "'\n" +
		"duration 36.953583\n"
	if string(content) != expected {
		t.Errorf("unexpected concat list:\n%s\nexpected:\n%s", content, expected)
	}
}

func TestGenerateConcatList_DurationsMismatch(t *testing.T) {
	tmpDir := t.TempDir()
	files := []string{filepath.Join(tmpDir, "a.mkv"), filepath.Join(tmpDir, "b.mkv")}
	if _, err := GenerateConcatList(tmpDir, files, []time.Duration{time.Second}); err == nil {
		t.Error("expected an error with fewer durations than files")
	}
	if _, err := GenerateConcatList(tmpDir, files, []time.Duration{time.Second, -time.Second}); err == nil {
		t.Error("expected an error with a negative duration")
	}
}

func TestFrameGridSnap(t *testing.T) {
	tests := []struct {
		frameRate string
		expected  string
	}{
		{"24000/1001", "setts=pts='if(eq(PTS,NOPTS),NOPTS,STARTPTS+round(round((PTS-STARTPTS)*TB*24000/1001)*1001/TB/24000))'" +
			":dts='if(eq(DTS,NOPTS),NOPTS,STARTPTS+round(round((DTS-STARTPTS)*TB*24000/1001)*1001/TB/24000))'"},
		{"25", "setts=pts='if(eq(PTS,NOPTS),NOPTS,STARTPTS+round(round((PTS-STARTPTS)*TB*25/1)*1/TB/25))'" +
			":dts='if(eq(DTS,NOPTS),NOPTS,STARTPTS+round(round((DTS-STARTPTS)*TB*25/1)*1/TB/25))'"},
		// 59.94 fps as the Matroska demuxer reads it
		{"19001/317", "setts=pts='if(eq(PTS,NOPTS),NOPTS,STARTPTS+round(round((PTS-STARTPTS)*TB*19001/317)*317/TB/19001))'" +
			":dts='if(eq(DTS,NOPTS),NOPTS,STARTPTS+round(round((DTS-STARTPTS)*TB*19001/317)*317/TB/19001))'"},
	}
	for _, tt := range tests {
		bsf, err := frameGridSnap(tt.frameRate)
		if err != nil {
			t.Errorf("frameGridSnap(%q) failed: %v", tt.frameRate, err)
			continue
		}
		if bsf != tt.expected {
			t.Errorf("frameGridSnap(%q) = %s\nexpected %s", tt.frameRate, bsf, tt.expected)
		}
	}
}

func TestFrameGridSnap_InvalidFrameRate(t *testing.T) {
	for _, frameRate := range []string{"", "23.976", "0/1", "24000/0", "-24000/1001", "24000/-1001", "24000/1001/1", "abc"} {
		if _, err := frameGridSnap(frameRate); err == nil {
			t.Errorf("expected an error with frame rate %q", frameRate)
		}
	}
}

// TestFrameGridSnap_Exact replays the expression written by frameGridSnap (pinned by
// TestFrameGridSnap) with the double precision operations of ffmpeg's evaluator, in the order
// it parses them (* and / from left to right), in the Matroska time base. Every timestamp up
// to 1.5 ms away from the exact time of its frame (two roundings move it by 1 ms at most) must
// land on that time rounded once, halves away from zero: at the start of a file, negative
// decode timestamps included, and after 1, 10, 24 and 100 hours. The frames are counted from
// the first video timestamp, wherever it is: 0, as in the segments of sptenc, 21 and 22 ms
// (half a frame at 23.976 fps, where a grid counted from 0 breaks), and further.
func TestFrameGridSnap_Exact(t *testing.T) {
	const tb = 1.0 / 1000 // TB, av_q2d(1/1000)
	for _, rate := range []struct{ num, den int64 }{
		{24000, 1001}, {30000, 1001}, {60000, 1001}, {120000, 1001},
		{19001, 317}, // 59.94 fps as read from Matroska
		{24, 1}, {25, 1}, {30, 1}, {50, 1}, {60, 1}, {120, 1},
	} {
		num, den := float64(rate.num), float64(rate.den)
		for _, start := range []int64{0, 21, 22, 125, 500, 3600000} { // STARTPTS
			for _, hours := range []int64{0, 1, 10, 24, 100} {
				first := hours*3600*rate.num/rate.den - 3
				for frame := first; frame < first+3000; frame++ {
					// the exact time of the frame, from the first one, is exact/rate.num ms
					exact := frame * rate.den * 1000
					expected := start + divRoundHalfAway(exact, rate.num)
					for timestamp := expected - 2; timestamp <= expected+2; timestamp++ {
						if 2*abs64((timestamp-start)*rate.num-exact) > 3*rate.num {
							continue // more than 1.5 ms away
						}
						index := math.Round((float64(timestamp) - float64(start)) * tb * num / den)
						if got := int64(float64(start) + math.Round(index*den/tb/num)); got != expected {
							t.Fatalf("%d/%d fps from %d ms, frame %d: %d ms snapped to %d ms, expected %d ms",
								rate.num, rate.den, start, frame, timestamp, got, expected)
						}
					}
				}
			}
		}
	}
}

// divRoundHalfAway returns a/b rounded to the nearest integer, halves away from zero (b > 0).
func divRoundHalfAway(a, b int64) int64 {
	if a < 0 {
		return -((-2*a + b) / (2 * b))
	}
	return (2*a + b) / (2 * b)
}

func abs64(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
}

// TestConcatSnap merges for real segments whose video does not land at 0 in the output, with the
// snap, as the encode pipeline and the concat command do. Three segments of 26, 51 and 30 frames
// at 23.976 fps, with B-frames. The output must hold every frame once, on one grid counted from
// its first frame (frames of 41 to 42 ms, no timestamp shared, decode timestamps increasing),
// and its video where the segments have it against their audio. Counted from 0, the grid gave
// timestamps shared by two frames with AAC audio in Matroska, and moved the video against its
// audio by 20 ms in the other two cases (see frameGridSnap). The video only cases are the merges
// of the encode pipeline: its encodes, which start with their video wherever it starts, and the
// reference of the final VMAF of a pre-split directory, the video of its segments alone.
func TestConcatSnap(t *testing.T) {
	for _, bin := range []string{FFMPEGBinary, FFProbeBinary} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not found: %s", bin, err)
		}
	}
	ctx := context.Background()
	run := func(args ...string) {
		t.Helper()
		if output, err := exec.CommandContext(ctx, FFMPEGBinary, append([]string{"-loglevel", "error", "-nostdin", "-y"}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("ffmpeg %v: %s\n%s", args, err, output)
		}
	}
	// the pts and dts of the packets of a stream, in file order, in seconds
	packets := func(path, stream string) (pts, dts []float64) {
		t.Helper()
		output, err := exec.CommandContext(ctx, FFProbeBinary, "-v", "error", "-select_streams", stream,
			"-show_entries", "packet=pts_time,dts_time", "-of", "csv=p=0", path).Output()
		if err != nil {
			t.Fatalf("ffprobe %s: %s", path, err)
		}
		for _, line := range strings.Fields(string(output)) {
			fields := strings.Split(line, ",")
			if len(fields) < 2 {
				t.Fatalf("packet %q of %s: no pts and dts", line, path)
			}
			values := make([]float64, 2)
			for i := range values {
				if values[i], err = strconv.ParseFloat(fields[i], 64); err != nil {
					t.Fatalf("packet %q of %s: %s", line, path, err)
				}
			}
			pts, dts = append(pts, values[0]), append(dts, values[1])
		}
		return
	}
	milliseconds := func(seconds float64) int64 { return int64(math.Round(seconds * 1000)) }
	// how long after the first audio packet the first video frame comes, in seconds
	videoAfterAudio := func(path string) float64 {
		t.Helper()
		videoPTS, _ := packets(path, "v:0")
		audioPTS, _ := packets(path, "a:0")
		return slices.Min(videoPTS) - slices.Min(audioPTS)
	}
	segmentsFrames := []int{26, 51, 30}
	segmentsLengths := []string{"1.08", "2.12", "1.25"} // what -t cuts those frames with
	durations := make([]time.Duration, len(segmentsFrames))
	totalFrames := 0
	for i, frames := range segmentsFrames {
		durations[i] = time.Duration(math.Round(float64(frames)*1001/24000*1e6)) * time.Microsecond
		totalFrames += frames
	}
	// a set of segments: video alone (audio ""), or with an audio stream
	segments := func(dir, container, audio string) (paths []string) {
		t.Helper()
		for i, length := range segmentsLengths {
			path := filepath.Join(dir, fmt.Sprintf("seg_%d_%s.%s", i, audio, container))
			args := []string{"-f", "lavfi", "-i", "testsrc2=size=320x180:rate=24000/1001"}
			if audio != "" {
				args = append(args, "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000", "-c:a", audio)
			}
			run(append(args, "-t", length, "-c:v", "mpeg4", "-bf", "2", "-q:v", "5", path)...)
			paths = append(paths, path)
		}
		return
	}
	dir := t.TempDir()
	videoOnly := segments(dir, "mkv", "")
	flac := segments(dir, "mkv", "flac")
	aac := segments(dir, "mkv", "aac")
	aacMP4 := segments(dir, "mp4", "aac")
	// the FLAC segments with their video 22 ms after their audio
	var videoLate []string
	for i, path := range flac {
		late := filepath.Join(dir, fmt.Sprintf("seg_%d_late.mkv", i))
		run("-itsoffset", "0.022", "-i", path, "-i", path, "-map", "0:v", "-map", "1:a", "-c", "copy", late)
		videoLate = append(videoLate, late)
	}
	// a video only file starting 83 ms after 0, as the encode of a segment whose video starts
	// after its audio does: the encoders number the frames from the timestamp of the first one
	videoOnlyLate := filepath.Join(dir, "seg_1_videoonly_late.mkv")
	run("-itsoffset", "0.083", "-i", videoOnly[1], "-c", "copy", videoOnlyLate)
	if video := probeVideo(t, ctx, videoOnlyLate); video.StartTime != "0.083000" {
		t.Fatalf("the video only file was expected to start at 0.083 s, got %s", video.StartTime)
	}
	for _, tc := range []struct {
		name      string
		files     []string
		videoOnly bool  // ConcatConfig.VideoOnly
		firstPTS  int64 // of the video, in ms
		wholeGrid bool  // every frame at the first one plus its time rounded to the millisecond
	}{
		// the first frame at 0: what sptenc's own segments give, and what the grid counted from 0 gave
		{"video only", videoOnly, false, 0, true},
		{"FLAC audio", flac, false, 0, true},
		// the first audio packet 21 ms before 0: ffmpeg brings the whole input 21 ms later
		{"AAC audio in Matroska", aac, false, 21, true},
		// MP4 holds the timestamps exact: the frames are at 21.333 ms plus their time, rounded once
		{"AAC audio in MP4", aacMP4, false, 21, false},
		{"video 22 ms after its audio", videoLate, false, 22, true},
		// the same video gap in every file, 0, whatever their audio starts with
		{"AAC and FLAC audio mixed", []string{aac[0], flac[1], aac[2]}, false, 21, true},
		{"FLAC and AAC audio mixed", []string{flac[0], aac[1], flac[2]}, false, 0, true},
		// a video only file starts with its video, wherever that is: no gap
		{"video only, a file not starting at 0", []string{videoOnly[0], videoOnlyLate, videoOnly[2]}, false, 0, true},
		// the video alone kept: the audio still sets where the output starts
		{"AAC audio, video only output", aac, true, 21, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := t.TempDir()
			list, err := GenerateConcatList(out, tc.files, durations)
			if err != nil {
				t.Fatalf("list: %s", err)
			}
			output := filepath.Join(out, "output.mkv")
			if err := Concat(ctx, ConcatConfig{ConcatListPath: list, ConcatUnsafe: true, OutputPath: output,
				VideoOnly: tc.videoOnly, FrameRate: "24000/1001"}); err != nil {
				t.Fatalf("concat: %s", err)
			}
			stats, err := GetStreamsInfos(ctx, GetStreamsInfosConfig{Path: output})
			if err != nil {
				t.Fatalf("probe: %s", err)
			}
			if tc.videoOnly && (len(stats.Streams) != 1 || stats.VideoTrack() == nil) {
				t.Errorf("%d streams in the output, want its video alone", len(stats.Streams))
			}
			pts, dts := packets(output, "v:0")
			if len(pts) != totalFrames {
				t.Fatalf("%d video packets, want %d", len(pts), totalFrames)
			}
			for i := 1; i < len(dts); i++ {
				if dts[i] <= dts[i-1] {
					t.Errorf("decode timestamp of packet %d (%f s) not after the previous one (%f s)", i, dts[i], dts[i-1])
					break
				}
			}
			slices.Sort(pts)
			if got := milliseconds(pts[0]); got != tc.firstPTS {
				t.Errorf("first frame at %d ms, want %d ms", got, tc.firstPTS)
			}
			for i := 1; i < len(pts); i++ {
				if frame := milliseconds(pts[i]) - milliseconds(pts[i-1]); frame < 41 || frame > 42 {
					t.Errorf("frame %d lasts %d ms (from %d ms), want 41 or 42 ms", i-1, frame, milliseconds(pts[i-1]))
					break
				}
			}
			if tc.wholeGrid {
				for i := range pts {
					if want := milliseconds(pts[0]) + divRoundHalfAway(int64(i)*1001*1000, 24000); milliseconds(pts[i]) != want {
						t.Errorf("frame %d at %d ms, want %d ms", i, milliseconds(pts[i]), want)
						break
					}
				}
			}
			if stats.AudioTrack() == nil {
				return
			}
			// within Matroska's millisecond of the segments' own
			if got, want := videoAfterAudio(output), videoAfterAudio(tc.files[0]); math.Abs(got-want) >= 0.001 {
				t.Errorf("the video comes %.3f s after the audio, %.3f s in the segments", got, want)
			}
		})
	}
}
