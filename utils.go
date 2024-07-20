package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

func getWorkingDirPath() string {
	return filepath.Join(os.TempDir(), fmt.Sprintf("scenc-%d", time.Now().Unix()))
}
