package main

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/hekmon/ffmpegutils"
	"github.com/hekmon/liveprogress/v2"
)

func findGOPQPQuickSearch(dir string, GOP, splitQP int, auditor VMAFChecker, convert10bits bool) (finalQP, finalGOPTotalFrames, nbTries int, bestEffort bool, duration time.Duration, err error) {
	bypass := liveprogress.Bypass()
	previousQPs := make([]string, 0, ffmpegutils.QPMaximum-ffmpegutils.QPMinimum+1)
	defer func() {
		if *debug {
			fmt.Fprintf(bypass, "QPs tested: %s\n", strings.Join(previousQPs, ","))
		}
	}()
	statusLine := liveprogress.AddCustomLine(func() string {
		return fmt.Sprintf("       GOP | #%d - Searching for QP (quicksearch): %s", GOP, strings.Join(previousQPs, ","))
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
			if len(results) == 0 {
				candidateQP = splitQP
			} else {
				candidateQP = min + ((max - min) / 2)
			}
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

func findGOPQPSplitInterpol(dir string, GOP, splitQP int, auditor VMAFChecker, convert10bits bool) (finalQP, GOPFrames, nbTries int, bestEffort bool, duration time.Duration, err error) {
	bypass := liveprogress.Bypass()
	previousQPs := make([]string, 0, ffmpegutils.QPMaximum-ffmpegutils.QPMinimum+1)
	defer func() {
		if *debug {
			fmt.Fprintf(bypass, "QPs tested: %s\n", strings.Join(previousQPs, ","))
		}
	}()
	statusLine := liveprogress.AddCustomLine(func() string {
		return fmt.Sprintf("       GOP | #%d - Searching for QP (split interpolation): %s", GOP, strings.Join(previousQPs, ","))
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

func findGOPQPQuickInterpol(dir string, GOP, splitQP int, auditor VMAFChecker, convert10bits bool) (finalQP, finalGOPTotalFrames, nbTries int, bestEffort bool, duration time.Duration, err error) {
	bypass := liveprogress.Bypass()
	previousQPs := make([]string, 0, ffmpegutils.QPMaximum-ffmpegutils.QPMinimum+1)
	defer func() {
		if *debug {
			fmt.Fprintf(bypass, "QPs tested: %s\n", strings.Join(previousQPs, ","))
		}
	}()
	statusLine := liveprogress.AddCustomLine(func() string {
		return fmt.Sprintf("       GOP | #%d - Searching for QP (QuickInterpol): %s", GOP, strings.Join(previousQPs, ","))
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
	var minComputed, maxComputed, found bool
	for {
		_, minComputed = results[min]
		_, maxComputed = results[max]
		if len(results) == 0 {
			// quick search
			candidateQP = splitQP
			if *debug {
				fmt.Fprintf(bypass, "Searching for QP in range [%d, %d] with candidate %d\n", min, max, candidateQP)
			}
		} else if len(results) < 2 || !(minComputed && maxComputed) {
			// quick search
			candidateQP = min + ((max - min) / 2)
			if *debug {
				fmt.Fprintf(bypass, "Searching for QP in range [%d, %d] with candidate %d\n", min, max, candidateQP)
			}
		} else {
			// switch to interpolation
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

func findGOPQPStdDevQuick(dir string, GOP, meanAvg, stdDevAvg int, auditor VMAFChecker, convert10bits bool) (finalQP, finalGOPTotalFrames, nbTries int, bestEffort bool, duration time.Duration, err error) {
	bypass := liveprogress.Bypass()
	previousQPs := make([]string, 0, ffmpegutils.QPMaximum-ffmpegutils.QPMinimum+1)
	defer func() {
		if *debug {
			fmt.Fprintf(bypass, "QPs tested: %s\n", strings.Join(previousQPs, ","))
		}
	}()
	statusLine := liveprogress.AddCustomLine(func() string {
		return fmt.Sprintf("       GOP | #%d - Searching for QP (quicksearch): %s", GOP, strings.Join(previousQPs, ","))
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
		if max-min >= 2 {
			// While we have a "wide" range (more than 2 candidates)
			if len(results) == 0 {
				// First time, start at meanAvg to determine if we have an upper range or lower range
				candidateQP = meanAvg
			} else if min == ffmpegutils.QPMinimum {
				// Starting at round 2, if we have still a lowest minimum, use increment of the standard deviation average toward minimum
				// to maximize the chance to find a better candidate while still keeping the range as low as possible
				if candidateQP = meanAvg - len(results)*stdDevAvg; candidateQP < ffmpegutils.QPMinimum {
					candidateQP = ffmpegutils.QPMinimum
				}
			} else if max == ffmpegutils.QPMaximum {
				// Starting at round 2, if we have still a highest maximum, use increment of the standard deviation average toward maximum
				// to maximize the chance to find a better candidate while still keeping the range as low as possible
				if candidateQP = meanAvg + len(results)*stdDevAvg; candidateQP > ffmpegutils.QPMaximum {
					candidateQP = ffmpegutils.QPMaximum
				}
			} else {
				// Once we have a closed range, switch to quick search with middle position
				candidateQP = min + ((max - min) / 2)
			}
		} else {
			// We now have only 2 candidates next to each other
			// Regular case (min is valid, max is invalid)
			if vmafStats, alreadyComputed = results[min]; alreadyComputed && auditor.Validate(vmafStats) {
				if vmafStats, alreadyComputed = results[max]; alreadyComputed && !auditor.Validate(vmafStats) {
					finalQP = min
					return
				}
			}
			// Special cases with extremes that may not have been computed yet
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
		}
		if _, alreadyComputed = results[candidateQP]; alreadyComputed {
			// Should not happen once algo is done, left for fail safe
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

func findGOPQPStdDevInterpol(dir string, GOP, meanAvg, stdDevAvg int, auditor VMAFChecker, convert10bits bool) (finalQP, finalGOPTotalFrames, nbTries int, bestEffort bool, duration time.Duration, err error) {
	bypass := liveprogress.Bypass()
	previousQPs := make([]string, 0, ffmpegutils.QPMaximum-ffmpegutils.QPMinimum+1)
	defer func() {
		if *debug {
			fmt.Fprintf(bypass, "QPs tested: %s\n", strings.Join(previousQPs, ","))
		}
	}()
	statusLine := liveprogress.AddCustomLine(func() string {
		return fmt.Sprintf("       GOP | #%d - Searching for QP (StdDevInterpolation): %s", GOP, strings.Join(previousQPs, ","))
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
	var minComputed, maxComputed, found bool
	for {
		_, minComputed = results[min]
		_, maxComputed = results[max]
		if len(results) == 0 {
			// quick search
			candidateQP = meanAvg
			if *debug {
				fmt.Fprintf(bypass, "Searching for QP in range [%d, %d] with candidate %d\n", min, max, candidateQP)
			}
		} else if !(minComputed && maxComputed) {
			// Use standard deviation avg to slowy expand range while maximizing our chances to find the best QP
			if min == ffmpegutils.QPMinimum {
				// Starting at round 2, if we have still a lowest minimum, use increment of the standard deviation average toward minimum
				// to maximize the chance to find a better candidate while still keeping the range as low as possible
				if candidateQP = meanAvg - len(results)*stdDevAvg; candidateQP < ffmpegutils.QPMinimum {
					candidateQP = ffmpegutils.QPMinimum
				}
			} else if max == ffmpegutils.QPMaximum {
				// Starting at round 2, if we have still a highest maximum, use increment of the standard deviation average toward maximum
				// to maximize the chance to find a better candidate while still keeping the range as low as possible
				if candidateQP = meanAvg + len(results)*stdDevAvg; candidateQP > ffmpegutils.QPMaximum {
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
