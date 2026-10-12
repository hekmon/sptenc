package ffmpeg

import (
	"context"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The range declared for the output must be the new video's whenever it declares one, the
// original's being only a fallback (see RemuxColorRange).
func TestRemuxColorRange(t *testing.T) {
	stream := func(colorRange string) *FFProbeBinaryStream {
		return &FFProbeBinaryStream{CodecType: "video", ColorRange: colorRange}
	}
	tests := []struct {
		name          string
		originalVideo *FFProbeBinaryStream
		newVideo      *FFProbeBinaryStream
		want          string
	}{
		// the master converts full range sources to limited range
		{"full range original, limited range new video", stream("pc"), stream("tv"), "tv"},
		{"limited range original and new video", stream("tv"), stream("tv"), "tv"},
		// the remux command is given any new video
		{"limited range original, full range new video", stream("tv"), stream("pc"), "pc"},
		{"original declaring no range", stream(""), stream("tv"), "tv"},
		{"original not probed", nil, stream("tv"), "tv"},
		// fallbacks on the original's range
		{"new video declaring no range", stream("pc"), stream(""), "pc"},
		{"new video declaring an unknown range", stream("pc"), stream("unknown"), "pc"},
		{"new video not probed", stream("pc"), nil, "pc"},
		{"no range anywhere", stream(""), stream(""), ""},
		{"nothing probed", nil, nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := RemuxColorRange(tt.originalVideo, tt.newVideo); got != tt.want {
				t.Errorf("RemuxColorRange() = %q, want %q", got, tt.want)
			}
		})
	}
}

// The matrix declared for the output must be the new video's whenever it declares one, the
// original's being only a fallback for a new video holding the same kind of pictures (see
// RemuxColorSpace).
func TestRemuxColorSpace(t *testing.T) {
	stream := func(pixFmt, matrix string) *FFProbeBinaryStream {
		return &FFProbeBinaryStream{CodecType: "video", PixFmt: pixFmt, ColorSpace: matrix}
	}
	rgb, yuv := "gbrp16le", "yuv420p10le"
	tests := []struct {
		name          string
		originalVideo *FFProbeBinaryStream
		newVideo      *FFProbeBinaryStream
		want          string
	}{
		// the master of an RGB source is converted with a matrix it declares
		{"RGB original, YUV new video", stream(rgb, "gbr"), stream(yuv, "bt709"), "bt709"},
		{"RGB original, matrix forced", stream(rgb, "gbr"), stream(yuv, "bt2020nc"), "bt2020nc"},
		// a YUV source keeps its matrix down to the new video
		{"YUV original and new video", stream(yuv, "bt709"), stream(yuv, "bt709"), "bt709"},
		{"original declaring no matrix", stream(yuv, ""), stream(yuv, "bt709"), "bt709"},
		{"original not probed", nil, stream(yuv, "bt709"), "bt709"},
		// the remux command is given any new video
		{"RGB original and new video", stream(rgb, "gbr"), stream(rgb, "gbr"), "gbr"},
		{"YUV original, RGB new video", stream(yuv, "bt709"), stream(rgb, "gbr"), "gbr"},
		// fallbacks on the original's matrix, for the same kind of pictures only
		{"new video declaring no matrix", stream(yuv, "bt709"), stream(yuv, ""), "bt709"},
		{"new video declaring an unknown matrix", stream(yuv, "smpte170m"), stream(yuv, "unknown"), "smpte170m"},
		{"new video declaring a reserved matrix", stream(yuv, "bt709"), stream(yuv, "reserved"), "bt709"},
		{"new video not probed", stream(yuv, "bt709"), nil, "bt709"},
		{"RGB original, RGB new video declaring no matrix", stream(rgb, "gbr"), stream(rgb, ""), "gbr"},
		{"RGB original, YUV new video declaring no matrix", stream(rgb, "gbr"), stream(yuv, ""), ""},
		{"RGB original declaring a YUV matrix", stream(rgb, "bt709"), stream(yuv, ""), ""},
		{"RGB original, new video not probed", stream(rgb, "gbr"), nil, ""},
		{"YUV original, RGB new video declaring no matrix", stream(yuv, "bt709"), stream(rgb, ""), ""},
		{"original declaring a reserved matrix", stream(yuv, "reserved"), stream(yuv, ""), ""},
		{"no matrix anywhere", stream(yuv, ""), stream(yuv, ""), ""},
		{"nothing probed", nil, nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := RemuxColorSpace(tt.originalVideo, tt.newVideo); got != tt.want {
				t.Errorf("RemuxColorSpace() = %q, want %q", got, tt.want)
			}
		})
	}
	if got := colorSpaceOptionValue("gbr"); got != "rgb" {
		t.Errorf("the identity must be handed to ffmpeg as rgb, got %q", got)
	}
}

