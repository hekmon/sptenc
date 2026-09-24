package core

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"al.essio.dev/pkg/shellescape"
	"golang.org/x/sync/errgroup"
	"gonum.org/v1/gonum/stat"
)

const (
	segEncodedOutputFormat = "seg_%06d_qp%03d.mkv"
)

// Logger emits debug, warning, and error output during QP search.
type Logger interface {
	Debug(workerID int, format string, a ...any)
	Warning(workerID int, format string, a ...any)
	Error(workerID int, err error)
}

// SegmentLifecycle marks the boundaries of a single segment's search.
// workerID is guaranteed to be within [0, QPSearchConfig.NbConcurrentSegments-1],
// so callers may index fixed-size slices rather than maintain maps or RWMutexes.
// With several workers, OnSegmentDone calls can come out of order: totals are consistent
// within a call but a call can carry lower totals than the previous one. Keep the highest.
type SegmentLifecycle interface {
	OnSegmentStart(workerID, segmentIndex int, segmentPath string)
	OnSegmentDone(workerID, segmentFinalQP, segmentFrames, segmentNbAttempts int, currentTotalDuration time.Duration, currentTotalSize int64)
}

// ProgressReporter receives fine-grained progress for UI and diagnostics.
// workerID is guaranteed to be within [0, QPSearchConfig.NbConcurrentSegments-1],
// so callers may index fixed-size slices rather than maintain maps or RWMutexes.
type ProgressReporter interface {
	OnSegmentNewCandidate(workerID, qpCandidate int)
	OnSegmentAnalysisStart(workerID int, duration time.Duration) // the frames are counted: progress is expressed in time
	OnSegmentAnalysisProgress(workerID int, stats ProgressStats)
	OnSegmentAnalysisStop(workerID int)
	OnSegmentEncodeStart(workerID int, totalFrames int)
	OnSegmentEncodeProgress(workerID int, stats ProgressStats)
	OnSegmentEncodeStop(workerID int)
	OnSegmentVMAFStart(workerID int, totalFrames int)
	OnSegmentVMAFProgress(workerID int, stats ProgressStats)
	OnSegmentVMAFStop(workerID int)
}

// QPSearchCallbacks is the complete surface expected by FindAllSegmentsQP and its helpers.
// It is composed of smaller role interfaces so callers can satisfy only what they need
// (e.g. a test harness may embed Logger and SegmentLifecycle while ignoring ProgressReporter).
// workerID is guaranteed to be within [0, QPSearchConfig.NbConcurrentSegments-1],
// so callers may index fixed-size slices rather than maintain maps or RWMutexes.
type QPSearchCallbacks interface {
	Logger
	SegmentLifecycle
	ProgressReporter
}

// QPSearchConfig holds the invariants for a QP search run.
type QPSearchConfig struct {
	// SegmentsPaths contains the file paths of the video segments to search for optimal QP.
	SegmentsPaths []string
	// Auditor validates whether a candidate QP meets the target quality (e.g. via VMAF).
	Auditor VMAFChecker
	// WorkingDir is the directory where temporary encoded segments and VMAF reports are written during the search.
	WorkingDir string
	// StatsCache holds previous QP search statistics to guide and accelerate the search.
	StatsCache StatsCache
	// KeepInvalidQP, if true, retains encoded segments with non-selected QPs instead of deleting them.
	KeepInvalidQP bool

	// Encoder abstracts the concrete backend used for test encodes and VMAF computation.
	Encoder SegmentEncoder
	// NbConcurrentSegments sets the number of segments that will be searched (and encoded) in parallel.
	// Can be omitted: 0 or negative values will be set to 1.
	// The results are the same whatever the value, only the time taken changes. GPU encoders have
	// hard session limits which must not be exceeded. CPU encoders do gain from a few concurrent
	// encodes on many-core machines (a single encode does not keep them fully busy), at the
	// cost of memory.
	NbConcurrentSegments int
	// SourceFrameRate is the frame rate of the file the segments were cut from (through its
	// master), as ffprobe writes it, when known. Empty, the rate the segments declare is used.
	// Every segment must declare the same rate up to Matroska's rounding (see SameFrameRate),
	// and the results carry the source's. The segments are Matroska files: at 59.94 fps (and at
	// any rate whose exact fraction has a term above 30000) they declare an approximation,
	// 19001/317 for 60000/1001, whose grid drifts from the exact one by 0.19 ms per hour, while
	// a master made from a source that is not Matroska is numbered at the exact rate. Concat
	// durations and timestamps built on the approximation put some frames 1 ms off the master
	// (one in 60 from the start at 59.94 fps, measured), the ones built on the source's rate
	// put none. The encoders keep numbering frames at the rate they read: within a segment the
	// grids drift apart by 0.9 ns per frame at 59.94 fps and 3.2 ns at 119.88 (0.19 and 1.4 ms
	// per hour), which the snap of the concat corrects while it stays under half a frame (8.3
	// and 4.2 ms), along with the frames rounding the other way.
	SourceFrameRate string

	// ephemeral holds the in-memory stats accumulator for this encode.
	// It is set internally by FindAllSegmentsQP and discarded after the search.
	ephemeral *EphemeralStatsCache
}

