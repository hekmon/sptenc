package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/hekmon/sptenc/ffmpeg"

	"al.essio.dev/pkg/shellescape"
	"github.com/hekmon/cunits/v3"
	"github.com/hekmon/liveprogress/v2"
	"github.com/urfave/cli/v3"
)

// Flag names for split-specific flags (HW dec flags are also used by master).
const (
	analyzeFlagName         = "analyze"
	masterFlagName          = "master"
	nvdecFlagName           = "nvdec"
	vaapiDecFlagName        = "vaapidec"
	d3d12DecFlagName        = "d3d12vadec"
	videoToolboxDecFlagName = "videotoolboxdec"
)

var splitCommand = &cli.Command{
	Name:     "split",
	Aliases:  []string{"s"},
	Category: "Tooling",
	Usage:    "Split a video file by scenes",
	Description: "Detect scene changes in a video and split it into separate files at each transition.\n\n" +
		"HOW IT WORKS\n" +
		"By default, the command first creates a lossless FFV1 master to ensure frame-accurate cuts,\n" +
		"then analyzes the video with ffmpeg's scdet filter to find scene boundaries.\n\n" +
		"WORKFLOW\n" +
		"  1. Use --" + analyzeFlagName + " to preview detected scenes without splitting.\n" +
		"  2. Tune --" + minThresholdFlagName + " to control sensitivity (experiment first).\n" +
		"  3. Validate with the original file before performing the actual split.\n\n" +
		"If the input has already been converted with the master command, use --" + masterFlagName + " to skip the\n" +
		"master creation phase.",
	Flags: []cli.Flag{
		&cli.Float64Flag{
			Name:      minThresholdFlagName,
			Aliases:   []string{"T"},
			Usage:     fmt.Sprintf("Scene detection threshold (%d-%d)", ffmpeg.SceneThresholdMin, ffmpeg.SceneThresholdMax),
			Value:     10,
			OnlyOnce:  true,
			Validator: validateSceneThreshold,
		},
		&cli.BoolFlag{
			Name:     analyzeFlagName,
			Aliases:  []string{"a"},
			Usage:    "Simply analyze the input file (skip the master creation phase)",
			Value:    false,
			OnlyOnce: true,
		},
		&cli.BoolFlag{
			Name:     masterFlagName,
			Aliases:  []string{"m"},
			Usage:    "Input is an already-processed master file",
			Value:    false,
			OnlyOnce: true,
		},
		// Directories
		&cli.StringFlag{
			Name:     outputDirFlagName,
			Aliases:  []string{"o"},
			Usage:    "Output directory for split scenes",
			Value:    "",
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
		// HW dec
		&cli.BoolFlag{
			Name:     nvdecFlagName,
			Usage:    "Use NVDEC hardware-accelerated decoding (NVIDIA GPU required)",
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
			Usage:    "GPU to use with --nvdec",
			Value:    ffmpeg.CUDADefaultDevice,
			OnlyOnce: true,
			Category: "Hardware accelerated decoding",
		},
		&cli.StringFlag{
			Name:     vaapiRendererPathFlagName,
			Usage:    "Direct Rendering Manager render node to use with --vaapidec",
			Value:    ffmpeg.VAAPIDefaultDevice,
			OnlyOnce: true,
			Category: "Hardware accelerated decoding",
		},
		&cli.IntFlag{
			Name:     d3d12vaGPUIndexFlagName,
			Usage:    "GPU to use with --d3d12vadec",
			Value:    ffmpeg.D3D12VADefaultDevice,
			OnlyOnce: true,
			Category: "Hardware accelerated decoding",
		},
	},
	Arguments: []cli.Argument{
		&cli.StringArg{
			Name:      "inputfile",
			UsageText: "<input file>",
		},
	},
	Before: func(ctx context.Context, cmd *cli.Command) (context.Context, error) {
		// Check required tools
		if err := checkFFMPEG(ctx); err != nil {
			return ctx, err
		}
		if err := checkFFProbe(ctx); err != nil {
			return ctx, err
		}
		// Input file arg
		if cmd.Args().Len() != 1 {
			return ctx, errors.New("only one input file is required")
		}
		fileInfos, err := os.Stat(cmd.Args().First()) // args are not parsed yet, can not use cmd.StringArg("inputfile")
		if err != nil {
			return ctx, fmt.Errorf("failed to access input file: %w", err)
		}
		if !fileInfos.Mode().IsRegular() {
			return ctx, errors.New("input file must be a regular file")
		}
		ctx = context.WithValue(ctx, inputFileSizeCtxKey, fileInfos.Size())
		// Check output directory if explicitly provided
		if outputDir := cmd.String(outputDirFlagName); outputDir != "" {
			if fileInfos, err = os.Stat(outputDir); err != nil {
				return ctx, fmt.Errorf("failed to access output directory: %w", err)
			}
			if !fileInfos.IsDir() {
				return ctx, errors.New("output directory path must be a directory")
			}
		}
		// Validate that at most one hardware decode flag is set
		var hwDecFlags int
		if cmd.Bool(nvdecFlagName) {
			hwDecFlags++
		}
		if cmd.Bool(vaapiDecFlagName) {
			hwDecFlags++
		}
		if cmd.Bool(d3d12DecFlagName) {
			hwDecFlags++
		}
		if cmd.Bool(videoToolboxDecFlagName) {
			hwDecFlags++
		}
		if hwDecFlags > 1 {
			return ctx, errors.New("only one hardware decode flag can be set at a time (--nvdec, --vaapidec, --d3d12vadec, --videotoolboxdec)")
		}
		return ctx, nil
	},
	Action: func(ctx context.Context, cmd *cli.Command) (err error) {
		inputFilePath := cmd.StringArg("inputfile")

		// start live progress
		if err = liveprogress.Start(); err != nil {
			return fmt.Errorf("failed to start live progress: %w", err)
		}
		defer liveprogress.Stop(false)
		bypass := liveprogress.Bypass()

		// handle modes and master preparation
		fileToProcess := inputFilePath
		var stats ffmpeg.FFProbeStats
		if stats, err = ffmpeg.GetStreamsInfos(ctx, ffmpeg.GetStreamsInfosConfig{
			Path: inputFilePath,
			Debug: func(s string) {
				if cmd.Bool(debugFlagName) {
					fmt.Fprintf(bypass, "DEBUG: %s\n", s)
				}
			},
			RuntimeError: func(err error) {
				fmt.Fprintf(bypass, "ERROR: %s\n", err)
			},
		}); err != nil {
			return fmt.Errorf("failed to get streams infos: %w", err)
		}
		duration := stats.Format.Duration
		if cmd.Bool(analyzeFlagName) {
			fmt.Fprintf(bypass, "Analyzing scenes of %s (%s) with threshold %s\n",
				shellescape.Quote(filepath.Base(inputFilePath)),
				cunits.ImportInBytes(float64(ctx.Value(inputFileSizeCtxKey).(int64))),
				strconv.FormatFloat(cmd.Float64(minThresholdFlagName), 'f', -1, 64),
			)
		} else {
			fmt.Fprintf(bypass, "Splitting scenes of %s (%s) with threshold %s\n",
				shellescape.Quote(filepath.Base(inputFilePath)),
				cunits.ImportInBytes(float64(ctx.Value(inputFileSizeCtxKey).(int64))),
				strconv.FormatFloat(cmd.Float64(minThresholdFlagName), 'f', -1, 64),
			)
			if !cmd.Bool(masterFlagName) {
				// create a temporary directory
				var workingDir string
				if workingDir, err = createTempDir(cmd.String(tmpDirFlagName)); err != nil {
					return fmt.Errorf("failed to create temporary working directory in %s: %w",
						shellescape.Quote(cmd.String(tmpDirFlagName)), err,
					)
				}
				defer func() {
					if (err != nil && ctx.Err() != context.Canceled) || cmd.Bool(debugFlagName) {
						fmt.Fprintf(bypass, "Temporary directory left for inspection: %s\n",
							shellescape.Quote(workingDir),
						)
					} else {
						if removeErr := os.RemoveAll(workingDir); removeErr != nil {
							fmt.Fprintf(bypass, "Failed to delete temporary working directory %s: %s\n",
								shellescape.Quote(workingDir), removeErr,
							)
						}
					}
				}()
				if cmd.Bool(debugFlagName) {
					fmt.Fprintf(bypass, "DEBUG: Temporary directory created: %s\n", shellescape.Quote(workingDir))
				}
				// build optional hw decode config
				masterConfig := ffmpeg.SelectCompatibleDecoders(ctx, inputFilePath,
					cmd.Bool(nvdecFlagName), cmd.Bool(vaapiDecFlagName), cmd.Bool(d3d12DecFlagName), cmd.Bool(videoToolboxDecFlagName),
					cmd.Int(nvidiaGPUIndexFlagName), cmd.String(vaapiRendererPathFlagName), cmd.Int(d3d12vaGPUIndexFlagName),
				).ToFFV1MasterConfig()
				if (cmd.Bool(nvdecFlagName) || cmd.Bool(vaapiDecFlagName) || cmd.Bool(d3d12DecFlagName) || cmd.Bool(videoToolboxDecFlagName)) &&
					!masterConfig.NVDec && !masterConfig.VAAPIDec && !masterConfig.D3D12Dec && !masterConfig.VideoToolboxDec {
					fmt.Fprintln(bypass, "WARNING: input codec is not compatible with the requested hardware decoder, falling back to software decode")
				}
				// create the master within
				if fileToProcess, _, duration, err = createMaster(ctx, inputFilePath, workingDir, ctx.Value(inputFileSizeCtxKey).(int64), cmd.Bool(debugFlagName), masterConfig); err != nil {
					return fmt.Errorf("failed to create the master file: %w", err)
				}
			}
		}

		/*
		 * Execute
		 */

		// analyze
		fmt.Fprintf(bypass, "Detecting scenes with threshold at %s...\n",
			strconv.FormatFloat(cmd.Float64(minThresholdFlagName), 'f', -1, 64),
		)
		start := time.Now()
		scenesConfig := ffmpeg.ScenesDetectionConfig{
			NVDec:           cmd.Bool(nvdecFlagName),
			NVDevice:        cmd.Int(nvidiaGPUIndexFlagName),
			VAAPIDec:        cmd.Bool(vaapiDecFlagName),
			VAAPIDevice:     cmd.String(vaapiRendererPathFlagName),
			D3D12Dec:        cmd.Bool(d3d12DecFlagName),
			D3D12Device:     cmd.Int(d3d12vaGPUIndexFlagName),
			VideoToolboxDec: cmd.Bool(videoToolboxDecFlagName),
		}
		scenes, err := liveDetectScenes(ctx, fileToProcess, cmd.Float64(minThresholdFlagName), duration, cmd.Bool(debugFlagName), scenesConfig)
		if err != nil {
			return fmt.Errorf("failed to detect scenes: %w", err)
		}
		fmt.Fprintf(bypass, "\tDetected %d scenes in %s\n",
			1+len(scenes), time.Since(start).Round(time.Second),
		)
		if cmd.Bool(analyzeFlagName) {
			if !cmd.Bool(debugFlagName) {
				for i, scene := range scenes {
					fmt.Fprintf(bypass, "Scene #%d at %s with score %s\n",
						2+i, scene.Start, strconv.FormatFloat(scene.Score, 'f', -1, 64),
					)
				}
			}
			return
		}

		// split
		outputDir := cmd.String(outputDirFlagName)
		if outputDir == "" {
			outputDir = filepath.Dir(inputFilePath)
		}
		fmt.Fprintf(bypass, "Splitting scenes...\n")
		start = time.Now()
		if err = liveSplitScenes(ctx, fileToProcess, outputDir, duration, scenes, cmd.Bool(debugFlagName)); err != nil {
			return fmt.Errorf("failed to split scenes: %w", err)
		}
		fmt.Fprintf(bypass, "\tSplit %d scenes in %s\n",
			1+len(scenes), time.Since(start).Round(time.Second),
		)

		return
	},
}
