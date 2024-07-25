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
	input   *string
	tmpDir  *string
	output  *string
	startQP *int
	gpu     *int
	nvdec   *bool
	nvenc   *bool
	debug   *bool
	keep    *bool
	//// vmaf
	vmafcuda       *bool
	vmafNEG        *bool
	vmafLimitMin   *float64
	vmafLimitP1    *float64
	vmafLimitHMean *float64
	vmafLimitMean  *float64
	// Run
	children         Children
	workingDirectory string
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
				if err := os.RemoveAll(workingDirectory); err != nil {
					fmt.Fprintf(os.Stderr, "Failed to clean working directory: %s\n", err)
					exitCode = 3
				}
			}
		case 1:
			// warmup / config issue
			// no tmp files to delete
		case 2:
			// runtime error, keep tmp files even if no -keep flag
			fmt.Fprintf(os.Stderr, "Temporary files has been kept for inspection: %s\n", workingDirectory)
		case 3:
			// exit error
		}
		os.Exit(exitCode)
	}()
	// Flags
	input = flag.String("input", "", "Input file to transcode.")
	tmpDir = flag.String("tmp", os.TempDir(), "Where to create the working directory to store reencoded parts and VMAF reports.")
	output = flag.String("output", "", "Output directory for the reencoded file. If empty, directory of input file will be used.")
	startQP = flag.Int("qp", 18, "Quantization Parameter value to start part encoding with. The higher the value, the more aggressive the encoding will be. Speed up process by setting a QP close to your VMAF limits.")
	gpu = flag.Int("gpu", 0, "GPU to use for hardware acceleration")
	nvdec = flag.Bool("nvdec", false, "Use NVIDIA CUDA acceleration for video decoding (NVDEC).")
	nvenc = flag.Bool("nvenc", false, "Use NVIDIA CUDA acceleration for video encoding (NVENC).")
	debug = flag.Bool("debug", false, "Print more logs, especially the executed commands.")
	keep = flag.Bool("keep", false, "Keep temporary files (beware of disk space usage !). Usefull for debugging only.")
	//// vmaf
	vmafcuda = flag.Bool("vmafcuda", false, "Activate CUDA acceleration for VMAF computing. libvmaf must have been compiled with CUDA support.")
	vmafNEG = flag.Bool("vmafneg", false, "Use VMAF NEG (No Enhancement Gain) alternative models. Can be useful when the original file has a different encoder. Beware that it can dramatically lower VMAF scoring.")
	vmafLimitMin = flag.Float64("vmafmin", 90, "VMAF acceptable score for the worst frame. If the VMAF score is below this value, the part encoding will be considered as invalid and a new encode will be done. If -1, this VMAF minimum score is not used.")
	vmafLimitP1 = flag.Float64("vmafp1", 95, "VMAF acceptable score for percentil 1. If the VMAF score is below this value, the part encoding will be considered as invalid and a new encode will be done. If -1, this VMAF minimum score is not used.")
	vmafLimitHMean = flag.Float64("vmafhmean", VMAFOffValue, "VMAF acceptable score for harmonic mean. If the VMAF score is below this value, the part encoding will be considered as invalid and a new encode will be done. If -1, this VMAF minimum score is not used.")
	vmafLimitMean = flag.Float64("vmafmean", 98, "VMAF acceptable score for mean. If the VMAF score is below this value, the part encoding will be considered as invalid and a new encode will be done. If -1, this VMAF minimum score is not used.")
	version := flag.Bool("version", false, "Show the current version of the Parts Encoder.")
	flag.Parse()
	if *version {
		fmt.Printf("%s %s\n", liveterm.Hyperlink(sptencURLTagValue, "Sp(li)tEnc(oder)"), Version)
		return
	}
	// Validate common flags
	if *input == "" {
		fmt.Fprintln(os.Stderr, "Please set the -input flag")
		exitCode = 1
		return
	}
	if *startQP < ffmpegutils.QPMinimum || *startQP > ffmpegutils.QPMaximum {
		fmt.Fprintf(os.Stderr, "Start QP must be between [%d, %d]\n", ffmpegutils.QPMinimum, ffmpegutils.QPMaximum)
		exitCode = 1
		return
	}
	if *gpu < 0 {
		fmt.Fprintln(os.Stderr, "GPU must be >= 0")
		exitCode = 1
		return
	}
	vmafAuditor, err := NewVMAFChecker(*vmafLimitMin, *vmafLimitP1, *vmafLimitHMean, *vmafLimitMean)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create VMAF auditor: %s\n", err)
		exitCode = 1
		return
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
	workingDirectory = generateWorkingDirectroryPath(*tmpDir)
	// Properly handle stop
	runCtx, _ := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	go cleanStop(runCtx)
	// Prepare live output
	if err = liveprogress.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to start liveprogress: %s\n", err)
		exitCode = 1
		return
	}
	defer func() {
		if err = liveprogress.Stop(true); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to stop liveprogress: %s\n", err)
			if exitCode == 0 {
				exitCode = 3
			}
		}
	}()
	// Ready, start processing
	liveprogress.AddCustomLine(func() string { return "" }) // separate logs and progress
	exitCode = sptenc(vmafAuditor)
}