// QPSearchResults holds the outcome of a QP search across all segments.
type QPSearchResults struct {
	// EncodedSegmentsPaths contains the file paths of the encoded segments that met the quality target.
	EncodedSegmentsPaths []string
	// QPs contains the selected QP value for each segment (aligned with EncodedSegmentsPaths).
	QPs []int
	// SegmentsFrames contains the exact number of frames of each segment (aligned with
	// EncodedSegmentsPaths): the source segment was decoded to count them, and its encode
	// was checked to hold as many.
	SegmentsFrames []int
	// FrameRate is the frame rate of the segments, as ffprobe writes it ("24000/1001"): the
	// source's when QPSearchConfig.SourceFrameRate is given, the one the segments declare
	// otherwise. All the segments declare the same one: the search fails on the first that
	// differs.
	FrameRate string
	// GlobalWeightedQP is the average QP across all segments, weighted by each segment's frame count.
	GlobalWeightedQP float64
	// TotalNbAttempts is the total number of encode attempts made during the search.
	TotalNbAttempts int
	// TotalSegmentsFrames is the sum of frame counts across all source segments.
	TotalSegmentsFrames int
	// TotalEncodedFrames is the sum of frame counts across all encode attempts (even from non selected qp encodes).
	TotalEncodedFrames int
	// NbBestEfforts is the number of segments that stop at the minimum QP without reaching the target VMAF profile.
	NbBestEfforts int
}

func (qpsr QPSearchResults) GetMinMaxQPs() (minQP, maxQP int) {
	switch len(qpsr.QPs) {
	case 0:
		return -1, -1
	case 1:
		return qpsr.QPs[0], qpsr.QPs[0]
	default:
		minQP, maxQP = qpsr.QPs[0], qpsr.QPs[0]
		for _, qp := range qpsr.QPs[1:] {
			if qp < minQP {
				minQP = qp
			}
			if qp > maxQP {
				maxQP = qp
			}
		}
		return
	}
}

// GetMeanStdDev returns the mean and standard deviation of the selected QPs.
// SegmentsDurations returns the exact duration of each segment (aligned with
// EncodedSegmentsPaths): its number of frames times the frame duration, computed from the
// frame rate fraction with integer arithmetic and rounded to the microsecond.
//
// # WHY THIS EXISTS
//
// The segments are concatenated by the ffmpeg concat demuxer, which places each file after
// the previous one at the previous one's duration. Left to itself, it takes that duration
// from the container: for a Matroska file it is the last frame's timestamp plus that frame's
// duration, both rounded to the millisecond, so a 23.976 fps segment declares itself 0.3 ms
// longer than its frames really last on average (its last frame counts for 42 ms instead of
// 41.708; the rounding of its last timestamp takes that from 0.17 ms shorter to 0.79 ms
// longer). That excess is never compensated: it adds up at every boundary, and the video
// ends 46 ms behind its audio on an episode of 163 segments (measured), a quarter of a
// second on a film of 800.
// The concat list can state a duration per file, which the demuxer uses instead: the
// sptenc pipeline knows the truth, the frame count of each segment (decoded and checked
// against its encode) and the constant frame rate the source was verified to have.
//
// # WHY NOT THE CONTAINER DURATION, SNAPPED
//
// Rounding the container duration to the nearest multiple of the frame duration would
// give the same value for files written by ffmpeg, but it is a guess on the muxer's
// arithmetic, and a wrong one on a container whose duration is not last pts plus last
// duration. The frame count is not a guess.
//
// # EDGE CASES
//
// The frame rate must be a fraction or an integer ("24000/1001", "24"), as ffprobe writes
// r_frame_rate; anything else is an error rather than a float parsed with a loss. The
// duration is rounded to the microsecond because that is the precision the concat demuxer
// parses: the error is then at most half a microsecond per boundary, in no fixed
// direction.
func (qpsr QPSearchResults) SegmentsDurations() (durations []time.Duration, err error) {
	durations = make([]time.Duration, len(qpsr.SegmentsFrames))
	for i, frames := range qpsr.SegmentsFrames {
		if durations[i], err = FramesDuration(frames, qpsr.FrameRate); err != nil {
			return nil, fmt.Errorf("segment %d: %w", i, err)
		}
	}
	return
}

