//go:build darwin

// Package fsatomic provides the small platform binding needed to publish a
// prepared directory without ever replacing a concurrently created target.
package fsatomic

import (
	"os"
	"runtime"
	"syscall"
	"unsafe"
)

// RenameNoReplace uses Darwin's renameatx_np(RENAME_EXCL). The operation is
// one kernel transaction and therefore cannot replace a destination which
// appears after the caller's advisory existence check.
func RenameNoReplace(oldPath, newPath string) (bool, error) {
	oldPointer, err := syscall.BytePtrFromString(oldPath)
	if err != nil {
		return false, &os.LinkError{Op: "rename-noreplace", Old: oldPath, New: newPath, Err: err}
	}
	newPointer, err := syscall.BytePtrFromString(newPath)
	if err != nil {
		return false, &os.LinkError{Op: "rename-noreplace", Old: oldPath, New: newPath, Err: err}
	}
	const (
		renameatxNPTrap           = 488
		renameExclusive           = 0x4
		atCurrentWorkingDirectory = ^uintptr(1) // -2 (AT_FDCWD)
	)
	_, _, errno := syscall.Syscall6(
		renameatxNPTrap,
		atCurrentWorkingDirectory, uintptr(unsafe.Pointer(oldPointer)),
		atCurrentWorkingDirectory, uintptr(unsafe.Pointer(newPointer)),
		renameExclusive, 0,
	)
	runtime.KeepAlive(oldPointer)
	runtime.KeepAlive(newPointer)
	if errno != 0 {
		return false, &os.LinkError{Op: "rename-noreplace", Old: oldPath, New: newPath, Err: errno}
	}
	return true, nil
}
