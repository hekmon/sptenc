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
		fmt.Println("Environment Check")
		fmt.Println()

		var (
			ffprobeOK      bool
			ffmpegOK       bool
			hasLibx265     bool
			hasHevcNvenc   bool
			hasLibvmaf     bool
			hasLibvmafCUDA bool
		)

		// Check ffprobe
		ffprobeVersion, ffprobeErr := ffmpeg.GetFFProbeVersion(ctx)
		if ffprobeErr == nil {
			ffprobeOK = true
		}
		renderCheckTable("ffprobe", ffprobeVersion.Version, ffprobeErr, nil)

		// Check ffmpeg
		ffmpegVersion, ffmpegErr := ffmpeg.GetFFMPEGVersion(ctx)
		var ffmpegDetails [][2]string
		if ffmpegErr == nil {
			encoders, encErr := ffmpeg.GetEncoders(ctx)
			if encErr != nil {
				ffmpegErr = fmt.Errorf("failed to list encoders: %w", encErr)
			} else {
				hasLibx265 = encoders.Has("libx265")
				hasHevcNvenc = encoders.Has("hevc_nvenc")
				ffmpegDetails = append(ffmpegDetails, [2]string{"libx265 encoder", boolToEmoji(hasLibx265)})
				ffmpegDetails = append(ffmpegDetails, [2]string{"hevc_nvenc encoder", boolToEmoji(hasHevcNvenc)})
			}

			filters, filtErr := ffmpeg.GetFilters(ctx)
			if filtErr != nil {
				ffmpegErr = fmt.Errorf("failed to list filters: %w", filtErr)
			} else {
				hasLibvmaf = filters.HasLibVMAF()
				hasLibvmafCUDA = filters.HasLibVMAFCUDA()
				ffmpegDetails = append(ffmpegDetails, [2]string{"libvmaf filter", boolToEmoji(hasLibvmaf)})
				ffmpegDetails = append(ffmpegDetails, [2]string{"libvmaf_cuda filter", boolToEmoji(hasLibvmafCUDA)})
			}
		}
		if ffmpegErr == nil {
			ffmpegOK = true
		}
		renderCheckTable("ffmpeg", ffmpegVersion.Version, ffmpegErr, ffmpegDetails)

		// Determine status
		status, statusEmoji := computeStatus(ffprobeOK, ffmpegOK, hasLibvmaf, hasLibx265 || hasHevcNvenc, hasLibx265 && hasHevcNvenc && hasLibvmafCUDA)
		fmt.Printf("\nStatus: %s %s\n", statusEmoji, status)

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

func renderCheckTable(name, version string, checkErr error, details [][2]string) {
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
	)

	if checkErr != nil {
		t.Append([]string{"Present", "❌"})
		t.Append([]string{"Error", checkErr.Error()})
	} else {
		t.Append([]string{"Present", "✅"})
		if version != "" {
			t.Append([]string{"Version", version})
		}
		for _, d := range details {
			t.Append([]string{d[0], d[1]})
		}
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