// FramesDuration returns how long frames last at frameRate, a fraction or an integer as
// ffprobe writes r_frame_rate ("24000/1001", "24"), computed with integer arithmetic and
// rounded to the microsecond (see QPSearchResults.SegmentsDurations for why).
func FramesDuration(frames int, frameRate string) (duration time.Duration, err error) {
	if frames < 0 {
		return 0, fmt.Errorf("negative frame count: %d", frames)
	}
	num, den, err := parseFrameRateFraction(frameRate)
	if err != nil {
		return 0, fmt.Errorf("invalid frame rate %q: %w", frameRate, err)
	}
	// frames * den / num seconds, in microseconds, rounded to nearest
	micros := (int64(frames)*den*int64(time.Second/time.Microsecond) + num/2) / num
	return time.Duration(micros) * time.Microsecond, nil
}

// frameRateTolerance is how far apart, relatively, two frame rates can be and still be the
// same one, see SameFrameRate.
const frameRateTolerance = 1e-4

// SameFrameRate reports whether a and b, written as ffprobe writes r_frame_rate ("24000/1001",
// "25"), are the same frame rate up to what Matroska does to it.
//
// # WHY NOT EQUALITY
//
// Matroska stores the frame duration in whole nanoseconds, and ffmpeg reads it back as the
// closest fraction whose terms do not exceed 30000: a rate whose exact fraction has a larger
// term comes back approximated. 60000/1001 (59.94 fps) comes back as 19001/317, 120000/1001
// (119.88 fps) as 29011/242, 30 fps written as 1000000/33333 as 30/1 (measured). The master
// and the segments are Matroska files: from a source that is not, they declare the
// approximation while the source declares the exact rate. The two are the same rate, and the
// source's is the right one (see QPSearchConfig.SourceFrameRate).
//
// # WHY 1e-4
//
// The tolerance must accept Matroska's approximations and reject the closest distinct usual
// rates. A fraction with terms up to 30000 lies within about 1/60000 of any rate, relatively
// (1.7e-5; 1.0e-5 measured for 1000000/33333 read as 30/1, which 1e-5 refused), and the
// closest distinct usual rates are 1000/1001 apart (23.976 and 24 fps: 1e-3). 1e-4 leaves a
// factor 6 on one side and 10 on the other.
//
// # EDGE CASES
//
//   - Below 5 fps and from 1000 fps, ffmpeg does not take the frame rate of a Matroska file
//     from its frame duration but guesses it from the timestamps: the segments of a 5000/1001
//     source declared 15000/1001 (measured). Not the same rate: the search refuses them when
//     given the source's rate, where it used to produce a broken output.
func SameFrameRate(a, b string) (same bool, err error) {
	aNum, aDen, err := parseFrameRateFraction(a)
	if err != nil {
		return false, fmt.Errorf("invalid frame rate %q: %w", a, err)
	}
	bNum, bDen, err := parseFrameRateFraction(b)
	if err != nil {
		return false, fmt.Errorf("invalid frame rate %q: %w", b, err)
	}
	aRate, bRate := float64(aNum)/float64(aDen), float64(bNum)/float64(bDen)
	return math.Abs(aRate-bRate) <= frameRateTolerance*bRate, nil
}

