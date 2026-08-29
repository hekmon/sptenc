package main

import (
	"path/filepath"
)

const (
	inputFileSizeCtxKey = "inputsize"
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
