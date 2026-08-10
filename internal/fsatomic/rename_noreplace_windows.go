//go:build windows

package fsatomic

import (
	"os"
	"syscall"
)

// RenameNoReplace uses MoveFileW, which refuses an existing destination and
// moves a same-volume directory atomically.
func RenameNoReplace(oldPath, newPath string) (bool, error) {
	oldPointer, err := syscall.UTF16PtrFromString(oldPath)
	if err != nil {
		return false, &os.LinkError{Op: "rename-noreplace", Old: oldPath, New: newPath, Err: err}
	}
	newPointer, err := syscall.UTF16PtrFromString(newPath)
	if err != nil {
		return false, &os.LinkError{Op: "rename-noreplace", Old: oldPath, New: newPath, Err: err}
	}
	if err := syscall.MoveFile(oldPointer, newPointer); err != nil {
		return false, &os.LinkError{Op: "rename-noreplace", Old: oldPath, New: newPath, Err: err}
	}
	return true, nil
}
