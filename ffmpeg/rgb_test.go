package ffmpeg

import (
	"context"
	"errors"
	"math"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
)

func TestIsRGBPixelFormat(t *testing.T) {
	for pixFmt, want := range map[string]bool{
		"gbrp": true, "gbrp10le": true, "gbrp16le": true, "gbrap": true, "gbrpf32le": true,
		"rgb24": true, "bgr24": true, "rgb48le": true, "rgba64le": true, "bgr0": true, "0rgb": true,
		"argb": true, "abgr": true, "x2rgb10le": true, "x2bgr10le": true, "pal8": true, "bayer_rggb8": true,
		"yuv420p": false, "yuv420p10le": false, "yuv444p16le": false, "yuvj420p": false, "nv12": false,
		"p010le": false, "gray": false, "gray10le": false, "ya8": false, "xyz12le": false, "": false,
	} {
		if got := IsRGBPixelFormat(pixFmt); got != want {
			t.Errorf("%q: want %t, got %t", pixFmt, want, got)
		}
	}
}

func TestYUVMatrixFromPrimaries(t *testing.T) {
	for primaries, want := range map[string]YUVMatrix{
		"bt709":  YUVMatrixBT709,
		"bt2020": YUVMatrixBT2020NC,
		// primaries calling for no matrix in particular, or none declared
		"smpte170m": "", "bt470bg": "", "smpte432": "", "unknown": "", "": "",
	} {
		got, known := YUVMatrixFromPrimaries(primaries)
		if got != want || known != (want != "") {
			t.Errorf("%q: want %q, got %q (known: %t)", primaries, want, got, known)
		}
	}
}

func TestRGBToYUVFilter(t *testing.T) {
	for matrix, want := range map[YUVMatrix]string{
		YUVMatrixBT709:    "scale=out_color_matrix=bt709:out_range=tv:out_chroma_loc=left,format=yuv420p10le",
		YUVMatrixBT2020NC: "scale=out_color_matrix=bt2020nc:out_range=tv:out_chroma_loc=left,format=yuv420p10le",
		// the scale filter knows BT.601's coefficients as bt601
		YUVMatrixBT601: "scale=out_color_matrix=bt601:out_range=tv:out_chroma_loc=left,format=yuv420p10le",
	} {
		if !matrix.Valid() {
			t.Errorf("%q should be valid", matrix)
		}
		if got := RGBToYUVFilter(matrix); got != want {
			t.Errorf("%q: want %q, got %q", matrix, want, got)
		}
	}
	for _, matrix := range []YUVMatrix{"", "gbr", "rgb", "bt601", "smpte170m", "bt2020c"} {
		if matrix.Valid() {
			t.Errorf("%q should not be valid", matrix)
		}
	}
}

// rgbSource writes an RGB clip the way upscalers deliver their masters: 16-bit RGB, full range,
// declaring BT.709 primaries and transfer and the identity matrix. Saturated colors (testsrc2's
// bars, then the SMPTE bars from frame 6 on, a scene change), the ones a matrix changes most. In
// 1280x720: at 320x180, VMAF v1 did not see chroma at all (a blur of the chroma planes alone
// scored 100), nor the chroma siting TestVMAFComputeRGBReference is about.
func rgbSource(t *testing.T, ctx context.Context, dir string) string {
	t.Helper()
	for _, bin := range []string{FFMPEGBinary, FFProbeBinary} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not found: %s", bin, err)
		}
	}
	path := filepath.Join(dir, "rgb.mkv")
	if output, err := exec.CommandContext(ctx, FFMPEGBinary, "-loglevel", "error", "-nostdin", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=1280x720:rate=24:duration=0.25",
		"-f", "lavfi", "-i", "smptehdbars=size=1280x720:rate=24:duration=0.25",
		"-filter_complex", "[0:v]format=gbrp16le,setsar=1[a];[1:v]format=gbrp16le,setsar=1[b];[a][b]concat=n=2:v=1:a=0,"+
			"setparams=range=pc:colorspace=gbr:color_primaries=bt709:color_trc=bt709",
		"-c:v", "ffv1", path).CombinedOutput(); err != nil {
		t.Fatalf("failed to create the RGB source: %s\n%s", err, output)
	}
	return path
}

// probeVideo returns the video stream of a file.
func probeVideo(t *testing.T, ctx context.Context, path string) *FFProbeBinaryStream {
	t.Helper()
	stats, err := GetStreamsInfos(ctx, GetStreamsInfosConfig{Path: path})
	if err != nil {
		t.Fatalf("probe %s: %s", path, err)
	}
	video := stats.VideoTrack()
	if video == nil {
		t.Fatalf("no video stream in %s", path)
	}
	return video
}

