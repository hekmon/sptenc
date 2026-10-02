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

var masterCommand = &cli.Command{
	Name:     "master",
	Aliases:  []string{"m"},
	Category: "Tooling",
	Usage:    "Create an intermediate lossless video master that can be cut at any frame",
	Description: "Most video files use Group of Pictures (GoP) encoding: only the keyframes (I-frames) are\n" +
		"self-contained, P and B frames are stored as differences from the frames they reference.\n\n" +
		"THE PROBLEM\n" +
		"  * Without re-encoding, a video can only be cut on a keyframe: a scene boundary falling between\n" +
		"    two keyframes can not be cut where it is.\n" +
		"  * Open GoPs make things worse: some frames reference frames of another group, which a cut, even\n" +
		"    on a keyframe, can leave in a different segment. These frames can not be decoded anymore and\n" +
		"    are dropped at decoding: every cut shortens the video a little more, a drift that accumulates\n" +
		"    (against the audio, for instance).\n\n" +
		"THE SOLUTION\n" +
		"This command re-encodes the source into a lossless all-intra master using the FFV1 codec.\n" +
		"Every frame becomes self-contained, so you can cut precisely at any frame with no quality loss\n" +
		"and no dropped frame.\n\n" +
		"PIXEL FORMAT\n" +
		"FFV1 is mathematically lossless, but the master is stored as 10-bit 4:2:0, the pixel format of\n" +
		"every sptenc output. From limited range 8-bit and 10-bit 4:2:0 sources, by far the most common\n" +
		"ones, it holds every sample of the original without loss (8-bit values are shifted to 10 bits).\n" +
		"Other sources are converted at this step: 4:2:2 and 4:4:4 ones get their chroma subsampled (luma\n" +
		"stays exact, the chroma of a 4:4:4 one sited on the left, where decoders expect it), sources\n" +
		"deeper than 10 bits are reduced to 10 bits, and full range sources are converted to limited\n" +
		"range (without loss from 8 bits, with a slight loss from 10 bits).\n" +
		"RGB sources are converted to YUV with the matrix of their primaries (bt709 for BT.709 ones,\n" +
		"bt2020nc for BT.2020 ones) or the one --" + rgbMatrixFlagName + " sets: a source declaring other primaries,\n" +
		"or none, is refused without it (see MANUAL.md, RGB sources).",
	Flags: func() []cli.Flag {
		flags := []cli.Flag{rgbMatrixFlag("")}
		flags = append(flags, hardwareAccelFlags(hwAccelScopeDecode)...)
		return flags
	}(),
	Arguments: []cli.Argument{
		&cli.StringArg{
			Name:      "inputfile",
			UsageText: "<input file>",
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
			return ctx, errors.New("exactly two arguments are required: input file and output file")
		}
		inputFilePath := cmd.Args().First()
		outputFilePath := cmd.Args().Get(1)
		fileInfos, err := os.Stat(inputFilePath)
		if err != nil {
			return ctx, fmt.Errorf("failed to access input file: %w", err)
		}
		if !fileInfos.Mode().IsRegular() {
			return ctx, errors.New("input file must be a regular file")
		}
		// Validate output path
		if err := validateOutputPath(outputFilePath); err != nil {
			return ctx, err
		}
		if inputFilePath == outputFilePath {
			return ctx, errors.New("input file and output file must be different paths")
		}
		if _, err := hwDecodeFlags(cmd); err != nil {
			return ctx, err
		}
		return ctx, nil
	},
	Action: func(ctx context.Context, cmd *cli.Command) (err error) {
		// handle input file
		inputFilePath := cmd.StringArg("inputfile")
		outputFilePath := cmd.StringArg("output")
		fileInfos, err := os.Stat(inputFilePath)
		if err != nil {
			return fmt.Errorf("failed to access input file: %w", err)
		}
		// reject what encode would reject later, before spending the time and the disk space
		sourceStats, err := getStreamsInfos(ctx, inputFilePath, cmd.Bool(debugFlagName))
		if err != nil {
			return fmt.Errorf("failed to probe input file: %w", err)
		}
		videoStream, err := checkSourceVideo(sourceStats)
		if err != nil {
			return
		}
		rgbToYUV, err := sourceYUVMatrix(cmd, os.Stdout, videoStream)
		if err != nil {
			return
		}
		fmt.Printf("Creating a master of %s (%s)\n",
			shellescape.Quote(filepath.Base(inputFilePath)),
			cunits.ImportInBytes(float64(fileInfos.Size())),
		)
		// start live progress
		if err = liveprogress.Start(); err != nil {
			return fmt.Errorf("failed to start live progress: %w", err)
		}
		defer liveprogress.Stop(false)
		liveprogress.AddCustomLine(func() string { return "" }) // separate logs from live status updates
		// build optional hw decode config
		requestedDecoder, _ := hwDecodeFlags(cmd) // validated in Before
		decoderCfg := requestedDecoder.CompatibleWith(ctx, inputFilePath)
		if requestedDecoder.Enabled() && !decoderCfg.Enabled() {
			fmt.Fprintf(liveprogress.Bypass(), "WARNING: input codec is not decoded with %s (not supported, or not decoded exactly: see MANUAL.md, Hardware decoding), falling back to software decode\n", requestedDecoder.Name())
		}
		// create master
		var outputFile string
		outputFile, _, _, err = createMaster(ctx, inputFilePath, outputFilePath, cmd.Bool(debugFlagName), decoderCfg, rgbToYUV)
		if err == nil {
			fmt.Fprintf(liveprogress.Bypass(), "Master saved to: %s\n", shellescape.Quote(outputFile))
		}
		return
	},
}

