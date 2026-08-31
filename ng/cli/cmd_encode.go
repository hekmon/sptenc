package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hekmon/sptenc/ng/core"
	"github.com/hekmon/sptenc/ng/ffmpeg"

	"al.essio.dev/pkg/shellescape"
	"github.com/hekmon/cunits/v3"
	"github.com/hekmon/liveprogress/v2"
	"github.com/urfave/cli/v3"
)

var encoders = []string{
	// HEVC
	string(ffmpeg.HEVCEncoderLibx265), string(ffmpeg.HEVCEncoderNVENC), string(ffmpeg.HEVCEncoderVAAPI),
	// AV1
	string(ffmpeg.AV1EncoderLibaom), string(ffmpeg.AV1EncoderNVENC), string(ffmpeg.AV1EncoderVAAPI),
}

var encodeCommand = &cli.Command{
	Name:        "encode",
	Aliases:     []string{"e"},
	Usage:       "Encode video segments to meet perceptual quality targets at minimal file size",
	Description: fmt.Sprintf("The input path can be provided in two forms:\n* pre-split video files: every video file within the pointed directory will be treated as already segmented scenes and used directly for the encode phase (see the split command)\n* single video file: sptenc will first create a lossless FFV1 master and split it into scene-aligned segments using the given threshold before encoding (one shot process)\n\nEach VMAF metric flag sets the minimum acceptable VMAF score (%d-%d) for that statistic. If a segment encoding falls below any enabled threshold, it is considered invalid and re-encoded at a lower QP. Set a value to %d to disable that metric.\nVMAF NEG (No Enhancement Gain) models are alternative VMAF model variants recommended when the source has undergone upscaling, sharpening, or denoising, as these can artificially inflate standard VMAF scores. NEG models provide more conservative scoring by ignoring enhancement gains, so expect lower scores. Use the --vmafneg flag to enable them.", core.VMAFMinValue, core.VMAFMaxValue, core.VMAFOffValue),
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:             "encoder",
			Aliases:          []string{"e"},
			Usage:            fmt.Sprintf("Encoder to use. Valid values: %s", strings.Join(encoders, ", ")),
			Value:            string(ffmpeg.HEVCEncoderLibx265),
			OnlyOnce:         true,
			Validator:        encoderValidator,
			ValidateDefaults: true,
		},
		// directories
		&cli.StringFlag{
			Name:     "outputdir",
			Aliases:  []string{"o"},
			Usage:    "Output directory",
			Value:    ".",
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
			Usage:            "Temporary directory location that will be used for intermediate files if needed",
			Value:            os.TempDir(),
			OnlyOnce:         true,
			Validator:        validateTmpDir,
			ValidateDefaults: true,
			Category:         "Directories",
		},
		// single video file
		&cli.Float64Flag{
			Name:      "threshold",
			Aliases:   []string{"T"},
			Usage:     fmt.Sprintf("Scene detection threshold for splitting video (%d-%d). Find the right value for your video with the split command.", ffmpeg.SceneThresholdMin, ffmpeg.SceneThresholdMax),
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
			Usage:    "Activate CUDA acceleration for VMAF computing. libvmaf must have been compiled with CUDA support.",
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
		// Input path argument
		if cmd.Args().Len() != 1 {
			return ctx, errors.New("only one input file is required")
		}
		fileInfos, err := os.Stat(cmd.Args().First()) // args are not parsed yet, can not use cmd.StringArg("inputpath")
		if err != nil {
			return ctx, fmt.Errorf("failed to access input file: %w", err)
		}
		ctx = context.WithValue(ctx, inputFileInfosCtxKey, fileInfos)
		if !fileInfos.IsDir() {
			if !fileInfos.Mode().IsRegular() {
				return ctx, errors.New("input path must be a directory or a regular file")
			}
			// if input is dir, we need the originalfile to be set
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
		// Check Output directory
		if fileInfos, err = os.Stat(cmd.String("outputdir")); err != nil {
			return ctx, fmt.Errorf("failed to access output directory: %w", err)
		}
		if !fileInfos.IsDir() {
			return ctx, errors.New("output directory path must be a directory")
		}
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
		defer func() {
			if saveErr := statsCache.SaveStats(); err != nil {
				fmt.Fprintf(bypass, "ERROR: failed to save stats cache: %s\n", saveErr)
			}
		}()

		/*
		 * Execute process
		 */

		// Step 1 - Segments and media infos
		var (
			segmentPaths  []string
			stats         ffmpeg.FFProbeStats
			totalDuration time.Duration
		)
		if !inputInfos.IsDir() {
			fmt.Fprintf(bypass, "Start encoding of %s (%s)\n",
				shellescape.Quote(filepath.Base(inputPath)),
				cunits.ImportInBytes(float64(inputInfos.Size())),
			)
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
			fmt.Fprintf(bypass, "\tSplit %d scenes in %w",
				len(scenes), time.Since(start).Round(time.Second),
			)
			if segmentPaths, err = getSegmentsFromDir(segmentsDir); err != nil {
				err = fmt.Errorf("Failed to get segments from directory: %w", err)
				return
			}
			// get stream infos
			if stats, err = getStreamsInfos(ctx, inputPath, cmd.Bool(debugFlagName)); err != nil {
				err = fmt.Errorf("Failed to probe input file: %w", err)
				return
			}
			totalDuration = stats.Format.Duration
		} else {
			fmt.Fprintf(bypass, "Start encoding of split video files within %s\n\t(source: %s (%s))\n",
				shellescape.Quote(filepath.Base(inputPath)),
				shellescape.Quote(filepath.Base(cmd.String("originalfile"))),
				cunits.ImportInBytes(float64(ctx.Value(inputFileSizeCtxKey).(int64))),
			)
			// get segments
			if segmentPaths, err = getSegmentsFromDir(inputPath); err != nil {
				err = fmt.Errorf("Failed to get segments from directory: %w", err)
				return
			}
			if len(segmentPaths) == 0 {
				err = fmt.Errorf("No segment files found in input directory")
				return
			}
			fmt.Fprintf(bypass, "Found %d segments in directory\n", len(segmentPaths))
			// get stream infos (from first segment)
			if stats, err = getStreamsInfos(ctx, segmentPaths[0], cmd.Bool(debugFlagName)); err != nil {
				err = fmt.Errorf("Failed to probe segment file: %w", err)
				return
			}
			// Calculate total duration of all segments for accurate progress bar
			fmt.Fprintln(bypass, "Calculating total duration of segments...")
			if totalDuration, err = getSegmentsTotalDuration(ctx, segmentPaths, cmd.Bool(debugFlagName)); err != nil {
				err = fmt.Errorf("Failed to calculate total duration: %w", err)
				return
			}
			fmt.Fprintf(bypass, "\tTotal duration of segments: %s\n", totalDuration)
		}

		// Step 2 - Encode segments (old, to remove)
		meanAvg, stdDevAvg := statsCache.GetMeanStdDev()
		fmt.Fprintf(bypass,
			"Searching the right QP for each GOP using %d as starting QP and %d as standard deviation range increment...\n",
			meanAvg, stdDevAvg,
		)
		var (
			segmentsQP []int
			statsQP    QPStats
		)
		if segmentsQP, statsQP, err = findAllSegmentsQP(ctx, workingDir, segmentPaths, meanAvg, stdDevAvg,
			totalDuration, vmafAuditor, statsCache, ffmpeg.Encoder(cmd.String("encoder")), cmd.Bool(debugFlagName)); err != nil {
			err = fmt.Errorf("Failed to encode segments: %w", err)
			return
		}

		// Step 2 - Encode segments (new)
		tobs := &TerminalObserver{
			debug: cmd.Bool(debugFlagName),
		}
		tobs.Start(len(segmentPaths), totalDuration)
		results, err := core.FindAllSegmentsQP(ctx, tobs,
			core.QPSearchConfig{
				SegmentPaths: segmentPaths,
				Auditor:      vmafAuditor,
				WorkingDir:   workingDir,
				StatsCache:   statsCache,
				Encoder:      ffmpeg.Encoder(cmd.String("encoder")),
			},
		)
		if err != nil {
			tobs.Stop()
			err = fmt.Errorf("Failed to encode segments: %w", err)
			return
		}
		tobs.Stop()
		fmt.Fprintf(bypass, "Segments QPs: %+v\n", results.QPs)
		fmt.Fprintf(bypass, "%d encoding attempts (for a total of %d encoded frames) were necessary to encode %d segments (containing %d frames) to their optimal QP.\n",
			results.TotalNbAttempts, results.TotalEncodedFrames, len(segmentPaths), results.TotalSegmentsFrames,
		)
		fmt.Fprintf(bypass, "Attempts ratio: x%02f\n", float64(results.TotalNbAttempts)/float64(len(segmentPaths)))
		fmt.Fprintf(bypass, "Frames ratio: x%02f\n", float64(results.TotalEncodedFrames)/float64(results.TotalSegmentsFrames))
		segmentQPmean, segmentQPstddev := statsCache.AddRun(results.QPs)
		fmt.Fprintf(bypass, "Segment QP mean is %s with a standard deviation of %s.\n",
			strconv.FormatFloat(segmentQPmean, 'f', -1, 64), strconv.FormatFloat(segmentQPstddev, 'f', -1, 64),
		)
		fmt.Fprintf(bypass, "Weighted global QP is %s.\n", strconv.FormatFloat(results.GlobalWeightedQP, 'f', -1, 64))
		if results.NbBestEfforts > 0 {
			fmt.Fprintf(bypass, "WARNING: %d segments were encoded with best effort, stopping at QP 0 but not validating VMAF config. Please check the logs.\n",
				results.NbBestEfforts,
			)
		}
		fmt.Fprintf(bypass, "Segments encoding QP search done in %s.\n", results.SearchDuration.Round(time.Second))

		// Step 3 - Merging
		// TODO
		return
	},
}

