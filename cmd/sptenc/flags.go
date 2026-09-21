package main

import (
	"fmt"
	"os"
	"runtime"
	"time"

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

	nvidiaGPUIndexFlagName    = "nvidia-gpu-index"
	vaapiRendererPathFlagName = "vaapi-renderer-path"
	d3d12vaGPUIndexFlagName   = "d3d12va-gpu-index"

	concurrentSegmentsFlagName = "concurrent-segments"
	vmafCUDAFlagName           = "vmaf-cuda"

	hardwareAccelerationCategoryName = "Hardware Acceleration"
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
			Category: hardwareAccelerationCategoryName,
		},
		&cli.StringFlag{
			Name:     vaapiRendererPathFlagName,
			Usage:    "DRM render node for VA-API hardware acceleration",
			Value:    ffmpeg.VAAPIDefaultDevice,
			OnlyOnce: true,
			Category: hardwareAccelerationCategoryName,
		},
		&cli.IntFlag{
			Name:     d3d12vaGPUIndexFlagName,
			Usage:    "GPU device index for D3D12VA hardware acceleration",
			Value:    ffmpeg.D3D12VADefaultDevice,
			OnlyOnce: true,
			Category: hardwareAccelerationCategoryName,
		},
	}
	if scope == hwAccelScopeDecode || scope == hwAccelScopeVMAF {
		flags = append(flags,
			&cli.BoolFlag{
				Name:     nvdecFlagName,
				Usage:    "Use NVDEC hardware decoding",
				Value:    false,
				OnlyOnce: true,
				Category: hardwareAccelerationCategoryName,
			},
			&cli.BoolFlag{
				Name:     vaapiDecFlagName,
				Usage:    "Use VA-API hardware decoding",
				Value:    false,
				OnlyOnce: true,
				Category: hardwareAccelerationCategoryName,
			},
			&cli.BoolFlag{
				Name:     d3d12DecFlagName,
				Usage:    "Use D3D12VA hardware decoding",
				Value:    false,
				OnlyOnce: true,
				Category: hardwareAccelerationCategoryName,
			},
			&cli.BoolFlag{
				Name:     videoToolboxDecFlagName,
				Usage:    "Use VideoToolbox hardware decoding",
				Value:    false,
				OnlyOnce: true,
				Category: hardwareAccelerationCategoryName,
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
				Category: hardwareAccelerationCategoryName,
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
				Category:  hardwareAccelerationCategoryName,
			},
		)
	}
	return
}

func validateConcurrentSegments(v int) error {
	if v < 1 {
		return fmt.Errorf("must be at least 1")
	}
	return nil
}

// Threshold search flag names and defaults.
const (
	minThresholdFlagName  = "min-threshold"
	minThresholdDefault   = 14.0
	maxThresholdFlagName  = "max-threshold"
	maxThresholdDefault   = 50.0
	maxCandidatesFlagName = "max-candidates"
	maxCandidatesDefault  = 20
	minDropFlagName       = "min-drop"
	minDropDefault        = 3
	thresholdCategoryName = "Threshold Search"
)

// Segment filter flag names and defaults.
const (
	minSegmentLengthFlagName = "min-segment-length"
	// minSegmentLengthDefault is set to 5 seconds because 24 fps content is the
	// limiting factor. p1 needs ≥100 frames to be statistically meaningful:
	//   - 24 fps (film): 4s = 96 frames, which falls short. 5s = 120 frames.
	//   - 25 fps (PAL):  4s = 100 frames, which meets the floor exactly but
	//     leaves no margin for segmenter rounding that can shift boundaries by
	//     ±1–2 frames. 5s = 125 frames.
	//   - 30 fps (NTSC): 4s = 120 frames, already comfortable. 5s = 150 frames.
	// The 5-second default guarantees all common frame rates stay above the p1
	// threshold with enough headroom to absorb segmenter frame-count variance.
	// Segments shorter than this produce VMAF metrics that are mathematically
	// unreliable (p1 variance dominates, p5 is marginal) and encoder-meaningless
	// (I-frame overhead overwhelms B/P-frame gains).
	minSegmentLengthDefault = 5 * time.Second
)

// segmentFilterFlag returns the min-segment-length flag. Commands that support
// scene detection and splitting (encode, split, thresholds, batchsearch) include it.
// If category is non-empty, the flag is grouped under that category in help output.
func segmentFilterFlag(category string) cli.Flag {
	return &cli.DurationFlag{
		Name:     minSegmentLengthFlagName,
		Aliases:  []string{"L"},
		Usage:    "Minimum segment duration. Boundaries creating shorter segments are merged.",
		Value:    minSegmentLengthDefault,
		Category: category,
		OnlyOnce: true,
		Validator: func(v time.Duration) error {
			if v < 0 {
				return fmt.Errorf("%s must be >= 0", minSegmentLengthFlagName)
			}
			return nil
		},
	}
}

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
			Category:  thresholdCategoryName,
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
			Category:  thresholdCategoryName,
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
			Category: thresholdCategoryName,
		},
		&cli.IntFlag{
			Name: minDropFlagName,
			// Not "d": it is the alias of the global --debug flag, which can be given after the
			// command name too. A local "d" would silently shadow it there.
			Aliases:  []string{"D"},
			Usage:    "Minimum scene drop between two candidate thresholds (auto-tuning will not go below this)",
			Value:    minDropDefault,
			OnlyOnce: true,
			Validator: func(v int) error {
				if v < 1 {
					return fmt.Errorf("%s must be 1 at minimum", minDropFlagName)
				}
				return nil
			},
			Category: thresholdCategoryName,
		},
	}
}