// createMaster counts the frames of the source then encodes its video stream to a lossless
// FFV1 master, both with the hardware decoder of decoderCfg (software decode when none). An RGB
// source is converted to YUV with the matrix rgbToYUV (see sourceYUVMatrix).
func createMaster(ctx context.Context, inputFilePath, outputFile string, debug bool, decoderCfg ffmpeg.HWDecoderConfig,
	rgbToYUV ffmpeg.YUVMatrix) (outputFileResult string, totalFrames int, duration time.Duration, err error) {
	if totalFrames, duration, err = countMasterFrames(ctx, inputFilePath, debug, decoderCfg); err != nil {
		return
	}
	// ffv1 encode
	fmt.Fprintln(liveprogress.Bypass(), "Creating a ffv1 lossless intra frames master...")
	start := time.Now()
	masterConfig := decoderCfg.ToFFV1MasterConfig()
	masterConfig.RGBToYUV = rgbToYUV
	if err = liveFFV1Master(ctx, inputFilePath, outputFile, totalFrames, debug, masterConfig); err != nil {
		err = fmt.Errorf("failed to encode the ffv1 master: %w", err)
		return
	}
	masterDuration := time.Since(start)
	fileInfos, err := os.Stat(outputFile)
	if err != nil {
		err = fmt.Errorf("failed to stat master file: %w", err)
		return
	}
	if debug {
		fmt.Fprintf(liveprogress.Bypass(), "\tMaster created in %s: %s (%s)\n",
			masterDuration.Round(time.Second),
			shellescape.Quote(outputFile),
			cunits.ImportInBytes(float64(fileInfos.Size())),
		)
	} else {
		fmt.Fprintf(liveprogress.Bypass(), "\tMaster created in %s (%s)\n",
			masterDuration.Round(time.Second),
			cunits.ImportInBytes(float64(fileInfos.Size())),
		)
	}
	outputFileResult = outputFile
	return
}

