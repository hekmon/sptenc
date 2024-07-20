package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/hekmon/ffmpegutils"
	"github.com/hekmon/liveprogress/v2"
)

func debugPrint(s string) {
	if *debug {
		fmt.Fprintf(liveprogress.Bypass(), "%s\n", s)
	}
}

func runtimeError(err error) {
	fmt.Fprintf(liveprogress.Bypass(), "%s\n", err)
}

func getStreamsInfos(path string) (stats ffmpegutils.FFProbeStats, err error) {
	return ffmpegutils.GetStreamsInfos(ffmpegutils.GetStreamsInfosConfig{
		// Input
		Path: path,
		// Reporting
		Debug:               debugPrint,
		RuntimeError:        runtimeError,
		ProcessRegistration: children.ProcessRegistration,
	})
}

func getScenes(path string, totalDuration time.Duration, threshold int, cudaVideo bool, gpuID *int) (scenes []*ffmpegutils.Scene, err error) {
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
	// Execute
	start := time.Now()
	if scenes, err = ffmpegutils.ScenesDetection(ffmpegutils.ScenesDetectionConfig{
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
	}); err != nil {
		return
	}
	// Done
	duration := time.Since(start)
	fmt.Fprintf(liveprogress.Bypass(), "Found %d scenes in %s\n", len(scenes)+1, duration.Round(time.Minute))
	return
}

func splitScenes(path, outputDir string, scenes []*ffmpegutils.Scene) (err error) {
	// Prepare
	markers := make([]float64, len(scenes))
	for i, scene := range scenes {
		markers[i] = scene.Start.Seconds()
	}
	// Execute
	start := time.Now()
	if err = ffmpegutils.ScenesSegment(ffmpegutils.ScenesSegmentConfig{
		// Input / output
		Input: path,
		// Output
		ScenesMarkers: markers,
		OutputDir:     outputDir,
		// Reporting
		Debug:               debugPrint,
		RuntimeError:        runtimeError,
		ProcessRegistration: children.ProcessRegistration,
		FFMPEGStatsReport:   nil,
	}); err != nil {
		return
	}
	// Done
	duration := time.Since(start)
	fmt.Fprintf(liveprogress.Bypass(), "Scenes slicing done in %s\n", duration.Round(time.Minute))
	return
}
