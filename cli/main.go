package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"runtime/debug"
	"syscall"
	"time"

	"github.com/hekmon/sptenc/ffmpeg"
	"github.com/hekmon/sptenc/mkvtoolnix"

	"al.essio.dev/pkg/shellescape"
	"github.com/hekmon/liveterm/v2"
	"github.com/urfave/cli/v3"
)

// Global flag names
const (
	debugFlagName           = "debug"
	ffmpegPathFlagName      = "ffmpegpath"
	ffprobePathFlagName     = "ffprobepath"
	mkvpropeditPathFlagName = "mkvpropeditpath"
)

const (
	// terminal update freq
	updateUIFreq = 100 * time.Millisecond
)

func init() {
	// Align libs on update freq
	liveterm.RefreshInterval = updateUIFreq
	ffmpeg.StatsPeriod = updateUIFreq
}

func main() {
	// Application-wide signal handling
	ctx, stop := signal.NotifyContext(context.Background(),
		os.Interrupt,    // Ctrl+C (all platforms)
		syscall.SIGTERM, // systemd stop (Unix)
		syscall.SIGQUIT, // debug dump (Unix)
	)
	defer stop()

	// Start application
	cmd := &cli.Command{
		Name:  linkedName(),
		Usage: "Split Encoder: a perceived-quality, VMAF-driven encoder",
		Description: "Split Encoder is a tool that performs scene-aware video encoding.\n\n" +
			"HOW IT WORKS\n" +
			"  1. Splits the input video into scene-aligned segments.\n" +
			"  2. Encodes each segment independently, searching for the highest QP\n" +
			"     (smallest file size) that still passes all enabled VMAF quality thresholds.\n" +
			"  3. Re-encodes at a lower QP if any threshold is not met.\n" +
			"  4. Merges all validated segments into the final output file.\n" +
			"  5. Embeds the final VMAF results as metadata tags.\n\n" +
			"TRADE-OFF\n" +
			"This guarantees the target perceptual quality at the smallest possible file size,\n" +
			"but encoding takes significantly longer than a standard single-pass encode\n" +
			"because multiple QP candidates are tested per segment.\n\n" +
			"PIPELINE\n" +
			"The encode command can handle the entire pipeline in one step, or you can use\n" +
			"the master and split commands to produce intermediate artifacts for finer control.",
		Version: version(),
		Flags: []cli.Flag{
			&cli.BoolFlag{
				Name:     debugFlagName,
				Aliases:  []string{"d"},
				Usage:    "print debug logs and keep temporary directory if any",
				Value:    false,
				OnlyOnce: true,
			},
			&cli.StringFlag{
				Name:     ffmpegPathFlagName,
				Usage:    "ffmpeg binary path",
				Value:    ffmpeg.FFMPEGBinary,
				OnlyOnce: true,
			},
			&cli.StringFlag{
				Name:     ffprobePathFlagName,
				Usage:    "ffprobe binary path",
				Value:    ffmpeg.FFProbeBinary,
				OnlyOnce: true,
			},
			&cli.StringFlag{
				Name:     mkvpropeditPathFlagName,
				Usage:    "mkvpropedit binary path",
				Value:    mkvtoolnix.MKVPropEdit,
				OnlyOnce: true,
			},
		},
		Before: func(ctx context.Context, cmd *cli.Command) (context.Context, error) {
			if cmd.String(ffmpegPathFlagName) != ffmpeg.FFMPEGBinary {
				ffmpeg.FFMPEGBinary = cmd.String(ffmpegPathFlagName)
				if cmd.Bool(debugFlagName) {
					fmt.Printf("DEBUG: using %s as custom ffmpeg path\n", shellescape.Quote(ffmpeg.FFMPEGBinary))
				}
			}
			if cmd.String(ffprobePathFlagName) != ffmpeg.FFProbeBinary {
				ffmpeg.FFProbeBinary = cmd.String(ffprobePathFlagName)
				if cmd.Bool(debugFlagName) {
					fmt.Printf("DEBUG: using %s as custom ffprobe path\n", shellescape.Quote(ffmpeg.FFProbeBinary))
				}
			}
			if cmd.String(mkvpropeditPathFlagName) != mkvtoolnix.MKVPropEdit {
				mkvtoolnix.MKVPropEdit = cmd.String(mkvpropeditPathFlagName)
				if cmd.Bool(debugFlagName) {
					fmt.Printf("DEBUG: using %s as custom mkvpropedit path\n", shellescape.Quote(mkvtoolnix.MKVPropEdit))
				}
			}
			return ctx, nil
		},
		Commands: []*cli.Command{
			// main
			encodeCommand,
			verifyCommand,
			// tooling
			masterCommand,
			thresholdsCommand,
			splitCommand,
			concatCommand,
			vmafCommand,
			cacheCommand,
			// advanced
			batchsearchCommand,
		},
	}
	if err := cmd.Run(ctx, os.Args); err != nil {
		fmt.Fprintf(os.Stderr, "FATAL ERROR: %v\n", err)
		os.Exit(1)
	}
}

func linkedName() string {
	infos, ok := debug.ReadBuildInfo()
	if !ok {
		return "sptenc"
	}
	return liveterm.Hyperlink("https://"+infos.Main.Path, "sptenc")
}

func version() string {
	infos, ok := debug.ReadBuildInfo()
	if !ok {
		return fmt.Sprintf("unknown (%s/%s)", runtime.GOOS, runtime.GOARCH)
	}
	return fmt.Sprintf("%s (%s, %s/%s)",
		infos.Main.Version, infos.GoVersion, runtime.GOOS, runtime.GOARCH,
	)
}
