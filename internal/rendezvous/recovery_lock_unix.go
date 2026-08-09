//go:build aix || darwin || dragonfly || freebsd || illumos || linux || netbsd || openbsd

package rendezvous

import (
	"errors"
	"os"
	"syscall"
)

func lockRecoveryFile(file *os.File) error {
	err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
		return errRecoveryBusy
	}
	return err
}

func unlockRecoveryFile(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
}
