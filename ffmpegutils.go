package main

import (
	"fmt"
	"os"
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

func getStreamsInfosCF(path string) (stats ffmpegutils.FFProbeStats, err error) {
	// Prepare
	fileInfos, err := os.Stat(path)
	if err != nil {
		err = fmt.Errorf("failed to stat the file: %w", err)
		return
	}
	// Live Progress
	var currentStats ffmpegutils.ProgressStats
	bar := liveprogress.AddBar(
		liveprogress.WithTotal(uint64(fileInfos.Size())),
		liveprogress.WithLineFillRunes(),
		// liveprogress.WithWidth(barsWidth),
		liveprogress.WithPrependDecorator(func(bar *liveprogress.Bar) string {
			return " Analyze | "
		}),
		// liveprogress.WithPrependTimeElapsed(liveprogress.BaseStyle()),
		liveprogress.WithAppendPercent(liveprogress.BaseStyle()),
		// liveprogress.WithAppendTimeRemaining(liveprogress.BaseStyle()),
		liveprogress.WithAppendDecorator(func(bar *liveprogress.Bar) string {
			var build strings.Builder
			build.WriteString(fmt.Sprintf(" | %d frames (%0.0f fps, speed: %0.2fx)",
				currentStats.CurrentFrame, currentStats.FPS, currentStats.Speed,
			))
			return build.String()
		}),
	)
	defer liveprogress.RemoveBar(bar)
	progress := func(n int) {
		bar.CurrentSet(uint64(n))
	}
	// Execute
	return ffmpegutils.GetStreamsInfosCF(ffmpegutils.GetStreamsInfosCFConfig{
		GetStreamsInfosConfig: ffmpegutils.GetStreamsInfosConfig{
			// Input
			Path: path,
			// Reporting
			Debug:               debugPrint,
			RuntimeError:        runtimeError,
			ProcessRegistration: children.ProcessRegistration,
		},
		ReadBytesReport: progress,
	})
}

func getScenes(path string, totalDuration time.Duration, threshold float64, cudaVideo bool, gpuID *int) (scenes []*ffmpegutils.Scene, err error) {
	// live progress
	var currentStats ffmpegutils.ProgressStats
	bar := liveprogress.AddBar(
		liveprogress.WithTotal(uint64(totalDuration)),
		liveprogress.WithLineFillRunes(),
		// liveprogress.WithWidth(barsWidth),
		liveprogress.WithPrependDecorator(func(bar *liveprogress.Bar) string {
			return " Detection | "
		}),
		liveprogress.WithPrependTimeElapsed(liveprogress.BaseStyle()),
		liveprogress.WithAppendPercent(liveprogress.BaseStyle()),
		liveprogress.WithAppendTimeRemaining(liveprogress.BaseStyle()),
		liveprogress.WithAppendDecorator(func(bar *liveprogress.Bar) string {
			var build strings.Builder
			build.WriteString(fmt.Sprintf(" left | %d frames (%0.0f fps, speed: %0.2fx)",
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
	duration := time.Since(start)
	// Done
	fmt.Fprintf(liveprogress.Bypass(), "Found %d scenes in %s\n", len(scenes)+1, duration.Round(time.Second))
	return
}

func splitScenes(path, outputDir string, totalDuration time.Duration, scenes []*ffmpegutils.Scene) (err error) {
	// Prepare
	markers := make([]float64, len(scenes))
	for i, scene := range scenes {
		markers[i] = scene.Start.Seconds()
	}
	// live progress
	var currentStats ffmpegutils.ProgressStats
	bar := liveprogress.AddBar(
		liveprogress.WithTotal(uint64(totalDuration)),
		liveprogress.WithLineFillRunes(),
		// liveprogress.WithWidth(barsWidth),
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
	progress := func(stats ffmpegutils.ProgressStats) {
		currentStats = stats
		bar.CurrentSet(uint64(stats.Time))
	}
	// Execute
	start := time.Now()
	if err = ffmpegutils.ScenesSegment(ffmpegutils.ScenesSegmentConfig{
		// Input
		Input: path,
		// Output
		ScenesMarkers:   markers,
		OutputDir:       outputDir,
		ResetTimestamps: true,
		// Reporting
		Debug:               debugPrint,
		RuntimeError:        runtimeError,
		ProcessRegistration: children.ProcessRegistration,
		FFMPEGStatsReport:   progress,
	}); err != nil {
		return
	}
	duration := time.Since(start)
	// Done
	fmt.Fprintf(liveprogress.Bypass(), "Scenes slicing done in %s\n", duration.Round(time.Second))
	return
}

func encodeQP(input, output string, totalFrames, qp int, cuda bool, gpu int) (err error) {
	// live progress
	var currentStats ffmpegutils.ProgressStats
	bar := liveprogress.AddBar(
		liveprogress.WithTotal(uint64(totalFrames)),
		liveprogress.WithLineFillRunes(),
		// liveprogress.WithWidth(barsWidth),
		liveprogress.WithPrependDecorator(func(bar *liveprogress.Bar) string {
			return "  Encode | "
		}),
		// liveprogress.WithPrependTimeElapsed(liveprogress.BaseStyle()),
		liveprogress.WithAppendPercent(liveprogress.BaseStyle()),
		// liveprogress.WithAppendTimeRemaining(liveprogress.BaseStyle()),
		liveprogress.WithAppendDecorator(func(bar *liveprogress.Bar) string {
			var build strings.Builder
			build.WriteString(fmt.Sprintf(" | %d frames (%0.0f fps, speed: %0.2fx)",
				currentStats.CurrentFrame, currentStats.FPS, currentStats.Speed,
			))
			return build.String()
		}),
	)
	defer liveprogress.RemoveBar(bar)
	progress := func(stats ffmpegutils.ProgressStats) {
		currentStats = stats
		bar.CurrentSet(uint64(stats.CurrentFrame))
	}
	// Execute
	start := time.Now()
	if err = ffmpegutils.Encode(ffmpegutils.EncodeConfig{
		// Input
		Input: input,
		// Output
		OutputFilePath: output,
		Quantization:   qp,
		Tags:           nil,
		// Hardware Acceleration
		NVDECENC: cuda,
		GPUID:    &gpu,
		// Reporting
		Debug:               debugPrint,
		RuntimeError:        runtimeError,
		ProcessRegistration: children.ProcessRegistration,
		FFMPEGStatsReport:   progress,
	}); err != nil {
		return
	}
	duration := time.Since(start)
	// Done
	if *debug {
		fmt.Fprintf(liveprogress.Bypass(), "Scene encoded in %s\n", duration.Round(time.Second))
	}
	return
}

func computeVMAF(distorted, reference, reportPath, frameRate string, totalFrames int, ultraHD, NEG, videoCUDA, VMAFCUDA bool, gpu int) (vmaf ffmpegutils.VMAFStats, err error) {
	// live progress
	var currentStats ffmpegutils.ProgressStats
	bar := liveprogress.AddBar(
		liveprogress.WithTotal(uint64(totalFrames)),
		liveprogress.WithLineFillRunes(),
		// liveprogress.WithWidth(barsWidth),
		liveprogress.WithPrependDecorator(func(bar *liveprogress.Bar) string {
			return "    VMAF | "
		}),
		// liveprogress.WithPrependTimeElapsed(liveprogress.BaseStyle()),
		liveprogress.WithAppendPercent(liveprogress.BaseStyle()),
		// liveprogress.WithAppendTimeRemaining(liveprogress.BaseStyle()),
		liveprogress.WithAppendDecorator(func(bar *liveprogress.Bar) string {
			var build strings.Builder
			build.WriteString(fmt.Sprintf(" | %d frames (%0.0f fps, speed: %0.2fx)",
				currentStats.CurrentFrame, currentStats.FPS, currentStats.Speed,
			))
			return build.String()
		}),
	)
	defer liveprogress.RemoveBar(bar)
	progress := func(stats ffmpegutils.ProgressStats) {
		currentStats = stats
		bar.CurrentSet(uint64(stats.CurrentFrame))
	}
	// Execute
	start := time.Now()
	report, err := ffmpegutils.VMAFCompute(ffmpegutils.VMAFComputeConfig{
		// Input
		ReferencePath:  reference,
		InputFrameRate: frameRate,
		DistortedPath:  distorted,
		// VMAF generation
		ReportPath:        reportPath,
		UltraHD:           ultraHD,
		VideoCuda:         videoCUDA,
		NoEnhancementGain: NEG,
		VMAFCuda:          VMAFCUDA,
		GPUID:             &gpu,
		// Reporting
		Debug:               debugPrint,
		RuntimeError:        runtimeError,
		ProcessRegistration: children.ProcessRegistration,
		FFMPEGStatsReport:   progress,
	})
	if err != nil {
		return
	}
	duration := time.Since(start)
	// Done
	vmaf = report.GetStats()
	if *debug {
		fmt.Fprintf(liveprogress.Bypass(), "Scene VMAF computed in %s\n", duration.Round(time.Second))
	}
	return
}

func scenesMerge(originalFile, scenesDir, outputPath string, tagsFlags []string, qps []int, expectedDuration time.Duration) (err error) {
	// Generate the concat script
	filesnames := make([]string, len(qps))
	for scene, qp := range qps {
		filesnames[scene] = fmt.Sprintf(ffmpegutils.SceneEncodedOutputFormat, scene, qp)
	}
	concatScript, err := ffmpegutils.GenerateConcatScript(scenesDir, filesnames)
	if err != nil {
		err = fmt.Errorf("failed to create the concat script file: %w", err)
		return
	}
	// live progress
	var currentStats ffmpegutils.ProgressStats
	bar := liveprogress.AddBar(
		liveprogress.WithTotal(uint64(expectedDuration)),
		liveprogress.WithLineFillRunes(),
		// liveprogress.WithWidth(barsWidth),
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
	progress := func(stats ffmpegutils.ProgressStats) {
		currentStats = stats
		bar.CurrentSet(uint64(stats.Time))
	}
	// Execute
	start := time.Now()
	if err = ffmpegutils.ScenesRemux(ffmpegutils.ScenesRemuxConfig{
		// Input
		OriginalFile:       originalFile,
		ScenesConcatScript: concatScript,
		// Output
		OutputFilePath: outputPath,
		Tags:           tagsFlags,
		// Reporting
		Debug:               debugPrint,
		RuntimeError:        runtimeError,
		ProcessRegistration: children.ProcessRegistration,
		FFMPEGStatsReport:   progress,
	}); err != nil {
		return
	}
	duration := time.Since(start)
	// Done
	if *debug {
		fmt.Fprintf(liveprogress.Bypass(), "Scenes remuxed within %q in %s\n", outputPath, duration.Round(time.Second))
	} else {
		fmt.Fprintf(liveprogress.Bypass(), "Scenes remuxed in %s\n", duration.Round(time.Second))
	}
	return
}

func regenerateMKVStats(path string) (err error) {
	// live progress
	bar := liveprogress.AddBar(
		liveprogress.WithTotal(uint64(100)),
		liveprogress.WithLineFillRunes(),
		// liveprogress.WithWidth(barsWidth),
		liveprogress.WithPrependDecorator(func(bar *liveprogress.Bar) string {
			return " MKV Stats | "
		}),
		liveprogress.WithPrependTimeElapsed(liveprogress.BaseStyle()),
		liveprogress.WithAppendPercent(liveprogress.BaseStyle()),
		liveprogress.WithAppendTimeRemaining(liveprogress.BaseStyle()),
		liveprogress.WithAppendDecorator(func(bar *liveprogress.Bar) string {
			return " remaining"
		}),
	)
	defer liveprogress.RemoveBar(bar)
	progress := func(percent int) {
		bar.CurrentSet(uint64(percent))
	}
	// Execute
	start := time.Now()
	if err = ffmpegutils.GenerateMKVStats(ffmpegutils.GenerateMKVStatsConfig{
		// Input
		Path: path,
		// Reporting
		Debug:               debugPrint,
		RuntimeError:        runtimeError,
		ProcessRegistration: children.ProcessRegistration,
		ProgressReport:      progress,
	}); err != nil {
		return
	}
	duration := time.Since(start)
	// Done
	fmt.Fprintf(liveprogress.Bypass(), "MKV stats regenerated in %s\n", duration.Round(time.Second))
	return
}
