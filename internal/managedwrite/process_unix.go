//go:build aix || darwin || dragonfly || freebsd || illumos || linux || netbsd || openbsd

package managedwrite

import (
	"errors"
	"os"
	"syscall"
)

func hostProcessAlive(pid int) (bool, error) {
	process, err := os.FindProcess(pid)
	if err != nil {
		return false, err
	}
	err = process.Signal(syscall.Signal(0))
	if err == nil || errors.Is(err, syscall.EPERM) {
		return true, nil
	}
	if errors.Is(err, syscall.ESRCH) {
		return false, nil
	}
	return false, err
}
