package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/hekmon/sptenc/ffmpeg"

	"github.com/urfave/cli/v3"
)

// Flag names for batchsearch-specific flags.
const (
	finalEncodeFlagName = "finalencode"
)

var batchsearchCommand = &cli.Command{
	Name:     "batchsearch",
	Aliases:  []string{"bs"},
	Category: "Advanced",
	Usage:    "Automatically search for the optimal scene threshold by encoding the video multiple times",
	Description: fmt.Sprintf(
		"Orchestrate multiple encode passes with different scene detection thresholds\n" +
			"to find the one that produces the smallest file while still passing your VMAF targets.\n\n" +
			"HOW IT WORKS\n" +
			"  1. Scans the source once to map all natural scene boundaries.\n" +
			"  2. Tests only thresholds that actually change the scene list.\n" +
			"  3. Stops automatically when consecutive thresholds no longer shrink the file.\n\n" +
			"SCENE THRESHOLDS\n" +
			"Scene detection splits a video into independent segments, each starting\n" +
			"with an I-frame. I-frames are large because they encode a full picture\n" +
			"without reference to previous frames.\n\n" +
			"A threshold set too low detects too many scenes. Each scene gets its own QP,\n" +
			"which is good for quality, but the sheer number of I-frames leaves\n" +
			"too few continuous frames for the encoder to build efficient B and P\n" +
			"frames. The result can be a larger file than necessary.\n\n" +
			"A threshold set too high produces fewer, longer scenes. The encoder has\n" +
			"plenty of room for B and P frames, BUT the entire scene must be encoded\n" +
			"at the QP demanded by its most difficult passage, because VMAF validates\n" +
			"the whole scene as a single unit. sptenc re-encodes the scene at progressively\n" +
			"lower QPs until the hardest part passes, forcing easy sections to the same\n" +
			"lower QP and unnecessarily increasing their quality, which inflates file size.\n\n" +
			"The sweet spot is therefore a threshold that gives each scene enough freedom\n" +
			"to use its own QP while still leaving enough continuous frames for\n" +
			"the encoder to compress efficiently.\n\n" +
			"TIME\n" +
			"A single VMAF-oriented encode is already significantly slower than a regular\n" +
			"encode, because each segment may be re-encoded multiple times until it passes\n" +
			"quality validation. Batchsearch multiplies that cost by testing many thresholds\n" +
			"in sequence.\n\n" +
			"  * GPU search: may take several days or more.\n" +
			"  * CPU search: can take several weeks.\n\n" +
			"This command is designed to run unattended.\n\n" +
			"ENCODERS\n" +
			"Use --" + encoderFlagName + " to choose the encoder for the search loop. GPU encoders\n" +
			"(e.g. hevc_nvenc) are strongly recommended for speed. If available, also\n" +
			"enable CUDA VMAF acceleration (--vmafcuda) to avoid bottlenecking the search\n" +
			"on CPU-side quality validation.\n\n" +
			"FINAL ENCODE\n" +
			"When --" + finalEncodeFlagName + " is set and the search encoder is GPU-based, the command\n" +
			"automatically derives the equivalent CPU encoder of the same codec\n" +
			"(e.g. hevc_nvenc -> libx265) and performs the final encode with the discovered\n" +
			"threshold. The GPU-found threshold is usually a close enough approximation\n" +
			"for the CPU pass to be worth the speedup, though it may not be exactly optimal.\n\n" +
			"If the search encoder is already CPU-based, --" + finalEncodeFlagName + " is a no-op because\n" +
			"the search result is already the most precise result possible.",
	),
	Flags: func() (flags []cli.Flag) {
		flags = []cli.Flag{
			&cli.StringFlag{
				Name:     encoderFlagName,
				Aliases:  []string{"e"},
				Usage:    "Encoder to use during the threshold search loop. GPU encoders (e.g. hevc_nvenc) are strongly recommended for speed.",
				Value:    "hevc_nvenc",
				OnlyOnce: true,
			},
			&cli.BoolFlag{
				Name:     finalEncodeFlagName,
				Aliases:  []string{"f"},
				Usage:    "Run a final CPU encode after GPU search (no-op if search encoder is already CPU).",
				Value:    false,
				OnlyOnce: true,
			},
			&cli.Float64Flag{
				Name:    thresholdFlagName,
				Aliases: []string{"T"},
				Usage: fmt.Sprintf("Start threshold search at this value (valid values: %d-%d).",
					ffmpeg.SceneThresholdMin, ffmpeg.SceneThresholdMax,
				),
				Value:     10,
				OnlyOnce:  true,
				Validator: validateSceneThreshold,
			},
			&cli.StringFlag{
				Name:     cacheProfileFlagName,
				Aliases:  []string{"c"},
				Usage:    "Cache profile name to further isolate QP history (e.g. pixar_animation, sopranos_s01, grainy_90s). Defaults to the shared profile of the encoder + VMAF profile combination.",
				Value:    "",
				OnlyOnce: true,
				Category: "Cache isolation",
			},
		}
		flags = append(flags, newGPUSelectionFlags()...)
		flags = append(flags, newDirectoryFlags(false)...)
		flags = append(flags, newVMAFFlags()...)
		return
	}(),
	Arguments: []cli.Argument{
		&cli.StringArg{
			Name:      "inputpath",
			UsageText: "<input path>",
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
		if err := checkMKVPropEdit(ctx); err != nil {
			return ctx, err
		}
		// Check requested encoder is available
		encoders, err := ffmpeg.GetEncoders(ctx)
		if err != nil {
			return ctx, fmt.Errorf("failed to list ffmpeg encoders: %w", err)
		}
		requestedEncoder := cmd.String(encoderFlagName)
		if !encoders.Has(requestedEncoder) {
			return ctx, fmt.Errorf("requested encoder %q is not available in this ffmpeg build; run 'sptenc verify' to see available encoders", requestedEncoder)
		}
		// Check CUDA VMAF support if requested
		if cmd.Bool(vmafCUDAFlagName) {
			filters, err := ffmpeg.GetFilters(ctx)
			if err != nil {
				return ctx, fmt.Errorf("failed to list ffmpeg filters: %w", err)
			}
			if !filters.HasLibVMAFCUDA() {
				return ctx, fmt.Errorf("CUDA VMAF was requested but libvmaf_cuda is not available in this ffmpeg build; run 'sptenc verify' to see available filters")
			}
		}
		// Input path argument: batchsearch only accepts a single regular file
		if cmd.Args().Len() != 1 {
			return ctx, errors.New("only one input file is required")
		}
		fileInfos, err := os.Stat(cmd.Args().First())
		if err != nil {
			return ctx, fmt.Errorf("failed to access input file: %w", err)
		}
		if fileInfos.IsDir() {
			return ctx, errors.New("input path must be a regular file")
		}
		if !fileInfos.Mode().IsRegular() {
			return ctx, errors.New("input path must be a regular file")
		}
		ctx = context.WithValue(ctx, inputFileInfosCtxKey, fileInfos)
		// Check output directory
		// Check output directory if explicitly provided
		if outputDir := cmd.String(outputDirFlagName); outputDir != "" {
			if fileInfos, err = os.Stat(outputDir); err != nil {
				return ctx, fmt.Errorf("failed to access output directory: %w", err)
			}
			if !fileInfos.IsDir() {
				return ctx, errors.New("output directory path must be a directory")
			}
		}
		// Create the cache dir if necessary
		if err = os.MkdirAll(cmd.String(statsCacheDirFlagName), 0755); err != nil {
			return ctx, fmt.Errorf("failed to create cache directory: %w", err)
		}
		return ctx, nil
	},
	Action: func(ctx context.Context, cmd *cli.Command) error {
		// TODO: Implement the batchsearch algorithm:
		//  1. Analyze at a low threshold (e.g. 8) to get all boundary scores
		//  2. Sort scores and collapse to distinct integer candidates (skip integers that remove no new boundaries)
		//  3. Encode at each candidate with --searchencoder, tracking minimum size
		//  4. Stop after 3 consecutive candidates fail to beat the best size found so far.
		//     A flat result (same size as best) counts as a strike. This handles plateaus
		//     without chasing local jitter past the global minimum.
		//  5. Report the winning threshold
		//  6. If --finalencode and searchencoder is GPU: derive CPU equivalent of same codec, run final encode with winning threshold
		return nil
	},
}
