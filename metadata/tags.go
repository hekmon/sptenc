package metadata

import (
	"fmt"
	"runtime/debug"
	"strconv"
	"time"

	"github.com/hekmon/sptenc/core"
	"github.com/hekmon/sptenc/ffmpeg"
	"github.com/hekmon/sptenc/pipeline"
)

const (
	sptencURLTagKey           = "sptenc_url"
	sptencVersionTagKey       = "sptenc_version"
	sptencEncoderTagKey       = "sptenc_encoder"
	sptencEncoderPresetTagKey = "sptenc_encoder_preset"
	sptencSegmentsCountTagKey = "sptenc_segments_count"
	// Scene cuts
	sptencSceneDetectorTagKey         = "sptenc_scene_detector"
	sptencSceneThresholdTagKey        = "sptenc_scene_threshold"
	sptencSceneMinSegmentLengthTagKey = "sptenc_scene_min_segment_length"
	// the helper of a detector that runs in one (see pipeline.SceneDetector.HasHelper)
	sptencSceneDetectorVersionTagKey     = "sptenc_scene_detector_version"
	sptencSceneDetectorModelSHA256TagKey = "sptenc_scene_detector_model_sha256"
	// Encoding QP stats
	sptencStatsMinQP      = "sptenc_stats_min_qp"
	sptencStatsMaxQP      = "sptenc_stats_max_qp"
	sptencStatsWeightedQP = "sptenc_stats_weighted_qp"
	// VMAF Infos
	sptencVMAFModelTagKey = "sptenc_vmaf_model"
	sptencVMAFScoreTagKey = "sptenc_vmaf_score"
	// VMAF Conf
	sptencVMAFConfMinAltTagKey = "sptenc_vmaf_conf_min_alt"
	sptencVMAFConfMinTagKey    = "sptenc_vmaf_conf_min"
	sptencVMAFConfP1TagKey     = "sptenc_vmaf_conf_p1"
	sptencVMAFConfP5TagKey     = "sptenc_vmaf_conf_p5"
	sptencVMAFConfP10TagKey    = "sptenc_vmaf_conf_p10"
	sptencVMAFConfP25TagKey    = "sptenc_vmaf_conf_p25"
	sptencVMAFConfMedianTagKey = "sptenc_vmaf_conf_median"
	sptencVMAFConfHMeanTagKey  = "sptenc_vmaf_conf_hmean"
	sptencVMAFConfMeanTagKey   = "sptenc_vmaf_conf_mean"
	// VMAF Results
	sptencVMAFResultMinTagKey    = "sptenc_vmaf_result_min"
	sptencVMAFResultP1TagKey     = "sptenc_vmaf_result_p1"
	sptencVMAFResultP5TagKey     = "sptenc_vmaf_result_p5"
	sptencVMAFResultP10TagKey    = "sptenc_vmaf_result_p10"
	sptencVMAFResultP25TagKey    = "sptenc_vmaf_result_p25"
	sptencVMAFResultMedianTagKey = "sptenc_vmaf_result_median"
	sptencVMAFResultHMeanTagKey  = "sptenc_vmaf_result_hmean"
	sptencVMAFResultMeanTagKey   = "sptenc_vmaf_result_mean"
	sptencVMAFResultMaxTagKey    = "sptenc_vmaf_result_max"
	// the harmonic mean of the score not gated: sptenc_vmaf_original_result_hmean when fidelity was,
	// sptenc_vmaf_fidelity_result_hmean when the original score was
	sptencVMAFOtherResultHMeanTagKeyFormat = "sptenc_vmaf_%s_result_hmean"
	sptencBestEffortTagKey                 = "sptenc_best_effort_segments"
	// CAMBI Conf
	sptencCAMBIConfMeanTagKey = "sptenc_cambi_conf_mean"
	sptencCAMBIConfMaxTagKey  = "sptenc_cambi_conf_max"
	// CAMBI Results
	sptencCAMBIResultAddedMeanTagKey  = "sptenc_cambi_result_added_mean"
	sptencCAMBIResultAddedMaxTagKey   = "sptenc_cambi_result_added_max"
	sptencCAMBIResultSourceMeanTagKey = "sptenc_cambi_result_source_mean"
	sptencCAMBIResultEncodeMeanTagKey = "sptenc_cambi_result_encode_mean"
	sptencCAMBILoweredTagKey          = "sptenc_cambi_lowered_segments"
	sptencCAMBIBestEffortTagKey       = "sptenc_cambi_best_effort_segments"
)

