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

var splitCommand = &cli.Command{
	Name:        "split",
	Aliases:     []string{"s"},
	Usage:       "Split a video file by scenes",
	Description: "Detect scene changes in a video and split it into separate files at each transition. By default, the command first creates a lossless FFV1 master to ensure frame-accurate cuts, then analyzes the video with ffmpeg's scdet filter to find scene boundaries. Use --analyze to preview detected scenes without splitting, or --master if the input has already been converted with the master command. The detection threshold can be (and should be!) tuned with --threshold to control sensitivity: experiment different values with --analyze first and validate with the original file before performing the actual splitting.",
	Flags: []cli.Flag{
		&cli.Float64Flag{
			Name:      "threshold",
			Aliases:   []string{"T"},
			Usage:     fmt.Sprintf("Scene detection threshold for splitting video (%d-%d). See https://ffmpeg.org/ffmpeg-filters.html#scdet-1", ffmpeg.SceneThresholdMin, ffmpeg.SceneThresholdMax),
			Value:     10,
			OnlyOnce:  true,
			Validator: validateSceneThreshold,
		},
		&cli.BoolFlag{
			Name:     "analyze",
			Aliases:  []string{"a"},
			Usage:    "Simply analyze the input file (skip the master creation phase)",
			Value:    false,
			OnlyOnce: true,
		},
		&cli.BoolFlag{
			Name:     "master",
			Aliases:  []string{"m"},
			Usage:    "Use if the input file is an already-processed master file (see the master command). Without this flag, the split command will create one before splitting it.",
			Value:    false,
			OnlyOnce: true,
		},
		&cli.StringFlag{
			Name:     "outputdir",
			Aliases:  []string{"o"},
			Usage:    "Output directory for split scenes",
			Value:    "",
			OnlyOnce: true,
		},
		&cli.StringFlag{
			Name:             "tmpdir",
			Aliases:          []string{"t"},
			Usage:            "temporary directory location that will be used for intermediate files if needed",
			Value:            os.TempDir(),
			OnlyOnce:         true,
			Validator:        validateTmpDir,
			ValidateDefaults: true,
		},
		&cli.BoolFlag{
			Name:     "nvdec",
			Usage:    "Use NVDEC hardware-accelerated decoding when creating the master (NVIDIA GPU required)",
			Value:    false,
			OnlyOnce: true,
		},
		&cli.BoolFlag{
			Name:     "vadec",
			Usage:    "Use VA-API hardware-accelerated decoding when creating the master (Intel/AMD GPU required)",
			Value:    false,
			OnlyOnce: true,
		},
		&cli.BoolFlag{
			Name:     "d3d12dec",
			Usage:    "Use D3D12VA hardware-accelerated decoding when creating the master (Windows, GPU required)",
			Value:    false,
			OnlyOnce: true,
		},
		&cli.IntFlag{
			Name:     "nvidiagpuindex",
			Usage:    "GPU to use with --nvdec",
			Value:    ffmpeg.CUDADefaultDevice,
			OnlyOnce: true,
		},
		&cli.StringFlag{
			Name:     "vaapirendererpath",
			Usage:    "Direct Rendering Manager render node to use with --vadec",
			Value:    ffmpeg.VAAPIDefaultDevice,
			OnlyOnce: true,
		},
		&cli.IntFlag{
			Name:     "d3d12vagpuindex",
			Usage:    "GPU to use with --d3d12dec",
			Value:    ffmpeg.D3D12VADefaultDevice,
			OnlyOnce: true,
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
		// Resolve and check output directory
		outputDir := cmd.String("outputdir")
		if outputDir == "" {
			outputDir = filepath.Dir(cmd.Args().First())
		}
		if fileInfos, err = os.Stat(outputDir); err != nil {
			return ctx, fmt.Errorf("failed to access output directory: %w", err)
		}
		if !fileInfos.IsDir() {
			return ctx, errors.New("output directory path must be a directory")
		}
		ctx = context.WithValue(ctx, outputDirCtxKey, outputDir)
		// Validate that at most one hardware decode flag is set
		var hwDecFlags int
		if cmd.Bool("nvdec") {
			hwDecFlags++
		}
		if cmd.Bool("vadec") {
			hwDecFlags++
		}
		if cmd.Bool("d3d12dec") {
			hwDecFlags++
		}
		if hwDecFlags > 1 {
			return ctx, errors.New("only one hardware decode flag can be set at a time (--nvdec, --vadec, --d3d12dec)")
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

		// handle modes and master preparation
		fileToProcess := inputFilePath
		var stats ffmpeg.FFProbeStats
		if stats, err = ffmpeg.GetStreamsInfos(ctx, ffmpeg.GetStreamsInfosConfig{
			Path: inputFilePath,
			Debug: func(s string) {
				if cmd.Bool(debugFlagName) {
					fmt.Fprintf(liveprogress.Bypass(), "DEBUG: %s\n", s)
				}
			},
			RuntimeError: func(err error) {
				fmt.Fprintf(liveprogress.Bypass(), "ERROR: %s\n", err)
			},
		}); err != nil {
			return fmt.Errorf("failed to get streams infos: %w", err)
		}
		duration := stats.Format.Duration
		if cmd.Bool("analyze") {
			fmt.Fprintf(liveprogress.Bypass(), "Analyzing scenes of %s (%s) with threshold %s\n",
				shellescape.Quote(filepath.Base(inputFilePath)),
				cunits.ImportInBytes(float64(ctx.Value(inputFileSizeCtxKey).(int64))),
				strconv.FormatFloat(cmd.Float64("threshold"), 'f', -1, 64),
			)
		} else {
			fmt.Fprintf(liveprogress.Bypass(), "Splitting scenes of %s (%s) with threshold %s\n",
				shellescape.Quote(filepath.Base(inputFilePath)),
				cunits.ImportInBytes(float64(ctx.Value(inputFileSizeCtxKey).(int64))),
				strconv.FormatFloat(cmd.Float64("threshold"), 'f', -1, 64),
			)
			if !cmd.Bool("master") {
				// create a temporary directory
				workingDir := generateWorkingDirectoryPath(cmd.String("tmpdir"))
				if cmd.Bool(debugFlagName) {
					fmt.Fprintf(liveprogress.Bypass(), "DEBUG: Creating temporary working directory %s\n",
						shellescape.Quote(workingDir),
					)
				}
				if err = os.MkdirAll(workingDir, 0755); err != nil {
					return fmt.Errorf("failed to create temporary working directory %s: %w",
						shellescape.Quote(workingDir), err,
					)
				}
				defer func() {
					if err != nil || cmd.Bool(debugFlagName) {
						fmt.Fprintf(liveprogress.Bypass(), "Temporary directory left for inspection: %s\n",
							shellescape.Quote(workingDir),
						)
					} else {
						if removeErr := os.RemoveAll(workingDir); removeErr != nil {
							fmt.Fprintf(liveprogress.Bypass(), "Failed to delete temporary working directory %s: %s\n",
								shellescape.Quote(workingDir), removeErr,
							)
						}
					}
				}()
				// build optional hw decode config
				masterConfig := buildFFV1MasterConfigForFlags(ctx, inputFilePath,
					cmd.Bool("nvdec"), cmd.Bool("vadec"), cmd.Bool("d3d12dec"),
					cmd.Int("nvidiagpuindex"), cmd.String("vaapirendererpath"), cmd.Int("d3d12vagpuindex"),
				)
				if (cmd.Bool("nvdec") || cmd.Bool("vadec") || cmd.Bool("d3d12dec")) &&
					!masterConfig.NVDec && !masterConfig.VADec && !masterConfig.D3D12Dec {
					fmt.Fprintln(liveprogress.Bypass(), "WARNING: input codec is not compatible with the requested hardware decoder, falling back to software decode")
				}
				// create the master within
				if fileToProcess, duration, err = createMaster(ctx, inputFilePath, workingDir, cmd.Bool(debugFlagName), masterConfig); err != nil {
					return fmt.Errorf("failed to create the master file: %w", err)
				}
			}
		}

		// analyze
		fmt.Fprintf(liveprogress.Bypass(), "Detecting scenes with threshold at %s...\n",
			strconv.FormatFloat(cmd.Float64("threshold"), 'f', -1, 64),
		)
		start := time.Now()
		scenes, err := liveDetectScenes(ctx, fileToProcess, cmd.Float64("threshold"), duration, cmd.Bool(debugFlagName))
		if err != nil {
			return fmt.Errorf("failed to detect scenes: %w", err)
		}
		fmt.Fprintf(liveprogress.Bypass(), "\tDetected %d scenes in %s\n",
			1+len(scenes), time.Since(start).Round(time.Second),
		)
		if cmd.Bool("analyze") {
			if !cmd.Bool(debugFlagName) {
				for i, scene := range scenes {
					fmt.Fprintf(liveprogress.Bypass(), "Scene #%d at %s with score %s\n",
						2+i, scene.Start, strconv.FormatFloat(scene.Score, 'f', -1, 64),
					)
				}
			}
			return
		}

		// split
		fmt.Fprintf(liveprogress.Bypass(), "Splitting scenes...\n")
		start = time.Now()
		if err = liveSplitScenes(ctx, fileToProcess, ctx.Value(outputDirCtxKey).(string), duration, scenes, cmd.Bool(debugFlagName)); err != nil {
			return fmt.Errorf("failed to split scenes: %w", err)
		}
		fmt.Fprintf(liveprogress.Bypass(), "\tSplit %d scenes in %s\n",
			1+len(scenes), time.Since(start).Round(time.Second),
		)

		return
	},
}
