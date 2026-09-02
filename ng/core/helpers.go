package core

import (
	"fmt"
	"os"

	"github.com/hekmon/cunits/v3"
)

func getFileSize(path string) (size cunits.Bits, err error) {
	info, err := os.Stat(path)
	if err != nil {
		err = fmt.Errorf("failed to stat path: %w", err)
	} else {
		size = cunits.ImportInBytes(float64(info.Size()))
	}
	return
}