// createMasterSegments counts the frames of the source then encodes its video stream to the
// FFV1 master already cut at the scenes, into outputDir: the very segments createMaster then
// Segment give, same packets and same timestamps (see ffmpeg.TestFFV1VideoMasterSegments),
// without the master ever being on disk whole. Both with the hardware decoder of decoderCfg, and
// an RGB source converted to YUV with the matrix rgbToYUV (see sourceYUVMatrix).
//
// # WHY THIS EXISTS
//
// The master and its segments are the same frames. Written then cut, both are on disk whole at
// the end of the split: a run needs room for twice the master. Written cut, for the master once.
//
// # WHY NOT DELETE THE MASTER ONCE CUT
//
// The split is a single ffmpeg pass: when it ends, the master and all its segments are on disk,
// the peak is reached already. Deleting the master then only shortens the time spent there.
//
// # WHY NOT A PIPE FROM THE MASTER ENCODE TO SEGMENT
//
// It would reproduce the split exactly, but the standard output of both processes carries their
// progress, and Go can not hand a child process another descriptor on Windows (exec.Cmd
// ExtraFiles). It is not needed either: the segment muxer cuts the packets of an encoder as it
// cuts those of a stream copy.
//
// # WHO WRITES THE MASTER CUT
//
// encode and split given a source: both detect the scenes on the source first. batchsearch cuts
// its master once per candidate, and the master command writes it for the user to act on.
//
// # EDGE CASES
//
//   - A single scene: the master is written as the only segment, as Segment would copy it.
//   - The duration a segment's container declares can end 1 ms later than the one Segment
//     writes (seen at 24000/1001 and 60000/1001 fps): the frames and their timestamps are the
//     same, and nothing but the progress bars reads that duration.
func createMasterSegments(ctx context.Context, inputFilePath, outputDir string, scenes []ffmpeg.Scene, debug bool,
	decoderCfg ffmpeg.HWDecoderConfig, rgbToYUV ffmpeg.YUVMatrix) (totalFrames int, err error) {
	if totalFrames, _, err = countMasterFrames(ctx, inputFilePath, debug, decoderCfg); err != nil {
		return
	}
	fmt.Fprintln(liveprogress.Bypass(), "Creating the ffv1 lossless intra frames master, cut into its segments...")
	start := time.Now()
	masterConfig := decoderCfg.ToFFV1MasterConfig()
	masterConfig.RGBToYUV = rgbToYUV
	masterConfig.SegmentsDir = outputDir
	masterConfig.ScenesFrames = scenesFrames(scenes)
	if err = liveFFV1Master(ctx, inputFilePath, "", totalFrames, debug, masterConfig); err != nil {
		err = fmt.Errorf("failed to encode the ffv1 master segments: %w", err)
		return
	}
	var size int64
	for i := range len(scenes) + 1 {
		var fileInfos os.FileInfo
		if fileInfos, err = os.Stat(filepath.Join(outputDir, fmt.Sprintf(ffmpeg.SegmentOutputFormat, i))); err != nil {
			err = fmt.Errorf("failed to stat master segment: %w", err)
			return
		}
		size += fileInfos.Size()
	}
	segments := "segments"
	if len(scenes) == 0 {
		segments = "segment"
	}
	fmt.Fprintf(liveprogress.Bypass(), "\tMaster created in %s, as %d %s (%s)\n",
		time.Since(start).Round(time.Second), len(scenes)+1, segments, cunits.ImportInBytes(float64(size)))
	return
}

