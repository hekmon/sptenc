//go:build !windows

package main

import (
	"fmt"
	"os"
	"syscall"
)

const (
	FFMPEG = "ffmpeg"
)

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
