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
	"github.com/hekmon/sptenc/pipeline"

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
		"--" + minThresholdFlagName + ", --" + maxThresholdFlagName + ", --" + maxCandidatesFlagName + ", --" + minDropFlagName + ", and --" + minSegmentLengthFlagName + " before running\n" +
		"batchsearch, where a bad range can cost hours of encoding time.\n\n" +
		"WHY THRESHOLD SELECTION MATTERS\n" +
		"Scene detection splits a video into independent segments. Each segment gets its own QP, so\n" +
		"splitting finely lets hard passages use low QP and easy ones high. But every split forces an\n" +
		"I-frame, and short runs starve B/P compression.\n\n" +
		"Split coarsely and B/P frames thrive across long runs, yet the whole segment must bow to its\n" +
		"hardest passage — easy sections pay for quality they do not need. Worse, a short complex\n" +
		"passage inside a long easy segment can fail VMAF locally while the segment-wide average still\n" +
		"passes. The bad frames are statistically invisible, undermining the guarantee that every part\n" +
		"of the video meets your quality floor.\n\n" +
		"There is no single right threshold. This command lets you preview the tradeoffs so you can\n" +
		"choose based on your own tolerance for file size vs quality visibility.\n\n" +
		"SEGMENT LENGTH\n" +
		"The --" + minSegmentLengthFlagName + " filter removes boundaries that would create segments shorter\n" +
		"than the given duration. It is applied before candidate generation, so the auto-tuner and the\n" +
		"table both operate on the same post-filter scene count that encode and batchsearch will use.\n" +
		"When disabled (--" + minSegmentLengthFlagName + " 0), the table shows raw distributions plus a column\n" +
		"indicating how many segments the default filter would catch.\n\n" +
		"The candidate generation logic is identical to batchsearch, so the preview is a\n" +
		"faithful map of what the search will explore.",
	Flags: func() []cli.Flag {
		flags := []cli.Flag{}
		flags = append(flags, thresholdSearchFlags()...)
		flags = append(flags, segmentFilterFlag(thresholdCategoryName))
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
		if minSegLen := cmd.Duration(minSegmentLengthFlagName); minSegLen > 0 {
			fmt.Fprintf(bypass, "\t• minimum segment length: %s\n", minSegLen)
		}
		fmt.Fprintln(bypass)
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
		// Apply min-segment-length filter to the full scene list once. All downstream
		// steps (candidate generation and table stats) use this filtered list.
		minSegLen := cmd.Duration(minSegmentLengthFlagName)
		if minSegLen > 0 {
			filtered := pipeline.FilterShortScenes(scenes, stats.Format.Duration, minSegLen)
			if removed := len(scenes) - len(filtered); removed > 0 {
				fmt.Fprintf(bypass, "\tMerged %d boundaries to enforce min segment length of %s → %d scenes\n",
					removed, minSegLen, 1+len(filtered))
			}
			scenes = filtered
		}
		// Cap scenes to the max threshold
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
		filterActive := minSegLen > 0
		var buff strings.Builder
		cfg := thresholdsTableConfig
		if filterActive {
			// 6 columns when filter is active (no Merged: all merging happened upfront)
			cfg = tablewriter.Config{
				Header: tw.CellConfig{
					Formatting: tw.CellFormatting{
						AutoFormat: tw.Off,
					},
				},
				Row: tw.CellConfig{
					Alignment: tw.CellAlignment{
						PerColumn: []tw.Align{
							tw.AlignRight, tw.AlignRight, tw.AlignRight, tw.AlignRight, tw.AlignRight, tw.AlignRight,
						},
					},
				},
			}
		}
		table := tablewriter.NewTable(&buff, tablewriter.WithConfig(cfg))
		if filterActive {
			table.Header("Threshold", "Scenes", "Longest", "Std Dev", "Mean", "Shortest")
		} else {
			table.Header("Threshold", "Scenes", "Longest", "Std Dev", "Mean", "Shortest",
				fmt.Sprintf("≤%s", minSegmentLengthDefault), "≤1s", "≤0.5s")
		}
		for _, candidate := range candidates {
			row := computeCandidateStats(scenes, stats.Format.Duration, candidate, minSegLen)
			if filterActive {
				table.Append([]string{
					strconv.FormatFloat(candidate, 'f', -1, 64),
					strconv.Itoa(row.scenes),
					row.longest.Round(time.Millisecond).String(),
					row.stddev.Round(time.Millisecond).String(),
					row.mean.Round(time.Millisecond).String(),
					row.shortest.Round(time.Millisecond).String(),
				})
			} else {
				table.Append([]string{
					strconv.FormatFloat(candidate, 'f', -1, 64),
					strconv.Itoa(row.scenes),
					row.longest.Round(time.Millisecond).String(),
					row.stddev.Round(time.Millisecond).String(),
					row.mean.Round(time.Millisecond).String(),
					row.shortest.Round(time.Millisecond).String(),
					strconv.Itoa(row.subDefault),
					strconv.Itoa(row.short1s),
					strconv.Itoa(row.shortHalf),
				})
			}
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
	merged     int // boundaries removed by filter (when active)
	subDefault int // segments ≤ default min (when disabled)
}

func computeCandidateStats(scenes []ffmpeg.Scene, totalDuration time.Duration, threshold float64, minSegLen time.Duration) candidateStats {
	// Keep only scenes that meet the threshold
	filtered := make([]ffmpeg.Scene, 0, len(scenes))
	for _, s := range scenes {
		if s.Score >= threshold {
			filtered = append(filtered, s)
		}
	}

	// Build segment durations from threshold-filtered boundaries
	durations := buildDurations(filtered, totalDuration)

	if minSegLen > 0 {
		// Input is already pre-filtered upstream; just compute stats.
		return buildStats(durations, 0, 0, 0, 0)
	}

	// Filter disabled: compute raw stats and simulate what the default filter would catch.
	shortHalf := 0
	short1s := 0
	subDefault := 0
	for _, d := range durations {
		if d < 500*time.Millisecond {
			shortHalf++
		}
		if d < time.Second {
			short1s++
		}
		if d <= minSegmentLengthDefault {
			subDefault++
		}
	}
	return buildStats(durations, 0, shortHalf, short1s, subDefault)
}

// buildDurations converts a list of scene boundaries into segment durations.
func buildDurations(scenes []ffmpeg.Scene, totalDuration time.Duration) []time.Duration {
	durations := make([]time.Duration, 0, len(scenes)+1)
	if len(scenes) == 0 {
		durations = append(durations, totalDuration)
	} else {
		durations = append(durations, scenes[0].Start)
		for i := 1; i < len(scenes); i++ {
			durations = append(durations, scenes[i].Start-scenes[i-1].Start)
		}
		durations = append(durations, totalDuration-scenes[len(scenes)-1].Start)
	}
	return durations
}

// buildStats computes duration statistics from a segment durations slice.
func buildStats(durations []time.Duration, merged, shortHalf, short1s, subDefault int) candidateStats {
	durationsFloat := make([]float64, len(durations))
	minDur := durations[0]
	maxDur := durations[0]
	for i, d := range durations {
		durationsFloat[i] = float64(d)
		if d < minDur {
			minDur = d
		}
		if d > maxDur {
			maxDur = d
		}
	}
	mean, stddev := stat.MeanStdDev(durationsFloat, nil)
	return candidateStats{
		scenes:     len(durations),
		mean:       time.Duration(mean),
		stddev:     time.Duration(stddev),
		shortest:   minDur,
		longest:    maxDur,
		shortHalf:  shortHalf,
		short1s:    short1s,
		short1sPct: float64(short1s) / float64(len(durations)) * 100,
		merged:     merged,
		subDefault: subDefault,
	}
}
