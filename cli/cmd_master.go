package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/hekmon/sptenc/ffmpeg"

	"al.essio.dev/pkg/shellescape"
	"github.com/hekmon/cunits/v3"
	"github.com/hekmon/liveprogress/v2"
	"github.com/urfave/cli/v3"
)

var masterCommand = &cli.Command{
	Name:     "master",
	Aliases:  []string{"m"},
	Category: "Tooling",
	Usage:    "Create an intermediate lossless video master that can be cut at any frame",
	Description: "Most video files use Group of Pictures (GoP) encoding, which mixes I, P, and B frames.\n\n" +
		"THE PROBLEM\n" +
		"  * Cuts can only happen on I-frames (keyframes).\n" +
		"  * Open GoPs make things worse: some B and P frames depend on data outside their own group,\n" +
		"    so cutting at an I-frame can still silently drop surrounding frames.\n\n" +
		"THE SOLUTION\n" +
		"This command re-encodes the source into a lossless all-intra master using the FFV1 codec.\n" +
		"Every frame becomes self-contained, so you can cut precisely at any frame with no quality loss.\n" +
		"FFV1 is mathematically lossless, so this introduces no degradation compared to the original.",
	Flags: func() []cli.Flag {
		flags := []cli.Flag{
			&cli.StringFlag{
				Name:     outputDirFlagName,
				Aliases:  []string{"o"},
				Usage:    "Output directory",
				Value:    "",
				OnlyOnce: true,
			},
		}
		flags = append(flags, hwDecodeFlags(false)...)
		return flags
	}(),
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
		// Input file
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
			return ctx, errors.New("only one hardware decode flag can be set at a time (--nvdec, --vaapi-dec, --d3d12va-dec, --videotoolbox-dec)")
		}
		return ctx, nil
	},
	Action: func(ctx context.Context, cmd *cli.Command) (err error) {
		// handle input file
		inputFilePath := cmd.StringArg("inputfile")
		fmt.Printf("Creating a master of %s (%s)\n",
			shellescape.Quote(filepath.Base(inputFilePath)),
			cunits.ImportInBytes(float64(ctx.Value(inputFileSizeCtxKey).(int64))),
		)
		// start live progress
		if err = liveprogress.Start(); err != nil {
			return fmt.Errorf("failed to start live progress: %w", err)
		}
		defer liveprogress.Stop(false)
		// build optional hw decode config
		masterConfig := ffmpeg.SelectCompatibleDecoders(ctx, inputFilePath,
			cmd.Bool(nvdecFlagName), cmd.Bool(vaapiDecFlagName), cmd.Bool(d3d12DecFlagName), cmd.Bool(videoToolboxDecFlagName),
			cmd.Int(nvidiaGPUIndexFlagName), cmd.String(vaapiRendererPathFlagName), cmd.Int(d3d12vaGPUIndexFlagName),
		).ToFFV1MasterConfig()
		if (cmd.Bool(nvdecFlagName) || cmd.Bool(vaapiDecFlagName) || cmd.Bool(d3d12DecFlagName) || cmd.Bool(videoToolboxDecFlagName)) &&
			!masterConfig.NVDec && !masterConfig.VAAPIDec && !masterConfig.D3D12Dec && !masterConfig.VideoToolboxDec {
			fmt.Fprintln(liveprogress.Bypass(), "WARNING: input codec is not compatible with the requested hardware decoder, falling back to software decode")
		}
		// create master
		outputDir := cmd.String(outputDirFlagName)
		if outputDir == "" {
			outputDir = filepath.Dir(inputFilePath)
		}
		var outputFile string
		outputFile, _, _, err = createMaster(ctx, inputFilePath, outputDir, ctx.Value(inputFileSizeCtxKey).(int64), cmd.Bool(debugFlagName), masterConfig)
		if err == nil {
			fmt.Fprintf(liveprogress.Bypass(), "Master saved to: %s\n", shellescape.Quote(outputFile))
		}
		return
	},
}

func createMaster(ctx context.Context, inputFilePath, outputDir string, inputFileSize int64, debug bool, masterConfig ffmpeg.FFV1VideoMasterConfig) (
	outputFile string, totalFrames int, duration time.Duration, err error) {
	// count frames
	fmt.Fprintln(liveprogress.Bypass(), "Counting the exact number of frames...")
	start := time.Now()
	totalFrames, codec, duration, err := liveCountNbFrames(ctx, inputFilePath, inputFileSize, debug)
	if err != nil {
		err = fmt.Errorf("failed to count number of frames: %w", err)
		return
	}
	fmt.Fprintf(liveprogress.Bypass(), "\tCounted %d %s frames in %s\n",
		totalFrames, codec, time.Since(start).Round(time.Second),
	)
	// ffv1 encode
	fmt.Fprintln(liveprogress.Bypass(), "Creating a ffv1 lossless intra frames master...")
	inputFileName, _ := extractFileNameInfos(inputFilePath)
	outputFile = filepath.Join(outputDir, fmt.Sprintf("%s - ffv1 master.mkv", inputFileName))
	start = time.Now()
	if err = liveFFV1Master(ctx, inputFilePath, outputFile, totalFrames, debug, masterConfig); err != nil {
		err = fmt.Errorf("failed to encode the ffv1 master: %w", err)
		return
	}
	masterDuration := time.Since(start)
	fileInfos, err := os.Stat(outputFile)
	if err != nil {
		err = fmt.Errorf("failed to stat master file: %w", err)
		return
	}
	if debug {
		fmt.Fprintf(liveprogress.Bypass(), "\tMaster created in %s: %s (%s)\n",
			masterDuration.Round(time.Second),
			shellescape.Quote(outputFile),
			cunits.ImportInBytes(float64(fileInfos.Size())),
		)
	} else {
		fmt.Fprintf(liveprogress.Bypass(), "\tMaster created in %s (%s)\n",
			masterDuration.Round(time.Second),
			cunits.ImportInBytes(float64(fileInfos.Size())),
		)
	}
	return
}
