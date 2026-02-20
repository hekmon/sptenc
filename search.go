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

	"github.com/hekmon/cunits/v3"
	"github.com/hekmon/ffmpegutils"
	"github.com/hekmon/liveprogress/v2"
)

type QPStats struct {
	Minimum        int
	Maximum        int
	GlobalWeighted float64
}

func findAllGOPQP(ctx context.Context, dir string, nbGOP, meanAvg, stdDevAvg int, globalDuration time.Duration, auditor, auditorAlt *VMAFChecker, convert10bits bool) (results []int, stats QPStats, err error) {
	// Prepare
	var (
		GOPDuration                                   time.Duration
		GOPFrames, totalGOPFrames, totalEncodedFrames int
		GOPQP, QPWeights                              int
		GOPSize, allGOPSize                           cunits.Bits
		totalNbattempts, GOPNbattempts                int
		bestEffort, alternateVMAF                     bool
		nbBestEfforts, nbAlternateVMAF                int
	)
	stats.Minimum = ffmpegutils.QPMaximum + 1
	stats.Maximum = ffmpegutils.QPMinimum - 1
	results = make([]int, nbGOP)
	bypass := liveprogress.Bypass()
	// Live progress
	var GOPDone int
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
			return fmt.Sprintf(" left | %d/%d GOP done | %s",
				GOPDone, nbGOP, allGOPSize,
			)
		}),
	)
	defer liveprogress.RemoveBar(bar)
	// Go
	start := time.Now()
	for GOP := range nbGOP {
		if *debug {
			fmt.Fprintf(bypass, "GOP %d: Search for the right QP\n", GOP)
		}
		if GOPQP, GOPFrames, GOPNbattempts, bestEffort, alternateVMAF, GOPDuration, err = findGOPQP(ctx, dir, GOP, meanAvg, stdDevAvg, auditor, auditorAlt, convert10bits); err != nil {
			err = fmt.Errorf("failed to find the right encoding QP GOP %d: %w", GOP, err)
			return
		}
		results[GOP] = GOPQP
		totalGOPFrames += GOPFrames
		totalEncodedFrames += GOPFrames * GOPNbattempts
		fmt.Fprintf(bypass, "GOP %d: QP %d selected for this GOP of %d frames (%d attempts)\n", GOP, GOPQP, GOPFrames, GOPNbattempts)
		// Compute stats
		if GOPQP < stats.Minimum {
			stats.Minimum = GOPQP
		}
		if GOPQP > stats.Maximum {
			stats.Maximum = GOPQP
		}
		QPWeights += GOPQP * GOPFrames
		if GOPSize, err = getFileSize(filepath.Join(dir, fmt.Sprintf(ffmpegutils.SegEncodedOutputFormat, GOP, GOPQP))); err != nil {
			err = fmt.Errorf("failed to get the size of GOP %d: %w", GOP, err)
			return
		}
		allGOPSize += GOPSize
		totalNbattempts += GOPNbattempts
		if bestEffort {
			nbBestEfforts++
		}
		if alternateVMAF {
			nbAlternateVMAF++
		}
		// Update live progress
		bar.CurrentAdd(uint64(GOPDuration))
		GOPDone++
	}
	duration := time.Since(start)
	// Done, print and save stats
	fmt.Fprintf(bypass, "GOP QPs: %+v\n", results)
	fmt.Fprintf(bypass, "%d encoding attempts (for a total of %d encoded frames) were necessary to encode %d GOP (containing %d frames) to their optimal QP.\n",
		totalNbattempts, totalEncodedFrames, nbGOP, totalGOPFrames)
	fmt.Fprintf(bypass, "Attempts ratio: x%02f\n", float64(totalNbattempts)/float64(nbGOP))
	fmt.Fprintf(bypass, "Frames ratio: x%02f\n", float64(totalEncodedFrames)/float64(totalGOPFrames))
	gopqpmean, gopqpstddev, err := previousRuns.AddRun(results)
	if err != nil {
		fmt.Fprintf(bypass, "failed to save mean and stddev of QPs for next run: %s\n", err)
		err = nil // do not return, not critical
	}
	fmt.Fprintf(bypass, "GOP QP mean is %s with a standard deviation of %s.\n", strconv.FormatFloat(gopqpmean, 'f', -1, 64), strconv.FormatFloat(gopqpstddev, 'f', -1, 64))
	stats.GlobalWeighted = float64(QPWeights) / float64(totalGOPFrames)
	fmt.Fprintf(bypass, "Weighted global QP is %s.\n",
		strconv.FormatFloat(stats.GlobalWeighted, 'f', -1, 64),
	)
	if nbBestEfforts > 0 {
		fmt.Fprintf(bypass, "WARNING: %d GOP were encoded with best effort, stopping at QP 0 but not validating VMAF config. Please check the logs.\n",
			nbBestEfforts)
	}
	if nbAlternateVMAF > 0 {
		fmt.Fprintf(bypass, "INFO: %d GOP were encoded with alternate VMAF config, stopping at QP 0 but not validating VMAF config. Please check the logs.\n",
			nbAlternateVMAF)
	}
	fmt.Fprintf(bypass, "GOP encoding QP search done in %s.\n", duration.Round(time.Second))
	return
}

