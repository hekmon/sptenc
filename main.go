package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/hekmon/ffmpegutils"
	"github.com/hekmon/liveprogress/v2"
	"github.com/hekmon/liveterm/v2"
)

var (
	//  overrided during compilation
	Version = "dev"
	// Flags
	input       *string
	source      *string
	tmpDir      *string
	output      *string
	animeTuning *bool
	gpu         *int
	nvdec       *bool
	nvenc       *bool
	force10bits *bool
	flac        *bool
	debug       *bool
	keep        *bool
	//// vmaf
	vmafcuda        *bool
	vmafNEG         *bool
	vmafLimitMinAlt *float64
	vmafLimitMin    *float64
	vmafLimitP1     *float64
	vmafLimitP5     *float64
	vmafLimitP10    *float64
	vmafLimitP25    *float64
	vmafLimitMedian *float64
	vmafLimitHMean  *float64
	vmafLimitMean   *float64
	// Run
	workingDirectory string
	interrupted      bool
)

func main() {
	var exitCode int
	defer func() {
		switch exitCode {
		case 0:
			// all good
			if *keep {
				fmt.Fprintf(os.Stdout, "You can find kept temporary files here: %s\n", workingDirectory)
			} else {
				fmt.Fprintf(os.Stdout, "Cleaning working directory...")
				if err := os.RemoveAll(workingDirectory); err != nil {
					fmt.Fprintf(os.Stderr, "Failed to clean working directory: %s\n", err)
					exitCode = 3
				}
				fmt.Fprintf(os.Stdout, "Done.\n")
			}
		case 1:
			// warmup / config issue
			// no tmp files to delete
		case 2:
			// runtime error, keep tmp files even if no -keep flag
			fmt.Fprintf(os.Stderr, "Temporary work directory has been kept for inspection: %s\n", workingDirectory)
		case 3:
			// exit error
		}
		os.Exit(exitCode)
	}()
	// Flags
	input = flag.String("input", "", "Input file to transcode or directory containing pre-segmented GOP files.")
	source = flag.String("source", "", "Original source file with audio for remux (optional, used when -input is a directory).")
	tmpDir = flag.String("tmp", os.TempDir(), "Where to create the working directory to store reencoded GOP and VMAF reports.")
	output = flag.String("output", "", "Output directory for the reencoded file. If empty, directory of input file will be used.")
	animeTuning = flag.Bool("anime", false, "Use anime tuning parameters. Only for libx265 encoder, ignored for NVENC.")
	gpu = flag.Int("gpu", 0, "GPU to use for hardware acceleration")
	nvdec = flag.Bool("nvdec", false, "Use NVIDIA CUDA acceleration for video decoding (NVDEC).")
	nvenc = flag.Bool("nvenc", false, "Use NVIDIA CUDA acceleration for video encoding (NVENC). While faster, NVENC tends to produce more than 2x bigger files than libx265 for the same perceived quality. Recommended to quickly find a visually acceptable VMAF profile before switching to libx265 for a smaller file size.")
	force10bits = flag.Bool("force10bits", false, "Force 10 bits encoding. This is normaly not necessary as all regular 8 bits input files (with yup420p pixel format) will be automaticaly converted to 10bits (with p010le pixel format). Use this flag to force the conversion not matter the input file's pixel format.")
	flac = flag.Bool("flac", false, "Encode the audio in FLAC during the merging phase if the input audio is in PCM.")
	debug = flag.Bool("debug", false, "Print more logs, especially the executed commands.")
	keep = flag.Bool("keep", false, "Keep temporary files (beware of disk space usage !). Usefull for debugging only.")
	//// vmaf
	vmafcuda = flag.Bool("vmafcuda", false, "Activate CUDA acceleration for VMAF computing. libvmaf must have been compiled with CUDA support.")
	vmafNEG = flag.Bool("vmafneg", false, "Use VMAF NEG (No Enhancement Gain) alternative models. Recommended when either source has undergone upscaling, sharpening, or denoising, as these can artificially inflate standard VMAF scores. NEG models provide more conservative scoring by ignoring enhancement gains. Expect lower scores compared to standard VMAF models.")
	vmafLimitMinAlt = flag.Float64("vmafminalt", VMAFOffValue, "VMAF alternate acceptable score for the worst frame. Sometimes (especialy when setting high VMAF config such as 100 in a percentil) even with QP 0 a scene won't match its VMAF config (indicated by best effort in the logs). This can dramatically increase output file size. This parameter setup an alternate VMAF validator that only force a minimum value to let the distribution of frames do what it can be that is used if a GOP has reached best effort. Advanced feature, you should start without and consider it if you encounter a lot of best effort GOP results. If -1, this alternate VMAF validator is not used.")
	vmafLimitMin = flag.Float64("vmafmin", VMAFOffValue, "VMAF acceptable score for the worst frame. If the VMAF score for a GOP encoding is below this value, the encoding will be considered as invalid and a new encode will be done. If -1, this VMAF minimum score is not used.")
	vmafLimitP1 = flag.Float64("vmafp1", 93, "VMAF acceptable score for percentil 1. If the VMAF score for a GOP encoding is below this value, the encoding will be considered as invalid and a new encode will be done. If -1, this VMAF minimum score is not used.")
	vmafLimitP5 = flag.Float64("vmafp5", VMAFOffValue, "VMAF acceptable score for percentil 5. If the VMAF score for a GOP encoding is below this value, the encoding will be considered as invalid and a new encode will be done. If -1, this VMAF minimum score is not used.")
	vmafLimitP10 = flag.Float64("vmafp10", VMAFOffValue, "VMAF acceptable score for percentil 10. If the VMAF score for a GOP encoding is below this value, the encoding will be considered as invalid and a new encode will be done. If -1, this VMAF minimum score is not used.")
	vmafLimitP25 = flag.Float64("vmafp25", VMAFOffValue, "VMAF acceptable score for percentil 25. If the VMAF score for a GOP encoding is below this value, the encoding will be considered as invalid and a new encode will be done. If -1, this VMAF minimum score is not used.")
	vmafLimitMedian = flag.Float64("vmafmedian", VMAFOffValue, "VMAF acceptable score for median (percentil 50). If the VMAF score for a GOP encoding is below this value, the encoding will be considered as invalid and a new encode will be done. If -1, this VMAF minimum score is not used.")
	vmafLimitHMean = flag.Float64("vmafhmean", VMAFOffValue, "VMAF acceptable score for harmonic mean. If the VMAF score for a GOP encoding is below this value, the encoding will be considered as invalid and a new encode will be done. If -1, this VMAF minimum score is not used.")
	vmafLimitMean = flag.Float64("vmafmean", 99, "VMAF acceptable score for mean. If the VMAF score for a GOP encoding is below this value, the encoding will be considered as invalid and a new encode will be done. If -1, this VMAF minimum score is not used.")
	version := flag.Bool("version", false, "Show the current version of the GOP Encoder.")
	flag.Parse()
	if *version {
		fmt.Printf("%s %s\n", liveterm.Hyperlink(sptencURLTagValue, "SplitEncoder"), Version)
		return
	}
	// Validate common flags
	if *input == "" {
		fmt.Fprintln(os.Stderr, "Please set the -input flag")
		exitCode = 1
		return
	}

	if *gpu < 0 {
		fmt.Fprintln(os.Stderr, "GPU must be >= 0")
		exitCode = 1
		return
	}
	vmafAuditor, err := NewVMAFChecker(*vmafLimitMin, *vmafLimitP1, *vmafLimitP5, *vmafLimitP10, *vmafLimitP25, *vmafLimitMedian, *vmafLimitHMean, *vmafLimitMean)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create VMAF auditor: %s\n", err)
		exitCode = 1
		return
	}
	var vmafAuditorAlt *VMAFChecker
	if *vmafLimitMinAlt != VMAFOffValue {
		if vmafAuditorAlt, err = NewVMAFChecker(*vmafLimitMinAlt, VMAFOffValue, VMAFOffValue, VMAFOffValue, VMAFOffValue, VMAFOffValue, VMAFOffValue, VMAFOffValue); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to create VMAF alt auditor: %s\n", err)
			exitCode = 1
			return
		}
		if *vmafLimitMinAlt <= *vmafLimitMin {
			fmt.Fprintln(os.Stdout, "WARNING: VMAF alt minimum limit is lower than the VMAF minimum limit (weird)")
		}
	}
	// Switch to full paths
	currentWorkingDirectory, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to get current working directory: %s\n", err)
		exitCode = 1
		return
	}
	if !filepath.IsAbs(*input) {
		*input = filepath.Join(currentWorkingDirectory, *input)
	}
	if *output != "" && !filepath.IsAbs(*output) {
		*output = filepath.Join(currentWorkingDirectory, *output)
	}
	if !filepath.IsAbs(*tmpDir) {
		*tmpDir = filepath.Join(currentWorkingDirectory, *tmpDir)
	}
	// If source is provided, convert to absolute path and validate
	if *source != "" {
		if !filepath.IsAbs(*source) {
			*source = filepath.Join(currentWorkingDirectory, *source)
		}
		sourceInfo, err := os.Stat(*source)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to access source file: %s\n", err)
			exitCode = 1
			return
		}
		if sourceInfo.IsDir() {
			fmt.Fprintln(os.Stderr, "Source must be a file, not a directory")
			exitCode = 1
			return
		}
	}
	workingDirectory = generateWorkingDirectoryPath(*tmpDir)
	// Load previous ideal QPs
	if err = loadStats(); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load previous ideal QPs: %s\n", err)
		exitCode = 1
		return
	}
	// Properly handle stop
	runCtx, _ := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	// Prepare live output
	if err = liveprogress.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to start liveprogress: %s\n", err)
		exitCode = 1
		return
	}
	defer func() {
		if err = liveprogress.Stop(exitCode == 0); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to stop liveprogress: %s\n", err)
			if exitCode == 0 {
				exitCode = 3
			}
		}
	}()
	// Ready, start processing
	liveprogress.AddCustomLine(func() string { return "" }) // separate logs and progress
	exitCode = sptenc(runCtx, vmafAuditor, vmafAuditorAlt)
}