// TerminalObserver received and process search progress signals to translate them as terminal UI progress
// it implements the core.SearchCallbacks interface required by core.FindAllSegmentsQP()
type TerminalObserver struct {
	debug bool
	// Global progress
	globalProgressBar    *liveprogress.Bar
	globalNbSegmentsDone int
	globalAllSegmentSize cunits.Bits
	// Segment progress
	segmentCurrent          int
	segmentStatusLine       *liveprogress.CustomLine
	segmentCandidates       []string
	segmentCandidatesAccess sync.Mutex
	// File analysis
	analysisProgressBar *liveprogress.Bar
}

func (to *TerminalObserver) Start(totalSegments int, globalDuration time.Duration) {
	to.globalProgressBar = liveprogress.SetMainLineAsBar(
		liveprogress.WithTotal(uint64(globalDuration)),
		liveprogress.WithMultiplyRunes(),
		// liveprogress.WithWidth(barsWidth),
		liveprogress.WithSameAutoSizeInternalPadding(true, false),
		liveprogress.WithPrependDecorator(func(bar *liveprogress.Bar) string {
			return "    Global | "
		}),
		liveprogress.WithPrependTimeElapsed(liveprogress.BaseStyle()),
		liveprogress.WithAppendPercent(liveprogress.BaseStyle()),
		liveprogress.WithAppendTimeRemaining(liveprogress.BaseStyle()),
		liveprogress.WithAppendDecorator(func(bar *liveprogress.Bar) string {
			return fmt.Sprintf(" left | %d/%d segments done | %s",
				to.globalNbSegmentsDone, totalSegments, to.globalAllSegmentSize,
			)
		}),
	)
}

