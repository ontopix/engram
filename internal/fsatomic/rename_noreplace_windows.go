//go:build windows

package fsatomic

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// RenameNoReplace uses MoveFileW, which refuses an existing destination and
// moves a same-volume directory atomically.
func RenameNoReplace(oldPath, newPath string) (bool, error) {
	oldNative, err := extendedPath(oldPath)
	if err != nil {
		return false, &os.LinkError{Op: "rename-noreplace", Old: oldPath, New: newPath, Err: err}
	}
	newNative, err := extendedPath(newPath)
	if err != nil {
		return false, &os.LinkError{Op: "rename-noreplace", Old: oldPath, New: newPath, Err: err}
	}
	oldPointer, err := syscall.UTF16PtrFromString(oldNative)
	if err != nil {
		return false, &os.LinkError{Op: "rename-noreplace", Old: oldPath, New: newPath, Err: err}
	}
	newPointer, err := syscall.UTF16PtrFromString(newNative)
	if err != nil {
		return false, &os.LinkError{Op: "rename-noreplace", Old: oldPath, New: newPath, Err: err}
	}
	if err := syscall.MoveFile(oldPointer, newPointer); err != nil {
		// MoveFileW sometimes reports access denied, rather than
		// ERROR_ALREADY_EXISTS, when the colliding destination is a directory.
		// The observable fact is still a no-replace collision.
		if _, statErr := os.Lstat(newPath); statErr == nil {
			err = errors.Join(os.ErrExist, err)
		}
		return false, &os.LinkError{Op: "rename-noreplace", Old: oldPath, New: newPath, Err: err}
	}
	return true, nil
}

func extendedPath(name string) (string, error) {
	if strings.HasPrefix(name, `\\?\`) {
		return name, nil
	}
	absolute, err := filepath.Abs(name)
	if err != nil {
		return "", err
	}
	absolute = filepath.Clean(absolute)
	if strings.HasPrefix(absolute, `\\.\`) {
		return "", errors.New("device paths are not valid publication paths")
	}
	if strings.HasPrefix(absolute, `\\`) {
		return `\\?\UNC\` + strings.TrimPrefix(absolute, `\\`), nil
	}
	return `\\?\` + absolute, nil
}