// parseFrameRateFraction parses a frame rate written as "num/den" or "num" (den 1) into
// positive integers.
func parseFrameRateFraction(s string) (num, den int64, err error) {
	numStr, denStr, isFraction := strings.Cut(s, "/")
	if !isFraction {
		denStr = "1"
	}
	if num, err = strconv.ParseInt(strings.TrimSpace(numStr), 10, 64); err != nil {
		return 0, 0, fmt.Errorf("invalid numerator %q: %w", numStr, err)
	}
	if den, err = strconv.ParseInt(strings.TrimSpace(denStr), 10, 64); err != nil {
		return 0, 0, fmt.Errorf("invalid denominator %q: %w", denStr, err)
	}
	if num <= 0 || den <= 0 {
		return 0, 0, fmt.Errorf("frame rate %d/%d is not positive", num, den)
	}
	return
}

func (qpsr QPSearchResults) GetMeanStdDev() (mean, stddev float64) {
	if len(qpsr.QPs) == 0 {
		return 0, 0
	}
	if len(qpsr.QPs) == 1 {
		return float64(qpsr.QPs[0]), 0
	}
	qpf := make([]float64, len(qpsr.QPs))
	for i, q := range qpsr.QPs {
		qpf[i] = float64(q)
	}
	return stat.MeanStdDev(qpf, nil)
}

type job struct {
	segment int
	path    string
}

// FindAllSegmentsQP searches for the optimal QP for each segment.
func FindAllSegmentsQP(ctx context.Context, scb QPSearchCallbacks, config QPSearchConfig) (results QPSearchResults, err error) {
	// Validate required dependencies before spawning goroutines so that nil
	// inputs return an error instead of a panic.
	if scb == nil {
		err = fmt.Errorf("scb is nil")
		return
	}
	if config.Encoder == nil {
		err = fmt.Errorf("Encoder is nil")
		return
	}
	if config.SourceFrameRate != "" {
		if _, _, err = parseFrameRateFraction(config.SourceFrameRate); err != nil {
			err = fmt.Errorf("invalid source frame rate %q: %w", config.SourceFrameRate, err)
			return
		}
	}
	// Prepare
	var (
		segmentWeights int
		doneDuration   time.Duration
		allSegmentSize int64
		resultsAccess  sync.Mutex
	)
	results.EncodedSegmentsPaths = make([]string, len(config.SegmentsPaths))
	results.QPs = make([]int, len(config.SegmentsPaths))
	results.SegmentsFrames = make([]int, len(config.SegmentsPaths))
	if config.NbConcurrentSegments < 1 {
		config.NbConcurrentSegments = 1
	}
	// Wrap the persistent cache with an ephemeral one that learns from each
	// segment within this encode. It is discarded after the search.
	qpMin, qpMax, _ := config.Encoder.QPRange()
	config.ephemeral = NewEphemeralStatsCache(config.StatsCache, qpMin, qpMax)
	config.StatsCache = config.ephemeral
	// Launch Workers
	jobsChan := make(chan job)
	workers, workersCtx := errgroup.WithContext(ctx)
	for workerID := range config.NbConcurrentSegments {
		workers.Go(func(workerCtx context.Context, wID int, jobs <-chan job) func() error {
			var (
				segmentDuration   time.Duration
				segmentFrames     int
				segmentFrameRate  string
				segmentQP         int
				segmentSize       int64
				segmentNbAttempts int
				bestEffort        bool
			)
			return func() (err error) {
				for job := range jobs {
					scb.OnSegmentStart(wID, job.segment, job.path)
					// Find this segment QP
					if segmentQP, segmentFrames, segmentFrameRate, segmentNbAttempts, bestEffort, segmentDuration, err =
						findSegmentQP(workerCtx, scb, config, wID, job.segment, job.path); err != nil {
						err = fmt.Errorf("failed to find the right encoding QP for segment %d: %w", job.segment, err)
						return
					}
					encodedSegmentPath := filepath.Join(config.WorkingDir, fmt.Sprintf(segEncodedOutputFormat, job.segment, segmentQP))
					if segmentSize, err = getFileSize(encodedSegmentPath); err != nil {
						err = fmt.Errorf("failed to get the size of segment %d: %w", job.segment, err)
						return
					}
					// Update global stats
					resultsAccess.Lock()
					results.EncodedSegmentsPaths[job.segment] = encodedSegmentPath
					results.QPs[job.segment] = segmentQP
					results.SegmentsFrames[job.segment] = segmentFrames
					// One frame rate for the whole set: the concat durations and the final
					// VMAF (-r) rely on it.
					if results.FrameRate == "" {
						results.FrameRate = segmentFrameRate
					} else if segmentFrameRate != results.FrameRate {
						resultsAccess.Unlock()
						return fmt.Errorf("segment %d has a frame rate of %s while the previous ones have %s",
							job.segment, segmentFrameRate, results.FrameRate)
					}
					results.TotalSegmentsFrames += segmentFrames
					results.TotalEncodedFrames += segmentFrames * segmentNbAttempts
					results.TotalNbAttempts += segmentNbAttempts
					if bestEffort {
						results.NbBestEfforts++
					}
					doneDuration += segmentDuration
					allSegmentSize += segmentSize
					segmentWeights += segmentQP * segmentFrames
					config.ephemeral.addQP(segmentQP)
					// The totals to report must be read while the lock is held: another worker
					// can be updating them as soon as it is released. The callback itself is
					// called without the lock, a slow callback must not hold the other workers.
					currentTotalDuration, currentTotalSize := doneDuration, allSegmentSize
					resultsAccess.Unlock()
					// Done
					scb.OnSegmentDone(wID, segmentQP, segmentFrames, segmentNbAttempts, currentTotalDuration, currentTotalSize)
				}
				return
			}
		}(workersCtx, workerID, jobsChan))
	}
	// Launch Feeder
	workers.Go(func(workerCtx context.Context, jobs chan<- job) func() error {
		return func() error {
			defer close(jobs)
			for segment, segmentPath := range config.SegmentsPaths {
				select {
				case jobs <- job{
					segment: segment,
					path:    segmentPath,
				}:
					// job sent, let's loop
				case <-workerCtx.Done():
					// early exit, one of the process worker encountered an error:
					// let's close the jobs channel feeder by safety (as no one will be reading it anymore)
					return nil
				}
			}
			// All jobs sent, let's exit (and close the feeder chan to signal workers they can stop once done)
			return nil
		}
	}(workersCtx, jobsChan))
	// Wait until the end
	if err = workers.Wait(); err != nil {
		return
	}
	if config.SourceFrameRate != "" {
		// every segment was checked against it (findSegmentQP)
		results.FrameRate = config.SourceFrameRate
	}
	results.GlobalWeightedQP = float64(segmentWeights) / float64(results.TotalSegmentsFrames)
	return
}

