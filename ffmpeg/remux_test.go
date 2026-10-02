package ffmpeg

import (
	"context"
	"path/filepath"
	"testing"
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
