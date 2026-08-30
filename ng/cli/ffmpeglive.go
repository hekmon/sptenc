package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/hekmon/sptenc/ng/ffmpeg"

	"github.com/hekmon/cunits/v3"
	"github.com/hekmon/liveprogress/v2"
)

/*
 * Master
 */

func liveCountNbFrames(ctx context.Context, inputFilePath string, debug bool) (nbFrames int, codec string, duration time.Duration, err error) {
	// prepare live progress
	analyzeBar := liveprogress.AddBar(
		liveprogress.WithTotal(uint64(ctx.Value(inputFileSizeCtxKey).(int64))),
		liveprogress.WithMultiplyRunes(),
		// liveprogress.WithWidth(barsWidth),
		liveprogress.WithSameAutoSizeInternalPadding(true, false),
		liveprogress.WithPrependDecorator(func(bar *liveprogress.Bar) string {
			return "   Analyze | "
		}),
		liveprogress.WithPrependTimeElapsed(liveprogress.BaseStyle()),
		liveprogress.WithAppendPercent(liveprogress.BaseStyle()),
		liveprogress.WithAppendTimeRemaining(liveprogress.BaseStyle()),
		liveprogress.WithAppendDecorator(func(bar *liveprogress.Bar) string {
			return fmt.Sprintf(" left | %s/%s",
				cunits.ImportInBytes(float64(bar.Current())), cunits.ImportInBytes(float64(bar.Total())),
			)
		}),
	)
	defer liveprogress.RemoveBar(analyzeBar)
	analyzeProgress := func(n int) {
		analyzeBar.CurrentAdd(uint64(n))
	}
	// exec ffprobe
	mediaInfos, err := ffmpeg.GetStreamsInfosCF(ctx, ffmpeg.GetStreamsInfosCFConfig{
		GetStreamsInfosConfig: ffmpeg.GetStreamsInfosConfig{
			// Input
			Path: inputFilePath,
			// Reporting
			Debug: func(s string) {
				if debug {
					fmt.Fprintf(liveprogress.Bypass(), "DEBUG: %s\n", s)
				}
			},
			RuntimeError: func(err error) {
				fmt.Fprintf(liveprogress.Bypass(), "ERROR: %s\n", err)
			},
		},
		ReadBytesReport: analyzeProgress,
	})
	if err != nil {
		return
	}
	duration = mediaInfos.Format.Duration
	// extract number of frames from results
	videoInfos := mediaInfos.VideoTrack()
	if videoInfos == nil {
		err = errors.New("input file has no video stream")
		return
	}
	nbFrames = videoInfos.NbReadFrames
	codec = string(videoInfos.CodecName)
	return
}

func liveFFV1Master(ctx context.Context, inputFilePath, finalFile string, nbFrames int, debug bool) (err error) {
	// prepare live progress
	encodeBar := liveprogress.AddBar(
		liveprogress.WithTotal(uint64(nbFrames)),
		liveprogress.WithMultiplyRunes(),
		// liveprogress.WithWidth(barsWidth),
		liveprogress.WithSameAutoSizeInternalPadding(true, false),
		liveprogress.WithPrependDecorator(func(bar *liveprogress.Bar) string {
			return "    Encode | "
		}),
		liveprogress.WithPrependTimeElapsed(liveprogress.BaseStyle()),
		liveprogress.WithAppendPercent(liveprogress.BaseStyle()),
		liveprogress.WithAppendTimeRemaining(liveprogress.BaseStyle()),
		liveprogress.WithAppendDecorator(func(bar *liveprogress.Bar) string {
			return fmt.Sprintf(" | %d/%d frames", bar.Current(), bar.Total())
		}),
	)
	defer liveprogress.RemoveBar(encodeBar)
	progress := func(stats ffmpeg.ProgressStats) {
		encodeBar.CurrentSet(uint64(stats.CurrentFrame))
	}
	// encode
	return ffmpeg.FFV1VideoMaster(ctx, ffmpeg.FFV1VideoMasterConfig{
		InputFilePath:  inputFilePath,
		OutputFilePath: finalFile,
		Debug: func(s string) {
			if debug {
				fmt.Fprintf(liveprogress.Bypass(), "DEBUG: %s\n", s)
			}
		},
		RuntimeError: func(err error) {
			fmt.Fprintf(liveprogress.Bypass(), "ERROR: %s\n", err)
		},
		StatsReport: progress,
	})
}

/*
 * Split
 */

