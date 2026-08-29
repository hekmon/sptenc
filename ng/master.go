package main

import (
	"context"
	"errors"
	"fmt"
	"os"

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
			Name:     "output",
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
		return ctx, nil
	},
	Action: func(ctx context.Context, cmd *cli.Command) (err error) {
		inputFile := cmd.Args().First()
		fmt.Printf("Processing %q\n", inputFile)
		return nil
	},
}
