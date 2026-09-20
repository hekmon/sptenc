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
	"github.com/hekmon/cunits/v3"
	"github.com/hekmon/liveprogress/v2"
	"github.com/urfave/cli/v3"
)

// Flag names for encode-specific flags and shared helpers defined in this file.
const (
	encoderFlagName      = "encoder"
	cacheProfileFlagName = "cache-profile"
	originalFileFlagName = "original-file"
)

var encodeCommand = &cli.Command{
	Name:    "encode",
	Aliases: []string{"e"},
	Usage:   "Encode video segments to meet perceptual quality targets at minimal file size",
	Description: fmt.Sprintf(
		"INPUT\n"+
			"The input path can be provided in two forms:\n"+
			"  * Single video file: sptenc creates a lossless FFV1 master, splits it into scene-aligned\n"+
			"    segments, and encodes each one (one-shot process).\n"+
			"  * Directory of pre-split video files: every file is treated as an already-segmented scene.\n"+
			"    Files are processed in alphabetical order — name them accordingly (e.g. seg_01.mkv,\n"+
			"    seg_02.mkv) to preserve scene order. All files must have the same codec and frame rate.\n\n"+
			"SCENE DETECTION\n"+
			"Use the split command to preview scene boundaries for a given threshold, the thresholds\n"+
			"command to inspect candidate thresholds and their scene-distribution statistics (scenes count,\n"+
			"longest segment, std dev, mean, shortest segment) so you can pick a single threshold to feed\n"+
			"into encode, or batchsearch to find the optimal threshold automatically.\n"+
			"VMAF METRICS\n"+
			"Each VMAF metric flag sets the minimum acceptable VMAF score (%d-%d). If a segment falls\n"+
			"below any enabled threshold, it is re-encoded at a lower QP. Set a value to %d to disable\n"+
			"that metric.\n\n"+
			"STATS CACHE\n"+
			"The cache records QP search statistics to speed up future encodes with the same encoder\n"+
			"and VMAF profile. Different content types (clean animation vs grainy film) need very\n"+
			"different QP distributions, so mixing them slows convergence. Use --"+cacheProfileFlagName+" to keep\n"+
			"these histories separate.\n\n"+
			"ENCODERS\n"+
			"Use GPU encoders for quick VMAF profile testing, but prefer CPU encoders for the final\n"+
			"encode to get the smallest file size. Run 'sptenc verify' to see which encoders are\n"+
			"available on your system.\n\n"+
			"AUDIO\n"+
			"If all audio tracks are PCM (e.g. from Blu-ray remuxes), they are automatically compressed\n"+
			"to FLAC during the final remux step. This reduces file size with no quality loss.",
		core.VMAFMinValue, core.VMAFMaxValue, core.VMAFOffValue),
	Flags: func() (flags []cli.Flag) {
		flags = []cli.Flag{
			&cli.StringFlag{
				Name:             encoderFlagName,
				Aliases:          []string{"e"},
				Usage:            fmt.Sprintf("Encoder to use. Valid values: %s", strings.Join(allEncoders, ", ")),
				Value:            string(ffmpeg.HEVCEncoderLibx265),
				OnlyOnce:         true,
				Validator:        encoderValidator,
				ValidateDefaults: true,
			},
			&cli.Float64Flag{
				Name:    minThresholdFlagName,
				Aliases: []string{"T"},
				Usage: fmt.Sprintf("Scene detection threshold for splitting video (%d-%d)",
					ffmpeg.SceneThresholdMin, ffmpeg.SceneThresholdMax,
				),
				Value:     minThresholdDefault,
				OnlyOnce:  true,
				Category:  "Single Video File",
				Validator: validateSceneThreshold,
			},
			&cli.StringFlag{
				Name:     originalFileFlagName,
				Aliases:  []string{"f"},
				Usage:    "Original media file for remuxing to recover audio, subtitles, etc.",
				Value:    "",
				OnlyOnce: true,
				Category: "Pre-Split Video Files",
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
		flags = append(flags, newDirectoryFlags(true)...)
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
		// Input path argument
		if cmd.Args().Len() != 1 {
			return ctx, errors.New("only one input file is required")
		}
		fileInfos, err := os.Stat(cmd.Args().First()) // args are not parsed yet, can not use cmd.StringArg("inputpath")
		if err != nil {
			return ctx, fmt.Errorf("failed to access input file: %w", err)
		}
		ctx = context.WithValue(ctx, inputFileInfosCtxKey, fileInfos)
		inputIsDir := fileInfos.IsDir()
		if !inputIsDir {
			if !fileInfos.Mode().IsRegular() {
				return ctx, errors.New("input path must be a directory or a regular file")
			}
			ctx = context.WithValue(ctx, inputFileSizeCtxKey, fileInfos.Size())
			// --original-file is only meaningful with directory input; reject it for single files
			if cmd.String(originalFileFlagName) != "" {
				return ctx, errors.New("--original-file can not be used when input path is a single file")
			}
		} else {
			// Directory input: need original file for remuxing audio/subs
			if cmd.String(originalFileFlagName) == "" {
				return ctx, errors.New("when input path is a directory, you must specify the --original-file flag")
			}
			if fileInfos, err = os.Stat(cmd.String(originalFileFlagName)); err != nil {
				return ctx, fmt.Errorf("failed to access original file: %w", err)
			}
			if !fileInfos.Mode().IsRegular() {
				return ctx, errors.New("original file must be a regular file")
			}
			ctx = context.WithValue(ctx, inputFileSizeCtxKey, fileInfos.Size())
		}
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
			err = fmt.Errorf("failed to create VMAF auditor: %w", err)
			return
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
		statsCache, err := core.NewStatsCacheHistory(cmd.String(statsCacheDirFlagName), encoderAdapter, vmafAuditor, cmd.String(cacheProfileFlagName))
		if err != nil {
			err = fmt.Errorf("failed to create stats cache: %w", err)
			return
		}
		if cmd.Bool(debugFlagName) {
			fmt.Fprintf(bypass, "DEBUG: Using stats cache at: %s\n", shellescape.Quote(statsCache.GetPath()))
		}

		/*
		 * Execute process
		 */

		globalStart := time.Now()

		// Step 1 - Segments and media infos
		var (
			segmentsPaths []string
			sourceStats   ffmpeg.FFProbeStats
			totalDuration time.Duration
		)
		if !inputInfos.IsDir() {
			fmt.Fprintf(bypass, "\nStarting split encoding of %s (%s) with %s.\n",
				shellescape.Quote(filepath.Base(inputPath)),
				cunits.ImportInBytes(float64(inputInfos.Size())),
				cmd.String(encoderFlagName),
			)
			fmt.Fprintf(bypass, "Each segment will have to validate the following VMAF profile:\n\n%s\n", vmafAuditor)
			// create master
			var masterFile string
			masterConfig := ffmpeg.SelectDecoderForEncoder(ctx, inputPath, ffmpeg.Encoder(cmd.String(encoderFlagName)),
				cmd.Int(nvidiaGPUIndexFlagName), cmd.String(vaapiRendererPathFlagName), cmd.Int(d3d12vaGPUIndexFlagName)).ToFFV1MasterConfig()
			if masterFile, _, totalDuration, err = createMaster(ctx, inputPath, workingDir, ctx.Value(inputFileSizeCtxKey).(int64), cmd.Bool(debugFlagName), masterConfig); err != nil {
				return fmt.Errorf("failed to create the master file: %w", err)
			}
			// analyze
			fmt.Fprintf(bypass, "Detecting scenes with threshold at %s...\n",
				strconv.FormatFloat(cmd.Float64(minThresholdFlagName), 'f', -1, 64),
			)
			var scenes []ffmpeg.Scene
			start := time.Now()
			scenesConfig := ffmpeg.ScenesDetectionConfig{
				NVDec:           masterConfig.NVDec,
				NVDevice:        masterConfig.NVDevice,
				VAAPIDec:        masterConfig.VAAPIDec,
				VAAPIDevice:     masterConfig.VAAPIDevice,
				D3D12Dec:        masterConfig.D3D12Dec,
				D3D12Device:     masterConfig.D3D12Device,
				VideoToolboxDec: masterConfig.VideoToolboxDec,
			}
			if scenes, err = liveDetectScenes(ctx, masterFile, cmd.Float64(minThresholdFlagName), totalDuration,
				cmd.Bool(debugFlagName), scenesConfig); err != nil {
				return fmt.Errorf("failed to detect scenes: %w", err)
			}
			fmt.Fprintf(bypass, "\tDetected %d scenes in %s\n",
				1+len(scenes), time.Since(start).Round(time.Second),
			)
			// split
			fmt.Fprintf(bypass, "Splitting scenes...\n")
			start = time.Now()
			if err = liveSplitScenes(ctx, masterFile, workingDir, totalDuration, scenes, cmd.Bool(debugFlagName)); err != nil {
				return fmt.Errorf("failed to split scenes: %w", err)
			}
			fmt.Fprintf(bypass, "\tSplit %d scenes in %v\n",
				1+len(scenes), time.Since(start).Round(time.Second),
			)
			// Build segment paths directly from known naming convention rather than
			// scanning the directory, which avoids filesystem ordering issues.
			segmentsPaths = make([]string, len(scenes)+1)
			for i := range segmentsPaths {
				segmentsPaths[i] = filepath.Join(workingDir, fmt.Sprintf(ffmpeg.SegmentOutputFormat, i))
			}
			// get stream infos
			if sourceStats, err = getStreamsInfos(ctx, inputPath, cmd.Bool(debugFlagName)); err != nil {
				return fmt.Errorf("failed to probe input file: %w", err)
			}
		} else {
			fmt.Fprintf(bypass, "\nStarting split encoding of already splitted video files within %s\n\t(source: %s (%s)) with %s.\n",
				shellescape.Quote(filepath.Base(inputPath)),
				shellescape.Quote(filepath.Base(cmd.String(originalFileFlagName))),
				cunits.ImportInBytes(float64(ctx.Value(inputFileSizeCtxKey).(int64))),
				cmd.String(encoderFlagName),
			)
			fmt.Fprintf(bypass, "Each segment will have to validate the following VMAF profile:\n\n%s\n", vmafAuditor)
			// get segments
			if segmentsPaths, err = getSegmentsFromDir(inputPath); err != nil {
				err = fmt.Errorf("Failed to get segments from directory: %w", err)
				return
			}
			if len(segmentsPaths) == 0 {
				err = fmt.Errorf("No segment files found in input directory")
				return
			}
			fmt.Fprintf(bypass, "Found %d segments in directory\n", len(segmentsPaths))
			// get stream infos (from first segment)
			if sourceStats, err = getStreamsInfos(ctx, segmentsPaths[0], cmd.Bool(debugFlagName)); err != nil {
				err = fmt.Errorf("Failed to probe segment file: %w", err)
				return
			}
			// Calculate total duration of all segments for accurate progress bar
			fmt.Fprintln(bypass, "Calculating total duration of segments...")
			if totalDuration, err = getSegmentsTotalDuration(ctx, segmentsPaths, cmd.Bool(debugFlagName)); err != nil {
				err = fmt.Errorf("Failed to calculate total duration: %w", err)
				return
			}
			fmt.Fprintf(bypass, "\tTotal duration of segments: %s\n", totalDuration)
		}
		// Validate video stream presence and reject VFR content
		videoStream := sourceStats.VideoTrack()
		if videoStream == nil {
			err = errors.New("no video stream found in source")
			return
		}
		if !videoStream.IsConstantFrameRate() {
			err = errors.New("variable frame rate (VFR) content is not supported: VMAF requires CFR for frame-exact alignment")
			return
		}

		// Step 2 - Process segments
		results, encodedSegmentsMerged, err := processSegments(ctx, segmentsPaths, workingDir, totalDuration,
			vmafAuditor, statsCache, encoderAdapter, cmd.Bool(debugFlagName))
		if err != nil {
			err = fmt.Errorf("failed to encode segments: %w", err)
			return
		}

		// Step 3 - Prepare source for VMAF if necessary
		var (
			start      time.Time
			duration   time.Duration
			vmafSource string
		)
		if inputInfos.IsDir() {
			fmt.Fprintln(bypass, "Merging source segments...")
			vmafSource = filepath.Join(workingDir, "source_segments_merged.mkv")
			start = time.Now()
			// Here we use results.TotalSegmentsFrames because all segments frames number have been checked against source in QP search
			if err = liveConcat(ctx, workingDir, vmafSource, segmentsPaths, results.TotalSegmentsFrames, cmd.Bool(debugFlagName)); err != nil {
				err = fmt.Errorf("failed to concat source segments: %w", err)
				return
			}
			duration = time.Since(start)
			fmt.Fprintf(bypass, "Source segments merged in %s.\n", duration.Round(time.Second))
		} else {
			vmafSource = cmd.StringArg("inputpath")
		}

		// Step 4 - final vmaf check
		// Verify frame counts match before computing VMAF to catch misalignment early.
		// Use -count_frames (NbReadFrames) via liveCountNbFrames for accurate counts.
		fmt.Fprintln(bypass, "Verifying frame counts for final VMAF...")
		var sourceFrames, encodedFrames int
		sourceFileInfo, err := os.Stat(vmafSource)
		if err != nil {
			fmt.Fprintf(bypass, "WARNING: could not stat source for frame count verification: %s\n", err)
		} else {
			sourceFrames, _, _, err = liveCountNbFrames(ctx, vmafSource, sourceFileInfo.Size(), cmd.Bool(debugFlagName))
			if err != nil {
				fmt.Fprintf(bypass, "WARNING: could not count frames in source for verification: %s\n", err)
				sourceFrames = 0
			}
		}
		encodedFileInfo, err := os.Stat(encodedSegmentsMerged)
		if err != nil {
			fmt.Fprintf(bypass, "WARNING: could not stat encoded output for frame count verification: %s\n", err)
		} else {
			encodedFrames, _, _, err = liveCountNbFrames(ctx, encodedSegmentsMerged, encodedFileInfo.Size(), cmd.Bool(debugFlagName))
			if err != nil {
				fmt.Fprintf(bypass, "WARNING: could not count frames in encoded output for verification: %s\n", err)
				encodedFrames = 0
			}
		}
		if cmd.Bool(debugFlagName) {
			fmt.Fprintf(bypass, "DEBUG: Frame counts — source: %d, encoded: %d\n", sourceFrames, encodedFrames)
		}
		if sourceFrames > 0 && encodedFrames > 0 && sourceFrames != encodedFrames {
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
		duration = time.Since(start)
		finalVMAFStats := finalVMAFreport.GetStats()
		fmt.Fprintf(bypass, "\tFinal VMAF computed in %s:\n%s", duration.Round(time.Second), finalVMAFStats)

		// Step 5 - remux final file
		fmt.Fprintln(bypass, "Remuxing to final file...")
		var originalFile string
		if inputInfos.IsDir() {
			originalFile = cmd.String(originalFileFlagName)
		} else {
			originalFile = inputPath
		}
		outputDir := cmd.String(outputDirFlagName)
		if outputDir == "" {
			if inputInfos.IsDir() {
				outputDir = filepath.Dir(cmd.String(originalFileFlagName))
			} else {
				outputDir = filepath.Dir(inputPath)
			}
		}
		outputPath := computeFinalPath(originalFile, outputDir, ffmpeg.Encoder(cmd.String(encoderFlagName)))
		// Determine whether to auto-convert audio to FLAC.
		// sourceStats was probed from either the input file (single file) or the first segment (directory).
		// Probe originalFile directly to get the correct audio stream info in both cases.
		var encodeToFlac bool
		if originalStats, err := getStreamsInfos(ctx, originalFile, cmd.Bool(debugFlagName)); err == nil {
			encodeToFlac = AllAudioTracksPCM(originalStats)
		} else {
			fmt.Fprintf(bypass, "WARNING: failed to probe original file for audio, skipping FLAC check: %s\n", err)
		}
		if encodeToFlac {
			fmt.Fprintf(bypass, "\tAll audio tracks are PCM, encoding to FLAC during video remuxing.\n")
		}
		tags := metadata.GenerateTags(*sourceStats.Format, vmafAuditor, ffmpeg.Encoder(cmd.String(encoderFlagName)),
			results, finalVMAFStats, cmd.Bool(vmafNegFlagName), videoStream.Height >= ffmpeg.Height4K, len(segmentsPaths))
		start = time.Now()
		if err = liveRemuxSwapVideo(ctx, originalFile, encodedSegmentsMerged, outputPath, encodeToFlac, tags,
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
				if inputInfos.IsDir() {
					fmt.Fprintf(bypass, "\tCompression (source video from segments vs final remux incl. audio/subtitles): %s -> %s (%s change, %s)\n",
						cunits.ImportInBytes(float64(originalSize)), cunits.ImportInBytes(float64(finalFileSize)),
						formatPercent(compressionRatio), cunits.ImportInBytes(float64(sizeReduction)),
					)
				} else {
					fmt.Fprintf(bypass, "\tCompression: %s -> %s (%s reduction, %s saved)\n",
						cunits.ImportInBytes(float64(originalSize)), cunits.ImportInBytes(float64(finalFileSize)),
						formatPercent(compressionRatio), cunits.ImportInBytes(float64(sizeReduction)),
					)
				}
			}
		}

		// Step 6 - regen mkv stats
		fmt.Fprintln(bypass, "Regenerating MKV statistics tags...")
		start = time.Now()
		if err = liveGenerateMKVStats(ctx, outputPath, cmd.Bool(debugFlagName)); err != nil {
			err = fmt.Errorf("failed to regenerate MKV statistics tags: %w", err)
			return
		}
		duration = time.Since(start)
		fmt.Fprintf(bypass, "\tMKV statistics tags regenerated in %s\n", duration.Round(time.Second))
		// Verify container-level color metadata was propagated correctly.
		// Bitstream-level HDR SEIs (mastering display, content light level)
		// are the encoder's responsibility and are verified implicitly by
		// the -c:v copy remux; this check only validates the Matroska
		// Colour elements we explicitly injected.
		// If this fails (e.g. due to an ffmpeg muxer regression), a future
		// fallback could use mkvpropedit to fix the container tags.
		verifyColorMetadata(ctx, outputPath, videoStream, cmd.Bool(debugFlagName))

		// Done
		fmt.Fprintf(bypass, "Complete split encoding took %s\n", time.Since(globalStart).Round(time.Second))
		return
	},
}

// processSegments runs QP search on the given segments and concatenates the encoded results.
func processSegments(ctx context.Context, segmentsPaths []string, workingDir string, totalDuration time.Duration,
	vmafAuditor core.VMAFChecker, statsCache *core.StatsCacheHistory, encoder core.SegmentEncoder, debug bool) (results core.QPSearchResults, encodedSegmentsMerged string, err error) {
	bypass := liveprogress.Bypass()
	fmt.Fprintln(bypass, "Finding optimal QP for each segment...")
	lqps := &LiveQPSearch{
		PrintDebug: debug,
	}
	lqps.Start(len(segmentsPaths), totalDuration)
	start := time.Now()
	results, err = core.FindAllSegmentsQP(ctx, lqps,
		core.QPSearchConfig{
			SegmentsPaths: segmentsPaths,
			Auditor:       vmafAuditor,
			WorkingDir:    workingDir,
			StatsCache:    statsCache,
			KeepInvalidQP: debug,
			Encoder:       encoder,
		},
	)
	if err != nil {
		lqps.Stop()
		err = fmt.Errorf("failed to encode segments: %w", err)
		return
	}
	duration := time.Since(start)
	lqps.Stop()
	// Print stats
	if debug {
		fmt.Fprintf(bypass, "DEBUG: Segments QPs: %+v\n", results.QPs)
	}
	minQP, maxQP := results.GetMinMaxQPs()
	fmt.Fprintf(bypass, "\t***\n\tSegments QP range: [%d,%d]\n", minQP, maxQP)
	if results.NbBestEfforts > 0 {
		if results.NbBestEfforts == 1 {
			fmt.Fprintln(bypass, "WARNING: 1 segment was encoded with best effort, stopping at QP 0 but not validating VMAF config. Please check the logs.")
		} else {
			fmt.Fprintf(bypass, "WARNING: %d segments were encoded with best effort, stopping at QP 0 but not validating VMAF config. Please check the logs.\n",
				results.NbBestEfforts,
			)
		}
	}
	segmentQPmean, segmentQPstddev, err := statsCache.AddRun(results.QPs)
	if err != nil {
		fmt.Fprintf(bypass, "ERROR: failed to save stats: %s\n", err.Error())
		err = nil
	}
	fmt.Fprintf(bypass, "\tSegment QP mean is %s with a standard deviation of %s.\n",
		strconv.FormatFloat(segmentQPmean, 'f', -1, 64), strconv.FormatFloat(segmentQPstddev, 'f', -1, 64),
	)
	fmt.Fprintf(bypass, "\tWeighted global QP is %s.\n", strconv.FormatFloat(results.GlobalWeightedQP, 'f', -1, 64))
	fmt.Fprintf(bypass, "\t%d encoding attempts (for a total of %d encoded frames) were necessary to encode %d segments (containing %d frames) to their optimal QP.\n",
		results.TotalNbAttempts, results.TotalEncodedFrames, len(segmentsPaths), results.TotalSegmentsFrames,
	)
	fmt.Fprintf(bypass, "\tAttempts ratio: x%s\n", strconv.FormatFloat(float64(results.TotalNbAttempts)/float64(len(segmentsPaths)), 'f', -1, 64))
	fmt.Fprintf(bypass, "\tFrames ratio: x%s\n", strconv.FormatFloat(float64(results.TotalEncodedFrames)/float64(results.TotalSegmentsFrames), 'f', -1, 64))
	fmt.Fprintf(bypass, "\tSegments encoding QP search done in %s.\n", duration.Round(time.Second))

	// Merge encoded segments
	fmt.Fprintln(bypass, "Merging segments...")
	encodedSegmentsMerged = filepath.Join(workingDir, "encoded_segments_merged.mkv")
	start = time.Now()
	if err = liveConcat(ctx, workingDir, encodedSegmentsMerged, results.EncodedSegmentsPaths, results.TotalSegmentsFrames, debug); err != nil {
		err = fmt.Errorf("failed to concat encoded segments: %w", err)
		return
	}
	duration = time.Since(start)
	fmt.Fprintf(bypass, "\tEncoded segments merged in %s.\n", duration.Round(time.Second))
	return
}
