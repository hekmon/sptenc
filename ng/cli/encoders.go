package main

import (
	"fmt"
	"slices"
	"strings"

	"github.com/hekmon/sptenc/ng/ffmpeg"

	"github.com/fatih/color"
	"github.com/olekukonko/tablewriter"
	"github.com/olekukonko/tablewriter/renderer"
	"github.com/olekukonko/tablewriter/tw"
)

var (
	encodersHEVC = []string{
		// HEVC
		string(ffmpeg.HEVCEncoderLibx265), string(ffmpeg.HEVCEncoderNVEnc),
		string(ffmpeg.HEVCEncoderVAAPI), string(ffmpeg.HEVCEncoderD3D12VA),
	}
	encodersAV1 = []string{
		// AV1
		string(ffmpeg.AV1EncoderLibaom), string(ffmpeg.AV1EncoderSVTAV1),
		string(ffmpeg.AV1EncoderNVEnc), string(ffmpeg.AV1EncoderVAAPI),
	}
	allEncoders = append(encodersHEVC, encodersAV1...)
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

func renderHEVCEncodersAvailability(encoders ffmpeg.EncodersInfo) string {
	var buff strings.Builder
	table := tablewriter.NewTable(&buff,
		tablewriter.WithConfig(tableConfig),
		tablewriter.WithRenderer(renderer.NewColorized(tableColorCfg)),
	)
	table.Header("HEVC", "CPU", "NVIDIA", "AMD", "Intel")
	table.Append([]string{
		"Linux",
		strikeIfUnavailable(string(ffmpeg.HEVCEncoderLibx265), encoders.Has(string(ffmpeg.HEVCEncoderLibx265))),
		strikeIfUnavailable(string(ffmpeg.HEVCEncoderNVEnc), encoders.Has(string(ffmpeg.HEVCEncoderNVEnc))),
		strikeIfUnavailable(string(ffmpeg.HEVCEncoderVAAPI), encoders.Has(string(ffmpeg.HEVCEncoderVAAPI))),
		strikeIfUnavailable(string(ffmpeg.HEVCEncoderVAAPI), encoders.Has(string(ffmpeg.HEVCEncoderVAAPI))),
	})
	table.Append([]string{
		"Windows",
		strikeIfUnavailable(string(ffmpeg.HEVCEncoderLibx265), encoders.Has(string(ffmpeg.HEVCEncoderLibx265))),
		strikeIfUnavailable(string(ffmpeg.HEVCEncoderNVEnc), encoders.Has(string(ffmpeg.HEVCEncoderNVEnc))),
		strikeIfUnavailable(string(ffmpeg.HEVCEncoderD3D12VA), encoders.Has(string(ffmpeg.HEVCEncoderD3D12VA))),
		strikeIfUnavailable(string(ffmpeg.HEVCEncoderD3D12VA), encoders.Has(string(ffmpeg.HEVCEncoderD3D12VA))),
	})
	table.Append([]string{
		"MacOS",
		strikeIfUnavailable(string(ffmpeg.HEVCEncoderLibx265), encoders.Has(string(ffmpeg.HEVCEncoderLibx265))),
		"n/a",
		"n/a",
		"n/a",
	})
	table.Render()
	return buff.String()
}

func renderAV1EncodersAvailability(encoders ffmpeg.EncodersInfo) string {
	var buff strings.Builder
	table := tablewriter.NewTable(&buff,
		tablewriter.WithConfig(tableConfig),
		tablewriter.WithRenderer(renderer.NewColorized(tableColorCfg)),
	)
	table.Header("AV1", "CPU", "NVIDIA", "AMD", "Intel")
	cpuLinux := fmt.Sprintf("%s / %s",
		strikeIfUnavailable(string(ffmpeg.AV1EncoderLibaom), encoders.Has(string(ffmpeg.AV1EncoderLibaom))),
		strikeIfUnavailable(string(ffmpeg.AV1EncoderSVTAV1), encoders.Has(string(ffmpeg.AV1EncoderSVTAV1))),
	)
	table.Append([]string{
		"Linux",
		cpuLinux,
		strikeIfUnavailable(string(ffmpeg.AV1EncoderNVEnc), encoders.Has(string(ffmpeg.AV1EncoderNVEnc))),
		strikeIfUnavailable(string(ffmpeg.AV1EncoderVAAPI), encoders.Has(string(ffmpeg.AV1EncoderVAAPI))),
		strikeIfUnavailable(string(ffmpeg.AV1EncoderVAAPI), encoders.Has(string(ffmpeg.AV1EncoderVAAPI))),
	})
	table.Append([]string{
		"Windows",
		cpuLinux,
		strikeIfUnavailable(string(ffmpeg.AV1EncoderNVEnc), encoders.Has(string(ffmpeg.AV1EncoderNVEnc))),
		"unsupported",
		"unsupported",
	})
	table.Append([]string{
		"MacOS",
		cpuLinux,
		"n/a",
		"n/a",
		"n/a",
	})
	table.Render()
	return buff.String()
}

func strikeIfUnavailable(name string, available bool) string {
	if !available {
		return color.New(color.CrossedOut).Sprint(name)
	}
	return name
}

func encoderValidator(e string) error {
	if !slices.Contains(allEncoders, e) {
		return fmt.Errorf("invalid encoder %q, valid values are: %s", e, strings.Join(allEncoders, ", "))
	}
	return nil
}
