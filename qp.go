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
	ScenesMean     float64
	GlobalWeighted float64
}

func findScenesQP(dir string, nbScenes, startQP int, totalDuration time.Duration, auditor VMAFChecker, videoCUDA, VMAFCUDA bool, gpu int) (results []int, stats QPStats, err error) {
	// Prepare
	var (
		sceneDuration, scenesDuration time.Duration
		sceneQP, totalQP, QPWeights   int
		sceneSize, scenesSize         cunits.Bits
	)
	results = make([]int, nbScenes)
	bypass := liveprogress.Bypass()
	// Live progress
	var scenesDone int
	bar := liveprogress.SetMainLineAsBar(
		liveprogress.WithTotal(uint64(totalDuration)),
		liveprogress.WithLineFillRunes(),
		// liveprogress.WithWidth(barsWidth),
		liveprogress.WithPrependDecorator(func(bar *liveprogress.Bar) string {
			return "  Scenes | "
		}),
		liveprogress.WithPrependTimeElapsed(liveprogress.BaseStyle()),
		liveprogress.WithAppendPercent(liveprogress.BaseStyle()),
		liveprogress.WithAppendTimeRemaining(liveprogress.BaseStyle()),
		liveprogress.WithAppendDecorator(func(bar *liveprogress.Bar) string {
			return fmt.Sprintf(" left | %d/%d scenes done (%s)",
				scenesDone, nbScenes, scenesSize,
			)
		}),
	)
	defer liveprogress.RemoveBar(bar)
	// Go
	stats.Minimum = ffmpegutils.QPMaximum + 1
	stats.Maximum = ffmpegutils.QPMinimum - 1
	start := time.Now()
	for scene := 0; scene < nbScenes; scene++ {
		fmt.Fprintf(bypass, "Scene %d: Search for the right QP, starting with %d\n", scene, startQP)
		if sceneQP, sceneDuration, err = findSceneQP(dir, scene, startQP, auditor, videoCUDA, VMAFCUDA, gpu); err != nil {
			err = fmt.Errorf("failed to find the right encoding QP scene %d: %w", scene, err)
			return
		}
		results[scene] = sceneQP
		// Compute stats
		if sceneQP < stats.Minimum {
			stats.Minimum = sceneQP
		}
		if sceneQP > stats.Maximum {
			stats.Maximum = sceneQP
		}
		totalQP += sceneQP
		scenesDuration += sceneDuration
		QPWeights += sceneQP * int(sceneDuration.Milliseconds())
		if sceneSize, err = getFileSize(filepath.Join(dir, fmt.Sprintf(ffmpegutils.SceneEncodedOutputFormat, scene, sceneQP))); err != nil {
			err = fmt.Errorf("failed to get the size of scene %d: %w", scene, err)
			return
		}
		scenesSize += sceneSize
		// Update live progress
		bar.CurrentAdd(uint64(sceneDuration))
		scenesDone++
	}
	duration := time.Since(start)
	// Done
	stats.ScenesMean = float64(totalQP) / float64(len(results))
	stats.GlobalWeighted = float64(QPWeights) / float64(scenesDuration.Milliseconds())
	fmt.Fprintf(bypass, "Scenes encoding QP search done in %s. Mean scene QP is %s and weighted global QP is %s.\n",
		duration.Round(time.Second),
		strconv.FormatFloat(stats.ScenesMean, 'f', -1, 64),
		strconv.FormatFloat(stats.GlobalWeighted, 'f', -1, 64),
	)
	if *debug {
		fmt.Fprintf(bypass, "Scenes QPs: %+v\nScenes duration: %s (original duration: %s)\n",
			results, scenesDuration, totalDuration)
	}
	return
}

