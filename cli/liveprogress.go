package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hekmon/sptenc/core"
	"github.com/hekmon/sptenc/ffmpeg"
	"github.com/hekmon/sptenc/mkvtoolnix"

	"github.com/hekmon/cunits/v3"
	"github.com/hekmon/liveprogress/v2"
)

/*
 * Master
 */

func liveCountNbFrames(ctx context.Context, inputFilePath string, fileSize int64, debug bool) (
	nbFrames int, codec string, duration time.Duration, err error) {
	// prepare live progress
	analyzeBar := liveprogress.AddBar(
		liveprogress.WithTotal(uint64(fileSize)),
		liveprogress.WithMultiplyRunes(),
		// liveprogress.WithWidth(barsWidth),
		liveprogress.WithSameAutoSizeInternalPadding(true, false),
		liveprogress.WithPrependDecorator(func(bar *liveprogress.Bar) string {
			return "    Analyze | "
		}),
		liveprogress.WithPrependTimeElapsed(liveprogress.BaseStyle()),
		liveprogress.WithAppendPercent(liveprogress.BaseStyle()),
		liveprogress.WithAppendTimeRemaining(liveprogress.BaseStyle()),
		liveprogress.WithAppendDecorator(func(bar *liveprogress.Bar) string {
			return fmt.Sprintf(" left | %s/%s",
				cunits.ImportInBytes(float64(bar.Current())), cunits.ImportInBytes(float64(bar.Total())),
			)
		}),
	)
	defer liveprogress.RemoveBar(analyzeBar)
	analyzeProgress := func(n int) {
		analyzeBar.CurrentAdd(uint64(n))
	}
	// exec ffprobe
	mediaInfos, err := ffmpeg.GetStreamsInfosCF(ctx, ffmpeg.GetStreamsInfosCFConfig{
		GetStreamsInfosConfig: ffmpeg.GetStreamsInfosConfig{
			// Input
			Path: inputFilePath,
			// Reporting
			Debug: func(s string) {
				if debug {
					fmt.Fprintf(liveprogress.Bypass(), "DEBUG: %s\n", s)
				}
			},
			RuntimeError: func(err error) {
				fmt.Fprintf(liveprogress.Bypass(), "ERROR: %s\n", err)
			},
		},
		ReadBytesReport: analyzeProgress,
	})
	if err != nil {
		return
	}
	duration = mediaInfos.Format.Duration
	// extract number of frames from results
	videoInfos := mediaInfos.VideoTrack()
	if videoInfos == nil {
		err = errors.New("input file has no video stream")
		return
	}
	nbFrames = videoInfos.NbReadFrames
	codec = string(videoInfos.CodecName)
	return
}

func liveFFV1Master(ctx context.Context, inputFilePath, finalFile string, nbFrames int, debug bool, masterConfig ffmpeg.FFV1VideoMasterConfig) (err error) {
	// prepare live progress
	encodeBar := liveprogress.AddBar(
		liveprogress.WithTotal(uint64(nbFrames)),
		liveprogress.WithMultiplyRunes(),
		// liveprogress.WithWidth(barsWidth),
		liveprogress.WithSameAutoSizeInternalPadding(true, false),
		liveprogress.WithPrependDecorator(func(bar *liveprogress.Bar) string {
			return "     Encode | "
		}),
		liveprogress.WithPrependTimeElapsed(liveprogress.BaseStyle()),
		liveprogress.WithAppendPercent(liveprogress.BaseStyle()),
		liveprogress.WithAppendTimeRemaining(liveprogress.BaseStyle()),
		liveprogress.WithAppendDecorator(func(bar *liveprogress.Bar) string {
			return fmt.Sprintf(" | %d/%d frames", bar.Current(), bar.Total())
		}),
	)
	defer liveprogress.RemoveBar(encodeBar)
	progress := func(stats ffmpeg.ProgressStats) {
		encodeBar.CurrentSet(uint64(stats.CurrentFrame))
	}
	// build config
	config := ffmpeg.FFV1VideoMasterConfig{
		InputFilePath:   inputFilePath,
		OutputFilePath:  finalFile,
		NVDec:           masterConfig.NVDec,
		NVDevice:        masterConfig.NVDevice,
		VAAPIDec:        masterConfig.VAAPIDec,
		VAAPIDevice:     masterConfig.VAAPIDevice,
		D3D12Dec:        masterConfig.D3D12Dec,
		D3D12Device:     masterConfig.D3D12Device,
		VideoToolboxDec: masterConfig.VideoToolboxDec,
		Debug: func(s string) {
			if debug {
				fmt.Fprintf(liveprogress.Bypass(), "DEBUG: %s\n", s)
			}
		},
		RuntimeError: func(err error) {
			fmt.Fprintf(liveprogress.Bypass(), "ERROR: %s\n", err)
		},
		StatsReport: progress,
	}
	// encode
	return ffmpeg.FFV1VideoMaster(ctx, config)
}

