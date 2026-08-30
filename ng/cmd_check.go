package main

import (
	"context"

	"github.com/urfave/cli/v3"
)

var checkCommand = &cli.Command{
	Name:        "check",
	Aliases:     []string{"c"},
	Usage:       "Verify third-party tools are present and usable",
	Description: "Check that required external tools are available and functional: ffmpeg (with libx265 and libvmaf), ffprobe, and mkvpropedit (MKVToolNix)",
	Action: func(ctx context.Context, cmd *cli.Command) (err error) {
		// TODO
		return
	},
}
