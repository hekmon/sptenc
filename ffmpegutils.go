package main

import (
	"fmt"
	"os"
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

func getStreamsInfosCF(path string, timeStats bool) (stats ffmpegutils.FFProbeStats, err error) {
	// Prepare
	fileInfos, err := os.Stat(path)
	if err != nil {
		err = fmt.Errorf("failed to stat the file: %w", err)
		return
	}
	// Live Progress
	var bar *liveprogress.Bar
	if timeStats {
		bar = liveprogress.AddBar(
			liveprogress.WithTotal(uint64(fileInfos.Size())),
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
					cunits.ImportInByte(float64(bar.Current())), cunits.ImportInByte(float64(bar.Total())),
				)
			}),
		)
	} else {
		bar = liveprogress.AddBar(
			liveprogress.WithTotal(uint64(fileInfos.Size())),
			liveprogress.WithMultiplyRunes(),
			// liveprogress.WithWidth(barsWidth),
			liveprogress.WithSameAutoSizeInternalPadding(true, false),
			liveprogress.WithPrependDecorator(func(bar *liveprogress.Bar) string {
				return "   Analyze | "
			}),
			liveprogress.WithAppendPercent(liveprogress.BaseStyle()),
			liveprogress.WithAppendDecorator(func(bar *liveprogress.Bar) string {
				return fmt.Sprintf(" | %s/%s",
					cunits.ImportInByte(float64(bar.Current())), cunits.ImportInByte(float64(bar.Total())),
				)
			}),
		)
	}
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
		liveprogress.WithMultiplyRunes(),
		// liveprogress.WithWidth(barsWidth),
		liveprogress.WithSameAutoSizeInternalPadding(true, false),
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
	fmt.Fprintf(liveprogress.Bypass(), "GOP slicing done in %s\n", duration.Round(time.Second))
	return
}