func (to *TerminalObserver) Stop() {
	if to.globalProgressBar == nil {
		return
	}
	liveprogress.RemoveBar(to.globalProgressBar)
	to.globalProgressBar = nil
}

func (to *TerminalObserver) Debug(format string, a ...any) {
	if to.debug {
		fmt.Fprintln(liveprogress.Bypass(), "DEBUG: "+fmt.Sprintf(format, a...))
	}
}

func (to *TerminalObserver) Warning(format string, a ...any) {
	fmt.Fprintln(liveprogress.Bypass(), "WARNING: "+fmt.Sprintf(format, a...))
}

func (to *TerminalObserver) Error(err error) {
	fmt.Fprintln(liveprogress.Bypass(), "ERROR: "+err.Error())
}

func (to *TerminalObserver) OnSegmentStart(segmentIndex int, segmentPath string) {
	to.segmentCurrent = segmentIndex
	if to.segmentCandidates != nil {
		to.segmentCandidatesAccess.Lock()
		to.segmentCandidates = to.segmentCandidates[:0] // reset while keeping cap
		to.segmentCandidatesAccess.Unlock()
	}
	if to.segmentStatusLine != nil {
		liveprogress.RemoveCustomLine(to.segmentStatusLine)
	}
	to.segmentStatusLine = liveprogress.AddCustomLine(func() string {
		to.segmentCandidatesAccess.Lock()
		defer to.segmentCandidatesAccess.Unlock()
		return fmt.Sprintf("   Segment | #%d - Searching for QP: %s", segmentIndex, strings.Join(to.segmentCandidates, ","))
	})
}