func sptenc(ctx context.Context, auditor, auditorAlt *VMAFChecker) (exitCode int) {
	var err error
	bypass := liveprogress.Bypass()
	start := time.Now()
	// Prepare
	fmt.Fprintf(bypass, "Input: %q\n", filepath.Base(*input))
	//// Working directory
	if err = os.MkdirAll(workingDirectory, 0755); err != nil {
		fmt.Fprintf(bypass, "Failed to create working directory: %s\n", err)
		exitCode = 2
		return
	}
	if *debug {
		fmt.Fprintf(bypass, "Working directory: %s\n", workingDirectory)
	}
	// Check if input is a directory
	inputInfo, err := os.Stat(*input)
	if err != nil {
		fmt.Fprintf(bypass, "Failed to access input: %s\n", err)
		exitCode = 2
		return
	}
	inputIsDir := inputInfo.IsDir()
	//// Input file container infos
	var stats ffmpegutils.FFProbeStats
	if inputIsDir {
		// For directory input, we need a source file for stream info
		var sourcePath string
		if *source != "" {
			sourcePath = *source
		} else {
			// Use first segment for stream info when no source is provided
			var segments []string
			if segments, err = getSegmentsFromDir(*input); err != nil {
				fmt.Fprintf(bypass, "Failed to get segments from directory: %s\n", err)
				exitCode = 2
				return
			}
			if len(segments) == 0 {
				fmt.Fprintln(bypass, "No segment files found in input directory")
				exitCode = 2
				return
			}
			sourcePath = segments[0]
		}
		if stats, err = getStreamsInfos(ctx, sourcePath); err != nil {
			fmt.Fprintf(bypass, "Failed to probe source file: %s\n", err)
			exitCode = 2
			return
		}
	} else {
		if stats, err = getStreamsInfos(ctx, *input); err != nil {
			fmt.Fprintf(bypass, "Failed to probe input file: %s\n", err)
			exitCode = 2
			return
		}
	}
	//// Allow user to visually check its VMAF configuration
	fmt.Fprintf(bypass, "Each GOP encoding will have to reach theses VMAF scores:\n%s", auditor)
	if auditorAlt != nil {
		fmt.Fprintf(bypass, "Alternative VMAF configuration for GOP reaching QP 0 as best effort:\n%s", auditorAlt)
	}
	// Step 1 - Split file by GOP (or use existing segments from directory)
	var (
		nbGOP         int
		segmentPaths  []string
		totalDuration time.Duration
	)
	if inputIsDir {
		fmt.Fprintln(bypass, "Using pre-segmented GOP files from input directory...")
		if segmentPaths, err = getSegmentsFromDir(*input); err != nil {
			fmt.Fprintf(bypass, "Failed to get segments from directory: %s\n", err)
			exitCode = 2
			return
		}
		nbGOP = len(segmentPaths)
		fmt.Fprintf(bypass, "Found %d GOP segments in directory\n", nbGOP)
		// Calculate total duration of all segments for accurate progress bar
		fmt.Fprintln(bypass, "Calculating total duration of segments...")
		if totalDuration, err = getSegmentsTotalDuration(ctx, segmentPaths); err != nil {
			fmt.Fprintf(bypass, "Failed to calculate total duration: %s\n", err)
			exitCode = 2
			return
		}
		fmt.Fprintf(bypass, "Total duration of segments: %s\n", totalDuration)
	} else {
		fmt.Fprintln(bypass, "Splitting video stream by groups of pictures (GOP)...")
		if err = splitFile(ctx, *input, workingDirectory, stats.Format.Duration); err != nil {
			fmt.Fprintf(bypass, "Failed to split GOP: %s\n", err)
			exitCode = 2
			return
		}
		if nbGOP, err = getDirFilesNumber(workingDirectory); err != nil {
			fmt.Fprintf(bypass, "Failed to get number of splitted GOPs: %s\n", err)
			exitCode = 2
			return
		}
		fmt.Fprintf(bypass, "Splitting managed to separate the file in %d GOP\n", nbGOP)
		totalDuration = stats.Format.Duration
	}
	// Step 2 - Encode GOPs
	meanAvg, stdDevAvg := previousRuns.GetMeanStdDev()
	fmt.Fprintf(bypass,
		"Searching the right QP for each GOP using %d as starting QP and %d as standard deviation range increment...\n",
		meanAvg, stdDevAvg,
	)
	var (
		GOPQP          []int
		statsQP        QPStats
		aggregatedVMAF *ffmpegutils.VMAFStats
	)
	if !*force10bits && stats.VideoTrack().PixFmt == "yuv420p" {
		*force10bits = true
		if *debug {
			fmt.Fprintf(bypass, "Activating 10bits encoding conversion because input is 8bits.\n")
		}
	}
	if GOPQP, statsQP, aggregatedVMAF, err = findAllGOPQP(
		ctx, workingDirectory, segmentPaths, nbGOP, meanAvg,
		stdDevAvg, totalDuration, auditor, auditorAlt, *force10bits,
	); err != nil {
		fmt.Fprintf(bypass, "Failed to encode GOP: %s\n", err)
		exitCode = 2
		return
	}
	// Step 3 - Merge segments and check them
	fmt.Fprintf(bypass, "Merging encoded GOP into one video stream...\n")
	concatScriptPath, err := generateConcatScript(workingDirectory, GOPQP)
	if err != nil {
		fmt.Fprintf(bypass, "Failed to generate concat script: %s\n", err)
		exitCode = 2
		return
	}
	concatVideoPath := filepath.Join(workingDirectory, "concat.mkv")
	if err = GOPMerge(ctx, concatScriptPath, concatVideoPath, totalDuration); err != nil {
		fmt.Fprintf(bypass, "Failed to merge encoded GOP: %s\n", err)
		exitCode = 2
		return
	}
	// Step 4 - Remux new video to final file
	tagsFlags := generateTags(*stats.Format, statsQP, aggregatedVMAF, stats.VideoTrack().Height >= ffmpegutils.UltraHDHeight)
	var finalFilePath string
	if !inputIsDir || (inputIsDir && *source != "") {
		fmt.Fprintln(bypass, "Remuxing to final file...")
		var remuxSource string
		if inputIsDir {
			remuxSource = *source
		} else {
			remuxSource = *input
		}
		finalFilePath = computeNewDirFilePath(remuxSource, workingDirectory, true)
		var convertFlac bool
		if *flac {
			audioTrack := stats.AudioTrack()
			if audioTrack != nil &&
				(audioTrack.CodecName == ffmpegutils.CodecAudioPCM || audioTrack.CodecName == ffmpegutils.CodecAudioPCM24b) {
				convertFlac = true
				if *debug {
					fmt.Fprintln(bypass, "Input has PCM audio and -flac flag is on: audio stream will be converted to FLAC")
				}
			}
		}
		if err = RemuxDual(ctx, remuxSource, concatVideoPath, finalFilePath, tagsFlags, totalDuration, convertFlac); err != nil {
			fmt.Fprintf(bypass, "Failed to merge GOP: %s\n", err)
			exitCode = 2
			return
		}
	} else {
		// source is dir but we do not have a source file
		finalFilePath = computeNewDirFilePath(filepath.Join(*input, "segments.mkv"), workingDirectory, true)
		if err = Remux(ctx, concatVideoPath, finalFilePath, tagsFlags, totalDuration); err != nil {
			fmt.Fprintf(bypass, "Failed to merge GOP: %s\n", err)
			exitCode = 2
			return
		}
	}
	finalFileSize, err := getFileSize(finalFilePath)
	if err != nil {
		fmt.Fprintf(bypass, "Failed to get final file size: %s\n", err)
		exitCode = 2
		return
	}
	fmt.Fprintf(bypass, "Final file %q size: %s\n", filepath.Base(finalFilePath), finalFileSize)
	// Step 5 - Recompute MKV stats if necessary
	fmt.Fprintf(bypass, "Regenerating MKV stats...\n")
	if err = regenerateMKVStats(ctx, finalFilePath); err != nil {
		fmt.Fprintf(bypass, "Failed to regenerate MKV stats: %s\n", err)
		exitCode = 2
		return
	}
	// Step 6 - Move final file to output directory
	fmt.Fprintf(bypass, "Moving final file to output directory...\n")
	finalOutputPath := computeNewDirFilePath(finalFilePath, *output, false)
	if err = MoveProgress(finalFilePath, finalOutputPath); err != nil {
		fmt.Fprintf(bypass, "Failed to move final file to output directory: %s\n", err)
		exitCode = 2
		return
	}
	fmt.Fprintf(bypass, "Final file has been moved to: %s\n", finalOutputPath)
	// Done
	duration := time.Since(start)
	fmt.Fprintf(bypass, "Complete process took %s\n", duration.Round(time.Second))
	return
}
