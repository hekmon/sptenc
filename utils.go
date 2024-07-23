package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/hekmon/cunits/v2"
)

func getWorkingDirPath(basePath string) string {
	return filepath.Join(basePath, fmt.Sprintf("scenc-%d", time.Now().Unix()))
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

func computeOutputFilePath(file, outputDir string) (output string) {
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
