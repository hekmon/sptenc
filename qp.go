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
	PartsMean      float64
	GlobalWeighted float64
}

func findPartsQP(dir string, nbParts int, totalDuration time.Duration, auditor VMAFChecker) (results []int, stats QPStats, err error) {
	// Prepare
	var (
		partDuration, partsDuration time.Duration
		partQP, totalQP, QPWeights  int
		partSize, partsSize         cunits.Bits
		bestEffort                  bool
		nbBestEfforts               int
	)
	results = make([]int, nbParts)
	bypass := liveprogress.Bypass()
	// Live progress
	var partsDone int
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
			return fmt.Sprintf(" left | %d/%d parts done | %s)",
				partsDone, nbParts, partsSize,
			)
		}),
	)
	defer liveprogress.RemoveBar(bar)
	// Go
	stats.Minimum = ffmpegutils.QPMaximum + 1
	stats.Maximum = ffmpegutils.QPMinimum - 1
	start := time.Now()
	for part := 0; part < nbParts; part++ {
		fmt.Fprintf(bypass, "Part %d: Search for the right QP, starting with %d\n", part, *startQP)
		if partQP, bestEffort, partDuration, err = findPartQP(dir, part, auditor); err != nil {
			err = fmt.Errorf("failed to find the right encoding QP part %d: %w", part, err)
			return
		}
		results[part] = partQP
		// Compute stats
		if partQP < stats.Minimum {
			stats.Minimum = partQP
		}
		if partQP > stats.Maximum {
			stats.Maximum = partQP
		}
		totalQP += partQP
		partsDuration += partDuration
		QPWeights += partQP * int(partDuration.Milliseconds())
		if partSize, err = getFileSize(filepath.Join(dir, fmt.Sprintf(ffmpegutils.SceneEncodedOutputFormat, part, partQP))); err != nil {
			err = fmt.Errorf("failed to get the size of part %d: %w", part, err)
			return
		}
		partsSize += partSize
		if bestEffort {
			nbBestEfforts++
		}
		// Update live progress
		bar.CurrentAdd(uint64(partDuration))
		partsDone++
	}
	duration := time.Since(start)
	// Done
	if partsDuration != totalDuration {
		fmt.Fprintf(bypass, "WARNING: Desync possible: encoded parts duration: %s, original duration: %s\n",
			partsDuration, totalDuration)
	}
	stats.PartsMean = float64(totalQP) / float64(len(results))
	stats.GlobalWeighted = float64(QPWeights) / float64(partsDuration.Milliseconds())
	fmt.Fprintf(bypass, "Parts QPs: %+v\n", results)
	fmt.Fprintf(bypass, "Mean part QP is %s and weighted global QP is %s.\n",
		strconv.FormatFloat(stats.PartsMean, 'f', -1, 64),
		strconv.FormatFloat(stats.GlobalWeighted, 'f', -1, 64),
	)
	if nbBestEfforts > 0 {
		fmt.Fprintf(bypass, "WARNING: %d parts were encoded with best effort, stopping at QP 0 but not validating VMAF config. Please check the logs.\n", nbBestEfforts)
	}
	fmt.Fprintf(bypass, "Parts encoding QP search done in %s.\n", duration.Round(time.Second))
	return
}

