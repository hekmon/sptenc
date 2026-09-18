package core

import (
	"fmt"
	"os"
)

func getFileSize(path string) (size int64, err error) {
	info, err := os.Stat(path)
	if err != nil {
		err = fmt.Errorf("failed to stat path: %w", err)
	} else {
		size = info.Size()
	}
	return
}
