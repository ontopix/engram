// Package fileidentity materializes the physical identity carried by an
// os.FileInfo before its path can be replaced.
package fileidentity

import (
	"errors"
	"os"
)

// ErrUnavailable reports that a FileInfo does not carry a usable physical
// identity. On Windows, Stat and Lstat defer loading that identity from the
// path until the first os.SameFile call; comparing the value with itself pins
// it while the observed path still names the intended filesystem object.
var ErrUnavailable = errors.New("physical filesystem identity is unavailable")

// Pin materializes info's physical identity immediately. Callers that retain
// a FileInfo across callbacks, subprocesses, or filesystem mutations must pin
// it before allowing any such operation.
func Pin(info os.FileInfo) error {
	if info == nil || !os.SameFile(info, info) {
		return ErrUnavailable
	}
	return nil
}

// PersistentID returns a platform-qualified physical identity obtained from
// file's already-open descriptor. Unlike os.FileInfo.Sys on Windows, this does
// not discard the volume serial number and file index. The caller must pass an
// info value captured from the same descriptor while it still names the
// object being approved.
func PersistentID(file *os.File, info os.FileInfo) ([]byte, error) {
	if file == nil || info == nil {
		return nil, ErrUnavailable
	}
	identity, ok := persistentID(file, info)
	if !ok || len(identity) == 0 {
		return nil, ErrUnavailable
	}
	return append([]byte(nil), identity...), nil
}