func findPartQP(dir string, part int, auditor VMAFChecker) (finalQP int, bestEffort bool, duration time.Duration, err error) {
	bypass := liveprogress.Bypass()
	// Prepare
	input := filepath.Join(dir, fmt.Sprintf(ffmpegutils.SceneOutputFormat, part))
	partInfos, err := getStreamsInfosCF(input)
	if err != nil {
		err = fmt.Errorf("failed to get streams infos: %w", err)
		return
	}
	duration = partInfos.Format.Duration
	videoTrack := partInfos.VideoTrack()
	totalFrames, err := strconv.Atoi(videoTrack.NbReadFrames)
	if err != nil {
		err = fmt.Errorf("failed to get total frames: %w", err)
		return
	}
	if *debug {
		fmt.Fprintf(bypass, "Part %d: contains %d frames\n", part, totalFrames)
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
			finalPartInfos       ffmpegutils.FFProbeStats
			finalPartTotalFrames int
		)
		if finalPartInfos, err = getStreamsInfosCF(filepath.Join(dir, fmt.Sprintf(ffmpegutils.SceneEncodedOutputFormat, part, finalQP))); err != nil {
			err = fmt.Errorf("failed to get streams infos of final part: %w", err)
			return
		}
		if finalPartTotalFrames, err = strconv.Atoi(finalPartInfos.VideoTrack().NbReadFrames); err != nil {
			err = fmt.Errorf("failed to get total frames of final part: %w", err)
			return
		}
		if finalPartTotalFrames != totalFrames {
			err = fmt.Errorf("final part has %d frames instead of %d", finalPartTotalFrames, totalFrames)
			return
		}
		if *debug {
			fmt.Fprintf(bypass, "Final part has %d frames as original part.\n", finalPartTotalFrames)
		}
	}()
	// Execute first test and loop
	partQPOutput := filepath.Join(dir, fmt.Sprintf(ffmpegutils.SceneEncodedOutputFormat, part, *startQP))
	report := partQPOutput + "_vmaf.json"
	var vmafStats ffmpegutils.VMAFStats
	if vmafStats, err = partQP(input, partQPOutput, report, frameRate, totalFrames, part, *startQP, ultraHD); err != nil {
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
			fmt.Fprintf(bypass, "Part %d: QP %d is good enough, let's try to decrease size with QP %d\n",
				part, lastValid, QPCandidate)
			// Check QP
			if QPCandidate > ffmpegutils.QPMaximum {
				fmt.Fprintf(bypass, "Part %d: QP %d is invalid, rolling back to QP %d with the following VMAF:\n%s",
					part, QPCandidate, lastValid, vmafStats)
				finalQP = lastValid
				return
			}
			// Test QP
			partQPOutput = filepath.Join(dir, fmt.Sprintf(ffmpegutils.SceneEncodedOutputFormat, part, QPCandidate))
			report = partQPOutput + "_vmaf.json"
			if vmafStats, err = partQP(input, partQPOutput, report, frameRate, totalFrames, part, QPCandidate, ultraHD); err != nil {
				err = fmt.Errorf("failed to produce QP %d: %w", QPCandidate, err)
				return
			}
			// If the new QP is invalid, we return the previous one
			if !auditor.Validate(vmafStats) {
				// We reach an invalid QP, let's use the previous valid QP
				fmt.Fprintf(bypass, "Part %d: QP %d is not good enough, rolling back to QP %d\n",
					part, QPCandidate, lastValid)
				finalQP = lastValid
				// Remove invalid QP
				if !*keep {
					if err = os.Remove(filepath.Join(dir, fmt.Sprintf(ffmpegutils.SceneEncodedOutputFormat, part, QPCandidate))); err != nil {
						err = fmt.Errorf("failed to remove previous valid QP at %s: %w", partQPOutput, err)
						return
					}
				}
				return
			}
			// We found a new valid QP
			if !*keep {
				// Remove previous valid QP
				if err = os.Remove(filepath.Join(dir, fmt.Sprintf(ffmpegutils.SceneEncodedOutputFormat, part, lastValid))); err != nil {
					err = fmt.Errorf("failed to remove previous valid QP at %s: %w", partQPOutput, err)
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
			fmt.Fprintf(bypass, "Part %d: QP %d is not good enough, let's try to increase quality with QP %d\n",
				part, lastInvalid, QPCandidate)
			// Check QP
			if QPCandidate < ffmpegutils.QPMinimum {
				fmt.Fprintf(bypass, "Part %d: QP %d is invalid, rolling back to QP %d with the following VMAF:\n%s",
					part, QPCandidate, lastInvalid, vmafStats)
				finalQP = lastInvalid
				bestEffort = true
				return
			}
			// We can still test candidate QP, delete previous invalid QP
			if !*keep {
				if err = os.Remove(filepath.Join(dir, fmt.Sprintf(ffmpegutils.SceneEncodedOutputFormat, part, lastInvalid))); err != nil {
					err = fmt.Errorf("failed to remove previous invalid QP at %s: %w", partQPOutput, err)
					return
				}
			}
			// Test QP
			partQPOutput = filepath.Join(dir, fmt.Sprintf(ffmpegutils.SceneEncodedOutputFormat, part, QPCandidate))
			report = partQPOutput + "_vmaf.json"
			if vmafStats, err = partQP(input, partQPOutput, report, frameRate, totalFrames, part, QPCandidate, ultraHD); err != nil {
				err = fmt.Errorf("failed to produce QP %d: %w", QPCandidate, err)
				return
			}
			// We found a valid QP after encountering an invalid QP, let's use it
			if auditor.Validate(vmafStats) {
				fmt.Fprintf(bypass, "Part %d: QP %d is good enough, keeping it\n",
					part, QPCandidate)
				finalQP = QPCandidate
				return
			}
			// If still invalid, continue to increase quality
			lastInvalid = QPCandidate
			QPCandidate--
		}
	}
}

func partQP(input, output, vmafReportPath, frameRate string, totalFrames, partID, qp int, ultraHD bool) (vmafStats ffmpegutils.VMAFStats, err error) {
	// Encode
	if err = encodeQP(input, output, totalFrames, qp); err != nil {
		err = fmt.Errorf("failed to encode part: %w", err)
		return
	}
	// Compute VMAF
	if vmafStats, err = computeVMAF(output, input, vmafReportPath, frameRate, totalFrames, ultraHD); err != nil {
		err = fmt.Errorf("failed to compute VMAF for part: %w", err)
		return
	}
	if *debug {
		fmt.Fprintf(liveprogress.Bypass(), "Part %d: QP %d: VMAF results:\n%s\n", partID, qp, vmafStats)
	}
	return
}
