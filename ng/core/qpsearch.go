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
	QPSearchCallbacksLogging
	// Segment lifecycle
	OnSegmentStart(segmentIndex int, segmentPath string)
	OnSegmentNewCandidate(qpCandidate int)
	QPSearchCallbacksAnalysis
	OnSegmentDone(segmentFinalQP, segmentFrames, segmentNbAttempts int, currentTotalDuration time.Duration, currentTotalSize cunits.Bits)
}

type QPSearchCallbacksLogging interface {
	Debug(format string, a ...any)
	Warning(format string, a ...any)
	Error(err error)
}

type QPSearchCallbacksAnalysis interface {
	OnSegmentAnalysisStart(filePath string, fileSize int64) // fileSize in bytes
	OnSegmentAnalysisProgress(bytesRead int)                // additional bytes read since last call
	OnSegmentAnalysisStop()
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
	segmentInfos, err := getStreamsInfosCF(ctx, segmentPath, scb, scb)
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
		if finalSegmentInfos, err = getStreamsInfosCF(ctx, finalQPSegmentPath, scb, scb); err != nil {
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
	qpMin, qpMax, found := ffmpeg.GetEncoderQPRange(config.Encoder)
	if !found {
		err = fmt.Errorf("failed to get QP range for encoder %s", config.Encoder)
		return
	}
	results := make(map[int]ffmpeg.VMAFStats, qpMax-qpMin+1)
	if !config.KeepInvalidQP {
		// Delete invalid QPs once finished
		defer func() {
			for testedQP := range results {
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
	if finalQP, nbAttempts, bestEffort, err = searchSegmentQP(ctx, scb, config, segment, segmentPath, videoTrack, results); err != nil {
		err = fmt.Errorf("failed to search segment QP: %w", err)
		return
	}
	if bestEffort {
		scb.Warning("Impossible to validate VMAF config with lowest possible QP (highest quality), keeping it anyway:\n%s", results[qpMin])
	}
	return
}

func searchSegmentQP(ctx context.Context, scb QPSearchCallbacks, config QPSearchConfig,
	segment int, segmentPath string, videoTrack *ffmpeg.FFProbeBinaryStream, results map[int]ffmpeg.VMAFStats) (
	finalQP int, nbattempts int, bestEffort bool, err error) {
	// Keep track of tested QPs
	qpMin, qpMax, found := ffmpeg.GetEncoderQPRange(config.Encoder)
	if !found {
		err = fmt.Errorf("failed to get QP range for encoder %s", config.Encoder)
		return
	}
	testedQPs := make([]int, 0, qpMax-qpMin+1)
	defer func() {
		scb.Debug("QPs tested: %+v", testedQPs)
	}()
	var (
		candidateQP                               int
		alreadyComputed, minComputed, maxComputed bool
		segmentQPOutput, report                   string
		vmafStats                                 ffmpeg.VMAFStats
	)
	min := qpMin
	max := qpMax
	defer func() {
		// Just in case, to be sure
		if finalQP == qpMin && !config.Auditor.Validate(results[qpMin]) {
			bestEffort = true
		}
	}()
	for {
		_, minComputed = results[min]
		_, maxComputed = results[max]
		if len(testedQPs) == 0 {
			// quick search
			candidateQP = meanAvg
			if debug {
				fmt.Fprintf(bypass, "DEBUG: Searching for QP in range [%d, %d] with candidate %d\n", min, max, candidateQP)
			}
		} else if !(minComputed && maxComputed) {
			// Use standard deviation avg to slowy expand range while maximizing our chances to find the best QP
			if min == encoderQPMin {
				// Starting at round 2, if we have still a lowest minimum, use increment of the standard deviation average toward minimum
				// to maximize the chance to find a better candidate while still keeping the range as small as possible
				if candidateQP = meanAvg - len(results)*stdDevAvg; candidateQP < encoderQPMin {
					if _, alreadyComputed = results[encoderQPMin]; alreadyComputed {
						finalQP = encoderQPMin
						bestEffort = true
						return
					}
					candidateQP = encoderQPMin
				}
			} else if max == encoderQPMax {
				// Starting at round 2, if we have still a highest maximum, use increment of the standard deviation average toward maximum
				// to maximize the chance to find a better candidate while still keeping the range as small as possible
				if candidateQP = meanAvg + len(results)*stdDevAvg; candidateQP > encoderQPMax {
					if _, alreadyComputed = results[encoderQPMax]; alreadyComputed {
						finalQP = encoderQPMax
						return
					}
					candidateQP = encoderQPMax
				}
			} else {
				// should not happen
				err = fmt.Errorf("invalid devstdinterpol state: minComputed=%t (%d), maxComputed=%t (%d)", minComputed, min, maxComputed, max)
				return
			}
		} else {
			// switch to interpolation once we have a closed range
			if candidateQP, err = findCandidate(min, max, encoderQPMin, encoderQPMax, results, auditor, debug); err != nil {
				err = fmt.Errorf("failed to find candidate: %w", err)
				return
			}
			// Handle predicted candidate
			if vmafStats, found = results[candidateQP]; found {
				if debug {
					fmt.Fprintf(bypass, "DEBUG: Predicted candidate %d already computed (valid: %t)\n", candidateQP, auditor.Validate(vmafStats))
				}
				// we already computed this candidate, let's think this thru
				if auditor.Validate(vmafStats) {
					// Are we sure that next higher candidate does not validate ?
					if candidateQP == encoderQPMax {
						finalQP = candidateQP
						return
					}
					for i := candidateQP + 1; i <= encoderQPMax; i++ {
						if vmafStats, found = results[i]; found {
							if i == encoderQPMax {
								finalQP = i
								return
							}
							// we already computed this candidate
							if !auditor.Validate(vmafStats) {
								// Invalid, previous was the last valid
								finalQP = i - 1
								return
							}
							// else continue to go up
							if debug {
								fmt.Fprintf(bypass, "DEBUG: Looking up: candidate %d already computed (valid: %t)\n", i, true)
							}
						} else {
							// we found a candidate for smaller size that we did not compute yet
							candidateQP = i
							break
						}
					}
				} else {
					// else, this candidate does not validate
					if candidateQP == encoderQPMin {
						// but we can not make it better
						finalQP = candidateQP
						bestEffort = true
						return
					}
					// Let's take a candidate with better quality
					for i := candidateQP - 1; i >= encoderQPMin; i-- {
						if vmafStats, found = results[i]; found {
							if i == encoderQPMin {
								finalQP = i
								bestEffort = true
								return
							}
							// we already computed this candidate
							if auditor.Validate(vmafStats) {
								// So if it is valid, this is the one we need as all previous are invalid
								finalQP = i
								return
							}
							// else continue to go down
							if debug {
								fmt.Fprintf(bypass, "DEBUG: Looking down: candidate %d already computed (valid: %t)\n", i, false)
							}
						} else {
							// we found a candidate for better quality that we did not compute yet
							candidateQP = i
							break
						}
					}
				}
			} else if debug {
				fmt.Fprintf(bypass, "DEBUG: Predicted candidate %d selected for computation\n", candidateQP)
			}
		}
		// Test candidate
		testedQPsAccess.Lock()
		testedQPs = append(testedQPs, candidateQP)
		testedQPsAccess.Unlock()
		if vmafStats, alreadyComputed = results[candidateQP]; !alreadyComputed {
			// Encode with candidateQP
			segmentQPOutput = filepath.Join(workingDir, fmt.Sprintf(segEncodedOutputFormat, segment, candidateQP))
			report = segmentQPOutput + "_vmaf.json"
			ultraHD := videoTrack.Height >= ffmpeg.UltraHDHeight
			if vmafStats, err = segmentQP(ctx, input, segmentQPOutput, report, videoTrack.RFrameRate, totalFrames, segment, candidateQP, ultraHD, debug); err != nil {
				err = fmt.Errorf("failed to produce QP %d: %w", candidateQP, err)
				return
			}
			nbattempts++
			results[candidateQP] = vmafStats
		} // else we might be within the second pass with the alternate auditor and have encountered an already encoded candidate during the first pass
		if auditor.Validate(vmafStats) {
			min = candidateQP
		} else {
			max = candidateQP
		}
	}
}
