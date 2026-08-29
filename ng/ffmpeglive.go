package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/hekmon/sptenc/ng/ffmpeg"

	"github.com/hekmon/cunits/v3"
	"github.com/hekmon/liveprogress/v2"
)

func liveCountNbFrames(ctx context.Context, inputFilePath string, debug func(msg string), runtimeError func(err error)) (nbFrames int, codec string, err error) {
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
			Debug:        debug,
			RuntimeError: runtimeError,
		},
		ReadBytesReport: analyzeProgress,
	})
	if err != nil {
		err = fmt.Errorf("ffprobe execution error: %w", err)
		return
	}
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

func liveFFV1Master(ctx context.Context, inputFilePath, finalFile string, nbFrames int, debug func(msg string), runtimeError func(err error)) (err error) {
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
	if err = ffmpeg.FFV1VideoMaster(ctx, ffmpeg.FFV1VideoMasterConfig{
		InputFilePath:  inputFilePath,
		OutputFilePath: finalFile,
		Debug:          debug,
		RuntimeError:   runtimeError,
		StatsReport:    progress,
	}); err != nil {
		return fmt.Errorf("failed to encode ffv1 master: %w", err)
	}
	return
}