func findSegmentQP(ctx context.Context, scb QPSearchCallbacks, config QPSearchConfig,
	workerID, segment int, segmentPath string) (
	finalQP, segmentFrames int, frameRate string, nbAttempts int, bestEffort bool, duration time.Duration, err error) {
	scb.Debug(workerID, "Segment %d: Search for the right QP", segment)
	// Prepare
	videoTrack, err := probeVideoStream(ctx, scb, config, workerID, segmentPath)
	if err != nil {
		err = fmt.Errorf("failed to get streams infos: %w", err)
		return
	}
	duration = videoTrack.Duration
	frameRate = videoTrack.RFrameRate
	// Before any encode: a segment whose frame rate is not its source's is refused here (a
	// Matroska file below 5 fps, whose rate ffmpeg guesses, a directory mixing segments of
	// different files)
	if config.SourceFrameRate != "" {
		var same bool
		if same, err = SameFrameRate(config.SourceFrameRate, frameRate); err != nil {
			return
		}
		if !same {
			err = fmt.Errorf("the segment declares a frame rate of %s while its source is at %s: its frames can not be put back on the source's frame grid",
				frameRate, config.SourceFrameRate)
			return
		}
	}
	// Abort if frame count is 0 or negative
	totalFrames := videoTrack.NbReadFrames
	if totalFrames <= 0 {
		err = fmt.Errorf("segment %d: frame count is 0 or negative (NbReadFrames: %d, duration: %s, frameRate: %s). Cannot proceed without valid frame count",
			segment, totalFrames, duration, videoTrack.RFrameRate,
		)
		return
	}
	// Verify output files frames count when done
	defer func() {
		if err != nil {
			// if we exit with an error, no need to check that everything is fine
			return
		}
		finalQPSegmentPath := filepath.Join(config.WorkingDir, fmt.Sprintf(segEncodedOutputFormat, segment, finalQP))
		var finalStream VideoStream
		if finalStream, err = probeVideoStream(ctx, scb, config, workerID, finalQPSegmentPath); err != nil {
			// make findSegmentQP return an error
			err = fmt.Errorf("failed to get streams infos of final segment: %w", err)
			return
		}
		if segmentFrames = finalStream.NbReadFrames; segmentFrames != totalFrames {
			// make findSegmentQP return an error
			err = fmt.Errorf("final segment has %d frames instead of %d", segmentFrames, totalFrames)
			return
		}
		scb.Debug(workerID, "Final segment has %d frames, as original GOP.", segmentFrames)
	}()
	// Search
	var testedQPs []int
	if !config.KeepInvalidQP {
		// Delete invalid QPs once finished
		defer func() {
			for _, testedQP := range testedQPs {
				if testedQP == finalQP {
					continue
				}
				invalidQPPath := filepath.Join(config.WorkingDir, fmt.Sprintf(segEncodedOutputFormat, segment, testedQP))
				if err := os.Remove(invalidQPPath); err != nil {
					scb.Error(workerID, fmt.Errorf("Failed to remove %s: %s", shellescape.Quote(invalidQPPath), err))
				}
			}
		}()
	}
	if finalQP, nbAttempts, bestEffort, testedQPs, err = searchSegmentQP(ctx, scb, config, workerID, segment, segmentPath, videoTrack); err != nil {
		err = fmt.Errorf("failed to search segment QP: %w", err)
		return
	}
	if bestEffort {
		scb.Warning(workerID, "Segment %d: Impossible to validate VMAF config with lowest possible QP (highest quality), keeping it anyway", segment+1)
	}
	return
}

