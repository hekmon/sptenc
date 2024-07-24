//go:build windows

package main

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

var (
	procGetDriveType         = syscall.MustLoadDLL("kernel32.dll").MustFindProc("GetDriveTypeW")
	procGetVolumeInformation = syscall.MustLoadDLL("kernel32.dll").MustFindProc("GetVolumeInformationW")
)

func sameFileSystem(path1, path2 string) (same bool, err error) {
	drive1 := path1[:3]
	drive2 := path2[:3]
	// Get the drive type for each drive
	//// drive 1
	var driveType1 uint32
	drive1UTF16Ptr, err := syscall.UTF16PtrFromString(drive1)
	if err != nil {
		err = fmt.Errorf("Error converting drive1 to UTF-16: %w", err)
		return
	}
	syscall.SyscallN(
		procGetDriveType.Addr(),
		1,
		uintptr(unsafe.Pointer(drive1UTF16Ptr)),
		uintptr(unsafe.Pointer(&driveType1)),
		0,
	)
	//// drive 2
	var driveType2 uint32
	drive2UTF16Ptr, err := syscall.UTF16PtrFromString(drive2)
	if err != nil {
		err = fmt.Errorf("Error converting drive2 to UTF-16: %w", err)
		return
	}
	syscall.SyscallN(
		procGetDriveType.Addr(),
		1,
		uintptr(unsafe.Pointer(drive2UTF16Ptr)),
		uintptr(unsafe.Pointer(&driveType2)),
		0,
	)
	//// If the drive types are different, the paths are on different filesystems
	if driveType1 != driveType2 {
		if *debug {
			fmt.Printf("%s and %s are on different drive types\n", path1, path2)
		}
		return
	}
	// Get the volume name for each drive
	//// drive 1
	var volumeName1 [256]uint16
	var bytesReturned1 uint32
	syscall.SyscallN(
		procGetVolumeInformation.Addr(),
		5,
		uintptr(unsafe.Pointer(drive1UTF16Ptr)),
		uintptr(unsafe.Pointer(&volumeName1[0])),
		256,
		uintptr(unsafe.Pointer(&bytesReturned1)),
		0,
		0,
	)
	//// drive 2
	var volumeName2 [256]uint16
	var bytesReturned2 uint32
	syscall.SyscallN(
		procGetVolumeInformation.Addr(),
		5,
		uintptr(unsafe.Pointer(drive2UTF16Ptr)),
		uintptr(unsafe.Pointer(&volumeName2[0])),
		256,
		uintptr(unsafe.Pointer(&bytesReturned2)),
		0,
		0,
	)
	//// Compare the volume names
	same = syscall.UTF16ToString(volumeName1[:bytesReturned1]) == syscall.UTF16ToString(volumeName2[:bytesReturned2])
	return
}

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