/*
 * Split
 */

func liveDetectScenes(ctx context.Context, path string, threshold float64, totalDuration time.Duration, debug bool, scenesConfig ffmpeg.ScenesDetectionConfig) (scenes []ffmpeg.Scene, err error) {
	// live progress
	var currentStats ffmpeg.ProgressStats
	bar := liveprogress.AddBar(
		liveprogress.WithTotal(uint64(totalDuration)),
		liveprogress.WithMultiplyRunes(),
		// liveprogress.WithWidth(barsWidth),
		liveprogress.WithSameAutoSizeInternalPadding(true, false),
		liveprogress.WithPrependDecorator(func(bar *liveprogress.Bar) string {
			return "  Detecting | "
		}),
		liveprogress.WithPrependTimeElapsed(liveprogress.BaseStyle()),
		liveprogress.WithAppendPercent(liveprogress.BaseStyle()),
		liveprogress.WithAppendTimeRemaining(liveprogress.BaseStyle()),
		liveprogress.WithAppendDecorator(func(bar *liveprogress.Bar) string {
			return fmt.Sprintf(" left | speed: %0.2fx",
				currentStats.Speed,
			)
		}),
	)
	defer liveprogress.RemoveBar(bar)
	progress := func(stats ffmpeg.ProgressStats) {
		currentStats = stats
		bar.CurrentSet(uint64(stats.Time))
	}
	// Execute scene detection
	scenesConfig.Path = path
	scenesConfig.Threshold = threshold
	scenesConfig.Debug = func(s string) {
		if debug {
			fmt.Fprintf(liveprogress.Bypass(), "DEBUG: %s\n", s)
		}
	}
	scenesConfig.RuntimeError = func(err error) {
		fmt.Fprintf(liveprogress.Bypass(), "ERROR: %s\n", err)
	}
	scenesConfig.FFMPEGStatsReport = progress
	scenes, err = ffmpeg.ScenesDetection(ctx, scenesConfig)
	return
}

func liveSplitScenes(ctx context.Context, path, outputDir string, totalDuration time.Duration, scenes []ffmpeg.Scene, debug bool) (err error) {
	// live progress for splitting
	var currentStats ffmpeg.ProgressStats
	bar := liveprogress.AddBar(
		liveprogress.WithTotal(uint64(totalDuration)),
		liveprogress.WithMultiplyRunes(),
		// liveprogress.WithWidth(barsWidth),
		// liveprogress.WithSameAutoSizeInternalPadding(true, false),
		liveprogress.WithPrependDecorator(func(bar *liveprogress.Bar) string {
			return "  Splitting | "
		}),
		liveprogress.WithPrependTimeElapsed(liveprogress.BaseStyle()),
		liveprogress.WithAppendPercent(liveprogress.BaseStyle()),
		liveprogress.WithAppendTimeRemaining(liveprogress.BaseStyle()),
		liveprogress.WithAppendDecorator(func(bar *liveprogress.Bar) string {
			return fmt.Sprintf(" left | speed: %0.2fx",
				currentStats.Speed,
			)
		}),
	)
	defer liveprogress.RemoveBar(bar)
	progress := func(stats ffmpeg.ProgressStats) {
		currentStats = stats
		bar.CurrentSet(uint64(stats.Time))
	}
	// extract timestamps
	scenesMarkers := make([]time.Duration, len(scenes))
	for i, scene := range scenes {
		scenesMarkers[i] = scene.Start
	}
	// Execute segmentation
	return ffmpeg.Segment(ctx, ffmpeg.SegmentConfig{
		// Input
		Input: path,
		// Output
		ScenesMarkers: scenesMarkers,
		OutputDir:     outputDir,
		// Reporting
		Debug: func(s string) {
			if debug {
				fmt.Fprintf(liveprogress.Bypass(), "DEBUG: %s\n", s)
			}
		},
		RuntimeError: func(err error) {
			fmt.Fprintf(liveprogress.Bypass(), "ERROR: %s\n", err)
		},
		FFMPEGStatsReport: progress,
	})
}

