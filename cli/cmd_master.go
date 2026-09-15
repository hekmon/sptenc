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
	Name:        "master",
	Aliases:     []string{"m"},
	Usage:       "Create an intermediate lossless video master that can be cut at any frame",
	Description: "Most video files use Group of Pictures (GoP) encoding, mixing I, P, and B frames. Cutting can only happen on I frames (keyframes), which limits where cuts are possible. Open GoPs make things worse: some B and P frames depend on data outside the GoP, so cutting at an I frame can still silently drop surrounding frames. This command re-encodes the source into a lossless all-intra master using the FFV1 codec, producing a video-only stream where every frame is self-contained. Because FFV1 is mathematically lossless, this introduces no quality degradation compared to the original, enabling precise cuts at any frame with no generational loss.",
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:     "outputdir",
			Aliases:  []string{"o"},
			Usage:    "Output directory",
			Value:    "",
			OnlyOnce: true,
		},
		&cli.BoolFlag{
			Name:     "nvdec",
			Usage:    "Use NVDEC hardware-accelerated decoding (NVIDIA GPU required)",
			Value:    false,
			OnlyOnce: true,
			Category: "Hardware accelerated decoding",
		},
		&cli.BoolFlag{
			Name:     "vadec",
			Usage:    "Use VA-API hardware-accelerated decoding (Intel/AMD GPU required)",
			Value:    false,
			OnlyOnce: true,
			Category: "Hardware accelerated decoding",
		},
		&cli.BoolFlag{
			Name:     "d3d12dec",
			Usage:    "Use D3D12VA hardware-accelerated decoding (Windows, GPU required)",
			Value:    false,
			OnlyOnce: true,
			Category: "Hardware accelerated decoding",
		},
		&cli.IntFlag{
			Name:     "nvidiagpuindex",
			Usage:    "GPU to use with --nvdec",
			Value:    ffmpeg.CUDADefaultDevice,
			OnlyOnce: true,
			Category: "Hardware accelerated decoding",
		},
		&cli.StringFlag{
			Name:     "vaapirendererpath",
			Usage:    "Direct Rendering Manager render node to use with --vadec",
			Value:    ffmpeg.VAAPIDefaultDevice,
			OnlyOnce: true,
			Category: "Hardware accelerated decoding",
		},
		&cli.IntFlag{
			Name:     "d3d12vagpuindex",
			Usage:    "GPU to use with --d3d12dec",
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
		masterConfig := buildFFV1MasterConfigForFlags(ctx, inputFilePath,
			cmd.Bool("nvdec"), cmd.Bool("vadec"), cmd.Bool("d3d12dec"),
			cmd.Int("nvidiagpuindex"), cmd.String("vaapirendererpath"), cmd.Int("d3d12vagpuindex"),
		)
		if (cmd.Bool("nvdec") || cmd.Bool("vadec") || cmd.Bool("d3d12dec")) &&
			!masterConfig.NVDec && !masterConfig.VADec && !masterConfig.D3D12Dec {
			fmt.Fprintln(liveprogress.Bypass(), "WARNING: input codec is not compatible with the requested hardware decoder, falling back to software decode")
		}
		// create master
		var outputFile string
		outputFile, _, err = createMaster(ctx, inputFilePath, ctx.Value(outputDirCtxKey).(string), cmd.Bool(debugFlagName), masterConfig)
		if err == nil {
			fmt.Fprintf(liveprogress.Bypass(), "Master saved to: %s\n", shellescape.Quote(outputFile))
		}
		return
	},
}

// buildFFV1MasterConfigForFlags determines hardware decode settings for FFV1 master creation
// based on explicit user flags and input codec compatibility.
func buildFFV1MasterConfigForFlags(ctx context.Context, inputPath string, nvdec, vadec, d3d12dec bool, nvDevice int, vaDevice string, d3d12Device int) ffmpeg.FFV1VideoMasterConfig {
	var config ffmpeg.FFV1VideoMasterConfig
	if !nvdec && !vadec && !d3d12dec {
		return config
	}
	stats, err := ffmpeg.GetStreamsInfos(ctx, ffmpeg.GetStreamsInfosConfig{Path: inputPath})
	if err != nil {
		return config
	}
	video := stats.VideoTrack()
	if video == nil {
		return config
	}
	switch {
	case nvdec && ffmpeg.IsNVDecCompatible(video.CodecName):
		config.NVDec = true
		config.NVDevice = nvDevice
	case vadec && ffmpeg.IsVAAPIDecCompatible(video.CodecName):
		config.VADec = true
		config.VADevice = vaDevice
	case d3d12dec && ffmpeg.IsD3D12DecCompatible(video.CodecName):
		config.D3D12Dec = true
		config.D3D12Device = d3d12Device
	}
	return config
}

func createMaster(ctx context.Context, inputFilePath, outputDir string, debug bool, masterConfig ffmpeg.FFV1VideoMasterConfig) (outputFile string, duration time.Duration, err error) {
	// count frames
	fmt.Fprintln(liveprogress.Bypass(), "Counting the exact number of frames...")
	start := time.Now()
	nbFrames, codec, duration, err := liveCountNbFrames(ctx, inputFilePath, debug)
	if err != nil {
		err = fmt.Errorf("failed to count number of frames: %w", err)
		return
	}
	fmt.Fprintf(liveprogress.Bypass(), "\tCounted %d %s frames in %s\n",
		nbFrames, codec, time.Since(start).Round(time.Second),
	)
	// ffv1 encode
	fmt.Fprintln(liveprogress.Bypass(), "Creating a ffv1 lossless intra frames master...")
	inputFileName, _ := extractFileNameInfos(inputFilePath)
	outputFile = filepath.Join(outputDir, fmt.Sprintf("%s - ffv1 master.mkv", inputFileName))
	start = time.Now()
	if err = liveFFV1Master(ctx, inputFilePath, outputFile, nbFrames, debug, masterConfig); err != nil {
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
