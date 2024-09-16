package main

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/hekmon/cunits/v2"
	"github.com/hekmon/ffmpegutils"
	"github.com/hekmon/liveprogress/v2"
)

type QPStats struct {
	Minimum        int
	Maximum        int
	GlobalWeighted float64
}

func findAllGOPQP(dir string, nbGOP int, globalDuration time.Duration, auditor VMAFChecker, convert10bits bool) (results []int, stats QPStats, err error) {
	// Prepare
	var (
		GOPDuration               time.Duration
		GOPFrames, totalGOPFrames int
		GOPQP, QPWeights          int
		GOPSize, allGOPSize       cunits.Bits
		totalNbTries, GOPNbTries  int
		bestEffort                bool
		nbBestEfforts             int
	)
	stats.Minimum = ffmpegutils.QPMaximum + 1
	stats.Maximum = ffmpegutils.QPMinimum - 1
	results = make([]int, nbGOP)
	segmentsFrames := make([]int, nbGOP)
	bypass := liveprogress.Bypass()
	// Live progress
	var GOPDone int
	bar := liveprogress.SetMainLineAsBar(
		liveprogress.WithTotal(uint64(globalDuration)),
		liveprogress.WithLineFillRunes(),
		// liveprogress.WithWidth(barsWidth),
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
	var splitQP int
	if *smartSplit {
		splitQP = previousMediansQPs.GetIdealSplitQP()
		fmt.Fprintf(bypass, "Smart start QP set the start QP at %d\n", splitQP)
	} else {
		splitQP = (ffmpegutils.QPMaximum - ffmpegutils.QPMinimum + 1) / 2
		fmt.Fprintf(bypass, "Split QP is set to %d\n", splitQP)
	}
	for GOP := 0; GOP < nbGOP; GOP++ {
		if *debug {
			fmt.Fprintf(bypass, "GOP %d: Search for the right QP\n", GOP)
		}
		if GOPQP, GOPFrames, GOPNbTries, bestEffort, GOPDuration, err = findGOPQP(dir, GOP, splitQP, auditor, convert10bits); err != nil {
			err = fmt.Errorf("failed to find the right encoding QP GOP %d: %w", GOP, err)
			return
		}
		results[GOP] = GOPQP
		segmentsFrames[GOP] = GOPFrames
		totalGOPFrames += GOPFrames
		fmt.Fprintf(bypass, "GOP %d: QP %d selected for this GOP of %d frames (%d tries)\n", GOP, GOPQP, GOPFrames, GOPNbTries)
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
		totalNbTries += GOPNbTries
		if bestEffort {
			nbBestEfforts++
		}
		// Update live progress
		bar.CurrentAdd(uint64(GOPDuration))
		GOPDone++
	}
	duration := time.Since(start)
	// Done
	fmt.Fprintf(bypass, "GOP QPs: %+v\n", results)
	stats.GlobalWeighted = float64(QPWeights) / float64(totalGOPFrames)
	fmt.Fprintf(bypass, "Weighted global QP is %s.\n", strconv.FormatFloat(stats.GlobalWeighted, 'f', -1, 64))
	if nbBestEfforts > 0 {
		fmt.Fprintf(bypass, "WARNING: %d GOP were encoded with best effort, stopping at QP 0 but not validating VMAF config. Please check the logs.\n", nbBestEfforts)
	}
	// Stats
	fmt.Fprintf(bypass, "GOP encoding QP search done in %s.\n", duration.Round(time.Second))
	return
}

func findGOPQP(dir string, GOP, splitQP int, auditor VMAFChecker, convert10bits bool) (finalQP, GOPFrames, nbTries int, bestEffort bool, duration time.Duration, err error) {
	bypass := liveprogress.Bypass()
	previousQPs := make([]string, 0, ffmpegutils.QPMaximum-ffmpegutils.QPMinimum+1)
	defer func() {
		if *debug {
			fmt.Fprintf(bypass, "QPs tested: %s\n", strings.Join(previousQPs, ","))
		}
	}()
	statusLine := liveprogress.AddCustomLine(func() string {
		return fmt.Sprintf("       GOP | #%d - Searching for QP: %s", GOP, strings.Join(previousQPs, ","))
	})
	defer liveprogress.RemoveCustomLine(statusLine)
	// Read input segment
	input := filepath.Join(dir, fmt.Sprintf(ffmpegutils.SegOutputFormat, GOP))
	GOPInfos, err := getStreamsInfosCF(input, false)
	if err != nil {
		err = fmt.Errorf("failed to get streams infos: %w", err)
		return
	}
	duration = GOPInfos.Format.Duration
	videoTrack := GOPInfos.VideoTrack()
	totalFrames := videoTrack.NbReadFrames
	frameRate := videoTrack.RFrameRate
	ultraHD := videoTrack.Height >= ffmpegutils.UltraHDHeight
	// Verify output files frames count when done
	defer func() {
		if err != nil {
			// if we exit with an error, no need to check that everything is fine
			return
		}
		var finalGOPInfos ffmpegutils.FFProbeStats
		if finalGOPInfos, err = getStreamsInfosCF(filepath.Join(dir, fmt.Sprintf(ffmpegutils.SegEncodedOutputFormat, GOP, finalQP)), false); err != nil {
			err = fmt.Errorf("failed to get streams infos of final GOP: %w", err)
			return
		}
		GOPFrames = finalGOPInfos.VideoTrack().NbReadFrames
		if GOPFrames != totalFrames {
			err = fmt.Errorf("final GOP has %d frames instead of %d", GOPFrames, totalFrames)
			return
		}
		if *debug {
			fmt.Fprintf(bypass, "Final GOP has %d frames, as original GOP.\n", GOPFrames)
		}
	}()
	// Prepare compute
	results := make(map[int]ffmpegutils.VMAFStats, ffmpegutils.QPMaximum-ffmpegutils.QPMinimum+1)
	candidateQP := splitQP
	// Compute first point as middle QPs
	previousQPs = append(previousQPs, strconv.Itoa(candidateQP))
	GOPQPOutput := filepath.Join(dir, fmt.Sprintf(ffmpegutils.SegEncodedOutputFormat, GOP, candidateQP))
	report := GOPQPOutput + "_vmaf.json"
	var vmafStats ffmpegutils.VMAFStats
	if vmafStats, err = GOPQP(input, GOPQPOutput, report, frameRate, totalFrames, GOP, candidateQP, ultraHD, convert10bits); err != nil {
		err = fmt.Errorf("failed to produce QP %d: %w", candidateQP, err)
		return
	}
	nbTries++
	results[candidateQP] = vmafStats
	// Compute second point based on results of the middle one to define a upper or lower range and cut the total QP range in half
	var min, max int
	if auditor.Validate(vmafStats) {
		// report 26 valid, range is 26 to max
		max = ffmpegutils.QPMaximum
		min = candidateQP
		// Compute second point
		candidateQP = ffmpegutils.QPMaximum
		previousQPs = append(previousQPs, strconv.Itoa(candidateQP))
		GOPQPOutput = filepath.Join(dir, fmt.Sprintf(ffmpegutils.SegEncodedOutputFormat, GOP, candidateQP))
		report = GOPQPOutput + "_vmaf.json"
		if vmafStats, err = GOPQP(input, GOPQPOutput, report, frameRate, totalFrames, GOP, candidateQP, ultraHD, convert10bits); err != nil {
			err = fmt.Errorf("failed to produce QP %d: %w", candidateQP, err)
			return
		}
		nbTries++
		if auditor.Validate(vmafStats) {
			// we can not reduce size anymore, early exit
			finalQP = candidateQP
			return
		}
	} else {
		// report 26 is invalid, range is min to 26
		max = candidateQP
		min = ffmpegutils.QPMinimum
		// Compute second point
		candidateQP = ffmpegutils.QPMinimum
		previousQPs = append(previousQPs, strconv.Itoa(candidateQP))
		GOPQPOutput := filepath.Join(dir, fmt.Sprintf(ffmpegutils.SegEncodedOutputFormat, GOP, candidateQP))
		report := GOPQPOutput + "_vmaf.json"
		if vmafStats, err = GOPQP(input, GOPQPOutput, report, frameRate, totalFrames, GOP, candidateQP, ultraHD, convert10bits); err != nil {
			err = fmt.Errorf("failed to produce QP %d: %w", candidateQP, err)
			return
		}
		nbTries++
		if !auditor.Validate(vmafStats) {
			// we can't increase quality anymore even if invalid, early exit
			bestEffort = true
			finalQP = candidateQP
			return
		}
	}
	results[candidateQP] = vmafStats
	// Start interpolation searching loop
	defer func() {
		if bestEffort {
			fmt.Fprintf(bypass, "Best effort reached, VMAF stats of candidate %d\n:%s", finalQP, vmafStats)
		}
	}()
	var found bool
	for {
		// Predict next candidate
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
		previousQPs = append(previousQPs, strconv.Itoa(candidateQP))
		// Encode with candidateQP
		GOPQPOutput = filepath.Join(dir, fmt.Sprintf(ffmpegutils.SegEncodedOutputFormat, GOP, candidateQP))
		report = GOPQPOutput + "_vmaf.json"
		if vmafStats, err = GOPQP(input, GOPQPOutput, report, frameRate, totalFrames, GOP, candidateQP, ultraHD, convert10bits); err != nil {
			err = fmt.Errorf("failed to produce QP %d: %w", candidateQP, err)
			return
		}
		results[candidateQP] = vmafStats
		nbTries++
	}
}

func FindCandidate(min, max int, existingResults map[int]ffmpegutils.VMAFStats, auditor VMAFChecker) (candidateQP int, err error) {
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
	// we have reached ffmpegutils.QPMinimum, this will be a best effort scenario
	return
}

func GOPQP(input, output, vmafReportPath, frameRate string, totalFrames, GOPID, qp int, ultraHD, convert10bits bool) (vmafStats ffmpegutils.VMAFStats, err error) {
	// Encode
	if err = encodeQP(input, output, totalFrames, qp, convert10bits); err != nil {
		err = fmt.Errorf("failed to encode GOP: %w", err)
		return
	}
	// Compute VMAF
	if vmafStats, err = computeVMAF(output, input, vmafReportPath, frameRate, totalFrames, ultraHD, false); err != nil {
		err = fmt.Errorf("failed to compute VMAF for GOP: %w", err)
		return
	}
	if *debug {
		fmt.Fprintf(liveprogress.Bypass(), "GOP %d: QP %d: VMAF results:\n%s", GOPID, qp, vmafStats)
	}
	return
}
