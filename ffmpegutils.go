package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/hekmon/cunits/v2"
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
			build.WriteString(fmt.Sprintf(" | %s/%s",
				cunits.ImportInByte(float64(bar.Current())), cunits.ImportInByte(float64(bar.Total())),
			))
			return build.String()
		}),
	)
	defer liveprogress.RemoveBar(bar)
	progress := func(n int) {
		bar.CurrentAdd(uint64(n))
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

func splitFile(path, outputDir string, totalDuration time.Duration) (err error) {
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
	if err = ffmpegutils.Segment(ffmpegutils.SegmentConfig{
		// Input
		Input: path,
		// Output
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
	fmt.Fprintf(liveprogress.Bypass(), "Parts slicing done in %s\n", duration.Round(time.Second))
	return
}

func encodeQP(input, output string, totalFrames, qp int) (err error) {
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
		NVDEC: *nvdec,
		NVENC: *nvenc,
		GPUID: gpu,
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
		fmt.Fprintf(liveprogress.Bypass(), "Part encoded in %s\n", duration.Round(time.Second))
	}
	return
}

func computeVMAF(distorted, reference, reportPath, frameRate string, totalFrames int, ultraHD bool) (vmaf ffmpegutils.VMAFStats, err error) {
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
		NVDEC:             *nvdec,
		NoEnhancementGain: *vmafNEG,
		VMAFCuda:          *vmafcuda,
		GPUID:             gpu,
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
		fmt.Fprintf(liveprogress.Bypass(), "Part VMAF computed in %s\n", duration.Round(time.Second))
	}
	return
}

func partsMerge(originalFile, partsDir, outputPath string, tagsFlags []string, qps []int, expectedDuration time.Duration) (err error) {
	// Generate the concat script
	filesnames := make([]string, len(qps))
	for part, qp := range qps {
		filesnames[part] = fmt.Sprintf(ffmpegutils.SceneEncodedOutputFormat, part, qp)
	}
	concatScript, err := ffmpegutils.GenerateConcatScript(partsDir, filesnames)
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
	if err = ffmpegutils.ConcatRemux(ffmpegutils.ConcatRemuxConfig{
		// Input
		OriginalFile:     originalFile,
		ConcatScriptPath: concatScript,
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
		fmt.Fprintf(liveprogress.Bypass(), "Parts remuxed within %q in %s\n", outputPath, duration.Round(time.Second))
	} else {
		fmt.Fprintf(liveprogress.Bypass(), "Parts remuxed in %s\n", duration.Round(time.Second))
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
