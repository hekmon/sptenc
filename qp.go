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

func findAllGOPQP(dir string, nbGOP int, globalDuration time.Duration, auditor VMAFChecker, convert10bits bool) (results []int, stats QPStats, err error) {
	// Prepare
	var (
		GOPDuration                                   time.Duration
		GOPFrames, totalGOPFrames, totalEncodedFrames int
		GOPQP, QPWeights                              int
		GOPSize, allGOPSize                           cunits.Bits
		totalNbTries, GOPNbTries                      int
		bestEffort                                    bool
		nbBestEfforts                                 int
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
		if GOPQP, GOPFrames, GOPNbTries, bestEffort, GOPDuration, err = findGOPQP(dir, GOP, batchStartQP, auditor, convert10bits); err != nil {
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
		totalEncodedFrames += GOPFrames * GOPNbTries
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
	fmt.Fprintf(bypass, "%d encoding attempts (for a total of %d encoded frames) were performed to find the best possible QP for %d GOPs.\n",
		totalNbTries, totalEncodedFrames, len(results))
	processIdealQP(results, allGOPFrames, totalEncodedFrames, totalNbTries, batchStartQP)
	if nbBestEfforts > 0 {
		fmt.Fprintf(bypass, "WARNING: %d GOP were encoded with best effort, stopping at QP 0 but not validating VMAF config. Please check the logs.\n", nbBestEfforts)
	}
	fmt.Fprintf(bypass, "GOP encoding QP search done in %s.\n", duration.Round(time.Second))
	return
}

func findGOPQP(dir string, GOP, batchStartQP int, auditor VMAFChecker, convert10bits bool) (finalQP, finalGOPTotalFrames, nbTries int, bestEffort bool, duration time.Duration, err error) {
	bypass := liveprogress.Bypass()
	status := fmt.Sprintf("Preparing for GOP %d encoding", GOP)
	statusLine := liveprogress.AddCustomLine(func() string {
		return fmt.Sprintf("       GOP | #%d - %s", GOP, status)
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
	results := make(map[int]ffmpegutils.VMAFStats, ffmpegutils.QPMaximum-ffmpegutils.QPMinimum+1)
	// Compute first point
	previousQP := batchStartQP
	status = fmt.Sprintf("Warming up QP search with QP %d (1/2)", previousQP)
	GOPQPOutput := filepath.Join(dir, fmt.Sprintf(ffmpegutils.SegEncodedOutputFormat, GOP, previousQP))
	report := GOPQPOutput + "_vmaf.json"
	var previousVMAFStats ffmpegutils.VMAFStats
	if previousVMAFStats, err = GOPQP(input, GOPQPOutput, report, frameRate, totalFrames, GOP, previousQP, ultraHD, convert10bits); err != nil {
		err = fmt.Errorf("failed to produce QP %d: %w", previousQP, err)
		return
	}
	results[previousQP] = previousVMAFStats
	nbTries++
	// Compute second point
	var currentQP int
	if auditor.Validate(previousVMAFStats) {
		currentQP = batchStartQP + 1
	} else {
		currentQP = batchStartQP - 1
	}
	status = fmt.Sprintf("Warming up QP search with QP %d (2/2)", currentQP)
	GOPQPOutput = filepath.Join(dir, fmt.Sprintf(ffmpegutils.SegEncodedOutputFormat, GOP, currentQP))
	report = GOPQPOutput + "_vmaf.json"
	var currentVMAFStats ffmpegutils.VMAFStats
	if currentVMAFStats, err = GOPQP(input, GOPQPOutput, report, frameRate, totalFrames, GOP, currentQP, ultraHD, convert10bits); err != nil {
		err = fmt.Errorf("failed to produce QP %d: %w", currentQP, err)
		return
	}
	results[currentQP] = currentVMAFStats
	nbTries++
	for {
		// Compute candidate
		candidateQP := InterpolateCandidate(previousQP, currentQP, previousVMAFStats, currentVMAFStats, auditor)
		if candidateResults, found := results[candidateQP]; found {
			if *debug {
				fmt.Fprintf(bypass, "Interpolated candidate %d already computed (valid: %t)\n", candidateQP, auditor.Validate(candidateResults))
			}
			// we already computed this candidate, let's think this thru
			if auditor.Validate(candidateResults) {
				// Are we sure that next higher candidate does not validate ?
				if candidateQP == ffmpegutils.QPMaximum {
					finalQP = candidateQP
					return
				}
				for i := candidateQP + 1; i <= ffmpegutils.QPMaximum; i++ {
					if candidateResults, found := results[i]; found {
						if i == ffmpegutils.QPMaximum {
							finalQP = i
							return
						}
						// we already computed this candidate
						if !auditor.Validate(candidateResults) {
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
					if candidateResults, found := results[i]; found {
						if i == ffmpegutils.QPMinimum {
							finalQP = i
							bestEffort = true
							return
						}
						// we already computed this candidate
						if auditor.Validate(candidateResults) {
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
			fmt.Fprintf(bypass, "Interpolated candidate %d selected for computation\n", candidateQP)
		}
		if auditor.Validate(currentVMAFStats) {
			status = fmt.Sprintf("QP %d is good enough, let's try to decrease size with QP %d", currentQP, candidateQP)
		} else {
			status = fmt.Sprintf("QP %d is not good enough, let's try to increase quality with QP %d", currentQP, candidateQP)
		}
		// Switch values
		previousQP = currentQP
		previousVMAFStats = currentVMAFStats
		// Encode with candidate
		currentQP = candidateQP
		GOPQPOutput = filepath.Join(dir, fmt.Sprintf(ffmpegutils.SegEncodedOutputFormat, GOP, currentQP))
		report = GOPQPOutput + "_vmaf.json"
		if currentVMAFStats, err = GOPQP(input, GOPQPOutput, report, frameRate, totalFrames, GOP, currentQP, ultraHD, convert10bits); err != nil {
			err = fmt.Errorf("failed to produce QP %d: %w", currentQP, err)
			return
		}
		results[currentQP] = currentVMAFStats
		nbTries++
	}
}

func InterpolateCandidate(previousQP, currentQP int, previousResults, currentResult ffmpegutils.VMAFStats, auditor VMAFChecker) (candidateQP int) {
	var start, increment, end int
	if auditor.Validate(previousResults) {
		if auditor.Validate(currentResult) {
			// We need to further decrease quality
			start = currentQP + 1
			increment = 1
			end = ffmpegutils.QPMaximum
		} else {
			// We need more quality (in between)
			start = currentQP - 1
			increment = -1
			end = previousQP + 1
			if end > start {
				// avoid infinite loop
				// Ex: previous 20 OK, current 21 KO
				return previousQP
			}
		}
	} else {
		if auditor.Validate(currentResult) {
			// We need to reduce quality to reduce size (in between)
			start = currentQP + 1
			increment = 1
			end = previousQP - 1
			if end > start {
				// avoid infinite loop
				// Ex: previous 20 KO, current 19 OK
				return currentQP
			}
		} else {
			// We need more quality
			start = currentQP - 1
			increment = -1
			end = ffmpegutils.QPMinimum
		}
	}
	if start < ffmpegutils.QPMinimum {
		start = ffmpegutils.QPMinimum
	}
	if start > ffmpegutils.QPMaximum {
		start = ffmpegutils.QPMaximum
	}
	if end < ffmpegutils.QPMinimum {
		end = ffmpegutils.QPMinimum
	}
	if end > ffmpegutils.QPMaximum {
		end = ffmpegutils.QPMaximum
	}
	for candidateQP = start; candidateQP != end; candidateQP += increment {
		// Build a theorical VMAF result by interpolation
		var interpolatedVMAF ffmpegutils.VMAFStats
		if *vmafLimitMin != -1 {
			interpolatedVMAF.Minimum = LinearInterpolation(
				float64(currentQP), currentResult.Minimum,
				float64(previousQP), previousResults.Minimum,
				float64(candidateQP),
			)
		}
		if *vmafLimitP1 != -1 {
			interpolatedVMAF.Percentile1 = LinearInterpolation(
				float64(currentQP), currentResult.Percentile1,
				float64(previousQP), previousResults.Percentile1,
				float64(candidateQP),
			)
		}
		if *vmafLimitP5 != -1 {
			interpolatedVMAF.Percentile5 = LinearInterpolation(
				float64(currentQP), currentResult.Percentile5,
				float64(previousQP), previousResults.Percentile5,
				float64(candidateQP),
			)
		}
		if *vmafLimitP10 != -1 {
			interpolatedVMAF.Percentile10 = LinearInterpolation(
				float64(currentQP), currentResult.Percentile10,
				float64(previousQP), previousResults.Percentile10,
				float64(candidateQP),
			)
		}
		if *vmafLimitP25 != -1 {
			interpolatedVMAF.Percentile25 = LinearInterpolation(
				float64(currentQP), currentResult.Percentile25,
				float64(previousQP), previousResults.Percentile25,
				float64(candidateQP),
			)
		}
		if *vmafLimitMedian != -1 {
			interpolatedVMAF.Median = LinearInterpolation(
				float64(currentQP), currentResult.Median,
				float64(previousQP), previousResults.Median,
				float64(candidateQP),
			)
		}
		if *vmafLimitHMean != -1 {
			interpolatedVMAF.HarmonicMean = LinearInterpolation(
				float64(currentQP), currentResult.HarmonicMean,
				float64(previousQP), previousResults.HarmonicMean,
				float64(candidateQP),
			)
		}
		if *vmafLimitMean != -1 {
			interpolatedVMAF.Mean = LinearInterpolation(
				float64(currentQP), currentResult.Mean,
				float64(previousQP), previousResults.Mean,
				float64(candidateQP),
			)
		}
		validQP := auditor.Validate(interpolatedVMAF)
		if increment == -1 && validQP {
			// We were looking for a better quality and we found one
			return
		}
		if increment == 1 && !validQP {
			// We were looking for a smaller size but we found one that did not validate anymore, take the previous one
			return candidateQP - 1
		}
	}
	// We either reach minimumQP or maximumQP
	return
}

func LinearInterpolation(x0, y0, x1, y1, x float64) float64 {
	return y0 + (x-x0)*(y1-y0)/(x1-x0)
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

func processIdealQP(segmentsQP []int, segmentsFrames []int, totalEncodedFrames, totalTries, batchStartQP int) {
	idealStartQPbyTries, idealStartQPTries, idealStartQPbyFrames, idealStartQPFrames := computeIdealStartQP(segmentsQP, segmentsFrames)
	idealQPs.AddIdealQPs(idealStartQPbyTries, idealStartQPbyFrames)
	// We already are on an optimal setting
	if idealStartQPbyFrames == idealStartQPbyTries && idealStartQPbyFrames == batchStartQP {
		fmt.Fprintf(liveprogress.Bypass(), "For this file and this VMAF config, start QP %d was ideal.\n", batchStartQP)
		return
	}
	// Not ideal, let's compute the diff
	lessFrames := totalEncodedFrames - idealStartQPFrames
	framesRatio := float64(idealStartQPFrames) / float64(totalEncodedFrames)
	percentFramesLess := math.Round((1-framesRatio)*10000) / 100
	lessTries := totalTries - idealStartQPTries
	triesRatio := float64(idealStartQPTries) / float64(totalTries)
	percentTriesLess := math.Round((1-triesRatio)*10000) / 100
	// Current startQP is not ideal, but are the ideal QPs the same ?
	if idealStartQPbyFrames == idealStartQPbyTries {
		fmt.Fprintf(liveprogress.Bypass(), "For this file and this VMAF config, the ideal -qp flag value would have been %d (%d less tries [-%0.2f%%] and %d less encoded frames [-%0.2f%%]).\n",
			idealStartQPbyFrames, lessTries, percentTriesLess, lessFrames, percentFramesLess)
		return
	}
	// Ideal QPs are different but is one of them equals to our actual QP ?
	if idealStartQPbyFrames == batchStartQP {
		fmt.Fprintf(liveprogress.Bypass(), "For this file and this VMAF config, the ideal -qp flag value would have been %d (current start QP, the lowest encoded frames) and %d (%d less tries [-%0.2f%%]).\n",
			idealStartQPbyFrames, idealStartQPbyTries, lessTries, percentTriesLess)
		return
	}
	if idealStartQPbyTries == batchStartQP {
		fmt.Fprintf(liveprogress.Bypass(), "For this file and this VMAF config, the ideal -qp flag value would have been %d (current start QP, the lowest tries) and %d (%d less encoded frames [-%0.2f%%]).\n",
			idealStartQPbyTries, idealStartQPbyFrames, lessFrames, percentFramesLess)
		return
	}
	// All start QP are differents
	fmt.Fprintf(liveprogress.Bypass(), "For this file and this VMAF config, the ideal -qp flag value would have been between %d (%d less tries [-%0.2f%%]) and %d (%d less encoded frames [-%0.2f%%]).\n",
		idealStartQPbyTries, lessTries, percentTriesLess, idealStartQPbyFrames, lessFrames, percentFramesLess)
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
