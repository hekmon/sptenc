package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hekmon/cunits/v2"
	"github.com/hekmon/ffmpegutils"
	"github.com/hekmon/liveprogress/v2"
)

var (
	// TMP for bench
	pType          PredicatorType
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
	var batchStartQP int
	if *smartStartQP {
		batchStartQP = idealQPs.GetIdealStartQP()
		fmt.Fprintf(bypass, "Smart start QP set the start QP at %d\n", batchStartQP)
	} else {
		batchStartQP = *startQP
		fmt.Fprintf(bypass, "Start QP is set to %d\n", batchStartQP)
	}
	for GOP := 0; GOP < nbGOP; GOP++ {
		if *debug {
			fmt.Fprintf(bypass, "GOP %d: Search for the right QP\n", GOP)
		}
		if GOPQP, GOPFrames, bestEffort, GOPDuration, err = findGOPQP(dir, GOP, auditor, convert10bits); err != nil {
			err = fmt.Errorf("failed to find the right encoding QP GOP %d: %w", GOP, err)
			return
		}
		results[GOP] = GOPQP
		segmentsFrames[GOP] = GOPFrames
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
	return
}

func findGOPQP(dir string, GOP int, auditor VMAFChecker, convert10bits bool) (finalQP, GOPFrames int, bestEffort bool, duration time.Duration, err error) {
	// init
	var nbTries, methodQP int
	gopQPCache = make(map[int]ffmpegutils.VMAFStats, ffmpegutils.QPMaximum-ffmpegutils.QPMinimum+1)
	// Full interpolation
	{
		pType = AkimaSpline
		method = fmt.Sprintf("full_interpolation_%s", pType)
		if methodQP, GOPFrames, nbTries, bestEffort, duration, err = findGOPQPFullInterpolation(dir, GOP, auditor, convert10bits); err != nil {
			return
		}
		methodNbTries[method] += nbTries
		methodNbFrames[method] += nbTries * GOPFrames
		finalQP = methodQP
	}
	{
		pType = ClampedCubic
		method = fmt.Sprintf("full_interpolation_%s", pType)
		if methodQP, GOPFrames, nbTries, bestEffort, duration, err = findGOPQPFullInterpolation(dir, GOP, auditor, convert10bits); err != nil {
			return
		}
		methodNbTries[method] += nbTries
		methodNbFrames[method] += nbTries * GOPFrames
		if methodQP != finalQP {
			err = fmt.Errorf("Different QP found %d != %d (%s)", methodQP, finalQP, method)
			return
		}
	}
	{
		pType = FritschButland
		method = fmt.Sprintf("full_interpolation_%s", pType)
		if finalQP, GOPFrames, nbTries, bestEffort, duration, err = findGOPQPFullInterpolation(dir, GOP, auditor, convert10bits); err != nil {
			return
		}
		methodNbTries[method] += nbTries
		methodNbFrames[method] += nbTries * GOPFrames
		if methodQP != finalQP {
			err = fmt.Errorf("Different QP found %d != %d (%s)", methodQP, finalQP, method)
			return
		}
	}
	{
		pType = NaturalCubic
		method = fmt.Sprintf("full_interpolation_%s", pType)
		if finalQP, GOPFrames, nbTries, bestEffort, duration, err = findGOPQPFullInterpolation(dir, GOP, auditor, convert10bits); err != nil {
			return
		}
		methodNbTries[method] += nbTries
		methodNbFrames[method] += nbTries * GOPFrames
		if methodQP != finalQP {
			err = fmt.Errorf("Different QP found %d != %d (%s)", methodQP, finalQP, method)
			return
		}
	}
	// {
	// 	pType = NotAKnotCubic
	// 	if finalQP, GOPFrames, nbTries, bestEffort, duration, err = findGOPQPFullInterpolation(dir, GOP, auditor, convert10bits); err != nil {
	// 		return
	// 	}
	// 	method = fmt.Sprintf("full_interpolation_%s", pType)
	// 	methodNbTries[method] += nbTries
	// 	methodNbFrames[method] += nbTries * GOPFrames
	// }
	{
		pType = PiecewiseConstant
		method = fmt.Sprintf("full_interpolation_%s", pType)
		if finalQP, GOPFrames, nbTries, bestEffort, duration, err = findGOPQPFullInterpolation(dir, GOP, auditor, convert10bits); err != nil {
			return
		}
		methodNbTries[method] += nbTries
		methodNbFrames[method] += nbTries * GOPFrames
		if methodQP != finalQP {
			err = fmt.Errorf("Different QP found %d != %d (%s)", methodQP, finalQP, method)
			return
		}
	}
	{
		pType = PiecewiseLinear
		method = fmt.Sprintf("full_interpolation_%s", pType)
		if finalQP, GOPFrames, nbTries, bestEffort, duration, err = findGOPQPFullInterpolation(dir, GOP, auditor, convert10bits); err != nil {
			return
		}
		methodNbTries[method] += nbTries
		methodNbFrames[method] += nbTries * GOPFrames
		if methodQP != finalQP {
			err = fmt.Errorf("Different QP found %d != %d (%s)", methodQP, finalQP, method)
			return
		}
	}
	// Hybrid quick interpolation
	{
		pType = AkimaSpline
		method = fmt.Sprintf("quick_interpolation_%s", pType)
		if finalQP, GOPFrames, nbTries, bestEffort, duration, err = findGOPQPQuickInterpolation(dir, GOP, auditor, convert10bits); err != nil {
			return
		}
		methodNbTries[method] += nbTries
		methodNbFrames[method] += nbTries * GOPFrames
		if methodQP != finalQP {
			err = fmt.Errorf("Different QP found %d != %d (%s)", methodQP, finalQP, method)
			return
		}
	}
	{
		pType = ClampedCubic
		method = fmt.Sprintf("quick_interpolation_%s", pType)
		if finalQP, GOPFrames, nbTries, bestEffort, duration, err = findGOPQPQuickInterpolation(dir, GOP, auditor, convert10bits); err != nil {
			return
		}
		methodNbTries[method] += nbTries
		methodNbFrames[method] += nbTries * GOPFrames
		if methodQP != finalQP {
			err = fmt.Errorf("Different QP found %d != %d (%s)", methodQP, finalQP, method)
			return
		}
	}
	{
		pType = FritschButland
		method = fmt.Sprintf("quick_interpolation_%s", pType)
		if finalQP, GOPFrames, nbTries, bestEffort, duration, err = findGOPQPQuickInterpolation(dir, GOP, auditor, convert10bits); err != nil {
			return
		}
		methodNbTries[method] += nbTries
		methodNbFrames[method] += nbTries * GOPFrames
		if methodQP != finalQP {
			err = fmt.Errorf("Different QP found %d != %d (%s)", methodQP, finalQP, method)
			return
		}
	}
	{
		pType = NaturalCubic
		method = fmt.Sprintf("quick_interpolation_%s", pType)
		if finalQP, GOPFrames, nbTries, bestEffort, duration, err = findGOPQPQuickInterpolation(dir, GOP, auditor, convert10bits); err != nil {
			return
		}
		methodNbTries[method] += nbTries
		methodNbFrames[method] += nbTries * GOPFrames
		if methodQP != finalQP {
			err = fmt.Errorf("Different QP found %d != %d (%s)", methodQP, finalQP, method)
			return
		}
	}
	// {
	// 	pType = NotAKnotCubic
	// 	method = fmt.Sprintf("quick_interpolation_%s", pType)
	// 	if finalQP, GOPFrames, nbTries, bestEffort, duration, err = findGOPQPQuickInterpolation(dir, GOP, auditor, convert10bits); err != nil {
	// 		return
	// 	}
	// 	methodNbTries[method] += nbTries
	// 	methodNbFrames[method] += nbTries * GOPFrames
	// }
	{
		pType = PiecewiseConstant
		method = fmt.Sprintf("quick_interpolation_%s", pType)
		if finalQP, GOPFrames, nbTries, bestEffort, duration, err = findGOPQPQuickInterpolation(dir, GOP, auditor, convert10bits); err != nil {
			return
		}
		methodNbTries[method] += nbTries
		methodNbFrames[method] += nbTries * GOPFrames
		if methodQP != finalQP {
			err = fmt.Errorf("Different QP found %d != %d (%s)", methodQP, finalQP, method)
			return
		}
	}
	{
		pType = PiecewiseLinear
		method = fmt.Sprintf("quick_interpolation_%s", pType)
		if finalQP, GOPFrames, nbTries, bestEffort, duration, err = findGOPQPQuickInterpolation(dir, GOP, auditor, convert10bits); err != nil {
			return
		}
		methodNbTries[method] += nbTries
		methodNbFrames[method] += nbTries * GOPFrames
		if methodQP != finalQP {
			err = fmt.Errorf("Different QP found %d != %d (%s)", methodQP, finalQP, method)
			return
		}
	}
	// quick search
	{
		method = "quick_search"
		if finalQP, GOPFrames, nbTries, bestEffort, duration, err = findGOPQPQuickSearch(dir, GOP, auditor, convert10bits); err != nil {
			return
		}
		methodNbTries[method] += nbTries
		methodNbFrames[method] += nbTries * GOPFrames
		if methodQP != finalQP {
			err = fmt.Errorf("Different QP found %d != %d (%s)", methodQP, finalQP, method)
			return
		}
	}
	// done
	return
}

func findGOPQPFullInterpolation(dir string, GOP int, auditor VMAFChecker, convert10bits bool) (finalQP, finalGOPTotalFrames, nbTries int, bestEffort bool, duration time.Duration, err error) {
	bypass := liveprogress.Bypass()
	previousQPs := make([]string, 0, ffmpegutils.QPMaximum-ffmpegutils.QPMinimum+1)
	statusLine := liveprogress.AddCustomLine(func() string {
		return fmt.Sprintf("       GOP | #%d - Searching for QP (%s): %s", GOP, method, strings.Join(previousQPs, ", "))
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
		finalGOPTotalFrames = finalGOPInfos.VideoTrack().NbReadFrames
		if finalGOPTotalFrames != totalFrames {
			err = fmt.Errorf("final GOP has %d frames instead of %d", finalGOPTotalFrames, totalFrames)
			return
		}
		if *debug {
			fmt.Fprintf(bypass, "Final GOP has %d frames, as original GOP.\n", finalGOPTotalFrames)
		}
	}()
	// Compute first point
	results := make(map[int]ffmpegutils.VMAFStats, ffmpegutils.QPMaximum-ffmpegutils.QPMinimum+1)
	candidateQP := ffmpegutils.QPMinimum
	previousQPs = append(previousQPs, strconv.Itoa(candidateQP))
	GOPQPOutput := filepath.Join(dir, fmt.Sprintf(ffmpegutils.SegEncodedOutputFormat, GOP, candidateQP))
	report := GOPQPOutput + "_vmaf.json"
	var vmafStats ffmpegutils.VMAFStats
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
	results[candidateQP] = vmafStats
	min := candidateQP
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
	results[candidateQP] = vmafStats
	max := candidateQP
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

func findGOPQPQuickInterpolation(dir string, GOP int, auditor VMAFChecker, convert10bits bool) (finalQP, finalGOPTotalFrames, nbTries int, bestEffort bool, duration time.Duration, err error) {
	bypass := liveprogress.Bypass()
	previousQPs := make([]string, 0, ffmpegutils.QPMaximum-ffmpegutils.QPMinimum+1)
	statusLine := liveprogress.AddCustomLine(func() string {
		return fmt.Sprintf("       GOP | #%d - Searching for QP (%s): %s", GOP, method, strings.Join(previousQPs, ", "))
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
		finalGOPTotalFrames = finalGOPInfos.VideoTrack().NbReadFrames
		if finalGOPTotalFrames != totalFrames {
			err = fmt.Errorf("final GOP has %d frames instead of %d", finalGOPTotalFrames, totalFrames)
			return
		}
		if *debug {
			fmt.Fprintf(bypass, "Final GOP has %d frames, as original GOP.\n", finalGOPTotalFrames)
		}
	}()
	// Compute first point as middle QPs
	results := make(map[int]ffmpegutils.VMAFStats, ffmpegutils.QPMaximum-ffmpegutils.QPMinimum+1)
	candidateQP := (ffmpegutils.QPMaximum - ffmpegutils.QPMinimum + 1) / 2
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

func findGOPQPQuickSearch(dir string, GOP int, auditor VMAFChecker, convert10bits bool) (finalQP, finalGOPTotalFrames, nbTries int, bestEffort bool, duration time.Duration, err error) {
	bypass := liveprogress.Bypass()
	previousQPs := make([]string, 0, ffmpegutils.QPMaximum-ffmpegutils.QPMinimum+1)
	statusLine := liveprogress.AddCustomLine(func() string {
		return fmt.Sprintf("       GOP | #%d - Searching for QP (%s): %s", GOP, method, strings.Join(previousQPs, ", "))
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
		finalGOPTotalFrames = finalGOPInfos.VideoTrack().NbReadFrames
		if finalGOPTotalFrames != totalFrames {
			err = fmt.Errorf("final GOP has %d frames instead of %d", finalGOPTotalFrames, totalFrames)
			return
		}
		if *debug {
			fmt.Fprintf(bypass, "Final GOP has %d frames, as original GOP.\n", finalGOPTotalFrames)
		}
	}()
	var (
		candidateQP         int
		alreadyComputed     bool
		GOPQPOutput, report string
		vmafStats           ffmpegutils.VMAFStats
	)
	results := make(map[int]ffmpegutils.VMAFStats, ffmpegutils.QPMaximum-ffmpegutils.QPMinimum+1)
	min := ffmpegutils.QPMinimum
	max := ffmpegutils.QPMaximum
	// Search
	defer func() {
		if finalQP == 0 && !auditor.Validate(results[0]) {
			bestEffort = true
		}
	}()
	for {
		if *debug {
			fmt.Fprintf(bypass, "Searching for QP in range [%d, %d]\n", min, max)
		}
		// New candidate
		if max-min < 2 {
			// Regular case (min is valid, max is invalid)
			if vmafStats, alreadyComputed = results[min]; alreadyComputed && auditor.Validate(vmafStats) {
				if vmafStats, alreadyComputed = results[max]; alreadyComputed && !auditor.Validate(vmafStats) {
					finalQP = min
					return
				}
			}
			// Special cases
			if max == ffmpegutils.QPMaximum {
				// first time: min is 50 and valid and max is 51 (upper limit) but might not have been computed yet
				if _, alreadyComputed = results[max]; alreadyComputed {
					// second time when 51 has been computed:
					//   - if 51 did not validate, min and max are 50
					//   - if 51 validated, min and max are 51
					if min != max {
						err = fmt.Errorf("failed to find QP in range [%d, %d]", min, max)
					} else {
						finalQP = max
					}
					return
				}
				// Compute max, and come back here
				candidateQP = max
			} else if min == ffmpegutils.QPMinimum {
				// first time: max is 1 and does not validate, finalQP will be 0, but is it a best effort ?
				if vmafStats, alreadyComputed = results[min]; alreadyComputed {
					// second time: min and max are 0, but we can now know if 0 validate or is a best effort
					finalQP = min
					if !auditor.Validate(vmafStats) {
						bestEffort = true
					}
					return
				}
				// Compute min and come back here
				candidateQP = min
			} else {
				// Should not happen once algo is done, left for fail safe
				var minStatus string
				if vmafStats, alreadyComputed = results[min]; alreadyComputed {
					minStatus = fmt.Sprintf("min %d (computed: %t, valid: %t)", min, alreadyComputed, auditor.Validate(vmafStats))
				}
				var maxStatus string
				if vmafStats, alreadyComputed = results[max]; alreadyComputed {
					maxStatus = fmt.Sprintf("max %d (computed: %t, valid: %t)", max, alreadyComputed, auditor.Validate(vmafStats))
				}
				err = fmt.Errorf("quick search done but invalid: %s | %s", minStatus, maxStatus)
				return
			}
		} else {
			candidateQP = min + ((max - min) / 2)
		}
		if _, alreadyComputed = results[candidateQP]; alreadyComputed {
			err = fmt.Errorf("quick search candidate %d already computed", candidateQP)
			return
		}
		// Encode with candidateQP
		previousQPs = append(previousQPs, strconv.Itoa(candidateQP))
		GOPQPOutput = filepath.Join(dir, fmt.Sprintf(ffmpegutils.SegEncodedOutputFormat, GOP, candidateQP))
		report = GOPQPOutput + "_vmaf.json"
		if vmafStats, err = GOPQP(input, GOPQPOutput, report, frameRate, totalFrames, GOP, candidateQP, ultraHD, convert10bits); err != nil {
			err = fmt.Errorf("failed to produce QP %d: %w", candidateQP, err)
			return
		}
		nbTries++
		results[candidateQP] = vmafStats
		if auditor.Validate(vmafStats) {
			min = candidateQP
		} else {
			max = candidateQP
		}
	}
}

func FindCandidate(min, max int, existingResults map[int]ffmpegutils.VMAFStats, auditor VMAFChecker) (candidateQP int, err error) {
	bypass := liveprogress.Bypass()
	predicator, err := NewPredicator(pType, existingResults)
	if err != nil {
		err = fmt.Errorf("failed to create predicator: %w", err)
		return
	}
	if *debug {
		fmt.Fprintf(bypass, "Built a interpolation predicator with %d results\n", len(existingResults))
	}
	// Range from maximum QP (lower quality) to minimum QP (higher quality) to find the first (theorical or real) candidate that validate
	var (
		candidateResults ffmpegutils.VMAFStats
		exists           bool
	)
	for candidateQP = max; candidateQP > min; candidateQP-- {
		if candidateResults, exists = existingResults[candidateQP]; !exists {
			candidateResults = predicator.Predict(candidateQP)
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

/*
startqp 10, target 12 (delta 2)
10 ok, 11 ok, 12 ok, 13ko --> 4 tries (delta +2)

startqp 12, target 12 (delta 0)
12 ok, 13ko --> 2 tries (delta +2)

startqp 14, target 12 (delta -2)
14 ko, 13 ko, 12 ok --> 3 tries (-delta +1)

startqp 1, target 0 (delta -1)
1 ko, 0 ok/ko --> 2 tries (-delta +1)

// special case
startqp 0, target 0 (delta 0)
0 ok --> 1 try (delta +1)
*/
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

/*
	Smart ideal start QPs
*/

type idealQPsCollection map[idealQPType]qpList

func (c idealQPsCollection) AddIdealQPs(triesQP, framesQP int) {
	c[idealTries] = append(c[idealTries], triesQP)
	c[idealFrames] = append(c[idealFrames], framesQP)
}

func (c idealQPsCollection) GetIdealStartQP() int {
	var key idealQPType
	if *nvenc {
		key = idealTries
	} else {
		key = idealFrames
	}
	if len(c[key]) == 0 {
		return *startQP
	}
	return c[key].Average()
}

type idealQPType string

type qpList []int

func (qps qpList) Average() int {
	sum := 0
	for _, qp := range qps {
		sum += qp
	}
	return int(math.Round(float64(sum) / float64(len(qps))))
}

const (
	qpstatsFormat             = "smartQP_%s.json"
	idealFrames   idealQPType = "frames"
	idealTries    idealQPType = "tries"
)

var (
	idealQPs idealQPsCollection
)

func computeIdealQPFile() string {
	var builder bytes.Buffer
	builder.WriteString(strconv.FormatFloat(*vmafLimitMin, 'f', -1, 64))
	builder.WriteString(strconv.FormatFloat(*vmafLimitP1, 'f', -1, 64))
	builder.WriteString(strconv.FormatFloat(*vmafLimitP5, 'f', -1, 64))
	builder.WriteString(strconv.FormatFloat(*vmafLimitP10, 'f', -1, 64))
	builder.WriteString(strconv.FormatFloat(*vmafLimitP25, 'f', -1, 64))
	builder.WriteString(strconv.FormatFloat(*vmafLimitMedian, 'f', -1, 64))
	builder.WriteString(strconv.FormatFloat(*vmafLimitHMean, 'f', -1, 64))
	builder.WriteString(strconv.FormatFloat(*vmafLimitMean, 'f', -1, 64))
	return fmt.Sprintf(qpstatsFormat, base64.RawStdEncoding.EncodeToString(builder.Bytes()))
}

func loadIdealQPs() (err error) {
	fd, err := os.Open(computeIdealQPFile())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			idealQPs = make(map[idealQPType]qpList)
			err = nil
		}
		return
	}
	defer fd.Close()
	return json.NewDecoder(fd).Decode(&idealQPs)
}

func saveIdealQPs() error {
	// Create or truncate file
	fd, err := os.Create(computeIdealQPFile())
	if err != nil {
		return err
	}
	defer fd.Close()
	// Make it human readable
	enc := json.NewEncoder(fd)
	enc.SetIndent("", "  ")
	// Dump data
	return enc.Encode(idealQPs)
}
