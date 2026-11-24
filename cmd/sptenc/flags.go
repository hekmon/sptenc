package main

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/hekmon/sptenc/core"
	"github.com/hekmon/sptenc/ffmpeg"
	"github.com/hekmon/sptenc/pipeline"

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

// Scene threshold flag names, and the defaults of the search that do not depend on the detector.
//
// The defaults, ranges and scales of the thresholds belong to the detector (see
// pipeline.SceneDetector, where the rationale of scdet's values lives): a static Value in the
// flag definition can not follow --detector, so the threshold flags have none and are resolved
// at run time (see singleSceneThreshold and searchSceneThresholds).
const (
	detectorFlagName      = "detector"
	minThresholdFlagName  = "min-threshold"
	maxThresholdFlagName  = "max-threshold"
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

// rgbMatrixFlagName is the flag forcing the matrix an RGB source is converted to YUV with (see
// sourceYUVMatrix).
const rgbMatrixFlagName = "rgb-matrix"

// rgbMatrixFlag returns the flag forcing the matrix an RGB source is converted to YUV with. The
// commands reading a source include it: master, split, encode, batchsearch, thresholds, vmaf.
// If category is non-empty, the flag is grouped under that category in help output.
func rgbMatrixFlag(category string) cli.Flag {
	matrices := fmt.Sprintf("%s, %s or %s (BT.601)", ffmpeg.YUVMatrixBT709, ffmpeg.YUVMatrixBT2020NC, ffmpeg.YUVMatrixBT601)
	return &cli.StringFlag{
		Name: rgbMatrixFlagName,
		Usage: fmt.Sprintf("Matrix an RGB source is converted to YUV with: %s. Taken from the primaries the source declares when not set: bt709 ones give %s, bt2020 ones %s (see MANUAL.md, RGB sources)",
			matrices, ffmpeg.YUVMatrixBT709, ffmpeg.YUVMatrixBT2020NC),
		OnlyOnce: true,
		Category: category,
		Validator: func(v string) error {
			if !ffmpeg.YUVMatrix(v).Valid() {
				return fmt.Errorf("must be %s", matrices)
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
			Usage: thresholdUsage("Scene detection threshold",
				func(d pipeline.SceneDetector) float64 { min, _ := d.DefaultSearchRange(); return min }),
			// no Value: the default is the detector's, see searchSceneThresholds; the usage states it
			HideDefault: true,
			OnlyOnce:    true,
			Category:    thresholdCategoryName,
		},
		&cli.Float64Flag{
			Name:    maxThresholdFlagName,
			Aliases: []string{"M"},
			Usage: thresholdUsage("Maximum scene detection threshold",
				func(d pipeline.SceneDetector) float64 { _, max := d.DefaultSearchRange(); return max }),
			// no Value: the default is the detector's, see searchSceneThresholds; the usage states it
			HideDefault: true,
			OnlyOnce:    true,
			Category:    thresholdCategoryName,
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

// detectorFlag returns the flag selecting the scene detector. The commands detecting scenes
// include it: encode, split, thresholds, batchsearch. If category is non-empty, the flag is grouped
// under that category in help output. No alias: none of the free letters reads as "detector"
// (-d is --debug, -D --min-drop).
func detectorFlag(category string) cli.Flag {
	return &cli.StringFlag{
		Name:     detectorFlagName,
		Usage:    detectorUsage(),
		Value:    string(pipeline.SceneDetectors[0]),
		OnlyOnce: true,
		Category: category,
		Validator: func(v string) error {
			_, err := pipeline.ParseSceneDetector(v)
			return err
		},
		ValidateDefaults: true,
	}
}

// detectorUsage returns the help of the detector flag: each detector, what it is, the range of its
// thresholds and its defaults (single detection, then search), since a threshold is on the scale
// of the detector selected.
func detectorUsage() string {
	perDetector := make([]string, len(pipeline.SceneDetectors))
	for i, d := range pipeline.SceneDetectors {
		min, max := d.ThresholdRange()
		searchMin, searchMax := d.DefaultSearchRange()
		var what, note string
		switch d {
		case pipeline.SceneDetectorScdet:
			what = "ffmpeg's scdet filter"
		case pipeline.SceneDetectorTransNetV2:
			what = "the TransNetV2 neural network, run by its helper sptenc-detector-transnetv2 (see --" + transnetv2PathFlagName + ")"
			note = ", provisional defaults"
		}
		perDetector[i] = fmt.Sprintf("%s: %s, thresholds %s-%s, default %s, search %s-%s%s", d, what,
			d.FormatThreshold(min), d.FormatThreshold(max), d.FormatThreshold(d.DefaultThreshold()),
			d.FormatThreshold(searchMin), d.FormatThreshold(searchMax), note)
	}
	return fmt.Sprintf("Scene detector, whose scale the thresholds are on. %s", strings.Join(perDetector, "; "))
}

// singleThresholdFlag returns the threshold flag of the commands detecting scenes once: encode and
// split. If category is non-empty, the flag is grouped under that category in help output.
func singleThresholdFlag(intro, category string) cli.Flag {
	return &cli.Float64Flag{
		Name:    minThresholdFlagName,
		Aliases: []string{"T"},
		Usage:   thresholdUsage(intro, pipeline.SceneDetector.DefaultThreshold),
		// no Value: the default is the detector's, see singleSceneThreshold; the usage states it
		HideDefault: true,
		OnlyOnce:    true,
		Category:    category,
	}
}

// thresholdUsage returns the help of a threshold flag: its range and its default, for each
// detector, since a threshold is on the scale of the detector selected.
func thresholdUsage(intro string, defaultOf func(pipeline.SceneDetector) float64) string {
	perDetector := make([]string, len(pipeline.SceneDetectors))
	for i, d := range pipeline.SceneDetectors {
		min, max := d.ThresholdRange()
		perDetector[i] = fmt.Sprintf("%s: %s-%s, default %s", d,
			d.FormatThreshold(min), d.FormatThreshold(max), d.FormatThreshold(defaultOf(d)))
	}
	return fmt.Sprintf("%s, on the scale of --%s (%s)", intro, detectorFlagName, strings.Join(perDetector, "; "))
}

// sceneDetector returns the detector of the command. Its flag validates it already: an error here
// means a command without the flag.
func sceneDetector(cmd *cli.Command) (pipeline.SceneDetector, error) {
	return pipeline.ParseSceneDetector(cmd.String(detectorFlagName))
}

// validateSceneThresholds checks the threshold flags given against the range of the detector
// selected: a flag validator only sees its own value, so it is done once every flag is parsed (in
// the Before of the command). The flags not given take the detector's defaults, valid by
// construction.
func validateSceneThresholds(cmd *cli.Command, flagNames ...string) error {
	d, err := sceneDetector(cmd)
	if err != nil {
		return err
	}
	for _, name := range flagNames {
		if !cmd.IsSet(name) {
			continue
		}
		if err := d.ValidateThreshold(cmd.Float64(name)); err != nil {
			return fmt.Errorf("--%s %s: %w", name, d.FormatThreshold(cmd.Float64(name)), err)
		}
	}
	return nil
}

// singleSceneThreshold returns the threshold of a single detection (encode, split): the one given,
// else the default of the detector (see sceneDetector).
func singleSceneThreshold(cmd *cli.Command, d pipeline.SceneDetector) float64 {
	if cmd.IsSet(minThresholdFlagName) {
		return cmd.Float64(minThresholdFlagName)
	}
	return d.DefaultThreshold()
}

// searchSceneThresholds returns the range of a threshold search (batchsearch, thresholds): each
// bound given, else the default of the detector (see sceneDetector).
func searchSceneThresholds(cmd *cli.Command, d pipeline.SceneDetector) (min, max float64) {
	min, max = d.DefaultSearchRange()
	if cmd.IsSet(minThresholdFlagName) {
		min = cmd.Float64(minThresholdFlagName)
	}
	if cmd.IsSet(maxThresholdFlagName) {
		max = cmd.Float64(maxThresholdFlagName)
	}
	return
}

// validateSearchSceneThresholds checks the range of a threshold search: both bounds in the range
// of the detector, the lowest strictly below the highest.
func validateSearchSceneThresholds(cmd *cli.Command) error {
	if err := validateSceneThresholds(cmd, minThresholdFlagName, maxThresholdFlagName); err != nil {
		return err
	}
	d, err := sceneDetector(cmd)
	if err != nil {
		return err
	}
	if min, max := searchSceneThresholds(cmd, d); min >= max {
		return fmt.Errorf("--%s must be strictly less than --%s", minThresholdFlagName, maxThresholdFlagName)
	}
	return nil
}

// defaultSearchRanges returns the default range of a threshold search of each detector, for help
// texts: "scdet 8–40".
func defaultSearchRanges() string {
	ranges := make([]string, len(pipeline.SceneDetectors))
	for i, d := range pipeline.SceneDetectors {
		min, max := d.DefaultSearchRange()
		ranges[i] = fmt.Sprintf("%s %s–%s", d, d.FormatThreshold(min), d.FormatThreshold(max))
	}
	return strings.Join(ranges, ", ")
}

// reuseThresholdFlags returns the flags reproducing the scenes of a threshold with encode or split
// (see pipeline.SceneDetector.ReusableThreshold), detector included: a threshold is on the scale of
// its detector, and means nothing on another's.
func reuseThresholdFlags(d pipeline.SceneDetector, threshold float64) string {
	return fmt.Sprintf("--%s %s --%s %s", detectorFlagName, d, minThresholdFlagName, d.FormatThreshold(threshold))
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
	// safety net: no segment of the two real contents of BENCHMARKS.md reached it at the QPs
	// their VMAF search picked (0.80 at most, with hevc_nvenc), and it catches the smooth
	// gradients fidelity passes with bands added. Lower, it would chase CAMBI's own floor:
	// hevc_nvenc adds more than 0.5 to a clean radial gradient at every QP from 0 to 40. The
	// worst frame is for strict limits, off by default.
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

// vmafOriginalFlagName is the flag gating the model's original score instead of fidelity (see
// setupVMAF).
//
// # WHY THIS NAME
//
// It is there to compare with published VMAF v1 figures, not to encode: the banding the source
// already has counts against every encode (see ffmpeg.VMAFScore). Its name says which score it
// is, the one of the model as libvmaf computes it, and must not make it look like the better
// setting ("official", "full", "complete" would).
const vmafOriginalFlagName = "vmaf-original"

// vmafOriginalFlag returns the flag gating (reporting, for a command that gates nothing) the
// model's original score instead of fidelity.
func vmafOriginalFlag(category string, gate bool) cli.Flag {
	usage := "Report the model's original score, its banding feature (CAMBI) included, instead of fidelity: the banding the reference already has then counts against the distorted video (see MANUAL.md, Fidelity and banding)"
	if gate {
		usage = "Gate the model's original score, its banding feature (CAMBI) included, instead of fidelity, to compare with published VMAF v1 figures: the banding the source already has then counts against every encode (see MANUAL.md, Fidelity and banding)"
	}
	return &cli.BoolFlag{
		Name:     vmafOriginalFlagName,
		Usage:    usage,
		Value:    false,
		OnlyOnce: true,
		Category: category,
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
		vmafOriginalFlag(vmafProfileCategoryName, true),
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
