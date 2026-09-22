package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"al.essio.dev/pkg/shellescape"
	"github.com/hekmon/cunits/v3"
	"github.com/hekmon/liveprogress/v2"
	"github.com/hekmon/sptenc/core"
	"github.com/urfave/cli/v3"
)

var concatCommand = &cli.Command{
	Name:     "concat",
	Aliases:  []string{"c"},
	Category: "Tooling",
	Usage:    "Concatenate video files from a directory into a single file",
	Description: "Reads all video files (.mkv, .mp4) from the input directory, sorts them alphabetically, and\n" +
		"concatenates them into a single output file using ffmpeg's concat demuxer.\n\n" +
		"This is useful for merging segments produced by the split command without re-encoding.",
	Flags: []cli.Flag{
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
	},
	Arguments: []cli.Argument{
		&cli.StringArg{
			Name:      "inputdir",
			UsageText: "<input directory>",
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
		if cmd.Args().Len() != 2 {
			return ctx, errors.New("exactly two arguments are required: input directory and output file")
		}
		inputDir := cmd.Args().First()
		outputPath := cmd.Args().Get(1)
		fileInfos, err := os.Stat(inputDir)
		if err != nil {
			return ctx, fmt.Errorf("failed to access input path: %w", err)
		}
		if !fileInfos.IsDir() {
			return ctx, errors.New("input path must be a directory")
		}
		// Validate output path
		if err := validateOutputPath(outputPath); err != nil {
			return ctx, err
		}
		if inputDir == outputPath {
			return ctx, errors.New("input directory and output file must be different paths")
		}
		return ctx, nil
	},
	Action: func(ctx context.Context, cmd *cli.Command) (err error) {
		/*
			Prepare
		*/
		inputDir := cmd.StringArg("inputdir")
		outputPath := cmd.StringArg("output")
		// start live progress
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
					fmt.Fprintf(bypass, "Failed to delete temporary working directory %s: %s\n",
						shellescape.Quote(workingDir), removeErr,
					)
				}
			}
		}()
		if cmd.Bool(debugFlagName) {
			fmt.Fprintf(bypass, "DEBUG: Temporary directory created: %s\n", shellescape.Quote(workingDir))
		}

		/*
			Execute
		*/

		fmt.Fprintf(bypass, "Concatenating video files from %s\n",
			shellescape.Quote(inputDir),
		)

		// get segments
		segmentsPaths, err := getSegmentsFromDir(inputDir)
		if err != nil {
			return fmt.Errorf("failed to get segments from directory: %w", err)
		}
		if len(segmentsPaths) == 0 {
			return errors.New("no video files found in input directory")
		}

		// Calculate total duration and size
		// The concat demuxer places each file at the end of the previous one, and takes
		// that end from the duration the container declares, rounded to its time base: the
		// rounding adds up at every boundary (see ffmpeg.GenerateConcatList). Give it the
		// exact duration of every file instead: its number of video packets (one per frame)
		// at its frame rate. The whole file is read for that, no frame is decoded.
		var (
			fileInfo          os.FileInfo
			totalSize         int64
			totalDuration     time.Duration
			segmentsDurations = make([]time.Duration, len(segmentsPaths))
		)
		fmt.Fprintln(bypass, "Counting the frames of each file...")
		for i, path := range segmentsPaths {
			if fileInfo, err = os.Stat(path); err != nil {
				err = fmt.Errorf("failed to access %s: %w", shellescape.Quote(path), err)
				return
			}
			totalSize += fileInfo.Size()
			stats, probeErr := countStreamsPackets(ctx, path, cmd.Bool(debugFlagName))
			if probeErr != nil {
				return fmt.Errorf("failed to probe %s: %w", shellescape.Quote(filepath.Base(path)), probeErr)
			}
			videoStream := stats.VideoTrack()
			if videoStream == nil {
				return fmt.Errorf("no video stream found in %s", shellescape.Quote(filepath.Base(path)))
			}
			if segmentsDurations[i], err = core.FramesDuration(videoStream.NbReadPackets, videoStream.RFrameRate); err != nil {
				return fmt.Errorf("failed to compute the duration of %s: %w", shellescape.Quote(filepath.Base(path)), err)
			}
			totalDuration += segmentsDurations[i]
			if cmd.Bool(debugFlagName) {
				fmt.Fprintf(bypass, "DEBUG: %s %d frames at %s fps, %s (%s), container declares %s\n",
					shellescape.Quote(path), videoStream.NbReadPackets, videoStream.RFrameRate,
					segmentsDurations[i], cunits.ImportInBytes(float64(fileInfo.Size())),
					stats.Format.Duration,
				)
			}
		}
		fmt.Fprintf(bypass, "\t%d video files with a total duration of %s and a total size of %s\n",
			len(segmentsPaths), totalDuration.Round(time.Millisecond), cunits.ImportInBytes(float64(totalSize)),
		)

		// concat
		start := time.Now()
		if err = liveConcatDuration(ctx, workingDir, outputPath, segmentsPaths, segmentsDurations, totalDuration, cmd.Bool(debugFlagName)); err != nil {
			return fmt.Errorf("failed to concatenate segments: %w", err)
		}
		duration := time.Since(start)

		// report
		var outputSizeStr string
		outputSize, sizeErr := getFileSize(outputPath)
		if sizeErr != nil {
			fmt.Fprintf(bypass, "WARNING: failed to get output file size: %s\n", sizeErr)
		} else {
			outputSizeStr = fmt.Sprintf(" (%s)", cunits.ImportInBytes(float64(outputSize)))
		}
		fmt.Fprintf(bypass, "\tConcatenated %d files into %s%s in %s\n",
			len(segmentsPaths), shellescape.Quote(outputPath), outputSizeStr, duration.Round(time.Second),
		)

		return
	},
}
