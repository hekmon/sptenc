package core

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"al.essio.dev/pkg/shellescape"
	"golang.org/x/sync/errgroup"
)

const (
	segEncodedOutputFormat = "seg_%06d_qp%03d.mkv"
)

// Logger emits debug, warning, and error output during QP search.
type Logger interface {
	Debug(format string, a ...any)
	Warning(format string, a ...any)
	Error(err error)
}

// SegmentLifecycle marks the boundaries of a single segment's search.
// workerID is guaranteed to be within [0, QPSearchConfig.NbConcurrentSegments-1],
// so callers may index fixed-size slices rather than maintain maps or RWMutexes.
type SegmentLifecycle interface {
	OnSegmentStart(workerID, segmentIndex int, segmentPath string)
	OnSegmentDone(workerID, segmentFinalQP, segmentFrames, segmentNbAttempts int, currentTotalDuration time.Duration, currentTotalSize int64)
}

// ProgressReporter receives fine-grained progress for UI and diagnostics.
// workerID is guaranteed to be within [0, QPSearchConfig.NbConcurrentSegments-1],
// so callers may index fixed-size slices rather than maintain maps or RWMutexes.
type ProgressReporter interface {
	OnSegmentNewCandidate(workerID, qpCandidate int)
	OnSegmentAnalysisStart(workerID int, fileSize int64)
	OnSegmentAnalysisProgress(workerID int, read int64) // not total, additional
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
	// USE WITH CAUTION: GPU encoders have hard session limits, and CPU encoders already
	// saturate physical cores in most cases. Only increase this if you know how many parallel encodes
	// your specific hardware can sustain.
	NbConcurrentSegments int

	// ephemeral holds the in-memory stats accumulator for this encode.
	// It is set internally by FindAllSegmentsQP and discarded after the search.
	ephemeral *ephemeralStatsCache
}

