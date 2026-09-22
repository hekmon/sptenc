package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/hekmon/sptenc/ffmpeg"

	"al.essio.dev/pkg/shellescape"
	"github.com/hekmon/liveprogress/v2"
	"github.com/urfave/cli/v3"
)

var vmafCommand = &cli.Command{
	Name:     "vmaf",
	Aliases:  []string{"v"},
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
		flags = append(flags, hardwareAccelFlags(hwAccelScopeVMAF)...)
		flags = append(flags,
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
		)
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
			return ctx, errors.New("only one hardware decode flag can be set at a time (--nvdec, --vaapi-dec, --d3d12va-dec, --videotoolbox-dec)")
		}
		// Check CUDA VMAF support if requested
		if cmd.Bool(vmafCUDAFlagName) {
			filters, err := ffmpeg.GetFilters(ctx)
			if err != nil {
				return ctx, fmt.Errorf("failed to list ffmpeg filters: %w", err)
			}
			if !filters.HasLibVMAFCUDA() {
				return ctx, fmt.Errorf("CUDA VMAF was requested but libvmaf_cuda is not available in this ffmpeg build; run 'sptenc check' to see available filters")
			}
		}
		return ctx, nil
	},
	Action: func(ctx context.Context, cmd *cli.Command) (err error) {
		/*
		 * Prepare
		 */
		referencePath := cmd.StringArg("reference")
		distortedPath := cmd.StringArg("distorted")

		// Start live progress
		if err = liveprogress.Start(); err != nil {
			return fmt.Errorf("failed to start live progress: %w", err)
		}
		defer liveprogress.Stop(false)
		bypass := liveprogress.Bypass()

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
					fmt.Fprintf(bypass, "ERROR: Failed to delete temporary working directory %s: %s\n",
						shellescape.Quote(workingDir), removeErr,
					)
				}
			}
		}()
		if cmd.Bool(debugFlagName) {
			fmt.Fprintf(bypass, "DEBUG: Temporary directory created: %s\n", shellescape.Quote(workingDir))
		}

		/*
		 * Execute
		 */

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

		// Probe reference file for stream info and frame count (incompatible decoders are ignored)
		fmt.Fprintln(bypass, "Probing reference file...")
		videoStream, _, err := liveProbeVideoCF(ctx, referencePath, cmd.Bool(debugFlagName), decoderCfg)
		if err != nil {
			return fmt.Errorf("failed to probe reference file: %w", err)
		}
		if !videoStream.IsConstantFrameRate() {
			return errors.New("variable frame rate (VFR) content is not supported: VMAF requires CFR for frame-exact alignment")
		}

		// Probe distorted file and validate compatibility
		fmt.Fprintln(bypass, "Probing distorted file...")
		distVideoStream, _, err := liveProbeVideoCF(ctx, distortedPath, cmd.Bool(debugFlagName), decoderCfg)
		if err != nil {
			return fmt.Errorf("failed to probe distorted file: %w", err)
		}
		if !distVideoStream.IsConstantFrameRate() {
			return errors.New("distorted file has variable frame rate (VFR): VMAF requires CFR for frame-exact alignment")
		}

		// Validate frame counts match
		refFrames := videoStream.NbReadFrames
		distFrames := distVideoStream.NbReadFrames
		if refFrames != 0 && distFrames != 0 && refFrames != distFrames {
			return fmt.Errorf("frame count mismatch: reference has %d frames, distorted has %d frames", refFrames, distFrames)
		}

		// Validate frame rates match (same -r will be forced on both inputs)
		if videoStream.RFrameRate != distVideoStream.RFrameRate {
			return fmt.Errorf("frame rate mismatch: reference is %s fps, distorted is %s fps", videoStream.RFrameRate, distVideoStream.RFrameRate)
		}

		totalFrames := refFrames
		if totalFrames == 0 {
			totalFrames = distFrames
		}

		// Compute VMAF with progress
		fmt.Fprintln(bypass, "Computing VMAF...")
		start := time.Now()
		gpuIndex := cmd.Int(nvidiaGPUIndexFlagName)
		report, err := liveVMAF(ctx, ffmpeg.VMAFComputeConfig{
			ReferencePath:     referencePath,
			DistortedPath:     distortedPath,
			InputFrameRate:    videoStream.RFrameRate,
			ReportPath:        filepath.Join(workingDir, "vmaf.json"),
			UltraHD:           videoStream.Height >= ffmpeg.Height4K,
			NoEnhancementGain: cmd.Bool(vmafNegFlagName),
			VMAFCuda:          cmd.Bool(vmafCUDAFlagName),
			GPUID:             &gpuIndex,
			HWDecoderConfig:   decoderCfg,
		}, totalFrames, cmd.Bool(debugFlagName))
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