// TestFFV1VideoMasterRGB checks that the master of an RGB source is converted with the matrix it is
// given, and declares it: limited range, left chroma siting, the primaries and transfer of the
// source. Left to ffmpeg, it declared no matrix and held BT.601 (see YUVMatrix).
func TestFFV1VideoMasterRGB(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	source := rgbSource(t, ctx, dir)
	for _, matrix := range YUVMatrices {
		master := filepath.Join(dir, "master_"+string(matrix)+".mkv")
		if err := FFV1VideoMaster(ctx, FFV1VideoMasterConfig{InputFilePath: source, RGBToYUV: matrix, OutputFilePath: master}); err != nil {
			t.Fatalf("master with %s: %s", matrix, err)
		}
		video := probeVideo(t, ctx, master)
		got := [6]string{video.PixFmt, video.ColorSpace, video.ColorRange, video.ChromaLocation, video.ColorPrimaries, video.ColorTransfer}
		if want := [6]string{"yuv420p10le", string(matrix), "tv", "left", "bt709", "bt709"}; got != want {
			t.Errorf("master with %s: want pixel format, matrix, range, chroma siting, primaries and transfer %v, got %v", matrix, want, got)
		}
	}
	if err := FFV1VideoMaster(ctx, FFV1VideoMasterConfig{InputFilePath: source, RGBToYUV: "gbr",
		OutputFilePath: filepath.Join(dir, "invalid.mkv")}); err == nil {
		t.Error("a matrix an RGB source can not be converted with should be refused")
	}
}

// TestVMAFComputeRGBReference checks the final VMAF of an RGB source: the source must be scored as
// its master, the very pictures every encode is made of. Its master scores 100 against it on every
// frame (the RGB master against its own BT.709 conversion), and a distorted copy of the master
// scores frame by frame what it scores against the master. Left to ffmpeg, the RGB reference was
// converted with the matrix the distorted video declares but with the chroma sited in the center:
// the scores of the distorted copy differed from those against the master by up to 1.04 (a real
// libx265 encode of a 1280x720 RGB clip: from -1.08 to +0.71 per frame). Now that both inputs
// declare the same matrix (see vmafSameMatrix), ffmpeg would convert it with BT.601's.
func TestVMAFComputeRGBReference(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	source := rgbSource(t, ctx, dir)
	master := filepath.Join(dir, "master.mkv")
	if err := FFV1VideoMaster(ctx, FFV1VideoMasterConfig{InputFilePath: source, RGBToYUV: YUVMatrixBT709, OutputFilePath: master}); err != nil {
		t.Fatalf("master: %s", err)
	}
	// An encode of the master, without an encoder: the master blurred, without loss from there
	blurred := filepath.Join(dir, "blurred.mkv")
	if output, err := exec.CommandContext(ctx, FFMPEGBinary, "-loglevel", "error", "-nostdin", "-y", "-i", master,
		"-vf", "gblur=sigma=0.8", "-c:v", "ffv1", blurred).CombinedOutput(); err != nil {
		t.Fatalf("failed to blur the master: %s\n%s", err, output)
	}
	compute := func(reference string, matrix YUVMatrix, distorted string) []float64 {
		report, err := VMAFCompute(ctx, VMAFComputeConfig{
			ReferencePath:     reference,
			ReferenceRGBToYUV: matrix,
			DistortedPath:     distorted,
			InputFrameRate:    "24",
			ReportPath:        filepath.Join(dir, "report.json"),
			Model:             VMAFModelFHD,
			ModelCAMBI:        true,
			Measures:          VMAFMeasures{Fidelity: true},
		})
		if errors.Is(err, ErrVMAFModelUnavailable) {
			t.Skipf("the libvmaf of %s does not know %s: %s", FFMPEGBinary, VMAFModelFHD, err)
		}
		if err != nil {
			t.Fatalf("VMAF of %s against %s: %s", distorted, reference, err)
		}
		if len(report.Frames) != 12 {
			t.Fatalf("expected 12 frames, got %d", len(report.Frames))
		}
		scores := make([]float64, len(report.Frames))
		for i, frame := range report.Frames {
			scores[i] = frame.Fidelity
		}
		return scores
	}
	if scores := compute(source, YUVMatrixBT709, master); slices.Min(scores) != 100 {
		t.Errorf("the master scored %v against its RGB source, want 100 on every frame", scores)
	}
	againstMaster := compute(master, "", blurred)
	if slices.Min(againstMaster) == 100 {
		t.Fatalf("the blurred master is not distorted: %v", againstMaster)
	}
	if againstSource := compute(source, YUVMatrixBT709, blurred); !slices.Equal(againstSource, againstMaster) {
		t.Errorf("the blurred master scores against the RGB source:\n%v\nand against the master:\n%v", againstSource, againstMaster)
	}
	leftToFFmpeg := compute(source, "", blurred)
	var largest float64
	for i := range leftToFFmpeg {
		largest = max(largest, math.Abs(leftToFFmpeg[i]-againstMaster[i]))
	}
	t.Logf("left to ffmpeg, the RGB reference makes the scores of the blurred master differ by up to %.4f", largest)
}

