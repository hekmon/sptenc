package metadata

import (
	"fmt"
	"runtime/debug"
	"strconv"

	"github.com/hekmon/sptenc/core"
	"github.com/hekmon/sptenc/ffmpeg"
)

const (
	titleTagKey               = "title"
	TitleTagValue             = "SptEncoded"
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
	sptencBestEffortTagKey       = "sptenc_best_effort_segments"
)

func GenerateTags(format ffmpeg.FFProbeFormat, vc core.VMAFChecker, encoder ffmpeg.Encoder, statsQP core.QPSearchResults, vmaf ffmpeg.VMAFStats, vmafNEG, ultraHD bool, segmentsCount int) (flags ffmpeg.FFMEGTags) {
	flags = make(ffmpeg.FFMEGTags, 0, 57)
	// Global
	module, version := signature()
	flags = append(flags,
		"-metadata", fmt.Sprintf("%s=%s", titleTagKey, computeNewTitle(format.Tags)),
		"-metadata:s:v:0", fmt.Sprintf("%s=%s", sptencURLTagKey, module),
		"-metadata:s:v:0", fmt.Sprintf("%s=%s", sptencVersionTagKey, version),
		"-metadata:s:v:0", "vendor_id=", // prevent ffmpeg from inserting VENDOR_ID : [0][0][0][0]
	)
	// Encoding
	flags = append(flags, "-metadata:s:v:0", fmt.Sprintf("%s=%d", sptencSegmentsCountTagKey, segmentsCount))
	flags = append(flags, "-metadata:s:v:0", fmt.Sprintf("%s=%s", sptencEncoderTagKey, encoder))
	switch encoder {
	case ffmpeg.HEVCEncoderLibx265:
		flags = append(flags, "-metadata:s:v:0", fmt.Sprintf("%s=%s", sptencEncoderPresetTagKey, ffmpeg.Libx265PresetSlow))
	case ffmpeg.HEVCEncoderNVEnc, ffmpeg.AV1EncoderNVEnc:
		flags = append(flags, "-metadata:s:v:0", fmt.Sprintf("%s=%s", sptencEncoderPresetTagKey, ffmpeg.NVEncPresetP7))
	}
	// QP Stats
	minQP, maxQP := statsQP.GetMinMaxQPs()
	flags = append(flags,
		"-metadata:s:v:0", fmt.Sprintf("%s=%d", sptencStatsMinQP, minQP),
		"-metadata:s:v:0", fmt.Sprintf("%s=%d", sptencStatsMaxQP, maxQP),
		"-metadata:s:v:0", fmt.Sprintf("%s=%s", sptencStatsWeightedQP, strconv.FormatFloat(statsQP.GlobalWeightedQP, 'f', -1, 64)),
	)
	// VMAF
	flags = append(flags, "-metadata:s:v:0", fmt.Sprintf("%s=%s", sptencVMAFModelTagKey, ffmpeg.VMAFModel(ultraHD, vmafNEG)))
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
		"-metadata:s:v:0", fmt.Sprintf("%s=%s", sptencVMAFResultMinTagKey, strconv.FormatFloat(vmaf.Minimum, 'f', -1, 64)),
		"-metadata:s:v:0", fmt.Sprintf("%s=%s", sptencVMAFResultP1TagKey, strconv.FormatFloat(vmaf.Percentile1, 'f', -1, 64)),
		"-metadata:s:v:0", fmt.Sprintf("%s=%s", sptencVMAFResultP5TagKey, strconv.FormatFloat(vmaf.Percentile5, 'f', -1, 64)),
		"-metadata:s:v:0", fmt.Sprintf("%s=%s", sptencVMAFResultP10TagKey, strconv.FormatFloat(vmaf.Percentile10, 'f', -1, 64)),
		"-metadata:s:v:0", fmt.Sprintf("%s=%s", sptencVMAFResultP25TagKey, strconv.FormatFloat(vmaf.Percentile25, 'f', -1, 64)),
		"-metadata:s:v:0", fmt.Sprintf("%s=%s", sptencVMAFResultMedianTagKey, strconv.FormatFloat(vmaf.Median, 'f', -1, 64)),
		"-metadata:s:v:0", fmt.Sprintf("%s=%s", sptencVMAFResultHMeanTagKey, strconv.FormatFloat(vmaf.HarmonicMean, 'f', -1, 64)),
		"-metadata:s:v:0", fmt.Sprintf("%s=%s", sptencVMAFResultMeanTagKey, strconv.FormatFloat(vmaf.Mean, 'f', -1, 64)),
		"-metadata:s:v:0", fmt.Sprintf("%s=%s", sptencVMAFResultMaxTagKey, strconv.FormatFloat(vmaf.Maximum, 'f', -1, 64)),
	)
	return
}

func computeNewTitle(tags map[string]string) (newTitle string) {
	if tags != nil {
		if oldTitle, ok := tags[titleTagKey]; ok {
			return oldTitle + " [" + TitleTagValue + "]"
		}
	}
	return TitleTagValue
}

func signature() (module, version string) {
	infos, ok := debug.ReadBuildInfo()
	if !ok {
		return "<unknown>", "<unknown>"
	}
	return infos.Main.Path, infos.Main.Version
}
