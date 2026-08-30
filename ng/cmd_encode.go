package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"al.essio.dev/pkg/shellescape"
	"github.com/hekmon/cunits/v3"
	"github.com/hekmon/liveprogress/v2"
	"github.com/urfave/cli/v3"
)

var encodeCommand = &cli.Command{
	Name:        "encode",
	Aliases:     []string{"e"},
	Usage:       "Encode video segments to meet perceptual quality targets at minimal file size",
	Description: fmt.Sprintf("Performs scene-aware encoding where each segment is independently encoded and validated against configurable VMAF thresholds. For each segment, sptenc searches for the highest QP (smallest file size) that still passes all enabled VMAF metrics. If a segment fails at a given QP, it re-encodes at a lower QP until all thresholds are met or QP 0 is reached. Once all segments pass validation, they are merged into the final output file with VMAF results embedded as metadata tags. This approach guarantees the target perceptual quality at the smallest possible file size for the given encoder, but encoding takes significantly longer than a standard single-pass encode because multiple QP candidates are tested per segment.\n\nThe input path can be provided in two forms:\n* pre-split video files: every video file within the pointed directory will be treated as already segmented scenes and used directly for the encode phase (see the split command)\n* single video file: sptenc will first create a lossless FFV1 master and split it into scene-aligned segments using the given threshold before encoding\n\nEach VMAF metric flag sets the minimum acceptable VMAF score (%d-%d) for that statistic. If a segment encoding falls below any enabled threshold, it is considered invalid and re-encoded at a lower QP. Set a value to %d to disable that metric.\nVMAF NEG (No Enhancement Gain) models are alternative VMAF model variants recommended when the source has undergone upscaling, sharpening, or denoising, as these can artificially inflate standard VMAF scores. NEG models provide more conservative scoring by ignoring enhancement gains, so expect lower scores. Use the --vmafneg flag to enable them.", VMAFMinValue, VMAFMaxValue, VMAFOffValue),
	Flags: []cli.Flag{
		// directories
		&cli.StringFlag{
			Name:     "outputdir",
			Aliases:  []string{"o"},
			Usage:    "Output directory",
			Value:    ".",
			OnlyOnce: true,
			Category: "Directories",
		},
		&cli.StringFlag{
			Name:     "statscachedir",
			Aliases:  []string{"s"},
			Usage:    "Stats cache directory. Used to save encoding QP search mean and stddev to speed up future encoding.",
			Value:    getCacheDir(),
			OnlyOnce: true,
			Category: "Directories",
		},
		&cli.StringFlag{
			Name:             "tmpdir",
			Aliases:          []string{"t"},
			Usage:            "Temporary directory location that will be used for intermediate files if needed",
			Value:            os.TempDir(),
			OnlyOnce:         true,
			Validator:        validateTmpDir,
			ValidateDefaults: true,
			Category:         "Directories",
		},
		// single video file
		&cli.Float64Flag{
			Name:      "threshold",
			Aliases:   []string{"T"},
			Usage:     fmt.Sprintf("Scene detection threshold for splitting video (%d-%d). Find the right value for your video with the split command.", sceneThresholdMin, sceneThresholdMax),
			Value:     10,
			OnlyOnce:  true,
			Category:  "Single Video File",
			Validator: validateSceneThreshold,
		},
		// pre-split video files
		&cli.StringFlag{
			Name:     "originalfile",
			Aliases:  []string{"f"},
			Usage:    "Original file to use when performing the final remuxing (used to recover all other streams: audio, subtitles, etc.)",
			Value:    "",
			OnlyOnce: true,
			Category: "Pre-Split Video Files",
		},
		// VMAF
		&cli.BoolFlag{
			Name:     "vmafcuda",
			Usage:    "Activate CUDA acceleration for VMAF computing. libvmaf must have been compiled with CUDA support.",
			Value:    false,
			OnlyOnce: true,
			Category: "VMAF",
		},
		&cli.BoolFlag{
			Name:     "vmafneg",
			Usage:    "Use VMAF NEG models",
			Value:    false,
			OnlyOnce: true,
			Category: "VMAF",
		},
		&cli.Float64Flag{
			Name:      "vmafmin",
			Usage:     "Minimum acceptable VMAF score for the worst frame.",
			Value:     VMAFOffValue,
			OnlyOnce:  true,
			Category:  "VMAF",
			Validator: vmafValueValidator,
		},
		&cli.Float64Flag{
			Name:      "vmafp1",
			Usage:     "Minimum acceptable VMAF score for the 1st percentile.",
			Value:     VMAFOffValue,
			OnlyOnce:  true,
			Category:  "VMAF",
			Validator: vmafValueValidator,
		},
		&cli.Float64Flag{
			Name:      "vmafp5",
			Usage:     "Minimum acceptable VMAF score for the 5th percentile.",
			Value:     VMAFOffValue,
			OnlyOnce:  true,
			Category:  "VMAF",
			Validator: vmafValueValidator,
		},
		&cli.Float64Flag{
			Name:      "vmafp10",
			Usage:     "Minimum acceptable VMAF score for the 10th percentile.",
			Value:     VMAFOffValue,
			OnlyOnce:  true,
			Category:  "VMAF",
			Validator: vmafValueValidator,
		},
		&cli.Float64Flag{
			Name:      "vmafp25",
			Usage:     "Minimum acceptable VMAF score for the 25th percentile.",
			Value:     VMAFOffValue,
			OnlyOnce:  true,
			Category:  "VMAF",
			Validator: vmafValueValidator,
		},
		&cli.Float64Flag{
			Name:      "vmafmedian",
			Usage:     "Minimum acceptable VMAF score for the median (50th percentile).",
			Value:     VMAFOffValue,
			OnlyOnce:  true,
			Category:  "VMAF",
			Validator: vmafValueValidator,
		},
		&cli.Float64Flag{
			Name:      "vmafhmean",
			Usage:     "Minimum acceptable VMAF score for harmonic mean.",
			Value:     VMAFOffValue,
			OnlyOnce:  true,
			Category:  "VMAF",
			Validator: vmafValueValidator,
		},
		&cli.Float64Flag{
			Name:      "vmafmean",
			Usage:     "Minimum acceptable VMAF score for mean.",
			Value:     93,
			OnlyOnce:  true,
			Category:  "VMAF",
			Validator: vmafValueValidator,
		},
	},
	Arguments: []cli.Argument{
		&cli.StringArg{
			Name:      "inputpath",
			UsageText: "<input path>",
		},
	},
	Before: func(ctx context.Context, cmd *cli.Command) (context.Context, error) {
		// Input path argument
		if cmd.Args().Len() != 1 {
			return ctx, errors.New("only one input file is required")
		}
		fileInfos, err := os.Stat(cmd.Args().First()) // args are not parsed yet, can not use cmd.StringArg("inputpath")
		if err != nil {
			return ctx, fmt.Errorf("failed to access input file: %w", err)
		}
		ctx = context.WithValue(ctx, inputFileInfosCtxKey, fileInfos)
		if !fileInfos.IsDir() {
			if !fileInfos.Mode().IsRegular() {
				return ctx, errors.New("input path must be a directory or a regular file")
			}
			// if input is dir, we need the originalfile to be set
			if cmd.String("originalfile") == "" {
				return ctx, errors.New("when input path is a directory, you must specify the --originalfile flag")
			}
			if fileInfos, err = os.Stat(cmd.String("originalfile")); err != nil {
				return ctx, fmt.Errorf("failed to access original file: %w", err)
			}
			if !fileInfos.Mode().IsRegular() {
				return ctx, errors.New("original file must be a regular file")
			}
		}
		//
		return ctx, nil
	},
	Action: func(ctx context.Context, cmd *cli.Command) (err error) {
		// handle input file
		inputPath := cmd.StringArg("inputpath")
		fmt.Printf("Creating a master of %s (%s)\n",
			shellescape.Quote(filepath.Base(inputPath)),
			cunits.ImportInBytes(float64(ctx.Value(inputFileSizeCtxKey).(int64))),
		)
		// start live progress
		if err = liveprogress.Start(); err != nil {
			return fmt.Errorf("failed to start live progress: %w", err)
		}
		defer liveprogress.Stop(false)
		// create master
		_, _, err = createMaster(ctx, inputPath, cmd.String("outputdir"), cmd.Bool(debugFlagName))
		return
	},
}