func (to *TerminalObserver) OnSegmentNewCandidate(qpCandidate int) {
	to.segmentCandidatesAccess.Lock()
	to.segmentCandidates = append(to.segmentCandidates, strconv.Itoa(qpCandidate))
	to.segmentCandidatesAccess.Unlock()
}

func (to *TerminalObserver) OnSegmentAnalysisStart(filePath string, fileSize int64) {
	if to.analysisProgressBar != nil {
		liveprogress.RemoveBar(to.analysisProgressBar)
	}
	to.analysisProgressBar = liveprogress.AddBar(
		liveprogress.WithTotal(uint64(fileSize)),
		liveprogress.WithMultiplyRunes(),
		// liveprogress.WithWidth(barsWidth),
		liveprogress.WithSameAutoSizeInternalPadding(true, false),
		liveprogress.WithPrependDecorator(func(bar *liveprogress.Bar) string {
			return "   Analyze | "
		}),
		liveprogress.WithAppendPercent(liveprogress.BaseStyle()),
		liveprogress.WithAppendDecorator(func(bar *liveprogress.Bar) string {
			return fmt.Sprintf(" | %s/%s",
				cunits.ImportInBytes(float64(bar.Current())), cunits.ImportInBytes(float64(bar.Total())),
			)
		}),
	)
}

func (to *TerminalObserver) OnSegmentAnalysisProgress(bytesRead int) {
	if to.analysisProgressBar != nil {
		to.analysisProgressBar.CurrentAdd(uint64(bytesRead))
	}
}

func (to *TerminalObserver) OnSegmentAnalysisStop() {
	if to.analysisProgressBar != nil {
		liveprogress.RemoveBar(to.analysisProgressBar)
		to.analysisProgressBar = nil
	}
}

func (to *TerminalObserver) OnSegmentDone(segmentFinalQP, segmentFrames, segmentNbAttempts int, currentTotalDuration time.Duration, currentTotalSize cunits.Bits) {
	// Remove bars
	if to.globalProgressBar == nil {
		return
	}
	// TODO check others in case of error returning midflight
	// Finished segment data
	fmt.Fprintf(liveprogress.Bypass(), "Segment #%d: QP %d selected for this segment of %d frames (%d attempts)\n",
		to.segmentCurrent, segmentFinalQP, segmentFrames, segmentNbAttempts,
	)
	if to.segmentStatusLine != nil {
		liveprogress.RemoveCustomLine(to.segmentStatusLine)
		to.segmentStatusLine = nil
	}
	// Global progress
	to.globalProgressBar.CurrentSet(uint64(currentTotalDuration))
	to.globalNbSegmentsDone++
	to.globalAllSegmentSize = currentTotalSize
}
