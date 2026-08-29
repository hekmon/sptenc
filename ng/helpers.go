package main

import (
	"fmt"
	"path/filepath"
	"time"
)

type ctxKey string

const (
	inputFileSizeCtxKey ctxKey = "inputsize"
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
