package core

import (
	"context"
	"fmt"
	"os"

	"github.com/hekmon/cunits/v3"
	"github.com/hekmon/sptenc/ng/ffmpeg"
)

func getFileSize(path string) (size cunits.Bits, err error) {
	info, err := os.Stat(path)
	if err != nil {
		err = fmt.Errorf("failed to stat path: %w", err)
	} else {
		size = cunits.ImportInBytes(float64(info.Size()))
	}
	return
}

func getStreamsInfosCF(ctx context.Context, filePath string, logging QPSearchCallbacksLogging, signals QPSearchCallbacksAnalysis) (
	stats ffmpeg.FFProbeStats, err error) {
	// Recover size
	fileInfos, err := os.Stat(filePath)
	if err != nil {
		err = fmt.Errorf("failed to stat the file: %w", err)
		return
	}
	// Prepare signals
	signals.OnSegmentAnalysisStart(filePath, fileInfos.Size())
	defer signals.OnSegmentAnalysisStop()
	// Start analysis
	return ffmpeg.GetStreamsInfosCF(ctx, ffmpeg.GetStreamsInfosCFConfig{
		GetStreamsInfosConfig: ffmpeg.GetStreamsInfosConfig{
			// Input
			Path: filePath,
			// Reporting
			Debug: func(s string) {
				logging.Debug(s)
			},
			RuntimeError: logging.Error,
		},
		ReadBytesReport: signals.OnSegmentAnalysisProgress,
	})
}