func validateSceneThreshold(v float64) error {
	if v < ffmpeg.SceneThresholdMin || v > ffmpeg.SceneThresholdMax {
		return fmt.Errorf("must be between %d and %d", ffmpeg.SceneThresholdMin, ffmpeg.SceneThresholdMax)
	}
	return nil
}

const (
	outputDirFlagName       = "output-dir"
	statsCacheDirFlagName   = "stats-cache-dir"
	tmpDirFlagName          = "tmp-dir"
	directoriesCategoryName = "Directories"
)

// directoryFlags returns the stats cache and temporary directory flags.
func directoryFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{
			Name:     statsCacheDirFlagName,
			Aliases:  []string{"s"},
			Usage:    "Directory for QP history cache",
			Value:    getCacheDir(),
			OnlyOnce: true,
			Category: directoriesCategoryName,
		},
		&cli.StringFlag{
			Name:             tmpDirFlagName,
			Aliases:          []string{"t"},
			Usage:            "Directory for temporary working files",
			Value:            os.TempDir(),
			OnlyOnce:         true,
			Validator:        validateTmpDir,
			ValidateDefaults: true,
			Category:         directoriesCategoryName,
		},
	}
}

func validateTmpDir(path string) error {
	// validate tmpDir path for non-ASCII characters (Windows compatibility issue with libvmaf)
	if runtime.GOOS == "windows" && !isASCII(path) {
		return fmt.Errorf("the temporary directory path contains non-ASCII characters which are not compatible with libvmaf on Windows\n"+
			"Please use a path with only ASCII characters (no accents or special characters).\n"+
			"Current path: %s", path)
	}
	return nil
}

const (
	vmafNegFlagName         = "vmaf-neg"
	vmafMinFlagName         = "vmaf-min"
	vmafP1FlagName          = "vmaf-p1"
	vmafP5FlagName          = "vmaf-p5"
	vmafP10FlagName         = "vmaf-p10"
	vmafP25FlagName         = "vmaf-p25"
	vmafMedianFlagName      = "vmaf-median"
	vmafHMeanFlagName       = "vmaf-hmean"
	vmafMeanFlagName        = "vmaf-mean"
	vmafProfileCategoryName = "VMAF Profile"
)

// VMAFFlags returns the VMAF quality metric flags.
func VMAFFlags() []cli.Flag {
	return []cli.Flag{
		&cli.BoolFlag{
			Name:     vmafNegFlagName,
			Usage:    "Use VMAF NEG models",
			Value:    false,
			OnlyOnce: true,
			Category: vmafProfileCategoryName,
		},
		&cli.Float64Flag{
			Name:      vmafMinFlagName,
			Usage:     "Minimum acceptable VMAF score for the worst frame.",
			Value:     core.VMAFOffValue,
			OnlyOnce:  true,
			Category:  vmafProfileCategoryName,
			Validator: vmafValueValidator,
		},
		&cli.Float64Flag{
			Name:      vmafP1FlagName,
			Usage:     "Minimum acceptable VMAF score for the 1st percentile.",
			Value:     core.VMAFOffValue,
			OnlyOnce:  true,
			Category:  vmafProfileCategoryName,
			Validator: vmafValueValidator,
		},
		&cli.Float64Flag{
			Name:      vmafP5FlagName,
			Usage:     "Minimum acceptable VMAF score for the 5th percentile.",
			Value:     core.VMAFOffValue,
			OnlyOnce:  true,
			Category:  vmafProfileCategoryName,
			Validator: vmafValueValidator,
		},
		&cli.Float64Flag{
			Name:      vmafP10FlagName,
			Usage:     "Minimum acceptable VMAF score for the 10th percentile.",
			Value:     core.VMAFOffValue,
			OnlyOnce:  true,
			Category:  vmafProfileCategoryName,
			Validator: vmafValueValidator,
		},
		&cli.Float64Flag{
			Name:      vmafP25FlagName,
			Usage:     "Minimum acceptable VMAF score for the 25th percentile.",
			Value:     core.VMAFOffValue,
			OnlyOnce:  true,
			Category:  vmafProfileCategoryName,
			Validator: vmafValueValidator,
		},
		&cli.Float64Flag{
			Name:      vmafMedianFlagName,
			Usage:     "Minimum acceptable VMAF score for the median (50th percentile).",
			Value:     core.VMAFOffValue,
			OnlyOnce:  true,
			Category:  vmafProfileCategoryName,
			Validator: vmafValueValidator,
		},
		&cli.Float64Flag{
			Name:      vmafMeanFlagName,
			Usage:     "Minimum acceptable VMAF score for mean.",
			Value:     core.VMAFOffValue,
			OnlyOnce:  true,
			Category:  vmafProfileCategoryName,
			Validator: vmafValueValidator,
		},
		&cli.Float64Flag{
			Name:      vmafHMeanFlagName,
			Usage:     "Minimum acceptable VMAF score for harmonic mean.",
			Value:     93,
			OnlyOnce:  true,
			Category:  vmafProfileCategoryName,
			Validator: vmafValueValidator,
		},
	}
}

func vmafValueValidator(v float64) error {
	if v != core.VMAFOffValue && (v < core.VMAFMinValue || v > core.VMAFMaxValue) {
		return fmt.Errorf("must be between %d and %d, or %d to disable", core.VMAFMinValue, core.VMAFMaxValue, core.VMAFOffValue)
	}
	return nil
}