// TestRemuxSwapVideoRGBOriginal remuxes for real onto an RGB original, which declares the identity
// matrix: its master converted to YUV comes out with the matrix it declares, an RGB video with the
// identity. Both failed before, ffmpeg refusing the identity under the name ffprobe gives it.
func TestRemuxSwapVideoRGBOriginal(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	original := rgbSource(t, ctx, dir)
	master := filepath.Join(dir, "master.mkv")
	if err := FFV1VideoMaster(ctx, FFV1VideoMasterConfig{InputFilePath: original, RGBToYUV: YUVMatrixBT709, OutputFilePath: master}); err != nil {
		t.Fatalf("master: %s", err)
	}
	for _, tc := range []struct {
		name, newVideo string
		want           [4]string // matrix, range, primaries, transfer
	}{
		{"its master", master, [4]string{"bt709", "tv", "bt709", "bt709"}},
		{"an RGB video", original, [4]string{"gbr", "pc", "bt709", "bt709"}},
	} {
		output := filepath.Join(dir, "output.mkv")
		if err := RemuxSwapVideo(ctx, RemuxSwapVideoConfig{OriginalFile: original, NewVideoFile: tc.newVideo, OutputFilePath: output}); err != nil {
			t.Fatalf("%s: %s", tc.name, err)
		}
		video := probeVideo(t, ctx, output)
		if got := [4]string{video.ColorSpace, video.ColorRange, video.ColorPrimaries, video.ColorTransfer}; got != tc.want {
			t.Errorf("%s: want matrix, range, primaries and transfer %v, got %v", tc.name, tc.want, got)
		}
	}
}