func liveDetectScenes(ctx context.Context, path string, threshold float64, totalDuration time.Duration, debug bool) (scenes []ffmpeg.Scene, err error) {
	// live progress
	var currentStats ffmpeg.ProgressStats
	bar := liveprogress.AddBar(
		liveprogress.WithTotal(uint64(totalDuration)),
		liveprogress.WithMultiplyRunes(),
		// liveprogress.WithWidth(barsWidth),
		liveprogress.WithSameAutoSizeInternalPadding(true, false),
		liveprogress.WithPrependDecorator(func(bar *liveprogress.Bar) string {
			return " Detecting | "
		}),
		liveprogress.WithPrependTimeElapsed(liveprogress.BaseStyle()),
		liveprogress.WithAppendPercent(liveprogress.BaseStyle()),
		liveprogress.WithAppendTimeRemaining(liveprogress.BaseStyle()),
		liveprogress.WithAppendDecorator(func(bar *liveprogress.Bar) string {
			return fmt.Sprintf(" left | speed: %0.2fx",
				currentStats.Speed,
			)
		}),
	)
	defer liveprogress.RemoveBar(bar)
	progress := func(stats ffmpeg.ProgressStats) {
		currentStats = stats
		bar.CurrentSet(uint64(stats.Time))
	}
	// Execute scene detection
	scenes, err = ffmpeg.ScenesDetection(ctx, ffmpeg.ScenesDetectionConfig{
		// Input
		Path: path,
		// scdet
		Threshold: threshold,
		// Reporting
		Debug: func(s string) {
			if debug {
				fmt.Fprintf(liveprogress.Bypass(), "DEBUG: %s\n", s)
			}
		},
		RuntimeError: func(err error) {
			fmt.Fprintf(liveprogress.Bypass(), "ERROR: %s\n", err)
		},
		FFMPEGStatsReport: progress,
	})
	return
}

func liveSplitScenes(ctx context.Context, path, outputDir string, totalDuration time.Duration, scenes []ffmpeg.Scene, debug bool) (err error) {
	// live progress for splitting
	var currentStats ffmpeg.ProgressStats
	bar := liveprogress.AddBar(
		liveprogress.WithTotal(uint64(totalDuration)),
		liveprogress.WithMultiplyRunes(),
		// liveprogress.WithWidth(barsWidth),
		// liveprogress.WithSameAutoSizeInternalPadding(true, false),
		liveprogress.WithPrependDecorator(func(bar *liveprogress.Bar) string {
			return " Splitting | "
		}),
		liveprogress.WithPrependTimeElapsed(liveprogress.BaseStyle()),
		liveprogress.WithAppendPercent(liveprogress.BaseStyle()),
		liveprogress.WithAppendTimeRemaining(liveprogress.BaseStyle()),
		liveprogress.WithAppendDecorator(func(bar *liveprogress.Bar) string {
			return fmt.Sprintf(" left | speed: %0.2fx",
				currentStats.Speed,
			)
		}),
	)
	defer liveprogress.RemoveBar(bar)
	progress := func(stats ffmpeg.ProgressStats) {
		currentStats = stats
		bar.CurrentSet(uint64(stats.Time))
	}
	// extract timestamps
	scenesMarkers := make([]time.Duration, len(scenes))
	for i, scene := range scenes {
		scenesMarkers[i] = scene.Start
	}
	// Execute segmentation
	return ffmpeg.Segment(ctx, ffmpeg.SegmentConfig{
		// Input
		Input: path,
		// Output
		ScenesMarkers: scenesMarkers,
		OutputDir:     outputDir,
		// Reporting
		Debug: func(s string) {
			if debug {
				fmt.Fprintf(liveprogress.Bypass(), "DEBUG: %s\n", s)
			}
		},
		RuntimeError: func(err error) {
			fmt.Fprintf(liveprogress.Bypass(), "ERROR: %s\n", err)
		},
		FFMPEGStatsReport: progress,
	})
}

/*
 * Encode
 */

func getStreamsInfos(ctx context.Context, path string, debug bool) (stats ffmpeg.FFProbeStats, err error) {
	return ffmpeg.GetStreamsInfos(ctx, ffmpeg.GetStreamsInfosConfig{
		// Input
		Path: path,
		// Reporting
		Debug: func(s string) {
			if debug {
				fmt.Fprintf(liveprogress.Bypass(), "DEBUG: %s\n", s)
			}
		},
		RuntimeError: func(err error) {
			fmt.Fprintf(liveprogress.Bypass(), "ERROR: %s\n", err)
		},
	})
}