// SceneCuts tells how the segments of an encode were cut, when sptenc detected its scenes (encode
// on a file, batchsearch): the detector, the threshold that reproduces the cuts with encode -T and
// that detector (for batchsearch, the reusable threshold of the winner), and the minimum segment
// length applied after it. Together they are enough to cut the source again the same way.
//
// A detector that runs in a helper (see pipeline.SceneDetector.HasHelper) scores with the helper's
// model: another version or another model can score the same frames otherwise, and the threshold
// would not reproduce the cuts. So the version of the helper that detected the scenes and the
// SHA-256 of its model are recorded too: the caller fills them from the helper's version line
// (transnetv2.VersionInfo). They are ignored for a detector without a helper.
type SceneCuts struct {
	Detector         pipeline.SceneDetector
	Threshold        float64
	MinSegmentLength time.Duration
	// The helper's, for a detector that runs in one
	DetectorVersion     string // the helper's own version
	DetectorModelSHA256 string // the SHA-256 of the model the helper runs, in hexadecimal
}

// GenerateTags builds the ffmpeg metadata flags documenting an encode. They are all set on the
// video stream: container level metadata (the title in particular) belongs to the user and is
// passed through from the source untouched.
//
// The scene tags are written when sptenc detected the scenes (cuts not nil): a pre-split directory
// gets none, nothing was detected. The VMAF results are the ones of the score the thresholds gated,
// named in sptenc_vmaf_score, and the harmonic mean of the other one, when the model feeds on
// CAMBI: a model without it has a single score, which names neither. The CAMBI tags are written
// when the CAMBI gate is on. A model without CAMBI and the gate off gets the tags of sptenc v0.1.0
// plus the scene tags, and exactly those of v0.1.0 on a pre-split directory (see MANUAL.md,
// Metadata tags).
//
// A detector with a helper adds the helper's version and the SHA-256 of its model to the scene
// tags (see SceneCuts). scdet does not: its scene tags are the detector, the threshold and the
// minimum segment length.
func GenerateTags(vc core.VMAFChecker, cambi core.CAMBIChecker, encoder ffmpeg.Encoder, statsQP core.QPSearchResults,
	vmaf ffmpeg.VMAFSummary, model ffmpeg.VMAFModel, segmentsCount int, cuts *SceneCuts) (flags ffmpeg.FFMEGTags) {
	flags = make(ffmpeg.FFMEGTags, 0, 82)
	// Global
	module, version := signature()
	flags = append(flags,
		"-metadata:s:v:0", fmt.Sprintf("%s=%s", sptencURLTagKey, module),
		"-metadata:s:v:0", fmt.Sprintf("%s=%s", sptencVersionTagKey, version),
		"-metadata:s:v:0", "vendor_id=", // prevent ffmpeg from inserting VENDOR_ID : [0][0][0][0]
	)
	// Encoding
	flags = append(flags, "-metadata:s:v:0", fmt.Sprintf("%s=%d", sptencSegmentsCountTagKey, segmentsCount))
	if cuts != nil {
		flags = append(flags,
			"-metadata:s:v:0", fmt.Sprintf("%s=%s", sptencSceneDetectorTagKey, cuts.Detector),
			"-metadata:s:v:0", fmt.Sprintf("%s=%s", sptencSceneThresholdTagKey, cuts.Detector.FormatThreshold(cuts.Threshold)),
			// as --min-segment-length takes it: 5s
			"-metadata:s:v:0", fmt.Sprintf("%s=%s", sptencSceneMinSegmentLengthTagKey, cuts.MinSegmentLength),
		)
		if cuts.Detector.HasHelper() {
			flags = append(flags,
				"-metadata:s:v:0", fmt.Sprintf("%s=%s", sptencSceneDetectorVersionTagKey, cuts.DetectorVersion),
				"-metadata:s:v:0", fmt.Sprintf("%s=%s", sptencSceneDetectorModelSHA256TagKey, cuts.DetectorModelSHA256),
			)
		}
	}
	flags = append(flags, "-metadata:s:v:0", fmt.Sprintf("%s=%s", sptencEncoderTagKey, encoder))
	// Preset: must list every encoder the pipeline runs with a preset (see pipeline.EncoderAdapter),
	// with the very value it uses. VAAPI, D3D12VA and VideoToolbox encoders have no preset.
	switch encoder {
	case ffmpeg.HEVCEncoderLibx265:
		flags = append(flags, "-metadata:s:v:0", fmt.Sprintf("%s=%s", sptencEncoderPresetTagKey, ffmpeg.Libx265PresetSlow))
	case ffmpeg.HEVCEncoderNVEnc, ffmpeg.AV1EncoderNVEnc:
		flags = append(flags, "-metadata:s:v:0", fmt.Sprintf("%s=%s", sptencEncoderPresetTagKey, ffmpeg.NVEncPresetP7))
	case ffmpeg.AV1EncoderSVTAV1:
		// SVT-AV1 presets are numbers, from 1 (slowest, best compression) to 13 (fastest)
		flags = append(flags, "-metadata:s:v:0", fmt.Sprintf("%s=%d", sptencEncoderPresetTagKey, ffmpeg.AV1SVTAV1PresetDefault))
	}
	// QP Stats
	minQP, maxQP := statsQP.GetMinMaxQPs()
	flags = append(flags,
		"-metadata:s:v:0", fmt.Sprintf("%s=%d", sptencStatsMinQP, minQP),
		"-metadata:s:v:0", fmt.Sprintf("%s=%d", sptencStatsMaxQP, maxQP),
		"-metadata:s:v:0", fmt.Sprintf("%s=%s", sptencStatsWeightedQP, strconv.FormatFloat(statsQP.GlobalWeightedQP, 'f', -1, 64)),
	)
	// VMAF
	flags = append(flags, "-metadata:s:v:0", fmt.Sprintf("%s=%s", sptencVMAFModelTagKey, model))
	if vmaf.Score != "" {
		flags = append(flags, "-metadata:s:v:0", fmt.Sprintf("%s=%s", sptencVMAFScoreTagKey, vmaf.Score))
	}
	//// VMAF conf
	thresholdTags := map[string]string{
		"min":    sptencVMAFConfMinTagKey,
		"p1":     sptencVMAFConfP1TagKey,
		"p5":     sptencVMAFConfP5TagKey,
		"p10":    sptencVMAFConfP10TagKey,
		"p25":    sptencVMAFConfP25TagKey,
		"median": sptencVMAFConfMedianTagKey,
		"hmean":  sptencVMAFConfHMeanTagKey,
		"mean":   sptencVMAFConfMeanTagKey,
	}
	for name, value := range vc.Thresholds() {
		if tagKey, ok := thresholdTags[name]; ok {
			flags = append(flags, "-metadata:s:v:0", fmt.Sprintf("%s=%s", tagKey, strconv.FormatFloat(value, 'f', -1, 64)))
		}
	}
	//// Best effort
	if statsQP.NbBestEfforts > 0 {
		flags = append(flags, "-metadata:s:v:0", fmt.Sprintf("%s=%d", sptencBestEffortTagKey, statsQP.NbBestEfforts))
	}
	//// VMAF results
	flags = append(flags,
		"-metadata:s:v:0", fmt.Sprintf("%s=%s", sptencVMAFResultMinTagKey, strconv.FormatFloat(vmaf.Stats.Minimum, 'f', -1, 64)),
		"-metadata:s:v:0", fmt.Sprintf("%s=%s", sptencVMAFResultP1TagKey, strconv.FormatFloat(vmaf.Stats.Percentile1, 'f', -1, 64)),
		"-metadata:s:v:0", fmt.Sprintf("%s=%s", sptencVMAFResultP5TagKey, strconv.FormatFloat(vmaf.Stats.Percentile5, 'f', -1, 64)),
		"-metadata:s:v:0", fmt.Sprintf("%s=%s", sptencVMAFResultP10TagKey, strconv.FormatFloat(vmaf.Stats.Percentile10, 'f', -1, 64)),
		"-metadata:s:v:0", fmt.Sprintf("%s=%s", sptencVMAFResultP25TagKey, strconv.FormatFloat(vmaf.Stats.Percentile25, 'f', -1, 64)),
		"-metadata:s:v:0", fmt.Sprintf("%s=%s", sptencVMAFResultMedianTagKey, strconv.FormatFloat(vmaf.Stats.Median, 'f', -1, 64)),
		"-metadata:s:v:0", fmt.Sprintf("%s=%s", sptencVMAFResultHMeanTagKey, strconv.FormatFloat(vmaf.Stats.HarmonicMean, 'f', -1, 64)),
		"-metadata:s:v:0", fmt.Sprintf("%s=%s", sptencVMAFResultMeanTagKey, strconv.FormatFloat(vmaf.Stats.Mean, 'f', -1, 64)),
		"-metadata:s:v:0", fmt.Sprintf("%s=%s", sptencVMAFResultMaxTagKey, strconv.FormatFloat(vmaf.Stats.Maximum, 'f', -1, 64)),
	)
	if vmaf.OtherScore != "" {
		flags = append(flags, "-metadata:s:v:0", fmt.Sprintf("%s=%s", fmt.Sprintf(sptencVMAFOtherResultHMeanTagKeyFormat, vmaf.OtherScore),
			strconv.FormatFloat(vmaf.OtherHarmonicMean, 'f', -1, 64)))
	}
	// CAMBI
	if !cambi.Enabled() {
		return
	}
	//// CAMBI conf
	cambiThresholds := cambi.Thresholds()
	if mean, ok := cambiThresholds["mean"]; ok {
		flags = append(flags, "-metadata:s:v:0", fmt.Sprintf("%s=%s", sptencCAMBIConfMeanTagKey, strconv.FormatFloat(mean, 'f', -1, 64)))
	}
	if max, ok := cambiThresholds["max"]; ok {
		flags = append(flags, "-metadata:s:v:0", fmt.Sprintf("%s=%s", sptencCAMBIConfMaxTagKey, strconv.FormatFloat(max, 'f', -1, 64)))
	}
	//// CAMBI results: the final pass measures the banding with the gate on (see vmaf.HasBanding)
	if vmaf.HasBanding {
		flags = append(flags,
			"-metadata:s:v:0", fmt.Sprintf("%s=%s", sptencCAMBIResultAddedMeanTagKey, strconv.FormatFloat(vmaf.Banding.AddedMean, 'f', -1, 64)),
			"-metadata:s:v:0", fmt.Sprintf("%s=%s", sptencCAMBIResultAddedMaxTagKey, strconv.FormatFloat(vmaf.Banding.AddedMax, 'f', -1, 64)),
			"-metadata:s:v:0", fmt.Sprintf("%s=%s", sptencCAMBIResultSourceMeanTagKey, strconv.FormatFloat(vmaf.Banding.SourceMean, 'f', -1, 64)),
			"-metadata:s:v:0", fmt.Sprintf("%s=%s", sptencCAMBIResultEncodeMeanTagKey, strconv.FormatFloat(vmaf.Banding.EncodeMean, 'f', -1, 64)),
		)
	}
	if lowered := statsQP.NbCAMBILowered(); lowered > 0 {
		flags = append(flags, "-metadata:s:v:0", fmt.Sprintf("%s=%d", sptencCAMBILoweredTagKey, lowered))
	}
	if statsQP.NbCAMBIBestEfforts > 0 {
		flags = append(flags, "-metadata:s:v:0", fmt.Sprintf("%s=%d", sptencCAMBIBestEffortTagKey, statsQP.NbCAMBIBestEfforts))
	}
	return
}

func signature() (module, version string) {
	infos, ok := debug.ReadBuildInfo()
	if !ok {
		return "<unknown>", "<unknown>"
	}
	return infos.Main.Path, infos.Main.Version
}
