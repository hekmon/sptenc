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
)

var (
	// Flags
	input          *string
	workingDir     *string
	output         *string
	sceneThreshold *float64
	startQP        *int
	gpu            *int
	nvc            *bool
	debug          *bool
	keep           *bool
	//// vmaf
	vmafcuda       *bool
	vmafLimitMin   *float64
	vmafLimitP1    *float64
	vmafLimitHMean *float64
	vmafLimitMean  *float64
	// Reencoder flags
	// Run
	children Children
)

func main() {
	var exitCode int
	defer func() {
		if exitCode != 0 {
			os.Exit(exitCode)
		}
	}()
	// Flags
	input = flag.String("input", "", "Input file to transcode.")
	workingDir = flag.String("tmp", os.TempDir(), "Where to create the working directory to store reencoded scenes and VMAF reports.")
	output = flag.String("output", "", "Output directory for the reencoded file. If empty directory of input file will be used.")
	sceneThreshold = flag.Float64("scenethreshold", 14, "Scene detection threshold. Valid range is [0., 100], good values are [8.0, 14.0].")
	startQP = flag.Int("qp", 16, "Quantization Parameter value to start with. The higher the value, the more aggressive the encoding will be. Speed up initial process by setting a QP close to your VMAF limits.")
	gpu = flag.Int("gpu", 0, "GPU to use for hardware acceleration")
	nvc = flag.Bool("nvc", false, "Use NVIDIA CUDA acceleration for video decoding (NVDEC) and video encoding (NVENC). Recommended for NVIDIA graphic (and not compute!) cards.")
	debug = flag.Bool("debug", false, "Print more logs, especially the executed commands.")
	keep = flag.Bool("keep", false, "Keep temporary files (beware of disk space usage !). Usefull for debugging only.")
	//// vmaf
	vmafcuda = flag.Bool("vmafcuda", false, "Activate CUDA acceleration for VMAF computing. libvmaf must have been compiled with CUDA support.")
	vmafLimitMin = flag.Float64("vmafmin", 90, "VMAF acceptable score for the worst frame. If the VMAF score is below this value, the scene encoding will be considered as invalid and a new encode will be done. If -1, the minimum VMAF score is not used.")
	vmafLimitP1 = flag.Float64("vmafp1", 95, "VMAF acceptable score for percentil 1. If the VMAF score is below this value, the scene encoding will be considered as invalid and a new encode will be done. If -1, the VMAF score is not used.")
	vmafLimitHMean = flag.Float64("vmafhmean", VMAFOffValue, "VMAF acceptable score for harmonic mean. If the VMAF score is below this value, the scene encoding will be considered as invalid and a new encode will be done. If -1, the VMAF score is not used.")
	vmafLimitMean = flag.Float64("vmafmean", 98, "VMAF acceptable score for mean. If the VMAF score is below this value, the scene encoding will be considered as invalid and a new encode will be done. If -1, the VMAF score is not used.")
	flag.Parse()
	// Validate common flags
	if *input == "" {
		fmt.Fprintln(os.Stderr, "Please set the -input flag")
		exitCode = 1
		return
	}
	if *sceneThreshold < 0 || *sceneThreshold > 100 {
		fmt.Fprintln(os.Stderr, "Scene threshold must be between 0 and 100")
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
	if !filepath.IsAbs(*output) {
		*output = filepath.Join(currentWorkingDirectory, *output)
	}
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
	exitCode = scenc(vmafAuditor)
}

func scenc(auditor VMAFChecker) (exitCode int) {
	bypass := liveprogress.Bypass()
	start := time.Now()
	// Prepare
	stats, err := getStreamsInfos(*input)
	if err != nil {
		fmt.Fprintf(bypass, "Failed to probe input file: %s\n", err)
		exitCode = 2
		return
	}
	tmpDir := getWorkingDirPath(*workingDir)
	if err = os.MkdirAll(tmpDir, 0755); err != nil {
		fmt.Fprintf(bypass, "Failed to create working directory: %s\n", err)
		exitCode = 2
		return
	}
	// Step 1 - Detect scenes
	fmt.Fprintf(bypass, "Detecting scenes...\n")
	scenes, err := getScenes(*input, stats.Format.Duration, *sceneThreshold, *nvc, gpu)
	if err != nil {
		fmt.Fprintf(bypass, "Failed to detect scenes: %s\n", err)
		exitCode = 2
		return
	}
	// Step 3 - Split file by scenes
	fmt.Fprintf(bypass, "Splitting scenes...\n")
	if err = splitScenes(*input, tmpDir, stats.Format.Duration, scenes); err != nil {
		fmt.Fprintf(bypass, "Failed to split scenes: %s\n", err)
		exitCode = 2
		return
	}
	splittedScenes, err := getDirFilesNumber(tmpDir)
	if err != nil {
		fmt.Fprintf(bypass, "Failed to get number of splitted scenes: %s\n", err)
		exitCode = 2
		return
	}
	fmt.Fprintf(bypass, "Splitting managed to separate %d scenes (on %d detected)\n", splittedScenes, len(scenes)+1)
	// Step 4 - Encode scenes
	fmt.Fprintf(bypass, "Searching the right QP for each scenes...\n")
	var scenesQP []int
	if scenesQP, err = findScenesQP(tmpDir, splittedScenes, *startQP, stats.Format.Duration, auditor, *nvc, *vmafcuda, *gpu); err != nil {
		fmt.Fprintf(bypass, "Failed to encode scenes: %s\n", err)
		exitCode = 2
		return
	}
	// Step 5 - Merge scenes
	fmt.Fprintln(bypass, "Merging scenes into one video stream...")
	outputPath := computeOutputFilePath(*input, *output)
	tagsFlags := generateTags(*stats.Format)
	if err = scenesMerge(*input, tmpDir, outputPath, tagsFlags, scenesQP, stats.Format.Duration); err != nil {
		fmt.Fprintf(bypass, "Failed to merge scenes: %s\n", err)
		exitCode = 2
		return
	}
	// Done
	if !*keep {
		if err = os.RemoveAll(tmpDir); err != nil {
			fmt.Fprintf(bypass, "Failed to clean working directory: %s\n", err)
		}
	}
	duration := time.Since(start)
	fmt.Fprintf(bypass, "Complete process took %s\n", duration.Round(time.Second))
	return
}

func cleanStop(ctx context.Context) {
	var err error
	<-ctx.Done()
	if err = liveprogress.Stop(false); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to stop liveprogress properly: %s\n", err)
	}
	fmt.Fprintf(os.Stderr, "Stop signal catched, stopping...\n")
	if err = children.StopAndWait(); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to stop and wait for current child process(es): %s\n", err)
	}
	os.Exit(2)
}
