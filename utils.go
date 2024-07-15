package main

import (
	"os"
	"path/filepath"
)

func getWorkingDirPath() string {
	return filepath.Join(os.TempDir(), "scenc")
}
