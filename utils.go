package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hekmon/cunits/v3"
	"github.com/hekmon/ffmpegutils"
	"github.com/hekmon/liveprogress/v2"
)

func generateWorkingDirectoryPath(basePath string) string {
	return filepath.Join(basePath, fmt.Sprintf("sptenc-%d", time.Now().Unix()))
}

func getDirFilesNumber(dir string) (num int, err error) {
	files, err := os.ReadDir(dir)
	if err != nil {
		err = fmt.Errorf("failed to read output directory: %w", err)
		return
	}
	num = len(files)
	return
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
	for i := 0; i < len(files); i++ {
		for j := i + 1; j < len(files); j++ {
			if files[i].Name() > files[j].Name() {
				files[i], files[j] = files[j], files[i]
			}
		}
	}
	// Build full paths
	filePaths = make([]string, len(files))
	for i, file := range files {
		filePaths[i] = filepath.Join(inputDir, file.Name())
	}
	return
}

// getSegmentsTotalDuration calculates the total duration of all segment files
func getSegmentsTotalDuration(ctx context.Context, segmentPaths []string) (totalDuration time.Duration, err error) {
	var stats ffmpegutils.FFProbeStats
	for _, path := range segmentPaths {
		if stats, err = getStreamsInfos(ctx, path); err != nil {
			err = fmt.Errorf("failed to get stream info for segment %s: %w", path, err)
			return
		}
		totalDuration += stats.Format.Duration
	}
	return
}

func computeNewDirFilePath(file, outputDir string, rename bool) (output string) {
	// Work on a clean path
	file = filepath.Clean(file)
	if !rename {
		output = filepath.Join(outputDir, filepath.Base(file))
		return
	}
	// Cut GOP of file name
	inputFileName := filepath.Base(file)
	extension := filepath.Ext(file)
	baseName := inputFileName[:len(inputFileName)-len(extension)]
	// encoder
	var encoder string
	if *nvenc {
		encoder = "HEVC NVENC"
	} else {
		encoder = "libx265"
	}
	// Recompose
	output = filepath.Join(outputDir,
		fmt.Sprintf("%s [%s %s].mkv", baseName, encoder, titleTagValue))
	return
}

func getFileSize(path string) (size cunits.Bits, err error) {
	info, err := os.Stat(path)
	if err != nil {
		err = fmt.Errorf("failed to stat path: %w", err)
		return
	}
	size = cunits.ImportInBytes(float64(info.Size()))
	return
}

func filesCheck(ctx context.Context, original, encoded string) (vmafStats *ffmpegutils.VMAFStats, err error) {
	bypass := liveprogress.Bypass()
	start := time.Now()
	// Get original file stats
	cl := liveprogress.AddCustomLine(func() string { return "           | Checking original file..." })
	originalStats, err := getStreamsInfosCF(ctx, original, true)
	if err != nil {
		liveprogress.RemoveCustomLine(cl)
		err = fmt.Errorf("failed to get original file stats: %w", err)
		return
	}
	liveprogress.RemoveCustomLine(cl)
	// Get reencoded file stats
	cl = liveprogress.AddCustomLine(func() string { return "           | Checking encoded file..." })
	encodedStats, err := getStreamsInfosCF(ctx, encoded, true)
	if err != nil {
		liveprogress.RemoveCustomLine(cl)
		err = fmt.Errorf("failed to get reencoded file stats: %w", err)
		return
	}
	liveprogress.RemoveCustomLine(cl)
	// Compare
	duration := time.Since(start)
	if originalStats.VideoTrack().NbReadFrames != encodedStats.VideoTrack().NbReadFrames {
		fmt.Fprintf(bypass, "WARNING: Number of read frames is different between original and reencoded files: original has %d and reencoded has %d.\n\t Final VMAF won't be computed. Does the input file has been encoded with open GOP?\n",
			originalStats.VideoTrack().NbReadFrames, encodedStats.VideoTrack().NbReadFrames)
		fmt.Fprintf(bypass, "Files frames check took %s\n", duration.Round(time.Second))
		return
	}
	fmt.Fprintf(bypass, "Number of frames is the same between original and reencoded merged stream: %d\n", originalStats.VideoTrack().NbReadFrames)
	fmt.Fprintf(bypass, "Files frames check took %s\n", duration.Round(time.Second))
	// Now compute their VMAF together
	encodedVideoTrack := encodedStats.VideoTrack()
	start = time.Now()
	vmaf, err := computeVMAF(ctx, encoded, original, encoded+"_vmaf.json", encodedVideoTrack.RFrameRate, encodedVideoTrack.NbReadFrames, encodedVideoTrack.Height >= ffmpegutils.UltraHDHeight, true)
	if err != nil {
		err = fmt.Errorf("failed to compute VMAF: %w", err)
		return
	}
	vmafStats = &vmaf
	duration = time.Since(start)
	// Print VMAF
	fmt.Fprintf(bypass, "Final VMAF:\n%s", vmaf)
	fmt.Fprintf(bypass, "VMAF computation took %s\n", duration.Round(time.Second))
	return
}

