//go:build !darwin && !dragonfly && !freebsd && !illumos && !linux && !netbsd && !openbsd && !windows

package pullflow

import (
	"errors"
	"os"
)

func lockReplayControllerFile(_ *os.File) error {
	return errors.New("replay controller advisory locks are unavailable on this platform")
}

func unlockReplayControllerFile(_ *os.File) error { return nil }