/*
 * Encode
 */

func liveConcatDuration(ctx context.Context, workingDir, outputFile string, segments []string, totalDuration time.Duration, debug bool) (err error) {
	concatList, err := ffmpeg.GenerateConcatList(workingDir, segments)
	if err != nil {
		err = fmt.Errorf("failed to generate concat list file: %w", err)
		return
	}
	var speed float64
	concatBar := liveprogress.AddBar(
		liveprogress.WithTotal(uint64(totalDuration)),
		liveprogress.WithMultiplyRunes(),
		liveprogress.WithSameAutoSizeInternalPadding(true, false),
		liveprogress.WithPrependDecorator(func(bar *liveprogress.Bar) string {
			return "     Concat | "
		}),
		liveprogress.WithPrependTimeElapsed(liveprogress.BaseStyle()),
		liveprogress.WithAppendPercent(liveprogress.BaseStyle()),
		liveprogress.WithAppendTimeRemaining(liveprogress.BaseStyle()),
		liveprogress.WithAppendDecorator(func(bar *liveprogress.Bar) string {
			return fmt.Sprintf(" | speed: %sx", strconv.FormatFloat(speed, 'f', -1, 64))
		}),
	)
	defer liveprogress.RemoveBar(concatBar)
	progress := func(stats ffmpeg.ProgressStats) {
		concatBar.CurrentSet(uint64(stats.Time))
		speed = stats.Speed
	}
	return ffmpeg.Concat(ctx, ffmpeg.ConcatConfig{
		ConcatListPath: concatList,
		ConcatUnsafe:   true,
		OutputPath:     outputFile,
		Debug: func(s string) {
			if debug {
				fmt.Fprintf(liveprogress.Bypass(), "DEBUG: %s\n", s)
			}
		},
		RuntimeError: func(err error) {
			fmt.Fprintf(liveprogress.Bypass(), "ERROR: %s\n", err)
		},
		FFMPEGStatsReport: progress,
	})
}

/*
 * Probe
 */

func getStreamsInfos(ctx context.Context, path string, debug bool) (stats ffmpeg.FFProbeStats, err error) {
	return ffmpeg.GetStreamsInfos(ctx, ffmpeg.GetStreamsInfosConfig{
		// Input
		Path: path,
		// Reporting
		Debug: func(s string) {
			if debug {
				fmt.Fprintf(liveprogress.Bypass(), "DEBUG: %s\n", s)
			}
		},
		RuntimeError: func(err error) {
			fmt.Fprintf(liveprogress.Bypass(), "ERROR: %s\n", err)
		},
	})
}