func findSceneQP(dir string, scene, startQP int, auditor VMAFChecker, videoCUDA, VMAFCUDA bool, gpu int) (finalQP int, duration time.Duration, err error) {
	// Prepare
	input := filepath.Join(dir, fmt.Sprintf(ffmpegutils.SceneOutputFormat, scene))
	sceneInfos, err := getStreamsInfosCF(input)
	if err != nil {
		err = fmt.Errorf("failed to get streams infos: %w", err)
		return
	}
	duration = sceneInfos.Format.Duration
	videoTrack := sceneInfos.VideoTrack()
	totalFrames, err := strconv.Atoi(videoTrack.NbReadFrames)
	if err != nil {
		err = fmt.Errorf("failed to get total frames: %w", err)
		return
	}
	frameRate := videoTrack.RFrameRate
	ultraHD := videoTrack.Height >= ffmpegutils.UltraHDHeight
	bypass := liveprogress.Bypass()
	var (
		valid          bool
		output, report string
	)
	// Execute first test and loop
	output = filepath.Join(dir, fmt.Sprintf(ffmpegutils.SceneEncodedOutputFormat, scene, startQP))
	report = output + "_vmaf.json"
	if valid, err = sceneQP(input, output, report, frameRate, totalFrames, scene, startQP, auditor, ultraHD, *vmafNEG, videoCUDA, VMAFCUDA, gpu); err != nil {
		err = fmt.Errorf("failed to test QP %d: %w", startQP, err)
		return
	}
	// Inscrease search or decrease search
	if valid {
		// We got a valid QP, try to increase QP to reduce space while we can
		lastValid := startQP
		QPCandidate := startQP + 1
		// Search
		for {
			fmt.Fprintf(bypass, "Scene %d: QP %d is good enough, let's try to decrease size with QP %d\n",
				scene, lastValid, QPCandidate)
			// Check QP
			if QPCandidate > ffmpegutils.QPMaximum {
				fmt.Fprintf(bypass, "Scene %d: QP %d is invalid, rolling back to QP %d\n",
					scene, QPCandidate, lastValid)
				finalQP = lastValid
				return
			}
			// Test QP
			output = filepath.Join(dir, fmt.Sprintf(ffmpegutils.SceneEncodedOutputFormat, scene, QPCandidate))
			report = output + "_vmaf.json"
			if valid, err = sceneQP(input, output, report, frameRate, totalFrames, scene, QPCandidate, auditor, ultraHD, *vmafNEG, videoCUDA, VMAFCUDA, gpu); err != nil {
				err = fmt.Errorf("failed to test QP %d: %w", QPCandidate, err)
				return
			}
			// If the new QP is invalid, we return the previous one
			if !valid {
				// We reach an invalid QP, let's use the previous valid QP
				fmt.Fprintf(bypass, "Scene %d: QP %d is not good enough, rolling back to QP %d\n",
					scene, QPCandidate, lastValid)
				finalQP = lastValid
				// Remove invalid QP
				if !*keep {
					if err = os.Remove(filepath.Join(dir, fmt.Sprintf(ffmpegutils.SceneEncodedOutputFormat, scene, QPCandidate))); err != nil {
						err = fmt.Errorf("failed to remove previous valid QP at %s: %w", output, err)
						return
					}
				}
				return
			}
			// We found a new valid QP
			if !*keep {
				// Remove previous valid QP
				if err = os.Remove(filepath.Join(dir, fmt.Sprintf(ffmpegutils.SceneEncodedOutputFormat, scene, lastValid))); err != nil {
					err = fmt.Errorf("failed to remove previous valid QP at %s: %w", output, err)
					return
				}
			}
			// Let's try to increase QP to reduce size
			lastValid = QPCandidate
			QPCandidate++
		}
	} else {
		// We got an invalid QP, try to decrease QP to increase quality until we have a valid QP
		lastInvalid := startQP
		QPCandidate := startQP - 1
		// Search
		for {
			fmt.Fprintf(bypass, "Scene %d: QP %d is not good enough, let's try to increase quality with QP %d\n",
				scene, lastInvalid, QPCandidate)
			// Check QP
			if QPCandidate < ffmpegutils.QPMinimum {
				fmt.Fprintf(bypass, "Scene %d: QP %d is invalid, rolling back to QP %d\n",
					scene, QPCandidate, lastInvalid)
				finalQP = lastInvalid
				return
			}
			// We can still test candidate QP, delete previous invalid QP
			if !*keep {
				if err = os.Remove(filepath.Join(dir, fmt.Sprintf(ffmpegutils.SceneEncodedOutputFormat, scene, lastInvalid))); err != nil {
					err = fmt.Errorf("failed to remove previous invalid QP at %s: %w", output, err)
					return
				}
			}
			// Test QP
			output = filepath.Join(dir, fmt.Sprintf(ffmpegutils.SceneEncodedOutputFormat, scene, QPCandidate))
			report = output + "_vmaf.json"
			if valid, err = sceneQP(input, output, report, frameRate, totalFrames, scene, QPCandidate, auditor, ultraHD, *vmafNEG, videoCUDA, VMAFCUDA, gpu); err != nil {
				err = fmt.Errorf("failed to test QP %d: %w", QPCandidate, err)
				return
			}
			// We found a valid QP after encountering an invalid QP, let's use it
			if valid {
				fmt.Fprintf(bypass, "Scene %d: QP %d is good enough, keeping it\n",
					scene, QPCandidate)
				finalQP = QPCandidate
				return
			}
			// If still invalid, continue to increase quality
			lastInvalid = QPCandidate
			QPCandidate--
		}
	}
}

func sceneQP(input, output, vmafReportPath, frameRate string, totalFrames, sceneID, qp int, auditor VMAFChecker, ultraHD, NEG, videoCUDA, VMAFCUDA bool, gpu int) (valid bool, err error) {
	// Encode
	if err = encodeQP(input, output, totalFrames, qp, videoCUDA, gpu); err != nil {
		err = fmt.Errorf("failed to encode scene: %w", err)
		return
	}
	// Compute VMAF
	var vmaf ffmpegutils.VMAFStats
	if vmaf, err = computeVMAF(output, input, vmafReportPath, frameRate, totalFrames, ultraHD, NEG, videoCUDA, VMAFCUDA, gpu); err != nil {
		err = fmt.Errorf("failed to compute VMAF for scene: %w", err)
		return
	}
	// Check
	valid = auditor.Validate(vmaf)
	if *debug {
		fmt.Fprintf(liveprogress.Bypass(), "Scene %d: QP %d: VMAF results:\n%s", sceneID, qp, vmaf.String())
	}
	return
}
