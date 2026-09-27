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

	hardwareAccelerationCategoryName = "Hardware Acceleration"
)

// hwAccelScope controls which hardware acceleration flags are emitted.
type hwAccelScope int

const (
	hwAccelScopeDecode hwAccelScope = iota // decode-only commands (master, split, thresholds, vmaf)
	hwAccelScopeEncode                     // encode commands (decode toggles + concurrency)
)

// hardwareAccelFlags returns hardware acceleration flags under a single category. The device
// selectors and the decode toggles are common to every scope: the encoding commands decode
// too, and a CPU encoder does not mean there is no accelerator (see ffmpeg.ResolveHWDecoder).
func hardwareAccelFlags(scope hwAccelScope) (flags []cli.Flag) {
	// the encoding commands decode with the GPU of a hardware encoder by themselves: the decode
	// flags are for a CPU encoder, say so where the user reads them
	var decodeUsageSuffix string
	if scope == hwAccelScopeEncode {
		decodeUsageSuffix = " with a CPU encoder (a hardware encoder decodes on its GPU by itself)"
	}
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
	flags = append(flags,
		&cli.BoolFlag{
			Name:     nvdecFlagName,
			Usage:    "Use NVDEC hardware decoding" + decodeUsageSuffix,
			Value:    false,
			OnlyOnce: true,
			Category: hardwareAccelerationCategoryName,
		},
		&cli.BoolFlag{
			Name:     vaapiDecFlagName,
			Usage:    "Use VA-API hardware decoding" + decodeUsageSuffix,
			Value:    false,
			OnlyOnce: true,
			Category: hardwareAccelerationCategoryName,
		},
		&cli.BoolFlag{
			Name:     d3d12DecFlagName,
			Usage:    "Use D3D12VA hardware decoding" + decodeUsageSuffix,
			Value:    false,
			OnlyOnce: true,
			Category: hardwareAccelerationCategoryName,
		},
		&cli.BoolFlag{
			Name:     videoToolboxDecFlagName,
			Usage:    "Use VideoToolbox hardware decoding" + decodeUsageSuffix,
			Value:    false,
			OnlyOnce: true,
			Category: hardwareAccelerationCategoryName,
		},
	)
	if scope == hwAccelScopeEncode {
		flags = append(flags,
			&cli.IntFlag{
				Name:      concurrentSegmentsFlagName,
				Aliases:   []string{"C"},
				Usage:     "Number of segments to search and encode in parallel",
				Value:     1,
				OnlyOnce:  true,
				Validator: validateConcurrentSegments,
				Category:  hardwareAccelerationCategoryName,
			},
		)
	}
	return
}

// hwDecodeFlags returns the hardware decoder requested with the decode flags, along with the
// devices of the device flags. No decode flag means software decode, more than one is an error.
func hwDecodeFlags(cmd *cli.Command) (dec ffmpeg.HWDecoderConfig, err error) {
	dec = ffmpeg.HWDecoderConfig{
		NVDec:           cmd.Bool(nvdecFlagName),
		NVDevice:        cmd.Int(nvidiaGPUIndexFlagName),
		VAAPIDec:        cmd.Bool(vaapiDecFlagName),
		VAAPIDevice:     cmd.String(vaapiRendererPathFlagName),
		D3D12Dec:        cmd.Bool(d3d12DecFlagName),
		D3D12Device:     cmd.Int(d3d12vaGPUIndexFlagName),
		VideoToolboxDec: cmd.Bool(videoToolboxDecFlagName),
	}
	var nbFlags int
	for _, set := range []bool{dec.NVDec, dec.VAAPIDec, dec.D3D12Dec, dec.VideoToolboxDec} {
		if set {
			nbFlags++
		}
	}
	if nbFlags > 1 {
		err = fmt.Errorf("only one hardware decode flag can be set at a time (--%s, --%s, --%s, --%s)",
			nvdecFlagName, vaapiDecFlagName, d3d12DecFlagName, videoToolboxDecFlagName)
	}
	return
}

// encodeHWDecoder returns the hardware decoder of an encoding run (see ffmpeg.ResolveHWDecoder)
// out of the encoder and the decode flags of the command.
func encodeHWDecoder(cmd *cli.Command, encoder ffmpeg.Encoder) (dec ffmpeg.HWDecoderConfig, err error) {
	requested, err := hwDecodeFlags(cmd)
	if err != nil {
		return
	}
	return ffmpeg.ResolveHWDecoder(encoder, requested)
}

func validateConcurrentSegments(v int) error {
	if v < 1 {
		return fmt.Errorf("must be at least 1")
	}
	return nil
}

