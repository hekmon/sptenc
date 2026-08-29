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

	"al.essio.dev/pkg/shellescape"
	"github.com/urfave/cli/v3"
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
		Name:    "sptenc",
		Usage:   "Split Encoder: a percieved quality (VMAF) driven encoder",
		Version: version(),
		Flags: []cli.Flag{
			&cli.BoolFlag{
				Name:     "debug",
				Aliases:  []string{"d"},
				Usage:    "print debug logs",
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
		},
		Before: func(ctx context.Context, cmd *cli.Command) (context.Context, error) {
			if cmd.String("ffmpegpath") != ffmpeg.FFMPEGBinary {
				ffmpeg.FFMPEGBinary = cmd.String("ffmpegpath")
				if cmd.Bool("debug") {
					fmt.Printf("DEBUG: using %s as custom ffmpeg path\n", shellescape.Quote(ffmpeg.FFMPEGBinary))
				}
			}
			if cmd.String("ffprobepath") != ffmpeg.FFProbeBinary {
				ffmpeg.FFProbeBinary = cmd.String("ffprobepath")
				if cmd.Bool("debug") {
					fmt.Printf("DEBUG: using %s as custom ffprobe path\n", shellescape.Quote(ffmpeg.FFProbeBinary))
				}
			}
			return ctx, nil
		},
		Commands: []*cli.Command{
			masterCommand,
		},
	}
	if err := cmd.Run(ctx, os.Args); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func version() string {
	infos, ok := debug.ReadBuildInfo()
	if !ok {
		return fmt.Sprintf("unknown (%s/%s)", runtime.GOOS, runtime.GOARCH)
	}
	return fmt.Sprintf("%s (%s, %s/%s)", infos.Main.Version, infos.GoVersion, runtime.GOOS, runtime.GOARCH)
}
