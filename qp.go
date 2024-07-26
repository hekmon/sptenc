package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/hekmon/cunits/v2"
	"github.com/hekmon/ffmpegutils"
	"github.com/hekmon/liveprogress/v2"
)

type QPStats struct {
	Minimum        int
	Maximum        int
	GOPMean        float64
	GlobalWeighted float64
}

func findAllGOPQP(dir string, nbGOP int, totalDuration time.Duration, auditor VMAFChecker) (results []int, stats QPStats, err error) {
	// Prepare
	var (
		GOPDuration, allGOPDuration time.Duration
		GOPQP, totalQP, QPWeights   int
		GOPSize, allGOPSize         cunits.Bits
		bestEffort                  bool
		nbBestEfforts               int
	)
	results = make([]int, nbGOP)
	bypass := liveprogress.Bypass()
	// Live progress
	var GOPDone int
	bar := liveprogress.SetMainLineAsBar(
		liveprogress.WithTotal(uint64(totalDuration)),
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
	stats.Minimum = ffmpegutils.QPMaximum + 1
	stats.Maximum = ffmpegutils.QPMinimum - 1
	start := time.Now()
	for GOP := 0; GOP < nbGOP; GOP++ {
		fmt.Fprintf(bypass, "GOP %d: Search for the right QP, starting with %d\n", GOP, *startQP)
		if GOPQP, bestEffort, GOPDuration, err = findGOPQP(dir, GOP, auditor); err != nil {
			err = fmt.Errorf("failed to find the right encoding QP GOP %d: %w", GOP, err)
			return
		}
		results[GOP] = GOPQP
		// Compute stats
		if GOPQP < stats.Minimum {
			stats.Minimum = GOPQP
		}
		if GOPQP > stats.Maximum {
			stats.Maximum = GOPQP
		}
		totalQP += GOPQP
		allGOPDuration += GOPDuration
		QPWeights += GOPQP * int(GOPDuration.Milliseconds())
		if GOPSize, err = getFileSize(filepath.Join(dir, fmt.Sprintf(ffmpegutils.SegEncodedOutputFormat, GOP, GOPQP))); err != nil {
			err = fmt.Errorf("failed to get the size of GOP %d: %w", GOP, err)
			return
		}
		allGOPSize += GOPSize
		if bestEffort {
			nbBestEfforts++
		}
		// Update live progress
		bar.CurrentAdd(uint64(GOPDuration))
		GOPDone++
	}
	duration := time.Since(start)
	// Done
	if allGOPDuration != totalDuration {
		fmt.Fprintf(bypass, "WARNING: Desync possible: encoded GOP duration: %s, original duration: %s\n",
			allGOPDuration, totalDuration)
	}
	stats.GOPMean = float64(totalQP) / float64(len(results))
	stats.GlobalWeighted = float64(QPWeights) / float64(allGOPDuration.Milliseconds())
	fmt.Fprintf(bypass, "GOP QPs: %+v\n", results)
	fmt.Fprintf(bypass, "Mean GOP QP is %s and weighted global QP is %s.\n",
		strconv.FormatFloat(stats.GOPMean, 'f', -1, 64),
		strconv.FormatFloat(stats.GlobalWeighted, 'f', -1, 64),
	)
	if nbBestEfforts > 0 {
		fmt.Fprintf(bypass, "WARNING: %d GOP were encoded with best effort, stopping at QP 0 but not validating VMAF config. Please check the logs.\n", nbBestEfforts)
	}
	fmt.Fprintf(bypass, "GOP encoding QP search done in %s.\n", duration.Round(time.Second))
	return
}

func findGOPQP(dir string, GOP int, auditor VMAFChecker) (finalQP int, bestEffort bool, duration time.Duration, err error) {
	bypass := liveprogress.Bypass()
	// Prepare
	input := filepath.Join(dir, fmt.Sprintf(ffmpegutils.SegOutputFormat, GOP))
	GOPInfos, err := getStreamsInfosCF(input)
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
	if *debug {
		fmt.Fprintf(bypass, "GOP %d: contains %d frames\n", GOP, totalFrames)
	}
	frameRate := videoTrack.RFrameRate
	ultraHD := videoTrack.Height >= ffmpegutils.UltraHDHeight
	// Verify output files frames count when done
	defer func() {
		if err != nil {
			// if we exit with an error, no need to check that everything is fine
			return
		}
		var (
			finalGOPInfos       ffmpegutils.FFProbeStats
			finalGOPTotalFrames int
		)
		if finalGOPInfos, err = getStreamsInfosCF(filepath.Join(dir, fmt.Sprintf(ffmpegutils.SegEncodedOutputFormat, GOP, finalQP))); err != nil {
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
			fmt.Fprintf(bypass, "Final GOP has %d frames as original GOP.\n", finalGOPTotalFrames)
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
	// Inscrease search or decrease search
	if auditor.Validate(vmafStats) {
		// We got a valid QP, try to increase QP to reduce space while we can
		lastValid := *startQP
		QPCandidate := *startQP + 1
		// Search
		for {
			fmt.Fprintln(bypass, faint.Styled(
				fmt.Sprintf("GOP %d: QP %d is good enough, let's try to decrease size with QP %d", GOP, lastValid, QPCandidate),
			))
			// Check QP
			if QPCandidate > ffmpegutils.QPMaximum {
				fmt.Fprintf(bypass, "GOP %d: QP %d is invalid, rolling back to QP %s\n",
					GOP, QPCandidate, bold.Styled(strconv.Itoa(lastValid)))
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
			// If the new QP is invalid, we return the previous one
			if !auditor.Validate(vmafStats) {
				// We reach an invalid QP, let's use the previous valid QP
				fmt.Fprintf(bypass, "GOP %d: QP %d is not good enough, rolling back to QP %s\n",
					GOP, QPCandidate, bold.Styled(strconv.Itoa(lastValid)))
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
			fmt.Fprintln(bypass, faint.Styled(
				fmt.Sprintf("GOP %d: QP %d is not good enough, let's try to increase quality with QP %d", GOP, lastInvalid, QPCandidate),
			))
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
			// We found a valid QP after encountering an invalid QP, let's use it
			if auditor.Validate(vmafStats) {
				fmt.Fprintf(bypass, "GOP %d: QP %s is good enough, keeping it\n", GOP, bold.Styled(strconv.Itoa(QPCandidate)))
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
	if vmafStats, err = computeVMAF(output, input, vmafReportPath, frameRate, totalFrames, ultraHD); err != nil {
		err = fmt.Errorf("failed to compute VMAF for GOP: %w", err)
		return
	}
	if *debug {
		fmt.Fprintf(liveprogress.Bypass(), "GOP %d: QP %d: VMAF results:\n%s\n", GOPID, qp, vmafStats)
	}
	return
}
