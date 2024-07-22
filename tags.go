package main

import (
	"fmt"
	"strconv"

	"github.com/hekmon/ffmpegutils"
)

const (
	titleTagKey              = "title"
	titleTagValue            = "ScEnc"
	scencURLTagKey           = "scenc_url"
	scencURLTagValue         = "https://github.com/hekmon/scenc"
	scencEncoderTagKey       = "scenc_encoder"
	scencEncoderPresetTagKey = "scenc_encoder_preset"
	scencVMAFMeanTagKey      = "scenc_vmaf_mean"
	scencVMAFHMeanTagKey     = "scenc_vmaf_hmean"
	scencVMAFP1TagKey        = "scenc_vmaf_p1"
	scencVMAFMinTagKey       = "scenc_vmaf_min"
)

func generateTags(format ffmpegutils.FFProbeFormat) (flags []string) {
	switch format.Name {
	case ffmpegutils.FormatQuickTime:
		// write metadata using iTunes-style metadata tags in MOV/MP4 files
		flags = append(flags, "-movflags", "use_metadata_tags")
		fallthrough
	case ffmpegutils.FormatAVI:
		flags = append(flags, "-metadata", fmt.Sprintf("%s=%s", titleTagKey, computeNewTitle(format.Tags)))
	default:
		// ffmpeg does not seem to fail when non injectable tags are provided, let's try to set them in case the format supports them
		fallthrough
	case ffmpegutils.FormatMatroska:
		flags = append(flags,
			"-metadata", fmt.Sprintf("%s=%s", titleTagKey, computeNewTitle(format.Tags)),
			"-metadata:s:v:0", fmt.Sprintf("%s=%s", scencURLTagKey, scencURLTagValue),
		)
		// Encoding
		if *nvc {
			flags = append(flags, "-metadata:s:v:0", fmt.Sprintf("%s=hevc_nvenc", scencEncoderTagKey))
			flags = append(flags, "-metadata:s:v:0", fmt.Sprintf("%s=%s", scencEncoderPresetTagKey, ffmpegutils.NVENCPreset))
		} else {
			flags = append(flags, "-metadata:s:v:0", fmt.Sprintf("%s=libx265", scencEncoderTagKey))
			flags = append(flags, "-metadata:s:v:0", fmt.Sprintf("%s=%s", scencEncoderPresetTagKey, ffmpegutils.Libx265Preset))
		}
		// VMAF
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
