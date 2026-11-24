package metadata

import (
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hekmon/sptenc/core"
	"github.com/hekmon/sptenc/ffmpeg"
	"github.com/hekmon/sptenc/pipeline"
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
		// no scene cuts: a pre-split directory, see TestGenerateTagsSceneCuts
		tags := tagsOf(t, GenerateTags(vmafChecker, tc.cambi, ffmpeg.HEVCEncoderNVEnc, results, tc.summary, tc.model, 2, nil))
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
		ffmpeg.VMAFSummary{Score: ffmpeg.VMAFScoreFidelity, Stats: stats, HasBanding: true, Banding: banding}, ffmpeg.VMAFModelFHD, 2, nil))
	for _, key := range []string{"sptenc_best_effort_segments", "sptenc_cambi_lowered_segments", "sptenc_cambi_best_effort_segments"} {
		if _, found := tags[key]; found {
			t.Errorf("%s should be left out at zero", key)
		}
	}
}

// TestGenerateTagsSceneCuts checks the scene tags: written with the cuts of a detection (encode on
// a file, batchsearch), none for a pre-split directory, the other tags the same either way. The
// helper's version and model are written for a detector with a helper alone (transnetv2), never
// for scdet, even when the fields are set.
func TestGenerateTagsSceneCuts(t *testing.T) {
	vmafChecker, err := core.NewVMAFChecker(core.VMAFOffValue, core.VMAFOffValue, core.VMAFOffValue, core.VMAFOffValue,
		core.VMAFOffValue, core.VMAFOffValue, 93, core.VMAFOffValue)
	if err != nil {
		t.Fatal(err)
	}
	cambi, _ := core.NewCAMBIChecker(1, core.CAMBIOffValue)
	results := core.QPSearchResults{QPs: []int{20, 22}, VMAFSearchQPs: []int{20, 22}, GlobalWeightedQP: 21}
	summary := ffmpeg.VMAFSummary{Score: ffmpeg.VMAFScoreFidelity, Stats: ffmpeg.VMAFStats{HarmonicMean: 93.5}}
	sceneKeys := []string{"sptenc_scene_detector", "sptenc_scene_threshold", "sptenc_scene_min_segment_length",
		"sptenc_scene_detector_version", "sptenc_scene_detector_model_sha256"}
	const sha256 = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	preSplit := tagsOf(t, GenerateTags(vmafChecker, cambi, ffmpeg.HEVCEncoderLibx265, results, summary, ffmpeg.VMAFModelFHD, 2, nil))
	for _, key := range sceneKeys {
		if _, found := preSplit[key]; found {
			t.Errorf("pre-split directory: %s should be left out", key)
		}
	}
	for _, tc := range []struct {
		name     string
		cuts     SceneCuts
		expected map[string]string
	}{
		{
			name: "encode, default threshold",
			cuts: SceneCuts{Detector: pipeline.SceneDetectorScdet, Threshold: 10, MinSegmentLength: 5 * time.Second},
			expected: map[string]string{"sptenc_scene_detector": "scdet", "sptenc_scene_threshold": "10",
				"sptenc_scene_min_segment_length": "5s"},
		},
		{
			name: "batchsearch winner, its reusable threshold",
			cuts: SceneCuts{Detector: pipeline.SceneDetectorScdet, Threshold: 24.2765, MinSegmentLength: 2500 * time.Millisecond},
			expected: map[string]string{"sptenc_scene_detector": "scdet", "sptenc_scene_threshold": "24.2765",
				"sptenc_scene_min_segment_length": "2.5s"},
		},
		{
			name: "no minimum segment length",
			cuts: SceneCuts{Detector: pipeline.SceneDetectorScdet, Threshold: 12.5},
			expected: map[string]string{"sptenc_scene_detector": "scdet", "sptenc_scene_threshold": "12.5",
				"sptenc_scene_min_segment_length": "0s"},
		},
		{
			name: "scdet has no helper: its fields are ignored",
			cuts: SceneCuts{Detector: pipeline.SceneDetectorScdet, Threshold: 10, MinSegmentLength: 5 * time.Second,
				DetectorVersion: "0.1.0", DetectorModelSHA256: sha256},
			expected: map[string]string{"sptenc_scene_detector": "scdet", "sptenc_scene_threshold": "10",
				"sptenc_scene_min_segment_length": "5s"},
		},
		{
			name: "transnetv2, its helper recorded",
			cuts: SceneCuts{Detector: pipeline.SceneDetectorTransNetV2, Threshold: 0.5, MinSegmentLength: 5 * time.Second,
				DetectorVersion: "0.1.0", DetectorModelSHA256: sha256},
			expected: map[string]string{"sptenc_scene_detector": "transnetv2", "sptenc_scene_threshold": "0.5",
				"sptenc_scene_min_segment_length": "5s", "sptenc_scene_detector_version": "0.1.0",
				"sptenc_scene_detector_model_sha256": sha256},
		},
		{
			name: "transnetv2 batchsearch winner, a score of 4 decimals",
			cuts: SceneCuts{Detector: pipeline.SceneDetectorTransNetV2, Threshold: 0.6123, MinSegmentLength: 5 * time.Second,
				DetectorVersion: "0.2.0", DetectorModelSHA256: sha256},
			expected: map[string]string{"sptenc_scene_detector": "transnetv2", "sptenc_scene_threshold": "0.6123",
				"sptenc_scene_min_segment_length": "5s", "sptenc_scene_detector_version": "0.2.0",
				"sptenc_scene_detector_model_sha256": sha256},
		},
	} {
		tags := tagsOf(t, GenerateTags(vmafChecker, cambi, ffmpeg.HEVCEncoderLibx265, results, summary, ffmpeg.VMAFModelFHD, 2, &tc.cuts))
		for key, value := range tc.expected {
			if tags[key] != value {
				t.Errorf("%s: %s: want %q, got %q", tc.name, key, value, tags[key])
			}
		}
		for _, key := range sceneKeys {
			if _, expected := tc.expected[key]; !expected {
				if value, found := tags[key]; found {
					t.Errorf("%s: %s should be left out, got %q", tc.name, key, value)
				}
			}
		}
		// the scene tags on top of the others, nothing else changed
		for _, key := range sceneKeys {
			delete(tags, key)
		}
		if !maps.Equal(tags, preSplit) {
			t.Errorf("%s: the other tags differ from a pre-split directory's:\n%v\n%v", tc.name, tags, preSplit)
		}
	}
}

func merge(a, b map[string]string) map[string]string {
	merged := maps.Clone(a)
	maps.Copy(merged, b)
	return merged
}
