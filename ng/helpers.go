package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

type ctxKey string

const (
	inputFileSizeCtxKey  ctxKey = "inputsize"
	inputFileInfosCtxKey ctxKey = "fileInfos"
)

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

func generateWorkingDirectoryPath(basePath string) string {
	return filepath.Join(basePath, fmt.Sprintf("sptenc-%d", time.Now().Unix()))
}

const (
	sceneTresholdMin = 0
	sceneTresholdMax = 100
)

func validateSceneTreshold(v float64) error {
	if v < sceneTresholdMin || v > sceneTresholdMax {
		return fmt.Errorf("must be between %d and %d", sceneTresholdMin, sceneTresholdMax)
	}
	return nil
}

func getCacheDir() string {
	userCacheDir, err := os.UserCacheDir()
	if err != nil {
		userCacheDir = os.TempDir()
	}
	return filepath.Join(userCacheDir, "sptenc")
}

const (
	VMAFOffValue = -1
	VMAFMinValue = 0
	VMAFMaxValue = 100
)

func vmafValueValidator(v float64) error {
	if v != VMAFOffValue && (v < VMAFMinValue || v > VMAFMaxValue) {
		return fmt.Errorf("must be between %d and %d or %d to disable", VMAFMinValue, VMAFMaxValue, VMAFOffValue)
	}
	return nil
}

func validateTmpDir(path string) error {
	if runtime.GOOS == "windows" && !isASCII(path) {
		return fmt.Errorf("the temporary directory path contains non-ASCII characters which are not compatible with libvmaf on Windows\n"+
			"Please use a path with only ASCII characters (no accents or special characters).\n"+
			"Current path: %s", path)
	}
	return nil
}
