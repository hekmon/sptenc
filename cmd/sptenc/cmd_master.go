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
		"Every frame becomes self-contained, so you can cut precisely at any frame with no quality loss.\n\n" +
		"PIXEL FORMAT\n" +
		"FFV1 is mathematically lossless, but the master is stored as 10-bit 4:2:0, the pixel format of\n" +
		"every sptenc output. For 8-bit and 10-bit 4:2:0 sources, by far the most common ones, the master\n" +
		"is bit-exact with the original. 4:2:2 and 4:4:4 sources get their chroma subsampled (luma stays\n" +
		"exact) and sources deeper than 10 bits are reduced to 10 bits: the conversion the final encode\n" +
		"requires anyway simply happens at this step.",
	Flags: func() []cli.Flag {
		flags := []cli.Flag{}
		flags = append(flags, hardwareAccelFlags(hwAccelScopeDecode)...)
		return flags
	}(),
	Arguments: []cli.Argument{
		&cli.StringArg{
			Name:      "inputfile",
			UsageText: "<input file>",
		},
		&cli.StringArg{
			Name:      "output",
			UsageText: "<output file>",
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
		// Check arguments
		if cmd.Args().Len() != 2 {
			return ctx, errors.New("exactly two arguments are required: input file and output file")
		}
		inputFilePath := cmd.Args().First()
		outputFilePath := cmd.Args().Get(1)
		fileInfos, err := os.Stat(inputFilePath)
		if err != nil {
			return ctx, fmt.Errorf("failed to access input file: %w", err)
		}
		if !fileInfos.Mode().IsRegular() {
			return ctx, errors.New("input file must be a regular file")
		}
		// Validate output path
		if err := validateOutputPath(outputFilePath); err != nil {
			return ctx, err
		}
		if inputFilePath == outputFilePath {
			return ctx, errors.New("input file and output file must be different paths")
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
		outputFilePath := cmd.StringArg("output")
		fileInfos, err := os.Stat(inputFilePath)
		if err != nil {
			return fmt.Errorf("failed to access input file: %w", err)
		}
		// reject what encode would reject later, before spending the time and the disk space
		sourceStats, err := getStreamsInfos(ctx, inputFilePath, cmd.Bool(debugFlagName))
		if err != nil {
			return fmt.Errorf("failed to probe input file: %w", err)
		}
		if _, err = checkSourceVideo(sourceStats); err != nil {
			return
		}
		fmt.Printf("Creating a master of %s (%s)\n",
			shellescape.Quote(filepath.Base(inputFilePath)),
			cunits.ImportInBytes(float64(fileInfos.Size())),
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
		var outputFile string
		outputFile, _, _, err = createMaster(ctx, inputFilePath, outputFilePath, fileInfos.Size(), cmd.Bool(debugFlagName), masterConfig)
		if err == nil {
			fmt.Fprintf(liveprogress.Bypass(), "Master saved to: %s\n", shellescape.Quote(outputFile))
		}
		return
	},
}

func createMaster(ctx context.Context, inputFilePath, outputFile string, inputFileSize int64, debug bool, masterConfig ffmpeg.FFV1VideoMasterConfig) (
	outputFileResult string, totalFrames int, duration time.Duration, err error) {
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
	outputFileResult = outputFile
	return
}
