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
	sptencStatsMinQP          = "sptenc_stats_min_qp"
	sptencStatsWaxQP          = "sptenc_stats_max_qp"
	sptencStatsMeanQP         = "sptenc_stats_GOP_mean_qp"
	sptencStatsWeightedQP     = "sptenc_stats_global_weighted_qp"
	sptencVMAFMeanTagKey      = "sptenc_vmaf_conf_mean"
	sptencVMAFHMeanTagKey     = "sptenc_vmaf_conf_hmean"
	sptencVMAFP1TagKey        = "sptenc_vmaf_conf_p1"
	sptencVMAFMinTagKey       = "sptenc_vmaf_conf_min"
)

func generateTags(format ffmpegutils.FFProbeFormat, statsQP QPStats) (flags []string) {
	flags = make([]string, 0, 10)
	// Global
	flags = append(flags,
		"-metadata", fmt.Sprintf("%s=%s", titleTagKey, computeNewTitle(format.Tags)),
		"-metadata:s:v:0", fmt.Sprintf("%s=%s", sptencURLTagKey, sptencURLTagValue),
	)
	// Encoding
	if *nvenc {
		flags = append(flags,
			"-metadata:s:v:0", fmt.Sprintf("%s=hevc_nvenc", sptencEncoderTagKey),
			"-metadata:s:v:0", fmt.Sprintf("%s=%s", sptencEncoderPresetTagKey, ffmpegutils.NVENCPreset),
		)
	} else {
		flags = append(flags,
			"-metadata:s:v:0", fmt.Sprintf("%s=libx265", sptencEncoderTagKey),
			"-metadata:s:v:0", fmt.Sprintf("%s=%s", sptencEncoderPresetTagKey, ffmpegutils.Libx265Preset),
		)
	}
	// Stats
	flags = append(flags,
		"-metadata:s:v:0", fmt.Sprintf("%s=%d", sptencStatsMinQP, statsQP.Minimum),
		"-metadata:s:v:0", fmt.Sprintf("%s=%d", sptencStatsWaxQP, statsQP.Maximum),
		"-metadata:s:v:0", fmt.Sprintf("%s=%s", sptencStatsMeanQP, strconv.FormatFloat(statsQP.GOPMean, 'f', -1, 64)),
		"-metadata:s:v:0", fmt.Sprintf("%s=%s", sptencStatsWeightedQP, strconv.FormatFloat(statsQP.GlobalWeighted, 'f', -1, 64)),
	)
	// VMAF conf
	if *vmafLimitMin != VMAFOffValue {
		flags = append(flags, "-metadata:s:v:0", fmt.Sprintf("%s=%s", sptencVMAFMinTagKey, strconv.FormatFloat(*vmafLimitMin, 'f', -1, 64)))
	}
	if *vmafLimitP1 != VMAFOffValue {
		flags = append(flags, "-metadata:s:v:0", fmt.Sprintf("%s=%s", sptencVMAFP1TagKey, strconv.FormatFloat(*vmafLimitP1, 'f', -1, 64)))
	}
	if *vmafLimitHMean != VMAFOffValue {
		flags = append(flags, "-metadata:s:v:0", fmt.Sprintf("%s=%s", sptencVMAFHMeanTagKey, strconv.FormatFloat(*vmafLimitHMean, 'f', -1, 64)))
	}
	if *vmafLimitMean != VMAFOffValue {
		flags = append(flags, "-metadata:s:v:0", fmt.Sprintf("%s=%s", sptencVMAFMeanTagKey, strconv.FormatFloat(*vmafLimitMean, 'f', -1, 64)))
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