func encodeQP(input, output string, totalFrames, qp int, convert10bits bool) (err error) {
	// Prepare
	var preset ffmpegutils.EncodingPreset
	if *nvenc {
		preset = ffmpegutils.NVENCPresetP5
	} else {
		preset = ffmpegutils.Libx265PresetSlow
	}
	// live progress
	bar := liveprogress.AddBar(
		liveprogress.WithTotal(uint64(totalFrames)),
		liveprogress.WithMultiplyRunes(),
		// liveprogress.WithWidth(barsWidth),
		liveprogress.WithSameAutoSizeInternalPadding(true, false),
		liveprogress.WithPrependDecorator(func(bar *liveprogress.Bar) string {
			return "    Encode | "
		}),
		// liveprogress.WithPrependTimeElapsed(liveprogress.BaseStyle()),
		liveprogress.WithAppendPercent(liveprogress.BaseStyle()),
		// liveprogress.WithAppendTimeRemaining(liveprogress.BaseStyle()),
		liveprogress.WithAppendDecorator(func(bar *liveprogress.Bar) string {
			return fmt.Sprintf(" | %d/%d frames", bar.Current(), bar.Total())
		}),
	)
	defer liveprogress.RemoveBar(bar)
	progress := func(stats ffmpegutils.ProgressStats) {
		bar.CurrentSet(uint64(stats.CurrentFrame))
	}
	// Execute
	start := time.Now()
	if err = ffmpegutils.AnimeEncode(ffmpegutils.AnimeEncodeConfig{
		// Input
		Input: input,
		// Output
		Quantization:    qp,
		ConvertTo10bits: convert10bits,
		Preset:          preset,
		Tags:            nil,
		OutputFilePath:  output,
		// Hardware Acceleration
		Libx265HeavyMultiThreading: *heavyMultithreading,
		NVDEC:                      *nvdec,
		NVENC:                      *nvenc,
		GPUID:                      gpu,
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
		fmt.Fprintf(liveprogress.Bypass(), "GOP encoded in %s\n", duration.Round(time.Second))
	}
	return
}

func computeVMAF(distorted, reference, reportPath, frameRate string, totalFrames int, ultraHD, timeStats bool) (vmaf ffmpegutils.VMAFStats, err error) {
	// live progress
	var bar *liveprogress.Bar
	if timeStats {
		bar = liveprogress.AddBar(
			liveprogress.WithTotal(uint64(totalFrames)),
			liveprogress.WithMultiplyRunes(),
			// liveprogress.WithWidth(barsWidth),
			liveprogress.WithSameAutoSizeInternalPadding(true, false),
			liveprogress.WithPrependDecorator(func(bar *liveprogress.Bar) string {
				return "      VMAF | "
			}),
			liveprogress.WithPrependTimeElapsed(liveprogress.BaseStyle()),
			liveprogress.WithAppendPercent(liveprogress.BaseStyle()),
			liveprogress.WithAppendTimeRemaining(liveprogress.BaseStyle()),
			liveprogress.WithAppendDecorator(func(bar *liveprogress.Bar) string {
				return fmt.Sprintf(" left | %d/%d frames", bar.Current(), bar.Total())
			}),
		)
	} else {
		bar = liveprogress.AddBar(
			liveprogress.WithTotal(uint64(totalFrames)),
			liveprogress.WithMultiplyRunes(),
			// liveprogress.WithWidth(barsWidth),
			liveprogress.WithSameAutoSizeInternalPadding(true, false),
			liveprogress.WithPrependDecorator(func(bar *liveprogress.Bar) string {
				return "      VMAF | "
			}),
			// liveprogress.WithPrependTimeElapsed(liveprogress.BaseStyle()),
			liveprogress.WithAppendPercent(liveprogress.BaseStyle()),
			// liveprogress.WithAppendTimeRemaining(liveprogress.BaseStyle()),
			liveprogress.WithAppendDecorator(func(bar *liveprogress.Bar) string {
				return fmt.Sprintf(" | %d/%d frames", bar.Current(), bar.Total())
			}),
		)
	}
	defer liveprogress.RemoveBar(bar)
	progress := func(stats ffmpegutils.ProgressStats) {
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
		fmt.Fprintf(liveprogress.Bypass(), "GOP VMAF computed in %s\n", duration.Round(time.Second))
	}
	return
}

func generateConcatScript(segmentsDir string, qps []int) (concatScriptPath string, err error) {
	// Generate the concat script
	filesnames := make([]string, len(qps))
	for GOP, qp := range qps {
		filesnames[GOP] = fmt.Sprintf(ffmpegutils.SegEncodedOutputFormat, GOP, qp)
	}
	if concatScriptPath, err = ffmpegutils.GenerateConcatScript(segmentsDir, filesnames); err != nil {
		err = fmt.Errorf("failed to create the concat script file: %w", err)
	}
	return
}

func GOPMerge(concatScript, outputPath string, expectedDuration time.Duration) (err error) {
	// live progress
	var currentStats ffmpegutils.ProgressStats
	bar := liveprogress.AddBar(
		liveprogress.WithTotal(uint64(expectedDuration)),
		liveprogress.WithMultiplyRunes(),
		// liveprogress.WithWidth(barsWidth),
		liveprogress.WithSameAutoSizeInternalPadding(true, false),
		liveprogress.WithPrependDecorator(func(bar *liveprogress.Bar) string {
			return "  Merging | "
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
	if err = ffmpegutils.Concat(ffmpegutils.ConcatConfig{
		// Input
		ConcatScriptPath: concatScript,
		OutputPath:       outputPath,
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
		fmt.Fprintf(liveprogress.Bypass(), "GOP merged within %q in %s\n", outputPath, duration.Round(time.Second))
	} else {
		fmt.Fprintf(liveprogress.Bypass(), "GOP merged in %s\n", duration.Round(time.Second))
	}
	return
}

func Remux(originalFile, newVideo, outputPath string, tagsFlags []string, expectedDuration time.Duration, convertFlac bool) (err error) {
	// live progress
	var currentStats ffmpegutils.ProgressStats
	bar := liveprogress.AddBar(
		liveprogress.WithTotal(uint64(expectedDuration)),
		liveprogress.WithMultiplyRunes(),
		// liveprogress.WithWidth(barsWidth),
		liveprogress.WithSameAutoSizeInternalPadding(true, false),
		liveprogress.WithPrependDecorator(func(bar *liveprogress.Bar) string {
			return "  Remuxing | "
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
	if err = ffmpegutils.Remux(ffmpegutils.RemuxConfig{
		// Input
		OriginalFile: originalFile,
		NewVideoFile: newVideo,
		// Output
		OutputFilePath: outputPath,
		Tags:           tagsFlags,
		FLAC:           convertFlac,
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
		fmt.Fprintf(liveprogress.Bypass(), "Remuxed within %q in %s\n", outputPath, duration.Round(time.Second))
	} else {
		fmt.Fprintf(liveprogress.Bypass(), "Remuxed in %s\n", duration.Round(time.Second))
	}
	return
}

func regenerateMKVStats(path string) (err error) {
	// live progress
	bar := liveprogress.AddBar(
		liveprogress.WithTotal(uint64(100)),
		liveprogress.WithMultiplyRunes(),
		// liveprogress.WithWidth(barsWidth),
		liveprogress.WithSameAutoSizeInternalPadding(true, false),
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
