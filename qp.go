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
	status := fmt.Sprintf("starting with QP %d", batchStartQP)
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
	// Execute first test and loop
	GOPQPOutput := filepath.Join(dir, fmt.Sprintf(ffmpegutils.SegEncodedOutputFormat, GOP, batchStartQP))
	report := GOPQPOutput + "_vmaf.json"
	var vmafStats ffmpegutils.VMAFStats
	if vmafStats, err = GOPQP(input, GOPQPOutput, report, frameRate, totalFrames, GOP, batchStartQP, ultraHD, convert10bits); err != nil {
		err = fmt.Errorf("failed to produce QP %d: %w", batchStartQP, err)
		return
	}
	nbTries++
	// Inscrease search or decrease search
	if auditor.Validate(vmafStats) {
		// We got a valid QP, try to increase QP to reduce space while we can
		lastValid := batchStartQP
		QPCandidate := batchStartQP + 1
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
			if vmafStats, err = GOPQP(input, GOPQPOutput, report, frameRate, totalFrames, GOP, QPCandidate, ultraHD, convert10bits); err != nil {
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
		lastInvalid := batchStartQP
		QPCandidate := batchStartQP - 1
		// Search
		for {
			status = fmt.Sprintf("QP %d is not good enough, let's increase quality with QP %d", lastInvalid, QPCandidate)
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
			if vmafStats, err = GOPQP(input, GOPQPOutput, report, frameRate, totalFrames, GOP, QPCandidate, ultraHD, convert10bits); err != nil {
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
