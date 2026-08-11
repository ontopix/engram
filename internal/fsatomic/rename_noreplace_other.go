//go:build !darwin && !linux && !windows

package fsatomic

import (
	"errors"
	"os"
)

// RenameNoReplace deliberately fails closed on unsupported platforms. A
// check followed by os.Rename could replace a concurrently created target.
func RenameNoReplace(oldPath, newPath string) (bool, error) {
	return false, &os.LinkError{
		Op: "rename-noreplace", Old: oldPath, New: newPath,
		Err: errors.New("atomic no-replace directory publication is unsupported on this platform"),
	}
}
