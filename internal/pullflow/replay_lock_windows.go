//go:build windows

package pullflow

import (
	"errors"
	"os"
	"syscall"
	"unsafe"
)

const (
	lockfileExclusiveLock   = 0x00000002
	lockfileFailImmediately = 0x00000001
	errorSharingViolation   = syscall.Errno(32)
	errorLockViolation      = syscall.Errno(33)
)

var (
	kernel32LockFileEx   = syscall.NewLazyDLL("kernel32.dll").NewProc("LockFileEx")
	kernel32UnlockFileEx = syscall.NewLazyDLL("kernel32.dll").NewProc("UnlockFileEx")
)

func lockReplayControllerFile(file *os.File) error {
	if file == nil {
		return errors.New("nil replay controller lock")
	}
	var overlapped syscall.Overlapped
	result, _, callErr := kernel32LockFileEx.Call(
		file.Fd(), uintptr(lockfileExclusiveLock|lockfileFailImmediately), 0,
		1, 0, uintptr(unsafe.Pointer(&overlapped)),
	)
	if result != 0 {
		return nil
	}
	if errors.Is(callErr, errorLockViolation) || errors.Is(callErr, errorSharingViolation) {
		return errReplayControllerBusy
	}
	return callErr
}

func unlockReplayControllerFile(file *os.File) error {
	if file == nil {
		return nil
	}
	var overlapped syscall.Overlapped
	result, _, callErr := kernel32UnlockFileEx.Call(
		file.Fd(), 0, 1, 0, uintptr(unsafe.Pointer(&overlapped)),
	)
	if result == 0 {
		return callErr
	}
	return nil
}
