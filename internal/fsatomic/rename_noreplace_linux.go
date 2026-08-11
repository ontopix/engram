//go:build linux

package fsatomic

import (
	"errors"
	"os"
	"runtime"
	"syscall"
	"unsafe"
)

const linuxRenameNoReplace = 1

// RenameNoReplace atomically publishes oldPath only while newPath is absent.
// A kernel or architecture without renameat2 fails closed instead of falling
// back to the check-then-Rename race.
func RenameNoReplace(oldPath, newPath string) (bool, error) {
	trap := linuxRenameat2Trap()
	if trap == 0 {
		return false, &os.LinkError{Op: "rename-noreplace", Old: oldPath, New: newPath, Err: errors.New("renameat2 is unsupported on this Linux architecture")}
	}
	oldPointer, err := syscall.BytePtrFromString(oldPath)
	if err != nil {
		return false, &os.LinkError{Op: "rename-noreplace", Old: oldPath, New: newPath, Err: err}
	}
	newPointer, err := syscall.BytePtrFromString(newPath)
	if err != nil {
		return false, &os.LinkError{Op: "rename-noreplace", Old: oldPath, New: newPath, Err: err}
	}
	const atCurrentWorkingDirectory = ^uintptr(99) // -100 (AT_FDCWD)
	_, _, errno := syscall.Syscall6(
		trap,
		atCurrentWorkingDirectory, uintptr(unsafe.Pointer(oldPointer)),
		atCurrentWorkingDirectory, uintptr(unsafe.Pointer(newPointer)),
		linuxRenameNoReplace, 0,
	)
	runtime.KeepAlive(oldPointer)
	runtime.KeepAlive(newPointer)
	if errno != 0 {
		return false, &os.LinkError{Op: "rename-noreplace", Old: oldPath, New: newPath, Err: errno}
	}
	return true, nil
}

func linuxRenameat2Trap() uintptr {
	switch runtime.GOARCH {
	case "386":
		return 353
	case "amd64":
		return 316
	case "arm":
		return 382
	case "arm64", "loong64", "riscv64":
		return 276
	case "mips", "mipsle":
		return 4351
	case "mips64", "mips64le":
		return 5311
	case "ppc64", "ppc64le":
		return 357
	case "s390x":
		return 347
	default:
		return 0
	}
}
