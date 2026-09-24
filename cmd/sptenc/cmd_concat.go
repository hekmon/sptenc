package main

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"

	"al.essio.dev/pkg/shellescape"
	"github.com/hekmon/cunits/v3"
	"github.com/hekmon/liveprogress/v2"
	"github.com/hekmon/sptenc/core"
	"github.com/hekmon/sptenc/ffmpeg"
	"github.com/urfave/cli/v3"
)

var concatCommand = &cli.Command{
	Name:     "concat",
	Aliases:  []string{"c"},
	Category: "Tooling",
	Usage:    "Concatenate video files from a directory into a single file",
	Description: "Reads all video files (.mkv, .mp4) from the input directory, sorts them alphabetically, and\n" +
		"concatenates them into a single output file using ffmpeg's concat demuxer.\n\n" +
		"This is useful for merging segments produced by the split command without re-encoding.\n\n" +
		"ORIGINAL FILE\n" +
		"--" + originalFileFlagName + " is optional, and without it the difference is minimal: give it the file the\n" +
		"segments were cut from if you want their timestamps exactly on its frames. Matroska stores the\n" +
		"frame duration in whole nanoseconds, and some frame rates come back from it approximated:\n" +
		"59.94 fps reads as 19001/317 instead of 60000/1001. Merged at that rate, the segments of a source\n" +
		"that was not a Matroska file have some frames about a millisecond off (one in 60 at 59.94 fps,\n" +
		"more over hours), far below anything anyone can see or hear. With the original file its exact\n" +
		"rate is used instead: only its frame rate is read, and it must be the segments' one up to that\n" +
		"rounding.",
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:     originalFileFlagName,
			Aliases:  []string{"f"},
			Usage:    "Original media file the segments were cut from, for their exact frame rate (optional, see ORIGINAL FILE)",
			OnlyOnce: true,
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
		// Validate the original file, when given
		if originalPath := cmd.String(originalFileFlagName); originalPath != "" {
			originalInfos, err := os.Stat(originalPath)
			if err != nil {
				return ctx, fmt.Errorf("failed to access the original file: %w", err)
			}
			if !originalInfos.Mode().IsRegular() {
				return ctx, errors.New("the original file must be a regular file")
			}
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

		// With the original file, its frame rate: exact where the files may declare Matroska's
		// approximation of it (see ORIGINAL FILE in the description)
		var originalFrameRate string
		if originalPath := cmd.String(originalFileFlagName); originalPath != "" {
			originalStats, probeErr := getStreamsInfos(ctx, originalPath, cmd.Bool(debugFlagName))
			if probeErr != nil {
				return fmt.Errorf("failed to probe the original file: %w", probeErr)
			}
			originalVideoStream := originalStats.VideoTrack()
			if originalVideoStream == nil {
				return errors.New("no video stream found in the original file")
			}
			originalFrameRate = originalVideoStream.RFrameRate
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
			segmentsStats     = make([]ffmpeg.FFProbeStats, len(segmentsPaths))
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
			segmentsStats[i] = stats
			videoStream := stats.VideoTrack()
			if videoStream == nil {
				return fmt.Errorf("no video stream found in %s", shellescape.Quote(filepath.Base(path)))
			}
			fileFrameRate := videoStream.RFrameRate
			if originalFrameRate != "" {
				same, sameErr := core.SameFrameRate(originalFrameRate, fileFrameRate)
				if sameErr != nil {
					return fmt.Errorf("failed to compare the frame rate of %s with the original file's: %w",
						shellescape.Quote(filepath.Base(path)), sameErr)
				}
				if !same {
					return fmt.Errorf("%s declares a frame rate of %s, not the original file's %s",
						shellescape.Quote(filepath.Base(path)), fileFrameRate, originalFrameRate)
				}
				fileFrameRate = originalFrameRate
			}
			if segmentsDurations[i], err = core.FramesDuration(videoStream.NbReadPackets, fileFrameRate); err != nil {
				return fmt.Errorf("failed to compute the duration of %s: %w", shellescape.Quote(filepath.Base(path)), err)
			}
			totalDuration += segmentsDurations[i]
			if cmd.Bool(debugFlagName) {
				fmt.Fprintf(bypass, "DEBUG: %s %d frames at %s fps, %s (%s), container declares %s\n",
					shellescape.Quote(path), videoStream.NbReadPackets, fileFrameRate,
					segmentsDurations[i], cunits.ImportInBytes(float64(fileInfo.Size())),
					stats.Format.Duration,
				)
			}
		}
		fmt.Fprintf(bypass, "\t%d video files with a total duration of %s and a total size of %s\n",
			len(segmentsPaths), totalDuration.Round(time.Millisecond), cunits.ImportInBytes(float64(totalSize)),
		)
		// The video timestamps are snapped to the frame grid when the files allow it
		frameRate, whyNotSnapped := concatSnapFrameRate(segmentsPaths, segmentsStats, originalFrameRate)
		if whyNotSnapped != "" {
			fmt.Fprintf(bypass, "\tVideo timestamps not snapped to a frame grid: %s\n", whyNotSnapped)
		} else if originalFrameRate == "" {
			if usualRate, found := matroskaApproximatedRate(frameRate); found {
				fmt.Fprintf(bypass, "\tThe files declare %s fps, which is %s fps up to Matroska's rounding: if they come from a %s fps file that is not Matroska, --%s puts their timestamps exactly on its frames (without it, some frames are about a millisecond off)\n",
					frameRate, usualRate, usualRate, originalFileFlagName)
			}
		}

		// concat
		start := time.Now()
		if err = liveConcatDuration(ctx, workingDir, outputPath, segmentsPaths, segmentsDurations, frameRate, totalDuration, cmd.Bool(debugFlagName)); err != nil {
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

// concatSnapFrameRate returns the frame rate the video timestamps of files concatenated in
// that order can be snapped to (see ffmpeg.Concat), or why they can not be. With the frame
// rate of the original file (empty without it), every file has been checked to be at it up to
// Matroska's rounding (core.SameFrameRate): the snap uses it, whatever approximation of it the
// files declare.
//
// # WHY THE CONCAT COMMAND DOES NOT ALWAYS SNAP
//
// The snap moves every video frame onto one frame grid starting at 0. The encode pipeline
// knows its segments are on one: cut from the same master, at the same frame rate, video only.
// The concat command is given any files, and for two kinds of them the snap corrupts the
// output instead of fixing it (both measured):
//   - Files at different frame rates have no grid in common: a 23.976 fps file followed by a
//     25 fps one got frames sharing the same timestamp.
//   - A file whose video starts after its other streams is placed by its first stream: its
//     frames are off the grid by the gap, and the snap moved them by up to half a frame
//     against the audio (20 ms for a video starting 22 ms after its audio).
//
// Such files are merged as the demuxer places them, as before the snap existed: frames up to
// 1 ms off.
//
// # EDGE CASES
//
//   - A start time ffprobe can not give (N/A) can not be compared: no snap.
//   - A file whose frames do not follow its declared frame rate is not detected, no frame is
//     decoded here. Its duration is already wrong in that case: it is computed from its
//     number of frames and its declared frame rate.
func concatSnapFrameRate(paths []string, stats []ffmpeg.FFProbeStats, originalFrameRate string) (frameRate, whyNot string) {
	frameRate = originalFrameRate
	for i := range stats {
		name := shellescape.Quote(filepath.Base(paths[i]))
		videoStream := stats[i].VideoTrack()
		if videoStream == nil {
			return "", fmt.Sprintf("%s has no video stream", name)
		}
		if originalFrameRate == "" { // otherwise checked against the original's while counting the frames
			if i == 0 {
				frameRate = videoStream.RFrameRate
			} else if videoStream.RFrameRate != frameRate {
				return "", fmt.Sprintf("%s is at %s fps, %s at %s fps",
					shellescape.Quote(filepath.Base(paths[0])), frameRate, name, videoStream.RFrameRate)
			}
		}
		var fileStart string
		if stats[i].Format != nil {
			fileStart = stats[i].Format.StartTime
		}
		if videoStream.StartTime == "" || videoStream.StartTime == "N/A" || videoStream.StartTime != fileStart {
			return "", fmt.Sprintf("the video of %s starts at %s s, the file at %s s", name, videoStream.StartTime, fileStart)
		}
	}
	return
}

// matroskaApproximatedRate returns the usual frame rate that frameRate is up to Matroska's
// rounding without being it, if any: 60000/1001 for 19001/317. The usual rates are the whole
// ones and the multiples of 1000/1001, up to 1000 fps: they are at least 1000/1001 apart, so
// at most one of them is within the rounding of any rate (see core.SameFrameRate). Nothing is
// decided on it: it only tells the user that --original-file can make the timestamps exact.
func matroskaApproximatedRate(frameRate string) (usualRate string, found bool) {
	declared, ok := new(big.Rat).SetString(frameRate)
	if !ok || declared.Sign() <= 0 {
		return "", false
	}
	for n := int64(1); n <= 1000; n++ {
		for _, usual := range []*big.Rat{big.NewRat(n, 1), big.NewRat(n*1000, 1001)} {
			if same, _ := core.SameFrameRate(usual.RatString(), frameRate); same {
				return usual.RatString(), usual.Cmp(declared) != 0
			}
		}
	}
	return "", false
}