func getStreamsInfosCF(ctx context.Context, path string, debug bool) (stats ffmpeg.FFProbeStats, err error) {
	// Prepare
	fileInfos, err := os.Stat(path)
	if err != nil {
		err = fmt.Errorf("failed to stat the file: %w", err)
		return
	}
	// Live Progress
	bar := liveprogress.AddBar(
		liveprogress.WithTotal(uint64(fileInfos.Size())),
		liveprogress.WithMultiplyRunes(),
		// liveprogress.WithWidth(barsWidth),
		liveprogress.WithSameAutoSizeInternalPadding(true, false),
		liveprogress.WithPrependDecorator(func(bar *liveprogress.Bar) string {
			return "    Analyze | "
		}),
		liveprogress.WithAppendPercent(liveprogress.BaseStyle()),
		liveprogress.WithAppendDecorator(func(bar *liveprogress.Bar) string {
			return fmt.Sprintf(" | %s/%s",
				cunits.ImportInBytes(float64(bar.Current())), cunits.ImportInBytes(float64(bar.Total())),
			)
		}),
	)
	defer liveprogress.RemoveBar(bar)
	progress := func(n int) {
		bar.CurrentAdd(uint64(n))
	}
	// Execute
	return ffmpeg.GetStreamsInfosCF(ctx, ffmpeg.GetStreamsInfosCFConfig{
		GetStreamsInfosConfig: ffmpeg.GetStreamsInfosConfig{
			// Input
			Path: path,
			// Reporting
			Debug: func(s string) {
				if debug {
					fmt.Fprintf(liveprogress.Bypass(), "DEBUG: %s\n", s)
				}
			},
			RuntimeError: func(err error) {
				fmt.Fprintf(liveprogress.Bypass(), "ERROR: %s\n", err)
			},
		},
		ReadBytesReport: progress,
	})
}

// LiveQPSearch received and process search progress signals to translate them as terminal UI progress
// it implements the core.QPSearchCallbacks interface required by core.FindAllSegmentsQP()
type LiveQPSearch struct {
	PrintDebug bool
	// Global progress
	globalProgressBar    *liveprogress.Bar
	globalNbSegmentsDone int
	globalAllSegmentSize cunits.Bits
	globalAccess         sync.Mutex
	/*
	 * Per worker
	 */
	// Segment progress (title + qp candidates listing)
	segmentCurrent          []int
	segmentStatusLine       []*liveprogress.CustomLine
	segmentCandidates       [][]string
	segmentCandidatesAccess []sync.Mutex
	// File analysis
	analysisProgressBar []*liveprogress.Bar
	// Encode
	encodeProgressBar []*liveprogress.Bar
	// VMAF
	vmafProgressBar []*liveprogress.Bar
}

func (to *LiveQPSearch) Start(totalSegments int, globalDuration time.Duration) {
	to.globalProgressBar = liveprogress.SetMainLineAsBar(
		liveprogress.WithTotal(uint64(globalDuration)),
		liveprogress.WithMultiplyRunes(),
		// liveprogress.WithWidth(barsWidth),
		liveprogress.WithSameAutoSizeInternalPadding(true, false),
		liveprogress.WithPrependDecorator(func(bar *liveprogress.Bar) string {
			return "   Progress | "
		}),
		liveprogress.WithPrependTimeElapsed(liveprogress.BaseStyle()),
		liveprogress.WithAppendPercent(liveprogress.BaseStyle()),
		liveprogress.WithAppendTimeRemaining(liveprogress.BaseStyle()),
		liveprogress.WithAppendDecorator(func(bar *liveprogress.Bar) string {
			return fmt.Sprintf(" left | %d/%d segments | %s",
				to.globalNbSegmentsDone, totalSegments, to.globalAllSegmentSize,
			)
		}),
	)
}

func (to *LiveQPSearch) cleanupSegmentUI() {
	if to.segmentStatusLine != nil {
		liveprogress.RemoveCustomLine(to.segmentStatusLine)
		to.segmentStatusLine = nil
	}
	if to.analysisProgressBar != nil {
		liveprogress.RemoveBar(to.analysisProgressBar)
		to.analysisProgressBar = nil
	}
	if to.encodeProgressBar != nil {
		liveprogress.RemoveBar(to.encodeProgressBar)
		to.encodeProgressBar = nil
	}
	if to.vmafProgressBar != nil {
		liveprogress.RemoveBar(to.vmafProgressBar)
		to.vmafProgressBar = nil
	}
}

