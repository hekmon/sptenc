package main

import (
	"context"
	"errors"
	"fmt"
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

func liveCountNbFrames(ctx context.Context, inputFilePath string, debug bool, dec ffmpeg.HWDecoderConfig) (
	nbFrames int, codec string, duration time.Duration, err error) {
	videoInfos, duration, err := liveProbeVideoCF(ctx, inputFilePath, debug, dec)
	if err != nil {
		return
	}
	nbFrames = videoInfos.NbReadFrames
	codec = string(videoInfos.CodecName)
	return
}

// liveProbeVideoCF returns the video stream of the file along with what can only be known by
// decoding it entirely (CF: count frames): its exact number of frames and how long they last.
// Decoders incompatible with the codec of the file are ignored (software decode).
func liveProbeVideoCF(ctx context.Context, inputFilePath string, debug bool, dec ffmpeg.HWDecoderConfig) (
	videoInfos *ffmpeg.FFProbeBinaryStream, duration time.Duration, err error) {
	mediaInfos, err := getStreamsInfos(ctx, inputFilePath, debug)
	if err != nil {
		return
	}
	duration = mediaInfos.Format.Duration
	if videoInfos = mediaInfos.VideoTrack(); videoInfos == nil {
		err = errors.New("input file has no video stream")
		return
	}
	frames, err := liveCountFrames(ctx, inputFilePath, duration, debug, dec)
	if err != nil {
		return
	}
	videoInfos.SetReadFrames(frames)
	return
}

func liveCountFrames(ctx context.Context, path string, duration time.Duration, debug bool, dec ffmpeg.HWDecoderConfig) (
	frames ffmpeg.ReadFrames, err error) {
	// prepare live progress
	var currentStats ffmpeg.ProgressStats
	bar := liveprogress.AddBar(
		liveprogress.WithTotal(uint64(duration)),
		liveprogress.WithMultiplyRunes(),
		// liveprogress.WithWidth(barsWidth),
		liveprogress.WithSameAutoSizeInternalPadding(true, false),
		liveprogress.WithPrependDecorator(func(bar *liveprogress.Bar) string {
			return "   Counting | "
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
	// exec ffmpeg
	return ffmpeg.CountFrames(ctx, ffmpeg.CountFramesConfig{
		// Input
		Path:            path,
		HWDecoderConfig: dec,
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
	// extract the frames to cut at (frames, not times: see ffmpeg.Scene)
	scenesFrames := make([]int, len(scenes))
	for i, scene := range scenes {
		scenesFrames[i] = scene.Frame
	}
	// Execute segmentation
	return ffmpeg.Segment(ctx, ffmpeg.SegmentConfig{
		// Input
		Input: path,
		// Output
		ScenesFrames: scenesFrames,
		OutputDir:    outputDir,
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

// LiveQPSearch received and process search progress signals to translate them as terminal UI progress
// it implements the core.QPSearchCallbacks interface required by core.FindAllSegmentsQP()
type LiveQPSearch struct {
	PrintDebug  bool
	Concurrency int
	// Global progress
	globalProgressBar    *liveprogress.Bar
	globalNbSegmentsDone int
	globalAllSegmentSize cunits.Bits
	globalAccess         sync.Mutex
	/*
	 * Per worker
	 */
	// Segment progress (title + qp candidates listing)
	segmentsCurrent          []int
	segmentsStatusLine       []*liveprogress.CustomLine
	segmentsCandidates       [][]string
	segmentsCandidatesAccess []sync.Mutex
	// File analysis
	analysisProgressBars []*liveprogress.Bar
	// Encode
	encodeProgressBars []*liveprogress.Bar
	// VMAF
	vmafProgressBars []*liveprogress.Bar
}

func (to *LiveQPSearch) Start(totalSegments int, globalDuration time.Duration) {
	to.segmentsCurrent = make([]int, to.Concurrency)
	to.segmentsStatusLine = make([]*liveprogress.CustomLine, to.Concurrency)
	to.segmentsCandidates = make([][]string, to.Concurrency)
	to.segmentsCandidatesAccess = make([]sync.Mutex, to.Concurrency)
	to.analysisProgressBars = make([]*liveprogress.Bar, to.Concurrency)
	to.encodeProgressBars = make([]*liveprogress.Bar, to.Concurrency)
	to.vmafProgressBars = make([]*liveprogress.Bar, to.Concurrency)
	to.globalProgressBar = liveprogress.SetMainLineAsBar(
		liveprogress.WithTotal(uint64(globalDuration)),
		liveprogress.WithMultiplyRunes(),
		// liveprogress.WithWidth(barsWidth),
		liveprogress.WithSameAutoSizeInternalPadding(true, false),
		liveprogress.WithPrependDecorator(func(bar *liveprogress.Bar) string {
			if to.Concurrency > 1 {
				return "        Progress | "
			}
			return "   Progress | "
		}),
		liveprogress.WithPrependTimeElapsed(liveprogress.BaseStyle()),
		liveprogress.WithAppendPercent(liveprogress.BaseStyle()),
		liveprogress.WithAppendTimeRemaining(liveprogress.BaseStyle()),
		liveprogress.WithAppendDecorator(func(bar *liveprogress.Bar) string {
			to.globalAccess.Lock()
			done, size := to.globalNbSegmentsDone, to.globalAllSegmentSize
			to.globalAccess.Unlock()
			return fmt.Sprintf(" left | %d/%d segments | %s",
				done, totalSegments, size,
			)
		}),
	)
}

func (to *LiveQPSearch) Stop() {
	for index, line := range to.segmentsStatusLine {
		if line != nil {
			liveprogress.RemoveCustomLine(line)
			to.segmentsStatusLine[index] = nil
		}
	}
	for index, bar := range to.analysisProgressBars {
		if bar != nil {
			liveprogress.RemoveBar(bar)
			to.analysisProgressBars[index] = nil
		}
	}
	for index, bar := range to.encodeProgressBars {
		if bar != nil {
			liveprogress.RemoveBar(bar)
			to.encodeProgressBars[index] = nil
		}
	}
	for index, bar := range to.vmafProgressBars {
		if bar != nil {
			liveprogress.RemoveBar(bar)
			to.vmafProgressBars[index] = nil
		}
	}
	if to.globalProgressBar != nil {
		liveprogress.RemoveBar(to.globalProgressBar)
		to.globalProgressBar = nil
	}
}

// Debug, Warning and Error are safe for concurrent use: liveprogress.Bypass()
// serializes writes internally with a mutex, so interleaved output is not possible.

func (to *LiveQPSearch) Debug(workerID int, format string, a ...any) {
	if !to.PrintDebug {
		return
	}
	if to.Concurrency > 1 {
		fmt.Fprintln(liveprogress.Bypass(), fmt.Sprintf("[worker %d] DEBUG: ", workerID)+fmt.Sprintf(format, a...))
	} else {
		fmt.Fprintln(liveprogress.Bypass(), "DEBUG: "+fmt.Sprintf(format, a...))
	}
}

func (to *LiveQPSearch) Warning(workerID int, format string, a ...any) {
	if to.Concurrency > 1 {
		fmt.Fprintln(liveprogress.Bypass(), fmt.Sprintf("[worker %d] WARNING: ", workerID)+fmt.Sprintf(format, a...))
	} else {
		fmt.Fprintln(liveprogress.Bypass(), "WARNING: "+fmt.Sprintf(format, a...))
	}
}

func (to *LiveQPSearch) Error(workerID int, err error) {
	if to.Concurrency > 1 {
		fmt.Fprintln(liveprogress.Bypass(), fmt.Sprintf("[worker %d] ERROR: ", workerID)+err.Error())
	} else {
		fmt.Fprintln(liveprogress.Bypass(), "ERROR: "+err.Error())
	}
}

func (to *LiveQPSearch) OnSegmentStart(workerID, segmentIndex int, segmentPath string) {
	to.segmentsCurrent[workerID] = segmentIndex
	if to.segmentsCandidates[workerID] != nil {
		to.segmentsCandidatesAccess[workerID].Lock()
		for i := range to.segmentsCandidates[workerID] {
			to.segmentsCandidates[workerID][i] = "" // drop references
		}
		to.segmentsCandidates[workerID] = to.segmentsCandidates[workerID][:0] // reset while keeping cap
		to.segmentsCandidatesAccess[workerID].Unlock()
	}
	if to.segmentsStatusLine[workerID] != nil {
		liveprogress.RemoveCustomLine(to.segmentsStatusLine[workerID])
		// no need to nullify we are about to reset it
	}
	to.segmentsStatusLine[workerID] = liveprogress.AddCustomLine(func() string {
		to.segmentsCandidatesAccess[workerID].Lock()
		defer to.segmentsCandidatesAccess[workerID].Unlock()
		if len(to.segmentsCandidates[workerID]) == 0 {
			// first step is to analyse source files for total number of frames, no candidate yet
			if to.Concurrency > 1 {
				return fmt.Sprintf(" [#%d]    Segment | %d - Searching for best QP...", workerID, segmentIndex+1)
			}
			return fmt.Sprintf("    Segment | %d - Searching for best QP...", segmentIndex+1)
		}
		if to.Concurrency > 1 {
			return fmt.Sprintf(" [#%d]    Segment | %d - Searching for best QP: %s", workerID, segmentIndex+1,
				strings.Join(to.segmentsCandidates[workerID], " "))
		}
		return fmt.Sprintf("    Segment | %d - Searching for best QP: %s", segmentIndex+1,
			strings.Join(to.segmentsCandidates[workerID], " "))
	})
}

func (to *LiveQPSearch) OnSegmentNewCandidate(workerID, qpCandidate int) {
	to.segmentsCandidatesAccess[workerID].Lock()
	to.segmentsCandidates[workerID] = append(to.segmentsCandidates[workerID], strconv.Itoa(qpCandidate))
	to.segmentsCandidatesAccess[workerID].Unlock()
}

func (to *LiveQPSearch) OnSegmentAnalysisStart(workerID int, duration time.Duration) {
	if to.analysisProgressBars[workerID] != nil {
		liveprogress.RemoveBar(to.analysisProgressBars[workerID])
		// no need to nullify we are about to reset it
	}
	to.analysisProgressBars[workerID] = liveprogress.AddBar(
		liveprogress.WithTotal(uint64(duration)),
		liveprogress.WithMultiplyRunes(),
		// liveprogress.WithWidth(barsWidth),
		liveprogress.WithSameAutoSizeInternalPadding(true, false),
		liveprogress.WithPrependDecorator(func(bar *liveprogress.Bar) string {
			if to.Concurrency > 1 {
				return fmt.Sprintf(" [#%d]   Counting | ", workerID)
			}
			return "   Counting | "
		}),
		liveprogress.WithAppendPercent(liveprogress.BaseStyle()),
		liveprogress.WithAppendDecorator(func(bar *liveprogress.Bar) string {
			return fmt.Sprintf(" | %d frames", bar.Current())
		}),
	)
}

func (to *LiveQPSearch) OnSegmentAnalysisProgress(workerID int, stats core.ProgressStats) {
	if to.analysisProgressBars[workerID] != nil {
		to.analysisProgressBars[workerID].CurrentSet(uint64(stats.Time))
	}
}

func (to *LiveQPSearch) OnSegmentAnalysisStop(workerID int) {
	if to.analysisProgressBars[workerID] != nil {
		liveprogress.RemoveBar(to.analysisProgressBars[workerID])
		to.analysisProgressBars[workerID] = nil
	}
}

func (to *LiveQPSearch) OnSegmentEncodeStart(workerID, totalFrames int) {
	if to.encodeProgressBars[workerID] != nil {
		liveprogress.RemoveBar(to.encodeProgressBars[workerID])
	}
	to.encodeProgressBars[workerID] = liveprogress.AddBar(
		liveprogress.WithTotal(uint64(totalFrames)),
		liveprogress.WithMultiplyRunes(),
		// liveprogress.WithWidth(barsWidth),
		liveprogress.WithSameAutoSizeInternalPadding(true, false),
		liveprogress.WithPrependDecorator(func(bar *liveprogress.Bar) string {
			if to.Concurrency > 1 {
				return fmt.Sprintf(" [#%d]     Encode | ", workerID)
			}
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

func (to *LiveQPSearch) OnSegmentEncodeProgress(workerID int, stats core.ProgressStats) {
	if to.encodeProgressBars[workerID] != nil {
		to.encodeProgressBars[workerID].CurrentSet(uint64(stats.CurrentFrame))
	}
}

func (to *LiveQPSearch) OnSegmentEncodeStop(workerID int) {
	if to.encodeProgressBars[workerID] != nil {
		liveprogress.RemoveBar(to.encodeProgressBars[workerID])
		to.encodeProgressBars[workerID] = nil
	}
}

func (to *LiveQPSearch) OnSegmentVMAFStart(workerID, totalFrames int) {
	if to.vmafProgressBars[workerID] != nil {
		liveprogress.RemoveBar(to.vmafProgressBars[workerID])
	}
	to.vmafProgressBars[workerID] = liveprogress.AddBar(
		liveprogress.WithTotal(uint64(totalFrames)),
		liveprogress.WithMultiplyRunes(),
		// liveprogress.WithWidth(barsWidth),
		liveprogress.WithSameAutoSizeInternalPadding(true, false),
		liveprogress.WithPrependDecorator(func(bar *liveprogress.Bar) string {
			if to.Concurrency > 1 {
				return fmt.Sprintf(" [#%d]       VMAF | ", workerID)
			}
			return "       VMAF | "
		}),
		liveprogress.WithAppendPercent(liveprogress.BaseStyle()),
		liveprogress.WithAppendDecorator(func(bar *liveprogress.Bar) string {
			return fmt.Sprintf(" | %d/%d frames", bar.Current(), bar.Total())
		}),
	)
}

func (to *LiveQPSearch) OnSegmentVMAFProgress(workerID int, stats core.ProgressStats) {
	if to.vmafProgressBars[workerID] != nil {
		to.vmafProgressBars[workerID].CurrentSet(uint64(stats.CurrentFrame))
	}
}

func (to *LiveQPSearch) OnSegmentVMAFStop(workerID int) {
	if to.vmafProgressBars[workerID] != nil {
		liveprogress.RemoveBar(to.vmafProgressBars[workerID])
		to.vmafProgressBars[workerID] = nil
	}
}

func (to *LiveQPSearch) OnSegmentDone(workerID, segmentFinalQP, segmentFrames, segmentNbAttempts int,
	currentTotalDuration time.Duration, currentTotalSize int64) {
	// Clean up this worker's child UI elements
	if to.segmentsStatusLine[workerID] != nil {
		liveprogress.RemoveCustomLine(to.segmentsStatusLine[workerID])
		to.segmentsStatusLine[workerID] = nil
	}
	if to.analysisProgressBars[workerID] != nil {
		liveprogress.RemoveBar(to.analysisProgressBars[workerID])
		to.analysisProgressBars[workerID] = nil
	}
	if to.encodeProgressBars[workerID] != nil {
		liveprogress.RemoveBar(to.encodeProgressBars[workerID])
		to.encodeProgressBars[workerID] = nil
	}
	if to.vmafProgressBars[workerID] != nil {
		liveprogress.RemoveBar(to.vmafProgressBars[workerID])
		to.vmafProgressBars[workerID] = nil
	}
	// Finished segment data
	var workerTail string
	if to.Concurrency > 1 {
		workerTail = fmt.Sprintf(" [worker #%d]", workerID)
	}
	fmt.Fprintf(liveprogress.Bypass(), "\tSegment %d: QP %d selected for this segment of %d frames (%d attempts)%s\n",
		to.segmentsCurrent[workerID]+1, segmentFinalQP, segmentFrames, segmentNbAttempts, workerTail,
	)
	// Global progress
	newSize := cunits.ImportInBytes(float64(currentTotalSize))
	to.globalAccess.Lock()
	if to.globalProgressBar != nil && uint64(currentTotalDuration) > to.globalProgressBar.Current() {
		to.globalProgressBar.CurrentSet(uint64(currentTotalDuration))
	}
	to.globalNbSegmentsDone++
	if newSize > to.globalAllSegmentSize {
		to.globalAllSegmentSize = newSize
	}
	to.globalAccess.Unlock()
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
// Both files are decoded by dec when their codec allows it (software decode otherwise).
func liveFinalVMAF(ctx context.Context, source, distorted string, videoStream *ffmpeg.FFProbeBinaryStream, totalFrames, gpuIndex int,
	vmafNeg, vmafCUDA, debug bool, dec ffmpeg.HWDecoderConfig) (stats ffmpeg.VMAFReport, err error) {
	return liveVMAF(ctx, ffmpeg.VMAFComputeConfig{
		ReferencePath:     source,
		DistortedPath:     distorted,
		InputFrameRate:    videoStream.RFrameRate,
		ReportPath:        distorted + "_vmaf.json",
		UltraHD:           videoStream.Height >= ffmpeg.Height4K,
		NoEnhancementGain: vmafNeg,
		VMAFCuda:          vmafCUDA,
		GPUID:             &gpuIndex,
		HWDecoderConfig:   dec,
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
