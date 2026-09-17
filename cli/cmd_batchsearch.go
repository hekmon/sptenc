package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/hekmon/sptenc/ffmpeg"

	"github.com/urfave/cli/v3"
)

// Flag names for batchsearch-specific flags.
const (
	searchEncoderFlagName  = "searchencoder"
	finalEncodeFlagName    = "finalencode"
	thresholdStartFlagName = "thresholdstart"
)

// batchsearch finds the optimal scene detection threshold for a given source file.
//
// Scene detection splits a video into independent segments, each starting with an
// I-frame. I-frames are large because they encode a full picture without reference
// to previous frames. Rate control also resets at every segment boundary, forcing
// the encoder to "warm up" again before it can efficiently use P and B frames.
//
// By raising the scene threshold, fewer boundaries are detected. Segments grow
// longer, and the encoder can keep running open-GOP prediction across what used
// to be hard cuts. With per-segment VMAF validation, brief visual inconsistencies
// at merged transitions are averaged out over the longer segment, so the quality
// target still passes while the overall bitrate drops.
//
// This command searches for the sweet spot for your specific content: the highest
// threshold that still shrinks the file before merging real scene changes starts
// costing more in prediction accuracy than it saves in I-frame overhead. It
// analyzes the source once to map all boundary scores, then tests only the
// structurally distinct thresholds, stopping automatically when consecutive steps
// inflate the size.
//
// # SEARCH ENCODER VS FINAL ENCODER
//
// The search phase uses whatever encoder you specify (e.g. hevc_nvenc for speed).
// If the search encoder is GPU-based, a warning is issued because the search is
// designed to be fast. The winning threshold from the search is then reused for
// the final encode.
//
// When --finalencode is set and the search encoder is GPU-based, the command
// derives the equivalent CPU encoder of the same codec (e.g. hevc_nvenc ->
// libx265) and runs a full-quality final encode at the discovered threshold.
// The sweet spot is usually close enough across encoders of the same codec
// family that the GPU-found threshold is a good approximation for the CPU pass,
// while being far faster to discover than a full CPU search. This is the intended
// speed-vs-accuracy tradeoff of GPU search mode.
//
// If the search encoder is already CPU-based, --finalencode is a no-op: the
// winning result from the search is the definitive output.
var batchsearchCommand = &cli.Command{
	Name:     "batchsearch",
	Aliases:  []string{"bs"},
	Category: "Advanced",
	Usage:    "Find the optimal scene detection threshold for a given source file",
	Description: fmt.Sprintf(
		"Analyze a video to discover the scene detection threshold that produces\n" +
			"the smallest encode which still passes your VMAF targets.\n\n" +
			"HOW IT WORKS\n" +
			"  1. Scans the source once to map all natural scene boundaries.\n" +
			"  2. Tests only thresholds that actually change the scene list.\n" +
			"  3. Stops automatically when consecutive thresholds no longer shrink the file.\n\n" +
			"SCENE THRESHOLDS\n" +
			"Scene detection splits a video into independent segments, each starting\n" +
			"with an I-frame. I-frames are large because they encode a full picture\n" +
			"without reference to previous frames.\n\n" +
			"By raising the scene threshold, fewer boundaries are detected and segments\n" +
			"grow longer. The encoder can then keep running efficient P/B prediction\n" +
			"across what used to be hard cuts.\n\n" +
			"Because VMAF is checked per-segment (not per-frame), a few smeared frames\n" +
			"at a merged boundary are diluted by the rest of the segment. The overall\n" +
			"score still passes, yet the encoder avoids the repeated I-frame and\n" +
			"rate-control overhead that a hard cut would force.\n\n" +
			"TIME\n" +
			"A single VMAF-oriented encode is already significantly slower than a regular\n" +
			"encode, because each segment may be re-encoded multiple times until it passes\n" +
			"quality validation. Batchsearch multiplies that cost by testing many thresholds\n" +
			"in sequence.\n\n" +
			"  * GPU search: may take a dozen hours or more.\n" +
			"  * CPU search: can take several days.\n\n" +
			"This command is designed to run unattended.\n\n" +
			"ENCODERS\n" +
			"Use --searchencoder to choose the encoder for the search loop. GPU encoders\n" +
			"(e.g. hevc_nvenc) are strongly recommended for speed. If available, also\n" +
			"enable CUDA VMAF acceleration (--vmafcuda) to avoid bottlenecking the search\n" +
			"on CPU-side quality validation. A warning is raised if a non-GPU encoder is selected.\n\n" +
			"FINAL ENCODE\n" +
			"When --finalencode is set and the search encoder is GPU-based, the command\n" +
			"automatically derives the equivalent CPU encoder of the same codec\n" +
			"(e.g. hevc_nvenc -> libx265) and performs the final encode with the discovered\n" +
			"threshold. The GPU-found threshold is usually a close enough approximation\n" +
			"for the CPU pass to be worth the speedup, though it may not be exactly optimal.\n\n" +
			"If the search encoder is already CPU-based, --finalencode is a no-op because\n" +
			"the search result is already the most precise result possible.",
	),
	Flags: func() []cli.Flag {
		flags := []cli.Flag{
			&cli.StringFlag{
				Name:     searchEncoderFlagName,
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
				Name:    thresholdStartFlagName,
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
				Usage:    "Cache profile name to isolate QP history (e.g. pixar_animation, sopranos_s01, grainy_90s). Defaults to the shared profile.",
				Value:    "",
				OnlyOnce: true,
			},
		}
		flags = append(flags, newGPUFlags(searchEncoderFlagName, "GPU Accelerated Encoders")...)
		flags = append(flags, newDirectoryFlags()...)
		flags = append(flags, newVMAFFlags()...)
		return flags
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
		requestedEncoder := cmd.String(searchEncoderFlagName)
		if !encoders.Has(requestedEncoder) {
			return ctx, fmt.Errorf("requested encoder %q is not available in this ffmpeg build; run 'sptenc check' to see available encoders", requestedEncoder)
		}
		// Check CUDA VMAF support if requested
		if cmd.Bool(vmafCUDAFlagName) {
			filters, err := ffmpeg.GetFilters(ctx)
			if err != nil {
				return ctx, fmt.Errorf("failed to list ffmpeg filters: %w", err)
			}
			if !filters.HasLibVMAFCUDA() {
				return ctx, fmt.Errorf("CUDA VMAF was requested but libvmaf_cuda is not available in this ffmpeg build; run 'sptenc check' to see available filters")
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
		ctx = context.WithValue(ctx, inputFileSizeCtxKey, fileInfos.Size())
		// Resolve and check output directory
		outputDir := cmd.String(outputDirFlagName)
		if outputDir == "" {
			outputDir = filepath.Dir(cmd.Args().First())
		}
		if fileInfos, err = os.Stat(outputDir); err != nil {
			return ctx, fmt.Errorf("failed to access output directory: %w", err)
		}
		if !fileInfos.IsDir() {
			return ctx, errors.New("output directory path must be a directory")
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
