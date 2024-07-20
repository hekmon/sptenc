package main

import (
	"fmt"
	"path/filepath"
	"time"
)

func getWorkingDirPath(basePath string) string {
	return filepath.Join(basePath, fmt.Sprintf("scenc-%d", time.Now().Unix()))
}
