package main

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"sync"
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

func findAllGOPQP(dir string, nbGOP int, globalDuration time.Duration, auditor VMAFChecker) (results []int, stats QPStats, err error) {
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
	allGOPFrames := make([]int, nbGOP)
	bypass := liveprogress.Bypass()
	// Live progress
	var GOPDone int
	bar := liveprogress.SetMainLineAsBar(
		liveprogress.WithTotal(uint64(globalDuration)),
		liveprogress.WithLineFillRunes(),
		// liveprogress.WithWidth(barsWidth),
		liveprogress.WithPrependDecorator(func(bar *liveprogress.Bar) string {
			return "  Global | "
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
	for GOP := 0; GOP < nbGOP; GOP++ {
		fmt.Fprintf(bypass, "GOP %d: Search for the right QP\n", GOP)
		if GOPQP, GOPFrames, GOPNbTries, bestEffort, GOPDuration, err = findGOPQP(dir, GOP, auditor); err != nil {
			err = fmt.Errorf("failed to find the right encoding QP GOP %d: %w", GOP, err)
			return
		}
		results[GOP] = GOPQP
		allGOPFrames[GOP] = GOPFrames
		fmt.Fprintf(bypass, "GOP %d: QP %d selected for this GOP of %d frames (%d tries)\n", GOP, GOPQP, GOPFrames, GOPNbTries)
		// Compute stats
		if GOPQP < stats.Minimum {
			stats.Minimum = GOPQP
		}
		if GOPQP > stats.Maximum {
			stats.Maximum = GOPQP
		}
		totalGOPFrames += GOPFrames
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
	fmt.Fprintf(bypass, "%d encoding attempts were performed to find the best possible QP for %d GOPs.\n", totalNbTries, len(results))
	printIdealQP(results, allGOPFrames, totalGOPFrames, totalNbTries)
	if nbBestEfforts > 0 {
		fmt.Fprintf(bypass, "WARNING: %d GOP were encoded with best effort, stopping at QP 0 but not validating VMAF config. Please check the logs.\n", nbBestEfforts)
	}
	fmt.Fprintf(bypass, "GOP encoding QP search done in %s.\n", duration.Round(time.Second))
	return
}

func findGOPQP(dir string, GOP int, auditor VMAFChecker) (finalQP, finalGOPTotalFrames, nbTries int, bestEffort bool, duration time.Duration, err error) {
	bypass := liveprogress.Bypass()
	status := fmt.Sprintf("starting with QP %d", *startQP)
	statusLine := liveprogress.AddCustomLine(func() string {
		return fmt.Sprintf("     GOP | #%d - %s", GOP, status)
	})
	defer liveprogress.RemoveCustomLine(statusLine)
	// Prepare
	input := filepath.Join(dir, fmt.Sprintf(ffmpegutils.SegOutputFormat, GOP))
	GOPInfos, err := getStreamsInfosCF(input, false)
	if err != nil {
		err = fmt.Errorf("failed to get streams infos: %w", err)
		return
	}
	duration = GOPInfos.Format.Duration
	videoTrack := GOPInfos.VideoTrack()
	totalFrames, err := strconv.Atoi(videoTrack.NbReadFrames)
	if err != nil {
		err = fmt.Errorf("failed to get total frames: %w", err)
		return
	}
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
		if finalGOPTotalFrames, err = strconv.Atoi(finalGOPInfos.VideoTrack().NbReadFrames); err != nil {
			err = fmt.Errorf("failed to get total frames of final GOP: %w", err)
			return
		}
		if finalGOPTotalFrames != totalFrames {
			err = fmt.Errorf("final GOP has %d frames instead of %d", finalGOPTotalFrames, totalFrames)
			return
		}
		if *debug {
			fmt.Fprintf(bypass, "Final GOP has %d frames, as original GOP.\n", finalGOPTotalFrames)
		}
	}()
	// Execute first test and loop
	GOPQPOutput := filepath.Join(dir, fmt.Sprintf(ffmpegutils.SegEncodedOutputFormat, GOP, *startQP))
	report := GOPQPOutput + "_vmaf.json"
	var vmafStats ffmpegutils.VMAFStats
	if vmafStats, err = GOPQP(input, GOPQPOutput, report, frameRate, totalFrames, GOP, *startQP, ultraHD); err != nil {
		err = fmt.Errorf("failed to produce QP %d: %w", *startQP, err)
		return
	}
	nbTries++
	// Inscrease search or decrease search
	if auditor.Validate(vmafStats) {
		// We got a valid QP, try to increase QP to reduce space while we can
		lastValid := *startQP
		QPCandidate := *startQP + 1
		// Search
		for {
			status = fmt.Sprintf("QP %d is good enough, let's try to decrease size with QP %d", lastValid, QPCandidate)
			// Check QP
			if QPCandidate > ffmpegutils.QPMaximum {
				if *debug {
					fmt.Fprintf(bypass, "GOP %d: QP %d does not validate VMAF, rolling back to QP %s\n",
						GOP, QPCandidate, bold.Styled(strconv.Itoa(lastValid)))
				}
				finalQP = lastValid
				return
			}
			// Test QP
			GOPQPOutput = filepath.Join(dir, fmt.Sprintf(ffmpegutils.SegEncodedOutputFormat, GOP, QPCandidate))
			report = GOPQPOutput + "_vmaf.json"
			if vmafStats, err = GOPQP(input, GOPQPOutput, report, frameRate, totalFrames, GOP, QPCandidate, ultraHD); err != nil {
				err = fmt.Errorf("failed to produce QP %d: %w", QPCandidate, err)
				return
			}
			nbTries++
			// If the new QP is invalid, we return the previous one
			if !auditor.Validate(vmafStats) {
				// We reach an invalid QP, let's use the previous valid QP
				if *debug {
					fmt.Fprintf(bypass, "GOP %d: QP %d is not good enough, rolling back to QP %s\n",
						GOP, QPCandidate, bold.Styled(strconv.Itoa(lastValid)))
				}
				finalQP = lastValid
				// Remove invalid QP
				if !*keep {
					if err = os.Remove(filepath.Join(dir, fmt.Sprintf(ffmpegutils.SegEncodedOutputFormat, GOP, QPCandidate))); err != nil {
						err = fmt.Errorf("failed to remove previous valid QP at %s: %w", GOPQPOutput, err)
						return
					}
				}
				return
			}
			// We found a new valid QP
			if !*keep {
				// Remove previous valid QP
				if err = os.Remove(filepath.Join(dir, fmt.Sprintf(ffmpegutils.SegEncodedOutputFormat, GOP, lastValid))); err != nil {
					err = fmt.Errorf("failed to remove previous valid QP at %s: %w", GOPQPOutput, err)
					return
				}
			}
			// Let's try to increase QP to reduce size
			lastValid = QPCandidate
			QPCandidate++
		}
	} else {
		// We got an invalid QP, try to decrease QP to increase quality until we have a valid QP
		lastInvalid := *startQP
		QPCandidate := *startQP - 1
		// Search
		for {
			status = fmt.Sprintf("QP %d is not good enough, let's try to increase quality with QP %d", lastInvalid, QPCandidate)
			// Check QP
			if QPCandidate < ffmpegutils.QPMinimum {
				fmt.Fprintf(bypass, "GOP %d: QP %d is invalid, rolling back to QP %s with the following VMAF:\n%s",
					GOP, QPCandidate, bold.Styled(strconv.Itoa(lastInvalid)), vmafStats)
				finalQP = lastInvalid
				bestEffort = true
				return
			}
			// We can still test candidate QP, delete previous invalid QP
			if !*keep {
				if err = os.Remove(filepath.Join(dir, fmt.Sprintf(ffmpegutils.SegEncodedOutputFormat, GOP, lastInvalid))); err != nil {
					err = fmt.Errorf("failed to remove previous invalid QP at %s: %w", GOPQPOutput, err)
					return
				}
			}
			// Test QP
			GOPQPOutput = filepath.Join(dir, fmt.Sprintf(ffmpegutils.SegEncodedOutputFormat, GOP, QPCandidate))
			report = GOPQPOutput + "_vmaf.json"
			if vmafStats, err = GOPQP(input, GOPQPOutput, report, frameRate, totalFrames, GOP, QPCandidate, ultraHD); err != nil {
				err = fmt.Errorf("failed to produce QP %d: %w", QPCandidate, err)
				return
			}
			nbTries++
			// We found a valid QP after encountering an invalid QP, let's use it
			if auditor.Validate(vmafStats) {
				if *debug {
					fmt.Fprintf(bypass, "GOP %d: QP %s is good enough, keeping it\n", GOP, bold.Styled(strconv.Itoa(QPCandidate)))
				}
				finalQP = QPCandidate
				return
			}
			// If still invalid, continue to increase quality
			lastInvalid = QPCandidate
			QPCandidate--
		}
	}
}

func GOPQP(input, output, vmafReportPath, frameRate string, totalFrames, GOPID, qp int, ultraHD bool) (vmafStats ffmpegutils.VMAFStats, err error) {
	// Encode
	if err = encodeQP(input, output, totalFrames, qp); err != nil {
		err = fmt.Errorf("failed to encode GOP: %w", err)
		return
	}
	// Compute VMAF
	if vmafStats, err = computeVMAF(output, input, vmafReportPath, frameRate, totalFrames, ultraHD, false); err != nil {
		err = fmt.Errorf("failed to compute VMAF for GOP: %w", err)
		return
	}
	if *debug {
		fmt.Fprintf(liveprogress.Bypass(), "GOP %d: QP %d: VMAF results:\n%s\n", GOPID, qp, vmafStats)
	}
	return
}

func printIdealQP(segmentsQP []int, segmentsFrames []int, totalEncodedFrames, totalTries int) {
	idealStartQPbyFrames, idealStartQPFrames, idealStartQPbyTries, idealStartQPTries := computeIdealStartQP(segmentsQP, segmentsFrames)
	// We already are on an optimal setting
	if idealStartQPbyFrames == idealStartQPbyTries && idealStartQPbyFrames == *startQP {
		fmt.Fprintf(liveprogress.Bypass(), "For this file, start QP %d was ideal.\n", *startQP)
		return
	}
	// Not ideal, let's compute the diff
	framesRatio := float64(idealStartQPFrames) / float64(totalEncodedFrames)
	percentFramesLess := math.Round((1 - framesRatio) * 100)
	lessTries := totalTries - idealStartQPTries
	// Current startQP is not ideal, but are the ideal QPs the same ?
	if idealStartQPbyFrames == idealStartQPbyTries {
		fmt.Fprintf(liveprogress.Bypass(), "For this file, the ideal start QP would have been %d (%d less tries for %0.0f less encoded frames).\n",
			idealStartQPbyFrames, lessTries, percentFramesLess)
		return
	}
	// Ideal QPs are different but is one of them equals to our actual QP ?
	if idealStartQPbyFrames == *startQP {
		fmt.Fprintf(liveprogress.Bypass(), "For this file, the ideal start QP would have been %d (current start QP) and %d (%d less tries).\n",
			idealStartQPbyFrames, idealStartQPTries, lessTries)
		return
	}
	if idealStartQPbyTries == *startQP {
		fmt.Fprintf(liveprogress.Bypass(), "For this file, the ideal start QP would have been %d (current start QP) and %d (%0.0f less encoded frames).\n",
			idealStartQPbyTries, idealStartQPbyFrames, percentFramesLess)
		return
	}
	// All start QP are differents
	fmt.Fprintf(liveprogress.Bypass(), "For this file, the ideal start QP would have been between %d (%d less tries) and %d (%0.0f less encoded frames).\n",
		idealStartQPbyTries, lessTries, idealStartQPbyFrames, percentFramesLess)
}

/*
startqp 10, target 12 (delta 2)
10 ok, 11 ok, 12 ok, 13ko --> 4 tries (delta +2)

startqp 12, target 12 (delta 0)
12 ok, 13ko --> 2 tries (delta +2)

startqp 14, target 12 (delta -2)
14 ko, 13 ko, 12 ok --> 3 tries (-delta +1)
*/
func computeIdealStartQP(segmentsQP []int, segmentsFrames []int) (idealQPLowestTries, tries, idealQPLowestFrames, frames int) {
	// Prepare
	if len(segmentsQP) != len(segmentsFrames) {
		panic("segmentsQP and segmentsFrames must have the same length")
	}
	allTries := make(map[int]int, ffmpegutils.QPMaximum-ffmpegutils.QPMinimum+1)
	allFrames := make(map[int]int, ffmpegutils.QPMaximum-ffmpegutils.QPMinimum+1)
	// Compute how many frames would be generated for each startQP given the actual segmentsQP results
	for startQP := ffmpegutils.QPMinimum; startQP <= ffmpegutils.QPMaximum; startQP++ {
		var totalTries, totalFrames int
		for segmentIndex, segmentQP := range segmentsQP {
			var delta, tries int
			delta = segmentQP - startQP
			if delta >= 0 {
				tries = delta + 2
			} else {
				tries = -delta + 1
			}
			totalTries += tries
			totalFrames += tries * segmentsFrames[segmentIndex]
		}
		allTries[startQP] = totalTries
		allFrames[startQP] = totalFrames
	}
	// Find the ideal startQP that encodes the least frames and has the least tries
	var workers sync.WaitGroup
	workers.Add(2)
	go func() {
		var initOK bool
		for qp, qpTries := range allTries {
			if !initOK {
				idealQPLowestTries = qp
				tries = qpTries
				initOK = true
			} else if qpTries < tries {
				idealQPLowestTries = qp
				tries = qpTries
			}
		}
		workers.Done()
	}()
	go func() {
		var initOK bool
		for qp, qpFrames := range allFrames {
			if !initOK {
				idealQPLowestFrames = qp
				frames = qpFrames
				initOK = true
			} else if qpFrames < frames {
				idealQPLowestFrames = qp
				frames = qpFrames
			}
		}
		workers.Done()
	}()
	workers.Wait()
	return
}
