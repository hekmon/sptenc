package main

import (
	"fmt"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/hekmon/cunits/v2"
	"github.com/hekmon/ffmpegutils"
	"github.com/hekmon/liveprogress/v2"
)

var (
	// TMP for bench
	method         string
	gopQPCache     map[int]ffmpegutils.VMAFStats
	methodNbTries  = make(map[string]int, 10)
	methodNbFrames = make(map[string]int, 10)
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
	meanAvg, stdDevAvg := previousRuns.GetMeanStdDev()
	for GOP := 0; GOP < nbGOP; GOP++ {
		if *debug {
			fmt.Fprintf(bypass, "GOP %d: Search for the right QP\n", GOP)
		}
		if GOPQP, GOPFrames, GOPNbTries, bestEffort, GOPDuration, err = findGOPQP(dir, GOP, meanAvg, stdDevAvg, auditor, convert10bits); err != nil {
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
	var (
		bestNbTriesMethod, bestNbFramesMethod string
		bestNbTries, bestNbFrames             int
	)
	for nbTriesMethod, nbTries := range methodNbTries {
		if bestNbTriesMethod == "" || nbTries < bestNbTries {
			bestNbTries = nbTries
			bestNbTriesMethod = nbTriesMethod
		}
		fmt.Fprintf(bypass, "Method %s: %d tries\n", nbTriesMethod, nbTries)
	}
	for nbFramesMethod, nbFrames := range methodNbFrames {
		if bestNbFramesMethod == "" || nbFrames < bestNbFrames {
			bestNbFrames = nbFrames
			bestNbFramesMethod = nbFramesMethod
		}
		fmt.Fprintf(bypass, "Method %s: %d frames\n", nbFramesMethod, nbFrames)
	}
	fmt.Fprintf(bypass, "Best method for tries is %s with %d tries.\n", bestNbTriesMethod, bestNbTries)
	fmt.Fprintf(bypass, "Best method for frames is %s with %d frames.\n", bestNbFramesMethod, bestNbFrames)
	fmt.Fprintf(bypass, "GOP encoding QP search done in %s.\n", duration.Round(time.Second))
	idealQPLowestTries, tries, idealQPLowestFrames, frames := computeIdealStartQP(results, segmentsFrames)
	fmt.Fprintf(bypass, "Manual start QP Ideal QP for lowest tries is %d with %d tries.\n", idealQPLowestTries, tries)
	fmt.Fprintf(bypass, "Manual start QP Ideal QP for lowest frames is %d with %d frames.\n", idealQPLowestFrames, frames)
	// Save stats for futur runs
	previousRuns.AddRun(results)
	return
}

func findGOPQP(dir string, GOP, meanAvg, stdDevAvg int, auditor VMAFChecker, convert10bits bool) (finalQP, GOPFrames, nbTries int, bestEffort bool, duration time.Duration, err error) {
	// init
	var methodQP int
	gopQPCache = make(map[int]ffmpegutils.VMAFStats, ffmpegutils.QPMaximum-ffmpegutils.QPMinimum+1)
	bypass := liveprogress.Bypass()
	fmt.Fprintln(bypass, "---------------8<---------------")
	{
		method = "quicksearch"
		if finalQP, GOPFrames, nbTries, bestEffort, duration, err = findGOPQPQuickSearch(dir, GOP, meanAvg, auditor, convert10bits); err != nil {
			err = fmt.Errorf("failed to find the right encoding QP GOP %d with method %s: %w", GOP, method, err)
			return
		}
		methodNbTries[method] += nbTries
		methodNbFrames[method] += nbTries * GOPFrames
	}
	fmt.Fprintln(bypass, "---------------8<---------------")
	{
		method = "split_interpolation"
		if methodQP, GOPFrames, nbTries, bestEffort, duration, err = findGOPQPSplitInterpol(dir, GOP, meanAvg, auditor, convert10bits); err != nil {
			err = fmt.Errorf("failed to find the right encoding QP GOP %d with method %s: %w", GOP, method, err)
			return
		}
		methodNbTries[method] += nbTries
		methodNbFrames[method] += nbTries * GOPFrames
		if methodQP != finalQP {
			fmt.Fprintf(bypass, "Different QP found %d != %d (%s)\n", methodQP, finalQP, method)
		}
	}
	fmt.Fprintln(bypass, "---------------8<---------------")
	{
		method = "quick_interpolation"
		if methodQP, GOPFrames, nbTries, bestEffort, duration, err = findGOPQPQuickInterpol(dir, GOP, meanAvg, auditor, convert10bits); err != nil {
			err = fmt.Errorf("failed to find the right encoding QP GOP %d with method %s: %w", GOP, method, err)
			return
		}
		methodNbTries[method] += nbTries
		methodNbFrames[method] += nbTries * GOPFrames
		if methodQP != finalQP {
			fmt.Fprintf(bypass, "Different QP found %d != %d (%s)\n", methodQP, finalQP, method)
		}
	}
	fmt.Fprintln(bypass, "---------------8<---------------")
	{
		method = "stddev_quick"
		if methodQP, GOPFrames, nbTries, bestEffort, duration, err = findGOPQPStdDevQuick(dir, GOP, meanAvg, stdDevAvg, auditor, convert10bits); err != nil {
			err = fmt.Errorf("failed to find the right encoding QP GOP %d with method %s: %w", GOP, method, err)
			return
		}
		methodNbTries[method] += nbTries
		methodNbFrames[method] += nbTries * GOPFrames
		if methodQP != finalQP {
			fmt.Fprintf(bypass, "Different QP found %d != %d (%s)\n", methodQP, finalQP, method)
		}
	}
	fmt.Fprintln(bypass, "---------------8<---------------")
	{
		method = "stddev_interpolation"
		if methodQP, GOPFrames, nbTries, bestEffort, duration, err = findGOPQPStdDevInterpol(dir, GOP, meanAvg, stdDevAvg, auditor, convert10bits); err != nil {
			err = fmt.Errorf("failed to find the right encoding QP GOP %d with method %s: %w", GOP, method, err)
			return
		}
		methodNbTries[method] += nbTries
		methodNbFrames[method] += nbTries * GOPFrames
		if methodQP != finalQP {
			fmt.Fprintf(bypass, "Different QP found %d != %d (%s)\n", methodQP, finalQP, method)
		}
	}
	fmt.Fprintln(bypass, "---------------8<---------------")
	return
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
	// return min
	if *debug {
		fmt.Fprintf(liveprogress.Bypass(), "No candidate found in range %d-%d, returning %d\n", min, max, candidateQP)
	}
	return
}

func GOPQP(input, output, vmafReportPath, frameRate string, totalFrames, GOPID, qp int, ultraHD, convert10bits bool) (vmafStats ffmpegutils.VMAFStats, err error) {
	// Check cache
	var found bool
	if vmafStats, found = gopQPCache[qp]; found {
		if *debug {
			fmt.Fprintf(liveprogress.Bypass(), "Found QP %d results in cache\n", qp)
		}
		return
	}
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
	gopQPCache[qp] = vmafStats
	return
}

func computeIdealStartQP(segmentsQP []int, segmentsFrames []int) (idealQPLowestTries, tries, idealQPLowestFrames, frames int) {
	// Prepare
	if len(segmentsQP) != len(segmentsFrames) {
		panic("segmentsQP and segmentsFrames must have the same length")
	}
	allTries := make([]int, ffmpegutils.QPMaximum+1)
	allFrames := make([]int, ffmpegutils.QPMaximum+1)
	var workers sync.WaitGroup
	// Compute how many frames would be generated for each startQP given the actual segmentsQP results
	for startQP := ffmpegutils.QPMinimum; startQP <= ffmpegutils.QPMaximum; startQP++ {
		workers.Add(1)
		go func(evaluatedStartQP int) {
			var totalTries, totalFrames int
			for segmentIndex, segmentQP := range segmentsQP {
				var delta, tries int
				delta = segmentQP - evaluatedStartQP
				if delta > 0 {
					tries = delta + 2
				} else if delta == 0 {
					if segmentQP == 0 && evaluatedStartQP == 0 {
						tries = 1
					} else {
						tries = 2
					}
				} else {
					// delta < 0
					tries = -delta + 1
				}
				totalTries += tries
				totalFrames += tries * segmentsFrames[segmentIndex]
			}
			allTries[evaluatedStartQP] = totalTries
			allFrames[evaluatedStartQP] = totalFrames
			workers.Done()
		}(startQP)
	}
	workers.Wait()
	// Print results in debug
	if *debug {
		for startQP, tries := range allTries {
			fmt.Fprintf(liveprogress.Bypass(), "With startQP %d there would have %d tries and %d encoded frames\n", startQP, tries, allFrames[startQP])
		}
	}
	// Find the ideal startQP that encodes the least frames and has the least tries
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
