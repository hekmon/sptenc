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

func findScenesQP(dir string, nbScenes, qp int, auditor VMAFChecker, videoCUDA, VMAFCUDA bool, gpu int) (results []int, err error) {
	// Prepare
	results = make([]int, nbScenes)
	bypass := liveprogress.Bypass()
	// Live progress
	bar := liveprogress.SetMainLineAsBar(
		liveprogress.WithTotal(uint64(nbScenes)),
		liveprogress.WithLineFillRunes(),
		// liveprogress.WithWidth(barsWidth),
		liveprogress.WithPrependDecorator(func(bar *liveprogress.Bar) string {
			return "   Scenes | "
		}),
		liveprogress.WithPrependTimeElapsed(liveprogress.BaseStyle()),
		liveprogress.WithAppendPercent(liveprogress.BaseStyle()),
		liveprogress.WithAppendTimeRemaining(liveprogress.BaseStyle()),
		liveprogress.WithAppendDecorator(func(bar *liveprogress.Bar) string {
			return fmt.Sprintf(" remaining | %d/%d scenes completed",
				bar.Current(), bar.Total(),
			)
		}),
	)
	defer liveprogress.RemoveBar(bar)
	// Go
	start := time.Now()
	for scene := 0; scene < nbScenes; scene++ {
		if qp, err = findSceneQP(dir, scene, qp, auditor, videoCUDA, VMAFCUDA, gpu); err != nil {
			err = fmt.Errorf("failed to find the right scene %d encoding QP: %w", scene, err)
			return
		}
		results[scene] = qp
		fmt.Fprintf(bypass, "Scene %d: QP %d will be used\n", scene, qp)
		bar.CurrentIncrement()
	}
	duration := time.Since(start)
	fmt.Fprintf(bypass, "Scenes encoding QP search done in %s\n", duration.Round(time.Second))
	return
}

func findSceneQP(dir string, scene, QPCandidate int, auditor VMAFChecker, videoCUDA, VMAFCUDA bool, gpu int) (qp int, err error) {
	// Prepare
	input := filepath.Join(dir, fmt.Sprintf(ffmpegutils.SceneOutputFormat, scene))
	sceneInfos, err := getStreamsInfosCF(input)
	if err != nil {
		err = fmt.Errorf("failed to get streams infos: %w", err)
		return
	}
	videoTrack := sceneInfos.VideoTrack()
	totalFrames, err := strconv.Atoi(videoTrack.NbReadFrames)
	if err != nil {
		err = fmt.Errorf("failed to get total frames: %w", err)
		return
	}
	frameRate := videoTrack.RFrameRate
	ultraHD := videoTrack.Height >= ffmpegutils.UltraHDHeight
	// Find the right QP
	var (
		valid                  bool
		lastInvalid, lastValid int
	)
	bypass := liveprogress.Bypass()
	for {
		// Encode
		output := filepath.Join(dir, fmt.Sprintf(ffmpegutils.SceneEncodedOutputFormat, scene, QPCandidate))
		report := output + "_vmaf.json"
		if valid, err = sceneQP(input, output, report, frameRate, totalFrames, scene, QPCandidate, auditor, ultraHD, videoCUDA, VMAFCUDA, gpu); err != nil {
			return
		}
		// Handle result
		if valid {
			if lastInvalid != 0 {
				// previous QP was invalid, so we got our first validQP
				qp = QPCandidate
				return
			}
			// We have a new valid QP, remove the old one if it exists
			if lastValid != 0 {
				if err = os.Remove(filepath.Join(dir, fmt.Sprintf(ffmpegutils.SceneEncodedOutputFormat, scene, lastValid))); err != nil {
					err = fmt.Errorf("failed to remove previous valid QP at %s: %w", output, err)
					return
				}
			}
			// Let's try to increase QP a bit more to see if we can decrease the size
			lastValid = QPCandidate
			QPCandidate++
			fmt.Fprintf(bypass, "Scene %d: QP %d is good enough, let's try to decrease quality with QP %d\n",
				scene, lastValid, QPCandidate)
		} else {
			// We failed to score a good enough VMAF
			if lastValid != 0 {
				// We already had a valid QP and decreasing quality is not working anymore. We are done
				qp = lastValid
				return
			}
			// Current QP is not good enough, let's increase quality
			lastInvalid = QPCandidate
			QPCandidate--
			if QPCandidate < 0 {
				fmt.Fprintf(liveprogress.Bypass(), "WARNING: Keeping QP 0 as it is not possible to do better\n")
				qp = 0
				return
			}
			fmt.Fprintf(bypass, "Scene %d: QP %d is not good enough, let's try to increase quality with QP %d\n",
				scene, lastInvalid, QPCandidate)
			// Remove the current QP as it is invalid
			if err = os.Remove(output); err != nil {
				err = fmt.Errorf("failed to remove invalid QP at %s: %w", output, err)
				return
			}
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
