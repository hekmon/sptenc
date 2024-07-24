package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/hekmon/cunits/v2"
	"github.com/hekmon/liveprogress/v2"
)

func generateWorkingDirectroryPath(basePath string) string {
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

func computeNewDirFilePath(file, outputDir string) (output string) {
	// Work on a clean path
	file = filepath.Clean(file)
	// If no output dir set, use file dir
	if outputDir == "" {
		outputDir = filepath.Dir(file)
	}
	// Cut part of file name
	inputFileName := filepath.Base(file)
	extension := filepath.Ext(file)
	baseName := inputFileName[:len(inputFileName)-len(extension)]
	// Recompose
	output = filepath.Join(outputDir,
		fmt.Sprintf("%s [%s].mkv", baseName, titleTagValue))
	return
}

func getFileSize(path string) (size cunits.Bits, err error) {
	info, err := os.Stat(path)
	if err != nil {
		err = fmt.Errorf("failed to stat path: %w", err)
		return
	}
	size = cunits.ImportInByte(float64(info.Size()))
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
		liveprogress.WithLineFillRunes(),
		// liveprogress.WithWidth(barsWidth),
		liveprogress.WithPrependDecorator(func(bar *liveprogress.Bar) string {
			return " Moving | "
		}),
		liveprogress.WithPrependTimeElapsed(liveprogress.BaseStyle()),
		liveprogress.WithAppendPercent(liveprogress.BaseStyle()),
		liveprogress.WithAppendTimeRemaining(liveprogress.BaseStyle()),
		liveprogress.WithAppendDecorator(func(bar *liveprogress.Bar) string {
			return fmt.Sprintf(" left | %s/%s copied",
				cunits.ImportInByte(float64(bar.Current())),
				cunits.ImportInByte(float64(bar.Total())),
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