// The delay given to the new video file must be the gap between the start of the original's video
// and the start of its file, less the same gap in the new video file (see RemuxVideoOffset).
func TestRemuxVideoOffset(t *testing.T) {
	// a probed file: where it starts, where its video does, then its other streams
	file := func(fileStart, videoStart string, others ...string) FFProbeStats {
		stats := FFProbeStats{
			Format:  &FFProbeFormat{StartTime: fileStart},
			Streams: []*FFProbeBinaryStream{{CodecType: "video", StartTime: videoStart}},
		}
		for _, start := range others {
			stats.Streams = append(stats.Streams, &FFProbeBinaryStream{CodecType: "audio", StartTime: start})
		}
		return stats
	}
	merged := file("0.000000", "0.000000") // the merged encodes: video only, from 0
	tests := []struct {
		name     string
		original FFProbeStats
		newVideo FFProbeStats
		want     time.Duration
	}{
		{"video starting with its file", file("0.000000", "0.000000", "0.000000"), merged, 0},
		{"video starting after its audio", file("0.000000", "0.123000", "0.000000"), merged, 123 * time.Millisecond},
		{"the same file moved whole", file("0.500000", "0.623000", "0.500000"), merged, 123 * time.Millisecond},
		{"audio starting after its video", file("0.000000", "0.000000", "0.123000"), merged, 0},
		{"file starting before 0", file("-0.021000", "0.000000", "-0.021000"), merged, 21 * time.Millisecond},
		{"start on a fraction of a millisecond", file("0.000000", "0.125125", "0.000000"), merged, 125125 * time.Microsecond},
		{"video only original", file("0.123000", "0.123000"), merged, 0},
		// the remux command is given any new video
		{"new video starting with its file, not at 0", file("0.000000", "0.123000", "0.000000"), file("0.125000", "0.125000"), 123 * time.Millisecond},
		{"new video holding the gap already", file("0.000000", "0.123000", "0.000000"), file("0.000000", "0.123000", "0.000000"), 0},
		{"new video holding a larger gap", file("0.000000", "0.000000", "0.000000"), file("0.000000", "0.500000", "0.000000"), -500 * time.Millisecond},
		// nothing to compare: no delay
		{"original video without a start", file("0.000000", "N/A", "0.000000"), merged, 0},
		{"original file without a start", file("N/A", "0.123000"), merged, 0},
		{"original without a video stream", FFProbeStats{Format: &FFProbeFormat{StartTime: "0.000000"}}, merged, 0},
		{"original without a format", FFProbeStats{Streams: file("", "0.123000").Streams}, merged, 0},
		{"new video without a start", file("0.000000", "0.123000", "0.000000"), file("N/A", "N/A"), 0},
		{"nothing probed", FFProbeStats{}, FFProbeStats{}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := RemuxVideoOffset(tt.original, tt.newVideo); got != tt.want {
				t.Errorf("RemuxVideoOffset() = %s, want %s", got, tt.want)
			}
		})
	}
}

