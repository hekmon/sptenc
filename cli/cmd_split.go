package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/hekmon/sptenc/ffmpeg"

	"al.essio.dev/pkg/shellescape"
	"github.com/hekmon/cunits/v3"
	"github.com/hekmon/liveprogress/v2"
	"github.com/urfave/cli/v3"
	"gonum.org/v1/gonum/stat"
)

// Flag name for split-specific flags.
const masterFlagName = "master"

var splitCommand = &cli.Command{
	Name:     "split",
	Aliases:  []string{"s"},
	Category: "Tooling",
	Usage:    "Split a video file by scenes",
	Description: "Detect scene changes in a video and split it into separate files at each transition.\n\n" +
		"HOW IT WORKS\n" +
		"By default, the command first creates a lossless FFV1 master to ensure frame-accurate cuts,\n" +
		"then analyzes the video with ffmpeg's scdet filter to find scene boundaries.\n\n" +
		"If the input has already been converted with the master command, use --" + masterFlagName + " to skip the\n" +
		"master creation phase.",
	Flags: func() []cli.Flag {
		flags := []cli.Flag{
			&cli.Float64Flag{
				Name:      minThresholdFlagName,
				Aliases:   []string{"T"},
				Usage:     fmt.Sprintf("Scene detection threshold (%d-%d)", ffmpeg.SceneThresholdMin, ffmpeg.SceneThresholdMax),
				Value:     minThresholdDefault,
				OnlyOnce:  true,
				Validator: validateSceneThreshold,
			},
			&cli.BoolFlag{
				Name:     masterFlagName,
				Aliases:  []string{"m"},
				Usage:    "Input is an already-processed master file",
				Value:    false,
				OnlyOnce: true,
			},
			// Directories
			&cli.StringFlag{
				Name:     outputDirFlagName,
				Aliases:  []string{"o"},
				Usage:    "Output directory for split scenes",
				Value:    "",
				OnlyOnce: true,
				Category: "Directories",
			},
			&cli.StringFlag{
				Name:             tmpDirFlagName,
				Aliases:          []string{"t"},
				Usage:            "Directory for temporary working files",
				Value:            os.TempDir(),
				OnlyOnce:         true,
				Validator:        validateTmpDir,
				ValidateDefaults: true,
				Category:         "Directories",
			},
		}
		// HW dec
		flags = append(flags, hardwareAccelFlags(hwAccelScopeDecode)...)
		return flags
	}(),
	Arguments: []cli.Argument{
		&cli.StringArg{
			Name:      "inputfile",
			UsageText: "<input file>",
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
		// Input file arg
		if cmd.Args().Len() != 1 {
			return ctx, errors.New("only one input file is required")
		}
		fileInfos, err := os.Stat(cmd.Args().First()) // args are not parsed yet, can not use cmd.StringArg("inputfile")
		if err != nil {
			return ctx, fmt.Errorf("failed to access input file: %w", err)
		}
		if !fileInfos.Mode().IsRegular() {
			return ctx, errors.New("input file must be a regular file")
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
		// Validate that at most one hardware decode flag is set
		var hwDecFlags int
		if cmd.Bool(nvdecFlagName) {
			hwDecFlags++
		}
		if cmd.Bool(vaapiDecFlagName) {
			hwDecFlags++
		}
		if cmd.Bool(d3d12DecFlagName) {
			hwDecFlags++
		}
		if cmd.Bool(videoToolboxDecFlagName) {
			hwDecFlags++
		}
		if hwDecFlags > 1 {
			return ctx, errors.New("only one hardware decode flag can be set at a time (--nvdec, --vaapi-dec, --d3d12va-dec, --videotoolbox-dec)")
		}
		return ctx, nil
	},
	Action: func(ctx context.Context, cmd *cli.Command) (err error) {
		inputFilePath := cmd.StringArg("inputfile")
		fileInfos, err := os.Stat(inputFilePath)
		if err != nil {
			return fmt.Errorf("failed to access input file: %w", err)
		}

		// start live progress
		if err = liveprogress.Start(); err != nil {
			return fmt.Errorf("failed to start live progress: %w", err)
		}
		defer liveprogress.Stop(false)
		bypass := liveprogress.Bypass()

		// handle modes and master preparation
		fileToProcess := inputFilePath
		var stats ffmpeg.FFProbeStats
		if stats, err = ffmpeg.GetStreamsInfos(ctx, ffmpeg.GetStreamsInfosConfig{
			Path: inputFilePath,
			Debug: func(s string) {
				if cmd.Bool(debugFlagName) {
					fmt.Fprintf(bypass, "DEBUG: %s\n", s)
				}
			},
			RuntimeError: func(err error) {
				fmt.Fprintf(bypass, "ERROR: %s\n", err)
			},
		}); err != nil {
			return fmt.Errorf("failed to get streams infos: %w", err)
		}
		duration := stats.Format.Duration
		fmt.Fprintf(bypass, "Splitting scenes of %s (%s) with threshold %s\n",
			shellescape.Quote(filepath.Base(inputFilePath)),
			cunits.ImportInBytes(float64(fileInfos.Size())),
			strconv.FormatFloat(cmd.Float64(minThresholdFlagName), 'f', -1, 64),
		)
		if !cmd.Bool(masterFlagName) {
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
			// build optional hw decode config
			masterConfig := ffmpeg.SelectCompatibleDecoders(ctx, inputFilePath,
				cmd.Bool(nvdecFlagName), cmd.Bool(vaapiDecFlagName), cmd.Bool(d3d12DecFlagName), cmd.Bool(videoToolboxDecFlagName),
				cmd.Int(nvidiaGPUIndexFlagName), cmd.String(vaapiRendererPathFlagName), cmd.Int(d3d12vaGPUIndexFlagName),
			).ToFFV1MasterConfig()
			if (cmd.Bool(nvdecFlagName) || cmd.Bool(vaapiDecFlagName) || cmd.Bool(d3d12DecFlagName) || cmd.Bool(videoToolboxDecFlagName)) &&
				!masterConfig.NVDec && !masterConfig.VAAPIDec && !masterConfig.D3D12Dec && !masterConfig.VideoToolboxDec {
				fmt.Fprintln(bypass, "WARNING: input codec is not compatible with the requested hardware decoder, falling back to software decode")
			}
			// create the master within
			if fileToProcess, _, duration, err = createMaster(ctx, inputFilePath, workingDir, fileInfos.Size(), cmd.Bool(debugFlagName), masterConfig); err != nil {
				return fmt.Errorf("failed to create the master file: %w", err)
			}
		}

		/*
		 * Execute
		 */

		// detect
		fmt.Fprintf(bypass, "Detecting scenes with threshold at %s...\n",
			strconv.FormatFloat(cmd.Float64(minThresholdFlagName), 'f', -1, 64),
		)
		start := time.Now()
		scenesConfig := ffmpeg.ScenesDetectionConfig{
			NVDec:           cmd.Bool(nvdecFlagName),
			NVDevice:        cmd.Int(nvidiaGPUIndexFlagName),
			VAAPIDec:        cmd.Bool(vaapiDecFlagName),
			VAAPIDevice:     cmd.String(vaapiRendererPathFlagName),
			D3D12Dec:        cmd.Bool(d3d12DecFlagName),
			D3D12Device:     cmd.Int(d3d12vaGPUIndexFlagName),
			VideoToolboxDec: cmd.Bool(videoToolboxDecFlagName),
		}
		scenes, err := liveDetectScenes(ctx, fileToProcess, cmd.Float64(minThresholdFlagName), duration, cmd.Bool(debugFlagName), scenesConfig)
		if err != nil {
			return fmt.Errorf("failed to detect scenes: %w", err)
		}
		fmt.Fprintf(bypass, "\tDetected %d scenes in %s\n",
			1+len(scenes), time.Since(start).Round(time.Second),
		)
		printSceneStats(bypass, scenes, duration, stats.VideoTrack())

		// split
		outputDir := cmd.String(outputDirFlagName)
		if outputDir == "" {
			outputDir = filepath.Dir(inputFilePath)
		}
		fmt.Fprintf(bypass, "Splitting scenes...\n")
		start = time.Now()
		if err = liveSplitScenes(ctx, fileToProcess, outputDir, duration, scenes, cmd.Bool(debugFlagName)); err != nil {
			return fmt.Errorf("failed to split scenes: %w", err)
		}
		fmt.Fprintf(bypass, "\tSplit %d scenes in %s\n",
			1+len(scenes), time.Since(start).Round(time.Second),
		)

		return
	},
}

// printSceneStats prints length statistics for the detected scenes to help the
// user judge whether the current threshold produces sensible boundaries.
func printSceneStats(bypass io.Writer, scenes []ffmpeg.Scene, totalDuration time.Duration, videoTrack *ffmpeg.FFProbeBinaryStream) {
	n := len(scenes) + 1
	if n == 0 {
		return
	}

	durations := make([]time.Duration, 0, n)
	if len(scenes) == 0 {
		durations = append(durations, totalDuration)
	} else {
		durations = append(durations, scenes[0].Start)
		for i := 1; i < len(scenes); i++ {
			durations = append(durations, scenes[i].Start-scenes[i-1].Start)
		}
		durations = append(durations, totalDuration-scenes[len(scenes)-1].Start)
	}

	var sum time.Duration
	minDur := durations[0]
	maxDur := durations[0]
	short1s := 0
	shortHalf := 0

	var frameRate float64
	if videoTrack != nil {
		frameRate, _ = parseFrameRateLocal(videoTrack.RFrameRate)
	}

	for _, d := range durations {
		sum += d
		if d < minDur {
			minDur = d
		}
		if d > maxDur {
			maxDur = d
		}
		if d < time.Second {
			short1s++
		}
		if d < 500*time.Millisecond {
			shortHalf++
		}
	}

	durationsFloat := make([]float64, len(durations))
	for i, d := range durations {
		durationsFloat[i] = float64(d)
	}
	meanF, stddevF := stat.MeanStdDev(durationsFloat, nil)
	mean := time.Duration(meanF)
	stddev := time.Duration(stddevF)

	fmt.Fprintf(bypass, "\nScene length statistics (n=%d):\n", n)
	fmt.Fprintf(bypass, "  Mean: %s, Std dev: %s\n", mean.Round(time.Millisecond), stddev.Round(time.Millisecond))
	fmt.Fprintf(bypass, "  Range: %s–%s", minDur.Round(time.Millisecond), maxDur.Round(time.Millisecond))
	if frameRate > 0 {
		minFrames := int(math.Round(float64(minDur) * frameRate / float64(time.Second)))
		fmt.Fprintf(bypass, " (shortest: %d frames)", minFrames)
	}
	fmt.Fprintln(bypass)

	if shortHalf > 0 || short1s > 0 {
		fmt.Fprintf(bypass, "  Short scenes: %d ≤ 0.5s, %d ≤ 1s\n", shortHalf, short1s)
	}
}

// parseFrameRateLocal converts an ffprobe frame-rate string into a float64.
func parseFrameRateLocal(s string) (float64, error) {
	if s == "" {
		return 0, errors.New("empty frame rate")
	}
	if strings.Contains(s, "/") {
		parts := strings.SplitN(s, "/", 2)
		num, err := strconv.ParseFloat(parts[0], 64)
		if err != nil {
			return 0, fmt.Errorf("invalid frame rate numerator %q: %w", parts[0], err)
		}
		den, err := strconv.ParseFloat(parts[1], 64)
		if err != nil {
			return 0, fmt.Errorf("invalid frame rate denominator %q: %w", parts[1], err)
		}
		if den == 0 {
			return 0, errors.New("zero denominator in frame rate")
		}
		return num / den, nil
	}
	return strconv.ParseFloat(s, 64)
}
