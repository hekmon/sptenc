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
	"github.com/olekukonko/tablewriter"
	"github.com/olekukonko/tablewriter/tw"
	"github.com/urfave/cli/v3"
	"gonum.org/v1/gonum/stat"
)

var (
	thresholdsTableConfig = tablewriter.Config{
		Header: tw.CellConfig{
			Formatting: tw.CellFormatting{
				AutoFormat: tw.Off,
			},
		},
		Row: tw.CellConfig{
			Alignment: tw.CellAlignment{
				PerColumn: []tw.Align{
					tw.AlignRight, tw.AlignRight, tw.AlignRight, tw.AlignRight, tw.AlignRight, tw.AlignRight, tw.AlignRight, tw.AlignRight,
				},
			},
		},
	}
)

var thresholdsCommand = &cli.Command{
	Name:     "thresholds",
	Aliases:  []string{"t"},
	Category: "Tooling",
	Usage:    "Preview candidate thresholds and their scene distributions",
	Description: "Detect scene boundaries once, then simulate what segment distributions\n" +
		"each candidate threshold would produce. This helps you choose sensible values for\n" +
		"--" + minThresholdFlagName + ", --" + maxThresholdFlagName + ", --" + maxCandidatesFlagName + ", and --" + minDropFlagName + " before running\n" +
		"batchsearch, where a bad range can cost hours of encoding time.\n\n" +
		"The candidate generation logic is identical to batchsearch, so the preview is a\n" +
		"faithful map of what the search will explore.",
	Flags: func() []cli.Flag {
		flags := []cli.Flag{}
		flags = append(flags, thresholdSearchFlags()...)
		flags = append(flags, hwDecodeFlags(false)...)
		return flags
	}(),
	Arguments: []cli.Argument{
		&cli.StringArg{
			Name:      "inputfile",
			UsageText: "<input file>",
		},
	},
	Before: func(ctx context.Context, cmd *cli.Command) (context.Context, error) {
		if err := checkFFMPEG(ctx); err != nil {
			return ctx, err
		}
		if err := checkFFProbe(ctx); err != nil {
			return ctx, err
		}
		if cmd.Args().Len() != 1 {
			return ctx, errors.New("only one input file is required")
		}
		fileInfos, err := os.Stat(cmd.Args().First())
		if err != nil {
			return ctx, fmt.Errorf("failed to access input file: %w", err)
		}
		if !fileInfos.Mode().IsRegular() {
			return ctx, errors.New("input file must be a regular file")
		}
		ctx = context.WithValue(ctx, inputFileSizeCtxKey, fileInfos.Size())
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
			return ctx, fmt.Errorf("only one hardware decode flag can be set at a time (--%s, --%s, --%s, --%s)",
				nvdecFlagName, vaapiDecFlagName, d3d12DecFlagName, videoToolboxDecFlagName)
		}
		if cmd.Float64(minThresholdFlagName) >= cmd.Float64(maxThresholdFlagName) {
			return ctx, fmt.Errorf("--%s must be strictly less than --%s", minThresholdFlagName, maxThresholdFlagName)
		}
		return ctx, nil
	},
	Action: func(ctx context.Context, cmd *cli.Command) (err error) {
		inputFilePath := cmd.StringArg("inputfile")

		if err = liveprogress.Start(); err != nil {
			return fmt.Errorf("failed to start live progress: %w", err)
		}
		defer liveprogress.Stop(false)
		bypass := liveprogress.Bypass()

		stats, err := ffmpeg.GetStreamsInfos(ctx, ffmpeg.GetStreamsInfosConfig{
			Path: inputFilePath,
			Debug: func(s string) {
				if cmd.Bool(debugFlagName) {
					fmt.Fprintf(bypass, "DEBUG: %s\n", s)
				}
			},
			RuntimeError: func(err error) {
				fmt.Fprintf(bypass, "ERROR: %s\n", err)
			},
		})
		if err != nil {
			return fmt.Errorf("failed to get streams infos: %w", err)
		}
		duration := stats.Format.Duration

		fmt.Fprintf(bypass, "Analyzing thresholds for %s (%s)\n",
			shellescape.Quote(filepath.Base(inputFilePath)),
			cunits.ImportInBytes(float64(ctx.Value(inputFileSizeCtxKey).(int64))),
		)

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
		scenes, err := liveDetectScenes(ctx, inputFilePath, cmd.Float64(minThresholdFlagName), duration, cmd.Bool(debugFlagName), scenesConfig)
		if err != nil {
			return fmt.Errorf("failed to detect scenes: %w", err)
		}
		fmt.Fprintf(bypass, "\tDetected %d scenes in %s\n",
			1+len(scenes), time.Since(start).Round(time.Second),
		)

		// Thresholds candidates refine
		cappedScenes := make([]core.Scene, 0, len(scenes))
		for _, scene := range scenes {
			if scene.Score <= cmd.Float64(maxThresholdFlagName) {
				cappedScenes = append(cappedScenes, core.Scene{Start: scene.Start, Score: scene.Score})
			}
		}
		if len(cappedScenes) < len(scenes) {
			fmt.Fprintf(bypass, "\tCapped to %d scenes with score ≤ %s\n",
				1+len(cappedScenes), strconv.FormatFloat(cmd.Float64(maxThresholdFlagName), 'f', -1, 64),
			)
		}
		candidates, effectiveMinDrop := core.GetOptimalMinDrop(cappedScenes, cmd.Int(maxCandidatesFlagName))
		if len(candidates) == 0 {
			fmt.Fprintln(bypass, "No candidates found in the given range.")
			return nil
		}
		if effectiveMinDrop < cmd.Int(minDropFlagName) {
			fmt.Fprintf(bypass, "\tWARNING: Auto-tuned scene drop of %d is below the minimum of %d; recomputing candidates...\n",
				effectiveMinDrop, cmd.Int(minDropFlagName),
			)
			candidates = core.GetSearchThresholdCandidates(cappedScenes, cmd.Int(minDropFlagName))
			effectiveMinDrop = cmd.Int(minDropFlagName)
		}
		fmt.Fprintf(bypass, "\tScene drop auto-tuned to %d to stay within %s=%d, producing %d candidates\n\n",
			effectiveMinDrop, maxCandidatesFlagName, cmd.Int(maxCandidatesFlagName), len(candidates),
		)
		if cmd.Bool(debugFlagName) {
			fmt.Fprintf(bypass, "\tCandidates: %v\n", candidates)
		}

		// Results table
		var buff strings.Builder
		table := tablewriter.NewTable(&buff, tablewriter.WithConfig(thresholdsTableConfig))
		table.Header("Threshold", "Scenes", "Longest", "Std Dev", "Mean", "Shortest", "≤1s", "≤0.5s")
		for _, candidate := range candidates {
			row := computeCandidateStats(scenes, duration, candidate)
			table.Append([]string{
				strconv.FormatFloat(candidate, 'f', -1, 64),
				strconv.Itoa(row.scenes),
				row.longest.Round(time.Millisecond).String(),
				row.stddev.Round(time.Millisecond).String(),
				row.mean.Round(time.Millisecond).String(),
				row.shortest.Round(time.Millisecond).String(),
				strconv.Itoa(row.short1s),
				strconv.Itoa(row.shortHalf),
			})
		}
		table.Render()
		fmt.Fprint(bypass, buff.String())

		return nil
	},
}

