//go:build windows

package main

import (
	"fmt"
	"os"
	"syscall"
)

func StopAndWait(process *os.Process) (err error) {
	handle, err := syscall.OpenProcess(syscall.PROCESS_TERMINATE, false, uint32(process.Pid))
	if err != nil {
		err = fmt.Errorf("failed to open process handle: %w", err)
		return
	}
	defer syscall.CloseHandle(handle)
	if err = syscall.TerminateProcess(handle, 0); err != nil {
		err = fmt.Errorf("failed to terminate process: %w", err)
		return
	}
	_, _ = process.Wait() // process might have already exited before we could wait for its pid, so ignore error in this case
	return
}
