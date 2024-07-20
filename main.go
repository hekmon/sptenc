package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/hekmon/liveprogress/v2"
)

var (
	// Flags
	input          *string
	workingDir     *string
	output         *string
	sceneThreshold *int
	gpu            *int
	nvc            *bool
	debug          *bool
	// Reencoder flags
	vmafcuda *bool
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
	input = flag.String("input", "", "Input file or directory. If input is a directory all video files of the directory and all the sub directories will be processed.")
	workingDir = flag.String("tmp", getWorkingDirPath(), "Where to create the working directory to store extracted and upscaled frames. You should put it on a fast SSD with plenty of space.")
	output = flag.String("output", "", "Output directory for upscailing and master modes, distorted file when using -vmaf alone.")
	sceneThreshold = flag.Int("scenethreshold", 14, "Scene detection threshold. Between 0 and 100.")
	gpu = flag.Int("gpu", 0, "GPU to use for hardware acceleration")
	nvc = flag.Bool("nvc", false, "Use NVIDIA CUDA acceleration for video decoding (NVDEC) and video encoding (NVENC). Recommended for NVIDIA graphic (and not compute!) cards.")
	debug = flag.Bool("debug", false, "Print more logs, especially the executed commands.")
	vmafcuda = flag.Bool("vmafcuda", false, "Activate CUDA acceleration for VMAF computing (see -vmaf). libvmaf must have been compiled with CUDA support.")
	flag.Parse()
	// Validate common flags
	if *input == "" {
		fmt.Fprintln(os.Stderr, "Please set the -input flag")
		exitCode = 1
		return
	}
	if *output == "" {
		fmt.Fprintln(os.Stderr, "Please set the -output flag")
		exitCode = 1
		return
	}
	if *sceneThreshold < 0 || *sceneThreshold > 100 {
		fmt.Fprintln(os.Stderr, "Scene threshold must be between 0 and 100")
		exitCode = 1
		return
	}
	if *gpu < 0 {
		fmt.Fprintln(os.Stderr, "GPU must be >= 0")
		exitCode = 1
		return
	}
	//// switch to full paths
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
	bypass := liveprogress.Bypass()
	// Probe file
	stats, err := getStreamsInfos(*input)
	if err != nil {
		fmt.Fprintf(bypass, "Failed to probe input file: %s\n", err)
		exitCode = 2
		return
	}
	// Detect scenes
	scenes, err := getScenes(*input, stats.Format.Duration, *sceneThreshold, *nvc, gpu)
	if err != nil {
		fmt.Fprintf(bypass, "Failed to detect scenes: %s\n", err)
		exitCode = 2
		return
	}
	// ChopChop file
	if err = splitScenes(*input, *workingDir, scenes); err != nil {
		fmt.Fprintf(bypass, "Failed to split scenes: %s\n", err)
		exitCode = 2
		return
	}
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
