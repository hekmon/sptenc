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
	Description: "Most video files use Group of Pictures (GoP) encoding: only the keyframes (I-frames) are\n" +
		"self-contained, P and B frames are stored as differences from the frames they reference.\n\n" +
		"THE PROBLEM\n" +
		"  * Without re-encoding, a video can only be cut on a keyframe: a scene boundary falling between\n" +
		"    two keyframes can not be cut where it is.\n" +
		"  * Open GoPs make things worse: some frames reference frames of another group, which a cut, even\n" +
		"    on a keyframe, can leave in a different segment. These frames can not be decoded anymore and\n" +
		"    are dropped at decoding: every cut shortens the video a little more, a drift that accumulates\n" +
		"    (against the audio, for instance).\n\n" +
		"THE SOLUTION\n" +
		"This command re-encodes the source into a lossless all-intra master using the FFV1 codec.\n" +
		"Every frame becomes self-contained, so you can cut precisely at any frame with no quality loss\n" +
		"and no dropped frame.\n\n" +
		"PIXEL FORMAT\n" +
		"FFV1 is mathematically lossless, but the master is stored as 10-bit 4:2:0, the pixel format of\n" +
		"every sptenc output. From limited range 8-bit and 10-bit 4:2:0 sources, by far the most common\n" +
		"ones, it holds every sample of the original without loss (8-bit values are shifted to 10 bits).\n" +
		"Other sources are converted at this step: 4:2:2 and 4:4:4 ones get their chroma subsampled (luma\n" +
		"stays exact), sources deeper than 10 bits are reduced to 10 bits, and full range sources are\n" +
		"converted to limited range (without loss from 8 bits, with a slight loss from 10 bits).",
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
		if _, err := hwDecodeFlags(cmd); err != nil {
			return ctx, err
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
		liveprogress.AddCustomLine(func() string { return "" }) // separate logs from live status updates
		// build optional hw decode config
		requestedDecoder, _ := hwDecodeFlags(cmd) // validated in Before
		decoderCfg := requestedDecoder.CompatibleWith(ctx, inputFilePath)
		if requestedDecoder.Enabled() && !decoderCfg.Enabled() {
			fmt.Fprintf(liveprogress.Bypass(), "WARNING: input codec is not decoded with %s (not supported, or not decoded exactly: see MANUAL.md, Hardware decoding), falling back to software decode\n", requestedDecoder.Name())
		}
		// create master
		var outputFile string
		outputFile, _, _, err = createMaster(ctx, inputFilePath, outputFilePath, cmd.Bool(debugFlagName), decoderCfg)
		if err == nil {
			fmt.Fprintf(liveprogress.Bypass(), "Master saved to: %s\n", shellescape.Quote(outputFile))
		}
		return
	},
}

// createMaster counts the frames of the source then encodes its video stream to a lossless
// FFV1 master, both with the hardware decoder of decoderCfg (software decode when none).
func createMaster(ctx context.Context, inputFilePath, outputFile string, debug bool, decoderCfg ffmpeg.HWDecoderConfig) (
	outputFileResult string, totalFrames int, duration time.Duration, err error) {
	// count frames
	fmt.Fprintln(liveprogress.Bypass(), "Counting the exact number of frames...")
	start := time.Now()
	videoInfos, duration, err := liveProbeVideoCF(ctx, inputFilePath, debug, decoderCfg)
	if err != nil {
		err = fmt.Errorf("failed to count number of frames: %w", err)
		return
	}
	totalFrames = videoInfos.NbReadFrames
	fmt.Fprintf(liveprogress.Bypass(), "\tCounted %d %s frames in %s\n",
		totalFrames, videoInfos.CodecName, time.Since(start).Round(time.Second),
	)
	// The frame rates declared by the source have been checked already (checkSourceVideo), but
	// they can not be trusted with every container: now that every frame has been read, check
	// how long they really last (see FFProbeBinaryStream.IsConstantFrameRate).
	if !videoInfos.IsConstantFrameRate() {
		err = fmt.Errorf("variable frame rate (VFR) content is not supported: frames last from %s to %s while the source declares a constant frame rate (%s fps)",
			videoInfos.ShortestFrameDuration, videoInfos.LongestFrameDuration, videoInfos.RFrameRate)
		return
	}
	// ffv1 encode
	fmt.Fprintln(liveprogress.Bypass(), "Creating a ffv1 lossless intra frames master...")
	start = time.Now()
	if err = liveFFV1Master(ctx, inputFilePath, outputFile, totalFrames, debug, decoderCfg.ToFFV1MasterConfig()); err != nil {
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
