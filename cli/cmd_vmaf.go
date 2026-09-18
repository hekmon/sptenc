package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"al.essio.dev/pkg/shellescape"
	"github.com/hekmon/liveprogress/v2"
	"github.com/hekmon/sptenc/ffmpeg"
	"github.com/urfave/cli/v3"
)

var vmafCommand = &cli.Command{
	Name:     "vmaf",
	Category: "Tooling",
	Usage:    "Compute VMAF between a reference and a distorted video",
	Description: "Compare a distorted (encoded) video against its reference (original) using VMAF.\n\n" +
		"This computes the full VMAF report and prints summary statistics including percentiles,\n" +
		"mean, harmonic mean, min and max scores.\n\n" +
		"HARDWARE ACCELERATION\n" +
		"Use --" + nvdecFlagName + " (NVIDIA), --" + vaapiDecFlagName + " (Intel/AMD), --" + d3d12DecFlagName + " (Windows),\n" +
		"or --" + videoToolboxDecFlagName + " (macOS) to offload frame decoding to the GPU. This reserves\n" +
		"CPU cycles for the software libvmaf computation. Each decoder is activated only if the\n" +
		"input codec is compatible.\n\n" +
		"Use --" + vmafCUDAFlagName + " to additionally accelerate the VMAF computation itself on the GPU\n" +
		"(requires libvmaf_cuda, not available in standard ffmpeg builds). When this is enabled,\n" +
		"NVDEC decoding is automatically used for compatible input codecs; --" + nvdecFlagName + " is implied.",
	Flags: func() (flags []cli.Flag) {
		flags = []cli.Flag{
			&cli.BoolFlag{
				Name:     nvdecFlagName,
				Usage:    "Use NVDEC hardware-accelerated decoding for compatible codecs (NVIDIA GPU required)",
				Value:    false,
				OnlyOnce: true,
				Category: "Hardware accelerated decoding",
			},
			&cli.BoolFlag{
				Name:     vaapiDecFlagName,
				Usage:    "Use VA-API hardware-accelerated decoding (Intel/AMD GPU required)",
				Value:    false,
				OnlyOnce: true,
				Category: "Hardware accelerated decoding",
			},
			&cli.BoolFlag{
				Name:     d3d12DecFlagName,
				Usage:    "Use D3D12VA hardware-accelerated decoding (Windows, GPU required)",
				Value:    false,
				OnlyOnce: true,
				Category: "Hardware accelerated decoding",
			},
			&cli.BoolFlag{
				Name:     videoToolboxDecFlagName,
				Usage:    "Use VideoToolbox hardware-accelerated decoding (macOS, Apple Silicon)",
				Value:    false,
				OnlyOnce: true,
				Category: "Hardware accelerated decoding",
			},
			&cli.IntFlag{
				Name:     nvidiaGPUIndexFlagName,
				Usage:    "GPU to use with --" + nvdecFlagName + " or --" + vmafCUDAFlagName,
				Value:    ffmpeg.CUDADefaultDevice,
				OnlyOnce: true,
				Category: "Hardware accelerated decoding",
			},
			&cli.StringFlag{
				Name:     vaapiRendererPathFlagName,
				Usage:    "Direct Rendering Manager render node to use with --" + vaapiDecFlagName,
				Value:    ffmpeg.VAAPIDefaultDevice,
				OnlyOnce: true,
				Category: "Hardware accelerated decoding",
			},
			&cli.IntFlag{
				Name:     d3d12vaGPUIndexFlagName,
				Usage:    "GPU to use with --" + d3d12DecFlagName,
				Value:    ffmpeg.D3D12VADefaultDevice,
				OnlyOnce: true,
				Category: "Hardware accelerated decoding",
			},
			&cli.BoolFlag{
				Name:     vmafCUDAFlagName,
				Usage:    "Use CUDA acceleration for VMAF computation",
				Value:    false,
				OnlyOnce: true,
				Category: "VMAF",
			},
			&cli.BoolFlag{
				Name:     vmafNegFlagName,
				Usage:    "Use VMAF NEG models",
				Value:    false,
				OnlyOnce: true,
				Category: "VMAF",
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
		return
	}(),
	Arguments: []cli.Argument{
		&cli.StringArg{
			Name:      "reference",
			UsageText: "<reference file>",
		},
		&cli.StringArg{
			Name:      "distorted",
			UsageText: "<distorted file>",
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
		if cmd.Args().Len() != 2 {
			return ctx, errors.New("exactly two arguments are required: reference file and distorted file")
		}
		referencePath := cmd.Args().First()
		distortedPath := cmd.Args().Get(1)
		// Validate reference file
		refInfo, err := os.Stat(referencePath)
		if err != nil {
			return ctx, fmt.Errorf("failed to access reference file: %w", err)
		}
		if !refInfo.Mode().IsRegular() {
			return ctx, errors.New("reference file must be a regular file")
		}
		// Validate distorted file
		distInfo, err := os.Stat(distortedPath)
		if err != nil {
			return ctx, fmt.Errorf("failed to access distorted file: %w", err)
		}
		if !distInfo.Mode().IsRegular() {
			return ctx, errors.New("distorted file must be a regular file")
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
			return ctx, errors.New("only one hardware decode flag can be set at a time (--nvdec, --vaapidec, --d3d12dec, --videotoolboxdec)")
		}
		// Check CUDA VMAF support if requested
		if cmd.Bool(vmafCUDAFlagName) {
			filters, err := ffmpeg.GetFilters(ctx)
			if err != nil {
				return ctx, fmt.Errorf("failed to list ffmpeg filters: %w", err)
			}
			if !filters.HasLibVMAFCUDA() {
				return ctx, fmt.Errorf("CUDA VMAF was requested but libvmaf_cuda is not available in this ffmpeg build; run 'sptenc verify' to see available filters")
			}
		}
		return ctx, nil
	},
	Action: func(ctx context.Context, cmd *cli.Command) (err error) {
		referencePath := cmd.StringArg("reference")
		distortedPath := cmd.StringArg("distorted")

		// Start live progress
		if err = liveprogress.Start(); err != nil {
			return fmt.Errorf("failed to start live progress: %w", err)
		}
		defer liveprogress.Stop(false)

		bypass := liveprogress.Bypass()

		// Probe reference file for stream info and frame count
		fmt.Fprintln(bypass, "Probing reference file...")
		refStats, err := getStreamsInfosCF(ctx, referencePath, cmd.Bool(debugFlagName))
		if err != nil {
			return fmt.Errorf("failed to probe reference file: %w", err)
		}
		videoStream := refStats.VideoTrack()
		if videoStream == nil {
			return errors.New("reference file has no video stream")
		}
		if !videoStream.IsConstantFrameRate() {
			return errors.New("variable frame rate (VFR) content is not supported: VMAF requires CFR for frame-exact alignment")
		}

		totalFrames := videoStream.NbReadFrames
		if totalFrames == 0 {
			totalFrames = videoStream.NbFrames
		}

		// Build hardware decode config and warn on incompatible codecs
		decoderCfg := ffmpeg.HWDecoderConfig{
			NVDec:           cmd.Bool(nvdecFlagName),
			NVDevice:        cmd.Int(nvidiaGPUIndexFlagName),
			VAAPIDec:        cmd.Bool(vaapiDecFlagName),
			VAAPIDevice:     cmd.String(vaapiRendererPathFlagName),
			D3D12Dec:        cmd.Bool(d3d12DecFlagName),
			D3D12Device:     cmd.Int(d3d12vaGPUIndexFlagName),
			VideoToolboxDec: cmd.Bool(videoToolboxDecFlagName),
		}
		if decoderCfg.NVDec || decoderCfg.VAAPIDec || decoderCfg.D3D12Dec || decoderCfg.VideoToolboxDec {
			distortedDec := ffmpeg.SelectCompatibleDecoders(ctx, distortedPath,
				decoderCfg.NVDec, decoderCfg.VAAPIDec, decoderCfg.D3D12Dec, decoderCfg.VideoToolboxDec,
				decoderCfg.NVDevice, decoderCfg.VAAPIDevice, decoderCfg.D3D12Device,
			)
			referenceDec := ffmpeg.SelectCompatibleDecoders(ctx, referencePath,
				decoderCfg.NVDec, decoderCfg.VAAPIDec, decoderCfg.D3D12Dec, decoderCfg.VideoToolboxDec,
				decoderCfg.NVDevice, decoderCfg.VAAPIDevice, decoderCfg.D3D12Device,
			)
			if !distortedDec.NVDec && !distortedDec.VAAPIDec && !distortedDec.D3D12Dec && !distortedDec.VideoToolboxDec {
				fmt.Fprintln(bypass, "WARNING: distorted codec is not compatible with the requested hardware decoder, falling back to software decode for distorted file")
			}
			if !referenceDec.NVDec && !referenceDec.VAAPIDec && !referenceDec.D3D12Dec && !referenceDec.VideoToolboxDec {
				fmt.Fprintln(bypass, "WARNING: reference codec is not compatible with the requested hardware decoder, falling back to software decode for reference file")
			}
		}

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

		// Compute VMAF with progress
		fmt.Fprintln(bypass, "Computing VMAF...")
		start := time.Now()
		barOpts := []liveprogress.BarOption{
			liveprogress.WithMultiplyRunes(),
			liveprogress.WithSameAutoSizeInternalPadding(true, false),
			liveprogress.WithPrependDecorator(func(bar *liveprogress.Bar) string {
				return "       VMAF | "
			}),
			liveprogress.WithPrependTimeElapsed(liveprogress.BaseStyle()),
			liveprogress.WithAppendPercent(liveprogress.BaseStyle()),
			liveprogress.WithAppendTimeRemaining(liveprogress.BaseStyle()),
			liveprogress.WithAppendDecorator(func(bar *liveprogress.Bar) string {
				return fmt.Sprintf(" | %d/%d frames", bar.Current(), bar.Total())
			}),
		}
		if totalFrames > 0 {
			barOpts = append(barOpts, liveprogress.WithTotal(uint64(totalFrames)))
		}
		vmafBar := liveprogress.AddBar(barOpts...)
		defer liveprogress.RemoveBar(vmafBar)

		gpuIndex := cmd.Int(nvidiaGPUIndexFlagName)
		report, err := ffmpeg.VMAFCompute(ctx, ffmpeg.VMAFComputeConfig{
			ReferencePath:     referencePath,
			DistortedPath:     distortedPath,
			InputFrameRate:    videoStream.RFrameRate,
			ReportPath:        filepath.Join(workingDir, "vmaf.json"),
			UltraHD:           videoStream.Height >= ffmpeg.Height4K,
			NoEnhancementGain: cmd.Bool(vmafNegFlagName),
			VMAFCuda:          cmd.Bool(vmafCUDAFlagName),
			GPUID:             &gpuIndex,
			HWDecoderConfig:   decoderCfg,
			Debug: func(s string) {
				if cmd.Bool(debugFlagName) {
					fmt.Fprintf(bypass, "DEBUG: %s\n", s)
				}
			},
			RuntimeError: func(err error) {
				fmt.Fprintf(bypass, "ERROR: %s\n", err)
			},
			FFMPEGStatsReport: func(stats ffmpeg.ProgressStats) {
				vmafBar.CurrentSet(uint64(stats.CurrentFrame))
			},
		})
		if err != nil {
			return fmt.Errorf("failed to compute VMAF: %w", err)
		}

		stats := report.GetStats()
		fmt.Fprintf(bypass, "\nVMAF computed in %s:\n\n%s\n",
			time.Since(start).Round(time.Second),
			stats,
		)
		return nil
	},
}
