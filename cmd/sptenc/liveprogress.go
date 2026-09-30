package main

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hekmon/sptenc/core"
	"github.com/hekmon/sptenc/ffmpeg"
	"github.com/hekmon/sptenc/mkvtoolnix"

	"github.com/hekmon/cunits/v3"
	"github.com/hekmon/liveprogress/v2"
	"github.com/muesli/termenv"
)

/*
 * Progress bar runes
 *
 * Each level of the hierarchy uses a rune matching what it counts:
 *   - segments (default WithMultiplyRunes): '×' discrete pieces of a file
 *   - file:  '━' one continuous line, the pieces merged together
 *   - batch: '═' a stack of complete files
 */

// fileProgressRunes is used for any step working on a whole file: the global progress of a search,
// and every step before the split or after the merge (the merge included).
var fileProgressRunes = liveprogress.BarRunes{
	LeftEnd:  '❮', // https://www.compart.com/unicode/U+276E
	Fill:     '━', // https://www.compart.com/unicode/U+2501
	Head:     '━', // https://www.compart.com/unicode/U+2501
	Empty:    ' ', // https://www.compart.com/unicode/U+0020
	RightEnd: '❯', // https://www.compart.com/unicode/U+276F
}

// batchProgressRunes is used for the progress across batch-search candidates (one full file per candidate).
var batchProgressRunes = liveprogress.BarRunes{
	LeftEnd:  '❮', // https://www.compart.com/unicode/U+276E
	Fill:     '═', // https://www.compart.com/unicode/U+2550
	Head:     '═', // https://www.compart.com/unicode/U+2550
	Empty:    ' ', // https://www.compart.com/unicode/U+0020
	RightEnd: '❯', // https://www.compart.com/unicode/U+276F
}

/*
 * Master
 */

// liveCountNbFrames is liveProbeVideoCF for callers only interested in the exact number of frames.
func liveCountNbFrames(ctx context.Context, inputFilePath string, debug bool, dec ffmpeg.HWDecoderConfig) (nbFrames int, err error) {
	videoInfos, _, err := liveProbeVideoCF(ctx, inputFilePath, debug, dec)
	if err != nil {
		return
	}
	return videoInfos.NbReadFrames, nil
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
	frames, err := liveCountFrames(ctx, inputFilePath, videoInfos.CodecName, duration, debug, dec)
	if err != nil {
		return
	}
	videoInfos.SetReadFrames(frames)
	return
}

