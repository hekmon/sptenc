package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
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