func (to *LiveQPSearch) Stop() {
	to.cleanupSegmentUI()
	if to.globalProgressBar != nil {
		liveprogress.RemoveBar(to.globalProgressBar)
		to.globalProgressBar = nil
	}
}

func (to *LiveQPSearch) Debug(format string, a ...any) {
	if to.PrintDebug {
		fmt.Fprintln(liveprogress.Bypass(), "DEBUG: "+fmt.Sprintf(format, a...))
	}
}

func (to *LiveQPSearch) Warning(format string, a ...any) {
	fmt.Fprintln(liveprogress.Bypass(), "WARNING: "+fmt.Sprintf(format, a...))
}

func (to *LiveQPSearch) Error(err error) {
	fmt.Fprintln(liveprogress.Bypass(), "ERROR: "+err.Error())
}

func (to *LiveQPSearch) OnSegmentStart(segmentIndex int, segmentPath string) {
	to.segmentCurrent = segmentIndex
	if to.segmentCandidates != nil {
		to.segmentCandidatesAccess.Lock()
		for i := range to.segmentCandidates {
			to.segmentCandidates[i] = "" // drop references
		}
		to.segmentCandidates = to.segmentCandidates[:0] // reset while keeping cap
		to.segmentCandidatesAccess.Unlock()
	}
	if to.segmentStatusLine != nil {
		liveprogress.RemoveCustomLine(to.segmentStatusLine)
	}
	to.segmentStatusLine = liveprogress.AddCustomLine(func() string {
		to.segmentCandidatesAccess.Lock()
		defer to.segmentCandidatesAccess.Unlock()
		if len(to.segmentCandidates) == 0 {
			// first step is to analyse source files for total number of frames, no candidate yet
			return fmt.Sprintf("    Segment | %d - Searching for best QP...", segmentIndex+1)
		}
		return fmt.Sprintf("    Segment | %d - Searching for best QP: %s", segmentIndex+1, strings.Join(to.segmentCandidates, " "))
	})
}

func (to *LiveQPSearch) OnSegmentNewCandidate(qpCandidate int) {
	to.segmentCandidatesAccess.Lock()
	to.segmentCandidates = append(to.segmentCandidates, strconv.Itoa(qpCandidate))
	to.segmentCandidatesAccess.Unlock()
}

func (to *LiveQPSearch) OnSegmentAnalysisStart(fileSize int64) {
	if to.analysisProgressBar != nil {
		liveprogress.RemoveBar(to.analysisProgressBar)
	}
	to.analysisProgressBar = liveprogress.AddBar(
		liveprogress.WithTotal(uint64(fileSize)),
		liveprogress.WithMultiplyRunes(),
		// liveprogress.WithWidth(barsWidth),
		liveprogress.WithSameAutoSizeInternalPadding(true, false),
		liveprogress.WithPrependDecorator(func(bar *liveprogress.Bar) string {
			return "    Analyze | "
		}),
		liveprogress.WithAppendPercent(liveprogress.BaseStyle()),
		liveprogress.WithAppendDecorator(func(bar *liveprogress.Bar) string {
			return fmt.Sprintf(" | %s/%s",
				cunits.ImportInBytes(float64(bar.Current())), cunits.ImportInBytes(float64(bar.Total())),
			)
		}),
	)
}

func (to *LiveQPSearch) OnSegmentAnalysisProgress(newRead int64) {
	if to.analysisProgressBar != nil {
		to.analysisProgressBar.CurrentAdd(uint64(newRead))
	}
}

func (to *LiveQPSearch) OnSegmentAnalysisStop() {
	if to.analysisProgressBar != nil {
		liveprogress.RemoveBar(to.analysisProgressBar)
		to.analysisProgressBar = nil
	}
}

