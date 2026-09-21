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
					tw.AlignRight, tw.AlignRight, tw.AlignRight, tw.AlignRight, tw.AlignRight, tw.AlignRight, tw.AlignRight, tw.AlignRight, tw.AlignRight,
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
		"WHY THRESHOLD SELECTION MATTERS\n" +
		"Scene detection splits a video into independent segments. Each segment gets its own QP, so\n" +
		"splitting finely lets hard passages use low QP and easy ones high. But every split forces an\n" +
		"I-frame, and short runs starve B/P compression. Split coarsely and B/P frames thrive across\n" +
		"long runs, yet the whole scene must bow to its hardest passage — easy sections pay for quality\n" +
		"they do not need.\n\n" +
		"The sweet spot is a threshold that gives each scene enough freedom to use its own QP while\n" +
		"leaving enough continuous frames for the encoder to compress efficiently. This command lets you\n" +
		"preview where that sweet spot likely lives without running a single encode.\n\n" +
		"The candidate generation logic is identical to batchsearch, so the preview is a\n" +
		"faithful map of what the search will explore.",
	Flags: func() []cli.Flag {
		flags := []cli.Flag{}
		flags = append(flags, thresholdSearchFlags()...)
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
		/*
		 * Prepare
		 */
		if err = liveprogress.Start(); err != nil {
			return fmt.Errorf("failed to start live progress: %w", err)
		}
		defer liveprogress.Stop(false)
		bypass := liveprogress.Bypass()

		/*
		 * Execute
		 */

		fmt.Fprintf(bypass, "\nDetecting scenes with threshold at %s...\n",
			strconv.FormatFloat(cmd.Float64(minThresholdFlagName), 'f', -1, 64),
		)
		fileInfos, err := os.Stat(cmd.StringArg("inputfile"))
		if err != nil {
			return fmt.Errorf("failed to access input file: %w", err)
		}
		fmt.Fprintf(bypass, "Analyzing thresholds for %s (%s)\n",
			shellescape.Quote(filepath.Base(cmd.StringArg("inputfile"))),
			cunits.ImportInBytes(float64(fileInfos.Size())),
		)
		fmt.Fprintf(bypass, "\t• search range: %s to %s\n",
			strconv.FormatFloat(cmd.Float64(minThresholdFlagName), 'f', -1, 64),
			strconv.FormatFloat(cmd.Float64(maxThresholdFlagName), 'f', -1, 64),
		)
		fmt.Fprintf(bypass, "\t• testing at most %d candidate thresholds\n", cmd.Int(maxCandidatesFlagName))
		fmt.Fprintf(bypass, "\t• minimum scene drop: %d\n", cmd.Int(minDropFlagName))
		// Detect scenes
		stats, err := ffmpeg.GetStreamsInfos(ctx, ffmpeg.GetStreamsInfosConfig{
			Path: cmd.StringArg("inputfile"),
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
		scenesConfig := ffmpeg.ScenesDetectionConfig{
			NVDec:           cmd.Bool(nvdecFlagName),
			NVDevice:        cmd.Int(nvidiaGPUIndexFlagName),
			VAAPIDec:        cmd.Bool(vaapiDecFlagName),
			VAAPIDevice:     cmd.String(vaapiRendererPathFlagName),
			D3D12Dec:        cmd.Bool(d3d12DecFlagName),
			D3D12Device:     cmd.Int(d3d12vaGPUIndexFlagName),
			VideoToolboxDec: cmd.Bool(videoToolboxDecFlagName),
		}
		start := time.Now()
		scenes, err := liveDetectScenes(ctx, cmd.StringArg("inputfile"), cmd.Float64(minThresholdFlagName),
			stats.Format.Duration, cmd.Bool(debugFlagName), scenesConfig)
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
		table.Header("Threshold", "Scenes", "Longest", "Std Dev", "Mean", "Shortest", "≤1s %", "≤1s", "≤0.5s")
		for _, candidate := range candidates {
			row := computeCandidateStats(scenes, stats.Format.Duration, candidate)
			table.Append([]string{
				strconv.FormatFloat(candidate, 'f', -1, 64),
				strconv.Itoa(row.scenes),
				row.longest.Round(time.Millisecond).String(),
				row.stddev.Round(time.Millisecond).String(),
				row.mean.Round(time.Millisecond).String(),
				row.shortest.Round(time.Millisecond).String(),
				strconv.FormatFloat(row.short1sPct, 'f', 1, 64) + "%",
				strconv.Itoa(row.short1s),
				strconv.Itoa(row.shortHalf),
			})
		}
		table.Render()
		fmt.Fprint(bypass, buff.String())
		return
	},
}

type candidateStats struct {
	scenes     int
	mean       time.Duration
	stddev     time.Duration
	shortest   time.Duration
	longest    time.Duration
	shortHalf  int
	short1s    int
	short1sPct float64
}

func computeCandidateStats(scenes []ffmpeg.Scene, totalDuration time.Duration, threshold float64) candidateStats {
	// Keep only if scene validate the minimum treshold
	filtered := make([]ffmpeg.Scene, 0, len(scenes))
	for _, s := range scenes {
		if s.Score >= threshold {
			filtered = append(filtered, s)
		}
	}
	// Build scenes durations list
	nbScenes := len(filtered) + 1
	durations := make([]time.Duration, 0, nbScenes)
	if len(filtered) == 0 {
		// no cut, the whole is single scene of totalDuration
		durations = append(durations, totalDuration)
	} else {
		// We do have cuts, let's compute duration per scene with retained cuts
		durations = append(durations, filtered[0].Start)
		for i := 1; i < len(filtered); i++ {
			durations = append(durations, filtered[i].Start-filtered[i-1].Start)
		}
		durations = append(durations, totalDuration-filtered[len(filtered)-1].Start)
	}
	// Extract stats
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
	// Return all stats
	return candidateStats{
		scenes:     nbScenes,
		mean:       time.Duration(mean),
		stddev:     time.Duration(stddev),
		shortest:   minDur,
		longest:    maxDur,
		shortHalf:  shortHalf,
		short1s:    short1s,
		short1sPct: float64(short1s) / float64(nbScenes) * 100,
	}
}
