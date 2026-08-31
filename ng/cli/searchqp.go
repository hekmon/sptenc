package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hekmon/sptenc/ng/core"
	"github.com/hekmon/sptenc/ng/ffmpeg"

	"github.com/hekmon/cunits/v3"
	"github.com/hekmon/liveprogress/v2"
)

const (
	segEncodedOutputFormat = "seg_%d_qp%d.mkv"
)

type QPStats struct {
	Minimum        int
	Maximum        int
	GlobalWeighted float64
}

func findAllSegmentsQP(ctx context.Context, workingDir string, segmentPaths []string, meanAvg, stdDevAvg int,
	globalDuration time.Duration, auditor core.VMAFChecker, statsCache *core.StatsCacheHistory, encoder ffmpeg.Encoder, debug bool) (
	results []int, stats QPStats, err error) {
	// Prepare
	var (
		segmentDuration                                       time.Duration
		segmentFrames, totalSegmentFrames, totalEncodedFrames int
		segmentQP, segmentWeights                             int
		segmentSize, allSegmentSize                           cunits.Bits
		totalNbattempts, segmentNbAttempts                    int
		bestEffort                                            bool
		nbBestEfforts                                         int
	)
	encoderQPMin, encoderQPMax, found := ffmpeg.GetEncoderQPRange(encoder)
	if !found {
		err = fmt.Errorf("unable to find QP range for encoder %q", encoder)
		return
	}
	stats.Minimum = encoderQPMax + 1
	stats.Maximum = encoderQPMin - 1
	results = make([]int, len(segmentPaths))
	bypass := liveprogress.Bypass()
	// Live progress
	var segmentsDone int
	bar := liveprogress.SetMainLineAsBar(
		liveprogress.WithTotal(uint64(globalDuration)),
		liveprogress.WithMultiplyRunes(),
		// liveprogress.WithWidth(barsWidth),
		liveprogress.WithSameAutoSizeInternalPadding(true, false),
		liveprogress.WithPrependDecorator(func(bar *liveprogress.Bar) string {
			return "    Global | "
		}),
		liveprogress.WithPrependTimeElapsed(liveprogress.BaseStyle()),
		liveprogress.WithAppendPercent(liveprogress.BaseStyle()),
		liveprogress.WithAppendTimeRemaining(liveprogress.BaseStyle()),
		liveprogress.WithAppendDecorator(func(bar *liveprogress.Bar) string {
			return fmt.Sprintf(" left | %d/%d segments done | %s",
				segmentsDone, len(segmentPaths), allSegmentSize,
			)
		}),
	)
	defer liveprogress.RemoveBar(bar)
	// Go
	start := time.Now()
	for segment := range len(segmentPaths) {
		if debug {
			fmt.Fprintf(bypass, "DEBUG: Segment %d: Search for the right QP\n", segment)
		}
		inputPath := segmentPaths[segment]
		if segmentQP, segmentFrames, segmentNbAttempts, bestEffort, segmentDuration, err = findSegmentQP(
			ctx, workingDir, inputPath, segment, meanAvg, stdDevAvg, encoderQPMin, encoderQPMax, auditor, debug); err != nil {
			err = fmt.Errorf("failed to find the right encoding QP for segment %d: %w", segment, err)
			return
		}
		results[segment] = segmentQP
		totalSegmentFrames += segmentFrames
		totalEncodedFrames += segmentFrames * segmentNbAttempts
		fmt.Fprintf(bypass, "Segment %d: QP %d selected for this segment of %d frames (%d attempts)\n",
			segment, segmentQP, segmentFrames, segmentNbAttempts,
		)
		// Compute stats
		if segmentQP < stats.Minimum {
			stats.Minimum = segmentQP
		}
		if segmentQP > stats.Maximum {
			stats.Maximum = segmentQP
		}
		segmentWeights += segmentQP * segmentFrames
		if segmentSize, err = getFileSize(filepath.Join(workingDir, fmt.Sprintf(segEncodedOutputFormat, segment, segmentQP))); err != nil {
			err = fmt.Errorf("failed to get the size of segment %d: %w", segment, err)
			return
		}
		allSegmentSize += segmentSize
		totalNbattempts += segmentNbAttempts
		if bestEffort {
			nbBestEfforts++
		}
		// Update live progress
		bar.CurrentAdd(uint64(segmentDuration))
		segmentsDone++
	}
	duration := time.Since(start)
	// Done, print and save stats
	fmt.Fprintf(bypass, "Segments QPs: %+v\n", results)
	fmt.Fprintf(bypass, "%d encoding attempts (for a total of %d encoded frames) were necessary to encode %d segments (containing %d frames) to their optimal QP.\n",
		totalNbattempts, totalEncodedFrames, len(segmentPaths), totalSegmentFrames)
	fmt.Fprintf(bypass, "Attempts ratio: x%02f\n", float64(totalNbattempts)/float64(len(segmentPaths)))
	fmt.Fprintf(bypass, "Frames ratio: x%02f\n", float64(totalEncodedFrames)/float64(totalSegmentFrames))
	segmentQPmean, segmentQPstddev := statsCache.AddRun(results)
	fmt.Fprintf(bypass, "Segment QP mean is %s with a standard deviation of %s.\n",
		strconv.FormatFloat(segmentQPmean, 'f', -1, 64), strconv.FormatFloat(segmentQPstddev, 'f', -1, 64),
	)
	stats.GlobalWeighted = float64(segmentWeights) / float64(totalSegmentFrames)
	fmt.Fprintf(bypass, "Weighted global QP is %s.\n",
		strconv.FormatFloat(stats.GlobalWeighted, 'f', -1, 64),
	)
	if nbBestEfforts > 0 {
		fmt.Fprintf(bypass, "WARNING: %d segments were encoded with best effort, stopping at QP 0 but not validating VMAF config. Please check the logs.\n",
			nbBestEfforts)
	}
	fmt.Fprintf(bypass, "Segments encoding QP search done in %s.\n", duration.Round(time.Second))
	return
}