func (to *LiveQPSearch) OnSegmentEncodeStart(totalFrames int) {
	if to.encodeProgressBar != nil {
		liveprogress.RemoveBar(to.encodeProgressBar)
	}
	to.encodeProgressBar = liveprogress.AddBar(
		liveprogress.WithTotal(uint64(totalFrames)),
		liveprogress.WithMultiplyRunes(),
		// liveprogress.WithWidth(barsWidth),
		liveprogress.WithSameAutoSizeInternalPadding(true, false),
		liveprogress.WithPrependDecorator(func(bar *liveprogress.Bar) string {
			return "     Encode | "
		}),
		// liveprogress.WithPrependTimeElapsed(liveprogress.BaseStyle()),
		liveprogress.WithAppendPercent(liveprogress.BaseStyle()),
		// liveprogress.WithAppendTimeRemaining(liveprogress.BaseStyle()),
		liveprogress.WithAppendDecorator(func(bar *liveprogress.Bar) string {
			return fmt.Sprintf(" | %d/%d frames", bar.Current(), bar.Total())
		}),
	)
}

func (to *LiveQPSearch) OnSegmentEncodeProgress(stats core.ProgressStats) {
	if to.encodeProgressBar != nil {
		to.encodeProgressBar.CurrentSet(uint64(stats.CurrentFrame))
	}
}

func (to *LiveQPSearch) OnSegmentEncodeStop() {
	if to.encodeProgressBar != nil {
		liveprogress.RemoveBar(to.encodeProgressBar)
		to.encodeProgressBar = nil
	}
}

func (to *LiveQPSearch) OnSegmentVMAFStart(totalFrames int) {
	if to.vmafProgressBar != nil {
		liveprogress.RemoveBar(to.vmafProgressBar)
	}
	to.vmafProgressBar = liveprogress.AddBar(
		liveprogress.WithTotal(uint64(totalFrames)),
		liveprogress.WithMultiplyRunes(),
		// liveprogress.WithWidth(barsWidth),
		liveprogress.WithSameAutoSizeInternalPadding(true, false),
		liveprogress.WithPrependDecorator(func(bar *liveprogress.Bar) string {
			return "       VMAF | "
		}),
		liveprogress.WithAppendPercent(liveprogress.BaseStyle()),
		liveprogress.WithAppendDecorator(func(bar *liveprogress.Bar) string {
			return fmt.Sprintf(" | %d/%d frames", bar.Current(), bar.Total())
		}),
	)
}

func (to *LiveQPSearch) OnSegmentVMAFProgress(stats core.ProgressStats) {
	if to.vmafProgressBar != nil {
		to.vmafProgressBar.CurrentSet(uint64(stats.CurrentFrame))
	}
}

func (to *LiveQPSearch) OnSegmentVMAFStop() {
	if to.vmafProgressBar != nil {
		liveprogress.RemoveBar(to.vmafProgressBar)
		to.vmafProgressBar = nil
	}
}

func (to *LiveQPSearch) OnSegmentDone(segmentFinalQP, segmentFrames, segmentNbAttempts int, currentTotalDuration time.Duration, currentTotalSize int64) {
	// Clean up possible orphans child status
	to.cleanupSegmentUI()
	// Finished segment data
	fmt.Fprintf(liveprogress.Bypass(), "\tSegment %d: QP %d selected for this segment of %d frames (%d attempts)\n",
		to.segmentCurrent+1, segmentFinalQP, segmentFrames, segmentNbAttempts,
	)
	// Global progress
	if to.globalProgressBar != nil {
		to.globalProgressBar.CurrentSet(uint64(currentTotalDuration))
	}
	to.globalNbSegmentsDone++
	to.globalAllSegmentSize = cunits.ImportInBytes(float64(currentTotalSize))
}

/*
 * VMAF
 */

