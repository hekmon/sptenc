package ffmpeg

import (
	"context"
	"errors"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestAdaptVMAFPath(t *testing.T) {
	for _, tc := range []struct {
		path, expected string
	}{
		{`/tmp/sptenc-123/seg_000000_qp026.mkv_vmaf.json`, `/tmp/sptenc-123/seg_000000_qp026.mkv_vmaf.json`},
		{`/tmp/with space/a=b/r.json`, `/tmp/with space/a=b/r.json`},
		{`/tmp/it's/r.json`, `/tmp/it\\\'s/r.json`},
		{`/tmp/a,b;c[d]/r.json`, `/tmp/a\,b\;c\[d\]/r.json`},
		{`/tmp/a:b/r.json`, `/tmp/a\\:b/r.json`},
		{`C:\Users\O'Brien\AppData\Local\Temp\r.json`, `C\\:\\\\Users\\\\O\\\'Brien\\\\AppData\\\\Local\\\\Temp\\\\r.json`},
	} {
		if got := adaptVMAFPath(tc.path); got != tc.expected {
			t.Errorf("path %q: expected %q, got %q", tc.path, tc.expected, got)
		}
	}
}

func TestVMAFModel(t *testing.T) {
	// the 8 v1 models libvmaf 3.2.0 builds in: 2 selected, 6 described for a run forcing one
	if n := len(VMAFModels) + len(VMAFForcedModels); n != 8 {
		t.Errorf("expected the 8 v1 models of libvmaf, got %d", n)
	}
	for _, model := range append(append([]VMAFModel{}, VMAFModels...), VMAFForcedModels...) {
		if !model.Valid() {
			t.Errorf("%s should be valid", model)
		}
		if model.Description() == "" {
			t.Errorf("%s has no description", model)
		}
	}
	// the v0 models are described, and told apart
	for _, model := range VMAFV0Models {
		if !model.Valid() || model.Description() == "" || !model.IsV0() {
			t.Errorf("%s should be a valid, described v0 model", model)
		}
	}
	if VMAFModelFHD.IsV0() {
		t.Errorf("%s is not a v0 model", VMAFModelFHD)
	}
	// any name libvmaf may know is valid, a future model included: whether libvmaf knows it
	// is for the probe to tell
	future := VMAFModel("vmaf_v1.0.17_3d0h")
	if !future.Valid() {
		t.Errorf("%q should be valid", future)
	}
	if description := future.Description(); description != "" {
		t.Errorf("%q should not be described, got %q", future, description)
	}
	// what the filter graph would read as options or filters is not a name
	for _, name := range []string{"", "vmaf_v1.0.16_3d0h:log_path=/tmp/x", "a=b", "a,b", "a;b", "a b", "a'b", "a[b]", "../a"} {
		if VMAFModel(name).Valid() {
			t.Errorf("%q should not be a valid model name", name)
		}
	}
}

func TestSelectVMAFModel(t *testing.T) {
	tests := []struct {
		height int
		want   VMAFModel
	}{
		{480, VMAFModelFHD},
		{720, VMAFModelFHD},
		{1080, VMAFModelFHD},
		{1440, VMAFModelFHD},
		{2159, VMAFModelFHD},
		{2160, VMAFModelUHD},
		{4320, VMAFModelUHD},
	}
	for _, tt := range tests {
		if got := SelectVMAFModel(tt.height); got != tt.want {
			t.Errorf("SelectVMAFModel(%d): want %s, got %s", tt.height, tt.want, got)
		}
		if mismatch := tt.want.ResolutionMismatch(tt.height); mismatch != "" {
			t.Errorf("%s on %dp should not mismatch: %s", tt.want, tt.height, mismatch)
		}
	}
	if mismatch := VMAFModelUHD.ResolutionMismatch(1080); mismatch == "" {
		t.Error("the 4K model on a 1080p source should be a mismatch")
	}
	if mismatch := VMAFModelFHD.ResolutionMismatch(2160); mismatch == "" {
		t.Error("the 1080p model on a 2160p source should be a mismatch")
	}
	// A forced model is compared by the display it predicts only: its viewing distance and
	// frame rate are the user's choice
	for _, tt := range []struct {
		model    VMAFModel
		height   int
		mismatch bool
	}{
		{VMAFModelPhone, 1080, false},
		{VMAFModelFHDHFR, 1080, false},
		{VMAFModelPhoneHFR, 720, false},
		{VMAFModelUHDFar, 2160, false},
		{VMAFModelUHDHFR, 2160, false},
		{VMAFModelUHDFarHFR, 2160, false},
		{VMAFModelPhone, 2160, true},
		{VMAFModelFHDHFR, 2160, true},
		{VMAFModelUHDFar, 1080, true},
		{VMAFModelUHDHFR, 1440, true},
		{VMAFModelV0FHD, 1080, false},
		{VMAFModelV0UHDNEG, 2160, false},
		{VMAFModelV0UHD, 1080, true},
		{VMAFModel("vmaf_v1.0.17_3d0h"), 2160, false}, // unknown to sptenc: nothing to compare
	} {
		if mismatch := tt.model.ResolutionMismatch(tt.height); (mismatch != "") != tt.mismatch {
			t.Errorf("%s on %dp: expected a mismatch %t, got %q", tt.model, tt.height, tt.mismatch, mismatch)
		}
	}
}

// TestVMAFPassFilter checks the filter and the report keys of every kind of pass.
func TestVMAFPassFilter(t *testing.T) {
	const (
		report  = `C:\tmp\seg_000001.mkv_vmaf.json`
		tail    = `:log_fmt=json:log_path=C\\:\\\\tmp\\\\seg_000001.mkv_vmaf.json:n_threads=8`
		banding = `:feature=name=cambi\\:full_ref=true\\:cambi_high_res_speedup=1080\\:cambi_vis_lum_threshold=0.06`
	)
	for _, tc := range []struct {
		name               string
		pass               vmafPass
		filter             string
		fidelity, original string
	}{
		{
			// the filter of sptenc v0.1.0, whether the model feeds on CAMBI or not
			name:     "original",
			pass:     vmafPass{model: VMAFModelFHD, modelCAMBI: true, measures: VMAFMeasures{Original: true}},
			filter:   `libvmaf=model=version=vmaf_v1.0.16_3d0h` + tail,
			original: "vmaf",
		},
		{
			name:     "original, model without CAMBI",
			pass:     vmafPass{model: VMAFModelV0FHD, measures: VMAFMeasures{Original: true}},
			filter:   `libvmaf=model=version=vmaf_v0.6.1` + tail,
			fidelity: "vmaf", original: "vmaf",
		},
		{
			name:     "fidelity",
			pass:     vmafPass{model: VMAFModelFHD, modelCAMBI: true, measures: VMAFMeasures{Fidelity: true}},
			filter:   `libvmaf=model=version=vmaf_v1.0.16_3d0h\\:cambi.cambi_max_val=0` + tail,
			fidelity: "vmaf",
		},
		{
			// no clip handed to a model without CAMBI: its score is both
			name:     "fidelity, model without CAMBI",
			pass:     vmafPass{model: VMAFModelV0FHD, measures: VMAFMeasures{Fidelity: true}},
			filter:   `libvmaf=model=version=vmaf_v0.6.1` + tail,
			fidelity: "vmaf", original: "vmaf",
		},
		{
			name: "both scores",
			pass: vmafPass{model: VMAFModelFHD, modelCAMBI: true, measures: VMAFMeasures{Fidelity: true, Original: true}},
			filter: `libvmaf=model=version=vmaf_v1.0.16_3d0h\\:cambi.cambi_max_val=0\\:name=fidelity|version=vmaf_v1.0.16_3d0h\\:name=original` +
				tail,
			fidelity: "fidelity", original: "original",
		},
		{
			name:     "both scores, model without CAMBI",
			pass:     vmafPass{model: VMAFModelV0FHD, measures: VMAFMeasures{Fidelity: true, Original: true}},
			filter:   `libvmaf=model=version=vmaf_v0.6.1` + tail,
			fidelity: "vmaf", original: "vmaf",
		},
		{
			name:   "banding only",
			pass:   vmafPass{model: VMAFModelFHD, modelCAMBI: true, measures: VMAFMeasures{Banding: true}},
			filter: `libvmaf=model=` + banding + tail,
		},
		{
			name:     "fidelity and banding",
			pass:     vmafPass{model: VMAFModelFHD, modelCAMBI: true, measures: VMAFMeasures{Fidelity: true, Banding: true}},
			filter:   `libvmaf=model=version=vmaf_v1.0.16_3d0h\\:cambi.cambi_max_val=0` + banding + tail,
			fidelity: "vmaf",
		},
		{
			name: "both scores and banding",
			pass: vmafPass{model: VMAFModelUHD, modelCAMBI: true, measures: VMAFMeasures{Fidelity: true, Original: true, Banding: true}},
			filter: `libvmaf=model=version=vmaf_v1.0.16_1d5h_2160\\:cambi.cambi_max_val=0\\:name=fidelity|version=vmaf_v1.0.16_1d5h_2160\\:name=original` +
				banding + tail,
			fidelity: "fidelity", original: "original",
		},
	} {
		if err := tc.pass.validate(); err != nil {
			t.Errorf("%s: %s", tc.name, err)
		}
		if got := tc.pass.filter(report, 8); got != tc.filter {
			t.Errorf("%s: want filter\n%s\ngot\n%s", tc.name, tc.filter, got)
		}
		if fidelity, original := tc.pass.scoreKeys(); fidelity != tc.fidelity || original != tc.original {
			t.Errorf("%s: want score keys %q and %q, got %q and %q", tc.name, tc.fidelity, tc.original, fidelity, original)
		}
	}
	// a pass measuring nothing, or loading a model that is not one, is refused; a banding pass
	// loads no model, whatever its name
	for _, pass := range []vmafPass{
		{model: VMAFModelFHD, modelCAMBI: true},
		{model: "a:b", measures: VMAFMeasures{Fidelity: true}},
	} {
		if err := pass.validate(); err == nil {
			t.Errorf("%+v should be refused", pass)
		}
	}
	if err := (vmafPass{model: "a:b", measures: VMAFMeasures{Banding: true}}).validate(); err != nil {
		t.Errorf("a banding pass loads no model: %s", err)
	}
}

// v1ReportSample is a libvmaf 3.2.0 report with the vmaf_v1.0.16_3d0h model, trimmed to 3
// frames and to the metrics of interest plus a few others: the feature keys are templated
// from the model options.
const v1ReportSample = `{
  "version": "3.2.0",
  "fps": 10.65,
  "frames": [
    {"frameNum": 0, "metrics": {"cambi_hrs_1080_cmxv_17_vlt_0.06": 5.187107, "speed_chroma_uv_mxv_45_nnf_0.1_snn_0.19_wvm_5": 0.393491, "integer_adm3_csf_2_dlmw_0.7_egl_1_min_0.5_nw_0.02": 0.983214, "integer_motion3_mmxv_18": 0.043983, "vmaf": 91.829956}},
    {"frameNum": 1, "metrics": {"cambi_hrs_1080_cmxv_17_vlt_0.06": 4.698233, "speed_chroma_uv_mxv_45_nnf_0.1_snn_0.19_wvm_5": 0.293687, "integer_adm3_csf_2_dlmw_0.7_egl_1_min_0.5_nw_0.02": 0.982643, "integer_motion3_mmxv_18": 0.043716, "vmaf": 92.099416}},
    {"frameNum": 2, "metrics": {"cambi_hrs_1080_cmxv_17_vlt_0.06": 3.997413, "speed_chroma_uv_mxv_45_nnf_0.1_snn_0.19_wvm_5": 0.308325, "integer_adm3_csf_2_dlmw_0.7_egl_1_min_0.5_nw_0.02": 0.982696, "integer_motion3_mmxv_18": 0.043456, "vmaf": 92.58156}}
  ],
  "pooled_metrics": {
    "cambi_hrs_1080_cmxv_17_vlt_0.06": {"min": 3.342183, "max": 5.187107, "mean": 4.254301, "harmonic_mean": 4.210},
    "integer_adm3_csf_2_dlmw_0.7_egl_1_min_0.5_nw_0.02": {"min": 0.98, "max": 0.99, "mean": 0.985, "harmonic_mean": 0.985},
    "vmaf": {"min": 91.829956, "max": 92.58156, "mean": 92.170311, "harmonic_mean": 92.169343}
  },
  "aggregate_metrics": {}
}`

func decodeSample(t *testing.T, sample string, pass vmafPass) VMAFReport {
	t.Helper()
	report, err := decodeVMAFReport(strings.NewReader(sample), pass)
	if err != nil {
		t.Fatalf("failed to decode the report: %s", err)
	}
	return report
}

func TestVMAFReport_Original(t *testing.T) {
	report := decodeSample(t, v1ReportSample, vmafPass{model: VMAFModelFHD, modelCAMBI: true, measures: VMAFMeasures{Original: true}})
	if report.Version != "3.2.0" || report.FPS != 10.65 {
		t.Errorf("version and fps: want 3.2.0 and 10.65, got %q and %v", report.Version, report.FPS)
	}
	if len(report.Frames) != 3 {
		t.Fatalf("frames: want 3, got %d", len(report.Frames))
	}
	if !report.HasOriginal || report.HasFidelity || report.HasBanding {
		t.Errorf("unexpected content: %+v", report)
	}
	if got := report.Frames[0].Original; got != 91.829956 {
		t.Errorf("frame 0 score: want 91.829956, got %v", got)
	}
	if got := report.Pooled.Original.HarmonicMean; got != 92.169343 {
		t.Errorf("pooled vmaf hmean: want 92.169343, got %v", got)
	}
	// the model's CAMBI, for VMAFCAMBIProbe
	const key = "cambi_hrs_1080_cmxv_17_vlt_0.06"
	if !slices.Equal(report.modelCAMBIKeys, []string{key}) || report.pooledCAMBIMax[key] != 5.187107 {
		t.Errorf("expected the model's CAMBI key %s at 5.187107, got %v and %v", key, report.modelCAMBIKeys, report.pooledCAMBIMax)
	}
	stats, err := report.Stats(VMAFScoreOriginal)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Version != "3.2.0" || stats.Minimum != 91.829956 || stats.Maximum != 92.58156 ||
		stats.Mean != 92.170311 || stats.HarmonicMean != 92.169343 {
		t.Errorf("unexpected pooled stats: %+v", stats)
	}
	if stats.Median != 92.099416 || stats.Percentile1 != 91.829956 {
		t.Errorf("median and p1: want 92.099416 and 91.829956, got %v and %v", stats.Median, stats.Percentile1)
	}
	// the statistics of a score, and nothing else: the model's CAMBI is no banding measure
	if rendered := stats.String(); !strings.Contains(rendered, "92.169343") || strings.Contains(rendered, "CAMBI") {
		t.Errorf("rendered stats should hold the score alone:\n%s", rendered)
	}
	// the frames keep their order: the percentiles are computed on a copy
	if report.Frames[0].Original != 91.829956 || report.Frames[2].Original != 92.58156 {
		t.Errorf("frames reordered: %+v", report.Frames)
	}
	// a score the pass did not compute is not read as zero
	if _, err := report.Stats(VMAFScoreFidelity); err == nil {
		t.Error("the report holds no fidelity score")
	}
}

// bothBandingReportSample is the report of a pass with both scores and the banding feature
// (vmaf_v1.0.16_3d0h clipped and as is, libvmaf f85a8536), 8-bit-like steps against the dark
// gradient of BENCHMARKS.md, trimmed to 2 frames and a few other metrics. cambi_full_reference
// sorts first among the CAMBI keys: a pick by prefix takes it for the model's CAMBI.
const bothBandingReportSample = `{
  "version": "f85a8536",
  "fps": 3.2,
  "frames": [
    {"frameNum": 0, "metrics": {"cambi_hrs_1080_cmxv_0_vlt_0.06": 0.000000, "integer_adm3_csf_2_dlmw_0.7_egl_1_min_0.5_nw_0.02": 0.986517, "cambi_hrs_1080_cmxv_17_vlt_0.06": 17.000000, "cambi_hrs_1080_vlt_0.06": 22.484294, "cambi_source": 6.653077, "cambi_full_reference": 15.831217, "integer_motion3_mmxv_18": 0.000000, "fidelity": 95.187485, "original": 84.518463}},
    {"frameNum": 1, "metrics": {"cambi_hrs_1080_cmxv_0_vlt_0.06": 0.000000, "integer_adm3_csf_2_dlmw_0.7_egl_1_min_0.5_nw_0.02": 0.986517, "cambi_hrs_1080_cmxv_17_vlt_0.06": 17.000000, "cambi_hrs_1080_vlt_0.06": 22.484294, "cambi_source": 6.653077, "cambi_full_reference": 15.831217, "integer_motion3_mmxv_18": 0.000000, "fidelity": 95.187485, "original": 84.518463}}
  ],
  "pooled_metrics": {
    "cambi_hrs_1080_cmxv_0_vlt_0.06": {"min": 0.000000, "max": 0.000000, "mean": 0.000000, "harmonic_mean": 0.000000},
    "cambi_hrs_1080_cmxv_17_vlt_0.06": {"min": 17.000000, "max": 17.000000, "mean": 17.000000, "harmonic_mean": 17.000000},
    "cambi_hrs_1080_vlt_0.06": {"min": 22.484294, "max": 22.484294, "mean": 22.484294, "harmonic_mean": 22.484294},
    "cambi_source": {"min": 6.653077, "max": 6.653077, "mean": 6.653077, "harmonic_mean": 6.653077},
    "cambi_full_reference": {"min": 15.831217, "max": 15.831217, "mean": 15.831217, "harmonic_mean": 15.831217},
    "fidelity": {"min": 95.187485, "max": 95.187485, "mean": 95.187485, "harmonic_mean": 95.187485},
    "original": {"min": 84.518463, "max": 84.518463, "mean": 84.518463, "harmonic_mean": 84.518463}
  },
  "aggregate_metrics": {}
}`

func TestVMAFReport_BothScoresAndBanding(t *testing.T) {
	report := decodeSample(t, bothBandingReportSample, vmafPass{model: VMAFModelFHD, modelCAMBI: true,
		measures: VMAFMeasures{Fidelity: true, Original: true, Banding: true}})
	if !report.HasFidelity || !report.HasOriginal || !report.HasBanding {
		t.Errorf("unexpected content: %+v", report)
	}
	frame := report.Frames[1]
	if frame.Fidelity != 95.187485 || frame.Original != 84.518463 {
		t.Errorf("frame 1 scores: want 95.187485 and 84.518463, got %v and %v", frame.Fidelity, frame.Original)
	}
	if want := (VMAFBanding{Encode: 22.484294, Source: 6.653077, Added: 15.831217}); frame.Banding != want {
		t.Errorf("frame 1 banding: want %+v, got %+v", want, frame.Banding)
	}
	if report.Pooled.Banding.Added.Mean != 15.831217 || report.Pooled.Banding.Source.Mean != 6.653077 ||
		report.Pooled.Banding.Encode.Max != 22.484294 {
		t.Errorf("unexpected pooled banding: %+v", report.Pooled.Banding)
	}
	// two models, two CAMBI keys of theirs: the banding feature's keys are never taken for one
	if want := []string{"cambi_hrs_1080_cmxv_0_vlt_0.06", "cambi_hrs_1080_cmxv_17_vlt_0.06"}; !slices.Equal(report.modelCAMBIKeys, want) {
		t.Errorf("model CAMBI keys: want %v, got %v", want, report.modelCAMBIKeys)
	}
	fidelity, err := report.Stats(VMAFScoreFidelity)
	if err != nil {
		t.Fatal(err)
	}
	original, err := report.Stats(VMAFScoreOriginal)
	if err != nil {
		t.Fatal(err)
	}
	if fidelity.HarmonicMean != 95.187485 || original.HarmonicMean != 84.518463 || fidelity.Median != 95.187485 {
		t.Errorf("unexpected stats: fidelity %+v, original %+v", fidelity, original)
	}
	// the summary of the score gated: its statistics, the other score, the banding
	for _, tc := range []struct {
		gated, other VMAFScore
		hmean        float64
		title, line  string
	}{
		{VMAFScoreFidelity, VMAFScoreOriginal, 84.518463, "Fidelity:\n", "Original score: harmonic mean 84.518463\n"},
		{VMAFScoreOriginal, VMAFScoreFidelity, 95.187485, "Original score:\n", "Fidelity: harmonic mean 95.187485\n"},
	} {
		summary, err := report.Summary(tc.gated, true)
		if err != nil {
			t.Fatal(err)
		}
		if summary.Score != tc.gated || summary.OtherScore != tc.other || summary.OtherHarmonicMean != tc.hmean ||
			!summary.HasBanding || summary.Banding != (VMAFBandingSummary{AddedMean: 15.831217, AddedMax: 15.831217, SourceMean: 6.653077, EncodeMean: 22.484294}) {
			t.Errorf("unexpected summary of %s: %+v", tc.gated, summary)
		}
		rendered := summary.String()
		if !strings.HasPrefix(rendered, tc.title) || !strings.Contains(rendered, tc.line) ||
			!strings.Contains(rendered, "Banding added (CAMBI, 0 = none, ~5 = slightly annoying): 15.831217 on average over the frames, 15.831217 on the worst frame. The reference rates 6.653077 on average, the distorted video 22.484294.") {
			t.Errorf("unexpected rendering of %s:\n%s", tc.gated, rendered)
		}
	}
	// the same report read for the fidelity and banding pass would miss its "vmaf" key
	if _, err := decodeVMAFReport(strings.NewReader(bothBandingReportSample), vmafPass{model: VMAFModelFHD, modelCAMBI: true,
		measures: VMAFMeasures{Fidelity: true, Banding: true}}); err == nil || !strings.Contains(err.Error(), `"vmaf"`) {
		t.Errorf("a missing score key should be an error naming it, got %v", err)
	}
}

// fidelityBandingReportSample is the report of a fidelity and banding pass: the model's clipped
// CAMBI is the only CAMBI key that is not the banding feature's.
const fidelityBandingReportSample = `{
  "version": "f85a8536",
  "frames": [
    {"frameNum": 0, "metrics": {"cambi_full_reference": 15.831217, "cambi_hrs_1080_cmxv_0_vlt_0.06": 0.0, "cambi_hrs_1080_vlt_0.06": 22.484294, "cambi_source": 6.653077, "vmaf": 95.187485}}
  ],
  "pooled_metrics": {
    "cambi_full_reference": {"min": 15.831217, "max": 15.831217, "mean": 15.831217, "harmonic_mean": 15.831217},
    "cambi_hrs_1080_cmxv_0_vlt_0.06": {"min": 0, "max": 0, "mean": 0, "harmonic_mean": 0},
    "cambi_hrs_1080_vlt_0.06": {"min": 22.484294, "max": 22.484294, "mean": 22.484294, "harmonic_mean": 22.484294},
    "cambi_source": {"min": 6.653077, "max": 6.653077, "mean": 6.653077, "harmonic_mean": 6.653077},
    "vmaf": {"min": 95.187485, "max": 95.187485, "mean": 95.187485, "harmonic_mean": 95.187485}
  }
}`

func TestVMAFReport_FidelityAndBanding(t *testing.T) {
	report := decodeSample(t, fidelityBandingReportSample, vmafPass{model: VMAFModelFHD, modelCAMBI: true,
		measures: VMAFMeasures{Fidelity: true, Banding: true}})
	frame := report.Frames[0]
	if frame.Fidelity != 95.187485 || frame.Banding.Added != 15.831217 {
		t.Errorf("unexpected frame: %+v", frame)
	}
	// the model's CAMBI is its own key, not the first "cambi" one
	const key = "cambi_hrs_1080_cmxv_0_vlt_0.06"
	if !slices.Equal(report.modelCAMBIKeys, []string{key}) || report.pooledCAMBIMax[key] != 0 {
		t.Errorf("expected the model's clipped CAMBI key %s at 0, got %v and %v", key, report.modelCAMBIKeys, report.pooledCAMBIMax)
	}
}

// bandingReportSample is the report of a banding pass: no model, no score.
const bandingReportSample = `{
  "version": "f85a8536",
  "frames": [
    {"frameNum": 0, "metrics": {"cambi_hrs_1080_vlt_0.06": 22.484294, "cambi_source": 6.653077, "cambi_full_reference": 15.831217}},
    {"frameNum": 1, "metrics": {"cambi_hrs_1080_vlt_0.06": 2.5, "cambi_source": 3.0, "cambi_full_reference": 0.0}}
  ],
  "pooled_metrics": {
    "cambi_hrs_1080_vlt_0.06": {"min": 2.5, "max": 22.484294, "mean": 12.492147, "harmonic_mean": 5.0},
    "cambi_source": {"min": 3.0, "max": 6.653077, "mean": 4.8265385, "harmonic_mean": 4.5},
    "cambi_full_reference": {"min": 0.0, "max": 15.831217, "mean": 7.9156085, "harmonic_mean": 1.9}
  }
}`

func TestVMAFReport_Banding(t *testing.T) {
	report := decodeSample(t, bandingReportSample, vmafPass{measures: VMAFMeasures{Banding: true}})
	if report.HasFidelity || report.HasOriginal || !report.HasBanding || len(report.modelCAMBIKeys) != 0 {
		t.Errorf("unexpected content: %+v", report)
	}
	// the distorted picture adds nothing where it is less banded than the reference
	if want := (VMAFBanding{Encode: 2.5, Source: 3, Added: 0}); report.Frames[1].Banding != want {
		t.Errorf("frame 1: want %+v, got %+v", want, report.Frames[1].Banding)
	}
	if report.Pooled.Banding.Added.Mean != 7.9156085 || report.Pooled.Banding.Added.Max != 15.831217 {
		t.Errorf("unexpected pooled added banding: %+v", report.Pooled.Banding.Added)
	}
	if _, err := report.Stats(VMAFScoreFidelity); err == nil {
		t.Error("a banding pass holds no score")
	}
}

// v0BandingReportSample is the report of a v0 model with the banding feature: the model has no
// CAMBI of its own, and its single score is both scores.
const v0BandingReportSample = `{
  "version": "f85a8536",
  "frames": [
    {"frameNum": 0, "metrics": {"integer_adm2": 1.008422, "integer_vif_scale0": 0.999998, "cambi_hrs_1080_vlt_0.06": 22.484294, "cambi_source": 6.653077, "cambi_full_reference": 15.831217, "integer_motion2": 0.0, "vmaf": 99.264425}}
  ],
  "pooled_metrics": {
    "cambi_hrs_1080_vlt_0.06": {"min": 22.484294, "max": 22.484294, "mean": 22.484294, "harmonic_mean": 22.484294},
    "cambi_source": {"min": 6.653077, "max": 6.653077, "mean": 6.653077, "harmonic_mean": 6.653077},
    "cambi_full_reference": {"min": 15.831217, "max": 15.831217, "mean": 15.831217, "harmonic_mean": 15.831217},
    "vmaf": {"min": 99.264425, "max": 99.264425, "mean": 99.264425, "harmonic_mean": 99.264425}
  }
}`

func TestVMAFReport_ModelWithoutCAMBI(t *testing.T) {
	report := decodeSample(t, v0BandingReportSample, vmafPass{model: VMAFModelV0FHD,
		measures: VMAFMeasures{Fidelity: true, Banding: true}})
	if !report.HasFidelity || !report.HasOriginal || len(report.modelCAMBIKeys) != 0 {
		t.Errorf("unexpected content: %+v", report)
	}
	fidelity, err := report.Stats(VMAFScoreFidelity)
	if err != nil {
		t.Fatal(err)
	}
	original, err := report.Stats(VMAFScoreOriginal)
	if err != nil {
		t.Fatal(err)
	}
	if fidelity != original || fidelity.Mean != 99.264425 {
		t.Errorf("both scores are the model's single one: fidelity %+v, original %+v", fidelity, original)
	}
	// its summary names no score, the banding of the banding feature is there
	summary, err := report.Summary(VMAFScoreFidelity, false)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Score != "" || summary.OtherScore != "" || !summary.HasBanding || summary.Banding.AddedMean != 15.831217 {
		t.Errorf("unexpected summary: %+v", summary)
	}
	if rendered := summary.String(); strings.Contains(rendered, "Fidelity") || strings.Contains(rendered, "Original") ||
		!strings.Contains(rendered, "Banding added") {
		t.Errorf("unexpected rendering:\n%s", rendered)
	}
	// without the banding feature, the summary of a model without CAMBI is its table alone, as
	// sptenc v0.1.0 printed it
	alone := decodeSample(t, `{"frames": [{"frameNum": 0, "metrics": {"vmaf": 99.264425}}], "pooled_metrics": {"vmaf": {"min": 99.264425, "max": 99.264425, "mean": 99.264425, "harmonic_mean": 99.264425}}}`,
		vmafPass{model: VMAFModelV0FHD, measures: VMAFMeasures{Fidelity: true}})
	if summary, err := alone.Summary(VMAFScoreFidelity, false); err != nil || summary.String() != summary.Stats.String() {
		t.Errorf("expected the table alone, got %v:\n%s", err, summary)
	}
}

// TestVMAFReport_Incomplete checks that what a pass asks for and a report lacks stops the read:
// libvmaf leaves it out while ffmpeg succeeds.
func TestVMAFReport_Incomplete(t *testing.T) {
	for _, tc := range []struct {
		name, sample, missing string
		pass                  vmafPass
	}{
		{
			// the banding feature taken for the model's CAMBI (see bandingFeature)
			name:    "banding feature merged",
			sample:  v1ReportSample,
			pass:    vmafPass{model: VMAFModelFHD, modelCAMBI: true, measures: VMAFMeasures{Original: true, Banding: true}},
			missing: bandingEncodeKey,
		},
		{
			// the pooled metrics lack the key, the frames have it
			name: "pooled banding missing",
			sample: `{"frames": [{"frameNum": 0, "metrics": {"cambi_hrs_1080_vlt_0.06": 1, "cambi_source": 1, "cambi_full_reference": 0}}],
				"pooled_metrics": {"cambi_hrs_1080_vlt_0.06": {"min": 1}, "cambi_source": {"min": 1}}}`,
			pass:    vmafPass{measures: VMAFMeasures{Banding: true}},
			missing: bandingAddedKey,
		},
		{
			// what libvmaf wrote for a banding pass on pictures CAMBI does not measure
			name:    "no frame",
			sample:  `{"version": "f85a8536", "frames": [], "pooled_metrics": {}, "aggregate_metrics": {}}`,
			pass:    vmafPass{measures: VMAFMeasures{Banding: true}},
			missing: "no frame",
		},
		{
			name:    "no pooled metrics",
			sample:  `{"frames": [{"frameNum": 0, "metrics": {"vmaf": 90}}]}`,
			pass:    vmafPass{model: VMAFModelV0FHD, measures: VMAFMeasures{Original: true}},
			missing: "no pooled metrics",
		},
		{
			name:    "no score",
			sample:  `{"frames": [{"frameNum": 0, "metrics": {"cambi_hrs_1080": 1.0}}], "pooled_metrics": {}}`,
			pass:    vmafPass{model: VMAFModelFHD, modelCAMBI: true, measures: VMAFMeasures{Original: true}},
			missing: `"vmaf"`,
		},
		{
			name:    "not a report",
			sample:  `[]`,
			pass:    vmafPass{model: VMAFModelFHD, modelCAMBI: true, measures: VMAFMeasures{Original: true}},
			missing: "expected",
		},
	} {
		_, err := decodeVMAFReport(strings.NewReader(tc.sample), tc.pass)
		if err == nil || !strings.Contains(err.Error(), tc.missing) {
			t.Errorf("%s: want an error about %s, got %v", tc.name, tc.missing, err)
		}
	}
}

// TestVMAFProbeSize runs the real thing: the smallest picture libvmaf scores depends on the
// model and on the pass, which is why the probe takes the size of the source and the passes of
// the run (see VMAFProbeConfig). The failing cases were measured with libvmaf f85a8536: a
// libvmaf scoring smaller pictures makes them pass, and the MANUAL figures need the same update.
func TestVMAFProbeSize(t *testing.T) {
	if _, err := exec.LookPath(FFMPEGBinary); err != nil {
		t.Skipf("%s not found: %s", FFMPEGBinary, err)
	}
	ctx := context.Background()
	dir := t.TempDir()
	var (
		original = VMAFMeasures{Original: true}
		fidelity = VMAFMeasures{Fidelity: true}
		banding  = VMAFMeasures{Banding: true}
	)
	for _, tc := range []struct {
		model         VMAFModel
		modelCAMBI    bool
		measures      VMAFMeasures
		width, height int
		ok            bool
	}{
		{VMAFModelFHD, true, original, 320, 180, true},
		{VMAFModelFHD, true, original, 16, 16, false},
		{VMAFModelFHD, true, original, 1920, 160, false}, // wide enough to matter at this height
		{VMAFModelPhone, true, original, 640, 360, true},
		{VMAFModelPhone, true, original, 320, 180, false}, // scored by VMAFModelFHD above
		{VMAFModelUHDFar, true, original, 640, 360, true},
		{VMAFModelUHDFar, true, original, 512, 288, false},
		// CAMBI measures nothing when both sides are under 216 pixels: the fidelity pass writes
		// no report, the banding pass reports nothing
		{VMAFModelFHD, true, fidelity, 216, 160, true},
		{VMAFModelFHD, true, fidelity, 215, 160, false},
		{VMAFModelFHD, true, banding, 160, 216, true},
		{VMAFModelFHD, true, banding, 160, 215, false},
		// a v0 model scores such pictures, the banding feature beside it does not
		{VMAFModelV0FHD, false, original, 215, 160, true},
		{VMAFModelV0FHD, false, VMAFMeasures{Original: true, Banding: true}, 215, 160, false},
		{VMAFModelV0FHD, false, VMAFMeasures{Original: true, Banding: true}, 216, 160, true},
	} {
		_, err := VMAFProbe(ctx, VMAFProbeConfig{Model: tc.model, ModelCAMBI: tc.modelCAMBI, Measures: tc.measures,
			Width: tc.width, Height: tc.height, ReportDir: dir})
		if errors.Is(err, ErrVMAFModelUnavailable) {
			t.Skipf("the libvmaf of %s does not know %s: %s", FFMPEGBinary, tc.model, err)
		}
		if (err == nil) != tc.ok {
			t.Errorf("%s %+v at %dx%d: expected success %t, got %v", tc.model, tc.measures, tc.width, tc.height, tc.ok, err)
		}
	}
	// no probe report left behind, crashes included
	if entries, err := os.ReadDir(dir); err != nil {
		t.Fatal(err)
	} else if len(entries) != 0 {
		t.Errorf("probe reports left behind: %v", entries)
	}
}

// TestVMAFProbe runs the real thing: it needs an ffmpeg with libvmaf 3.2.0 or newer in the
// PATH, and is skipped otherwise.
func TestVMAFProbe(t *testing.T) {
	if _, err := exec.LookPath(FFMPEGBinary); err != nil {
		t.Skipf("%s not found: %s", FFMPEGBinary, err)
	}
	ctx := context.Background()
	dir := t.TempDir()
	for _, model := range append(append(append([]VMAFModel{}, VMAFModels...), VMAFForcedModels...), VMAFV0Models...) {
		version, err := VMAFProbe(ctx, VMAFProbeConfig{Model: model, Measures: VMAFMeasures{Original: true}, ReportDir: dir})
		if errors.Is(err, ErrVMAFModelUnavailable) {
			t.Skipf("the libvmaf of %s does not know %s: %s", FFMPEGBinary, model, err)
		}
		if err != nil {
			t.Fatalf("probe with %s failed: %s", model, err)
		}
		if version == "" {
			t.Errorf("probe with %s returned no libvmaf version", model)
		}
	}
	// every pass a run can use, on a model feeding on CAMBI and on one without
	for _, tc := range []struct {
		model      VMAFModel
		modelCAMBI bool
	}{
		{VMAFModelFHD, true},
		{VMAFModelV0FHD, false},
	} {
		for _, measures := range []VMAFMeasures{
			{Fidelity: true},
			{Fidelity: true, Original: true},
			{Banding: true},
			{Fidelity: true, Banding: true},
			{Original: true, Banding: true},
			{Fidelity: true, Original: true, Banding: true},
		} {
			if _, err := VMAFProbe(ctx, VMAFProbeConfig{Model: tc.model, ModelCAMBI: tc.modelCAMBI, Measures: measures, ReportDir: dir}); err != nil {
				t.Errorf("probe of %s %+v failed: %s", tc.model, measures, err)
			}
		}
	}
	// a name libvmaf does not know is told apart from any other failure
	if _, err := VMAFProbe(ctx, VMAFProbeConfig{Model: "vmaf_v9.9.9_unknown", Measures: VMAFMeasures{Original: true}, ReportDir: dir}); !errors.Is(err, ErrVMAFModelUnavailable) {
		t.Errorf("an unknown model should be reported unavailable, got %v", err)
	}
	// and a name that is not one never reaches ffmpeg, nor a pass measuring nothing
	for _, config := range []VMAFProbeConfig{
		{Model: "a:b", Measures: VMAFMeasures{Original: true}, ReportDir: dir},
		{Model: VMAFModelFHD, ReportDir: dir},
	} {
		if _, err := VMAFProbe(ctx, config); err == nil {
			t.Errorf("%+v should be refused", config)
		}
	}
	if entries, err := os.ReadDir(dir); err != nil {
		t.Fatal(err)
	} else if len(entries) != 0 {
		t.Errorf("probe reports left behind: %v", entries)
	}
}

// TestVMAFCAMBIProbe runs the real thing: every v1 model feeds on CAMBI and takes the clip, no v0
// model feeds on it (libvmaf ignores the clip on them, see cambiClipParam).
func TestVMAFCAMBIProbe(t *testing.T) {
	if _, err := exec.LookPath(FFMPEGBinary); err != nil {
		t.Skipf("%s not found: %s", FFMPEGBinary, err)
	}
	ctx := context.Background()
	dir := t.TempDir()
	for _, tc := range []struct {
		models     []VMAFModel
		modelCAMBI bool
	}{
		{append(append([]VMAFModel{}, VMAFModels...), VMAFForcedModels...), true},
		{VMAFV0Models, false},
	} {
		for _, model := range tc.models {
			modelCAMBI, err := VMAFCAMBIProbe(ctx, VMAFCAMBIProbeConfig{Model: model, ReportDir: dir})
			if errors.Is(err, ErrVMAFModelUnavailable) {
				t.Skipf("the libvmaf of %s does not know %s: %s", FFMPEGBinary, model, err)
			}
			if err != nil {
				t.Fatalf("CAMBI probe of %s failed: %s", model, err)
			}
			if modelCAMBI != tc.modelCAMBI {
				t.Errorf("%s: expected to feed on CAMBI %t, got %t", model, tc.modelCAMBI, modelCAMBI)
			}
		}
	}
	if _, err := VMAFCAMBIProbe(ctx, VMAFCAMBIProbeConfig{Model: "vmaf_v9.9.9_unknown", ReportDir: dir}); !errors.Is(err, ErrVMAFModelUnavailable) {
		t.Errorf("an unknown model should be reported unavailable, got %v", err)
	}
	if _, err := VMAFCAMBIProbe(ctx, VMAFCAMBIProbeConfig{Model: "a:b", ReportDir: dir}); err == nil {
		t.Error("an invalid model name should be refused")
	}
	if entries, err := os.ReadDir(dir); err != nil {
		t.Fatal(err)
	} else if len(entries) != 0 {
		t.Errorf("probe reports left behind: %v", entries)
	}
}

// TestVMAFCompute runs the real thing on the 8-bit-like steps of the dark gradient of
// BENCHMARKS.md (Synthetic clips): the figures of its table, which a libvmaf scoring otherwise
// changes along with that table.
func TestVMAFCompute(t *testing.T) {
	if _, err := exec.LookPath(FFMPEGBinary); err != nil {
		t.Skipf("%s not found: %s", FFMPEGBinary, err)
	}
	ctx := context.Background()
	dir := t.TempDir()
	clip := func(name, luma string) string {
		path := filepath.Join(dir, name)
		out, err := exec.Command(FFMPEGBinary, "-loglevel", "error", "-nostdin", "-y", "-f", "lavfi", "-i",
			"color=black:s=1920x1080:r=24:d=0.5,format=yuv420p10le,geq=lum='"+luma+"':cb=512:cr=512",
			"-c:v", "ffv1", path).CombinedOutput()
		if err != nil {
			t.Fatalf("failed to generate %s: %s\n%s", name, err, out)
		}
		return path
	}
	gradient := clip("gradient.mkv", "64+floor(X/12)")
	steps := clip("steps.mkv", "64+4*floor(X/48)")
	compute := func(model VMAFModel, modelCAMBI bool, measures VMAFMeasures) VMAFReport {
		report, err := VMAFCompute(ctx, VMAFComputeConfig{
			ReferencePath:  gradient,
			DistortedPath:  steps,
			InputFrameRate: "24",
			ReportPath:     filepath.Join(dir, "report.json"),
			Model:          model,
			ModelCAMBI:     modelCAMBI,
			Measures:       measures,
		})
		if errors.Is(err, ErrVMAFModelUnavailable) {
			t.Skipf("the libvmaf of %s does not know %s: %s", FFMPEGBinary, model, err)
		}
		if err != nil {
			t.Fatalf("VMAF of %s %+v failed: %s", model, measures, err)
		}
		if len(report.Frames) != 12 {
			t.Fatalf("expected 12 frames, got %d", len(report.Frames))
		}
		return report
	}
	round := func(v float64) float64 { return math.Round(v*100) / 100 }
	// v1: fidelity passes at 95 where the original score falls to 84.52, CAMBI rating the steps
	// 22.48 against 6.65 for the gradient
	report := compute(VMAFModelFHD, true, VMAFMeasures{Fidelity: true, Original: true, Banding: true})
	if got := round(report.Pooled.Fidelity.HarmonicMean); got != 95.19 {
		t.Errorf("fidelity: want 95.19, got %v", got)
	}
	if got := round(report.Pooled.Original.HarmonicMean); got != 84.52 {
		t.Errorf("original score: want 84.52, got %v", got)
	}
	if got := (VMAFBanding{Encode: round(report.Pooled.Banding.Encode.Mean), Source: round(report.Pooled.Banding.Source.Mean),
		Added: round(report.Pooled.Banding.Added.Mean)}); got != (VMAFBanding{Encode: 22.48, Source: 6.65, Added: 15.83}) {
		t.Errorf("banding: want 22.48, 6.65 and 15.83 added, got %+v", got)
	}
	// the same scores in passes of their own
	if fidelity := compute(VMAFModelFHD, true, VMAFMeasures{Fidelity: true}); fidelity.Pooled.Fidelity != report.Pooled.Fidelity {
		t.Errorf("fidelity alone: want %+v, got %+v", report.Pooled.Fidelity, fidelity.Pooled.Fidelity)
	}
	if banding := compute(VMAFModelFHD, true, VMAFMeasures{Banding: true}); banding.Pooled.Banding != report.Pooled.Banding {
		t.Errorf("banding alone: want %+v, got %+v", report.Pooled.Banding, banding.Pooled.Banding)
	}
	// v0 sees the steps as an enhancement: 99.26, one score for both
	v0 := compute(VMAFModelV0FHD, false, VMAFMeasures{Fidelity: true, Original: true})
	if got := round(v0.Pooled.Fidelity.HarmonicMean); got != 99.26 || v0.Pooled.Original != v0.Pooled.Fidelity {
		t.Errorf("v0: want 99.26 for both scores, got %+v and %+v", v0.Pooled.Fidelity, v0.Pooled.Original)
	}
}
