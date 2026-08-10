//go:build darwin || dragonfly || freebsd || illumos || linux || netbsd || openbsd

package pullflow

import (
	"errors"
	"os"
	"syscall"
)

func lockReplayControllerFile(file *os.File) error {
	if file == nil {
		return errors.New("nil replay controller lock")
	}
	err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
		return errReplayControllerBusy
	}
	return err
}

func unlockReplayControllerFile(file *os.File) error {
	if file == nil {
		return nil
	}
	return syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
}