func liveVMAF(ctx context.Context, config ffmpeg.VMAFComputeConfig, totalFrames int, debug bool) (
	stats ffmpeg.VMAFReport, err error) {
	bypass := liveprogress.Bypass()
	barOpts := []liveprogress.BarOption{
		liveprogress.WithMultiplyRunes(),
		liveprogress.WithSameAutoSizeInternalPadding(true, false),
		liveprogress.WithPrependDecorator(func(bar *liveprogress.Bar) string {
			return "       VMAF | "
		}),
		liveprogress.WithPrependTimeElapsed(liveprogress.BaseStyle()),
		liveprogress.WithAppendPercent(liveprogress.BaseStyle()),
		liveprogress.WithAppendTimeRemaining(liveprogress.BaseStyle()),
		liveprogress.WithAppendDecorator(func(bar *liveprogress.Bar) string {
			return fmt.Sprintf(" | %d/%d frames", bar.Current(), bar.Total())
		}),
	}
	if totalFrames > 0 {
		barOpts = append(barOpts, liveprogress.WithTotal(uint64(totalFrames)))
	}
	vmafBar := liveprogress.AddBar(barOpts...)
	defer liveprogress.RemoveBar(vmafBar)
	// Inject progress into config (preserve caller callbacks if any)
	origStatsReport := config.FFMPEGStatsReport
	config.FFMPEGStatsReport = func(stats ffmpeg.ProgressStats) {
		vmafBar.CurrentSet(uint64(stats.CurrentFrame))
		if origStatsReport != nil {
			origStatsReport(stats)
		}
	}
	origDebug := config.Debug
	config.Debug = func(s string) {
		if debug {
			fmt.Fprintf(bypass, "DEBUG: %s\n", s)
		}
		if origDebug != nil {
			origDebug(s)
		}
	}
	origRuntimeError := config.RuntimeError
	config.RuntimeError = func(err error) {
		fmt.Fprintf(bypass, "ERROR: %s\n", err)
		if origRuntimeError != nil {
			origRuntimeError(err)
		}
	}
	return ffmpeg.VMAFCompute(ctx, config)
}

func liveConcat(ctx context.Context, workingDir, outputFile string, segments []string, totalFrames int, debug bool) (err error) {
	concatList, err := ffmpeg.GenerateConcatList(workingDir, segments)
	if err != nil {
		err = fmt.Errorf("failed to generate concat list file: %w", err)
		return
	}
	concatBar := liveprogress.AddBar(
		liveprogress.WithTotal(uint64(totalFrames)),
		liveprogress.WithMultiplyRunes(),
		// liveprogress.WithWidth(barsWidth),
		liveprogress.WithSameAutoSizeInternalPadding(true, false),
		liveprogress.WithPrependDecorator(func(bar *liveprogress.Bar) string {
			return "     Concat | "
		}),
		liveprogress.WithPrependTimeElapsed(liveprogress.BaseStyle()),
		liveprogress.WithAppendPercent(liveprogress.BaseStyle()),
		liveprogress.WithAppendTimeRemaining(liveprogress.BaseStyle()),
		liveprogress.WithAppendDecorator(func(bar *liveprogress.Bar) string {
			return fmt.Sprintf(" | %d/%d frames", bar.Current(), bar.Total())
		}),
	)
	defer liveprogress.RemoveBar(concatBar)
	progress := func(stats ffmpeg.ProgressStats) {
		concatBar.CurrentSet(uint64(stats.CurrentFrame))
	}
	return ffmpeg.Concat(ctx, ffmpeg.ConcatConfig{
		ConcatListPath: concatList,
		ConcatUnsafe:   true,
		OutputPath:     outputFile,
		Debug: func(s string) {
			if debug {
				fmt.Fprintf(liveprogress.Bypass(), "DEBUG: %s\n", s)
			}
		},
		RuntimeError: func(err error) {
			fmt.Fprintf(liveprogress.Bypass(), "ERROR: %s\n", err)
		},
		FFMPEGStatsReport: progress,
	})
}