// QPSearchResults holds the outcome of a QP search across all segments.
type QPSearchResults struct {
	// EncodedSegmentsPaths contains the file paths of the encoded segments that met the quality target.
	EncodedSegmentsPaths []string
	// QPs contains the selected QP value for each segment (aligned with EncodedSegmentsPaths).
	QPs []int
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

// workerLogWrapper delegates all QPSearchCallbacks methods to an inner implementation
// but conditionally prefixes Debug and Error messages with the worker ID only when
// there is more than one concurrent worker. This keeps single-worker logs clean.
type workerLogWrapper struct {
	QPSearchCallbacks
	workerID     int
	totalWorkers int
}

func (w workerLogWrapper) Debug(format string, a ...any) {
	if w.totalWorkers > 1 {
		w.QPSearchCallbacks.Debug("[worker %d] "+format, append([]any{w.workerID}, a...)...)
	} else {
		w.QPSearchCallbacks.Debug(format, a...)
	}
}

func (w workerLogWrapper) Error(err error) {
	if w.totalWorkers > 1 {
		w.QPSearchCallbacks.Error(fmt.Errorf("[worker %d] %w", w.workerID, err))
	} else {
		w.QPSearchCallbacks.Error(err)
	}
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
	// Prepare
	var (
		segmentWeights int
		doneDuration   time.Duration
		allSegmentSize int64
		resultsAccess  sync.Mutex
	)
	results.EncodedSegmentsPaths = make([]string, len(config.SegmentsPaths))
	results.QPs = make([]int, len(config.SegmentsPaths))
	if config.NbConcurrentSegments < 1 {
		config.NbConcurrentSegments = 1
	}
	// Wrap the persistent cache with an ephemeral one that learns from each
	// segment within this encode. It is discarded after the search.
	qpMin, qpMax, _ := config.Encoder.QPRange()
	config.ephemeral = newEphemeralStatsCache(config.StatsCache, qpMin, qpMax)
	config.StatsCache = config.ephemeral
	// Launch Workers
	jobsChan := make(chan job)
	workers, workersCtx := errgroup.WithContext(ctx)
	for workerID := range config.NbConcurrentSegments {
		workers.Go(func(workerCtx context.Context, wID int, jobs <-chan job) func() error {
			var (
				segmentDuration   time.Duration
				segmentFrames     int
				segmentQP         int
				segmentSize       int64
				segmentNbAttempts int
				bestEffort        bool
			)
			wcb := workerLogWrapper{QPSearchCallbacks: scb, workerID: wID, totalWorkers: config.NbConcurrentSegments}
			return func() (err error) {
				for job := range jobs {
					wcb.OnSegmentStart(wID, job.segment, job.path)
					// Find this segment QP
					if segmentQP, segmentFrames, segmentNbAttempts, bestEffort, segmentDuration, err =
						findSegmentQP(workerCtx, wcb, config, wID, job.segment, job.path); err != nil {
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
					resultsAccess.Unlock()
					// Done
					scb.OnSegmentDone(wID, segmentQP, segmentFrames, segmentNbAttempts, doneDuration, allSegmentSize)
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
	results.GlobalWeightedQP = float64(segmentWeights) / float64(results.TotalSegmentsFrames)
	return
}

func findSegmentQP(ctx context.Context, scb QPSearchCallbacks, config QPSearchConfig,
	workerID, segment int, segmentPath string) (
	finalQP, segmentFrames, nbAttempts int, bestEffort bool, duration time.Duration, err error) {
	scb.Debug("Segment %d: Search for the right QP", segment)
	// Prepare
	videoTrack, err := getStreamsInfosCF(ctx, scb, config, workerID, segmentPath)
	if err != nil {
		err = fmt.Errorf("failed to get streams infos: %w", err)
		return
	}
	duration = videoTrack.Duration
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
		if finalStream, err = getStreamsInfosCF(ctx, scb, config, workerID, finalQPSegmentPath); err != nil {
			// make findSegmentQP return an error
			err = fmt.Errorf("failed to get streams infos of final segment: %w", err)
			return
		}
		if segmentFrames = finalStream.NbReadFrames; segmentFrames != totalFrames {
			// make findSegmentQP return an error
			err = fmt.Errorf("final segment has %d frames instead of %d", segmentFrames, totalFrames)
			return
		}
		scb.Debug("Final segment has %d frames, as original GOP.", segmentFrames)
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
					scb.Error(fmt.Errorf("Failed to remove %s: %s", shellescape.Quote(invalidQPPath), err))
				}
			}
		}()
	}
	if finalQP, nbAttempts, bestEffort, testedQPs, err = searchSegmentQP(ctx, scb, config, workerID, segment, segmentPath, videoTrack); err != nil {
		err = fmt.Errorf("failed to search segment QP: %w", err)
		return
	}
	if bestEffort {
		scb.Warning("Segment %d: Impossible to validate VMAF config with lowest possible QP (highest quality), keeping it anyway", segment+1)
	}
	return
}

func getStreamsInfosCF(ctx context.Context, scb QPSearchCallbacks, config QPSearchConfig, workerID int, filePath string) (
	stream VideoStream, err error) {
	// Recover size
	fileInfos, err := os.Stat(filePath)
	if err != nil {
		err = fmt.Errorf("failed to stat the file: %w", err)
		return
	}
	// Prepare signals
	scb.OnSegmentAnalysisStart(workerID, fileInfos.Size())
	defer scb.OnSegmentAnalysisStop(workerID)
	// Start analysis
	return config.Encoder.ProbeStream(ctx, filePath, func(bytesRead int64) {
		scb.OnSegmentAnalysisProgress(workerID, bytesRead)
	}, func(s string) {
		scb.Debug(s)
	}, scb.Error)
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
		scb.Debug("QPs tested: %+v", testedQPs)
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
			scb.Debug("Searching for QP in range [%d, %d] with %d as first candidate", bestValid, firstInvalid, candidateQP)
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
				scb.Debug("Predicted candidate %d already computed (valid: %t)", candidateQP, config.Auditor.Validate(vmafStats))
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
							if i == qpMax {
								// we reached qp max, which is already computed, we are done
								finalQP = i
								return
							}
							if !config.Auditor.Validate(vmafStats) {
								// Invalid, previous was the last valid
								finalQP = i - 1
								return
							}
							// else continue to go up
							scb.Debug("Looking up: candidate %d already computed (valid: %t)", i, true)
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
							scb.Debug("Looking down: candidate %d already computed (valid: %t)", i, false)
						} else {
							// we found a candidate for better quality that we did not compute yet
							candidateQP = i
							break
						}
					}
				}
			} else {
				scb.Debug("Predicted candidate %d selected for computation", candidateQP)
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
		if config.Auditor.Validate(vmafStats) {
			bestValid = candidateQP
		} else {
			firstInvalid = candidateQP
		}
		testedQPs = append(testedQPs, candidateQP)
	}
}

func interpolateCandidate(scb QPSearchCallbacks, config QPSearchConfig,
	workerID, bestValid, firstInvalid, encoderQPMin, encoderQPMax int, existingResults map[int]VMAFStats) (
	candidateQP int, err error) {
	predicator, err := NewPredicator(existingResults, encoderQPMin, encoderQPMax, scb.Debug)
	if err != nil {
		err = fmt.Errorf("failed to create predicator: %w", err)
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
			candidateResults = predicator.Predict(candidateQP)
			// Predicted result will be validated below
		}
		if config.Auditor.Validate(candidateResults) {
			// Found a validating candidate within the bracket, either a real result or a forecast.
			// Return it for the search loop to handle.
			return
		}
	}
	// Nothing in the bracket validated, fall back to the known-good bestValid.
	scb.Debug("No candidate found in range %d-%d, returning %d", bestValid, firstInvalid, candidateQP)
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
		scb.Debug(msg)
	}, scb.Error)
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
		scb.Debug(msg)
	}, scb.Error)
	scb.OnSegmentVMAFStop(workerID)
	if vmafErr != nil {
		err = fmt.Errorf("failed to compute VMAF for segment: %w", vmafErr)
		return
	}
	scb.Debug("Segment %d: QP %d: VMAF results:\n%s", segment, qp, vmafStats)
	return
}
