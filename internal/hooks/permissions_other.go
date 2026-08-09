//go:build !windows

package hooks

import "os"

func privateRegistryFileMode(mode os.FileMode) bool {
	return mode.Perm()&0o077 == 0
}

func safeRegistryDirectoryMode(mode os.FileMode) bool {
	return mode.Perm()&0o022 == 0
}

func syncRegistryDirectory(directory string) error {
	handle, err := os.Open(directory)
	if err != nil {
		return err
	}
	syncErr := handle.Sync()
	closeErr := handle.Close()
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}
