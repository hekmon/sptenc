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
	"github.com/hekmon/sptenc/metadata"
	"github.com/hekmon/sptenc/pipeline"

	"al.essio.dev/pkg/shellescape"
	"github.com/fatih/color"
	"github.com/hekmon/cunits/v3"
	"github.com/hekmon/liveprogress/v2"
	"github.com/muesli/termenv"
	"github.com/olekukonko/tablewriter"
	"github.com/olekukonko/tablewriter/tw"
	"github.com/urfave/cli/v3"
)

// Flag names and defaults for batchsearch-specific flags.
const (
	finalEncodeFlagName = "final-encode"
	strikesFlagName     = "strikes"
	strikesMinimum      = 3
)

var (
	batchTableConfig = tablewriter.Config{
		Header: tw.CellConfig{
			Formatting: tw.CellFormatting{
				AutoFormat: tw.Off,
			},
		},
		Row: tw.CellConfig{
			Alignment: tw.CellAlignment{
				PerColumn: []tw.Align{tw.AlignCenter, tw.AlignCenter, tw.AlignCenter},
			},
		},
	}
)

var batchsearchCommand = &cli.Command{
	Name:     "batchsearch",
	Aliases:  []string{"bs"},
	Category: "Advanced",
	Usage:    "Automatically search for the optimal scene threshold by encoding the video multiple times",
	Description: "Orchestrate multiple encode passes with different scene detection thresholds\n" +
		"to find the one that produces the smallest file while still passing your VMAF targets.\n\n" +
		"HOW IT WORKS\n" +
		"  1. Scans the source once to map all natural scene boundaries. Only boundaries with scores\n" +
		"     between --" + minThresholdFlagName + " and --" + maxThresholdFlagName + " are considered.\n" +
		"  2. Builds candidate thresholds from those boundaries. Each candidate must eliminate at least\n" +
		"     N scenes compared to the previous one. N is auto-tuned so the total never exceeds your\n" +
		"     --" + maxCandidatesFlagName + " budget, but it will not go below --" + minDropFlagName + ".\n" +
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
		"PREVIEWING CANDIDATES\n" +
		"Use the thresholds command to preview what candidate thresholds and scene distributions your\n" +
		"chosen range will produce, without running any encodes. It is a fast way to validate that\n" +
		"--" + minThresholdFlagName + ", --" + maxThresholdFlagName + ", --" + maxCandidatesFlagName + ", and --" + minDropFlagName + " are set\n" +
		"sensibly before committing to a long batchsearch run.\n\n" +
		"CONTROLLING SEARCH COST\n" +
		"Each candidate is a full encode pass with VMAF validation. The complete batchsearch process is slow:\n" +
		"  * GPU search: may take several days in total.\n" +
		"  * CPU search: can take several weeks in total.\n\n" +
		"You control the cost with three levers: which boundaries are eligible, how many candidates\n" +
		"are generated from them, and when to give up.\n\n" +
		"THRESHOLD RANGE (--" + minThresholdFlagName + ", --" + maxThresholdFlagName + ")\n" +
		"These define the window of scene scores that can become candidates (defaults: " +
		strconv.FormatFloat(minThresholdDefault, 'f', -1, 64) + "–" + strconv.FormatFloat(maxThresholdDefault, 'f', -1, 64) + ").\n" +
		"A narrow range produces fewer candidates naturally; a wide range gives more opportunity but may\n" +
		"waste encodes on thresholds that merge too many scenes to be useful.\n\n" +
		"CANDIDATE DENSITY (--" + maxCandidatesFlagName + ", --" + minDropFlagName + ")\n" +
		"--" + maxCandidatesFlagName + " sets your absolute budget (default " + strconv.Itoa(maxCandidatesDefault) + "). The algorithm auto-tunes the\n" +
		"'scene drop' — how many boundaries disappear between two tested thresholds — to fit within it.\n" +
		"--" + minDropFlagName + " is a floor for that tuning (default " + strconv.Itoa(minDropDefault) + "). On long content with many\n" +
		"scenes the auto-tuner naturally exceeds this floor, so it has no effect. It mainly protects\n" +
		"shorter content (e.g. OVAs, episodes) from over-sampling: a low mindrop creates candidates that\n" +
		"are very close together, where the file-size differences are often small yet each still costs a\n" +
		"full encode. Raising the floor skips these diminishing returns and focuses the budget on\n" +
		"thresholds that are meaningfully different. Lower it only if you suspect the optimum sits between\n" +
		"two tightly spaced boundaries.\n\n" +
		"EARLY EXIT (--" + strikesFlagName + ")\n" +
		"File size does not decrease monotonically: candidates can sit on a plateau or even regress slightly\n" +
		"before a later threshold yields a significant drop. --" + strikesFlagName + " sets how many consecutive\n" +
		"non-improving candidates the search tolerates before giving up (default " + strconv.Itoa(strikesMinimum) + "). It acts as both an\n" +
		"early-exit mechanism and a safety buffer — preventing the search from running forever once the best\n" +
		"file size has been found, while tolerating short noisy plateaus so it does not bail out too soon.\n" +
		"Lower values make the search more aggressive; higher values increase patience at the cost of\n" +
		"additional full encode passes.\n\n" +
		"ENCODERS\n" +
		"Use --" + encoderFlagName + " to choose the encoder for the search loop. The default is " + string(ffmpeg.HEVCEncoderNVEnc) + "\n" +
		"(GPU-based). GPU encoders are strongly recommended for speed. If available, also enable CUDA\n" +
		"VMAF acceleration (--" + vmafCUDAFlagName + ") to avoid bottlenecking the search on CPU-side\n" +
		"quality validation.\n\n" +
		"CONCURRENT ENCODING\n" +
		"The --" + concurrentSegmentsFlagName + " flag controls how many segments are searched in parallel within\n" +
		"each candidate encode. On consumer hardware (even high-end) CPU encoders already saturate physical\n" +
		"cores with a single segment. With hyperthreading/SMT enabled, 50% of total threads is the effective\n" +
		"ceiling; exceeding it thrashes cache and memory bandwidth without improving throughput.\n" +
		"This option is intended for GPU encoders, which often support multiple parallel sessions.\n" +
		"Hard session limits vary by generation and SKU (typically 1-3 on consumer cards), so verify\n" +
		"your specific GPU's capabilities first. Once confirmed, raising this value is highly encouraged\n" +
		"for GPU-based searches — it can significantly reduce total runtime for a process that is already\n" +
		"long by nature.\n\n" +
		"FINAL ENCODE\n" +
		"When --" + finalEncodeFlagName + " is set and the search encoder is GPU-based, the command automatically\n" +
		"derives the equivalent CPU encoder of the same codec (e.g. hevc_nvenc -> libx265) and performs\n" +
		"the final encode with the discovered threshold. CPU encoders produce smaller files for an equivalent\n" +
		"quality compared to GPU encoders, which are optimized for speed rather than compression efficiency.\n" +
		"The GPU-found threshold is usually close enough for the CPU pass to be worth the speedup,\n" +
		"though it may not be exactly optimal.\n\n" +
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
				Name:             encoderFlagName,
				Aliases:          []string{"e"},
				Usage:            fmt.Sprintf("Encoder to use during the threshold search loop. Valid values: %s", strings.Join(allEncoders, ", ")),
				Value:            string(ffmpeg.HEVCEncoderNVEnc),
				OnlyOnce:         true,
				Validator:        encoderValidator,
				ValidateDefaults: true,
			},
			&cli.BoolFlag{
				Name:     finalEncodeFlagName,
				Aliases:  []string{"f"},
				Usage:    "Run a final CPU encode after GPU search (no-op if search encoder is already CPU).",
				Value:    false,
				OnlyOnce: true,
			},
			&cli.IntFlag{
				Name:      concurrentSegmentsFlagName,
				Aliases:   []string{"C"},
				Usage:     "Number of segments to encode in parallel. Not recommended for CPU encoders, see description.",
				Value:     1,
				OnlyOnce:  true,
				Validator: validateConcurrentSegments,
			},
		}
		flags = append(flags, thresholdSearchFlags()...)
		flags = append(flags,
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
		)
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
		// Check threshold range consistency
		if cmd.Float64(minThresholdFlagName) >= cmd.Float64(maxThresholdFlagName) {
			return ctx, fmt.Errorf("--%s must be strictly less than --%s", minThresholdFlagName, maxThresholdFlagName)
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
			if (err != nil && ctx.Err() != context.Canceled) || cmd.Bool(debugFlagName) {
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
		encoderAdapter := &pipeline.EncoderAdapter{
			Encoder:           ffmpeg.Encoder(cmd.String(encoderFlagName)),
			NVIDIAGPUIndex:    cmd.Int(nvidiaGPUIndexFlagName),
			VAAPIRendererPath: cmd.String(vaapiRendererPathFlagName),
			D3D12VAGPUIndex:   cmd.Int(d3d12vaGPUIndexFlagName),
			VMAFNeg:           cmd.Bool(vmafNegFlagName),
			VMAFCUDA:          cmd.Bool(vmafCUDAFlagName),
		}
		statsCache, err := core.NewStatsCacheHistory(cmd.String(statsCacheDirFlagName), encoderAdapter,
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

		// Validate threshold range
		if cmd.Float64(maxThresholdFlagName) <= cmd.Float64(minThresholdFlagName) {
			return fmt.Errorf("%s must be greater than %s", maxThresholdFlagName, minThresholdFlagName)
		}

		/*
		 * Execute process
		 */
		completeRunStart := time.Now()

		fmt.Fprintf(bypass, "\nStarting batch search encoding of %s (%s) with %s.\n",
			shellescape.Quote(filepath.Base(inputPath)),
			cunits.ImportInBytes(float64(inputInfos.Size())),
			cmd.String(encoderFlagName),
		)
		fmt.Fprintf(bypass, "\t• search range: %s to %s\n",
			strconv.FormatFloat(cmd.Float64(minThresholdFlagName), 'f', -1, 64),
			strconv.FormatFloat(cmd.Float64(maxThresholdFlagName), 'f', -1, 64),
		)
		fmt.Fprintf(bypass, "\t• testing at most %d candidate thresholds\n", cmd.Int(maxCandidatesFlagName))
		fmt.Fprintf(bypass, "\t• waiting at least %d strikes before stopping\n", cmd.Int(strikesFlagName))
		fmt.Fprintf(bypass, "\t• minimum scene drop: %d\n", cmd.Int(minDropFlagName))
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
		videoStream := sourceStats.VideoTrack()
		if videoStream == nil {
			return errors.New("no video stream found in source")
		}
		if !videoStream.IsConstantFrameRate() {
			return errors.New("variable frame rate (VFR) content is not supported: VMAF requires CFR for frame-exact alignment")
		}
		totalDuration := sourceStats.Format.Duration

		// Step 1 - Detect scenes on the source to get candidate thresholds immediately
		fmt.Fprintf(bypass, "Detecting scenes with threshold at %s...\n",
			strconv.FormatFloat(cmd.Float64(minThresholdFlagName), 'f', -1, 64),
		)
		start := time.Now()
		scenes, err := liveDetectScenes(ctx, inputPath, cmd.Float64(minThresholdFlagName), totalDuration, cmd.Bool(debugFlagName),
			decoderCfg.ToScenesDetectionConfig(),
		)
		if err != nil {
			return fmt.Errorf("failed to detect scenes: %w", err)
		}
		// Cap scenes to the max threshold
		cappedScenes := make([]core.Scene, 0, len(scenes))
		for _, scene := range scenes {
			if scene.Score <= cmd.Float64(maxThresholdFlagName) {
				cappedScenes = append(cappedScenes, core.Scene{Start: scene.Start, Score: scene.Score})
			}
		}
		fmt.Fprintf(bypass, "\tDetected %d scenes in %s\n",
			1+len(scenes), time.Since(start).Round(time.Second),
		)
		if len(cappedScenes) < len(scenes) {
			fmt.Fprintf(bypass, "\tCapped to %d scenes with score ≤ %s\n",
				1+len(cappedScenes), strconv.FormatFloat(cmd.Float64(maxThresholdFlagName), 'f', -1, 64),
			)
		}
		candidates, effectiveMinDrop := core.GetOptimalMinDrop(cappedScenes, cmd.Int(maxCandidatesFlagName))
		if len(candidates) == 0 {
			return fmt.Errorf("no candidates found in the %s–%s range",
				strconv.FormatFloat(cmd.Float64(minThresholdFlagName), 'f', -1, 64),
				strconv.FormatFloat(cmd.Float64(maxThresholdFlagName), 'f', -1, 64),
			)
		}
		if effectiveMinDrop < cmd.Int(minDropFlagName) {
			fmt.Fprintf(bypass, "\tWARNING: Auto-tuned scene drop of %d is below the minimum of %d; recomputing candidates...\n",
				effectiveMinDrop, cmd.Int(minDropFlagName),
			)
			candidates = core.GetSearchThresholdCandidates(cappedScenes, cmd.Int(minDropFlagName))
			effectiveMinDrop = cmd.Int(minDropFlagName)
		}
		fmt.Fprintf(bypass, "\tScene drop auto-tuned to %d to stay within %s=%d, producing %d candidates\n",
			effectiveMinDrop, maxCandidatesFlagName, cmd.Int(maxCandidatesFlagName), len(candidates),
		)
		if cmd.Bool(debugFlagName) {
			fmt.Fprintf(bypass, "\tCandidates: %v\n", candidates)
		}

		// Step 2 - Create the master for encoding
		masterFile, sourceTotalFrames, _, err := createMaster(ctx, inputPath, workingDir, inputInfos.Size(), cmd.Bool(debugFlagName), decoderCfg.ToFFV1MasterConfig())
		if err != nil {
			return fmt.Errorf("failed to create the master file: %w", err)
		}

		// Step 3 - start the search
		batch := batchStatus{
			candidates: candidates,
		}
		batch.Start()
		defer batch.Stop()
		for ; batch.currentCandidateIndex < len(batch.candidates); batch.currentCandidateIndex++ {
			// Strike early stop
			if batch.currentCandidateIndex-batch.bestCandidateIndex > cmd.Int(strikesFlagName) {
				fmt.Fprintf(bypass, "\n\nEarly batch search stop: %d strikes reached\n\n", cmd.Int(strikesFlagName))
				break
			}

			// Start testing candidate
			candidateStr := strconv.FormatFloat(batch.candidates[batch.currentCandidateIndex], 'f', -1, 64)
			fmt.Fprintf(bypass, "\n\nTesting threshold candidate %s (%d/%d)\n",
				candidateStr, batch.currentCandidateIndex+1, len(batch.candidates),
			)
			candidateWorkdir := filepath.Join(workingDir, fmt.Sprintf("candidate-%s", candidateStr))
			if err = os.Mkdir(candidateWorkdir, 0750); err != nil {
				return fmt.Errorf("failed to create workdir for candidate %s: %w", candidateStr, err)
			}

			// Step 3.A - Build the filtered scenes list
			runScenes := make([]ffmpeg.Scene, 0, len(scenes))
			for _, scene := range scenes {
				if scene.Score >= batch.candidates[batch.currentCandidateIndex] {
					runScenes = append(runScenes, scene)
				}
			}
			fmt.Fprintf(bypass, "\tWill produce %d scenes\n", 1+len(runScenes))

			// Step 3.B - Split scenes
			fmt.Fprintf(bypass, "Splitting scenes...\n")
			start = time.Now()
			if err = liveSplitScenes(ctx, masterFile, candidateWorkdir, totalDuration, runScenes, cmd.Bool(debugFlagName)); err != nil {
				return fmt.Errorf("failed to split scenes: %w", err)
			}
			fmt.Fprintf(bypass, "\tSplit %d scenes in %v\n",
				1+len(runScenes), time.Since(start).Round(time.Second),
			)
			// Build segment paths directly from known naming convention rather than
			// scanning the directory, which avoids filesystem ordering issues.
			segmentsPaths := make([]string, len(runScenes)+1)
			for i := range segmentsPaths {
				segmentsPaths[i] = filepath.Join(candidateWorkdir, fmt.Sprintf(ffmpeg.SegmentOutputFormat, i))
			}

			// Step 3.C - QP search on this candidate scenes
			batch.results[batch.currentCandidateIndex], batch.encoded[batch.currentCandidateIndex], err = processSegments(
				ctx, segmentsPaths, candidateWorkdir, totalDuration, vmafAuditor, statsCache, encoderAdapter, cmd.Int(concurrentSegmentsFlagName), cmd.Bool(debugFlagName))
			if err != nil {
				return fmt.Errorf("candidate %s: %w", candidateStr, err)
			}

			// Step 3.D - Ending this candidate
			var encodedStats os.FileInfo
			if encodedStats, err = os.Stat(batch.encoded[batch.currentCandidateIndex]); err != nil {
				return fmt.Errorf("failed to stat encoded file %s of candidate %s: %w",
					shellescape.Quote(batch.encoded[batch.currentCandidateIndex]), candidateStr, err,
				)
			}
			batch.sizes[batch.currentCandidateIndex] = encodedStats.Size()
			fmt.Fprintf(bypass, "\nCandidate %s done, weighting %s\n",
				candidateStr, cunits.ImportInBytes(float64(encodedStats.Size())),
			)
			batch.ComputeBest()
			if batch.bestCandidateIndex != 0 && batch.bestCandidateIndex == batch.currentCandidateIndex {
				fmt.Fprintf(bypass, "\tNew best found!\n")
			}
			// Once the video concatened, delete all segments to free up some disk space for next candidate
			//// ffv1 is the one taking the most space
			for _, segmentPath := range segmentsPaths {
				if err = os.Remove(segmentPath); err != nil {
					return fmt.Errorf("failed to delete candidate %s segment %s: %w",
						candidateStr, shellescape.Quote(segmentPath), err,
					)
				}
			}
			//// can reclain additionnal space with encoded segments as well now they are merged
			if !cmd.Bool(debugFlagName) {
				for _, encodedSegmentPath := range batch.results[batch.currentCandidateIndex].EncodedSegmentsPaths {
					if err = os.Remove(encodedSegmentPath); err != nil {
						return fmt.Errorf("failed to delete candidate %s encoded segment %s: %w",
							candidateStr, shellescape.Quote(encodedSegmentPath), err,
						)
					}
				}
			}
		}

		// Step 4 - final encode ? (CPU)
		bestIndex := batch.bestCandidateIndex
		bestCandidateStr := strconv.FormatFloat(batch.candidates[bestIndex], 'f', -1, 64)
		encodedSegmentsMerged := batch.encoded[bestIndex]
		results := batch.results[bestIndex]
		if finalEncoder != "" {
			fmt.Fprintf(bypass, "\n\nBest candidate is %s, running final encode with %s...\n", bestCandidateStr, finalEncoder)
			finalWorkdir := filepath.Join(workingDir, "final-encode")
			if err = os.Mkdir(finalWorkdir, 0750); err != nil {
				return fmt.Errorf("failed to create workdir for final encode: %w", err)
			}

			// Build the filtered scenes list for the best candidate
			bestScenes := make([]ffmpeg.Scene, 0, len(scenes))
			for _, scene := range scenes {
				if scene.Score >= batch.candidates[bestIndex] {
					bestScenes = append(bestScenes, scene)
				}
			}

			// Split scenes
			fmt.Fprintf(bypass, "Splitting scenes for final encode...\n")
			start = time.Now()
			if err = liveSplitScenes(ctx, masterFile, finalWorkdir, totalDuration, bestScenes, cmd.Bool(debugFlagName)); err != nil {
				return fmt.Errorf("failed to split scenes for final encode: %w", err)
			}
			fmt.Fprintf(bypass, "\tSplit %d scenes in %v\n", 1+len(bestScenes), time.Since(start).Round(time.Second))

			// Build segment paths
			finalSegments := make([]string, len(bestScenes)+1)
			for i := range finalSegments {
				finalSegments[i] = filepath.Join(finalWorkdir, fmt.Sprintf(ffmpeg.SegmentOutputFormat, i))
			}

			// Create stats cache for final encoder
			finalEncoderAdapter := &pipeline.EncoderAdapter{
				Encoder:           finalEncoder,
				NVIDIAGPUIndex:    cmd.Int(nvidiaGPUIndexFlagName),
				VAAPIRendererPath: cmd.String(vaapiRendererPathFlagName),
				D3D12VAGPUIndex:   cmd.Int(d3d12vaGPUIndexFlagName),
				VMAFNeg:           cmd.Bool(vmafNegFlagName),
				VMAFCUDA:          cmd.Bool(vmafCUDAFlagName),
			}
			finalStatsCache, err := core.NewStatsCacheHistory(cmd.String(statsCacheDirFlagName), finalEncoderAdapter,
				vmafAuditor, cmd.String(cacheProfileFlagName))
			if err != nil {
				return fmt.Errorf("failed to create stats cache for final encoder: %w", err)
			}

			// Run QP search with final encoder
			results, encodedSegmentsMerged, err = processSegments(ctx, finalSegments, finalWorkdir, totalDuration,
				vmafAuditor, finalStatsCache, finalEncoderAdapter, cmd.Int(concurrentSegmentsFlagName), cmd.Bool(debugFlagName))
			if err != nil {
				return fmt.Errorf("final encode with %s: %w", finalEncoder, err)
			}

			// Clean up final segments to free disk space
			for _, segmentPath := range finalSegments {
				if err = os.Remove(segmentPath); err != nil {
					fmt.Fprintf(bypass, "WARNING: failed to delete final encode segment %s: %s\n", shellescape.Quote(segmentPath), err)
				}
			}
		} else {
			fmt.Fprintf(bypass, "\n\nBest candidate is %s\n", bestCandidateStr)
		}

		// Step 5 - compute final VMAF
		vmafSource := inputPath
		fmt.Fprintln(bypass, "Verifying frame counts for final VMAF...")
		var sourceFrames, encodedFrames int
		// Reuse the exact frame count from createMaster instead of re-probing the source.
		sourceFrames = sourceTotalFrames
		var encodedFileInfo os.FileInfo
		encodedFileInfo, err = os.Stat(encodedSegmentsMerged)
		if err != nil {
			err = fmt.Errorf("could not stat encoded output for frame count verification: %w", err)
			return
		}
		encodedFrames, _, _, err = liveCountNbFrames(ctx, encodedSegmentsMerged, encodedFileInfo.Size(), cmd.Bool(debugFlagName))
		if err != nil {
			err = fmt.Errorf("could not count frames in encoded output for verification: %w", err)
			return
		}
		if cmd.Bool(debugFlagName) {
			fmt.Fprintf(bypass, "DEBUG: Frame counts — source: %d, encoded: %d\n", sourceFrames, encodedFrames)
		}
		if sourceFrames != encodedFrames {
			err = fmt.Errorf("frame count mismatch: source has %d frames but encoded output has %d frames. This will cause VMAF misalignment", sourceFrames, encodedFrames)
			return
		}
		fmt.Fprintln(bypass, "Computing final VMAF...")
		start = time.Now()
		finalVMAFreport, err := liveFinalVMAF(ctx, vmafSource, encodedSegmentsMerged, sourceStats.VideoTrack(),
			results.TotalSegmentsFrames, cmd.Int(nvidiaGPUIndexFlagName), cmd.Bool(vmafNegFlagName), cmd.Bool(vmafCUDAFlagName), cmd.Bool(debugFlagName),
		)
		if err != nil {
			err = fmt.Errorf("failed to compute final vmaf: %w", err)
			return
		}
		duration := time.Since(start)
		finalVMAFStats := finalVMAFreport.GetStats()
		fmt.Fprintf(bypass, "\tFinal VMAF computed in %s:\n\n%s\n", duration.Round(time.Second), finalVMAFStats)

		// Step 6 - remuxing
		fmt.Fprintln(bypass, "Remuxing to final file...")
		outputDir := cmd.String(outputDirFlagName)
		if outputDir == "" {
			outputDir = filepath.Dir(inputPath)
		}
		usedEncoder := ffmpeg.Encoder(cmd.String(encoderFlagName))
		if finalEncoder != "" {
			usedEncoder = finalEncoder
		}
		outputPath := computeFinalPath(inputPath, outputDir, usedEncoder)
		var encodeToFlac bool
		if originalStats, err := getStreamsInfos(ctx, inputPath, cmd.Bool(debugFlagName)); err == nil {
			encodeToFlac = AllAudioTracksPCM(originalStats)
		} else {
			fmt.Fprintf(bypass, "WARNING: failed to probe original file for audio, skipping FLAC check: %s\n", err)
		}
		if encodeToFlac {
			fmt.Fprintf(bypass, "\tAll audio tracks are PCM, encoding to FLAC during video remuxing.\n")
		}
		videoStream = sourceStats.VideoTrack()
		tags := metadata.GenerateTags(*sourceStats.Format, vmafAuditor, usedEncoder,
			results, finalVMAFStats, cmd.Bool(vmafNegFlagName), videoStream.Height >= ffmpeg.Height4K, len(results.EncodedSegmentsPaths))
		start = time.Now()
		if err = liveRemuxSwapVideo(ctx, inputPath, encodedSegmentsMerged, outputPath, encodeToFlac, tags,
			totalDuration, cmd.Bool(debugFlagName)); err != nil {
			return fmt.Errorf("failed to remux encoded video with original file: %w", err)
		}
		duration = time.Since(start)
		fmt.Fprintf(bypass, "\tRemuxed to final file %s in %s\n", shellescape.Quote(outputPath), duration.Round(time.Second))
		finalFileSize, err := getFileSize(outputPath)
		if err != nil {
			fmt.Fprintf(bypass, "WARNING: failed to get final file size: %s\n", err)
		} else {
			originalSize, err := getFileSize(vmafSource)
			if err != nil {
				fmt.Fprintf(bypass, "WARNING: failed to get source video size: %s\n", err)
			} else {
				sizeReduction := originalSize - finalFileSize
				compressionRatio := float64(sizeReduction) / float64(originalSize) * 100
				fmt.Fprintf(bypass, "\tCompression: %s -> %s (%s reduction, %s saved)\n",
					cunits.ImportInBytes(float64(originalSize)), cunits.ImportInBytes(float64(finalFileSize)),
					formatPercent(compressionRatio), cunits.ImportInBytes(float64(sizeReduction)),
				)
			}
		}

		// Regenerate MKV statistics tags
		fmt.Fprintln(bypass, "Regenerating MKV statistics tags...")
		start = time.Now()
		if err = liveGenerateMKVStats(ctx, outputPath, cmd.Bool(debugFlagName)); err != nil {
			err = fmt.Errorf("failed to regenerate MKV statistics tags: %w", err)
			return
		}
		duration = time.Since(start)
		fmt.Fprintf(bypass, "\tMKV statistics tags regenerated in %s\n", duration.Round(time.Second))

		// Verify container-level color metadata
		verifyColorMetadata(ctx, outputPath, videoStream, cmd.Bool(debugFlagName))

		// Step 7 - done
		fmt.Fprintln(bypass)
		var buff strings.Builder
		table := tablewriter.NewTable(&buff,
			tablewriter.WithConfig(batchTableConfig),
		)
		table.Header("Threshold", "Size", "Relative to Best")
		bestSize := batch.sizes[bestIndex]
		bold := color.New(color.Bold)
		for i, candidate := range batch.candidates {
			if batch.sizes[i] == 0 {
				continue // not tested (early stop)
			}
			thresholdStr := strconv.FormatFloat(candidate, 'f', -1, 64)
			sizeStr := fmt.Sprint(cunits.ImportInBytes(float64(batch.sizes[i])))
			relativeStr := formatPercent(float64(batch.sizes[i]) / float64(bestSize) * 100)
			if i == bestIndex {
				thresholdStr = bold.Sprint(thresholdStr)
				sizeStr = bold.Sprint(sizeStr)
				relativeStr = bold.Sprint(relativeStr)
			}
			table.Append([]string{thresholdStr, sizeStr, relativeStr})
		}
		table.Render()
		fmt.Fprint(bypass, buff.String())
		plateauToBest := computePlateauToBest(batch.sizes, bestIndex)
		sufficientStrikes := plateauToBest
		if bestIndex > 0 {
			sufficientStrikes++
		}
		if batch.currentCandidateIndex < len(batch.candidates) {
			fmt.Fprintf(bypass, "Search stopped after %d strikes.\n", cmd.Int(strikesFlagName))
			if plateauToBest == 0 {
				fmt.Fprintf(bypass, "\tBest among tested candidates was found immediately (no plateau).\n")
			} else {
				fmt.Fprintf(bypass, "\tBest among tested candidates was found after a plateau of %d non-improving candidate(s).\n", plateauToBest)
			}
			fmt.Fprintf(bypass, "\tA better threshold may exist among the untested ones.\n")
		} else {
			if plateauToBest == 0 {
				fmt.Fprintf(bypass, "Best candidate found immediately (no plateau).\n")
			} else {
				fmt.Fprintf(bypass, "Best candidate found after a plateau of %d non-improving candidate(s).\n", plateauToBest)
			}
			fmt.Fprintf(bypass, "\tA strikes value of %d would have been sufficient for this file.\n", sufficientStrikes)
			if sufficientStrikes < strikesMinimum {
				fmt.Fprintf(bypass, "\tWarning: while a lower strikes value works for this file, setting it below %d globally is dangerous — the search may stop at a suboptimal threshold.\n", strikesMinimum)
			}
		}
		fmt.Fprintf(bypass, "\nBatch search ended in %s\n\n", time.Since(completeRunStart).Round(time.Second))
		return nil
	},
}

type batchStatus struct {
	candidates            []float64
	currentCandidateIndex int // read by liveprogress render goroutine, written by main loop: see comment below
	bestCandidateIndex    int // read by liveprogress render goroutine, written by main loop: see comment below
	// results
	encoded []string
	sizes   []int64
	results []core.QPSearchResults
	// line formating
	currentCandidateStyle termenv.Style
	bestCandidateStyle    termenv.Style
	testedCandidateStyle  termenv.Style
	futureCandidateStyle  termenv.Style
	// liveprogress
	candidatesLine *liveprogress.CustomLine
	progressBar    *liveprogress.Bar
	separatorLine  *liveprogress.CustomLine
}

// Data race on currentCandidateIndex / bestCandidateIndex:
// These fields are read by the liveprogress render goroutine and written by the
// main loop without synchronization. This is intentional: the worst case is a
// briefly stale UI frame (100 ms), which is invisible to the human eye and does
// not affect algorithmic correctness or results. Fixing it with atomics or a
// mutex would add noise for zero functional benefit. If you run `go test -race`
// on this package, this will be flagged — that is expected and harmless.

func (bs *batchStatus) Start() {
	// Prepare results
	bs.encoded = make([]string, len(bs.candidates))
	bs.sizes = make([]int64, len(bs.candidates))
	bs.results = make([]core.QPSearchResults, len(bs.candidates))
	// Init styles
	termenvProfile := liveprogress.GetTermProfile()
	bs.currentCandidateStyle = termenvProfile.String().Underline()
	bs.bestCandidateStyle = termenvProfile.String().Bold()
	bs.testedCandidateStyle = termenvProfile.String().CrossOut()
	bs.futureCandidateStyle = termenvProfile.String().Faint()
	// Plug to liveprogress
	bs.candidatesLine = liveprogress.AddCustomLine(bs.line)
	bs.progressBar = liveprogress.AddBar(
		liveprogress.WithTotal(uint64(len(bs.candidates))),
		liveprogress.WithMultiplyRunes(),
		// liveprogress.WithWidth(barsWidth),
		liveprogress.WithSameAutoSizeInternalPadding(true, false),
		liveprogress.WithPrependDecorator(func(bar *liveprogress.Bar) string {
			// piggyback state updates on the render ticker so the bar stays in
			// sync on every redraw without needing a separate push loop.
			bar.CurrentSet(uint64(bs.currentCandidateIndex))
			return "    Batches | "
		}),
		liveprogress.WithPrependTimeElapsed(liveprogress.BaseStyle()),
		liveprogress.WithAppendPercent(liveprogress.BaseStyle()),
		liveprogress.WithAppendTimeRemaining(liveprogress.BaseStyle()),
		liveprogress.WithAppendDecorator(func(bar *liveprogress.Bar) string {
			return fmt.Sprintf(" left | %d/%d candidates done",
				bar.Current(), bar.Total(),
			)
		}),
	)
	bs.separatorLine = liveprogress.AddCustomLine(func() string { return "" })
}

func (bs *batchStatus) ComputeBest() {
	for index, size := range bs.sizes {
		if size == 0 {
			// sizes are filled sequentially; first zero means nothing more to compare
			return
		}
		if size < bs.sizes[bs.bestCandidateIndex] {
			bs.bestCandidateIndex = index
		}
	}
}

func (bs *batchStatus) Stop() {
	liveprogress.RemoveCustomLine(bs.separatorLine)
	liveprogress.RemoveBar(bs.progressBar)
	liveprogress.RemoveCustomLine(bs.candidatesLine)
}

func (bs *batchStatus) line() string {
	candidates := make([]string, len(bs.candidates))
	for i, candidate := range bs.candidates {
		switch {
		case i == bs.currentCandidateIndex:
			candidates[i] = bs.currentCandidateStyle.Styled(strconv.FormatFloat(candidate, 'f', -1, 64))
		case i == bs.bestCandidateIndex:
			candidates[i] = bs.bestCandidateStyle.Styled(strconv.FormatFloat(candidate, 'f', -1, 64))
		case i < bs.currentCandidateIndex:
			candidates[i] = bs.testedCandidateStyle.Styled(strconv.FormatFloat(candidate, 'f', -1, 64))
		default:
			candidates[i] = bs.futureCandidateStyle.Styled(strconv.FormatFloat(candidate, 'f', -1, 64))
		}
	}
	return fmt.Sprintf(" Candidates | %s", strings.Join(candidates, " "))
}

// computePlateauToBest returns the longest run of consecutive non-improving
// candidates that occurred before reaching bestIndex. The minimum strikes value
// that would have been sufficient to reach bestIndex is this return value plus
// one (except when bestIndex is 0, where 0 is already sufficient).
func computePlateauToBest(sizes []int64, bestIndex int) int {
	minStrikes := 0
	bestSoFar := 0
	for i := 0; i <= bestIndex; i++ {
		if sizes[i] == 0 {
			continue
		}
		if sizes[i] < sizes[bestSoFar] {
			bestSoFar = i
		}
		if diff := i - bestSoFar; diff > minStrikes {
			minStrikes = diff
		}
	}
	return minStrikes
}
