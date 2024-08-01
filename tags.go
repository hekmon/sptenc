package main

import (
	"fmt"
	"strconv"

	"github.com/hekmon/ffmpegutils"
)

const (
	titleTagKey               = "title"
	titleTagValue             = "SptEncoded"
	sptencURLTagKey           = "sptenc_url"
	sptencURLTagValue         = "https://github.com/hekmon/sptenc"
	sptencEncoderTagKey       = "sptenc_encoder"
	sptencEncoderPresetTagKey = "sptenc_encoder_preset"
	// Encoding QP stats
	sptencStatsMinQP      = "sptenc_stats_min_qp"
	sptencStatsWaxQP      = "sptenc_stats_max_qp"
	sptencStatsWeightedQP = "sptenc_stats_global_weighted_qp"
	// VMAF Infos
	sptencVMAFModelTagKey = "sptenc_vmaf_model"
	// VMAF Conf
	sptencVMAFConfMinTagKey    = "sptenc_vmaf_conf_min"
	sptencVMAFConfP1TagKey     = "sptenc_vmaf_conf_p1"
	sptencVMAFConfP5TagKey     = "sptenc_vmaf_conf_p5"
	sptencVMAFConfP10TagKey    = "sptenc_vmaf_conf_p10"
	sptencVMAFConfP25TagKey    = "sptenc_vmaf_conf_p25"
	sptencVMAFConfMedianTagKey = "sptenc_vmaf_conf_median"
	sptencVMAFConfMeanTagKey   = "sptenc_vmaf_conf_mean"
	sptencVMAFConfHMeanTagKey  = "sptenc_vmaf_conf_hmean"
	// VMAF Results (min, p1, p5, p10, p25, median, HarmonicMean, Mean, Max)
	sptencVMAFResultMinTagKey    = "sptenc_vmaf_result_min"
	sptencVMAFResultP1TagKey     = "sptenc_vmaf_result_p1"
	sptencVMAFResultP5TagKey     = "sptenc_vmaf_result_p5"
	sptencVMAFResultP10TagKey    = "sptenc_vmaf_result_p10"
	sptencVMAFResultP25TagKey    = "sptenc_vmaf_result_p25"
	sptencVMAFResultMedianTagKey = "sptenc_vmaf_result_median"
	sptencVMAFResultHMeanTagKey  = "sptenc_vmaf_result_hmean"
	sptencVMAFResultMeanTagKey   = "sptenc_vmaf_result_mean"
	sptencVMAFResultMaxTagKey    = "sptenc_vmaf_result_max"
)

func generateTags(format ffmpegutils.FFProbeFormat, statsQP QPStats, vmaf *ffmpegutils.VMAFStats, ultraHD bool) (flags []string) {
	flags = make([]string, 0, 42)
	// Global
	flags = append(flags,
		"-metadata", fmt.Sprintf("%s=%s", titleTagKey, computeNewTitle(format.Tags)),
		"-metadata:s:v:0", fmt.Sprintf("%s=%s", sptencURLTagKey, sptencURLTagValue),
	)
	// Encoding
	if *nvenc {
		flags = append(flags,
			"-metadata:s:v:0", fmt.Sprintf("%s=hevc_nvenc", sptencEncoderTagKey),
			"-metadata:s:v:0", fmt.Sprintf("%s=%s", sptencEncoderPresetTagKey, ffmpegutils.NVENCPresetP5),
		)
	} else {
		flags = append(flags,
			"-metadata:s:v:0", fmt.Sprintf("%s=libx265", sptencEncoderTagKey),
			"-metadata:s:v:0", fmt.Sprintf("%s=%s", sptencEncoderPresetTagKey, ffmpegutils.Libx265PresetSlow),
		)
	}
	// Stats
	flags = append(flags,
		"-metadata:s:v:0", fmt.Sprintf("%s=%d", sptencStatsMinQP, statsQP.Minimum),
		"-metadata:s:v:0", fmt.Sprintf("%s=%d", sptencStatsWaxQP, statsQP.Maximum),
		"-metadata:s:v:0", fmt.Sprintf("%s=%s", sptencStatsWeightedQP, strconv.FormatFloat(statsQP.GlobalWeighted, 'f', -1, 64)),
	)
	// VMAF
	flags = append(flags, "-metadata:s:v:0", fmt.Sprintf("%s=%s", sptencVMAFModelTagKey, ffmpegutils.VMAFModel(ultraHD, *vmafNEG)))
	// VMAF conf
	if *vmafLimitMin != VMAFOffValue {
		flags = append(flags, "-metadata:s:v:0", fmt.Sprintf("%s=%s", sptencVMAFConfMinTagKey, strconv.FormatFloat(*vmafLimitMin, 'f', -1, 64)))
	}
	if *vmafLimitP1 != VMAFOffValue {
		flags = append(flags, "-metadata:s:v:0", fmt.Sprintf("%s=%s", sptencVMAFConfP1TagKey, strconv.FormatFloat(*vmafLimitP1, 'f', -1, 64)))
	}
	if *vmafLimitP5 != VMAFOffValue {
		flags = append(flags, "-metadata:s:v:0", fmt.Sprintf("%s=%s", sptencVMAFConfP5TagKey, strconv.FormatFloat(*vmafLimitP5, 'f', -1, 64)))
	}
	if *vmafLimitP10 != VMAFOffValue {
		flags = append(flags, "-metadata:s:v:0", fmt.Sprintf("%s=%s", sptencVMAFConfP10TagKey, strconv.FormatFloat(*vmafLimitP10, 'f', -1, 64)))
	}
	if *vmafLimitP25 != VMAFOffValue {
		flags = append(flags, "-metadata:s:v:0", fmt.Sprintf("%s=%s", sptencVMAFConfP25TagKey, strconv.FormatFloat(*vmafLimitP25, 'f', -1, 64)))
	}
	if *vmafLimitMedian != VMAFOffValue {
		flags = append(flags, "-metadata:s:v:0", fmt.Sprintf("%s=%s", sptencVMAFConfMedianTagKey, strconv.FormatFloat(*vmafLimitMedian, 'f', -1, 64)))
	}
	if *vmafLimitHMean != VMAFOffValue {
		flags = append(flags, "-metadata:s:v:0", fmt.Sprintf("%s=%s", sptencVMAFConfHMeanTagKey, strconv.FormatFloat(*vmafLimitHMean, 'f', -1, 64)))
	}
	if *vmafLimitMean != VMAFOffValue {
		flags = append(flags, "-metadata:s:v:0", fmt.Sprintf("%s=%s", sptencVMAFConfMeanTagKey, strconv.FormatFloat(*vmafLimitMean, 'f', -1, 64)))
	}
	// VMAF results
	if vmaf != nil {
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
	}
	return
}

func computeNewTitle(tags map[string]string) (newTitle string) {
	if tags != nil {
		if oldTitle, ok := tags[titleTagKey]; ok {
			return oldTitle + " [" + titleTagValue + "]"
		}
	}
	return titleTagValue
}
