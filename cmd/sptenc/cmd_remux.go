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
	"github.com/hekmon/cunits/v3"
	"github.com/hekmon/liveprogress/v2"
	"github.com/urfave/cli/v3"
)

const encodeToFLACFlagName = "encode-to-flac"

var remuxCommand = &cli.Command{
	Name:     "remux",
	Aliases:  []string{"r"},
	Category: "Tooling",
	Usage:    "Replace the video track of a file with a new one without re-encoding",
	Description: "Takes the video stream from the new video file and copies all other streams\n" +
		"(audio, subtitles, data, attachments) from the original file into a new .mkv output.\n\n" +
		"This is the post-processing step used by the encode pipeline to graft an encoded video\n" +
		"back onto its source container.",
	Flags: []cli.Flag{
		&cli.BoolFlag{
			Name:     encodeToFLACFlagName,
			Aliases:  []string{"f"},
			Usage:    "Re-encode audio to FLAC (only if all tracks are PCM; otherwise copied)",
			Value:    false,
			OnlyOnce: true,
		},
	},
	Arguments: []cli.Argument{
		&cli.StringArg{
			Name:      "original",
			UsageText: "<original file>",
		},
		&cli.StringArg{
			Name:      "newvideo",
			UsageText: "<new video file>",
		},
		&cli.StringArg{
			Name:      "output",
			UsageText: "<output file>",
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
		if cmd.Args().Len() != 3 {
			return ctx, errors.New("exactly three arguments are required: original file, new video file, and output file")
		}
		originalPath := cmd.Args().First()
		newVideoPath := cmd.Args().Get(1)
		outputPath := cmd.Args().Get(2)
		// Validate original file
		origInfo, err := os.Stat(originalPath)
		if err != nil {
			return ctx, fmt.Errorf("failed to access original file: %w", err)
		}
		if !origInfo.Mode().IsRegular() {
			return ctx, errors.New("original file must be a regular file")
		}
		// Validate new video file
		newInfo, err := os.Stat(newVideoPath)
		if err != nil {
			return ctx, fmt.Errorf("failed to access new video file: %w", err)
		}
		if !newInfo.Mode().IsRegular() {
			return ctx, errors.New("new video file must be a regular file")
		}
		// Prevent same-file inputs/outputs
		if originalPath == newVideoPath {
			return ctx, errors.New("original file and new video file must be different paths")
		}
		if originalPath == outputPath {
			return ctx, errors.New("original file and output file must be different paths")
		}
		if newVideoPath == outputPath {
			return ctx, errors.New("new video file and output file must be different paths")
		}
		// Validate output path
		if err := validateOutputPath(outputPath); err != nil {
			return ctx, err
		}
		// Validate new video file has a video stream
		newStats, err := ffmpeg.GetStreamsInfos(ctx, ffmpeg.GetStreamsInfosConfig{
			Path: newVideoPath,
		})
		if err != nil {
			return ctx, fmt.Errorf("failed to probe new video file: %w", err)
		}
		if newStats.VideoTrack() == nil {
			return ctx, errors.New("new video file has no video stream")
		}
		return ctx, nil
	},
	Action: func(ctx context.Context, cmd *cli.Command) (err error) {
		originalPath := cmd.StringArg("original")
		newVideoPath := cmd.StringArg("newvideo")
		outputPath := cmd.StringArg("output")

		// Start live progress
		if err = liveprogress.Start(); err != nil {
			return fmt.Errorf("failed to start live progress: %w", err)
		}
		defer liveprogress.Stop(false)
		bypass := liveprogress.Bypass()

		// Probe original file for duration
		origStats, err := getStreamsInfos(ctx, originalPath, cmd.Bool(debugFlagName))
		if err != nil {
			return fmt.Errorf("failed to probe original file: %w", err)
		}

		// Determine FLAC encoding (only for PCM audio, like the encode pipeline)
		encodeToFlac := false
		if cmd.Bool(encodeToFLACFlagName) {
			encodeToFlac = AllAudioTracksPCM(origStats)
			if !encodeToFlac {
				// AllAudioTracksPCM is also false without any audio track: do not blame non PCM tracks then
				if hasAudioTracks(origStats) {
					fmt.Fprintf(bypass, "WARNING: --%s is set but not all audio tracks are PCM; copying audio instead\n", encodeToFLACFlagName)
				} else {
					fmt.Fprintf(bypass, "WARNING: --%s is set but the original file has no audio track; nothing to encode\n", encodeToFLACFlagName)
				}
			}
		}

		fmt.Fprintf(bypass, "Remuxing %s with video from %s into %s\n",
			shellescape.Quote(filepath.Base(originalPath)),
			shellescape.Quote(filepath.Base(newVideoPath)),
			shellescape.Quote(outputPath),
		)

		// Remux
		start := time.Now()
		if err = liveRemuxSwapVideo(ctx, originalPath, newVideoPath, outputPath,
			encodeToFlac, nil, origStats.Format.Duration, cmd.Bool(debugFlagName)); err != nil {
			return fmt.Errorf("failed to remux: %w", err)
		}
		duration := time.Since(start)

		// Report
		outputSize, sizeErr := getFileSize(outputPath)
		if sizeErr != nil {
			fmt.Fprintf(bypass, "WARNING: failed to get output file size: %s\n", sizeErr)
		}
		fmt.Fprintf(bypass, "\tRemuxed into %s (%s) in %s\n",
			shellescape.Quote(outputPath),
			cunits.ImportInBytes(float64(outputSize)),
			duration.Round(time.Second),
		)

		return
	},
}
