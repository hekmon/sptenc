package main

import (
	"fmt"
	"os"

	"github.com/hekmon/sptenc/core"
	"github.com/hekmon/sptenc/ffmpeg"

	"github.com/urfave/cli/v3"
)

// Hardware decode flag names.
const (
	nvdecFlagName           = "nvdec"
	vaapiDecFlagName        = "vaapidec"
	d3d12DecFlagName        = "d3d12vadec"
	videoToolboxDecFlagName = "videotoolboxdec"
)

// hwDecodeFlags returns the standard hardware-accelerated decoding flags.
// When forVMAF is true, the NVIDIA GPU index flag also references --vmafcuda
// since in the vmaf command that same index drives both NVDEC and CUDA VMAF.
func hwDecodeFlags(forVMAF bool) []cli.Flag {
	nvdecUsage := "Use NVDEC hardware-accelerated decoding (NVIDIA GPU required)"
	nvidiaGPUUsage := "GPU to use with --" + nvdecFlagName
	if forVMAF {
		nvdecUsage = "Use NVDEC hardware-accelerated decoding for compatible codecs (NVIDIA GPU required)"
		nvidiaGPUUsage = "GPU to use with --" + nvdecFlagName + " or --" + vmafCUDAFlagName
	}
	return []cli.Flag{
		&cli.BoolFlag{
			Name:     nvdecFlagName,
			Usage:    nvdecUsage,
			Value:    false,
			OnlyOnce: true,
			Category: "Hardware accelerated decoding",
		},
		&cli.BoolFlag{
			Name:     vaapiDecFlagName,
			Usage:    "Use VA-API hardware-accelerated decoding (Intel/AMD GPU required)",
			Value:    false,
			OnlyOnce: true,
			Category: "Hardware accelerated decoding",
		},
		&cli.BoolFlag{
			Name:     d3d12DecFlagName,
			Usage:    "Use D3D12VA hardware-accelerated decoding (Windows, GPU required)",
			Value:    false,
			OnlyOnce: true,
			Category: "Hardware accelerated decoding",
		},
		&cli.BoolFlag{
			Name:     videoToolboxDecFlagName,
			Usage:    "Use VideoToolbox hardware-accelerated decoding (macOS, Apple Silicon)",
			Value:    false,
			OnlyOnce: true,
			Category: "Hardware accelerated decoding",
		},
		&cli.IntFlag{
			Name:     nvidiaGPUIndexFlagName,
			Usage:    nvidiaGPUUsage,
			Value:    ffmpeg.CUDADefaultDevice,
			OnlyOnce: true,
			Category: "Hardware accelerated decoding",
		},
		&cli.StringFlag{
			Name:     vaapiRendererPathFlagName,
			Usage:    "Direct Rendering Manager render node to use with --" + vaapiDecFlagName,
			Value:    ffmpeg.VAAPIDefaultDevice,
			OnlyOnce: true,
			Category: "Hardware accelerated decoding",
		},
		&cli.IntFlag{
			Name:     d3d12vaGPUIndexFlagName,
			Usage:    "GPU to use with --" + d3d12DecFlagName,
			Value:    ffmpeg.D3D12VADefaultDevice,
			OnlyOnce: true,
			Category: "Hardware accelerated decoding",
		},
	}
}

// Threshold search flag names and defaults.
const (
	minThresholdFlagName  = "minthreshold"
	maxThresholdFlagName  = "maxthreshold"
	maxCandidatesFlagName = "maxcandidates"
	minDropFlagName       = "mindrop"

	minThresholdDefault  = 10.0
	maxThresholdDefault  = 40.0
	maxCandidatesDefault = 30
	minDropDefault       = 3
)

