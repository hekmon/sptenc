package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/hekmon/sptenc/core"
	"github.com/hekmon/sptenc/ffmpeg"

	"al.essio.dev/pkg/shellescape"
	"github.com/hekmon/cunits/v3"
	"github.com/hekmon/liveprogress/v2"
	"github.com/urfave/cli/v3"
)

// Flag names for batchsearch-specific flags.
const (
	finalEncodeFlagName            = "finalencode"
	searchIncrementMinimumFlagName = "searchincrement"
	searchIncrementMinimum         = 1
	nbStrikesFlagName              = "nbstrikes"
	nbStrikesMinimum               = 3
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
			"  3. Incrementally tests higher thresholds and stops after consecutive\n" +
			"     ones no longer shrink the file.\n\n" +
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
				Usage:    fmt.Sprintf("Encoder to use during the threshold search loop. Valid values: %s", strings.Join(allEncoders, ", ")),
				Value:    string(ffmpeg.HEVCEncoderNVEnc),
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
				Category:  "Threshold Search",
			},
			&cli.IntFlag{
				Name:     searchIncrementMinimumFlagName,
				Aliases:  []string{"i"},
				Usage:    "The minimum search increment",
				Value:    searchIncrementMinimum,
				OnlyOnce: true,
				Validator: func(v int) error {
					if v < searchIncrementMinimum {
						return fmt.Errorf("%s must be %d at minimum", searchIncrementMinimumFlagName, searchIncrementMinimum)
					}
					return nil
				},
				Category: "Threshold Search",
			},
			&cli.IntFlag{
				Name:     nbStrikesFlagName,
				Aliases:  []string{"s"},
				Usage:    "Stop the search after this many consecutive thresholds fail to reduce file size",
				Value:    nbStrikesMinimum,
				OnlyOnce: true,
				Validator: func(v int) error {
					if v < nbStrikesMinimum {
						return fmt.Errorf("%s must be %d at minimum", nbStrikesFlagName, nbStrikesMinimum)
					}
					return nil
				},
				Category: "Threshold Search",
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
	Action: func(ctx context.Context, cmd *cli.Command) (err error) {
		/*
		 * Prepare
		 */

		// retreive input infos
		inputPath := cmd.StringArg("inputpath")
		inputInfos := ctx.Value(inputFileInfosCtxKey).(os.FileInfo)

		// start live progress
		if err = liveprogress.Start(); err != nil {
			return fmt.Errorf("failed to start live progress: %w", err)
		}
		defer liveprogress.Stop(false)
		bypass := liveprogress.Bypass()
		liveprogress.AddCustomLine(func() string { return "" }) // separate logs from live status updates

		// create a temporary directory
		var workingDir string
		if workingDir, err = createTempDir(cmd.String(tmpDirFlagName)); err != nil {
			return fmt.Errorf("failed to create temporary working directory in %s: %w",
				shellescape.Quote(cmd.String(tmpDirFlagName)), err,
			)
		}
		defer func() {
			if err != nil || cmd.Bool(debugFlagName) {
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

		// Create the VMAF auditor
		vmafAuditor, err := core.NewVMAFChecker(
			cmd.Float64(vmafMinFlagName), cmd.Float64(vmafP1FlagName), cmd.Float64(vmafP5FlagName), cmd.Float64(vmafP10FlagName),
			cmd.Float64(vmafP25FlagName), cmd.Float64(vmafMedianFlagName), cmd.Float64(vmafHMeanFlagName), cmd.Float64(vmafMeanFlagName))
		if err != nil {
			err = fmt.Errorf("failed to create VMAF auditor: %w", err)
			return
		}

		// Get the stats cache
		statsCache, err := core.NewStatsCacheHistory(cmd.String(statsCacheDirFlagName), ffmpeg.Encoder(cmd.String(encoderFlagName)),
			vmafAuditor, cmd.String(cacheProfileFlagName))
		if err != nil {
			err = fmt.Errorf("failed to create stats cache: %w", err)
			return
		}
		if cmd.Bool(debugFlagName) {
			fmt.Fprintf(bypass, "DEBUG: Using stats cache at: %s\n", shellescape.Quote(statsCache.GetPath()))
		}

		// final encoding ?
		var finalEncoder ffmpeg.Encoder
		if cmd.Bool(finalEncodeFlagName) {
			cpuEncoder, alreadyCPU := ffmpeg.GetCPURelative(ffmpeg.Encoder(cmd.String(encoderFlagName)))
			if !alreadyCPU {
				finalEncoder = cpuEncoder
			}
		}

		/*
		 * Execute process
		 */
		// completeRunStart := time.Now()

		fmt.Fprintf(bypass, "\nStarting batch search encoding of %s (%s) with %s.\n",
			shellescape.Quote(filepath.Base(inputPath)),
			cunits.ImportInBytes(float64(inputInfos.Size())),
			cmd.String(encoderFlagName),
		)
		fmt.Fprintf(bypass, "\t• search starts at threshold %s\n", strconv.FormatFloat(cmd.Float64(thresholdFlagName), 'f', -1, 64))
		fmt.Fprintf(bypass, "\t• each candidate must have a minimum increment of %d\n", cmd.Int(searchIncrementMinimumFlagName))
		fmt.Fprintf(bypass, "\t• waiting at least %d strikes before stopping\n", cmd.Int(nbStrikesFlagName))
		if finalEncoder != "" {
			fmt.Fprintf(bypass, "\t• one the best treshold is found, a final encoding will be performed with %s\n", finalEncoder)
		}
		fmt.Fprintf(bypass, "\nEach segment will have to validate the following VMAF profile:\n\n%s\n", vmafAuditor)

		// Build decoder config independently so scene detection can run on the source
		// before the master is created.
		decoderCfg := ffmpeg.SelectDecoderForEncoder(ctx, inputPath, ffmpeg.Encoder(cmd.String(encoderFlagName)),
			cmd.Int(nvidiaGPUIndexFlagName), cmd.String(vaapiRendererPathFlagName), cmd.Int(d3d12vaGPUIndexFlagName),
		)

		// Get source duration for progress reporting
		sourceStats, err := getStreamsInfos(ctx, inputPath, cmd.Bool(debugFlagName))
		if err != nil {
			return fmt.Errorf("failed to get source stream info: %w", err)
		}
		totalDuration := sourceStats.Format.Duration

		// Step 1 - Detect scenes on the source to get candidate thresholds immediately
		fmt.Fprintf(bypass, "Detecting scenes with threshold above %s...\n",
			strconv.FormatFloat(cmd.Float64(thresholdFlagName), 'f', -1, 64),
		)
		start := time.Now()
		scenes, err := liveDetectScenes(ctx, inputPath, cmd.Float64(thresholdFlagName), totalDuration, cmd.Bool(debugFlagName),
			decoderCfg.ToScenesDetectionConfig(),
		)
		if err != nil {
			return fmt.Errorf("failed to detect scenes: %w", err)
		}
		fmt.Fprintf(bypass, "\tDetected %d scenes in %s\n",
			1+len(scenes), time.Since(start).Round(time.Second),
		)
		candidates := getSearchThresholdCandidates(scenes, cmd.Int(searchIncrementMinimumFlagName))
		fmt.Fprintf(bypass, "\tCandidates: %+v\n", candidates)

		// Step 2 - Create the master for encoding
		_, _, _, err = createMaster(ctx, inputPath, workingDir, inputInfos.Size(), cmd.Bool(debugFlagName), decoderCfg.ToFFV1MasterConfig())
		if err != nil {
			return fmt.Errorf("failed to create the master file: %w", err)
		}

		// TODO

		return nil
	},
}

func getSearchThresholdCandidates(scenes []ffmpeg.Scene, increment int) (candidates []float64) {
	candidates = make([]float64, 0, len(scenes))
	// First candidate is the smallest
	var candidate float64
	for index, scene := range scenes {
		if index == 0 || scene.Score < candidate {
			candidate = scene.Score
		}
	}
	candidates = append(candidates, candidate)
	// Compute nexts
	for minimum := candidate + float64(increment); minimum <= float64(ffmpeg.SceneThresholdMax); minimum = candidate + float64(increment) {
		candidate = -1
		for _, scene := range scenes {
			if scene.Score >= minimum && (candidate == -1 || scene.Score < candidate) {
				candidate = scene.Score
			}
		}
		if candidate == -1 {
			return
		}
		candidates = append(candidates, candidate)
	}
	return
}
