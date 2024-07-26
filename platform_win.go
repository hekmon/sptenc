//go:build windows

package main

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"

	"github.com/hekmon/liveprogress/v2"
)

type driveType uintptr

const (
	bufferSize = 256
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
		return "UNKNOWN"
	}
}

var (
	procGetDriveType         = syscall.MustLoadDLL("kernel32.dll").MustFindProc("GetDriveTypeW")
	procGetVolumeInformation = syscall.MustLoadDLL("kernel32.dll").MustFindProc("GetVolumeInformationW")
)

func sameFileSystem(path1, path2 string) (same bool, err error) {
	bypass := liveprogress.Bypass()
	drive1 := path1[:3]
	drive2 := path2[:3]
	// Get the drive type for each drive
	// https://learn.microsoft.com/fr-fr/windows/win32/api/fileapi/nf-fileapi-getdrivetypew
	//// path 1
	driveType1, err := getDriveType(drive1)
	if err != nil {
		err = fmt.Errorf("failed to get drive type for %q: %w", drive1, err)
		return
	}
	if driveType1 == DRIVE_UNKNOWN || driveType1 == DRIVE_NO_ROOT_DIR {
		err = fmt.Errorf("invalid drive type for %q: %w", drive1, err)
		return
	}
	if *debug {
		fmt.Fprintf(bypass, "Drive %s is %s\n", drive1, driveType1)
	}
	//// path 2
	driveType2, err := getDriveType(drive2)
	if err != nil {
		err = fmt.Errorf("failed to get drive type for %q: %w", drive2, err)
		return
	}
	if driveType2 == DRIVE_UNKNOWN || driveType2 == DRIVE_NO_ROOT_DIR {
		err = fmt.Errorf("invalid drive type for %q: %w", drive2, err)
		return
	}
	if *debug {
		fmt.Fprintf(bypass, "Drive %s is %s\n", drive2, driveType2)
	}
	//// If the drive types are different, the paths are necessarily on different filesystems
	if driveType1 != driveType2 {
		return
	}
	// Get the volume informations
	// https://learn.microsoft.com/windows/win32/api/fileapi/nf-fileapi-getvolumeinformationw
	drive1VolName, drive1Serial, err := getDriveInfos(drive1)
	if *debug {
		fmt.Fprintf(bypass, "Drive %s is named '%s' (SN: %d)\n", drive1, drive1VolName, drive1Serial)
	}
	drive2VolName, drive2Serial, err := getDriveInfos(drive2)
	if *debug {
		fmt.Fprintf(bypass, "Drive %s is named '%s' (SN: %d)\n", drive2, drive2VolName, drive2Serial)
	}
	//// Compare the serial numbers
	same = drive1Serial == drive2Serial
	return
}

func getDriveType(rootPath string) (dtype driveType, err error) {
	driveUTF16Ptr, err := syscall.UTF16PtrFromString(rootPath)
	if err != nil {
		err = fmt.Errorf("failed to convert rootPath to UTF-16: %w", err)
		return
	}
	returnValue, _, _ := syscall.SyscallN(
		procGetDriveType.Addr(),
		uintptr(unsafe.Pointer(driveUTF16Ptr)),
	)
	dtype = driveType(returnValue)
	return
}

func getDriveInfos(rootPath string) (volName string, serial int, err error) {
	volUTF16Ptr, err := syscall.UTF16PtrFromString(rootPath)
	if err != nil {
		err = fmt.Errorf("failed to convert rootPath to UTF-16: %w", err)
		return
	}
	var (
		vName [bufferSize]uint16
		vSN   uint32
	)
	returnValue, _, _ := syscall.SyscallN(
		procGetVolumeInformation.Addr(),
		// [in, optional] lpRootPathName
		uintptr(unsafe.Pointer(volUTF16Ptr)),
		// [out, optional] lpVolumeNameBuffer
		uintptr(unsafe.Pointer(&vName[0])),
		// [in] nVolumeNameSize
		bufferSize,
		// [out, optional] lpVolumeSerialNumber
		uintptr(unsafe.Pointer(&vSN)),
		// [out, optional] lpMaximumComponentLength
		0,
		// [out, optional] lpFileSystemFlags
		0,
		// [out, optional] lpFileSystemNameBuffer
		0,
		// [out, optional] lpFileSystemNameBuffer
		0,
	)
	if returnValue == 0 {
		err = fmt.Errorf("Error getting volume infos for %s: %w", rootPath, syscall.GetLastError())
		return
	}
	volName = syscall.UTF16ToString(vName[:])
	serial = int(vSN)
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