// TestRemuxSwapVideoOffset remuxes for real onto originals whose video and audio do not start
// together: every stream of the output must have the timestamps it has in the original, once the
// original is brought to 0 as ffmpeg brings any input file, the video included. It came out at 0
// whatever the original before, 123 ms early against the audio in the first two cases.
func TestRemuxSwapVideoOffset(t *testing.T) {
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
	// the timestamps of the packets of a stream, in Matroska's milliseconds, sorted
	timestamps := func(path, stream string) (pts []int64) {
		t.Helper()
		output, err := exec.CommandContext(ctx, FFProbeBinary, "-v", "error", "-select_streams", stream,
			"-show_entries", "packet=pts", "-of", "csv=p=0", path).Output()
		if err != nil {
			t.Fatalf("ffprobe %s: %s", path, err)
		}
		for _, line := range strings.Fields(string(output)) {
			value, err := strconv.ParseInt(strings.TrimRight(line, ","), 10, 64)
			if err != nil {
				t.Fatalf("timestamp %q of %s: %s", line, path, err)
			}
			pts = append(pts, value)
		}
		slices.Sort(pts)
		return
	}
	moved := func(pts []int64, by int64) []int64 {
		out := make([]int64, len(pts))
		for i := range pts {
			out[i] = pts[i] + by
		}
		return out
	}
	// 60 frames at 23.976 fps with B-frames, and AC-3 audio
	base := filepath.Join(dir, "base.mkv")
	run("-f", "lavfi", "-i", "testsrc2=size=320x180:rate=24000/1001", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000",
		"-t", "2.5", "-c:v", "mpeg4", "-bf", "2", "-q:v", "5", "-c:a", "ac3", base)
	// the new video: the same frames, video only, from 0, as the merged encodes are
	newVideo := filepath.Join(dir, "newvideo.mkv")
	run("-i", base, "-map", "0:v:0", "-c", "copy", newVideo)
	videoLate := filepath.Join(dir, "videolate.mkv")
	run("-itsoffset", "0.123", "-i", base, "-i", base, "-map", "0:v:0", "-map", "1:a:0", "-c", "copy", videoLate)
	movedWhole := filepath.Join(dir, "movedwhole.mkv")
	run("-itsoffset", "0.5", "-i", videoLate, "-map", "0", "-c", "copy", movedWhole)
	audioLate := filepath.Join(dir, "audiolate.mkv")
	run("-i", base, "-itsoffset", "0.123", "-i", base, "-map", "0:v:0", "-map", "1:a:0", "-c", "copy", audioLate)
	// a master starts with its file, not at 0: it holds the gap rounded to a frame
	master := filepath.Join(dir, "master.mkv")
	if err := FFV1VideoMaster(ctx, FFV1VideoMasterConfig{InputFilePath: videoLate, OutputFilePath: master}); err != nil {
		t.Fatalf("master: %s", err)
	}
	if video := probeVideo(t, ctx, master); video.StartTime != "0.125000" {
		t.Fatalf("the master of a video starting at 0.123 s is expected to start at 0.125 s, got %s", video.StartTime)
	}
	// an output of the remux, given back as the new video: it holds the gap already
	previous := filepath.Join(dir, "previous.mkv")
	if err := RemuxSwapVideo(ctx, RemuxSwapVideoConfig{OriginalFile: videoLate, NewVideoFile: newVideo, OutputFilePath: previous}); err != nil {
		t.Fatalf("previous output: %s", err)
	}
	for _, tc := range []struct {
		name, original, newVideo string
		videoStart, fileStart    string // of the original
		fileStartMS              int64
	}{
		{"video with its audio", base, newVideo, "0.000000", "0.000000", 0},
		{"video after its audio", videoLate, newVideo, "0.123000", "0.000000", 0},
		{"video after its audio, file moved whole", movedWhole, newVideo, "0.623000", "0.500000", 500},
		{"audio after its video", audioLate, newVideo, "0.000000", "0.000000", 0},
		{"video after its audio, the master as new video", videoLate, master, "0.123000", "0.000000", 0},
		{"video after its audio, a previous output as new video", videoLate, previous, "0.123000", "0.000000", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stats, err := GetStreamsInfos(ctx, GetStreamsInfosConfig{Path: tc.original})
			if err != nil {
				t.Fatalf("probe: %s", err)
			}
			if got := [2]string{stats.VideoTrack().StartTime, stats.Format.StartTime}; got != [2]string{tc.videoStart, tc.fileStart} {
				t.Fatalf("the original was expected with its video at %s s and its file at %s s, got %v", tc.videoStart, tc.fileStart, got)
			}
			output := filepath.Join(t.TempDir(), "output.mkv")
			if err := RemuxSwapVideo(ctx, RemuxSwapVideoConfig{OriginalFile: tc.original, NewVideoFile: tc.newVideo, OutputFilePath: output}); err != nil {
				t.Fatalf("remux: %s", err)
			}
			wantAudio := moved(timestamps(tc.original, "a:0"), -tc.fileStartMS)
			if got := timestamps(output, "a:0"); !slices.Equal(got, wantAudio) {
				t.Errorf("audio: %d packets from %d ms, want %d from %d ms", len(got), got[0], len(wantAudio), wantAudio[0])
			}
			wantVideo := moved(timestamps(tc.original, "v:0"), -tc.fileStartMS)
			got := timestamps(output, "v:0")
			if tc.newVideo == master {
				// the frames of the master are numbered from its own start: 1 ms after the original's
				// on some of them (see RemuxVideoOffset), each lasting what it does in the master
				masterPTS := timestamps(master, "v:0")
				wantVideo = moved(masterPTS, wantVideo[0]-masterPTS[0])
			}
			if !slices.Equal(got, wantVideo) {
				differing := 0
				for i := range min(len(got), len(wantVideo)) {
					if got[i] != wantVideo[i] {
						differing++
					}
				}
				t.Errorf("video: %d packets from %d ms, want %d from %d ms, %d timestamps differ",
					len(got), got[0], len(wantVideo), wantVideo[0], differing)
			}
		})
	}
}
