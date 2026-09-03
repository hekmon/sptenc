package core

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/hekmon/sptenc/ng/ffmpeg"

	"al.essio.dev/pkg/shellescape"
	"github.com/hekmon/cunits/v3"
)

const (
	segEncodedOutputFormat = "seg_%d_qp%d.mkv"
)

// QPSearchCallbacks is implemented by the caller to observe and present the search process.
type QPSearchCallbacks interface {
	// Logging
	Debug(format string, a ...any)
	Warning(format string, a ...any)
	Error(err error)
	// Segment lifecycle
	OnSegmentStart(segmentIndex int, segmentPath string)
	OnSegmentNewCandidate(qpCandidate int)
	OnSegmentAnalysisStart(filePath string, fileSize cunits.Bits)
	OnSegmentAnalysisProgress(newRead cunits.Bits)
	OnSegmentAnalysisStop()
	QPSearchCallbackEncodeStart(totalFrames int)
	QPSearchCallbackEncodeProgress(stats ffmpeg.ProgressStats)
	QPSearchCallbackEncodeStop()
	OnSegmentDone(segmentFinalQP, segmentFrames, segmentNbAttempts int, currentTotalDuration time.Duration, currentTotalSize cunits.Bits)
}

// QPSearchConfig holds the invariants for a QP search run.
type QPSearchConfig struct {
	SegmentPaths  []string
	Auditor       VMAFChecker
	WorkingDir    string
	StatsCache    *StatsCacheHistory
	Encoder       ffmpeg.Encoder
	KeepInvalidQP bool
}

type QPSearchResults struct {
	EncodedSegmentsPaths []string
	QPs                  []int
	SearchDuration       time.Duration
	GlobalWeightedQP     float64
	TotalNbAttempts      int
	TotalSegmentsFrames  int
	TotalEncodedFrames   int
	NbBestEfforts        int
}

// FindAllSegmentsQP searches for the optimal QP for each segment.
func FindAllSegmentsQP(ctx context.Context, scb QPSearchCallbacks, config QPSearchConfig) (results QPSearchResults, err error) {
	// Prepare
	var (
		segmentDuration, doneDuration time.Duration
		segmentFrames                 int
		segmentQP, segmentWeights     int
		segmentSize, allSegmentSize   cunits.Bits
		segmentNbAttempts             int
		bestEffort                    bool
	)
	results.EncodedSegmentsPaths = make([]string, len(config.SegmentPaths))
	results.QPs = make([]int, len(config.SegmentPaths))
	// Go
	start := time.Now()
	for segment, segmentPath := range config.SegmentPaths {
		scb.OnSegmentStart(segment, segmentPath)
		// Find this segment QP
		if segmentQP, segmentFrames, segmentNbAttempts, bestEffort, segmentDuration, err = findSegmentQP(ctx, scb, config, segment, segmentPath); err != nil {
			err = fmt.Errorf("failed to find the right encoding QP for segment %d: %w", segment, err)
			return
		}
		encodedSegmentPath := filepath.Join(config.WorkingDir, fmt.Sprintf(segEncodedOutputFormat, segment, segmentQP))
		if segmentSize, err = getFileSize(encodedSegmentPath); err != nil {
			err = fmt.Errorf("failed to get the size of segment %d: %w", segment, err)
			return
		}
		// Update stats
		results.EncodedSegmentsPaths[segment] = encodedSegmentPath
		results.QPs[segment] = segmentQP
		results.TotalSegmentsFrames += segmentFrames
		results.TotalEncodedFrames += segmentFrames * segmentNbAttempts
		results.TotalNbAttempts += segmentNbAttempts
		if bestEffort {
			results.NbBestEfforts++
		}
		doneDuration += segmentDuration
		allSegmentSize += segmentSize
		segmentWeights += segmentQP * segmentFrames
		// Done
		scb.OnSegmentDone(segmentQP, segmentFrames, segmentNbAttempts, doneDuration, allSegmentSize)
	}
	results.SearchDuration = time.Since(start)
	results.GlobalWeightedQP = float64(segmentWeights) / float64(results.TotalSegmentsFrames)
	return
}

