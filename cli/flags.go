package main

import (
	"fmt"
	"os"

	"github.com/hekmon/sptenc/core"
	"github.com/hekmon/sptenc/ffmpeg"

	"github.com/urfave/cli/v3"
)

// Hardware acceleration flag names.
const (
	nvdecFlagName           = "nvdec"
	vaapiDecFlagName        = "vaapi-dec"
	d3d12DecFlagName        = "d3d12va-dec"
	videoToolboxDecFlagName = "videotoolbox-dec"
)

const (
	nvidiaGPUIndexFlagName    = "nvidia-gpu-index"
	vaapiRendererPathFlagName = "vaapi-renderer-path"
	d3d12vaGPUIndexFlagName   = "d3d12va-gpu-index"
)

const (
	concurrentSegmentsFlagName = "concurrent-segments"
	vmafCUDAFlagName           = "vmaf-cuda"
)

// hwAccelScope controls which hardware acceleration flags are emitted.
type hwAccelScope int

const (
	hwAccelScopeDecode hwAccelScope = iota // decode-only commands (master, split, thresholds)
	hwAccelScopeVMAF                       // vmaf command (decode toggles + cuda)
	hwAccelScopeEncode                     // encode commands (device selectors + cuda + concurrency)
)

// hardwareAccelFlags returns hardware acceleration flags under a single category.
func hardwareAccelFlags(scope hwAccelScope) (flags []cli.Flag) {
	flags = []cli.Flag{
		&cli.IntFlag{
			Name:     nvidiaGPUIndexFlagName,
			Usage:    "NVIDIA GPU device index for hardware acceleration",
			Value:    ffmpeg.CUDADefaultDevice,
			OnlyOnce: true,
			Category: "Hardware acceleration",
		},
		&cli.StringFlag{
			Name:     vaapiRendererPathFlagName,
			Usage:    "DRM render node for VA-API hardware acceleration",
			Value:    ffmpeg.VAAPIDefaultDevice,
			OnlyOnce: true,
			Category: "Hardware acceleration",
		},
		&cli.IntFlag{
			Name:     d3d12vaGPUIndexFlagName,
			Usage:    "GPU device index for D3D12VA hardware acceleration",
			Value:    ffmpeg.D3D12VADefaultDevice,
			OnlyOnce: true,
			Category: "Hardware acceleration",
		},
	}
	if scope == hwAccelScopeDecode || scope == hwAccelScopeVMAF {
		flags = append(flags,
			&cli.BoolFlag{
				Name:     nvdecFlagName,
				Usage:    "Use NVDEC hardware decoding",
				Value:    false,
				OnlyOnce: true,
				Category: "Hardware acceleration",
			},
			&cli.BoolFlag{
				Name:     vaapiDecFlagName,
				Usage:    "Use VA-API hardware decoding",
				Value:    false,
				OnlyOnce: true,
				Category: "Hardware acceleration",
			},
			&cli.BoolFlag{
				Name:     d3d12DecFlagName,
				Usage:    "Use D3D12VA hardware decoding",
				Value:    false,
				OnlyOnce: true,
				Category: "Hardware acceleration",
			},
			&cli.BoolFlag{
				Name:     videoToolboxDecFlagName,
				Usage:    "Use VideoToolbox hardware decoding",
				Value:    false,
				OnlyOnce: true,
				Category: "Hardware acceleration",
			},
		)
	}
	if scope == hwAccelScopeVMAF || scope == hwAccelScopeEncode {
		flags = append(flags,
			&cli.BoolFlag{
				Name:     vmafCUDAFlagName,
				Usage:    "Use CUDA for VMAF computation",
				Value:    false,
				OnlyOnce: true,
				Category: "Hardware acceleration",
			},
		)
	}
	if scope == hwAccelScopeEncode {
		flags = append(flags,
			&cli.IntFlag{
				Name:      concurrentSegmentsFlagName,
				Aliases:   []string{"C"},
				Usage:     "Number of segments to encode in parallel (GPU encoders only)",
				Value:     1,
				OnlyOnce:  true,
				Validator: validateConcurrentSegments,
				Category:  "Hardware acceleration",
			},
		)
	}
	return
}

// Threshold search flag names and defaults.
const (
	minThresholdFlagName  = "min-threshold"
	minThresholdDefault   = 14.0
	maxThresholdFlagName  = "max-threshold"
	maxThresholdDefault   = 50.0
	maxCandidatesFlagName = "max-candidates"
	maxCandidatesDefault  = 30
	minDropFlagName       = "min-drop"
	minDropDefault        = 3
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
			Name:    maxThresholdFlagName,
			Aliases: []string{"M"},
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
	outputDirFlagName     = "output-dir"
	statsCacheDirFlagName = "stats-cache-dir"
	tmpDirFlagName        = "tmp-dir"
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
	vmafNegFlagName    = "vmaf-neg"
	vmafMinFlagName    = "vmaf-min"
	vmafP1FlagName     = "vmaf-p1"
	vmafP5FlagName     = "vmaf-p5"
	vmafP10FlagName    = "vmaf-p10"
	vmafP25FlagName    = "vmaf-p25"
	vmafMedianFlagName = "vmaf-median"
	vmafHMeanFlagName  = "vmaf-hmean"
	vmafMeanFlagName   = "vmaf-mean"
)

// newVMAFFlags returns the VMAF quality metric flags.
func newVMAFFlags() []cli.Flag {
	return []cli.Flag{
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
