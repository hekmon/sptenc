package main

import (
	"fmt"
	"strings"

	"github.com/hekmon/sptenc/ng/ffmpeg"

	"github.com/fatih/color"
	"github.com/olekukonko/tablewriter"
	"github.com/olekukonko/tablewriter/renderer"
	"github.com/olekukonko/tablewriter/tw"
)

var (
	encoders = []string{
		// HEVC
		string(ffmpeg.HEVCEncoderLibx265), string(ffmpeg.HEVCEncoderNVEnc),
		string(ffmpeg.HEVCEncoderVAAPI), string(ffmpeg.HEVCEncoderD3D12VA),
		// AV1
		string(ffmpeg.AV1EncoderLibaom), string(ffmpeg.AV1EncoderSVTAV1),
		string(ffmpeg.AV1EncoderNVEnc), string(ffmpeg.AV1EncoderVAAPI),
	}
	tableConfig = tablewriter.Config{
		Header: tw.CellConfig{
			Formatting: tw.CellFormatting{
				AutoFormat: tw.Off,
			},
			Alignment: tw.CellAlignment{
				PerColumn: []tw.Align{
					tw.AlignLeft, tw.AlignCenter, tw.AlignCenter, tw.AlignCenter, tw.AlignCenter,
				},
			},
		},
		Row: tw.CellConfig{
			Alignment: tw.CellAlignment{
				PerColumn: []tw.Align{
					tw.AlignRight, tw.AlignCenter, tw.AlignCenter, tw.AlignCenter, tw.AlignCenter,
				},
			},
		},
	}
	tableColorCfg = renderer.ColorizedConfig{
		Header: renderer.Tint{
			FG: renderer.Colors{color.Bold},
			Columns: []renderer.Tint{
				{FG: renderer.Colors{color.Underline, color.Bold}}, // column 0
			},
		},
		Column: renderer.Tint{
			Columns: []renderer.Tint{
				{FG: renderer.Colors{color.Bold}}, // column 0
			},
		},
	}
)

func renderHEVCEncodersTable() string {
	var buff strings.Builder
	table := tablewriter.NewTable(&buff,
		tablewriter.WithConfig(tableConfig),
		tablewriter.WithRenderer(renderer.NewColorized(tableColorCfg)),
	)
	table.Header("HEVC", "CPU", "NVIDIA", "AMD", "Intel")
	table.Bulk([][]string{
		{"Linux", string(ffmpeg.HEVCEncoderLibx265), string(ffmpeg.HEVCEncoderNVEnc), string(ffmpeg.HEVCEncoderVAAPI), string(ffmpeg.HEVCEncoderVAAPI)},
		{"Windows", string(ffmpeg.HEVCEncoderLibx265), string(ffmpeg.HEVCEncoderNVEnc), string(ffmpeg.HEVCEncoderD3D12VA), string(ffmpeg.HEVCEncoderD3D12VA)},
		{"MacOS", string(ffmpeg.HEVCEncoderLibx265), "n/a", "n/a", "n/a"},
	})
	table.Render()
	return buff.String()
}

func renderAV1EncodersTable() string {
	var buff strings.Builder
	table := tablewriter.NewTable(&buff,
		tablewriter.WithConfig(tableConfig),
		tablewriter.WithRenderer(renderer.NewColorized(tableColorCfg)),
	)
	table.Header("AV1", "CPU", "NVIDIA", "AMD", "Intel")
	av1CPUEncoders := fmt.Sprintf("%s (ref) / %s (faster)", ffmpeg.AV1EncoderLibaom, ffmpeg.AV1EncoderSVTAV1)
	table.Bulk([][]string{
		{"Linux", av1CPUEncoders, string(ffmpeg.AV1EncoderNVEnc), string(ffmpeg.AV1EncoderVAAPI), string(ffmpeg.AV1EncoderVAAPI)},
		{"Windows", av1CPUEncoders, string(ffmpeg.AV1EncoderNVEnc), "unsupported", "unsupported"},
		{"MacOS", av1CPUEncoders, "n/a", "n/a", "n/a"},
	})
	table.Render()
	return buff.String()
}
