package ffmpeg

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
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

// TestVMAFProbeSize runs the real thing: the smallest picture libvmaf scores depends on the
// model, which is why the probe takes the size of the source (see VMAFProbeConfig). The
// failing cases were measured with libvmaf f85a8536: a libvmaf scoring smaller pictures makes
// them pass, and the MANUAL figures need the same update.
func TestVMAFProbeSize(t *testing.T) {
	if _, err := exec.LookPath(FFMPEGBinary); err != nil {
		t.Skipf("%s not found: %s", FFMPEGBinary, err)
	}
	ctx := context.Background()
	dir := t.TempDir()
	for _, tc := range []struct {
		model         VMAFModel
		width, height int
		ok            bool
	}{
		{VMAFModelFHD, 320, 180, true},
		{VMAFModelFHD, 16, 16, false},
		{VMAFModelFHD, 1920, 160, false}, // wide enough to matter at this height
		{VMAFModelPhone, 640, 360, true},
		{VMAFModelPhone, 320, 180, false}, // scored by VMAFModelFHD above
		{VMAFModelUHDFar, 640, 360, true},
		{VMAFModelUHDFar, 512, 288, false},
	} {
		_, err := VMAFProbe(ctx, VMAFProbeConfig{Model: tc.model, Width: tc.width, Height: tc.height, ReportDir: dir})
		if errors.Is(err, ErrVMAFModelUnavailable) {
			t.Skipf("the libvmaf of %s does not know %s: %s", FFMPEGBinary, tc.model, err)
		}
		if (err == nil) != tc.ok {
			t.Errorf("%s at %dx%d: expected success %t, got %v", tc.model, tc.width, tc.height, tc.ok, err)
		}
	}
	// no probe report left behind, crashes included
	if entries, err := os.ReadDir(dir); err != nil {
		t.Fatal(err)
	} else if len(entries) != 0 {
		t.Errorf("probe reports left behind: %v", entries)
	}
}

func TestVMAFFilter(t *testing.T) {
	got := vmafFilter(VMAFModelFHD, `C:\tmp\seg_000001.mkv_vmaf.json`, 8)
	want := `libvmaf=model=version=vmaf_v1.0.16_3d0h:log_fmt=json:log_path=C\\:\\\\tmp\\\\seg_000001.mkv_vmaf.json:n_threads=8`
	if got != want {
		t.Errorf("want %q, got %q", want, got)
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

func TestVMAFReport_Unmarshal(t *testing.T) {
	var report VMAFReport
	if err := json.Unmarshal([]byte(v1ReportSample), &report); err != nil {
		t.Fatalf("failed to parse the v1 report: %s", err)
	}
	if report.Version != "3.2.0" {
		t.Errorf("version: want 3.2.0, got %q", report.Version)
	}
	if len(report.Frames) != 3 {
		t.Fatalf("frames: want 3, got %d", len(report.Frames))
	}
	if got := report.Frames[0].Metrics.VMAF; got != 91.829956 {
		t.Errorf("frame 0 vmaf: want 91.829956, got %v", got)
	}
	if got := report.Frames[0].Metrics.CAMBI; got != 5.187107 {
		t.Errorf("frame 0 cambi: want 5.187107, got %v", got)
	}
	if got := report.PooledMetrics.VMAF.HarmonicMean; got != 92.169343 {
		t.Errorf("pooled vmaf hmean: want 92.169343, got %v", got)
	}
	if got := report.PooledMetrics.CAMBI.Max; got != 5.187107 {
		t.Errorf("pooled cambi max: want 5.187107, got %v", got)
	}
	stats := report.GetStats()
	if stats.Version != "3.2.0" || stats.Minimum != 91.829956 || stats.Maximum != 92.58156 ||
		stats.Mean != 92.170311 || stats.HarmonicMean != 92.169343 {
		t.Errorf("unexpected pooled stats: %+v", stats)
	}
	if stats.Median != 92.099416 {
		t.Errorf("median: want 92.099416, got %v", stats.Median)
	}
	if stats.CAMBIMean != 4.254301 || stats.CAMBIMax != 5.187107 {
		t.Errorf("cambi stats: want mean 4.254301 max 5.187107, got mean %v max %v", stats.CAMBIMean, stats.CAMBIMax)
	}
	if rendered := stats.String(); !strings.Contains(rendered, "92.169343") || !strings.Contains(rendered, "CAMBI") {
		t.Errorf("rendered stats miss the score or the banding line:\n%s", rendered)
	}
}

func TestVMAFReport_UnmarshalNoScore(t *testing.T) {
	var frame VMAFFrame
	if err := json.Unmarshal([]byte(`{"frameNum": 0, "metrics": {"cambi_hrs_1080": 1.0}}`), &frame); err == nil {
		t.Error("a frame without a vmaf metric should be an error")
	}
	// A report without CAMBI is a report of another model: the score is still read
	var pooled VMAFPooledMetrics
	if err := json.Unmarshal([]byte(`{"vmaf": {"min": 1, "max": 2, "mean": 1.5, "harmonic_mean": 1.4}}`), &pooled); err != nil {
		t.Fatalf("pooled metrics without cambi should parse: %s", err)
	}
	if pooled.VMAF.Mean != 1.5 || pooled.CAMBI.Max != 0 || pooled.HasCAMBI {
		t.Errorf("unexpected pooled metrics: %+v", pooled)
	}
	// and the zero CAMBI of a model without it (a v0 one forced) is not printed as "no banding"
	if table := (VMAFReport{PooledMetrics: pooled}).GetStats().String(); strings.Contains(table, "Banding") {
		t.Errorf("no banding line expected without CAMBI:\n%s", table)
	}
}

func TestFindMetricKeys(t *testing.T) {
	vmafKey, cambiKey := findMetricKeys([]string{"integer_motion3_mmxv_18", "cambi_hrs_1080_cmxv_17_vlt_0.06", "vmaf"})
	if vmafKey != "vmaf" || cambiKey != "cambi_hrs_1080_cmxv_17_vlt_0.06" {
		t.Errorf("got vmaf %q cambi %q", vmafKey, cambiKey)
	}
	vmafKey, cambiKey = findMetricKeys([]string{"psnr_y"})
	if vmafKey != "" || cambiKey != "" {
		t.Errorf("nothing should match, got vmaf %q cambi %q", vmafKey, cambiKey)
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
		version, err := VMAFProbe(ctx, VMAFProbeConfig{Model: model, ReportDir: dir})
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
	// a name libvmaf does not know is told apart from any other failure
	if _, err := VMAFProbe(ctx, VMAFProbeConfig{Model: "vmaf_v9.9.9_unknown", ReportDir: dir}); !errors.Is(err, ErrVMAFModelUnavailable) {
		t.Errorf("an unknown model should be reported unavailable, got %v", err)
	}
	// and a name that is not one never reaches ffmpeg
	if _, err := VMAFProbe(ctx, VMAFProbeConfig{Model: "a:b", ReportDir: dir}); err == nil {
		t.Error("an invalid model name should be refused")
	}
	if entries, err := os.ReadDir(dir); err != nil {
		t.Fatal(err)
	} else if len(entries) != 0 {
		t.Errorf("probe reports left behind: %v", entries)
	}
}
