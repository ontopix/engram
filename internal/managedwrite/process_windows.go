//go:build windows

package managedwrite

import (
	"syscall"
	"unsafe"
)

const (
	processQueryLimitedInformation = 0x1000
	stillActive                    = 259
)

var (
	processKernel32        = syscall.NewLazyDLL("kernel32.dll")
	procGetExitCodeProcess = processKernel32.NewProc("GetExitCodeProcess")
)

func hostProcessAlive(pid int) (bool, error) {
	handle, err := syscall.OpenProcess(processQueryLimitedInformation, false, uint32(pid))
	if err != nil {
		if err == syscall.Errno(87) { // ERROR_INVALID_PARAMETER: no such PID.
			return false, nil
		}
		return false, err
	}
	defer syscall.CloseHandle(handle)
	var code uint32
	result, _, callErr := procGetExitCodeProcess.Call(uintptr(handle), uintptr(unsafe.Pointer(&code)))
	if result == 0 {
		return false, callErr
	}
	return code == stillActive, nil
}
