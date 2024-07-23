package main

import (
	"fmt"
	"strconv"

	"github.com/hekmon/ffmpegutils"
)

const (
	titleTagKey              = "title"
	titleTagValue            = "ScEncoded"
	scencURLTagKey           = "scenc_url"
	scencURLTagValue         = "https://github.com/hekmon/scenc"
	scencEncoderTagKey       = "scenc_encoder"
	scencEncoderPresetTagKey = "scenc_encoder_preset"
	scencStatsMinQP          = "scenc_stats_min_qp"
	scencStatsWaxQP          = "scenc_stats_max_qp"
	scencStatsMeanQP         = "scenc_stats_scenes_mean_qp"
	scencStatsWeightedQP     = "scenc_stats_global_weighted_qp"
	scencVMAFMeanTagKey      = "scenc_vmaf_conf_mean"
	scencVMAFHMeanTagKey     = "scenc_vmaf_conf_hmean"
	scencVMAFP1TagKey        = "scenc_vmaf_conf_p1"
	scencVMAFMinTagKey       = "scenc_vmaf_conf_min"
)

func generateTags(format ffmpegutils.FFProbeFormat, statsQP QPStats) (flags []string) {
	flags = make([]string, 0, 10)
	// Global
	flags = append(flags,
		"-metadata", fmt.Sprintf("%s=%s", titleTagKey, computeNewTitle(format.Tags)),
		"-metadata:s:v:0", fmt.Sprintf("%s=%s", scencURLTagKey, scencURLTagValue),
	)
	// Encoding
	if *nvc {
		flags = append(flags,
			"-metadata:s:v:0", fmt.Sprintf("%s=hevc_nvenc", scencEncoderTagKey),
			"-metadata:s:v:0", fmt.Sprintf("%s=%s", scencEncoderPresetTagKey, ffmpegutils.NVENCPreset),
		)
	} else {
		flags = append(flags,
			"-metadata:s:v:0", fmt.Sprintf("%s=libx265", scencEncoderTagKey),
			"-metadata:s:v:0", fmt.Sprintf("%s=%s", scencEncoderPresetTagKey, ffmpegutils.Libx265Preset),
		)
	}
	// Stats
	flags = append(flags,
		"-metadata:s:v:0", fmt.Sprintf("%s=%d", scencStatsMinQP, statsQP.Minimum),
		"-metadata:s:v:0", fmt.Sprintf("%s=%d", scencStatsWaxQP, statsQP.Maximum),
		"-metadata:s:v:0", fmt.Sprintf("%s=%s", scencStatsMeanQP, strconv.FormatFloat(statsQP.ScenesMean, 'f', -1, 64)),
		"-metadata:s:v:0", fmt.Sprintf("%s=%s", scencStatsWeightedQP, strconv.FormatFloat(statsQP.GlobalWeighted, 'f', -1, 64)),
	)
	// VMAF conf
	if *vmafLimitMin != VMAFOffValue {
		flags = append(flags, "-metadata:s:v:0", fmt.Sprintf("%s=%s", scencVMAFMinTagKey, strconv.FormatFloat(*vmafLimitMin, 'f', -1, 64)))
	}
	if *vmafLimitP1 != VMAFOffValue {
		flags = append(flags, "-metadata:s:v:0", fmt.Sprintf("%s=%s", scencVMAFP1TagKey, strconv.FormatFloat(*vmafLimitP1, 'f', -1, 64)))
	}
	if *vmafLimitHMean != VMAFOffValue {
		flags = append(flags, "-metadata:s:v:0", fmt.Sprintf("%s=%s", scencVMAFHMeanTagKey, strconv.FormatFloat(*vmafLimitHMean, 'f', -1, 64)))
	}
	if *vmafLimitMean != VMAFOffValue {
		flags = append(flags, "-metadata:s:v:0", fmt.Sprintf("%s=%s", scencVMAFMeanTagKey, strconv.FormatFloat(*vmafLimitMean, 'f', -1, 64)))
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
