package pipeline

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"

	"github.com/hekmon/sptenc/core"
	"github.com/hekmon/sptenc/ffmpeg"
	"github.com/hekmon/sptenc/transnetv2"
)

// TransNetV2ScenesConfig holds the configuration for DetectScenesTransNetV2.
type TransNetV2ScenesConfig struct {
	// Input
	Path string
	// Threshold is on the transnetv2 scale (see SceneDetectorTransNetV2): a probability, from
	// transnetv2.ProbabilityFloor to 1.
	Threshold float64
	// FrameRate is the frame rate of the first video stream, as ffprobe's r_frame_rate
	// ("24000/1001"): the Start of each scene is computed from it and its frame index.
	FrameRate string
	// HWDecoder decodes the frames fed to the helper (the decoders incompatible with the codec of
	// the input are ignored by ffmpeg.FeedFrames: software decode).
	HWDecoder ffmpeg.HWDecoderConfig
	// Reporting
	Debug        func(msg string)
	RuntimeError func(err error) // non fatal errors
	// FramesFedReport is given the number of frames written to the helper so far (see
	// ffmpeg.FeedFramesConfig.FramesReport), ProbabilitiesReport the number of probabilities it
	// returned so far (see transnetv2.DetectConfig.ProgressReport). The helper answers late by
	// design (the model works on windows of frames): the probabilities trail the frames fed, and
	// only they tell how much of the detection is done. Both are called from other goroutines.
	FramesFedReport     func(frames int)
	ProbabilitiesReport func(probabilities int)
}

// DetectScenesTransNetV2 detects the scenes of a file with TransNetV2: every frame of its first
// video stream is decoded and scaled by ffmpeg (ffmpeg.FeedFrames) into the helper
// (transnetv2.Detect), whose probabilities are turned into cuts by sptenc (transnetv2.Cuts). It
// returns what ffmpeg.ScenesDetection returns with scdet, with the same meaning: the scenes whose
// score reaches config.Threshold, ordered, each with Frame the index (from 0) of the first frame
// of the new scene, Start the time of that frame from the first frame, and Score.
//
// It fails when the feed does (ffmpeg, a picture change mid-stream refused as scene detection
// refuses it with scdet, see ffmpeg.FeedFrames) and when the helper does, with the cause of each.
//
// # WHY THE FRAME INDEX IS THE ONE OF THE FEED
//
// The probabilities come back one per frame fed, in order, and the feed writes every frame of the
// first video stream once (see ffmpeg.FeedFrames, WHY -fps_mode passthrough): probability N is
// the one of frame N of the master, so a cut's frame is a frame of the master, which is what cuts
// (see ffmpeg.Scene). Its Start is computed from that index and the frame rate (VFR sources are
// refused before any detection), not read from a timestamp: the helper sees no timestamp.
//
// # WHY THE THRESHOLD IS COMPARED WITH THE ROUNDED SCORE
//
// A cut's score is rounded by sptenc (transnetv2.Cuts, WHY THE SCORE IS ROUNDED) and the
// threshold is compared with that very score, the one printed and recorded. So a score handed
// back as a threshold keeps its scene, and every scene scoring less is printed less: a score is
// its own reusable threshold (see SceneDetector.ReusableThreshold), where scdet's are not.
func DetectScenesTransNetV2(ctx context.Context, config TransNetV2ScenesConfig) (scenes []ffmpeg.Scene, err error) {
	// Validate inputs
	if config.Path == "" {
		err = errors.New("input path cannot be empty")
		return
	}
	if err = SceneDetectorTransNetV2.ValidateThreshold(config.Threshold); err != nil {
		err = fmt.Errorf("invalid threshold %s: %w", SceneDetectorTransNetV2.FormatThreshold(config.Threshold), err)
		return
	}
	if _, err = core.FramesDuration(0, config.FrameRate); err != nil {
		return
	}
	// The frames the helper reads: a property of the detector
	width, height := SceneDetectorTransNetV2.HelperFrameSize()
	feed := func(ctx context.Context, frames io.Writer) (int, error) {
		return ffmpeg.FeedFrames(ctx, ffmpeg.FeedFramesConfig{
			Path:            config.Path,
			Width:           width,
			Height:          height,
			Output:          frames,
			NVDec:           config.HWDecoder.NVDec,
			NVDevice:        config.HWDecoder.NVDevice,
			VAAPIDec:        config.HWDecoder.VAAPIDec,
			VAAPIDevice:     config.HWDecoder.VAAPIDevice,
			D3D12Dec:        config.HWDecoder.D3D12Dec,
			D3D12Device:     config.HWDecoder.D3D12Device,
			VideoToolboxDec: config.HWDecoder.VideoToolboxDec,
			Debug:           config.Debug,
			RuntimeError:    config.RuntimeError,
			FramesReport:    config.FramesFedReport,
		})
	}
	// Detect
	probabilities, err := transnetv2.Detect(ctx, transnetv2.DetectConfig{
		Feed:           feed,
		Debug:          config.Debug,
		RuntimeError:   config.RuntimeError,
		ProgressReport: config.ProbabilitiesReport,
	})
	if err != nil {
		return
	}
	if scenes, err = transnetv2Scenes(probabilities, config.Threshold, config.FrameRate); err != nil {
		return
	}
	if config.Debug != nil {
		for i, scene := range scenes {
			config.Debug(fmt.Sprintf("Scene %d detected at frame %d (%v) with score %s",
				i+2, scene.Frame, scene.Start, strconv.FormatFloat(scene.Score, 'f', -1, 64)))
		}
	}
	return
}

// transnetv2Scenes turns the probabilities of the helper into the scenes whose score reaches the
// threshold, see WHY THE THRESHOLD IS COMPARED WITH THE ROUNDED SCORE in DetectScenesTransNetV2.
func transnetv2Scenes(probabilities []float32, threshold float64, frameRate string) (scenes []ffmpeg.Scene, err error) {
	cuts, err := transnetv2.Cuts(probabilities)
	if err != nil {
		return nil, err
	}
	for _, cut := range cuts {
		if cut.Score < threshold {
			continue
		}
		start, durationErr := core.FramesDuration(cut.Frame, frameRate)
		if durationErr != nil {
			return nil, durationErr
		}
		scenes = append(scenes, ffmpeg.Scene{Frame: cut.Frame, Start: start, Score: cut.Score})
	}
	return
}