func findGOPQP(ctx context.Context, dir string, GOP, meanAvg, stdDevAvg int, auditor, auditorAlt *VMAFChecker, convert10bits bool) (
	finalQP, GOPFrames, nbAttempts int, bestEffort, alternateVMAF bool, duration time.Duration, err error) {
	// Init
	bypass := liveprogress.Bypass()
	// Prepare
	input := filepath.Join(dir, fmt.Sprintf(ffmpegutils.SegOutputFormat, GOP))
	GOPInfos, err := getStreamsInfosCF(ctx, input, false)
	if err != nil {
		err = fmt.Errorf("failed to get streams infos: %w", err)
		return
	}
	duration = GOPInfos.Format.Duration
	videoTrack := GOPInfos.VideoTrack()
	// Verify output files frames count when done
	defer func() {
		if err != nil {
			// if we exit with an error, no need to check that everything is fine
			return
		}
		var finalGOPInfos ffmpegutils.FFProbeStats
		if finalGOPInfos, err = getStreamsInfosCF(ctx, filepath.Join(dir, fmt.Sprintf(ffmpegutils.SegEncodedOutputFormat, GOP, finalQP)), false); err != nil {
			err = fmt.Errorf("failed to get streams infos of final GOP: %w", err)
			return
		}
		GOPFrames = finalGOPInfos.VideoTrack().NbReadFrames
		if GOPFrames != videoTrack.NbReadFrames {
			err = fmt.Errorf("final GOP has %d frames instead of %d", GOPFrames, videoTrack.NbReadFrames)
			return
		}
		if *debug {
			fmt.Fprintf(bypass, "Final GOP has %d frames, as original GOP.\n", GOPFrames)
		}
	}()
	// Search
	results := make(map[int]ffmpegutils.VMAFStats, ffmpegutils.QPMaximum-ffmpegutils.QPMinimum+1)
	if !*keep {
		// Delete invalid QPs once finished
		defer func() {
			for testedQP := range results {
				if testedQP != finalQP {
					if err := os.Remove(filepath.Join(dir, fmt.Sprintf(ffmpegutils.SegEncodedOutputFormat, GOP, testedQP))); err != nil {
						fmt.Fprintf(bypass, "Failed to remove %q: %s\n", filepath.Join(dir, fmt.Sprintf(ffmpegutils.SegEncodedOutputFormat, GOP, testedQP)), err)
					}
				}
			}
		}()
	}
	if finalQP, nbAttempts, bestEffort, err = searchGOPQP(ctx, input, dir, GOP, meanAvg, stdDevAvg, auditor, videoTrack, convert10bits, results); err != nil {
		err = fmt.Errorf("failed to search GOP QP: %w", err)
		return
	}
	if bestEffort {
		if auditorAlt != nil {
			fmt.Fprintf(bypass, "WARNING: Impossible to validate VMAF config with lowest possible QP (highest quality), will switch to alternate validator:\n%s", results[ffmpegutils.QPMinimum])
			var nbAttemptsAlt int
			if finalQP, nbAttemptsAlt, bestEffort, err = searchGOPQP(ctx, input, dir, GOP, meanAvg, stdDevAvg, auditorAlt, videoTrack, convert10bits, results); err != nil {
				err = fmt.Errorf("failed to search GOP QP with alternate validator: %w", err)
				return
			}
			if bestEffort {
				fmt.Fprintf(bypass, "WARNING: Still impossible to validate with alternate VMAF config, keeping the lowest possible QP (highest quality) anyway.\n")
			} else {
				fmt.Fprintf(bypass, "SUCCESS: Validated VMAF alternate config:\n%s", results[finalQP])
				alternateVMAF = true
			}
			nbAttempts += nbAttemptsAlt
		} else {
			fmt.Fprintf(bypass, "WARNING: Impossible to validate VMAF config with lowest possible QP (highest quality), keeping it anyway:\n%s", results[ffmpegutils.QPMinimum])
		}
	}
	return
}

