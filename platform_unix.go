//go:build !windows

package main

import (
	"fmt"
	"os"
	"syscall"
)

func sameFileSystem(path1, path2 string) (same bool, err error) {
	fileInfo1, err := os.Stat(path1)
	if err != nil {
		err = fmt.Errorf("failed to stat %s: %w", path1, err)
		return
	}
	fileInfo2, err := os.Stat(path2)
	if err != nil {
		err = fmt.Errorf("failed to stat %s: %w", path2, err)
		return
	}
	same = fileInfo1.Sys().(*syscall.Stat_t).Dev == fileInfo2.Sys().(*syscall.Stat_t).Dev
	return
}