// probeVideoStream returns the video stream of a file with its exact number of frames: the
// metadata first, then the whole stream is decoded to count them (the long part, reported as
// the analysis of the segment).
func probeVideoStream(ctx context.Context, scb QPSearchCallbacks, config QPSearchConfig, workerID int, filePath string) (
	stream VideoStream, err error) {
	debug := func(s string) {
		scb.Debug(workerID, s)
	}
	runtimeError := func(err error) {
		scb.Error(workerID, err)
	}
	if stream, err = config.Encoder.ProbeStream(ctx, filePath, debug, runtimeError); err != nil {
		return
	}
	scb.OnSegmentAnalysisStart(workerID, stream.Duration)
	defer scb.OnSegmentAnalysisStop(workerID)
	stream.NbReadFrames, err = config.Encoder.CountFrames(ctx, filePath, func(stats ProgressStats) {
		scb.OnSegmentAnalysisProgress(workerID, stats)
	}, debug, runtimeError)
	return
}

// searchSegmentQP finds the highest valid QP (smallest file) for a segment.
//
// The algorithm intentionally keeps each phase (bracketing, interpolation,
// boundary walks) explicit and inline. Edge-case handling is subtle;
// resist collapsing into generic helpers - readability trumps brevity here.
//
// The QP→VMAF relationship is empirically monotonic (lower QP = higher VMAF).
// This has held across 2+ years of production encoding; non-monotonic edge cases
// have not been observed in practice.
func searchSegmentQP(ctx context.Context, scb QPSearchCallbacks, config QPSearchConfig,
	workerID, segment int, segmentPath string, videoTrack VideoStream) (
	finalQP int, nbAttempts int, bestEffort bool, testedQPs []int, err error) {
	// Keep track of tested QPs
	qpMin, qpMax, found := config.Encoder.QPRange()
	if !found {
		err = fmt.Errorf("failed to get QP range for encoder %s", config.Encoder.Name())
		return
	}
	testedQPs = make([]int, 0, qpMax-qpMin+1) // ordered
	results := make(map[int]VMAFStats, qpMax-qpMin+1)
	defer func() {
		scb.Debug(workerID, "QPs tested: %+v", testedQPs)
	}()
	// Search loop
	var (
		candidateQP                                          int
		alreadyComputed, bestValidTested, firstInvalidTested bool
		vmafStats                                            VMAFStats
	)
	bestValid := qpMin
	firstInvalid := qpMax
	mean, stddev := config.StatsCache.GetMeanStdDev()
	if stddev == 0 {
		stddev = 1
	}
	for {
		// Find a candidate
		_, bestValidTested = results[bestValid]
		_, firstInvalidTested = results[firstInvalid]
		if len(testedQPs) == 0 {
			// Step 1: test the mean as the starting point to determine the search direction.
			// If stats are out of the encoder range, the encode fails fast. Rotten data is
			// caught cheaply - no need to defensively clamp.
			candidateQP = mean
			scb.Debug(workerID, "Searching for QP in range [%d, %d] with %d as first candidate", bestValid, firstInvalid, candidateQP)
		} else if !(bestValidTested && firstInvalidTested) {
			// Step 2: close the range. A valid result raises bestValid; an invalid one lowers firstInvalid.
			// This leaves one bound at its original extreme, signaling which direction to search.
			// Step from the mean in stddev increments until we bracket the threshold.
			// Once both sides are known, interpolation walks from invalid toward valid to find
			// the highest valid QP - the one that yields the smallest file.
			if bestValid == qpMin {
				if candidateQP = mean - len(results)*stddev; candidateQP < qpMin {
					if _, alreadyComputed = results[qpMin]; alreadyComputed {
						// qpMin is already known valid but we have not closed the bracket.
						// Jump to the opposite extreme to find an invalid bound so that
						// interpolation can narrow the range instead of giving up.
						candidateQP = qpMax
					} else {
						candidateQP = qpMin
					}
				}
			} else if firstInvalid == qpMax {
				if candidateQP = mean + len(results)*stddev; candidateQP > qpMax {
					candidateQP = qpMax
				}
			} else {
				// should not happen
				err = fmt.Errorf("invalid devstdinterpol state: bestValidTested=%t (%d), firstInvalidTested=%t (%d)", bestValidTested, bestValid, firstInvalidTested, firstInvalid)
				return
			}
		} else {
			// Step 3: the range is closed - narrow it with interpolation.
			if candidateQP, err = interpolateCandidate(scb, config, workerID, bestValid, firstInvalid, qpMin, qpMax, results); err != nil {
				err = fmt.Errorf("failed to find candidate: %w", err)
				return
			}
			// Handle predicted candidate.
			// A valid QP is not optimal until the next higher QP is confirmed invalid.
			// The boundary must be found, not just any valid point.
			if vmafStats, found = results[candidateQP]; found {
				scb.Debug(workerID, "Predicted candidate %d already computed (valid: %t)", candidateQP, config.Auditor.Validate(vmafStats))
				// We already computed this candidate, let's think this thru
				if config.Auditor.Validate(vmafStats) {
					if candidateQP == qpMax {
						// Can not go higher, we are done
						finalQP = candidateQP
						return
					}
					// Are we sure that next higher candidate does not validate ?
					for i := candidateQP + 1; i <= qpMax; i++ {
						if vmafStats, found = results[i]; found {
							// we already computed this candidate
							// Validity must be checked before the qpMax shortcut: being already
							// computed does not make qpMax valid. It is even the opposite, a valid
							// qpMax ends the search as soon as it is tested (see the end of the
							// loop), so a qpMax found here is one that failed during bracketing.
							// Returning it because "we can not go higher" would accept a segment
							// below its VMAF profile when the optimal QP is qpMax-1.
							if !config.Auditor.Validate(vmafStats) {
								// Invalid, previous was the last valid
								finalQP = i - 1
								return
							}
							if i == qpMax {
								// we reached qp max, already computed and valid: we are done
								finalQP = i
								return
							}
							// else continue to go up
							scb.Debug(workerID, "Looking up: candidate %d already computed (valid: %t)", i, true)
						} else {
							// we found a candidate for smaller size that we did not compute yet
							candidateQP = i
							break
						}
					}
				} else {
					// the predicted candidate is already computed and it does not validate
					if candidateQP == qpMin {
						// can not go lower, and does not validate: we are done (best effort)
						finalQP = candidateQP
						bestEffort = true
						return
					}
					// Let's go down one by one until it validates
					for i := candidateQP - 1; i >= qpMin; i-- {
						if vmafStats, found = results[i]; found {
							// we already computed this candidate
							if i == qpMin {
								// we reached qp min, which is already computed, we are done (best effort)
								finalQP = i
								bestEffort = true
								return
							}
							if config.Auditor.Validate(vmafStats) {
								// This already computed lower QP is valid, no need to go lower
								finalQP = i
								return
							}
							// else continue to go down
							scb.Debug(workerID, "Looking down: candidate %d already computed (valid: %t)", i, false)
						} else {
							// we found a candidate for better quality that we did not compute yet
							candidateQP = i
							break
						}
					}
				}
			} else {
				scb.Debug(workerID, "Predicted candidate %d selected for computation", candidateQP)
			}
		}
		// Test candidate and narrow the search
		scb.OnSegmentNewCandidate(workerID, candidateQP)
		if vmafStats, err = segmentQP(ctx, scb, config, segmentPath, workerID, segment, candidateQP, videoTrack); err != nil {
			err = fmt.Errorf("failed to produce QP %d: %w", candidateQP, err)
			return
		}
		nbAttempts++
		results[candidateQP] = vmafStats
		testedQPs = append(testedQPs, candidateQP)
		if config.Auditor.Validate(vmafStats) {
			bestValid = candidateQP
			if candidateQP == qpMax {
				// Can not go higher: this is the optimal QP. Concluding here also covers
				// the case where qpMax is the very first candidate (a single result can
				// not feed the interpolation).
				finalQP = candidateQP
				return
			}
		} else {
			firstInvalid = candidateQP
			if candidateQP == qpMin {
				// Can not go lower, and it does not validate: we are done (best effort).
				// Same as above for qpMin being the very first candidate.
				finalQP = candidateQP
				bestEffort = true
				return
			}
		}
	}
}

