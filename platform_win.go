//go:build windows

package main

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

type driveType uintptr

const (
	successfullError = "The operation completed successfully."
	// https://learn.microsoft.com/windows/win32/api/fileapi/nf-fileapi-getdrivetypew#return-value
	DRIVE_UNKNOWN     driveType = 0
	DRIVE_NO_ROOT_DIR driveType = 1
	DRIVE_REMOVABLE   driveType = 2
	DRIVE_FIXED       driveType = 3
	DRIVE_REMOTE      driveType = 4
	DRIVE_CDROM       driveType = 5
	DRIVE_RAMDISK     driveType = 6
)

func (dt driveType) String() string {
	switch dt {
	case DRIVE_UNKNOWN:
		return "Unknown"
	case DRIVE_NO_ROOT_DIR:
		return "No Root Directory"
	case DRIVE_REMOVABLE:
		return "Removable"
	case DRIVE_FIXED:
		return "Fixed"
	case DRIVE_REMOTE:
		return "Remote"
	case DRIVE_CDROM:
		return "CD-ROM"
	case DRIVE_RAMDISK:
		return "RAM Disk"
	default:
		return "Unknown"
	}
}

var (
	procGetDriveType         = syscall.MustLoadDLL("kernel32.dll").MustFindProc("GetDriveTypeW")
	procGetVolumeInformation = syscall.MustLoadDLL("kernel32.dll").MustFindProc("GetVolumeInformationW")
)

func sameFileSystem(path1, path2 string) (same bool, err error) {
	drive1 := path1[:3]
	drive2 := path2[:3]
	// Get the drive type for each drive
	//// drive 1
	drive1UTF16Ptr, err := syscall.UTF16PtrFromString(drive1)
	if err != nil {
		err = fmt.Errorf("Error converting drive1 to UTF-16: %w", err)
		return
	}
	driveType1, _, err := syscall.SyscallN(
		procGetDriveType.Addr(),
		uintptr(unsafe.Pointer(drive1UTF16Ptr)),
	)
	if err.Error() != successfullError {
		err = fmt.Errorf("Error calling GetDriveType: %w", err)
		return
	}
	if driveType(driveType1) == DRIVE_NO_ROOT_DIR {
		err = fmt.Errorf("Drive %s does not have a root directory (invalid letter)", drive1)
		return
	}
	//// drive 2
	drive2UTF16Ptr, err := syscall.UTF16PtrFromString(drive2)
	if err != nil {
		err = fmt.Errorf("Error converting drive2 to UTF-16: %w", err)
		return
	}
	driveType2, _, err := syscall.SyscallN(
		procGetDriveType.Addr(),
		uintptr(unsafe.Pointer(drive2UTF16Ptr)),
	)
	if err.Error() != successfullError {
		err = fmt.Errorf("Error calling GetDriveType: %w", err)
		return
	}
	if driveType(driveType2) == DRIVE_NO_ROOT_DIR {
		err = fmt.Errorf("Drive %s does not have a root directory (invalid letter)", drive2)
		return
	}
	//// If the drive types are different, the paths are on different filesystems
	if driveType1 != driveType2 {
		if *debug {
			fmt.Printf("%s (%s) and %s (%s) are on different drive types\n", path1, driveType(driveType1), path2, driveType(driveType2))
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
