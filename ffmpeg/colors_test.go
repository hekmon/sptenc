package ffmpeg

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestColorConversionFilter(t *testing.T) {
	from := Colors{Matrix: "bt709", Range: "tv", Primaries: "bt709", Transfer: "bt709"}
	to := Colors{Matrix: "smpte170m", Range: "pc", Primaries: "bt470bg", Transfer: "bt470bg"}
	filter, err := ColorConversionFilter(from, to)
	if err != nil {
		t.Fatal(err)
	}
	if want := "colorspace=ispace=bt709:irange=tv:iprimaries=bt709:itrc=bt709:space=smpte170m:range=pc:primaries=bt470bg:trc=bt470bg"; filter != want {
		t.Errorf("want %q, got %q", want, filter)
	}
	for _, tc := range []struct {
		name    string
		to      Colors
		refusal string
	}{
		{"an HDR transfer", Colors{Matrix: "bt2020nc", Range: "tv", Primaries: "bt2020", Transfer: "smpte2084"}, `transfer "smpte2084"`},
		{"a matrix the filter does not know", Colors{Matrix: "ictcp", Range: "tv", Primaries: "bt2020", Transfer: "bt709"}, `matrix "ictcp"`},
		{"no range", Colors{Matrix: "bt709", Primaries: "bt709", Transfer: "bt709"}, "both ranges"},
		{"no primaries", Colors{Matrix: "bt709", Range: "tv", Transfer: "bt709"}, `primaries ""`},
	} {
		if _, err := ColorConversionFilter(from, tc.to); err == nil || !strings.Contains(err.Error(), tc.refusal) {
			t.Errorf("%s: want a refusal about %s, got %v", tc.name, tc.refusal, err)
		}
	}
}

// TestVMAFComputeReferenceColorFilter scores two conversions of the same RGB pictures, one into
// BT.709's matrix and one into BT.601's, both declared: compared as they are, the pictures differ;
// the BT.709 one converted into BT.601's matrix first, they are nearly the same pictures again.
func TestVMAFComputeReferenceColorFilter(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	source := rgbSource(t, ctx, dir)
	masters := map[YUVMatrix]string{}
	for _, matrix := range []YUVMatrix{YUVMatrixBT709, YUVMatrixBT601} {
		masters[matrix] = filepath.Join(dir, "master_"+string(matrix)+".mkv")
		if err := FFV1VideoMaster(ctx, FFV1VideoMasterConfig{InputFilePath: source, RGBToYUV: matrix, OutputFilePath: masters[matrix]}); err != nil {
			t.Fatalf("master with %s: %s", matrix, err)
		}
	}
	filter, err := ColorConversionFilter(Colors{Matrix: "bt709", Range: "tv", Primaries: "bt709", Transfer: "bt709"},
		Colors{Matrix: "bt470bg", Range: "tv", Primaries: "bt709", Transfer: "bt709"})
	if err != nil {
		t.Fatal(err)
	}
	compute := func(colorFilter string) VMAFPooledMetric {
		report, err := VMAFCompute(ctx, VMAFComputeConfig{
			ReferencePath:        masters[YUVMatrixBT709],
			ReferenceColorFilter: colorFilter,
			DistortedPath:        masters[YUVMatrixBT601],
			InputFrameRate:       "24",
			ReportPath:           filepath.Join(dir, "report.json"),
			Model:                VMAFModelFHD,
			ModelCAMBI:           true,
			Measures:             VMAFMeasures{Fidelity: true},
		})
		if errors.Is(err, ErrVMAFModelUnavailable) {
			t.Skipf("the libvmaf of %s does not know %s: %s", FFMPEGBinary, VMAFModelFHD, err)
		}
		if err != nil {
			t.Fatal(err)
		}
		return report.Pooled.Fidelity
	}
	asIs, converted := compute(""), compute(filter)
	// 98.8 and 69.0 measured: the BT.709 pictures go through a second conversion, which is not
	// exact on the sharp color edges of the bars
	if converted.HarmonicMean < 98 || asIs.HarmonicMean > 80 {
		t.Errorf("want a harmonic mean of 98 at least once converted, 80 at most as they are: got %+v converted, %+v as they are", converted, asIs)
	}
	t.Logf("as they are: %+v, converted: %+v", asIs, converted)
}