func searchGOPQP(ctx context.Context, input, dir string, GOP, meanAvg, stdDevAvg int, auditor *VMAFChecker, videoTrack *ffmpegutils.FFProbeBinaryStream,
	convert10bits bool, results map[int]ffmpegutils.VMAFStats) (finalQP int, nbattempts int, bestEffort bool, err error) {
	bypass := liveprogress.Bypass()
	testedQPs := make([]int, 0, ffmpegutils.QPMaximum-ffmpegutils.QPMinimum+1)
	var testedQPsAccess sync.Mutex
	defer func() {
		if *debug {
			fmt.Fprintf(bypass, "QPs tested: %+v\n", testedQPs)
		}
	}()
	statusLine := liveprogress.AddCustomLine(func() string {
		testedQPsAccess.Lock()
		testedQPStr := make([]string, len(testedQPs))
		for i, qp := range testedQPs {
			testedQPStr[i] = strconv.Itoa(qp)
		}
		testedQPsAccess.Unlock()
		return fmt.Sprintf("       GOP | #%d - Searching for QP: %s", GOP, strings.Join(testedQPStr, ","))
	})
	defer liveprogress.RemoveCustomLine(statusLine)
	var (
		candidateQP                                      int
		alreadyComputed, minComputed, maxComputed, found bool
		GOPQPOutput, report                              string
		vmafStats                                        ffmpegutils.VMAFStats
	)
	min := ffmpegutils.QPMinimum
	max := ffmpegutils.QPMaximum
	defer func() {
		// Just in case, to be sure
		if finalQP == ffmpegutils.QPMinimum && !auditor.Validate(results[ffmpegutils.QPMinimum]) {
			bestEffort = true
		}
	}()
	for {
		_, minComputed = results[min]
		_, maxComputed = results[max]
		if len(testedQPs) == 0 {
			// quick search
			candidateQP = meanAvg
			if *debug {
				fmt.Fprintf(bypass, "Searching for QP in range [%d, %d] with candidate %d\n", min, max, candidateQP)
			}
		} else if !(minComputed && maxComputed) {
			// Use standard deviation avg to slowy expand range while maximizing our chances to find the best QP
			if min == ffmpegutils.QPMinimum {
				// Starting at round 2, if we have still a lowest minimum, use increment of the standard deviation average toward minimum
				// to maximize the chance to find a better candidate while still keeping the range as small as possible
				if candidateQP = meanAvg - len(results)*stdDevAvg; candidateQP < ffmpegutils.QPMinimum {
					if _, alreadyComputed = results[ffmpegutils.QPMinimum]; alreadyComputed {
						finalQP = ffmpegutils.QPMinimum
						bestEffort = true
						return
					}
					candidateQP = ffmpegutils.QPMinimum
				}
			} else if max == ffmpegutils.QPMaximum {
				// Starting at round 2, if we have still a highest maximum, use increment of the standard deviation average toward maximum
				// to maximize the chance to find a better candidate while still keeping the range as small as possible
				if candidateQP = meanAvg + len(results)*stdDevAvg; candidateQP > ffmpegutils.QPMaximum {
					if _, alreadyComputed = results[ffmpegutils.QPMaximum]; alreadyComputed {
						finalQP = ffmpegutils.QPMaximum
						return
					}
					candidateQP = ffmpegutils.QPMaximum
				}
			} else {
				// should not happen
				err = fmt.Errorf("invalid devstdinterpol state: minComputed=%t (%d), maxComputed=%t (%d)", minComputed, min, maxComputed, max)
				return
			}
		} else {
			// switch to interpolation once we have a closed range
			if candidateQP, err = FindCandidate(min, max, results, auditor); err != nil {
				err = fmt.Errorf("failed to find candidate: %w", err)
				return
			}
			// Handle predicted candidate
			if vmafStats, found = results[candidateQP]; found {
				if *debug {
					fmt.Fprintf(bypass, "Predicted candidate %d already computed (valid: %t)\n", candidateQP, auditor.Validate(vmafStats))
				}
				// we already computed this candidate, let's think this thru
				if auditor.Validate(vmafStats) {
					// Are we sure that next higher candidate does not validate ?
					if candidateQP == ffmpegutils.QPMaximum {
						finalQP = candidateQP
						return
					}
					for i := candidateQP + 1; i <= ffmpegutils.QPMaximum; i++ {
						if vmafStats, found = results[i]; found {
							if i == ffmpegutils.QPMaximum {
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
							if *debug {
								fmt.Fprintf(bypass, "Looking up: candidate %d already computed (valid: %t)\n", i, true)
							}
						} else {
							// we found a candidate for smaller size that we did not compute yet
							candidateQP = i
							break
						}
					}
				} else {
					// else, this candidate does not validate
					if candidateQP == ffmpegutils.QPMinimum {
						// but we can not make it better
						finalQP = candidateQP
						bestEffort = true
						return
					}
					// Let's take a candidate with better quality
					for i := candidateQP - 1; i >= ffmpegutils.QPMinimum; i-- {
						if vmafStats, found = results[i]; found {
							if i == ffmpegutils.QPMinimum {
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
							if *debug {
								fmt.Fprintf(bypass, "Looking down: candidate %d already computed (valid: %t)\n", i, false)
							}
						} else {
							// we found a candidate for better quality that we did not compute yet
							candidateQP = i
							break
						}
					}
				}
			} else if *debug {
				fmt.Fprintf(bypass, "Predicted candidate %d selected for computation\n", candidateQP)
			}
		}
		// Test candidate
		testedQPsAccess.Lock()
		testedQPs = append(testedQPs, candidateQP)
		testedQPsAccess.Unlock()
		if vmafStats, alreadyComputed = results[candidateQP]; !alreadyComputed {
			// Encode with candidateQP
			GOPQPOutput = filepath.Join(dir, fmt.Sprintf(ffmpegutils.SegEncodedOutputFormat, GOP, candidateQP))
			report = GOPQPOutput + "_vmaf.json"
			ultraHD := videoTrack.Height >= ffmpegutils.UltraHDHeight
			if vmafStats, err = GOPQP(ctx, input, GOPQPOutput, report, videoTrack.RFrameRate, videoTrack.NbReadFrames, GOP, candidateQP, ultraHD, convert10bits); err != nil {
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

func FindCandidate(min, max int, existingResults map[int]ffmpegutils.VMAFStats, auditor *VMAFChecker) (candidateQP int, err error) {
	predicator, err := NewPredicator(existingResults)
	if err != nil {
		err = fmt.Errorf("failed to create predicator: %w", err)
		return
	}
	// Range from maximum QP (lower quality) to minimum QP (higher quality) to find the first (theorical or real) candidate that validate
	var (
		candidateResults ffmpegutils.VMAFStats
		exists           bool
	)
	for candidateQP = max; candidateQP > min; candidateQP-- {
		if candidateResults, exists = existingResults[candidateQP]; !exists {
			if candidateResults, err = predicator.Predict(candidateQP); err != nil {
				err = fmt.Errorf("failed to predict QP %d (within %d-%d): %w", candidateQP, min, max, err)
				return
			}
			// if *debug {
			// 	fmt.Fprintf(bypass, "Predicted candidate %d VMAF results:\n%s", candidateQP, candidateResults)
			// }
		}
		if auditor.Validate(candidateResults) {
			return
		}
	}
	// return min
	if *debug {
		fmt.Fprintf(liveprogress.Bypass(), "No candidate found in range %d-%d, returning %d\n", min, max, candidateQP)
	}
	return
}

func GOPQP(ctx context.Context, input, output, vmafReportPath, frameRate string, totalFrames, GOPID, qp int, ultraHD, convert10bits bool) (vmafStats ffmpegutils.VMAFStats, err error) {
	// Encode
	if err = encodeQP(ctx, input, output, totalFrames, qp, convert10bits); err != nil {
		err = fmt.Errorf("failed to encode GOP: %w", err)
		return
	}
	// Compute VMAF
	if vmafStats, err = computeVMAF(ctx, output, input, vmafReportPath, frameRate, totalFrames, ultraHD, false); err != nil {
		err = fmt.Errorf("failed to compute VMAF for GOP: %w", err)
		return
	}
	if *debug {
		fmt.Fprintf(liveprogress.Bypass(), "GOP %d: QP %d: VMAF results:\n%s", GOPID, qp, vmafStats)
	}
	return
}
