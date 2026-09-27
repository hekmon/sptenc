package metadata

import (
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/hekmon/sptenc/core"
	"github.com/hekmon/sptenc/ffmpeg"
)

// tagsOf reads the tags out of the ffmpeg metadata flags.
func tagsOf(t *testing.T, flags ffmpeg.FFMEGTags) map[string]string {
	t.Helper()
	tags := make(map[string]string, len(flags)/2)
	for i := 0; i+1 < len(flags); i += 2 {
		if flags[i] != "-metadata:s:v:0" {
			t.Fatalf("unexpected flag %q", flags[i])
		}
		key, value, _ := strings.Cut(flags[i+1], "=")
		tags[key] = value
	}
	return tags
}

func TestGenerateTags(t *testing.T) {
	vmafChecker, err := core.NewVMAFChecker(core.VMAFOffValue, core.VMAFOffValue, core.VMAFOffValue, core.VMAFOffValue,
		core.VMAFOffValue, core.VMAFOffValue, 93, core.VMAFOffValue)
	if err != nil {
		t.Fatal(err)
	}
	meanGate, _ := core.NewCAMBIChecker(1, core.CAMBIOffValue)
	bothGates, _ := core.NewCAMBIChecker(1, 2.5)
	stats := ffmpeg.VMAFStats{Minimum: 88, Percentile1: 89, Percentile5: 90, Percentile10: 91, Percentile25: 92,
		Median: 94, HarmonicMean: 93.5, Mean: 93.6, Maximum: 99}
	banding := ffmpeg.VMAFBandingSummary{AddedMean: 0.25, AddedMax: 3.5, SourceMean: 6.5, EncodeMean: 6.75}
	// two segments: the first lowered by the CAMBI gate, the second a best effort of it and of VMAF
	results := core.QPSearchResults{QPs: []int{20, 0}, VMAFSearchQPs: []int{22, 0}, GlobalWeightedQP: 10,
		NbBestEfforts: 1, NbCAMBIBestEfforts: 1}
	// the tags of sptenc v0.1.0
	v010 := []string{"sptenc_url", "sptenc_version", "vendor_id", "sptenc_segments_count", "sptenc_encoder",
		"sptenc_encoder_preset", "sptenc_stats_min_qp", "sptenc_stats_max_qp", "sptenc_stats_weighted_qp",
		"sptenc_vmaf_model", "sptenc_vmaf_conf_hmean", "sptenc_best_effort_segments",
		"sptenc_vmaf_result_min", "sptenc_vmaf_result_p1", "sptenc_vmaf_result_p5", "sptenc_vmaf_result_p10",
		"sptenc_vmaf_result_p25", "sptenc_vmaf_result_median", "sptenc_vmaf_result_hmean", "sptenc_vmaf_result_mean",
		"sptenc_vmaf_result_max"}
	cambiResults := map[string]string{
		"sptenc_cambi_result_added_mean":    "0.25",
		"sptenc_cambi_result_added_max":     "3.5",
		"sptenc_cambi_result_source_mean":   "6.5",
		"sptenc_cambi_result_encode_mean":   "6.75",
		"sptenc_cambi_lowered_segments":     "1",
		"sptenc_cambi_best_effort_segments": "1",
	}
	for _, tc := range []struct {
		name    string
		cambi   core.CAMBIChecker
		summary ffmpeg.VMAFSummary
		model   ffmpeg.VMAFModel
		extra   map[string]string // the tags beyond v0.1.0's, with their value
	}{
		{
			name:  "fidelity and the CAMBI gate",
			cambi: meanGate,
			summary: ffmpeg.VMAFSummary{Score: ffmpeg.VMAFScoreFidelity, Stats: stats,
				OtherScore: ffmpeg.VMAFScoreOriginal, OtherHarmonicMean: 91.25, HasBanding: true, Banding: banding},
			model: ffmpeg.VMAFModelFHD,
			extra: merge(map[string]string{
				"sptenc_vmaf_score":                 "fidelity",
				"sptenc_vmaf_original_result_hmean": "91.25",
				"sptenc_cambi_conf_mean":            "1",
			}, cambiResults),
		},
		{
			name:  "original score and both CAMBI thresholds",
			cambi: bothGates,
			summary: ffmpeg.VMAFSummary{Score: ffmpeg.VMAFScoreOriginal, Stats: stats,
				OtherScore: ffmpeg.VMAFScoreFidelity, OtherHarmonicMean: 95.5, HasBanding: true, Banding: banding},
			model: ffmpeg.VMAFModelFHD,
			extra: merge(map[string]string{
				"sptenc_vmaf_score":                 "original",
				"sptenc_vmaf_fidelity_result_hmean": "95.5",
				"sptenc_cambi_conf_mean":            "1",
				"sptenc_cambi_conf_max":             "2.5",
			}, cambiResults),
		},
		{
			// a model without CAMBI and the gate off: the tags of v0.1.0, nothing else
			name:    "v0, no gate",
			summary: ffmpeg.VMAFSummary{Stats: stats},
			model:   ffmpeg.VMAFModelV0FHD,
		},
		{
			// the gate turned on by a threshold: the CAMBI tags, still no score named
			name:    "v0 and the CAMBI gate",
			cambi:   meanGate,
			summary: ffmpeg.VMAFSummary{Stats: stats, HasBanding: true, Banding: banding},
			model:   ffmpeg.VMAFModelV0FHD,
			extra:   merge(map[string]string{"sptenc_cambi_conf_mean": "1"}, cambiResults),
		},
	} {
		tags := tagsOf(t, GenerateTags(vmafChecker, tc.cambi, ffmpeg.HEVCEncoderNVEnc, results, tc.summary, tc.model, 2))
		want := append(slices.Clone(v010), slices.Collect(maps.Keys(tc.extra))...)
		slices.Sort(want)
		if got := slices.Sorted(maps.Keys(tags)); !slices.Equal(got, want) {
			t.Errorf("%s: want the tags\n%v\ngot\n%v", tc.name, want, got)
		}
		for key, value := range tc.extra {
			if tags[key] != value {
				t.Errorf("%s: %s: want %q, got %q", tc.name, key, value, tags[key])
			}
		}
		if tags["sptenc_vmaf_result_hmean"] != "93.5" || tags["sptenc_vmaf_model"] != string(tc.model) {
			t.Errorf("%s: unexpected results: %v", tc.name, tags)
		}
	}
	// counts of zero are left out
	results.NbBestEfforts, results.NbCAMBIBestEfforts, results.VMAFSearchQPs = 0, 0, results.QPs
	tags := tagsOf(t, GenerateTags(vmafChecker, meanGate, ffmpeg.HEVCEncoderNVEnc, results,
		ffmpeg.VMAFSummary{Score: ffmpeg.VMAFScoreFidelity, Stats: stats, HasBanding: true, Banding: banding}, ffmpeg.VMAFModelFHD, 2))
	for _, key := range []string{"sptenc_best_effort_segments", "sptenc_cambi_lowered_segments", "sptenc_cambi_best_effort_segments"} {
		if _, found := tags[key]; found {
			t.Errorf("%s should be left out at zero", key)
		}
	}
}

func merge(a, b map[string]string) map[string]string {
	merged := maps.Clone(a)
	maps.Copy(merged, b)
	return merged
}