func interpolateCandidate(scb QPSearchCallbacks, config QPSearchConfig,
	workerID, bestValid, firstInvalid, encoderQPMin, encoderQPMax int, existingResults map[int]VMAFStats) (
	candidateQP int, err error) {
	predictor, err := NewPredictor(existingResults, encoderQPMin, encoderQPMax, func(format string, a ...any) {
		scb.Debug(workerID, format, a...)
	})
	if err != nil {
		err = fmt.Errorf("failed to create predictor: %w", err)
		return
	}
	// Walk the bracket from the invalid side down to the valid side.
	// Prefer real results; predict only when missing.
	// Return the first candidate that validates.
	var (
		candidateResults VMAFStats
		exists           bool
	)
	for candidateQP = firstInvalid; candidateQP > bestValid; candidateQP-- {
		if candidateResults, exists = existingResults[candidateQP]; !exists {
			candidateResults = predictor.Predict(candidateQP)
			// Predicted result will be validated below
		}
		if config.Auditor.Validate(candidateResults) {
			// Found a validating candidate within the bracket, either a real result or a forecast.
			// Return it for the search loop to handle.
			return
		}
	}
	// Nothing in the bracket validated, fall back to the known-good bestValid.
	scb.Debug(workerID, "No candidate found in range %d-%d, returning %d", bestValid, firstInvalid, candidateQP)
	return
}

