package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/hekmon/ffmpegutils"
	"github.com/hekmon/liveprogress/v2"
)

func findScenesQP(dir string, nbScenes, startQP int, totalDuration time.Duration, auditor VMAFChecker, videoCUDA, VMAFCUDA bool, gpu int) (results []int, err error) {
	// Prepare
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
			return fmt.Sprintf(" remaining | %d/%d scenes completed",
				scenesDone, nbScenes,
			)
		}),
	)
	defer liveprogress.RemoveBar(bar)
	// Go
	var (
		sceneDuration, scenesDuration time.Duration
		sceneQP, totalQP, QPWeights   int
	)

	start := time.Now()
	for scene := 0; scene < nbScenes; scene++ {
		fmt.Fprintf(bypass, "Scene %d: searching for the right QP\n", scene)
		if sceneQP, sceneDuration, err = findSceneQP(dir, scene, startQP, auditor, videoCUDA, VMAFCUDA, gpu); err != nil {
			err = fmt.Errorf("failed to find the right scene %d encoding QP: %w", scene, err)
			return
		}
		results[scene] = sceneQP
		// Compute stats
		totalQP += sceneQP
		scenesDuration += sceneDuration
		QPWeights += sceneQP * int(sceneDuration.Milliseconds())
		// Update live progress
		bar.CurrentAdd(uint64(sceneDuration))
		scenesDone++
	}
	duration := time.Since(start)
	// Done
	fmt.Fprintf(bypass, "Scenes encoding QP search done in %s. Mean scene QP is %s and weighted global QP is %s.\n",
		duration.Round(time.Second),
		strconv.FormatFloat(float64(totalQP)/float64(len(results)), 'f', -1, 64),
		strconv.FormatFloat(float64(QPWeights)/float64(scenesDuration.Milliseconds()), 'f', -1, 64),
	)
	if *debug {
		fmt.Fprintf(bypass, "Scenes QPs: %+v\nScenes duration: %s (expected duration: %s)\n",
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
	fmt.Fprintf(bypass, "Scene %d: start search with QP %d\n", scene, startQP)
	output = filepath.Join(dir, fmt.Sprintf(ffmpegutils.SceneEncodedOutputFormat, scene, startQP))
	report = output + "_vmaf.json"
	if valid, err = sceneQP(input, output, report, frameRate, totalFrames, scene, startQP, auditor, ultraHD, videoCUDA, VMAFCUDA, gpu); err != nil {
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
			if valid, err = sceneQP(input, output, report, frameRate, totalFrames, scene, QPCandidate, auditor, ultraHD, videoCUDA, VMAFCUDA, gpu); err != nil {
				err = fmt.Errorf("failed to test QP %d: %w", QPCandidate, err)
				return
			}
			// If the new QP is invalid, we return the previous one
			if !valid {
				// We reach an invalid QP, let's use the previous valid QP
				fmt.Fprintf(bypass, "Scene %d: QP %d is not good enough, rolling back to QP %d\n",
					scene, QPCandidate, lastValid)
				finalQP = lastValid
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
			if valid, err = sceneQP(input, output, report, frameRate, totalFrames, scene, QPCandidate, auditor, ultraHD, videoCUDA, VMAFCUDA, gpu); err != nil {
				err = fmt.Errorf("failed to test QP %d: %w", QPCandidate, err)
				return
			}
			// We found a valid QP after encountering an invalid QP, let's use it
			if valid {
				fmt.Fprintf(bypass, "Scene %d: QP %d is finaly good enough, keeping it\n",
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

func sceneQP(input, output, vmafReportPath, frameRate string, totalFrames, sceneID, qp int, auditor VMAFChecker, ultraHD, videoCUDA, VMAFCUDA bool, gpu int) (valid bool, err error) {
	// Encode
	if err = encodeQP(input, output, totalFrames, qp, videoCUDA, gpu); err != nil {
		err = fmt.Errorf("failed to encode scene at QP %d: %w", qp, err)
		return
	}
	// Compute VMAF
	var vmaf ffmpegutils.VMAFStats
	if vmaf, err = computeVMAF(output, input, vmafReportPath, frameRate, totalFrames, ultraHD, videoCUDA, VMAFCUDA, gpu); err != nil {
		err = fmt.Errorf("failed to compute VMAF for scene at QP %d: %w", qp, err)
		return
	}
	// Check
	valid = auditor.Validate(vmaf)
	if *debug {
		fmt.Fprintf(liveprogress.Bypass(), "Scene %d: QP %d: VMAF results:\n%s", sceneID, qp, vmaf.String())
	}
	return
}
