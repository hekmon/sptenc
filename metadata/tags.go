package metadata

import (
	"fmt"
	"runtime/debug"
	"strconv"

	"github.com/hekmon/sptenc/core"
	"github.com/hekmon/sptenc/ffmpeg"
)

const (
	sptencURLTagKey           = "sptenc_url"
	sptencVersionTagKey       = "sptenc_version"
	sptencEncoderTagKey       = "sptenc_encoder"
	sptencEncoderPresetTagKey = "sptenc_encoder_preset"
	sptencSegmentsCountTagKey = "sptenc_segments_count"
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

// GenerateTags builds the ffmpeg metadata flags documenting an encode. They are all set on the
// video stream: container level metadata (the title in particular) belongs to the user and is
// passed through from the source untouched.
//
// The VMAF results are the ones of the score the thresholds gated, named in sptenc_vmaf_score, and
// the harmonic mean of the other one, when the model feeds on CAMBI: a model without it has a
// single score, which names neither. The CAMBI tags are written when the CAMBI gate is on. A model
// without CAMBI and the gate off gets the tags of sptenc v0.1.0 (see MANUAL.md, Metadata tags).
func GenerateTags(vc core.VMAFChecker, cambi core.CAMBIChecker, encoder ffmpeg.Encoder, statsQP core.QPSearchResults,
	vmaf ffmpeg.VMAFSummary, model ffmpeg.VMAFModel, segmentsCount int) (flags ffmpeg.FFMEGTags) {
	flags = make(ffmpeg.FFMEGTags, 0, 76)
	// Global
	module, version := signature()
	flags = append(flags,
		"-metadata:s:v:0", fmt.Sprintf("%s=%s", sptencURLTagKey, module),
		"-metadata:s:v:0", fmt.Sprintf("%s=%s", sptencVersionTagKey, version),
		"-metadata:s:v:0", "vendor_id=", // prevent ffmpeg from inserting VENDOR_ID : [0][0][0][0]
	)
	// Encoding
	flags = append(flags, "-metadata:s:v:0", fmt.Sprintf("%s=%d", sptencSegmentsCountTagKey, segmentsCount))
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