// thresholdSearchFlags returns the standard threshold search tuning flags.
func thresholdSearchFlags() []cli.Flag {
	return []cli.Flag{
		&cli.Float64Flag{
			Name:    minThresholdFlagName,
			Aliases: []string{"T"},
			Usage: fmt.Sprintf("Scene detection threshold (%d-%d)",
				ffmpeg.SceneThresholdMin, ffmpeg.SceneThresholdMax,
			),
			Value:     minThresholdDefault,
			OnlyOnce:  true,
			Category:  "Threshold Search",
			Validator: validateSceneThreshold,
		},
		&cli.Float64Flag{
			Name: maxThresholdFlagName,
			Usage: fmt.Sprintf("Maximum scene detection threshold (%d-%d)",
				ffmpeg.SceneThresholdMin, ffmpeg.SceneThresholdMax,
			),
			Value:     maxThresholdDefault,
			OnlyOnce:  true,
			Category:  "Threshold Search",
			Validator: validateSceneThreshold,
		},
		&cli.IntFlag{
			Name:     maxCandidatesFlagName,
			Aliases:  []string{"m"},
			Usage:    "Maximum number of candidate thresholds to test",
			Value:    maxCandidatesDefault,
			OnlyOnce: true,
			Validator: func(v int) error {
				if v < 1 {
					return fmt.Errorf("%s must be at least 1", maxCandidatesFlagName)
				}
				return nil
			},
			Category: "Threshold Search",
		},
		&cli.IntFlag{
			Name:     minDropFlagName,
			Aliases:  []string{"d"},
			Usage:    "Minimum scene drop between two candidate thresholds (auto-tuning will not go below this)",
			Value:    minDropDefault,
			OnlyOnce: true,
			Validator: func(v int) error {
				if v < 1 {
					return fmt.Errorf("%s must be 1 at minimum", minDropFlagName)
				}
				return nil
			},
			Category: "Threshold Search",
		},
	}
}

const (
	nvidiaGPUIndexFlagName    = "nvidiagpuindex"
	vaapiRendererPathFlagName = "vaapirendererpath"
	d3d12vaGPUIndexFlagName   = "d3d12vagpuindex"
)

// newGPUSelectionFlags returns the GPU encoder flags.
func newGPUSelectionFlags() []cli.Flag {
	return []cli.Flag{
		&cli.IntFlag{
			Name:     nvidiaGPUIndexFlagName,
			Usage:    fmt.Sprintf("GPU to use when --%s is an NVIDIA NVENC encoder", encoderFlagName),
			Value:    ffmpeg.CUDADefaultDevice,
			OnlyOnce: true,
			Category: "GPU Selection",
		},
		&cli.StringFlag{
			Name:     vaapiRendererPathFlagName,
			Usage:    fmt.Sprintf("Direct Rendering Manager render node to use when --%s is a VA-API encoder", encoderFlagName),
			Value:    ffmpeg.VAAPIDefaultDevice,
			OnlyOnce: true,
			Category: "GPU Selection",
		},
		&cli.IntFlag{
			Name:     d3d12vaGPUIndexFlagName,
			Usage:    fmt.Sprintf("GPU to use when --%s is a D3D12VA encoder", encoderFlagName),
			Value:    ffmpeg.D3D12VADefaultDevice,
			OnlyOnce: true,
			Category: "GPU Selection",
		},
	}
}

const (
	outputDirFlagName     = "outputdir"
	statsCacheDirFlagName = "statscachedir"
	tmpDirFlagName        = "tmpdir"
)

// newDirectoryFlags returns the output, stats cache and temporary directory flags.
func newDirectoryFlags(segmented bool) []cli.Flag {
	var outputDefault string
	if segmented {
		outputDefault = " (defaults to input file directory, or original file directory for segment inputs)"
	} else {
		outputDefault = " (defaults to input file directory)"
	}
	return []cli.Flag{
		&cli.StringFlag{
			Name:     outputDirFlagName,
			Aliases:  []string{"o"},
			Usage:    "Output directory" + outputDefault,
			Value:    "",
			OnlyOnce: true,
			Category: "Directories",
		},
		&cli.StringFlag{
			Name:     statsCacheDirFlagName,
			Aliases:  []string{"s"},
			Usage:    "Directory for QP history cache",
			Value:    getCacheDir(),
			OnlyOnce: true,
			Category: "Directories",
		},
		&cli.StringFlag{
			Name:             tmpDirFlagName,
			Aliases:          []string{"t"},
			Usage:            "Directory for temporary working files",
			Value:            os.TempDir(),
			OnlyOnce:         true,
			Validator:        validateTmpDir,
			ValidateDefaults: true,
			Category:         "Directories",
		},
	}
}

