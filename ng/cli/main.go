package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"runtime/debug"
	"syscall"

	"github.com/hekmon/sptenc/ng/ffmpeg"
	"github.com/hekmon/sptenc/ng/mkvtoolnix"

	"al.essio.dev/pkg/shellescape"
	"github.com/hekmon/liveterm/v2"
	"github.com/urfave/cli/v3"
)

const (
	// set SoT for flag accessed across several cmds
	debugFlagName = "debug"
)

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
		Name:        "sptenc",
		Usage:       "Split Encoder: a perceived-quality, VMAF-driven encoder",
		Description: "Split Encoder is a tool that performs scene-aware video encoding where each segment is independently encoded and validated against configurable VMAF thresholds. For each segment, it searches for the highest QP (smallest file size) that still passes all enabled VMAF metrics, re-encoding at a lower QP if any threshold is not met. Once all segments pass validation, they are merged into the final output file with VMAF results embedded as metadata tags. This guarantees the target perceptual quality at the smallest possible file size for the given encoder, but encoding takes significantly longer than a standard single-pass encode because multiple QP candidates are tested per segment.\n\nThe encode command can be used on its own to handle the entire pipeline, or with intermediate artifacts produced by the master and split commands for finer control.",
		Version:     version(),
		Flags: []cli.Flag{
			&cli.BoolFlag{
				Name:     debugFlagName,
				Aliases:  []string{"d"},
				Usage:    "print debug logs and keep temporary directory if any",
				Value:    false,
				OnlyOnce: true,
			},
			&cli.StringFlag{
				Name:     "ffmpegpath",
				Usage:    "ffmpeg binary path",
				Value:    ffmpeg.FFMPEGBinary,
				OnlyOnce: true,
			},
			&cli.StringFlag{
				Name:     "ffprobepath",
				Usage:    "ffprobe binary path",
				Value:    ffmpeg.FFProbeBinary,
				OnlyOnce: true,
			},
			&cli.StringFlag{
				Name:     "mkvpropeditpath",
				Usage:    "mkvpropedit binary path",
				Value:    mkvtoolnix.MKVPropEdit,
				OnlyOnce: true,
			},
		},
		Before: func(ctx context.Context, cmd *cli.Command) (context.Context, error) {
			if cmd.String("ffmpegpath") != ffmpeg.FFMPEGBinary {
				ffmpeg.FFMPEGBinary = cmd.String("ffmpegpath")
				if cmd.Bool(debugFlagName) {
					fmt.Printf("DEBUG: using %s as custom ffmpeg path\n", shellescape.Quote(ffmpeg.FFMPEGBinary))
				}
			}
			if cmd.String("ffprobepath") != ffmpeg.FFProbeBinary {
				ffmpeg.FFProbeBinary = cmd.String("ffprobepath")
				if cmd.Bool(debugFlagName) {
					fmt.Printf("DEBUG: using %s as custom ffprobe path\n", shellescape.Quote(ffmpeg.FFProbeBinary))
				}
			}
			if cmd.String("mkvpropeditpath") != mkvtoolnix.MKVPropEdit {
				mkvtoolnix.MKVPropEdit = cmd.String("mkvpropeditpath")
				if cmd.Bool(debugFlagName) {
					fmt.Printf("DEBUG: using %s as custom mkvpropedit path\n", shellescape.Quote(mkvtoolnix.MKVPropEdit))
				}
			}
			return ctx, nil
		},
		Commands: []*cli.Command{
			checkCommand,
			masterCommand,
			splitCommand,
			encodeCommand,
		},
	}
	if err := cmd.Run(ctx, os.Args); err != nil {
		fmt.Fprintf(os.Stderr, "FATAL ERROR: %v\n", err)
		os.Exit(1)
	}
}

const sptencURL = "https://github.com/hekmon/sptenc"

func version() string {
	infos, ok := debug.ReadBuildInfo()
	if !ok {
		return fmt.Sprintf("%s unknown (%s/%s)", liveterm.Hyperlink(sptencURL, "sptenc"), runtime.GOOS, runtime.GOARCH)
	}
	return fmt.Sprintf("%s %s (%s, %s/%s)",
		liveterm.Hyperlink(sptencURL, "sptenc"), infos.Main.Version, infos.GoVersion, runtime.GOOS, runtime.GOARCH)
}
