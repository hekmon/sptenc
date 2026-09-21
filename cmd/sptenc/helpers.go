package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"al.essio.dev/pkg/shellescape"
	"github.com/hekmon/sptenc/ffmpeg"
	"github.com/hekmon/sptenc/mkvtoolnix"

	"github.com/hekmon/liveprogress/v2"
)

func checkFFMPEG(ctx context.Context) error {
	if _, err := ffmpeg.GetFFMPEGVersion(ctx); err != nil {
		return fmt.Errorf("ffmpeg check failed: %w", err)
	}
	return nil
}

func checkFFProbe(ctx context.Context) error {
	if _, err := ffmpeg.GetFFProbeVersion(ctx); err != nil {
		return fmt.Errorf("ffprobe check failed: %w", err)
	}
	return nil
}

func checkMKVPropEdit(ctx context.Context) error {
	if _, err := mkvtoolnix.GetMKVPropEditVersion(ctx); err != nil {
		return fmt.Errorf("mkvpropedit check failed: %w", err)
	}
	return nil
}

func extractFileNameInfos(path string) (name, extension string) {
	fullFileName := filepath.Base(path)
	extension = filepath.Ext(fullFileName)
	name = fullFileName[:len(fullFileName)-len(extension)]
	if len(extension) > 1 {
		extension = extension[1:]
	}
	return
}

// isASCII checks if a string contains only ASCII characters (code points 0-127)
func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] > 127 {
			return false
		}
	}
	return true
}

func createTempDir(basePath string) (path string, err error) {
	return os.MkdirTemp(basePath, "sptenc-*")
}

func getCacheDir() string {
	userCacheDir, err := os.UserCacheDir()
	if err != nil {
		userCacheDir = os.TempDir()
	}
	return filepath.Join(userCacheDir, "sptenc")
}

// getSegmentsFromDir returns sorted list of video file paths from inputDir.
// Files are sorted alphabetically and returned as full paths.
func getSegmentsFromDir(inputDir string) (filePaths []string, err error) {
	entries, err := os.ReadDir(inputDir)
	if err != nil {
		err = fmt.Errorf("failed to read input directory: %w", err)
		return
	}
	// Filter video files
	var files []os.DirEntry
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(entry.Name()))
		if ext == ".mkv" || ext == ".mp4" {
			files = append(files, entry)
		}
	}
	// Sort by name to ensure consistent ordering
	sort.Slice(files, func(i, j int) bool {
		return files[i].Name() < files[j].Name()
	})
	// Build full paths
	filePaths = make([]string, len(files))
	for i, file := range files {
		filePaths[i] = filepath.Join(inputDir, file.Name())
	}
	return
}

// getSegmentsTotalDuration calculates the total duration of all segment files
func getSegmentsTotalDuration(ctx context.Context, segmentPaths []string, debug bool) (totalDuration time.Duration, err error) {
	var stats ffmpeg.FFProbeStats
	for _, path := range segmentPaths {
		if stats, err = getStreamsInfos(ctx, path, debug); err != nil {
			err = fmt.Errorf("failed to get stream info for segment %s: %w", path, err)
			return
		}
		totalDuration += stats.Format.Duration
	}
	return
}

func getFileSize(path string) (size int64, err error) {
	info, err := os.Stat(path)
	if err != nil {
		err = fmt.Errorf("failed to stat path: %w", err)
		return
	}
	size = info.Size()
	return
}

// validateOutputPath checks that the output path has a .mkv extension, does not
// already exist and that its parent directory exists and is writable. The output
// file is only written at the very end of what can be a multi-day process, so a
// bad destination must be caught upfront.
func validateOutputPath(outputPath string) error {
	if filepath.Ext(outputPath) != ".mkv" {
		return errors.New("output file must have a .mkv extension")
	}
	if _, err := os.Stat(outputPath); err == nil {
		return fmt.Errorf("output file already exists: %s", shellescape.Quote(outputPath))
	}
	outputDir := filepath.Dir(outputPath)
	dirInfos, err := os.Stat(outputDir)
	if err != nil {
		return fmt.Errorf("output directory is not accessible: %w", err)
	}
	if !dirInfos.IsDir() {
		return fmt.Errorf("output file parent is not a directory: %s", shellescape.Quote(outputDir))
	}
	// Permission bits do not tell the whole story (ACLs, read-only mounts, Windows...):
	// actually try to create a file.
	probe, err := os.CreateTemp(outputDir, ".sptenc-write-test-*")
	if err != nil {
		return fmt.Errorf("output directory is not writable: %w", err)
	}
	probe.Close()
	if err = os.Remove(probe.Name()); err != nil {
		return fmt.Errorf("failed to remove the write test file: %w", err)
	}
	return nil
}

// AllAudioTracksPCM returns true if all audio streams in the given stats are PCM encoded.
func AllAudioTracksPCM(stats ffmpeg.FFProbeStats) bool {
	for _, stream := range stats.Streams {
		if stream.CodecType == "audio" {
			if stream.CodecName != ffmpeg.CodecAudioPCM && stream.CodecName != ffmpeg.CodecAudioPCM24b {
				return false
			}
		}
	}
	return true
}

// formatPercent formats a float64 as a percentage string with up to 1 decimal
// place, trimming trailing ".0" for whole numbers.
func formatPercent(v float64) string {
	s := strconv.FormatFloat(v, 'f', 1, 64)
	if strings.HasSuffix(s, ".0") {
		return s[:len(s)-2] + "%"
	}
	return s + "%"
}

// verifyColorMetadata probes the output file and warns via liveprogress.Bypass
// if any container-level color metadata does not match the source video stream.
func verifyColorMetadata(ctx context.Context, outputPath string, sourceStream *ffmpeg.FFProbeBinaryStream, debug bool) {
	bypass := liveprogress.Bypass()
	outputStats, err := getStreamsInfos(ctx, outputPath, debug)
	if err != nil {
		fmt.Fprintf(bypass, "WARNING: could not verify output color metadata: %s\n", err)
		return
	}
	outStream := outputStats.VideoTrack()
	if outStream == nil {
		fmt.Fprintf(bypass, "WARNING: output file has no video stream, can not verify color metadata\n")
		return
	}
	if sourceStream.ColorRange != "" && outStream.ColorRange != sourceStream.ColorRange {
		fmt.Fprintf(bypass, "WARNING: output color_range (%s) does not match source (%s)\n", outStream.ColorRange, sourceStream.ColorRange)
	}
	if sourceStream.ColorSpace != "" && outStream.ColorSpace != sourceStream.ColorSpace {
		fmt.Fprintf(bypass, "WARNING: output colorspace (%s) does not match source (%s)\n", outStream.ColorSpace, sourceStream.ColorSpace)
	}
	if sourceStream.ColorTransfer != "" && outStream.ColorTransfer != sourceStream.ColorTransfer {
		fmt.Fprintf(bypass, "WARNING: output color_trc (%s) does not match source (%s)\n", outStream.ColorTransfer, sourceStream.ColorTransfer)
	}
	if sourceStream.ColorPrimaries != "" && outStream.ColorPrimaries != sourceStream.ColorPrimaries {
		fmt.Fprintf(bypass, "WARNING: output color_primaries (%s) does not match source (%s)\n", outStream.ColorPrimaries, sourceStream.ColorPrimaries)
	}
}