const (
	vmafCUDAFlagName   = "vmafcuda"
	vmafNegFlagName    = "vmafneg"
	vmafMinFlagName    = "vmafmin"
	vmafP1FlagName     = "vmafp1"
	vmafP5FlagName     = "vmafp5"
	vmafP10FlagName    = "vmafp10"
	vmafP25FlagName    = "vmafp25"
	vmafMedianFlagName = "vmafmedian"
	vmafHMeanFlagName  = "vmafhmean"
	vmafMeanFlagName   = "vmafmean"
)

// newVMAFFlags returns the VMAF quality metric flags.
func newVMAFFlags() []cli.Flag {
	return []cli.Flag{
		&cli.BoolFlag{
			Name:     vmafCUDAFlagName,
			Usage:    "Use CUDA acceleration for VMAF computation",
			Value:    false,
			OnlyOnce: true,
			Category: "VMAF",
		},
		&cli.BoolFlag{
			Name:     vmafNegFlagName,
			Usage:    "Use VMAF NEG models",
			Value:    false,
			OnlyOnce: true,
			Category: "VMAF",
		},
		&cli.Float64Flag{
			Name:      vmafMinFlagName,
			Usage:     "Minimum acceptable VMAF score for the worst frame.",
			Value:     core.VMAFOffValue,
			OnlyOnce:  true,
			Category:  "VMAF",
			Validator: vmafValueValidator,
		},
		&cli.Float64Flag{
			Name:      vmafP1FlagName,
			Usage:     "Minimum acceptable VMAF score for the 1st percentile.",
			Value:     core.VMAFOffValue,
			OnlyOnce:  true,
			Category:  "VMAF",
			Validator: vmafValueValidator,
		},
		&cli.Float64Flag{
			Name:      vmafP5FlagName,
			Usage:     "Minimum acceptable VMAF score for the 5th percentile.",
			Value:     core.VMAFOffValue,
			OnlyOnce:  true,
			Category:  "VMAF",
			Validator: vmafValueValidator,
		},
		&cli.Float64Flag{
			Name:      vmafP10FlagName,
			Usage:     "Minimum acceptable VMAF score for the 10th percentile.",
			Value:     core.VMAFOffValue,
			OnlyOnce:  true,
			Category:  "VMAF",
			Validator: vmafValueValidator,
		},
		&cli.Float64Flag{
			Name:      vmafP25FlagName,
			Usage:     "Minimum acceptable VMAF score for the 25th percentile.",
			Value:     core.VMAFOffValue,
			OnlyOnce:  true,
			Category:  "VMAF",
			Validator: vmafValueValidator,
		},
		&cli.Float64Flag{
			Name:      vmafMedianFlagName,
			Usage:     "Minimum acceptable VMAF score for the median (50th percentile).",
			Value:     core.VMAFOffValue,
			OnlyOnce:  true,
			Category:  "VMAF",
			Validator: vmafValueValidator,
		},
		&cli.Float64Flag{
			Name:      vmafMeanFlagName,
			Usage:     "Minimum acceptable VMAF score for mean.",
			Value:     core.VMAFOffValue,
			OnlyOnce:  true,
			Category:  "VMAF",
			Validator: vmafValueValidator,
		},
		&cli.Float64Flag{
			Name:      vmafHMeanFlagName,
			Usage:     "Minimum acceptable VMAF score for harmonic mean.",
			Value:     93,
			OnlyOnce:  true,
			Category:  "VMAF",
			Validator: vmafValueValidator,
		},
	}
}