// writeSegmentsMasters writes the master of every segment of a pre-split directory into outputDir,
// for the segments sptenc must convert itself: RGB ones, converted to YUV with the matrix rgbToYUV
// (see sourceYUVMatrix), and full chroma YUV ones, subsampled to 4:2:0 (see
// ffmpeg.FullChromaToMasterFilter, rgbToYUV empty). It returns their paths, named as
// createMasterSegments names the segments of a source: the segments the search and the final VMAF
// work on. durations are those of the segments, for the progress.
//
// # WHY THIS EXISTS
//
// sptenc converts an RGB source to YUV once, with a matrix it states, when it writes its master
// (see ffmpeg.YUVMatrix), and subsamples the chroma of a full chroma one there, sited where decoders
// read it. A pre-split directory has no master: its segments reached the encoders as they were, and
// each encoder converted RGB ones its own way, with BT.601's matrix (measured with libx265 and
// hevc_nvenc), into an output declaring no matrix, whose colors shift when read with BT.709's.
// Splitting a source, upscaling its segments, which upscalers deliver in RGB, then encoding them, is
// a workflow pre-split directories are made for: their segments get the master a source gets.
//
// # WHY NOT CONVERT AT EVERY STEP
//
// The conversion would go into the path of every encoder and into the reference of every VMAF
// pass, each one a place to get it wrong. Converted once, the encoders, the search and the final
// VMAF read the very same pictures, as with a source. The masters take room in outputDir, as the
// master of a source does.
//
// # EDGE CASES
//
//   - The segments are decoded by the CPU: upscalers deliver lossless RGB, such as FFV1, which no
//     GPU decodes.
//   - The 4:2:0 segments of a directory holding full chroma ones get a master too: their pictures
//     are kept as they are.
func writeSegmentsMasters(ctx context.Context, segmentsPaths []string, durations []time.Duration, outputDir string,
	rgbToYUV ffmpeg.YUVMatrix, debug bool) (masters []string, err error) {
	fmt.Fprintf(liveprogress.Bypass(), "Writing the 4:2:0 masters of the %d segments...\n", len(segmentsPaths))
	start := time.Now()
	masters = make([]string, len(segmentsPaths))
	for i := range segmentsPaths {
		masters[i] = filepath.Join(outputDir, fmt.Sprintf(ffmpeg.SegmentOutputFormat, i))
	}
	if err = liveSegmentsMasters(ctx, segmentsPaths, masters, durations, rgbToYUV, debug); err != nil {
		return nil, fmt.Errorf("failed to write the masters of the segments: %w", err)
	}
	var size int64
	for _, master := range masters {
		var fileInfos os.FileInfo
		if fileInfos, err = os.Stat(master); err != nil {
			return nil, fmt.Errorf("failed to stat segment master: %w", err)
		}
		size += fileInfos.Size()
	}
	fmt.Fprintf(liveprogress.Bypass(), "\tMasters written in %s (%s)\n", time.Since(start).Round(time.Second),
		cunits.ImportInBytes(float64(size)))
	return
}

// countMasterFrames counts the frames of the source a master is made from, and rejects it when
// they do not last the same time, with the hardware decoder of decoderCfg.
func countMasterFrames(ctx context.Context, inputFilePath string, debug bool, decoderCfg ffmpeg.HWDecoderConfig) (
	totalFrames int, duration time.Duration, err error) {
	// count frames
	fmt.Fprintln(liveprogress.Bypass(), "Counting the exact number of frames...")
	start := time.Now()
	videoInfos, duration, err := liveProbeVideoCF(ctx, inputFilePath, debug, decoderCfg)
	if err != nil {
		err = fmt.Errorf("failed to count number of frames: %w", err)
		return
	}
	totalFrames = videoInfos.NbReadFrames
	fmt.Fprintf(liveprogress.Bypass(), "\tCounted %d %s frames in %s\n",
		totalFrames, videoInfos.CodecName, time.Since(start).Round(time.Second),
	)
	// The frame rates declared by the source have been checked already (checkSourceVideo), but
	// they can not be trusted with every container: now that every frame has been read, check
	// how long they really last (see FFProbeBinaryStream.IsConstantFrameRate).
	if !videoInfos.IsConstantFrameRate() {
		err = fmt.Errorf("variable frame rate (VFR) content is not supported: frames last from %s to %s while the source declares a constant frame rate (%s fps)",
			videoInfos.ShortestFrameDuration, videoInfos.LongestFrameDuration, videoInfos.RFrameRate)
		return
	}
	return
}
