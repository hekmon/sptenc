package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/hekmon/sptenc/ng/ffmpeg"

	"github.com/olekukonko/tablewriter"
	"github.com/olekukonko/tablewriter/renderer"
	"github.com/olekukonko/tablewriter/tw"
	"github.com/urfave/cli/v3"
)

var checkCommand = &cli.Command{
	Name:        "check",
	Aliases:     []string{"c"},
	Usage:       "Verify third-party tools are present and usable",
	Description: "Check that required external tools are available and functional: ffmpeg (with libx265 and libvmaf) and ffprobe",
	Action: func(ctx context.Context, cmd *cli.Command) (err error) {
		fmt.Println()
		fmt.Println("Third-Party Tools Check")
		fmt.Println()
		var (
			ffprobeOK      bool
			ffmpegOK       bool
			hasLibvmaf     bool
			hasLibvmafCUDA bool
			hasAnyHEVC     bool
			hasAnyAV1      bool
		)
		// Check ffprobe
		ffprobeVersion, ffprobeErr := ffmpeg.GetFFProbeVersion(ctx)
		if ffprobeErr == nil {
			ffprobeOK = true
		}
		ffprobeRows := buildTableRows(ffprobeVersion.Version, ffprobeErr, nil)
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
			encoders, encErr := ffmpeg.GetEncoders(ctx)
			if encErr != nil {
				ffmpegErr = fmt.Errorf("failed to list encoders: %w", encErr)
			} else {
				// HEVC
				hevcEncoders := []struct {
					name string
					has  bool
				}{
					{"libx265", encoders.Has("libx265")},
					{"hevc_nvenc", encoders.Has("hevc_nvenc")},
					{"hevc_vaapi", encoders.Has("hevc_vaapi")},
				}
				for _, e := range hevcEncoders {
					ffmpegDetails = append(ffmpegDetails, [2]string{fmt.Sprintf("HEVC: %s", e.name), boolToEmoji(e.has)})
					if e.has {
						hasAnyHEVC = true
					}
				}
				// AV1
				av1Encoders := []struct {
					name string
					has  bool
				}{
					{"libaom-av1", encoders.Has("libaom-av1")},
					{"av1_nvenc", encoders.Has("av1_nvenc")},
					{"av1_vaapi", encoders.Has("av1_vaapi")},
				}
				for _, e := range av1Encoders {
					ffmpegDetails = append(ffmpegDetails, [2]string{fmt.Sprintf("AV1: %s", e.name), boolToEmoji(e.has)})
					if e.has {
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
		maxLabelWidth := maxLabelLen(ffprobeRows, ffmpegRows)
		renderCheckTable("🔍  ffprobe", ffprobeRows, maxLabelWidth)
		renderCheckTable("🎬  ffmpeg", ffmpegRows, maxLabelWidth)
		// Determine status
		status, statusEmoji := computeStatus(ffprobeOK, ffmpegOK, hasLibvmaf, hasAnyHEVC || hasAnyAV1, hasAnyHEVC && hasAnyAV1 && hasLibvmafCUDA)
		fmt.Printf("\n  Status: %s %s\n\n", statusEmoji, status)
		if status == "not ok" {
			return fmt.Errorf("one or more required tools are missing or misconfigured")
		}
		return nil
	},
}

func computeStatus(ffprobeOK, ffmpegOK, hasLibvmaf, hasAnyEncoder, hasAllOptional bool) (status, emoji string) {
	if !ffprobeOK || !ffmpegOK || !hasLibvmaf || !hasAnyEncoder {
		return "not ok", "❌"
	}
	if hasAllOptional {
		return "full ok", "✅"
	}
	return "partial ok", "⚠️"
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
				Alignment: tw.CellAlignment{PerColumn: []tw.Align{tw.AlignLeft, tw.AlignCenter}},
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