func liveCountFrames(ctx context.Context, path string, codec ffmpeg.CodecName, duration time.Duration, debug bool, dec ffmpeg.HWDecoderConfig) (
	frames ffmpeg.ReadFrames, err error) {
	// prepare live progress
	var currentStats ffmpeg.ProgressStats
	bar := liveprogress.AddBar(
		liveprogress.WithTotal(uint64(duration)),
		liveprogress.WithRunes(fileProgressRunes),
		// liveprogress.WithWidth(barsWidth),
		liveprogress.WithSameAutoSizeInternalPadding(true, false),
		liveprogress.WithPrependDecorator(func(bar *liveprogress.Bar) string {
			return "   Counting | "
		}),
		liveprogress.WithPrependTimeElapsed(liveprogress.BaseStyle()),
		liveprogress.WithAppendPercent(liveprogress.BaseStyle()),
		liveprogress.WithAppendTimeRemaining(liveprogress.BaseStyle()),
		liveprogress.WithAppendDecorator(func(bar *liveprogress.Bar) string {
			return fmt.Sprintf(" left | %d frames | speed: %sx", currentStats.CurrentFrame, strconv.FormatFloat(currentStats.Speed, 'f', -1, 64))
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
		Codec:           codec,
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
		liveprogress.WithRunes(fileProgressRunes),
		// liveprogress.WithWidth(barsWidth),
		liveprogress.WithSameAutoSizeInternalPadding(true, false),
		liveprogress.WithPrependDecorator(func(bar *liveprogress.Bar) string {
			return "     Encode | "
		}),
		liveprogress.WithPrependTimeElapsed(liveprogress.BaseStyle()),
		liveprogress.WithAppendPercent(liveprogress.BaseStyle()),
		liveprogress.WithAppendTimeRemaining(liveprogress.BaseStyle()),
		liveprogress.WithAppendDecorator(func(bar *liveprogress.Bar) string {
			return fmt.Sprintf(" left | %d/%d frames", bar.Current(), bar.Total())
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
		SegmentsDir:     masterConfig.SegmentsDir,
		ScenesFrames:    masterConfig.ScenesFrames,
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
		liveprogress.WithRunes(fileProgressRunes),
		// liveprogress.WithWidth(barsWidth),
		liveprogress.WithSameAutoSizeInternalPadding(true, false),
		liveprogress.WithPrependDecorator(func(bar *liveprogress.Bar) string {
			return "  Detecting | "
		}),
		liveprogress.WithPrependTimeElapsed(liveprogress.BaseStyle()),
		liveprogress.WithAppendPercent(liveprogress.BaseStyle()),
		liveprogress.WithAppendTimeRemaining(liveprogress.BaseStyle()),
		liveprogress.WithAppendDecorator(func(bar *liveprogress.Bar) string {
			return fmt.Sprintf(" left | speed: %sx", strconv.FormatFloat(currentStats.Speed, 'f', -1, 64))
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

// scenesFrames returns the frames to cut at (frames, not times: see ffmpeg.Scene).
func scenesFrames(scenes []ffmpeg.Scene) (frames []int) {
	frames = make([]int, len(scenes))
	for i, scene := range scenes {
		frames[i] = scene.Frame
	}
	return
}

func liveSplitScenes(ctx context.Context, path, outputDir string, totalDuration time.Duration, scenes []ffmpeg.Scene, debug bool) (err error) {
	// live progress for splitting
	var currentStats ffmpeg.ProgressStats
	bar := liveprogress.AddBar(
		liveprogress.WithTotal(uint64(totalDuration)),
		liveprogress.WithRunes(fileProgressRunes),
		// liveprogress.WithWidth(barsWidth),
		liveprogress.WithSameAutoSizeInternalPadding(true, false),
		liveprogress.WithPrependDecorator(func(bar *liveprogress.Bar) string {
			return "  Splitting | "
		}),
		liveprogress.WithPrependTimeElapsed(liveprogress.BaseStyle()),
		liveprogress.WithAppendPercent(liveprogress.BaseStyle()),
		liveprogress.WithAppendTimeRemaining(liveprogress.BaseStyle()),
		liveprogress.WithAppendDecorator(func(bar *liveprogress.Bar) string {
			return fmt.Sprintf(" left | speed: %sx", strconv.FormatFloat(currentStats.Speed, 'f', -1, 64))
		}),
	)
	defer liveprogress.RemoveBar(bar)
	progress := func(stats ffmpeg.ProgressStats) {
		currentStats = stats
		bar.CurrentSet(uint64(stats.Time))
	}
	// Execute segmentation
	return ffmpeg.Segment(ctx, ffmpeg.SegmentConfig{
		// Input
		Input: path,
		// Output
		ScenesFrames: scenesFrames(scenes),
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

func liveConcatDuration(ctx context.Context, workingDir, outputFile string, segments []string, segmentsDurations []time.Duration, frameRate string, totalDuration time.Duration, debug bool) (err error) {
	concatList, err := ffmpeg.GenerateConcatList(workingDir, segments, segmentsDurations)
	if err != nil {
		err = fmt.Errorf("failed to generate concat list file: %w", err)
		return
	}
	var speed float64
	concatBar := liveprogress.AddBar(
		liveprogress.WithTotal(uint64(totalDuration)),
		liveprogress.WithRunes(fileProgressRunes),
		liveprogress.WithSameAutoSizeInternalPadding(true, false),
		liveprogress.WithPrependDecorator(func(bar *liveprogress.Bar) string {
			return "     Concat | "
		}),
		liveprogress.WithPrependTimeElapsed(liveprogress.BaseStyle()),
		liveprogress.WithAppendPercent(liveprogress.BaseStyle()),
		liveprogress.WithAppendTimeRemaining(liveprogress.BaseStyle()),
		liveprogress.WithAppendDecorator(func(bar *liveprogress.Bar) string {
			return fmt.Sprintf(" left | speed: %sx", strconv.FormatFloat(speed, 'f', -1, 64))
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
		FrameRate:      frameRate,
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
	return probeStreams(ctx, path, false, debug)
}

// countStreamsPackets is getStreamsInfos with the packets of every stream counted (the whole
// file is read, nothing is decoded).
func countStreamsPackets(ctx context.Context, path string, debug bool) (stats ffmpeg.FFProbeStats, err error) {
	return probeStreams(ctx, path, true, debug)
}

func probeStreams(ctx context.Context, path string, countPackets, debug bool) (stats ffmpeg.FFProbeStats, err error) {
	return ffmpeg.GetStreamsInfos(ctx, ffmpeg.GetStreamsInfosConfig{
		// Input
		Path:         path,
		CountPackets: countPackets,
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
	// Segment progress (title + qp candidates listing, see segmentCandidates)
	segmentsCurrent          []int
	segmentsStatusLine       []*liveprogress.CustomLine
	segmentsCandidates       []segmentCandidates
	segmentsCandidatesAccess []sync.Mutex
	failedCandidateStyle     termenv.Style
	vmafQPCandidateStyle     termenv.Style
	// File analysis (the bar is in time, the frames counted so far are shown next to it)
	analysisProgressBars []*liveprogress.Bar
	analysisFrames       []atomic.Int64
	// Encode
	encodeProgressBars []*liveprogress.Bar
	// VMAF
	vmafProgressBars []*liveprogress.Bar
}

func (to *LiveQPSearch) Start(totalSegments int, globalDuration time.Duration) {
	to.segmentsCurrent = make([]int, to.Concurrency)
	to.segmentsStatusLine = make([]*liveprogress.CustomLine, to.Concurrency)
	to.segmentsCandidates = make([]segmentCandidates, to.Concurrency)
	to.segmentsCandidatesAccess = make([]sync.Mutex, to.Concurrency)
	termenvProfile := liveprogress.GetTermProfile()
	to.failedCandidateStyle = termenvProfile.String().CrossOut()
	to.vmafQPCandidateStyle = termenvProfile.String().Bold()
	to.analysisProgressBars = make([]*liveprogress.Bar, to.Concurrency)
	to.analysisFrames = make([]atomic.Int64, to.Concurrency)
	to.encodeProgressBars = make([]*liveprogress.Bar, to.Concurrency)
	to.vmafProgressBars = make([]*liveprogress.Bar, to.Concurrency)
	to.globalProgressBar = liveprogress.SetMainLineAsBar(
		liveprogress.WithTotal(uint64(globalDuration)),
		liveprogress.WithRunes(fileProgressRunes),
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
	to.segmentsCandidatesAccess[workerID].Lock()
	to.segmentsCandidates[workerID].reset()
	to.segmentsCandidatesAccess[workerID].Unlock()
	if to.segmentsStatusLine[workerID] != nil {
		liveprogress.RemoveCustomLine(to.segmentsStatusLine[workerID])
		// no need to nullify we are about to reset it
	}
	to.segmentsStatusLine[workerID] = liveprogress.AddCustomLine(func() string {
		to.segmentsCandidatesAccess[workerID].Lock()
		candidates := to.segmentsCandidates[workerID].render(to.failedCandidateStyle.Styled, to.vmafQPCandidateStyle.Styled)
		to.segmentsCandidatesAccess[workerID].Unlock()
		if candidates == "" {
			// first step is to analyse source files for total number of frames, no candidate yet
			if to.Concurrency > 1 {
				return fmt.Sprintf(" [#%d]    Segment | %d - Searching for optimal QP...", workerID, segmentIndex+1)
			}
			return fmt.Sprintf("    Segment | %d - Searching for optimal QP...", segmentIndex+1)
		}
		if to.Concurrency > 1 {
			return fmt.Sprintf(" [#%d]    Segment | %d - Searching for optimal QP: %s", workerID, segmentIndex+1, candidates)
		}
		return fmt.Sprintf("    Segment | %d - Searching for optimal QP: %s", segmentIndex+1, candidates)
	})
}

func (to *LiveQPSearch) OnSegmentNewCandidate(workerID, qpCandidate int) {
	to.segmentsCandidatesAccess[workerID].Lock()
	to.segmentsCandidates[workerID].search = append(to.segmentsCandidates[workerID].search, liveCandidate{qp: qpCandidate})
	to.segmentsCandidatesAccess[workerID].Unlock()
}

func (to *LiveQPSearch) OnSegmentCandidateDone(workerID, qpCandidate int, passed bool) {
	to.segmentsCandidatesAccess[workerID].Lock()
	markCandidate(to.segmentsCandidates[workerID].search, qpCandidate, passed)
	to.segmentsCandidatesAccess[workerID].Unlock()
}

func (to *LiveQPSearch) OnSegmentCAMBIStart(workerID, vmafQP int) {
	to.segmentsCandidatesAccess[workerID].Lock()
	to.segmentsCandidates[workerID].vmafQP = vmafQP
	to.segmentsCandidatesAccess[workerID].Unlock()
}

func (to *LiveQPSearch) OnSegmentCAMBICandidate(workerID, qpCandidate int) {
	to.segmentsCandidatesAccess[workerID].Lock()
	to.segmentsCandidates[workerID].walk = append(to.segmentsCandidates[workerID].walk, liveCandidate{qp: qpCandidate})
	to.segmentsCandidatesAccess[workerID].Unlock()
}

func (to *LiveQPSearch) OnSegmentCAMBICandidateDone(workerID, qpCandidate int, passed bool) {
	to.segmentsCandidatesAccess[workerID].Lock()
	markCandidate(to.segmentsCandidates[workerID].walk, qpCandidate, passed)
	to.segmentsCandidatesAccess[workerID].Unlock()
}

// segmentDoneLine returns the line logged for a finished segment: the QP kept, and when the CAMBI
// stage lowered it, the QP of the VMAF search and the QPs its walk tried.
func segmentDoneLine(segmentIndex int, segment core.SegmentResult, workerID, concurrency int) string {
	line := fmt.Sprintf("\tSegment %d: QP %d selected for this segment of %d frames (%d attempts)",
		segmentIndex+1, segment.QP, segment.Frames, segment.Attempts)
	if segment.QP < segment.VMAFQP {
		walk := make([]string, len(segment.CAMBIWalk))
		for i, qp := range segment.CAMBIWalk {
			walk[i] = strconv.Itoa(qp)
		}
		line += fmt.Sprintf(", lowered from QP %d by the CAMBI gate (walk: %s)", segment.VMAFQP, strings.Join(walk, " "))
	}
	if concurrency > 1 {
		line += fmt.Sprintf(" [worker #%d]", workerID)
	}
	return line
}

// segmentCandidates is what the live line of a worker shows of the search of its segment: the QPs
// the VMAF search encoded, the one it found once the CAMBI stage starts, and the QPs of the walk.
type segmentCandidates struct {
	search []liveCandidate
	vmafQP int // the QP the VMAF search found, -1 until the CAMBI stage starts
	walk   []liveCandidate
}

// liveCandidate is a QP of the live line, measured or being measured.
type liveCandidate struct {
	qp     int
	done   bool
	passed bool
}

// reset empties the candidates for a new segment, keeping their capacity.
func (sc *segmentCandidates) reset() {
	sc.search, sc.vmafQP, sc.walk = sc.search[:0], -1, sc.walk[:0]
}

// markCandidate records the result of the last candidate of qp.
func markCandidate(candidates []liveCandidate, qp int, passed bool) {
	for i := len(candidates) - 1; i >= 0; i-- {
		if candidates[i].qp == qp {
			candidates[i].done, candidates[i].passed = true, passed
			return
		}
	}
}

// render returns the candidates as the live line shows them: the QPs that failed struck, the ones
// that passed or are being measured plain, the QP the VMAF search found in bold, and the walk of
// the CAMBI stage after a "· CAMBI" label, alone while the banding of that QP is being measured.
func (sc segmentCandidates) render(failed, vmafQP func(string) string) string {
	parts := make([]string, 0, len(sc.search)+1+len(sc.walk))
	for _, candidate := range sc.search {
		text := candidate.text(failed)
		if candidate.qp == sc.vmafQP {
			text = vmafQP(text)
		}
		parts = append(parts, text)
	}
	if sc.vmafQP >= 0 {
		parts = append(parts, "· CAMBI")
		for _, candidate := range sc.walk {
			parts = append(parts, candidate.text(failed))
		}
	}
	return strings.Join(parts, " ")
}

// text returns the QP of the candidate, struck when it failed.
func (lc liveCandidate) text(failed func(string) string) string {
	if lc.done && !lc.passed {
		return failed(strconv.Itoa(lc.qp))
	}
	return strconv.Itoa(lc.qp)
}

func (to *LiveQPSearch) OnSegmentAnalysisStart(workerID int, duration time.Duration) {
	if to.analysisProgressBars[workerID] != nil {
		liveprogress.RemoveBar(to.analysisProgressBars[workerID])
		// no need to nullify we are about to reset it
	}
	to.analysisFrames[workerID].Store(0)
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
			return fmt.Sprintf(" | %d frames", to.analysisFrames[workerID].Load())
		}),
	)
}

func (to *LiveQPSearch) OnSegmentAnalysisProgress(workerID int, stats core.ProgressStats) {
	if to.analysisProgressBars[workerID] != nil {
		to.analysisFrames[workerID].Store(int64(stats.CurrentFrame))
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

func (to *LiveQPSearch) OnSegmentDone(workerID int, segment core.SegmentResult,
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
	fmt.Fprintln(liveprogress.Bypass(), segmentDoneLine(to.segmentsCurrent[workerID], segment, workerID, to.Concurrency))
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
		liveprogress.WithRunes(fileProgressRunes),
		liveprogress.WithSameAutoSizeInternalPadding(true, false),
		liveprogress.WithPrependDecorator(func(bar *liveprogress.Bar) string {
			return "       VMAF | "
		}),
		liveprogress.WithPrependTimeElapsed(liveprogress.BaseStyle()),
		liveprogress.WithAppendPercent(liveprogress.BaseStyle()),
		liveprogress.WithAppendTimeRemaining(liveprogress.BaseStyle()),
		liveprogress.WithAppendDecorator(func(bar *liveprogress.Bar) string {
			return fmt.Sprintf(" left | %d/%d frames", bar.Current(), bar.Total())
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

func liveConcat(ctx context.Context, workingDir, outputFile string, segments []string, segmentsDurations []time.Duration, frameRate string, totalFrames int, debug bool) (err error) {
	concatList, err := ffmpeg.GenerateConcatList(workingDir, segments, segmentsDurations)
	if err != nil {
		err = fmt.Errorf("failed to generate concat list file: %w", err)
		return
	}
	concatBar := liveprogress.AddBar(
		liveprogress.WithTotal(uint64(totalFrames)),
		liveprogress.WithRunes(fileProgressRunes),
		// liveprogress.WithWidth(barsWidth),
		liveprogress.WithSameAutoSizeInternalPadding(true, false),
		liveprogress.WithPrependDecorator(func(bar *liveprogress.Bar) string {
			return "     Concat | "
		}),
		liveprogress.WithPrependTimeElapsed(liveprogress.BaseStyle()),
		liveprogress.WithAppendPercent(liveprogress.BaseStyle()),
		liveprogress.WithAppendTimeRemaining(liveprogress.BaseStyle()),
		liveprogress.WithAppendDecorator(func(bar *liveprogress.Bar) string {
			return fmt.Sprintf(" left | %d/%d frames", bar.Current(), bar.Total())
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
		FrameRate:      frameRate,
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

// liveFinalVMAF computes the final VMAF on the fully encoded/concatenated output, in one pass: the
// score the thresholds gated, the other score of a model fed with CAMBI, and the banding when the
// CAMBI gate is on (see vmafSetup.finalMeasures). It is used by the encode and batch-search
// pipelines as the last quality-check step. Both files are decoded by dec when their codec allows
// it (software decode otherwise).
func liveFinalVMAF(ctx context.Context, source, distorted string, videoStream *ffmpeg.FFProbeBinaryStream, totalFrames int,
	setup vmafSetup, debug bool, dec ffmpeg.HWDecoderConfig) (summary ffmpeg.VMAFSummary, err error) {
	report, err := liveVMAF(ctx, ffmpeg.VMAFComputeConfig{
		ReferencePath:   source,
		DistortedPath:   distorted,
		InputFrameRate:  videoStream.RFrameRate,
		ReportPath:      distorted + "_vmaf.json",
		Model:           setup.model,
		ModelCAMBI:      setup.modelCAMBI,
		Measures:        setup.finalMeasures(),
		HWDecoderConfig: dec,
	}, totalFrames, debug)
	if err != nil {
		return
	}
	return report.Summary(setup.score, setup.modelCAMBI)
}

/*
 * Post-processing
 */

func liveRemuxSwapVideo(ctx context.Context, originalFile, newVideoFile, outputFile string, encodeToFLAC bool, tags ffmpeg.FFMEGTags,
	expectedDuration time.Duration, debug bool) (err error) {
	var currentStats ffmpeg.ProgressStats
	remuxBar := liveprogress.AddBar(
		liveprogress.WithTotal(uint64(expectedDuration)),
		liveprogress.WithRunes(fileProgressRunes),
		liveprogress.WithSameAutoSizeInternalPadding(true, false),
		liveprogress.WithPrependDecorator(func(bar *liveprogress.Bar) string {
			return "   Remuxing | "
		}),
		liveprogress.WithPrependTimeElapsed(liveprogress.BaseStyle()),
		liveprogress.WithAppendPercent(liveprogress.BaseStyle()),
		liveprogress.WithAppendTimeRemaining(liveprogress.BaseStyle()),
		liveprogress.WithAppendDecorator(func(bar *liveprogress.Bar) string {
			return fmt.Sprintf(" left | speed: %sx", strconv.FormatFloat(currentStats.Speed, 'f', -1, 64))
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
		liveprogress.WithRunes(fileProgressRunes),
		liveprogress.WithSameAutoSizeInternalPadding(true, false),
		liveprogress.WithPrependDecorator(func(bar *liveprogress.Bar) string {
			return "  MKV Stats | "
		}),
		liveprogress.WithPrependTimeElapsed(liveprogress.BaseStyle()),
		liveprogress.WithAppendPercent(liveprogress.BaseStyle()),
		liveprogress.WithAppendTimeRemaining(liveprogress.BaseStyle()),
		liveprogress.WithAppendDecorator(func(bar *liveprogress.Bar) string {
			return " left"
		}),
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