// liveFinalVMAF computes the final VMAF on the fully encoded/concatenated output.
// It is used by the encode and batch-search pipelines as the last quality-check step.
func liveFinalVMAF(ctx context.Context, source, distorted string, videoStream *ffmpeg.FFProbeBinaryStream, totalFrames, gpuIndex int, vmafNeg, vmafCUDA, debug bool) (
	stats ffmpeg.VMAFReport, err error) {
	return liveVMAF(ctx, ffmpeg.VMAFComputeConfig{
		ReferencePath:     source,
		DistortedPath:     distorted,
		InputFrameRate:    videoStream.RFrameRate,
		ReportPath:        distorted + "_vmaf.json",
		UltraHD:           videoStream.Height >= ffmpeg.Height4K,
		NoEnhancementGain: vmafNeg,
		VMAFCuda:          vmafCUDA,
		GPUID:             &gpuIndex,
	}, totalFrames, debug)
}

/*
 * Post-processing
 */

func liveRemuxSwapVideo(ctx context.Context, originalFile, newVideoFile, outputFile string, encodeToFLAC bool, tags ffmpeg.FFMEGTags,
	expectedDuration time.Duration, debug bool) (err error) {
	var currentStats ffmpeg.ProgressStats
	remuxBar := liveprogress.AddBar(
		liveprogress.WithTotal(uint64(expectedDuration)),
		liveprogress.WithMultiplyRunes(),
		liveprogress.WithSameAutoSizeInternalPadding(true, false),
		liveprogress.WithPrependDecorator(func(bar *liveprogress.Bar) string {
			return "   Remuxing | "
		}),
		liveprogress.WithPrependTimeElapsed(liveprogress.BaseStyle()),
		liveprogress.WithAppendPercent(liveprogress.BaseStyle()),
		liveprogress.WithAppendTimeRemaining(liveprogress.BaseStyle()),
		liveprogress.WithAppendDecorator(func(bar *liveprogress.Bar) string {
			return fmt.Sprintf(" left | speed: %0.2fx",
				currentStats.Speed,
			)
		}),
	)
	defer liveprogress.RemoveBar(remuxBar)
	progress := func(stats ffmpeg.ProgressStats) {
		currentStats = stats
		remuxBar.CurrentSet(uint64(stats.Time))
	}
	return ffmpeg.RemuxSwapVideo(ctx, ffmpeg.RemuxSwapVideoConfig{
		OriginalFile:   originalFile,
		NewVideoFile:   newVideoFile,
		OutputFilePath: outputFile,
		EncodeToFLAC:   encodeToFLAC,
		Tags:           tags,
		Debug: func(s string) {
			if debug {
				fmt.Fprintf(liveprogress.Bypass(), "DEBUG: %s\n", s)
			}
		},
		RuntimeError: func(err error) {
			fmt.Fprintf(liveprogress.Bypass(), "ERROR: %s\n", err)
		},
		FFMPEGStatsReport: progress,
	})
}

func liveGenerateMKVStats(ctx context.Context, outputPath string, debug bool) (err error) {
	mkvStatsBar := liveprogress.AddBar(
		liveprogress.WithTotal(100),
		liveprogress.WithMultiplyRunes(),
		liveprogress.WithSameAutoSizeInternalPadding(true, false),
		liveprogress.WithPrependDecorator(func(bar *liveprogress.Bar) string {
			return "  MKV Stats | "
		}),
		liveprogress.WithPrependTimeElapsed(liveprogress.BaseStyle()),
		liveprogress.WithAppendPercent(liveprogress.BaseStyle()),
		liveprogress.WithAppendTimeRemaining(liveprogress.BaseStyle()),
	)
	defer liveprogress.RemoveBar(mkvStatsBar)
	return mkvtoolnix.GenerateMKVStats(ctx, mkvtoolnix.GenerateMKVStatsConfig{
		Path: outputPath,
		Debug: func(s string) {
			if debug {
				fmt.Fprintf(liveprogress.Bypass(), "DEBUG: %s\n", s)
			}
		},
		RuntimeError: func(err error) {
			fmt.Fprintf(liveprogress.Bypass(), "ERROR: %s\n", err)
		},
		ProgressReport: func(percent int) {
			mkvStatsBar.CurrentSet(uint64(percent))
		},
	})
}
