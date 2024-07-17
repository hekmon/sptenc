package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/hekmon/ffmpegutils"
	"github.com/hekmon/liveprogress/v2"
)

func getStreamsInfos(path string) (stats ffmpegutils.FFProbeStats, err error) {
	// output fx
	bypass := liveprogress.Bypass()
	runtimeError := func(err error) {
		fmt.Fprintf(bypass, "%s\n", err)
	}
	var debugPrint func(string)
	if *debug {
		debugPrint = func(s string) {
			fmt.Fprintf(bypass, "%s\n", s)
		}
	}
	// execute
	return ffmpegutils.GetStreamsInfos(ffmpegutils.GetStreamsInfosConfig{
		Path:                path,
		Debug:               debugPrint,
		RuntimeError:        runtimeError,
		ProcessRegistration: children.ProcessRegistration,
	})
}

func getScenes(path string, totalDuration time.Duration, threshold int, cudaVideo bool, gpuID *int) (scenes []*ffmpegutils.Scene, err error) {
	// reporting
	bypass := liveprogress.Bypass()
	runtimeError := func(err error) {
		fmt.Fprintf(bypass, "%s\n", err)
	}
	var debugPrint func(string)
	if *debug {
		debugPrint = func(s string) {
			fmt.Fprintf(bypass, "%s\n", s)
		}
	}
	// live progress
	var currentStats ffmpegutils.ProgressStats
	bar := liveprogress.AddBar(
		liveprogress.WithTotal(uint64(totalDuration)),
		liveprogress.WithLineFillRunes(),
		// liveprogress.WithWidth(barsWidth),
		liveprogress.WithPrependDecorator(func(bar *liveprogress.Bar) string {
			return "  Scene detections | "
		}),
		liveprogress.WithPrependTimeElapsed(liveprogress.BaseStyle()),
		liveprogress.WithAppendPercent(liveprogress.BaseStyle()),
		liveprogress.WithAppendTimeRemaining(liveprogress.BaseStyle()),
		liveprogress.WithAppendDecorator(func(bar *liveprogress.Bar) string {
			var build strings.Builder
			build.WriteString(fmt.Sprintf(" remaining | %d frames processed (%0.0f fps, speed: %0.2fx)",
				currentStats.CurrentFrame, currentStats.FPS, currentStats.Speed,
			))
			return build.String()
		}),
	)
	defer liveprogress.RemoveBar(bar)
	progress := func(stats ffmpegutils.ProgressStats) {
		currentStats = stats
		bar.CurrentSet(uint64(stats.Time))
	}
	// execute
	return ffmpegutils.ScenesDetection(ffmpegutils.ScenesDetectionConfig{
		// Input
		Path: path,
		// scdet
		Threshold: threshold,
		VideoCuda: cudaVideo,
		GPUID:     gpuID,
		// Reporting
		Debug:               debugPrint,
		RuntimeError:        runtimeError,
		ProcessRegistration: children.ProcessRegistration,
		FFMPEGStatsReport:   progress,
	})
}
