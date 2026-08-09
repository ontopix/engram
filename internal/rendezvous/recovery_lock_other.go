//go:build !aix && !darwin && !dragonfly && !freebsd && !illumos && !linux && !netbsd && !openbsd && !windows

package rendezvous

import (
	"fmt"
	"os"
)

func lockRecoveryFile(_ *os.File) error {
	return fmt.Errorf("host has no supported recovery advisory lock")
}

func unlockRecoveryFile(_ *os.File) error { return nil }
