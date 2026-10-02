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
	"github.com/hekmon/sptenc/pipeline"

	"al.essio.dev/pkg/shellescape"
	"github.com/hekmon/cunits/v3"
	"github.com/hekmon/liveprogress/v2"
	"github.com/olekukonko/tablewriter"
	"github.com/olekukonko/tablewriter/tw"
	"github.com/urfave/cli/v3"
	"gonum.org/v1/gonum/stat"
)

// Flag name for split-specific flags.
const (
	masterFlagName     = "master"
	listScenesFlagName = "list-scenes"
)

var splitCommand = &cli.Command{
	Name:     "split",
	Aliases:  []string{"s"},
	Category: "Tooling",
	Usage:    "Split a video file by scenes",
	Description: "Detect scene changes in a video and split it into separate files at each transition.\n\n" +
		"HOW IT WORKS\n" +
		"The command analyzes the video with ffmpeg's scdet filter to find scene boundaries, as encode,\n" +
		"batchsearch and thresholds do, then writes a lossless FFV1 master directly cut at them: every\n" +
		"frame of such a master is self-contained, so every cut falls on the exact frame.\n\n" +
		"If the input has already been converted with the master command, use --" + masterFlagName + ": it is cut as\n" +
		"is, its scenes detected on it. They are the source's when the master holds the source's luma\n" +
		"without loss, the only plane scdet reads: a limited range source of 8 or 10 bits. A full range\n" +
		"source's luma is converted to the limited range, and scores its scene changes lower: split the\n" +
		"source itself to cut where encode does.\n" +
		"Use --" + listScenesFlagName + " to detect and print the actual scene list (frame, time, duration,\n" +
		"and score) for a given threshold without creating any files. It is the fastest way to preview\n" +
		"exactly where the cuts will fall before committing to an encode or split.\n\n" +
		"SEGMENT LENGTH\n" +
		"The --" + minSegmentLengthFlagName + " flag removes scene boundaries that would create segments shorter\n" +
		"than the given duration. Short segments are merged into their shorter neighbour.\n" +
		"This is a quality-floor guardrail: segments under ~5 seconds do not yield statistically\n" +
		"valid VMAF percentile metrics (p1 needs ≥100 frames, p5 needs ≥20). Lower below the\n" +
		"default only if you explicitly accept the risk of sub-minimum segments.\n\n" +
		"CHOOSING A THRESHOLD\n" +
		"If you are trying to find a good threshold for encode or batchsearch, use the thresholds\n" +
		"command to preview candidate distributions without creating files. It is faster and avoids\n" +
		"filling your disk with test splits.",
	Flags: func() []cli.Flag {
		flags := []cli.Flag{
			&cli.Float64Flag{
				Name:      minThresholdFlagName,
				Aliases:   []string{"T"},
				Usage:     fmt.Sprintf("Scene detection threshold (%d-%d)", ffmpeg.SceneThresholdMin, ffmpeg.SceneThresholdMax),
				Value:     sceneThresholdDefault,
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
			&cli.BoolFlag{
				Name:     listScenesFlagName,
				Aliases:  []string{"l"},
				Usage:    "Only detect and print scenes, do not split the file",
				Value:    false,
				OnlyOnce: true,
			},
		}
		flags = append(flags, segmentFilterFlag(""), rgbMatrixFlag(""))
		// HW dec
		flags = append(flags, hardwareAccelFlags(hwAccelScopeDecode)...)
		return flags
	}(),
	Arguments: []cli.Argument{
		&cli.StringArg{
			Name:      "inputfile",
			UsageText: "<input file>",
		},
		&cli.StringArg{
			Name:      "outputdir",
			UsageText: "<output directory>",
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
		// Check arguments
		if cmd.Bool(listScenesFlagName) {
			if cmd.Args().Len() != 1 {
				return ctx, errors.New("exactly one argument is required in list mode: input file")
			}
		} else if cmd.Args().Len() != 2 {
			return ctx, errors.New("exactly two arguments are required: input file and output directory")
		}
		inputFilePath := cmd.Args().First()
		outputDir := cmd.Args().Get(1)
		fileInfos, err := os.Stat(inputFilePath) // args are not parsed yet, can not use cmd.StringArg("inputfile")
		if err != nil {
			return ctx, fmt.Errorf("failed to access input file: %w", err)
		}
		if !fileInfos.Mode().IsRegular() {
			return ctx, errors.New("input file must be a regular file")
		}
		// Validate or create output directory
		if !cmd.Bool(listScenesFlagName) {
			fileInfos, err = os.Stat(outputDir)
			if err == nil {
				if !fileInfos.IsDir() {
					return ctx, fmt.Errorf("output path exists and is not a directory: %s", shellescape.Quote(outputDir))
				}
			} else if os.IsNotExist(err) {
				if err = os.MkdirAll(outputDir, 0755); err != nil {
					return ctx, fmt.Errorf("failed to create output directory: %w", err)
				}
			} else {
				return ctx, fmt.Errorf("failed to access output directory: %w", err)
			}
		}
		if _, err = hwDecodeFlags(cmd); err != nil {
			return ctx, err
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
		liveprogress.AddCustomLine(func() string { return "" }) // separate logs from live status updates

		// probe the input
		requestedDecoder, _ := hwDecodeFlags(cmd) // validated in Before
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
		// reject what encode would reject later, before spending the time and the disk space
		var videoStream *ffmpeg.FFProbeBinaryStream
		if videoStream, err = checkSourceVideo(stats); err != nil {
			return
		}
		// The conversion of an RGB input, for its scene detection and its master. A master given
		// is cut as it is: encode converts its RGB segments (see writeSegmentsMasters)
		rgbToYUV, err := sourceYUVMatrix(cmd, bypass, videoStream)
		if err != nil {
			return
		}
		duration := stats.Format.Duration
		action := "Splitting"
		if cmd.Bool(listScenesFlagName) {
			action = "Listing"
		}
		fmt.Fprintf(bypass, "%s scenes of %s (%s) with threshold %s\n",
			action,
			shellescape.Quote(filepath.Base(inputFilePath)),
			cunits.ImportInBytes(float64(fileInfos.Size())),
			strconv.FormatFloat(cmd.Float64(minThresholdFlagName), 'f', -1, 64),
		)
		if minSegLen := cmd.Duration(minSegmentLengthFlagName); minSegLen > 0 {
			fmt.Fprintf(bypass, "Min segment length: %s\n", minSegLen)
		}
		// Decoder of the input (scene detection, frame count, master), as encode has it: a master
		// is FFV1, which no hardware decoder handles
		decoderCfg := requestedDecoder.CompatibleWith(ctx, inputFilePath)
		if requestedDecoder.Enabled() && !decoderCfg.Enabled() {
			fmt.Fprintf(bypass, "WARNING: input codec is not decoded with %s (not supported, or not decoded exactly: see MANUAL.md, Hardware decoding), falling back to software decode\n", requestedDecoder.Name())
		}

		/*
		 * Execute
		 */

		// Detect on the input itself, a source as encode, batchsearch and thresholds do. Its master,
		// made first, would not always do: a full range source is converted to the limited range
		// there, and its scene changes score lower (x0.856 on a test clip), so the same threshold
		// could keep fewer boundaries than those commands and --list-scenes do.
		fmt.Fprintf(bypass, "Detecting scenes with threshold at %s...\n",
			strconv.FormatFloat(cmd.Float64(minThresholdFlagName), 'f', -1, 64),
		)
		start := time.Now()
		scenesConfig := decoderCfg.ToScenesDetectionConfig()
		scenesConfig.RGBToYUV = rgbToYUV
		scenes, err := liveDetectScenes(ctx, inputFilePath, cmd.Float64(minThresholdFlagName), duration, cmd.Bool(debugFlagName),
			scenesConfig)
		if err != nil {
			return fmt.Errorf("failed to detect scenes: %w", err)
		}
		fmt.Fprintf(bypass, "\tDetected %d scenes in %s\n",
			1+len(scenes), time.Since(start).Round(time.Second),
		)
		printSceneStats(bypass, scenes, duration, stats.VideoTrack())

		// Apply min-segment-length filter if requested.
		if minSegLen := cmd.Duration(minSegmentLengthFlagName); minSegLen > 0 {
			var filtered []ffmpeg.Scene
			if filtered, err = pipeline.FilterShortScenes(scenes, videoStream.RFrameRate, duration, minSegLen); err != nil {
				return fmt.Errorf("failed to merge the scenes shorter than %s: %w", minSegLen, err)
			}
			if removed := len(scenes) - len(filtered); removed > 0 {
				fmt.Fprintf(bypass, "\tMerged %d boundaries to enforce min segment length of %s → %d scenes\n",
					removed, minSegLen, 1+len(filtered))
			}
			scenes = filtered
		}

		if cmd.Bool(listScenesFlagName) {
			printScenesList(bypass, scenes, duration)
			return
		}

		outputDir := cmd.StringArg("outputdir")
		if !cmd.Bool(masterFlagName) {
			// The master, written cut into its segments: see createMasterSegments
			if _, err = createMasterSegments(ctx, inputFilePath, outputDir, scenes, cmd.Bool(debugFlagName), decoderCfg, rgbToYUV); err != nil {
				return fmt.Errorf("failed to create the master segments: %w", err)
			}
			return
		}
		// split the master given
		fmt.Fprintf(bypass, "Splitting scenes...\n")
		start = time.Now()
		if err = liveSplitScenes(ctx, inputFilePath, outputDir, duration, scenes, cmd.Bool(debugFlagName)); err != nil {
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

	var statsOut strings.Builder
	fmt.Fprintf(&statsOut, "\nScene length statistics (n=%d):\n", n)
	fmt.Fprintf(&statsOut, "    Mean: %s, Std dev: %s\n", mean.Round(time.Millisecond), stddev.Round(time.Millisecond))
	fmt.Fprintf(&statsOut, "    Range: %s–%s", minDur.Round(time.Millisecond), maxDur.Round(time.Millisecond))
	if frameRate > 0 {
		minFrames := int(math.Round(float64(minDur) * frameRate / float64(time.Second)))
		fmt.Fprintf(&statsOut, " (shortest: %d frames)", minFrames)
	}
	fmt.Fprintln(&statsOut)
	if shortHalf > 0 || short1s > 0 {
		fmt.Fprintf(&statsOut, "    Short scenes: %d ≤ 0.5s, %d ≤ 1s\n", shortHalf, short1s)
	}
	fmt.Fprint(bypass, statsOut.String())
}

// printScenesList prints each scene with its start frame, time marker, duration,
// and the score of the boundary that starts it (if any).
func printScenesList(bypass io.Writer, scenes []ffmpeg.Scene, totalDuration time.Duration) {
	if len(scenes) == 0 {
		fmt.Fprintln(bypass, "\nNo scene boundaries detected.")
		fmt.Fprintf(bypass, "1 scene, duration %s\n", totalDuration.Round(time.Millisecond))
		return
	}

	var buff strings.Builder
	table := tablewriter.NewTable(&buff, tablewriter.WithConfig(tablewriter.Config{
		Header: tw.CellConfig{
			Formatting: tw.CellFormatting{AutoFormat: tw.Off},
		},
		Row: tw.CellConfig{
			Alignment: tw.CellAlignment{
				PerColumn: []tw.Align{tw.AlignRight, tw.AlignRight, tw.AlignRight, tw.AlignRight, tw.AlignRight},
			},
		},
	}))
	table.Header("#", "Frame", "Time", "Duration", "Score")

	// Scene 1 starts at frame 0
	prevTime := time.Duration(0)
	prevFrame := 0
	for i, s := range scenes {
		dur := s.Start - prevTime
		score := "-"
		if i > 0 {
			score = strconv.FormatFloat(scenes[i-1].Score, 'f', 2, 64)
		}
		table.Append([]string{
			strconv.Itoa(i + 1),
			strconv.Itoa(prevFrame),
			prevTime.Round(time.Millisecond).String(),
			dur.Round(time.Millisecond).String(),
			score,
		})
		prevTime = s.Start
		prevFrame = s.Frame
	}
	// Last scene
	lastDur := totalDuration - prevTime
	table.Append([]string{
		strconv.Itoa(len(scenes) + 1),
		strconv.Itoa(prevFrame),
		prevTime.Round(time.Millisecond).String(),
		lastDur.Round(time.Millisecond).String(),
		strconv.FormatFloat(scenes[len(scenes)-1].Score, 'f', 2, 64),
	})

	table.Render()
	var out strings.Builder
	out.WriteString("\nScenes:\n")
	out.WriteString(buff.String())
	fmt.Fprintf(&out, "%d scene(s), %d boundary(ies)\n", 1+len(scenes), len(scenes))
	fmt.Fprint(bypass, out.String())
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