func findSegmentQP(ctx context.Context, scb QPSearchCallbacks, config QPSearchConfig,
	segment int, segmentPath string) (
	finalQP, segmentFrames, nbAttempts int, bestEffort bool, duration time.Duration, err error) {
	scb.Debug("Segment %d: Search for the right QP", segment)
	// Prepare
	segmentInfos, err := getStreamsInfosCF(ctx, scb, segmentPath)
	if err != nil {
		err = fmt.Errorf("failed to get streams infos: %w", err)
		return
	}
	duration = segmentInfos.Format.Duration
	videoTrack := segmentInfos.VideoTrack()
	// Abort if frame count is 0 or negative
	totalFrames := videoTrack.NbReadFrames
	if totalFrames <= 0 {
		totalFrames = videoTrack.NbFrames
		if totalFrames <= 0 {
			err = fmt.Errorf("segment %d: frame count is 0 or negative (Nb(Read)Frames: %d, duration: %s, frameRate: %s). Cannot proceed without valid frame count",
				segment, totalFrames, duration, videoTrack.RFrameRate,
			)
			return
		}
	}
	// Verify output files frames count when done
	defer func() {
		if err != nil {
			// if we exit with an error, no need to check that everything is fine
			return
		}
		finalQPSegmentPath := filepath.Join(config.WorkingDir, fmt.Sprintf(segEncodedOutputFormat, segment, finalQP))
		var finalSegmentInfos ffmpeg.FFProbeStats
		if finalSegmentInfos, err = getStreamsInfosCF(ctx, scb, finalQPSegmentPath); err != nil {
			// make findSegmentQP return an error
			err = fmt.Errorf("failed to get streams infos of final segment: %w", err)
			return
		}
		if segmentFrames = finalSegmentInfos.VideoTrack().NbReadFrames; segmentFrames != totalFrames {
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
	if finalQP, nbAttempts, bestEffort, testedQPs, err = searchSegmentQP(ctx, scb, config, segment, segmentPath, videoTrack); err != nil {
		err = fmt.Errorf("failed to search segment QP: %w", err)
		return
	}
	if bestEffort {
		scb.Warning("Segment #%d: Impossible to validate VMAF config with lowest possible QP (highest quality), keeping it anyway", segment)
	}
	return
}

func getStreamsInfosCF(ctx context.Context, scb QPSearchCallbacks, filePath string) (
	stats ffmpeg.FFProbeStats, err error) {
	// Recover size
	fileInfos, err := os.Stat(filePath)
	if err != nil {
		err = fmt.Errorf("failed to stat the file: %w", err)
		return
	}
	// Prepare signals
	scb.OnSegmentAnalysisStart(filePath, cunits.ImportInBytes(float64(fileInfos.Size())))
	defer scb.OnSegmentAnalysisStop()
	// Start analysis
	return ffmpeg.GetStreamsInfosCF(ctx, ffmpeg.GetStreamsInfosCFConfig{
		GetStreamsInfosConfig: ffmpeg.GetStreamsInfosConfig{
			// Input
			Path: filePath,
			// Reporting
			Debug: func(s string) {
				scb.Debug(s)
			},
			RuntimeError: scb.Error,
		},
		ReadBytesReport: func(bytesRead int) {
			scb.OnSegmentAnalysisProgress(cunits.ImportInBytes(float64(bytesRead)))
		},
	})
}

// searchSegmentQP finds the highest valid QP (smallest file) for a segment.
//
// The algorithm intentionally keeps each phase (bracketing, interpolation,
// boundary walks) explicit and inline. Edge-case handling is subtle;
// resist collapsing into generic helpers — readability trumps brevity here.
//
// The QP→VMAF relationship is empirically monotonic (lower QP = higher VMAF).
// This has held across 2+ years of production encoding; non-monotonic edge cases
// have not been observed in practice.
func searchSegmentQP(ctx context.Context, scb QPSearchCallbacks, config QPSearchConfig,
	segment int, segmentPath string, videoTrack *ffmpeg.FFProbeBinaryStream) (
	finalQP int, nbAttempts int, bestEffort bool, testedQPs []int, err error) {
	// Keep track of tested QPs
	qpMin, qpMax, found := ffmpeg.GetEncoderQPRange(config.Encoder)
	if !found {
		err = fmt.Errorf("failed to get QP range for encoder %s", config.Encoder)
		return
	}
	testedQPs = make([]int, 0, qpMax-qpMin+1) // ordered
	results := make(map[int]ffmpeg.VMAFStats, qpMax-qpMin+1)
	defer func() {
		scb.Debug("QPs tested: %+v", testedQPs)
	}()
	// Search loop
	var (
		candidateQP                                          int
		alreadyComputed, bestValidTested, firstInvalidTested bool
		vmafStats                                            ffmpeg.VMAFStats
	)
	bestValid := qpMin
	firstInvalid := qpMax
	mean, stddev := config.StatsCache.GetMeanStdDev()
	for {
		// Find a candidate
		_, bestValidTested = results[bestValid]
		_, firstInvalidTested = results[firstInvalid]
		if len(testedQPs) == 0 {
			// Step 1: test the mean as the starting point to determine the search direction.
			// If stats are out of the encoder range, the encode fails fast. Rotten data is
			// caught cheaply — no need to defensively clamp.
			candidateQP = mean
			scb.Debug("Searching for QP in range [%d, %d] with %d as first candidate", bestValid, firstInvalid, candidateQP)
		} else if !(bestValidTested && firstInvalidTested) {
			// Step 2: close the range. A valid result raises bestValid; an invalid one lowers firstInvalid.
			// This leaves one bound at its original extreme, signaling which direction to search.
			// Step from the mean in stddev increments until we bracket the threshold.
			// Once both sides are known, interpolation walks from invalid toward valid to find
			// the highest valid QP — the one that yields the smallest file.
			if bestValid == qpMin {
				if candidateQP = mean - len(results)*stddev; candidateQP < qpMin {
					if _, alreadyComputed = results[qpMin]; alreadyComputed {
						finalQP = qpMin
						bestEffort = true
						return
					}
					candidateQP = qpMin
				}
			} else if firstInvalid == qpMax {
				if candidateQP = mean + len(results)*stddev; candidateQP > qpMax {
					if _, alreadyComputed = results[qpMax]; alreadyComputed {
						finalQP = qpMax
						return
					}
					candidateQP = qpMax
				}
			} else {
				// should not happen
				err = fmt.Errorf("invalid devstdinterpol state: bestValidTested=%t (%d), firstInvalidTested=%t (%d)", bestValidTested, bestValid, firstInvalidTested, firstInvalid)
				return
			}
		} else {
			// Step 3: the range is closed — narrow it with interpolation.
			if candidateQP, err = interpolateCandidate(scb, config, bestValid, firstInvalid, qpMin, qpMax, results); err != nil {
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
		scb.OnSegmentNewCandidate(candidateQP)
		if vmafStats, err = segmentQP(ctx, scb, config, segmentPath, segment, candidateQP, videoTrack); err != nil {
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
	bestValid, firstInvalid, encoderQPMin, encoderQPMax int, existingResults map[int]ffmpeg.VMAFStats) (
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
		candidateResults ffmpeg.VMAFStats
		exists           bool
	)
	for candidateQP = firstInvalid; candidateQP > bestValid; candidateQP-- {
		if candidateResults, exists = existingResults[candidateQP]; !exists {
			if candidateResults, err = predicator.Predict(candidateQP); err != nil {
				err = fmt.Errorf("failed to predict QP %d (within %d-%d): %w", candidateQP, bestValid, firstInvalid, err)
				return
			}
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
	input string, segment, qp int, videoTrack *ffmpeg.FFProbeBinaryStream) (
	vmafStats ffmpeg.VMAFStats, err error) {
	output := filepath.Join(config.WorkingDir, fmt.Sprintf(segEncodedOutputFormat, segment, qp))
	// Encode
	if err = encodeQP(ctx, scb, config, input, output, qp, videoTrack.NbReadFrames); err != nil {
		err = fmt.Errorf("failed to encode segment: %w", err)
		return
	}
	// Compute VMAF
	vmafReportPath := output + "_vmaf.json"
	frameRate := videoTrack.RFrameRate
	ultraHD := videoTrack.Height >= ffmpeg.UltraHDHeight
	if vmafStats, err = computeVMAF(ctx, output, input, vmafReportPath, frameRate, videoTrack.NbReadFrames, ultraHD, false, debug); err != nil {
		err = fmt.Errorf("failed to compute VMAF for segment: %w", err)
		return
	}
	scb.Debug("Segment %d: QP %d: VMAF results:\n%s", segment, qp, vmafStats)
	return
}

func encodeQP(ctx context.Context, scb QPSearchCallbacks, config QPSearchConfig,
	input, output string, qp, totalFrames int) (err error) {
	scb.QPSearchCallbackEncodeStart(totalFrames)
	var encodeDuration time.Duration
	defer func() {
		scb.QPSearchCallbackEncodeStop()
	}()
	// Execute the requested encoder
	start := time.Now()
	switch config.Encoder {
	case ffmpeg.HEVCEncoderLibx265:
		err = ffmpeg.HEVCLibx265Encode(ctx, ffmpeg.HEVCLibx265EncodeConfig{
			Input:          input,
			Preset:         ffmpeg.Libx265PresetSlow,
			Quantization:   qp,
			OutputFilePath: output,
			Debug: func(msg string) {
				scb.Debug(msg)
			},
			RuntimeError:      scb.Error,
			FFMPEGStatsReport: scb.QPSearchCallbackEncodeProgress,
		})
	default:
		return fmt.Errorf("unsupported encoder: %q", string(config.Encoder))
	}
	encodeDuration = time.Since(start)
	// Done
	if err == nil {
		scb.Debug("Segment encoded in %s", encodeDuration.Round(time.Second))
	}
	return
}