func MoveProgress(old, new string) (err error) {
	// Check that the target directory exists or create it
	if _, err = os.Stat(filepath.Dir(new)); err != nil {
		if os.IsNotExist(err) {
			if err = os.MkdirAll(filepath.Dir(new), 0755); err != nil {
				return
			}
		} else {
			err = fmt.Errorf("failed to stat target directory: %w", err)
			return
		}
	}
	// Are they on the same filesystem?
	same, err := sameFileSystem(filepath.Dir(old), filepath.Dir(new))
	if err != nil {
		err = fmt.Errorf("failed to check if both paths are on the same filesystem: %w", err)
		return
	}
	// If they are on the same filesystem, just move the file as it will be instant
	if same {
		return os.Rename(old, new)
	}
	// If not, copy the file and delete the old one
	//// First get file size
	size, err := getFileSize(old)
	if err != nil {
		err = fmt.Errorf("failed to get file size: %w", err)
		return
	}
	//// Then create the progress bar for the copy
	bar := liveprogress.AddBar(
		liveprogress.WithTotal(uint64(size)),
		liveprogress.WithMultiplyRunes(),
		// liveprogress.WithWidth(barsWidth),
		liveprogress.WithSameAutoSizeInternalPadding(true, false),
		liveprogress.WithPrependDecorator(func(bar *liveprogress.Bar) string {
			return " Moving | "
		}),
		liveprogress.WithPrependTimeElapsed(liveprogress.BaseStyle()),
		liveprogress.WithAppendPercent(liveprogress.BaseStyle()),
		liveprogress.WithAppendTimeRemaining(liveprogress.BaseStyle()),
		liveprogress.WithAppendDecorator(func(bar *liveprogress.Bar) string {
			return fmt.Sprintf(" left | %s/%s copied",
				cunits.ImportInBytes(float64(bar.Current())),
				cunits.ImportInBytes(float64(bar.Total())),
			)
		}),
	)
	defer liveprogress.RemoveBar(bar)
	//// Open files
	src, err := os.Open(old)
	if err != nil {
		err = fmt.Errorf("failed to open source file: %w", err)
		return
	}
	defer src.Close()
	dst, err := os.Create(new)
	if err != nil {
		err = fmt.Errorf("failed to create destination file: %w", err)
		return
	}
	defer dst.Close()
	//// Create the wrapper that will follow bytes transfert
	reader := readerCounter{
		wrapped: src,
		updater: func(bytesRead int) {
			bar.CurrentAdd(uint64(bytesRead))
		},
	}
	//// Copy
	if _, err = io.Copy(dst, reader); err != nil {
		err = fmt.Errorf("failed to copy file: %w", err)
		return
	}
	// Now that the file is copied, delete the old one
	src.Close()
	if err = os.Remove(old); err != nil {
		err = fmt.Errorf("failed to delete source file: %w", err)
		return
	}
	return
}

type readerCounter struct {
	wrapped io.Reader
	updater func(bytesRead int)
}

func (rc readerCounter) Read(p []byte) (n int, err error) {
	if rc.wrapped == nil {
		err = io.EOF
		return
	}
	n, err = rc.wrapped.Read(p)
	if rc.updater != nil {
		rc.updater(n)
	}
	return
}

func cleanUpTMPFiles(path string) {
	if *keep {
		fmt.Fprintf(liveprogress.Bypass(), "Temporary work directory can be found here: %s\n", path)
		return
	}
	cl := liveprogress.AddCustomLine(func() string {
		return "Cleaning working directory..."
	})
	defer liveprogress.RemoveCustomLine(cl)
	if err := os.RemoveAll(path); err != nil {
		fmt.Fprintf(liveprogress.Bypass(), "Failed to clean working directory %q: %s\n", path, err)
	}
}
