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
	Description: "INPUT\n" +
		"The input path can be provided in two forms:\n" +
		"  * Single video file: sptenc creates a lossless FFV1 master, splits it into scene-aligned\n" +
		"    segments, and encodes each one (one-shot process).\n" +
		"  * Directory of pre-split video files: every file is treated as an already-segmented scene.\n" +
		"    Files are processed in alphabetical order — name them accordingly (e.g. seg_01.mkv,\n" +
		"    seg_02.mkv) to preserve scene order. All files must have the same codec and frame rate.\n\n" +
		"SCENE DETECTION\n" +
		"Use 'split --" + listScenesFlagName + "' to preview the actual scene list (frame, time, duration, score)\n" +
		"for a given threshold, the thresholds command to inspect candidate thresholds and their\n" +
		"scene-distribution statistics (scenes count, longest segment, std dev, mean, shortest segment)\n" +
		"so you can pick a single threshold to feed into encode, or batchsearch to search for the\n" +
		"threshold that yields the smallest passing file automatically.\n\n" +
		"VMAF METRICS\n" +
		fmt.Sprintf("Each VMAF metric flag sets the minimum acceptable VMAF score (%d-%d). If a segment falls\n", core.VMAFMinValue, core.VMAFMaxValue) +
		fmt.Sprintf("below any enabled threshold, it is re-encoded at a lower QP. Set a value to %d to disable\n", core.VMAFOffValue) +
		"that metric.\n\n" +
		"STATS CACHE\n" +
		"The cache records QP search statistics to speed up future encodes with the same encoder\n" +
		"and VMAF profile. Different content types (clean animation vs grainy film) need very\n" +
		"different QP distributions, so mixing them slows convergence. Use --" + cacheProfileFlagName + " to keep\n" +
		"these histories separate.\n\n" +
		"ENCODERS\n" +
		"Use GPU encoders for quick VMAF profile testing, but prefer CPU encoders for the final\n" +
		"encode to get the smallest file size. Run 'sptenc check' to see which encoders are\n" +
		"available on your system.\n\n" +
		"CONCURRENT ENCODING\n" +
		"The --" + concurrentSegmentsFlagName + " flag controls how many segments are searched in parallel (default: 1).\n" +
		"The output is the same whatever the value, only the time it takes changes.\n" +
		"  * GPU encoders often support multiple parallel sessions. Hard session limits vary by\n" +
		"    generation and SKU (typically 1-3 on consumer cards), so verify your specific GPU's\n" +
		"    capabilities before raising this value.\n" +
		"  * CPU encoders use every thread of the machine on their own, but a single encode does not\n" +
		"    keep a many-core CPU fully busy. On a 16 cores / 32 threads CPU, libx265 at 1080p encoded\n" +
		"    26% more frames per second with 2 concurrent segments, and up to 46% more with 3 and\n" +
		"    --" + vmafCUDAFlagName + " (which takes VMAF away from the CPU). Expect less with fewer cores or bigger\n" +
		"    pictures, and mind the memory with 4K content. Measure on your machine.\n" +
		"Segments started together can not learn from each other: a run with several concurrent\n" +
		"segments needs a few more attempts at its beginning, a cost only visible on short inputs.\n\n" +
		"SEGMENT LENGTH\n" +
		"The --" + minSegmentLengthFlagName + " flag removes scene boundaries that would create segments shorter\n" +
		"than the given duration. Short segments are merged into their shorter neighbour.\n" +
		"This happens after scene detection but before splitting, so the QP search and VMAF\n" +
		"evaluation operate on segments long enough to yield statistically valid percentile\n" +
		"metrics (p1 needs ≥100 frames, p5 needs ≥20). It is a quality-floor guardrail, not a\n" +
		"compression tuning knob. Lower below the default only if you explicitly accept the risk\n" +
		"of sub-minimum segments.\n\n" +
		"AUDIO\n" +
		"If all audio tracks are PCM (e.g. from Blu-ray remuxes), they are automatically compressed\n" +
		"to FLAC during the final remux step. This reduces file size with no quality loss.",
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
		flags = append(flags, segmentFilterFlag(""))
		flags = append(flags, hardwareAccelFlags(hwAccelScopeEncode)...)
		flags = append(flags, directoryFlags()...)
		flags = append(flags, VMAFFlags()...)
		return
	}(),
	Arguments: []cli.Argument{
		&cli.StringArg{
			Name:      "inputpath",
			UsageText: "<input path>",
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
		// Check arguments
		if cmd.Args().Len() != 2 {
			return ctx, errors.New("exactly two arguments are required: input path and output file")
		}
		inputPath := cmd.Args().First() // args are not parsed yet, can not use cmd.StringArg("inputpath")
		outputPath := cmd.Args().Get(1)
		fileInfos, err := os.Stat(inputPath)
		if err != nil {
			return ctx, fmt.Errorf("failed to access input path: %w", err)
		}
		inputIsDir := fileInfos.IsDir()
		if !inputIsDir {
			if !fileInfos.Mode().IsRegular() {
				return ctx, errors.New("input path must be a directory or a regular file")
			}
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
		}
		// Validate output path
		if err := validateOutputPath(outputPath); err != nil {
			return ctx, err
		}
		if inputPath == outputPath {
			return ctx, errors.New("input path and output file must be different paths")
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
		inputInfos, err := os.Stat(inputPath)
		if err != nil {
			return fmt.Errorf("failed to access input file: %w", err)
		}
		var inputFileSize int64
		if !inputInfos.IsDir() {
			inputFileSize = inputInfos.Size()
		} else {
			originalFilePath := cmd.String(originalFileFlagName)
			originalInfos, err := os.Stat(originalFilePath)
			if err != nil {
				return fmt.Errorf("failed to access original file: %w", err)
			}
			inputFileSize = originalInfos.Size()
		}

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

		// Prepare the encoder adapter (used for cache creation and segment encoding)
		encoderAdapter := &pipeline.EncoderAdapter{
			Encoder:           ffmpeg.Encoder(cmd.String(encoderFlagName)),
			NVIDIAGPUIndex:    cmd.Int(nvidiaGPUIndexFlagName),
			VAAPIRendererPath: cmd.String(vaapiRendererPathFlagName),
			D3D12VAGPUIndex:   cmd.Int(d3d12vaGPUIndexFlagName),
			VMAFNeg:           cmd.Bool(vmafNegFlagName),
			VMAFCUDA:          cmd.Bool(vmafCUDAFlagName),
		}

		/*
		 * Execute process
		 */
		globalStart := time.Now()

		// Step 1 - Segments and media infos
		var (
			segmentsPaths     []string
			sourceStats       ffmpeg.FFProbeStats
			totalDuration     time.Duration
			videoStream       *ffmpeg.FFProbeBinaryStream
			sourceTotalFrames int
		)
		if !inputInfos.IsDir() {
			fmt.Fprintf(bypass, "\nStarting split encoding of %s (%s) with %s.\n",
				shellescape.Quote(filepath.Base(inputPath)),
				cunits.ImportInBytes(float64(inputInfos.Size())),
				cmd.String(encoderFlagName),
			)
			if minSegLen := cmd.Duration(minSegmentLengthFlagName); minSegLen > 0 {
				fmt.Fprintf(bypass, "Min segment length: %s\n", minSegLen)
			}
			fmt.Fprintf(bypass, "Each segment will have to validate the following VMAF profile:\n\n%s\n", vmafAuditor)
			// Build decoder config for scene detection on the original file (hw decoding if available)
			decoderCfg := ffmpeg.SelectDecoderForEncoder(ctx, inputPath, ffmpeg.Encoder(cmd.String(encoderFlagName)),
				cmd.Int(nvidiaGPUIndexFlagName), cmd.String(vaapiRendererPathFlagName), cmd.Int(d3d12vaGPUIndexFlagName))
			// Get source stats and validate early
			if sourceStats, err = getStreamsInfos(ctx, inputPath, cmd.Bool(debugFlagName)); err != nil {
				return fmt.Errorf("failed to probe input file: %w", err)
			}
			if videoStream, err = checkSourceVideo(sourceStats); err != nil {
				return
			}
			totalDuration = sourceStats.Format.Duration
			// Detect scenes on the original file to take advantage of hw decoding
			fmt.Fprintf(bypass, "Detecting scenes with threshold at %s...\n",
				strconv.FormatFloat(cmd.Float64(minThresholdFlagName), 'f', -1, 64),
			)
			var scenes []ffmpeg.Scene
			start := time.Now()
			if scenes, err = liveDetectScenes(ctx, inputPath, cmd.Float64(minThresholdFlagName), totalDuration,
				cmd.Bool(debugFlagName), decoderCfg.ToScenesDetectionConfig()); err != nil {
				return fmt.Errorf("failed to detect scenes: %w", err)
			}
			fmt.Fprintf(bypass, "\tDetected %d scenes in %s\n",
				1+len(scenes), time.Since(start).Round(time.Second),
			)
			// Apply min-segment-length filter if requested.
			if minSegLen := cmd.Duration(minSegmentLengthFlagName); minSegLen > 0 {
				filtered := pipeline.FilterShortScenes(scenes, totalDuration, minSegLen)
				if removed := len(scenes) - len(filtered); removed > 0 {
					fmt.Fprintf(bypass, "\tMerged %d boundaries to enforce min segment length of %s → %d scenes\n",
						removed, minSegLen, 1+len(filtered))
				}
				scenes = filtered
			}
			// create master
			var masterFile string
			if masterFile, sourceTotalFrames, _, err = createMaster(ctx, inputPath, filepath.Join(workingDir, "master.mkv"),
				inputFileSize, cmd.Bool(debugFlagName), decoderCfg.ToFFV1MasterConfig()); err != nil {
				return fmt.Errorf("failed to create the master file: %w", err)
			}
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
		} else {
			fmt.Fprintf(bypass, "\nStarting split encoding of already splitted video files within %s\n\t(source: %s (%s)) with %s.\n",
				shellescape.Quote(filepath.Base(inputPath)),
				shellescape.Quote(filepath.Base(cmd.String(originalFileFlagName))),
				cunits.ImportInBytes(float64(inputFileSize)),
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
		// Validate the video stream (both kinds of input end up here: the first segment stands
		// for the others with a directory)
		if videoStream, err = checkSourceVideo(sourceStats); err != nil {
			return
		}

		// Get the stats cache (after probing so we know the VMAF model)
		qpMin, qpMax, qpFound := encoderAdapter.QPRange()
		if !qpFound {
			err = fmt.Errorf("unsupported encoder %s", encoderAdapter.Name())
			return
		}
		vmafModel := ffmpeg.VMAFModel(videoStream.Height >= ffmpeg.Height4K, cmd.Bool(vmafNegFlagName))
		statsCache, err := core.NewStatsCacheHistory(cmd.String(statsCacheDirFlagName), encoderAdapter.Name(), qpMin, qpMax, vmafModel, vmafAuditor, cmd.String(cacheProfileFlagName))
		if err != nil {
			err = fmt.Errorf("failed to create stats cache: %w", err)
			return
		}
		if cmd.Bool(debugFlagName) {
			fmt.Fprintf(bypass, "DEBUG: Using stats cache at: %s\n", shellescape.Quote(statsCache.GetPath()))
		}

		// Step 2 - Process segments
		results, encodedSegmentsMerged, err := processSegments(ctx, segmentsPaths, workingDir, totalDuration,
			vmafAuditor, statsCache, encoderAdapter, cmd.Int(concurrentSegmentsFlagName), cmd.Bool(debugFlagName))
		if err != nil {
			err = fmt.Errorf("failed to encode segments: %w", err)
			return
		}
		if _, _, err := statsCache.AddRun(results.QPs); err != nil {
			fmt.Fprintf(bypass, "ERROR: failed to save stats: %s\n", err.Error())
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
		fmt.Fprintln(bypass, "Verifying frame counts for final VMAF...")
		start = time.Now()
		var sourceFrames, encodedFrames int
		if inputInfos.IsDir() {
			var sourceFileInfo os.FileInfo
			sourceFileInfo, err = os.Stat(vmafSource)
			if err != nil {
				err = fmt.Errorf("could not stat source for frame count verification: %w", err)
				return
			}
			sourceFrames, _, _, err = liveCountNbFrames(ctx, vmafSource, sourceFileInfo.Size(), cmd.Bool(debugFlagName))
			if err != nil {
				err = fmt.Errorf("could not count frames in source for verification: %w", err)
				return
			}
		} else {
			// Reuse the exact frame count from createMaster instead of re-probing the source.
			sourceFrames = sourceTotalFrames
		}
		var encodedFileInfo os.FileInfo
		if encodedFileInfo, err = os.Stat(encodedSegmentsMerged); err != nil {
			err = fmt.Errorf("could not stat encoded output for frame count verification: %w", err)
			return
		}
		encodedFrames, _, _, err = liveCountNbFrames(ctx, encodedSegmentsMerged, encodedFileInfo.Size(), cmd.Bool(debugFlagName))
		if err != nil {
			err = fmt.Errorf("could not count frames in encoded output for verification: %w", err)
			return
		}
		if cmd.Bool(debugFlagName) {
			fmt.Fprintf(bypass, "DEBUG: Frame counts - source: %d, encoded: %d\n", sourceFrames, encodedFrames)
		}
		if sourceFrames != encodedFrames {
			err = fmt.Errorf("frame count mismatch: source has %d frames but encoded output has %d frames. This will cause VMAF misalignment",
				sourceFrames, encodedFrames)
			return
		}
		duration = time.Since(start)
		fmt.Fprintf(bypass, "\tFrame counts verified in %s.\n", duration.Round(time.Second))
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
		fmt.Fprintf(bypass, "\tFinal VMAF computed in %s:\n\n%s\n", duration.Round(time.Second), finalVMAFStats)

		// Step 5 - remux final file
		fmt.Fprintln(bypass, "Remuxing to final file...")
		var originalFile string
		if inputInfos.IsDir() {
			originalFile = cmd.String(originalFileFlagName)
		} else {
			originalFile = inputPath
		}
		outputPath := cmd.StringArg("output")
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
		tags := metadata.GenerateTags(vmafAuditor, ffmpeg.Encoder(cmd.String(encoderFlagName)),
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
	vmafAuditor core.VMAFChecker, statsCache core.StatsCache, encoder core.SegmentEncoder, concurrency int, debug bool) (
	results core.QPSearchResults, encodedSegmentsMerged string, err error) {
	bypass := liveprogress.Bypass()
	fmt.Fprintln(bypass, "Finding optimal QP for each segment...")
	mean, stddev := statsCache.GetMeanStdDev()
	fmt.Fprintf(bypass, "\tUsing search parameters mean %d and stddev %d\n", mean, stddev)
	lqps := LiveQPSearch{
		PrintDebug:  debug,
		Concurrency: concurrency,
	}
	lqps.Start(len(segmentsPaths), totalDuration)
	start := time.Now()
	results, err = core.FindAllSegmentsQP(ctx, &lqps,
		core.QPSearchConfig{
			SegmentsPaths:        segmentsPaths,
			Auditor:              vmafAuditor,
			WorkingDir:           workingDir,
			StatsCache:           statsCache,
			KeepInvalidQP:        debug,
			Encoder:              encoder,
			NbConcurrentSegments: concurrency,
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
		// Best effort stops at the lowest QP of the encoder, which is not 0 for all of them
		qpMin, _, _ := encoder.QPRange()
		if results.NbBestEfforts == 1 {
			fmt.Fprintf(bypass, "WARNING: 1 segment was encoded with best effort, stopping at QP %d but not validating VMAF config. Please check the logs.\n",
				qpMin,
			)
		} else {
			fmt.Fprintf(bypass, "WARNING: %d segments were encoded with best effort, stopping at QP %d but not validating VMAF config. Please check the logs.\n",
				results.NbBestEfforts, qpMin,
			)
		}
	}
	segmentQPmean, segmentQPstddev := results.GetMeanStdDev()
	fmt.Fprintf(bypass, "\tSegment QP mean is %s with a standard deviation of %s.\n",
		strconv.FormatFloat(segmentQPmean, 'f', -1, 64), strconv.FormatFloat(segmentQPstddev, 'f', -1, 64),
	)
	fmt.Fprintf(bypass, "\tWeighted global QP is %s.\n", strconv.FormatFloat(results.GlobalWeightedQP, 'f', -1, 64))
	fmt.Fprintf(bypass, "\t%d encoding attempts (for a total of %d encoded frames) were necessary to encode %d segments (containing %d frames) to their optimal QP.\n",
		results.TotalNbAttempts, results.TotalEncodedFrames, len(segmentsPaths), results.TotalSegmentsFrames,
	)
	fmt.Fprintf(bypass, "\tAttempts ratio: x%s\n", strconv.FormatFloat(float64(results.TotalNbAttempts)/float64(len(segmentsPaths)), 'f', -1, 64))
	fmt.Fprintf(bypass, "\tEncoded frames ratio: x%s\n", strconv.FormatFloat(float64(results.TotalEncodedFrames)/float64(results.TotalSegmentsFrames), 'f', -1, 64))
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
