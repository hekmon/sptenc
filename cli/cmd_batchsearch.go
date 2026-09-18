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
	finalEncodeFlagName   = "finalencode"
	maxCandidatesFlagName = "maxcandidates"
	maxCandidatesDefault  = 40
	strikesFlagName       = "strikes"
	strikesMinimum        = 3
)

var batchsearchCommand = &cli.Command{
	Name:     "batchsearch",
	Aliases:  []string{"bs"},
	Category: "Advanced",
	Usage:    "Automatically search for the optimal scene threshold by encoding the video multiple times",
	Description: "Orchestrate multiple encode passes with different scene detection thresholds\n" +
		"to find the one that produces the smallest file while still passing your VMAF targets.\n\n" +
		"HOW IT WORKS\n" +
		"  1. Scans the source once to map all natural scene boundaries starting at --" + thresholdFlagName + ".\n" +
		"  2. Automatically selects thresholds so that each one eliminates at least N scenes compared\n" +
		"     to the previous candidate. N is tuned internally so the total number of candidates never\n" +
		"     exceeds your --" + maxCandidatesFlagName + " budget.\n" +
		"  3. Encodes each candidate threshold and tracks resulting file size.\n" +
		"  4. Stops after --" + strikesFlagName + " consecutive candidates fail to reduce file size.\n\n" +
		"WHY THRESHOLD SELECTION MATTERS\n" +
		"Scene detection splits a video into independent segments. Each segment gets its own QP, so\n" +
		"splitting finely lets hard passages use low QP and easy ones high. But every split forces an\n" +
		"I-frame, and short runs starve B/P compression. Split coarsely and B/P frames thrive across\n" +
		"long runs, yet the whole scene must bow to its hardest passage — easy sections pay for quality\n" +
		"they do not need.\n\n" +
		"The sweet spot is a threshold that gives each scene enough freedom to use its own QP while\n" +
		"leaving enough continuous frames for the encoder to compress efficiently. batchsearch finds this\n" +
		"automatically by testing candidates across the spectrum.\n\n" +
		"CONTROLLING SEARCH COST\n" +
		"Each candidate is a full encode pass with VMAF validation. The complete batchsearch process is slow:\n" +
		"  * GPU search: may take several days in total.\n" +
		"  * CPU search: can take several weeks in total.\n\n" +
		"Use --" + maxCandidatesFlagName + " to set your budget. The default (40) is a reasonable balance between\n" +
		"thoroughness and total search time. Lower values (20-30) reduce overall duration but may miss\n" +
		"the optimal threshold. Higher values increase precision at a linear time cost. Internally, the\n" +
		"algorithm tunes the 'scene drop' — how many scene boundaries disappear between two tested\n" +
		"thresholds — to fit within your budget.\n\n" +
		"Use --" + strikesFlagName + " to set how many consecutive candidates must fail to reduce file size\n" +
		"before the search gives up. File size does not decrease monotonically: candidates can sit on a\n" +
		"plateau or even regress slightly before a later threshold yields a significant drop. Strikes acts\n" +
		"as both an early-exit mechanism and a safety buffer — it prevents the search from running forever\n" +
		"once the minimum is passed, while tolerating short noisy plateaus so it does not bail out too soon.\n" +
		"The default (3) is usually enough to ride out temporary regressions without paying for a long tail\n" +
		"of diminishing returns. Lower values make the search more aggressive; higher values increase patience\n" +
		"at the cost of additional full encode passes.\n\n" +
		"ENCODERS\n" +
		"Use --" + encoderFlagName + " to choose the encoder for the search loop. GPU encoders (e.g. hevc_nvenc)\n" +
		"are strongly recommended for speed. If available, also enable CUDA VMAF acceleration (--" + vmafCUDAFlagName + ")\n" +
		"to avoid bottlenecking the search on CPU-side quality validation.\n\n" +
		"FINAL ENCODE\n" +
		"When --" + finalEncodeFlagName + " is set and the search encoder is GPU-based, the command automatically\n" +
		"derives the equivalent CPU encoder of the same codec (e.g. hevc_nvenc -> libx265) and performs\n" +
		"the final encode with the discovered threshold. The GPU-found threshold is usually close enough\n" +
		"for the CPU pass to be worth the speedup, though it may not be exactly optimal.\n\n" +
		"If the search encoder is already CPU-based, --" + finalEncodeFlagName + " is a no-op because the search\n" +
		"result is already the most precise result possible.\n\n" +
		"CACHE ISOLATION\n" +
		"By default all encodes for the same encoder + VMAF profile combo share a single QP history cache.\n" +
		"If you encode content with wildly different visual characteristics (e.g. grainy film vs. clean CGI),\n" +
		"sharing history can pollute the model and slow convergence. Use --" + cacheProfileFlagName + " to create\n" +
		"a separate cache namespace for a specific type of content (e.g. pixar_animation, sopranos_s01, grainy_90s).",
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
				Name:     maxCandidatesFlagName,
				Aliases:  []string{"m"},
				Usage:    "Maximum number of candidate thresholds to test",
				Value:    maxCandidatesDefault,
				OnlyOnce: true,
				Validator: func(v int) error {
					if v < 1 {
						return fmt.Errorf("%s must be at least 1", maxCandidatesFlagName)
					}
					return nil
				},
				Category: "Threshold Search",
			},
			&cli.IntFlag{
				Name:     strikesFlagName,
				Aliases:  []string{"S"},
				Usage:    "Stop the search after this many consecutive thresholds fail to reduce file size",
				Value:    strikesMinimum,
				OnlyOnce: true,
				Validator: func(v int) error {
					if v < strikesMinimum {
						return fmt.Errorf("%s must be %d at minimum", strikesFlagName, strikesMinimum)
					}
					return nil
				},
				Category: "Threshold Search",
			},
			&cli.StringFlag{
				Name:     cacheProfileFlagName,
				Aliases:  []string{"c"},
				Usage:    "Isolate QP history to a named profile",
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
			return fmt.Errorf("failed to create VMAF auditor: %w", err)
		}

		// Get the stats cache
		statsCache, err := core.NewStatsCacheHistory(cmd.String(statsCacheDirFlagName), ffmpeg.Encoder(cmd.String(encoderFlagName)),
			vmafAuditor, cmd.String(cacheProfileFlagName))
		if err != nil {
			return fmt.Errorf("failed to create stats cache: %w", err)
		}
		if cmd.Bool(debugFlagName) {
			fmt.Fprintf(bypass, "DEBUG: Using stats cache at: %s\n", shellescape.Quote(statsCache.GetPath()))
		}

		// final encoding ?
		var finalEncoder ffmpeg.Encoder
		if cmd.Bool(finalEncodeFlagName) {
			cpuEncoder, alreadyCPU := ffmpeg.GetCPURelative(ffmpeg.Encoder(cmd.String(encoderFlagName)))
			if !alreadyCPU && cpuEncoder != "" {
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
		fmt.Fprintf(bypass, "\t• testing at most %d candidate thresholds\n", cmd.Int(maxCandidatesFlagName))
		fmt.Fprintf(bypass, "\t• waiting at least %d strikes before stopping\n", cmd.Int(strikesFlagName))
		if finalEncoder != "" {
			fmt.Fprintf(bypass, "\t• once the best threshold is found, a final encoding will be performed with %s\n", finalEncoder)
		}
		fmt.Fprintf(bypass, "\nEach segment will have to validate the following VMAF profile:\n\n%s\n", vmafAuditor)

		// Build decoder config independently so scene detection can run on the source before the master is created.
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
		candidates, effectiveMinDrop := core.GetOptimalMinDrop(scenes, cmd.Int(maxCandidatesFlagName))
		fmt.Fprintf(bypass, "\tAuto-tuned to scene drop of %d to fit within --%s=%d, producing %d candidates\n",
			effectiveMinDrop, maxCandidatesFlagName, cmd.Int(maxCandidatesFlagName), len(candidates),
		)

		// Step 2 - Create the master for encoding
		_, _, _, err = createMaster(ctx, inputPath, workingDir, inputInfos.Size(), cmd.Bool(debugFlagName), decoderCfg.ToFFV1MasterConfig())
		if err != nil {
			return fmt.Errorf("failed to create the master file: %w", err)
		}

		// Step 3 - start the search

		return nil
	},
}
