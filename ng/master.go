package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"al.essio.dev/pkg/shellescape"
	"github.com/hekmon/cunits/v3"
	"github.com/hekmon/liveprogress/v2"
	"github.com/urfave/cli/v3"
)

var masterCommand = &cli.Command{
	Name:        "master",
	Aliases:     []string{"m"},
	Usage:       "Create an intermediate lossless video master that can be cut at any frame",
	ArgsUsage:   "<input_file>",
	Description: "Most video files use Group of Pictures (GoP) encoding, mixing I, P, and B frames. Cutting can only happen on I frames (keyframes), which limits where cuts are possible. Open GoPs make things worse: some B and P frames depend on data outside the GoP, so cutting at an I frame can still silently drop surrounding frames. This command reencodes the source into a lossless all-intra master using the FFV1 codec, producing a video-only stream where every frame is self-contained. Because FFV1 is mathematically lossless, this introduces no quality degradation compared to the original, enabling precise cuts at any frame with no generational loss.",
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:     "outputdir",
			Aliases:  []string{"o"},
			Usage:    "Output directory",
			Value:    ".",
			OnlyOnce: true,
		},
	},
	Before: func(ctx context.Context, cmd *cli.Command) (context.Context, error) {
		if cmd.Args().Len() != 1 {
			return ctx, errors.New("one input file only is required")
		}
		fileInfos, err := os.Stat(cmd.Args().First())
		if err != nil {
			return ctx, fmt.Errorf("failed to access input file: %w", err)
		}
		if !fileInfos.Mode().IsRegular() {
			return ctx, errors.New("input file must be a regular file")
		}
		ctx = context.WithValue(ctx, inputFileSizeCtxKey, fileInfos.Size())
		return ctx, nil
	},
	Action: func(ctx context.Context, cmd *cli.Command) (err error) {
		// handle input file parts
		inputFilePath := cmd.Args().First()
		fmt.Printf("Creating a master of %s (%s)\n",
			shellescape.Quote(filepath.Base(inputFilePath)),
			cunits.ImportInBytes(float64(ctx.Value(inputFileSizeCtxKey).(int64))),
		)
		// start live progress
		if err = liveprogress.Start(); err != nil {
			return fmt.Errorf("failed to start live progress: %w", err)
		}
		defer liveprogress.Stop(false)
		debugPrint := func(s string) {
			if cmd.Bool("debug") {
				fmt.Fprintf(liveprogress.Bypass(), "DEBUG: %s\n", s)
			}
		}
		runtimeError := func(err error) {
			fmt.Fprintf(liveprogress.Bypass(), "ERROR: %s\n", err)
		}
		// count frames
		fmt.Fprintln(liveprogress.Bypass(), "Counting the exact number of frames...")
		start := time.Now()
		nbFrames, codec, err := liveCountNbFrames(ctx, inputFilePath, debugPrint, runtimeError)
		if err != nil {
			return fmt.Errorf("failed to count number of frames: %w", err)
		}
		fmt.Fprintf(liveprogress.Bypass(), "\tCounted %d %s frames in %s\n",
			nbFrames, codec, time.Since(start).Round(time.Second),
		)
		// ffv1 encode
		fmt.Fprintln(liveprogress.Bypass(), "Creating a FFV1 lossless master...")
		inputFileName, _ := extractFileNameInfos(inputFilePath)
		finalFile := filepath.Join(cmd.String("outputdir"), fmt.Sprintf("%s - ffv1 master.mkv", inputFileName))
		start = time.Now()
		if err = liveFFV1Master(ctx, inputFilePath, finalFile, nbFrames, debugPrint, runtimeError); err != nil {
			return fmt.Errorf("failed to create the ffv1 master: %w", err)
		}
		fileInfos, err := os.Stat(finalFile)
		if err != nil {
			return fmt.Errorf("failed to stat master file: %w", err)
		}
		fmt.Fprintf(liveprogress.Bypass(), "\tMaster created in %s: %s (%s)\n",
			time.Since(start).Round(time.Second),
			shellescape.Quote(finalFile),
			cunits.ImportInBytes(float64(fileInfos.Size())),
		)
		return nil
	},
}
