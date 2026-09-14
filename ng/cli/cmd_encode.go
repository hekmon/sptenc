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

	"github.com/hekmon/sptenc/ng/core"
	"github.com/hekmon/sptenc/ng/ffmpeg"

	"al.essio.dev/pkg/shellescape"
	"github.com/hekmon/cunits/v3"
	"github.com/hekmon/liveprogress/v2"
	"github.com/urfave/cli/v3"
)

var encodeCommand = &cli.Command{
	Name:    "encode",
	Aliases: []string{"e"},
	Usage:   "Encode video segments to meet perceptual quality targets at minimal file size",
	Description: fmt.Sprintf("The input path can be provided in two forms:\n* single video file: sptenc will first create a lossless FFV1 master and split it into scene-aligned segments using the given threshold before encoding (one shot process)\n* pre-split video files: every video file within the pointed directory will be treated as already segmented scenes and used directly for the encode phase (see the split command). If using your own segments, make sure every file have the same codec and frame rate!\n\nEach VMAF metric flag sets the minimum acceptable VMAF score (%d-%d) for that statistic. If a segment encoding falls below any enabled threshold, it is considered invalid and re-encoded at a lower QP. Set a value to %d to disable that metric.\n\nAUDIO\nIf all audio tracks are PCM (e.g. from Blu-ray remuxes), they are automatically losslessly compressed to FLAC during the final remux step. This reduces file size without any quality loss, consistent with sptenc's goal of smallest file size at guaranteed perceptual quality.\n\nENCODERS\nUse GPU for quick VMAF profile testing but always prefer CPU encoders for final encode (lower file size). Run 'sptenc check' to see which encoders are available on your system.",
		core.VMAFMinValue, core.VMAFMaxValue, core.VMAFOffValue),
	Flags: []cli.Flag{
		// encoding
		&cli.StringFlag{
			Name:             "encoder",
			Aliases:          []string{"e"},
			Usage:            fmt.Sprintf("Encoder to use. Valid values: %s", strings.Join(allEncoders, ", ")),
			Value:            string(ffmpeg.HEVCEncoderLibx265),
			OnlyOnce:         true,
			Validator:        encoderValidator,
			ValidateDefaults: true,
		},
		// GPU Accelerated Encoders
		&cli.IntFlag{
			Name:     "nvidiagpuindex",
			Usage:    "GPU to use when --encoder is an NVIDIA NVENC encoder",
			Value:    ffmpeg.CUDADefaultDevice,
			OnlyOnce: true,
			Category: "GPU Accelerated Encoders",
		},
		&cli.StringFlag{
			Name:     "vaapirendererpath",
			Usage:    "Direct Rendering Manager render node to use when --encoder is a VA-API encoder",
			Value:    ffmpeg.VAAPIDefaultDevice,
			OnlyOnce: true,
			Category: "GPU Accelerated Encoders",
		},
		&cli.IntFlag{
			Name:     "d3d12vagpuindex",
			Usage:    "GPU to use when --encoder is a D3D12VA encoder",
			Value:    ffmpeg.D3D12VADefaultDevice,
			OnlyOnce: true,
			Category: "GPU Accelerated Encoders",
		},
		// directories
		&cli.StringFlag{
			Name:     "outputdir",
			Aliases:  []string{"o"},
			Usage:    "Output directory (defaults to input file directory, or original file directory for segment inputs)",
			Value:    "",
			OnlyOnce: true,
			Category: "Directories",
		},
		&cli.StringFlag{
			Name:     "statscachedir",
			Aliases:  []string{"s"},
			Usage:    "Stats cache directory. Used to save encoding QP search mean and stddev to speed up future encoding.",
			Value:    getCacheDir(),
			OnlyOnce: true,
			Category: "Directories",
		},
		&cli.StringFlag{
			Name:             "tmpdir",
			Aliases:          []string{"t"},
			Usage:            "Temporary directory location that will be used for intermediate files",
			Value:            os.TempDir(),
			OnlyOnce:         true,
			Validator:        validateTmpDir,
			ValidateDefaults: true,
			Category:         "Directories",
		},
		// single video file
		&cli.Float64Flag{
			Name:    "threshold",
			Aliases: []string{"T"},
			Usage: fmt.Sprintf("Scene detection threshold for splitting video (%d-%d). Find the right value for your video with the split command.",
				ffmpeg.SceneThresholdMin, ffmpeg.SceneThresholdMax,
			),
			Value:     10,
			OnlyOnce:  true,
			Category:  "Single Video File",
			Validator: validateSceneThreshold,
		},
		// pre-split video files
		&cli.StringFlag{
			Name:     "originalfile",
			Aliases:  []string{"f"},
			Usage:    "Original file to use when performing the final remuxing (used to recover all other streams: audio, subtitles, etc.)",
			Value:    "",
			OnlyOnce: true,
			Category: "Pre-Split Video Files",
		},
		// VMAF
		&cli.BoolFlag{
			Name:     "vmafcuda",
			Usage:    "Activate CUDA acceleration for VMAF computing. libvmaf must have been compiled with CUDA support in the ffmpeg build.",
			Value:    false,
			OnlyOnce: true,
			Category: "VMAF",
		},
		&cli.BoolFlag{
			Name:     "vmafneg",
			Usage:    "Use VMAF NEG models",
			Value:    false,
			OnlyOnce: true,
			Category: "VMAF",
		},
		&cli.Float64Flag{
			Name:      "vmafmin",
			Usage:     "Minimum acceptable VMAF score for the worst frame.",
			Value:     core.VMAFOffValue,
			OnlyOnce:  true,
			Category:  "VMAF",
			Validator: vmafValueValidator,
		},
		&cli.Float64Flag{
			Name:      "vmafp1",
			Usage:     "Minimum acceptable VMAF score for the 1st percentile.",
			Value:     core.VMAFOffValue,
			OnlyOnce:  true,
			Category:  "VMAF",
			Validator: vmafValueValidator,
		},
		&cli.Float64Flag{
			Name:      "vmafp5",
			Usage:     "Minimum acceptable VMAF score for the 5th percentile.",
			Value:     core.VMAFOffValue,
			OnlyOnce:  true,
			Category:  "VMAF",
			Validator: vmafValueValidator,
		},
		&cli.Float64Flag{
			Name:      "vmafp10",
			Usage:     "Minimum acceptable VMAF score for the 10th percentile.",
			Value:     core.VMAFOffValue,
			OnlyOnce:  true,
			Category:  "VMAF",
			Validator: vmafValueValidator,
		},
		&cli.Float64Flag{
			Name:      "vmafp25",
			Usage:     "Minimum acceptable VMAF score for the 25th percentile.",
			Value:     core.VMAFOffValue,
			OnlyOnce:  true,
			Category:  "VMAF",
			Validator: vmafValueValidator,
		},
		&cli.Float64Flag{
			Name:      "vmafmedian",
			Usage:     "Minimum acceptable VMAF score for the median (50th percentile).",
			Value:     core.VMAFOffValue,
			OnlyOnce:  true,
			Category:  "VMAF",
			Validator: vmafValueValidator,
		},
		&cli.Float64Flag{
			Name:      "vmafhmean",
			Usage:     "Minimum acceptable VMAF score for harmonic mean.",
			Value:     core.VMAFOffValue,
			OnlyOnce:  true,
			Category:  "VMAF",
			Validator: vmafValueValidator,
		},
		&cli.Float64Flag{
			Name:      "vmafmean",
			Usage:     "Minimum acceptable VMAF score for mean.",
			Value:     93,
			OnlyOnce:  true,
			Category:  "VMAF",
			Validator: vmafValueValidator,
		},
	},
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
		requestedEncoder := cmd.String("encoder")
		if !encoders.Has(requestedEncoder) {
			return ctx, fmt.Errorf("requested encoder %q is not available in this ffmpeg build; run 'sptenc check' to see available encoders", requestedEncoder)
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
		} else {
			// Directory input: need original file for remuxing audio/subs
			if cmd.String("originalfile") == "" {
				return ctx, errors.New("when input path is a directory, you must specify the --originalfile flag")
			}
			if fileInfos, err = os.Stat(cmd.String("originalfile")); err != nil {
				return ctx, fmt.Errorf("failed to access original file: %w", err)
			}
			if !fileInfos.Mode().IsRegular() {
				return ctx, errors.New("original file must be a regular file")
			}
			ctx = context.WithValue(ctx, inputFileSizeCtxKey, fileInfos.Size())
		}
		// Resolve and check output directory
		outputDir := cmd.String("outputdir")
		if outputDir == "" {
			if inputIsDir {
				outputDir = filepath.Dir(cmd.String("originalfile"))
			} else {
				outputDir = filepath.Dir(cmd.Args().First())
			}
		}
		if fileInfos, err = os.Stat(outputDir); err != nil {
			return ctx, fmt.Errorf("failed to access output directory: %w", err)
		}
		if !fileInfos.IsDir() {
			return ctx, errors.New("output directory path must be a directory")
		}
		ctx = context.WithValue(ctx, outputDirCtxKey, outputDir)
		// Create the cache dir if necessary
		if err = os.MkdirAll(cmd.String("statscachedir"), 0755); err != nil {
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
		workingDir := generateWorkingDirectoryPath(cmd.String("tmpdir"))
		if cmd.Bool(debugFlagName) {
			fmt.Fprintf(bypass, "DEBUG: Creating temporary working directory %s\n",
				shellescape.Quote(workingDir),
			)
		}
		if err = os.MkdirAll(workingDir, 0755); err != nil {
			return fmt.Errorf("failed to create temporary working directory %s: %w",
				shellescape.Quote(workingDir), err,
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

		// Create the VMAF auditor
		vmafAuditor, err := core.NewVMAFChecker(
			cmd.Float64("vmafmin"), cmd.Float64("vmafp1"), cmd.Float64("vmafp5"), cmd.Float64("vmafp10"),
			cmd.Float64("vmafp25"), cmd.Float64("vmafmedian"), cmd.Float64("vmafhmean"), cmd.Float64("vmafmean"))
		if err != nil {
			err = fmt.Errorf("failed to create VMAF auditor: %w", err)
			return
		}

		// Get the stats cache
		statsCache, err := core.NewStatsCacheHistory(cmd.String("statscachedir"), ffmpeg.Encoder(cmd.String("encoder")), vmafAuditor)
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
			fmt.Fprintf(bypass, "Starting split encoding of %s (%s) with %s.\n",
				shellescape.Quote(filepath.Base(inputPath)),
				cunits.ImportInBytes(float64(inputInfos.Size())),
				cmd.String("encoder"),
			)
			fmt.Fprintf(bypass, "Each segment will have to validate the following VMAF profile:\n%s\n", vmafAuditor)
			// create master
			var (
				masterFile string
				duration   time.Duration
			)
			if masterFile, duration, err = createMaster(ctx, inputPath, workingDir, cmd.Bool(debugFlagName)); err != nil {
				return fmt.Errorf("failed to create the master file: %w", err)
			}
			// analyze
			fmt.Fprintf(bypass, "Detecting scenes with threshold above %s...\n",
				strconv.FormatFloat(cmd.Float64("threshold"), 'f', -1, 64),
			)
			var scenes []ffmpeg.Scene
			start := time.Now()
			if scenes, err = liveDetectScenes(ctx, masterFile, cmd.Float64("threshold"), duration, cmd.Bool(debugFlagName)); err != nil {
				return fmt.Errorf("failed to detect scenes: %w", err)
			}
			fmt.Fprintf(bypass, "\tDetected %d scenes in %s\n",
				len(scenes), time.Since(start).Round(time.Second),
			)
			// split
			fmt.Fprintf(bypass, "Splitting scenes...\n")
			segmentsDir := filepath.Join(workingDir, "segments")
			if err = os.MkdirAll(segmentsDir, 0755); err != nil {
				return fmt.Errorf("failed to create segments directory: %w", err)
			}
			start = time.Now()
			if err = liveSplitScenes(ctx, masterFile, segmentsDir, duration, scenes, cmd.Bool(debugFlagName)); err != nil {
				return fmt.Errorf("failed to split scenes: %w", err)
			}
			fmt.Fprintf(bypass, "\tSplit %d scenes in %v\n",
				len(scenes), time.Since(start).Round(time.Second),
			)
			if segmentsPaths, err = getSegmentsFromDir(segmentsDir); err != nil {
				err = fmt.Errorf("Failed to get segments from directory: %w", err)
				return
			}
			// get stream infos
			if sourceStats, err = getStreamsInfos(ctx, inputPath, cmd.Bool(debugFlagName)); err != nil {
				err = fmt.Errorf("Failed to probe input file: %w", err)
				return
			}
			totalDuration = sourceStats.Format.Duration
		} else {
			fmt.Fprintf(bypass, "Starting split encoding of already splitted video files within %s\n\t(source: %s (%s)) with %s.\n",
				shellescape.Quote(filepath.Base(inputPath)),
				shellescape.Quote(filepath.Base(cmd.String("originalfile"))),
				cunits.ImportInBytes(float64(ctx.Value(inputFileSizeCtxKey).(int64))),
				cmd.String("encoder"),
			)
			fmt.Fprintf(bypass, "Each segment will have to validate the following VMAF profile:\n%s\n", vmafAuditor)
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

		// Step 2 - Encode segments
		fmt.Fprintln(bypass, "Finding optimal QP for segments...")
		lqps := &LiveQPSearch{
			PrintDebug: cmd.Bool(debugFlagName),
		}
		lqps.Start(len(segmentsPaths), totalDuration)
		start := time.Now()
		results, err := core.FindAllSegmentsQP(ctx, lqps,
			core.QPSearchConfig{
				SegmentsPaths:     segmentsPaths,
				Auditor:           vmafAuditor,
				WorkingDir:        workingDir,
				StatsCache:        statsCache,
				KeepInvalidQP:     cmd.Bool(debugFlagName),
				Encoder:           ffmpeg.Encoder(cmd.String("encoder")),
				NVIDIAGPUIndex:    cmd.Int("nvidiagpuindex"),
				VAAPIRendererPath: cmd.String("vaapirendererpath"),
				D3D12VAGPUIndex:   cmd.Int("d3d12vagpuindex"),
				VMAFNeg:           cmd.Bool("vmafneg"),
				VMAFCUDA:          cmd.Bool("vmafcuda"),
			},
		)
		if err != nil {
			lqps.Stop()
			err = fmt.Errorf("Failed to encode segments: %w", err)
			return
		}
		duration := time.Since(start)
		lqps.Stop()
		// Print stats
		if cmd.Bool(debugFlagName) {
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
		fmt.Fprintf(bypass, "\tAttempts ratio: x%02f\n", float64(results.TotalNbAttempts)/float64(len(segmentsPaths)))
		fmt.Fprintf(bypass, "\tFrames ratio: x%02f\n", float64(results.TotalEncodedFrames)/float64(results.TotalSegmentsFrames))
		fmt.Fprintf(bypass, "\tSegments encoding QP search done in %s.\n", duration.Round(time.Second))

		// Step 3 - merging
		fmt.Fprintln(bypass, "Merging segments...")
		encodedSegmentsMerged := filepath.Join(workingDir, "encoded_segments_merged.mkv")
		start = time.Now()
		if err = liveConcat(ctx, workingDir, encodedSegmentsMerged, results.EncodedSegmentsPaths, results.TotalSegmentsFrames, cmd.Bool(debugFlagName)); err != nil {
			err = fmt.Errorf("failed to concat encoded segments: %w", err)
			return
		}
		duration = time.Since(start)
		fmt.Fprintf(bypass, "\tEncoded segments merged in %s.\n", duration.Round(time.Second))
		var vmafSource string
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
		fmt.Fprintln(bypass, "Computing final VMAF...")
		start = time.Now()
		finalVMAFreport, err := liveFinalVMAF(ctx, vmafSource, encodedSegmentsMerged, sourceStats.VideoTrack(),
			results.TotalSegmentsFrames, cmd.Int("nvidiagpuindex"), cmd.Bool("vmafneg"), cmd.Bool("vmafcuda"), cmd.Bool(debugFlagName),
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
			originalFile = cmd.String("originalfile")
		} else {
			originalFile = inputPath
		}
		outputPath := computeFinalPath(originalFile, ctx.Value(outputDirCtxKey).(string), ffmpeg.Encoder(cmd.String("encoder")))
		// Determine whether to auto-convert audio to FLAC.
		// sourceStats was probed from either the input file (single file) or the first segment (directory).
		// Probe originalFile directly to get the correct audio stream info in both cases.
		var encodeToFlac bool
		if originalStats, err := getStreamsInfos(ctx, originalFile, cmd.Bool(debugFlagName)); err == nil {
			encodeToFlac = core.AllAudioTracksPCM(originalStats)
		} else {
			fmt.Fprintf(bypass, "WARNING: failed to probe original file for audio, skipping FLAC check: %s\n", err)
		}
		if encodeToFlac {
			fmt.Fprintf(bypass, "\tAll audio tracks are PCM, encoding to FLAC during video remuxing.\n")
		}
		tags := core.GenerateTags(*sourceStats.Format, vmafAuditor, ffmpeg.Encoder(cmd.String("encoder")),
			results, finalVMAFStats, cmd.Bool("vmafneg"), videoStream.Height >= ffmpeg.UltraHDHeight)
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
						originalSize, finalFileSize, formatPercent(compressionRatio), sizeReduction)
				} else {
					fmt.Fprintf(bypass, "\tCompression: %s -> %s (%s reduction, %s saved)\n",
						originalSize, finalFileSize, formatPercent(compressionRatio), sizeReduction)
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
		if outputStats, err := getStreamsInfos(ctx, outputPath, cmd.Bool(debugFlagName)); err == nil {
			if outStream := outputStats.VideoTrack(); outStream != nil {
				if videoStream.ColorRange != "" && outStream.ColorRange != videoStream.ColorRange {
					fmt.Fprintf(bypass, "WARNING: output color_range (%s) does not match source (%s)\n", outStream.ColorRange, videoStream.ColorRange)
				}
				if videoStream.ColorSpace != "" && outStream.ColorSpace != videoStream.ColorSpace {
					fmt.Fprintf(bypass, "WARNING: output colorspace (%s) does not match source (%s)\n", outStream.ColorSpace, videoStream.ColorSpace)
				}
				if videoStream.ColorTransfer != "" && outStream.ColorTransfer != videoStream.ColorTransfer {
					fmt.Fprintf(bypass, "WARNING: output color_trc (%s) does not match source (%s)\n", outStream.ColorTransfer, videoStream.ColorTransfer)
				}
				if videoStream.ColorPrimaries != "" && outStream.ColorPrimaries != videoStream.ColorPrimaries {
					fmt.Fprintf(bypass, "WARNING: output color_primaries (%s) does not match source (%s)\n", outStream.ColorPrimaries, videoStream.ColorPrimaries)
				}
			} else {
				fmt.Fprintf(bypass, "WARNING: output file has no video stream, can not verify color metadata\n")
			}
		} else {
			fmt.Fprintf(bypass, "WARNING: could not verify output color metadata: %s\n", err)
		}

		// Done
		duration = time.Since(globalStart)
		fmt.Fprintf(bypass, "Complete split encoding took %s\n", duration.Round(time.Millisecond))
		return
	},
}