func segmentQP(ctx context.Context, scb QPSearchCallbacks, config QPSearchConfig,
	input string, workerID, segment, qp int, videoTrack VideoStream) (
	vmafStats VMAFStats, err error) {
	output := filepath.Join(config.WorkingDir, fmt.Sprintf(segEncodedOutputFormat, segment, qp))
	// Encode
	scb.OnSegmentEncodeStart(workerID, videoTrack.NbReadFrames)
	encodeErr := config.Encoder.Encode(ctx, input, output, qp, videoTrack, func(stats ProgressStats) {
		scb.OnSegmentEncodeProgress(workerID, stats)
	}, func(msg string) {
		scb.Debug(workerID, msg)
	}, func(err error) {
		scb.Error(workerID, err)
	})
	scb.OnSegmentEncodeStop(workerID)
	if encodeErr != nil {
		err = fmt.Errorf("failed to encode segment: %w", encodeErr)
		return
	}
	// Compute VMAF
	scb.OnSegmentVMAFStart(workerID, videoTrack.NbReadFrames)
	vmafStats, vmafErr := config.Encoder.ComputeVMAF(ctx, input, output, videoTrack, func(stats ProgressStats) {
		scb.OnSegmentVMAFProgress(workerID, stats)
	}, func(msg string) {
		scb.Debug(workerID, msg)
	}, func(err error) {
		scb.Error(workerID, err)
	})
	scb.OnSegmentVMAFStop(workerID)
	if vmafErr != nil {
		err = fmt.Errorf("failed to compute VMAF for segment: %w", vmafErr)
		return
	}
	scb.Debug(workerID, "Segment %d: QP %d: VMAF results: %s", segment, qp, vmafStats)
	return
}
