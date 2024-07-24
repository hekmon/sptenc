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

func StopAndWait(process *os.Process) (err error) {
	if err = process.Signal(syscall.SIGTERM); err != nil {
		err = fmt.Errorf("failed to send %s signal to child process: %w", syscall.SIGTERM, err)
		if killErr := process.Kill(); killErr != nil {
			err = fmt.Errorf("failed to kill %d after sending signal %s: %s | %w",
				process.Pid, syscall.SIGTERM, err, killErr)
		}
		return
	}
	_, _ = process.Wait() // process might have already exited before we could wait for its pid, so ignore error in this case
	return
}
