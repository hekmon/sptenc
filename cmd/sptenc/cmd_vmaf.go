package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/hekmon/sptenc/core"
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
		"MODEL\n" +
		"The VMAF model is selected from the height of the reference (see MANUAL.md, Models):\n" +
		"--" + vmafModelFlagName + " forces one. libvmaf 3.2.0 or newer is required (VMAF v1 models).\n\n" +
		"HARDWARE ACCELERATION\n" +
		"Use --" + nvdecFlagName + " (NVIDIA), --" + vaapiDecFlagName + " (Intel/AMD), --" + d3d12DecFlagName + " (Windows),\n" +
		"or --" + videoToolboxDecFlagName + " (macOS) to offload frame decoding to the GPU. This reserves\n" +
		"CPU cycles for the libvmaf computation, which runs on the CPU: the VMAF v1 models have no\n" +
		"CUDA implementation. Each decoder is activated only if the input codec is compatible.",
	Flags: func() (flags []cli.Flag) {
		flags = append(flags, hardwareAccelFlags(hwAccelScopeDecode)...)
		flags = append(flags,
			vmafModelFlag("VMAF"),
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
		// Only one decode flag at a time
		if _, err := hwDecodeFlags(cmd); err != nil {
			return ctx, err
		}
		// libvmaf must know the model before anything is decoded
		if err := checkLibVMAF(ctx, cmd); err != nil {
			return ctx, err
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
		liveprogress.AddCustomLine(func() string { return "" }) // separate logs from live status updates

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
		globalStart := time.Now()
		fmt.Fprintf(bypass, "Computing VMAF of %s against %s\n",
			shellescape.Quote(filepath.Base(distortedPath)),
			shellescape.Quote(filepath.Base(referencePath)),
		)

		// Hardware decoder: the decode flags (validated in Before). Warn when a file can not be
		// decoded by it, the ffmpeg functions fall back by themselves.
		decoderCfg, _ := hwDecodeFlags(cmd)
		if decoderCfg.Enabled() {
			if !decoderCfg.CompatibleWith(ctx, distortedPath).Enabled() {
				fmt.Fprintf(bypass, "WARNING: distorted codec is not decoded with %s (not supported, or not decoded exactly: see MANUAL.md, Hardware decoding), falling back to software decode for distorted file\n", decoderCfg.Name())
			}
			if !decoderCfg.CompatibleWith(ctx, referencePath).Enabled() {
				fmt.Fprintf(bypass, "WARNING: reference codec is not decoded with %s (not supported, or not decoded exactly: see MANUAL.md, Hardware decoding), falling back to software decode for reference file\n", decoderCfg.Name())
			}
		}

		// Probe reference file for stream info and frame count (incompatible decoders are ignored)
		fmt.Fprintln(bypass, "Counting the frames of the reference file...")
		start := time.Now()
		videoStream, _, err := liveProbeVideoCF(ctx, referencePath, cmd.Bool(debugFlagName), decoderCfg)
		if err != nil {
			return fmt.Errorf("failed to probe reference file: %w", err)
		}
		fmt.Fprintf(bypass, "\tCounted %d %s frames in %s\n",
			videoStream.NbReadFrames, videoStream.CodecName, time.Since(start).Round(time.Second),
		)
		if !videoStream.IsConstantFrameRate() {
			return fmt.Errorf("variable frame rate (VFR) content is not supported: reference frames last from %s to %s while it declares a constant frame rate (%s fps)",
				videoStream.ShortestFrameDuration, videoStream.LongestFrameDuration, videoStream.RFrameRate)
		}
		model := resolveVMAFModel(cmd, bypass, videoStream)
		if err = checkVMAFPictures(ctx, cmd, model, videoStream); err != nil {
			return err
		}

		// Probe distorted file and validate compatibility
		fmt.Fprintln(bypass, "Counting the frames of the distorted file...")
		start = time.Now()
		distVideoStream, _, err := liveProbeVideoCF(ctx, distortedPath, cmd.Bool(debugFlagName), decoderCfg)
		if err != nil {
			return fmt.Errorf("failed to probe distorted file: %w", err)
		}
		fmt.Fprintf(bypass, "\tCounted %d %s frames in %s\n",
			distVideoStream.NbReadFrames, distVideoStream.CodecName, time.Since(start).Round(time.Second),
		)
		if !distVideoStream.IsConstantFrameRate() {
			return fmt.Errorf("variable frame rate (VFR) content is not supported: distorted frames last from %s to %s while it declares a constant frame rate (%s fps)",
				distVideoStream.ShortestFrameDuration, distVideoStream.LongestFrameDuration, distVideoStream.RFrameRate)
		}

		// Validate frame counts match
		refFrames := videoStream.NbReadFrames
		distFrames := distVideoStream.NbReadFrames
		if refFrames != 0 && distFrames != 0 && refFrames != distFrames {
			return fmt.Errorf("frame count mismatch: reference has %d frames, distorted has %d frames", refFrames, distFrames)
		}

		// Validate frame rates match (the reference's -r is forced on both inputs). They are
		// compared up to Matroska's rounding: sptenc's output of a 59.94 fps source that is not
		// Matroska declares 19001/317 where the source declares 60000/1001, the same rate (see
		// core.SameFrameRate), and refusing it made its own output impossible to check.
		sameRate, err := core.SameFrameRate(videoStream.RFrameRate, distVideoStream.RFrameRate)
		if err != nil {
			return fmt.Errorf("failed to compare the frame rates: %w", err)
		}
		if !sameRate {
			return fmt.Errorf("frame rate mismatch: reference is %s fps, distorted is %s fps", videoStream.RFrameRate, distVideoStream.RFrameRate)
		}

		totalFrames := refFrames
		if totalFrames == 0 {
			totalFrames = distFrames
		}

		// Compute VMAF with progress
		fmt.Fprintln(bypass, "Computing VMAF...")
		start = time.Now()
		report, err := liveVMAF(ctx, ffmpeg.VMAFComputeConfig{
			ReferencePath:   referencePath,
			DistortedPath:   distortedPath,
			InputFrameRate:  videoStream.RFrameRate,
			ReportPath:      filepath.Join(workingDir, "vmaf.json"),
			Model:           model,
			Measures:        ffmpeg.VMAFMeasures{Original: true},
			HWDecoderConfig: decoderCfg,
		}, totalFrames, cmd.Bool(debugFlagName))
		if err != nil {
			return fmt.Errorf("failed to compute VMAF: %w", err)
		}

		stats, err := report.Stats(ffmpeg.VMAFScoreOriginal)
		if err != nil {
			return fmt.Errorf("failed to read the VMAF report: %w", err)
		}
		fmt.Fprintf(bypass, "\tVMAF computed in %s:\n\n%s\n",
			time.Since(start).Round(time.Second),
			stats,
		)
		fmt.Fprintf(bypass, "Complete VMAF computation took %s\n", time.Since(globalStart).Round(time.Second))
		return nil
	},
}