// Scene threshold flag names and defaults.
//
// The scdet score is the mean absolute difference between two consecutive frames, as a
// percentage of the pixel range, or its change from the previous frame if smaller (so a
// sustained pan scores low, a cut scores high). ffmpeg documents "good values" in [8, 14]
// with 10 as the filter default, without any justification: no published study measures
// scdet against a ground truth, and none could answer the question asked here anyway, which
// is not "is this a cut" but "does splitting here give a smaller file".
//
// The single-threshold default (encode, split) is ffmpeg's own 10, the middle of that range:
// a blind guess should not sit at an edge of it.
//
// The search floor is the bottom of that range. It used to be 14 because a low threshold
// turned every pan and flash into segments of a few frames, and the floor was doubling as
// the quality guardrail. Since the minimum segment length (see minSegmentLengthDefault)
// took that role, a low threshold only adds cuts that survive as segments of 5 s or more,
// which is exactly what makes the file smaller: the search only walks upward and stops on
// strikes, and with the floor at 14 too many searches were won by their first candidate,
// which means a clipped range, not an optimum. Not lower than 8: below the documented
// range scdet flags motion onsets, every unique score is a raw candidate (each one costing a
// merge pass on the full markers list) and the candidates budget gets spread thinner.
//
// The ceiling has no rationale at all (it came in with the 14, before the segment filter,
// and was 40 before that): the scores of real hard cuts stay far below it, and the search
// stops long before on strikes. Its only real effect is on the candidates budget, which the
// auto-tuned scene drop spreads over the whole range: a lower ceiling samples the low end,
// where the winners are, more finely. To be revisited with a body of real search results.
const (
	minThresholdFlagName  = "min-threshold"
	sceneThresholdDefault = 10.0 // single-threshold commands: encode, split
	minThresholdDefault   = 8.0  // search floor: batchsearch, thresholds
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
	vmafModelFlagName       = "vmaf-model"
	vmafMinFlagName         = "vmaf-min"
	vmafP1FlagName          = "vmaf-p1"
	vmafP5FlagName          = "vmaf-p5"
	vmafP10FlagName         = "vmaf-p10"
	vmafP25FlagName         = "vmaf-p25"
	vmafMedianFlagName      = "vmaf-median"
	vmafHMeanFlagName       = "vmaf-hmean"
	vmafMeanFlagName        = "vmaf-mean"
	vmafProfileCategoryName = "VMAF Profile"
	// The CAMBI gate, on the banding the encoder adds (see core.CAMBIChecker). A mean of 1 is a
	// safety net: no segment of the two real contents of BENCHMARKS.md came near it at the QPs
	// their VMAF search picked (0.10 at most), and it catches the smooth gradients fidelity
	// passes with bands added. The worst frame is for strict limits, off by default.
	cambiMeanFlagName = "cambi-mean"
	cambiMaxFlagName  = "cambi-max"
	cambiMeanDefault  = 1
)

// vmafModelFlag returns the flag forcing the VMAF model. Not set, the model is selected from
// the height of the source (see resolveVMAFModel). The usage names the two models sptenc
// selects only: any other one libvmaf knows is accepted when forced, not advertised (see
// ffmpeg.VMAFModel).
func vmafModelFlag(category string) cli.Flag {
	return &cli.StringFlag{
		Name: vmafModelFlagName,
		Usage: fmt.Sprintf("VMAF model to score with. Selected from the source height when not set: %s below 2160 lines, %s from 2160. Any other model libvmaf knows is accepted (see MANUAL.md, Models)",
			ffmpeg.VMAFModelFHD, ffmpeg.VMAFModelUHD),
		Value:     "",
		OnlyOnce:  true,
		Category:  category,
		Validator: vmafModelValidator,
	}
}

// vmafModelValidator refuses the names that can not be handed to libvmaf. Whether libvmaf
// knows the model is checked before anything starts (see checkLibVMAF).
func vmafModelValidator(v string) error {
	if v != "" && !ffmpeg.VMAFModel(v).Valid() {
		return fmt.Errorf("invalid VMAF model name %q: letters, digits, '.', '_' and '-' only", v)
	}
	return nil
}

// VMAFFlags returns the VMAF quality metric flags.
func VMAFFlags() []cli.Flag {
	return []cli.Flag{
		vmafModelFlag(vmafProfileCategoryName),
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
		&cli.Float64Flag{
			Name: cambiMeanFlagName,
			Usage: fmt.Sprintf("Maximum banding the encoder may add to a segment, on average over its frames (CAMBI, 0 = none). %d to disable (see MANUAL.md, Fidelity and banding)",
				core.CAMBIOffValue),
			Value:     cambiMeanDefault,
			OnlyOnce:  true,
			Category:  vmafProfileCategoryName,
			Validator: cambiValueValidator,
		},
		&cli.Float64Flag{
			Name: cambiMaxFlagName,
			Usage: fmt.Sprintf("Maximum banding the encoder may add to the worst frame of a segment (CAMBI, 0 = none). %d to disable",
				core.CAMBIOffValue),
			Value:     core.CAMBIOffValue,
			OnlyOnce:  true,
			Category:  vmafProfileCategoryName,
			Validator: cambiValueValidator,
		},
	}
}

func vmafValueValidator(v float64) error {
	if v != core.VMAFOffValue && (v < core.VMAFMinValue || v > core.VMAFMaxValue) {
		return fmt.Errorf("must be between %d and %d, or %d to disable", core.VMAFMinValue, core.VMAFMaxValue, core.VMAFOffValue)
	}
	return nil
}

// cambiValueValidator refuses what core.NewCAMBIChecker refuses, before anything starts.
func cambiValueValidator(v float64) error {
	if _, err := core.NewCAMBIChecker(v, core.CAMBIOffValue); err != nil {
		return fmt.Errorf("must be 0 or more, or %d to disable", core.CAMBIOffValue)
	}
	return nil
}