type candidateStats struct {
	scenes    int
	mean      time.Duration
	stddev    time.Duration
	shortest  time.Duration
	longest   time.Duration
	shortHalf int
	short1s   int
}

func computeCandidateStats(scenes []ffmpeg.Scene, totalDuration time.Duration, threshold float64) candidateStats {
	filtered := make([]ffmpeg.Scene, 0, len(scenes))
	for _, s := range scenes {
		if s.Score >= threshold {
			filtered = append(filtered, s)
		}
	}

	n := len(filtered) + 1
	durations := make([]time.Duration, 0, n)
	if len(filtered) == 0 {
		durations = append(durations, totalDuration)
	} else {
		durations = append(durations, filtered[0].Start)
		for i := 1; i < len(filtered); i++ {
			durations = append(durations, filtered[i].Start-filtered[i-1].Start)
		}
		durations = append(durations, totalDuration-filtered[len(filtered)-1].Start)
	}

	durationsFloat := make([]float64, len(durations))
	minDur := durations[0]
	maxDur := durations[0]
	shortHalf := 0
	short1s := 0

	for i, d := range durations {
		durationsFloat[i] = float64(d)
		if d < minDur {
			minDur = d
		}
		if d > maxDur {
			maxDur = d
		}
		if d < 500*time.Millisecond {
			shortHalf++
		}
		if d < time.Second {
			short1s++
		}
	}

	mean, stddev := stat.MeanStdDev(durationsFloat, nil)

	return candidateStats{
		scenes:    n,
		mean:      time.Duration(mean),
		stddev:    time.Duration(stddev),
		shortest:  minDur,
		longest:   maxDur,
		shortHalf: shortHalf,
		short1s:   short1s,
	}
}