// TestScenesDetectionRGB checks that the scene changes of an RGB source score what they score on
// its master: the luma scdet reads is the master's.
func TestScenesDetectionRGB(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	source := rgbSource(t, ctx, dir)
	master := filepath.Join(dir, "master.mkv")
	if err := FFV1VideoMaster(ctx, FFV1VideoMasterConfig{InputFilePath: source, RGBToYUV: YUVMatrixBT709, OutputFilePath: master}); err != nil {
		t.Fatalf("master: %s", err)
	}
	detect := func(path string, matrix YUVMatrix) (scenes []Scene) {
		scenes, err := ScenesDetection(ctx, ScenesDetectionConfig{Path: path, Threshold: 1, RGBToYUV: matrix})
		if err != nil {
			t.Fatalf("scenes of %s: %s", path, err)
		}
		for i := range scenes {
			scenes[i].Start = 0 // only the frames and the scores are compared
		}
		return
	}
	onMaster := detect(master, "")
	if !slices.ContainsFunc(onMaster, func(scene Scene) bool { return scene.Frame == 6 }) {
		t.Fatalf("the scene change at frame 6 was not detected on the master: %+v", onMaster)
	}
	if onSource := detect(source, YUVMatrixBT709); !slices.Equal(onSource, onMaster) {
		t.Errorf("the source scores %+v, its master %+v", onSource, onMaster)
	}
}

// TestVMAFComputeSameMatrix checks that VMAF compares pictures as they are, whatever matrix each
// input declares: the master of an RGB source against a copy declaring no matrix scores 100 on
// every frame (77.3 harmonic mean when ffmpeg converted one into the matrix of the other), and so
// do the RGB source, as its master's conversion makes it, against that copy, and the master against
// the RGB source given as the distorted video.
func TestVMAFComputeSameMatrix(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	source := rgbSource(t, ctx, dir)
	master := filepath.Join(dir, "master.mkv")
	if err := FFV1VideoMaster(ctx, FFV1VideoMasterConfig{InputFilePath: source, RGBToYUV: YUVMatrixBT709, OutputFilePath: master}); err != nil {
		t.Fatalf("master: %s", err)
	}
	undeclared := filepath.Join(dir, "undeclared.mkv")
	if output, err := exec.CommandContext(ctx, FFMPEGBinary, "-loglevel", "error", "-nostdin", "-y", "-i", master,
		"-c", "copy", "-colorspace", "unknown", undeclared).CombinedOutput(); err != nil {
		t.Fatalf("failed to remove the matrix of the master: %s\n%s", err, output)
	}
	if video := probeVideo(t, ctx, undeclared); video.ColorSpace != "" { // ffprobe leaves an unspecified one out
		t.Fatalf("the copy of the master still declares the %s matrix", video.ColorSpace)
	}
	for _, tc := range []struct {
		name                                 string
		reference, distorted                 string
		referenceRGBToYUV, distortedRGBToYUV YUVMatrix
	}{
		{"master against a copy declaring no matrix", master, undeclared, "", ""},
		{"RGB source against a copy of its master declaring no matrix", source, undeclared, YUVMatrixBT709, ""},
		{"master against its RGB source", master, source, "", YUVMatrixBT709},
	} {
		report, err := VMAFCompute(ctx, VMAFComputeConfig{
			ReferencePath:     tc.reference,
			ReferenceRGBToYUV: tc.referenceRGBToYUV,
			DistortedPath:     tc.distorted,
			DistortedRGBToYUV: tc.distortedRGBToYUV,
			InputFrameRate:    "24",
			ReportPath:        filepath.Join(dir, "report.json"),
			Model:             VMAFModelFHD,
			ModelCAMBI:        true,
			Measures:          VMAFMeasures{Fidelity: true},
		})
		if errors.Is(err, ErrVMAFModelUnavailable) {
			t.Skipf("the libvmaf of %s does not know %s: %s", FFMPEGBinary, VMAFModelFHD, err)
		}
		if err != nil {
			t.Fatalf("%s: %s", tc.name, err)
		}
		if len(report.Frames) != 12 || report.Pooled.Fidelity.Min != 100 {
			t.Errorf("%s: want 100 on 12 frames, got %+v on %d frames", tc.name, report.Pooled.Fidelity, len(report.Frames))
		}
	}
}