func findSegmentQP(ctx context.Context, workingDir string, inputPath string, segment, meanAvg, stdDevAvg, encoderQPMin, encoderQPMax int, auditor core.VMAFChecker, debug bool) (
	finalQP, segmentFrames, nbAttempts int, bestEffort bool, duration time.Duration, err error) {
	// Init
	bypass := liveprogress.Bypass()
	// Prepare
	input := inputPath
	segmentInfos, err := getStreamsInfosCF(ctx, input, debug)
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
			err = fmt.Errorf("Segment %d: frame count is 0 or negative (Nb(Read)Frames: %d, duration: %s, frameRate: %s). Cannot proceed without valid frame count",
				segment, totalFrames, duration, videoTrack.RFrameRate)
			return
		}
	}
	// Verify output files frames count when done
	defer func() {
		if err != nil {
			// if we exit with an error, no need to check that everything is fine
			return
		}
		var finalSegmentInfos ffmpeg.FFProbeStats
		if finalSegmentInfos, err = getStreamsInfosCF(ctx, filepath.Join(workingDir, fmt.Sprintf(segEncodedOutputFormat, segment, finalQP)), debug); err != nil {
			err = fmt.Errorf("failed to get streams infos of final segment: %w", err)
			return
		}
		segmentFrames = finalSegmentInfos.VideoTrack().NbReadFrames
		if segmentFrames != totalFrames {
			err = fmt.Errorf("final segment has %d frames instead of %d", segmentFrames, totalFrames)
			return
		}
		if debug {
			fmt.Fprintf(bypass, "DEBUG: Final segment has %d frames, as original GOP.\n", segmentFrames)
		}
	}()
	// Search
	results := make(map[int]ffmpeg.VMAFStats, encoderQPMax-encoderQPMin+1)
	if !debug {
		// Delete invalid QPs once finished
		defer func() {
			for testedQP := range results {
				if testedQP != finalQP {
					if err := os.Remove(filepath.Join(workingDir, fmt.Sprintf(segEncodedOutputFormat, segment, testedQP))); err != nil {
						fmt.Fprintf(bypass, "Failed to remove %q: %s\n", filepath.Join(workingDir, fmt.Sprintf(segEncodedOutputFormat, segment, testedQP)), err)
					}
				}
			}
		}()
	}
	if finalQP, nbAttempts, bestEffort, err = searchSegmentQP(ctx, input, workingDir, segment, totalFrames, meanAvg, stdDevAvg,
		encoderQPMin, encoderQPMax, auditor, videoTrack, results, debug); err != nil {
		err = fmt.Errorf("failed to search GOP QP: %w", err)
		return
	}
	if bestEffort {
		fmt.Fprintf(bypass, "WARNING: Impossible to validate VMAF config with lowest possible QP (highest quality), keeping it anyway:\n%s", results[encoderQPMin])
	}
	return
}

