//go:build !windows

package hooks

import (
	"errors"
	"os"
)

func privateRegistryFileMode(mode os.FileMode) bool {
	return mode.Perm()&0o077 == 0
}

func safeRegistryDirectoryMode(mode os.FileMode) bool {
	return mode.Perm()&0o022 == 0
}

func syncRegistryDirectory(directory string) error {
	_, err := syncRegistryDirectoryState(directory)
	return err
}

func syncRegistryDirectoryState(directory string) (bool, error) {
	handle, err := os.Open(directory)
	if err != nil {
		return false, err
	}
	syncErr := handle.Sync()
	closeErr := handle.Close()
	if syncErr != nil {
		return false, errors.Join(syncErr, closeErr)
	}
	return true, closeErr
}
