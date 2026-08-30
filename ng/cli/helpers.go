package main

import (
	"fmt"
	"path/filepath"
	"runtime"
	"time"

	"github.com/hekmon/sptenc/ng/ffmpeg"
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

func validateSceneThreshold(v float64) error {
	if v < ffmpeg.SceneThresholdMin || v > ffmpeg.SceneThresholdMax {
		return fmt.Errorf("must be between %d and %d", ffmpeg.SceneThresholdMin, ffmpeg.SceneThresholdMax)
	}
	return nil
}

func validateTmpDir(path string) error {
	// validate tmpDir path for non-ASCII characters (Windows compatibility issue with libvmaf)
	if runtime.GOOS == "windows" && !isASCII(path) {
		return fmt.Errorf("the temporary directory path contains non-ASCII characters which are not compatible with libvmaf on Windows\n"+
			"Please use a path with only ASCII characters (no accents or special characters).\n"+
			"Current path: %s", path)
	}
	return nil
}