func searchSegmentQP(ctx context.Context, input, workingDir string, segment, totalFrames, meanAvg, stdDevAvg, encoderQPMin, encoderQPMax int,
	auditor core.VMAFChecker, videoTrack *ffmpeg.FFProbeBinaryStream, results map[int]ffmpeg.VMAFStats, debug bool) (
	finalQP int, nbattempts int, bestEffort bool, err error) {
	bypass := liveprogress.Bypass()
	testedQPs := make([]int, 0, encoderQPMax-encoderQPMin+1)
	var testedQPsAccess sync.Mutex
	defer func() {
		if debug {
			fmt.Fprintf(bypass, "DEBUG: QPs tested: %+v\n", testedQPs)
		}
	}()
	statusLine := liveprogress.AddCustomLine(func() string {
		testedQPsAccess.Lock()
		testedQPStr := make([]string, len(testedQPs))
		for i, qp := range testedQPs {
			testedQPStr[i] = strconv.Itoa(qp)
		}
		testedQPsAccess.Unlock()
		return fmt.Sprintf("   Segment | #%d - Searching for QP: %s", segment, strings.Join(testedQPStr, ","))
	})
	defer liveprogress.RemoveCustomLine(statusLine)
	var (
		candidateQP                                      int
		alreadyComputed, minComputed, maxComputed, found bool
		segmentQPOutput, report                          string
		vmafStats                                        ffmpeg.VMAFStats
	)
	min := encoderQPMin
	max := encoderQPMax
	defer func() {
		// Just in case, to be sure
		if finalQP == encoderQPMin && !auditor.Validate(results[encoderQPMin]) {
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

func findCandidate(min, max, encoderQPMin, encoderQPMax int, existingResults map[int]ffmpeg.VMAFStats, auditor core.VMAFChecker, debug bool) (candidateQP int, err error) {
	debugPrint := func(msg string) {
		if debug {
			fmt.Fprintf(liveprogress.Bypass(), "DEBUG: %s\n", msg)
		}
	}
	predicator, err := core.NewPredicator(existingResults, encoderQPMin, encoderQPMax, debugPrint)
	if err != nil {
		err = fmt.Errorf("failed to create predicator: %w", err)
		return
	}
	// Range from maximum QP (lower quality) to minimum QP (higher quality) to find the first (theorical or real) candidate that validate
	var (
		candidateResults ffmpeg.VMAFStats
		exists           bool
	)
	for candidateQP = max; candidateQP > min; candidateQP-- {
		if candidateResults, exists = existingResults[candidateQP]; !exists {
			if candidateResults, err = predicator.Predict(candidateQP); err != nil {
				err = fmt.Errorf("failed to predict QP %d (within %d-%d): %w", candidateQP, min, max, err)
				return
			}
			// if debug {
			// 	fmt.Fprintf(liveprogress.Bypass(), "Predicted candidate %d VMAF results:\n%s", candidateQP, candidateResults)
			// }
		}
		if auditor.Validate(candidateResults) {
			return
		}
	}
	// return min
	if debug {
		fmt.Fprintf(liveprogress.Bypass(), "DEBUG: No candidate found in range %d-%d, returning %d\n", min, max, candidateQP)
	}
	return
}

func segmentQP(ctx context.Context, input, output, vmafReportPath, frameRate string, totalFrames, segment, qp int, ultraHD, debug bool) (vmafStats ffmpeg.VMAFStats, err error) {
	// Encode
	if err = encodeQP(ctx, input, output, totalFrames, qp, debug); err != nil {
		err = fmt.Errorf("failed to encode segment: %w", err)
		return
	}
	// Compute VMAF
	if vmafStats, err = computeVMAF(ctx, output, input, vmafReportPath, frameRate, totalFrames, ultraHD, false); err != nil {
		err = fmt.Errorf("failed to compute VMAF for segment: %w", err)
		return
	}
	if debug {
		fmt.Fprintf(liveprogress.Bypass(), "DEBUG: Segment %d: QP %d: VMAF results:\n%s", segment, qp, vmafStats)
	}
	return
}