func sptenc(auditor VMAFChecker) (exitCode int) {
	var err error
	bypass := liveprogress.Bypass()
	start := time.Now()
	// Prepare
	//// Working directory
	if err = os.MkdirAll(workingDirectory, 0755); err != nil {
		fmt.Fprintf(bypass, "Failed to create working directory: %s\n", err)
		exitCode = 2
		return
	}
	if *debug {
		fmt.Fprintf(bypass, "Working directory: %s\n", workingDirectory)
	}
	//// Filesystems check (to be removed when properly tested)
	var sameFS bool
	if sameFS, err = sameFileSystem(*input, workingDirectory); err != nil {
		fmt.Fprintf(bypass, "Failed to check filesystems: %s\n", err)
		exitCode = 2
		return
	}
	if *debug {
		fmt.Fprintf(bypass, "Same filesystem: %t\n", sameFS)
	}
	//// Input file container infos
	stats, err := getStreamsInfos(*input)
	if err != nil {
		fmt.Fprintf(bypass, "Failed to probe input file: %s\n", err)
		exitCode = 2
		return
	}
	// Step 1 - Split file by GOP
	fmt.Fprintf(bypass, "Splitting file by groups of pictures (GOP)...\n")
	if err = splitFile(*input, workingDirectory, stats.Format.Duration); err != nil {
		fmt.Fprintf(bypass, "Failed to split parts: %s\n", err)
		exitCode = 2
		return
	}
	parts, err := getDirFilesNumber(workingDirectory)
	if err != nil {
		fmt.Fprintf(bypass, "Failed to get number of splitted GOPs: %s\n", err)
		exitCode = 2
		return
	}
	fmt.Fprintf(bypass, "Splitting managed to separate the file in %d parts\n", parts)
	// Step 2 - Encode parts
	fmt.Fprintf(bypass, "Searching the right QP for each parts...\n")
	var (
		partsQP []int
		statsQP QPStats
	)
	if partsQP, statsQP, err = findPartsQP(workingDirectory, parts, stats.Format.Duration, auditor); err != nil {
		fmt.Fprintf(bypass, "Failed to encode parts: %s\n", err)
		exitCode = 2
		return
	}
	// Step 3 - Merge parts and remux original file
	fmt.Fprintln(bypass, "Remuxing encoded parts to final file...")
	finalFilePath := computeNewDirFilePath(*input, workingDirectory)
	tagsFlags := generateTags(*stats.Format, statsQP)
	if err = partsMerge(*input, workingDirectory, finalFilePath, tagsFlags, partsQP, stats.Format.Duration); err != nil {
		fmt.Fprintf(bypass, "Failed to merge parts: %s\n", err)
		exitCode = 2
		return
	}
	fmt.Fprintf(bypass, "Output has been written to: %s\n", finalFilePath)
	// Step 4 - Recompute MKV stats if necessary
	fmt.Fprintf(bypass, "Regenerating MKV stats...\n")
	if err = regenerateMKVStats(finalFilePath); err != nil {
		fmt.Fprintf(bypass, "Failed to regenerate MKV stats: %s\n", err)
		exitCode = 2
		return
	}
	// Step 5 - Check both files
	fmt.Fprintf(bypass, "Checking both files...\n")
	originalStats, err := getStreamsInfosCF(*input)
	if err != nil {
		fmt.Fprintf(bypass, "Failed to get original file stats: %s\n", err)
		exitCode = 2
		return
	}
	reencodedStats, err := getStreamsInfosCF(finalFilePath)
	if err != nil {
		fmt.Fprintf(bypass, "Failed to get reencoded file stats: %s\n", err)
		exitCode = 2
		return
	}
	if originalStats.VideoTrack().NbReadFrames != reencodedStats.VideoTrack().NbReadFrames {
		fmt.Fprintf(bypass, "Number of read frames is different between original and reencoded files: original has %s and reencoded has %s\n",
			originalStats.VideoTrack().NbReadFrames, reencodedStats.VideoTrack().NbReadFrames)
		exitCode = 2
		return
	}
	// Step 6 - Move final file to output directory
	fmt.Fprintf(bypass, "Moving final file to output directory...\n")
	finalOutputPath := computeNewDirFilePath(finalFilePath, *output)
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

func cleanStop(ctx context.Context) {
	var err error
	<-ctx.Done()
	fmt.Fprintf(liveprogress.Bypass(), "Stop signal catched, stopping...\n")
	// Stop subprocess if any
	if err = children.StopAndWait(); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to stop and wait for current child process(es): %s\n", err)
	}
	// Stop UI
	if err = liveprogress.Stop(false); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to stop liveprogress properly: %s\n", err)
	}
	// Cleanup
	if *keep {
		fmt.Fprintf(os.Stdout, "You can find kept temporary files here: %s\n", workingDirectory)
	} else {
		if err := os.RemoveAll(workingDirectory); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to clean working directory: %s\n", err)
		}
	}
	os.Exit(3)
}
