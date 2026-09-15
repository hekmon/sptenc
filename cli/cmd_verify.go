package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/hekmon/sptenc/ffmpeg"
	"github.com/hekmon/sptenc/mkvtoolnix"

	"github.com/olekukonko/tablewriter"
	"github.com/olekukonko/tablewriter/renderer"
	"github.com/olekukonko/tablewriter/tw"
	"github.com/urfave/cli/v3"
)

var verifyCommand = &cli.Command{
	Name:        "verify",
	Aliases:     []string{"v"},
	Usage:       "Verify third-party tools are present and usable",
	Description: "Check that required external tools are available and functional: ffmpeg (with libx265 and libvmaf), ffprobe and mkvpropedit.\n\nENCODERS\nUse GPU for quick VMAF profile testing but always prefer CPU encoders for final encode (lower file size).",
	Action: func(ctx context.Context, cmd *cli.Command) (err error) {
		fmt.Println()
		fmt.Println("Third-Party Tools Check")
		fmt.Println()
		fmt.Println()
		var (
			ffprobeOK      bool
			ffmpegOK       bool
			mkvpropeditOK  bool
			hasLibvmaf     bool
			hasLibvmafCUDA bool
			hasAnyHEVC     bool
			hasAnyAV1      bool
			encoders       ffmpeg.EncodersInfo
		)
		// Check ffprobe
		ffprobeVersion, ffprobeErr := ffmpeg.GetFFProbeVersion(ctx)
		if ffprobeErr == nil {
			ffprobeOK = true
		}
		ffprobeRows := buildTableRows(ffprobeVersion.Version, ffprobeErr, nil)
		// Check mkvpropedit
		mkvpropeditVersion, mkvpropeditErr := mkvtoolnix.GetMKVPropEditVersion(ctx)
		if mkvpropeditErr == nil {
			mkvpropeditOK = true
		}
		mkvpropeditRows := buildTableRows(mkvpropeditVersion, mkvpropeditErr, nil)
		// Check ffmpeg
		ffmpegVersion, ffmpegErr := ffmpeg.GetFFMPEGVersion(ctx)
		var ffmpegDetails [][2]string
		if ffmpegErr == nil {
			// Check filters first (core of the program)
			filters, filtErr := ffmpeg.GetFilters(ctx)
			if filtErr != nil {
				ffmpegErr = fmt.Errorf("failed to list filters: %w", filtErr)
			} else {
				hasLibvmaf = filters.HasLibVMAF()
				hasLibvmafCUDA = filters.HasLibVMAFCUDA()
				ffmpegDetails = append(ffmpegDetails, [2]string{"libvmaf", boolToEmoji(hasLibvmaf)})
				ffmpegDetails = append(ffmpegDetails, [2]string{"libvmaf_cuda", boolToEmoji(hasLibvmafCUDA)})
			}
			// Check encoders
			var encErr error
			encoders, encErr = ffmpeg.GetEncoders(ctx)
			if encErr != nil {
				ffmpegErr = fmt.Errorf("failed to list encoders: %w", encErr)
			} else {
				// HEVC
				for _, e := range encodersHEVC {
					has := encoders.Has(e)
					ffmpegDetails = append(ffmpegDetails, [2]string{fmt.Sprintf("HEVC: %s", e), boolToEmoji(has)})
					if has {
						hasAnyHEVC = true
					}
				}
				// AV1
				for _, e := range encodersAV1 {
					has := encoders.Has(e)
					ffmpegDetails = append(ffmpegDetails, [2]string{fmt.Sprintf("AV1: %s", e), boolToEmoji(has)})
					if has {
						hasAnyAV1 = true
					}
				}
			}
		}
		if ffmpegErr == nil {
			ffmpegOK = true
		}
		ffmpegRows := buildTableRows(ffmpegVersion.Version, ffmpegErr, ffmpegDetails)
		// Compute common label width across all tables
		maxLabelWidth := maxLabelLen(ffprobeRows, mkvpropeditRows, ffmpegRows)
		renderCheckTable("🔍  ffprobe", ffprobeRows, maxLabelWidth)
		renderCheckTable("📦  mkvpropedit", mkvpropeditRows, maxLabelWidth)
		renderCheckTable("🎬  ffmpeg", ffmpegRows, maxLabelWidth)
		// Print encoder availability tables
		if ffmpegOK {
			fmt.Println()
			fmt.Println("Encoder Availability")
			fmt.Println()
			fmt.Println(renderHEVCEncodersAvailability(encoders))
			fmt.Println(renderAV1EncodersAvailability(encoders))
		}
		// Determine status
		status, statusEmoji := computeStatus(ffprobeOK, ffmpegOK, mkvpropeditOK, hasLibvmaf, hasAnyHEVC || hasAnyAV1)
		fmt.Printf("\n  Status: %s %s\n\n", statusEmoji, status)
		if status == "not ok" {
			return fmt.Errorf("one or more required tools are missing or misconfigured")
		}
		return nil
	},
}

func computeStatus(ffprobeOK, ffmpegOK, mkvpropeditOK, hasLibvmaf, hasAnyEncoder bool) (status, emoji string) {
	if !ffprobeOK || !ffmpegOK || !mkvpropeditOK || !hasLibvmaf || !hasAnyEncoder {
		return "not ok", "❌"
	}
	return "ok", "✅"
}

func buildTableRows(version string, checkErr error, details [][2]string) [][2]string {
	var rows [][2]string
	if checkErr != nil {
		rows = append(rows, [2]string{"Present", "❌"})
		rows = append(rows, [2]string{"Error", checkErr.Error()})
		return rows
	}
	rows = append(rows, [2]string{"Present", "✅"})
	if version != "" {
		rows = append(rows, [2]string{"Version", version})
	}
	rows = append(rows, details...)
	return rows
}

func maxLabelLen(tables ...[][2]string) int {
	max := 0
	for _, table := range tables {
		for _, row := range table {
			if l := len(row[0]); l > max {
				max = l
			}
		}
	}
	return max
}

func renderCheckTable(name string, rows [][2]string, labelWidth int) {
	var buf strings.Builder
	t := tablewriter.NewTable(&buf,
		tablewriter.WithRenderer(renderer.NewBlueprint(tw.Rendition{
			Borders: tw.BorderNone,
			Settings: tw.Settings{
				Separators: tw.Separators{
					BetweenRows:    tw.Off,
					BetweenColumns: tw.On,
				},
			},
		})),
		tablewriter.WithConfig(tablewriter.Config{
			Row: tw.CellConfig{
				Alignment: tw.CellAlignment{PerColumn: []tw.Align{tw.AlignLeft, tw.AlignLeft}},
				Formatting: tw.CellFormatting{
					AutoWrap: tw.WrapNone,
				},
			},
		}),
	)
	for _, row := range rows {
		t.Append([]string{fmt.Sprintf("%*s", labelWidth, row[0]), row[1]})
	}
	t.Render()
	fmt.Printf("%s\n%s\n", name, buf.String())
}

func boolToEmoji(v bool) string {
	if v {
		return "✅"
	}
	return "❌"
}
