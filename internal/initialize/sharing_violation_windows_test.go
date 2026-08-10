//go:build windows

package initialize

import (
	"errors"
	"syscall"
)

// syscall does not export the Win32 ERROR_SHARING_VIOLATION name.
const errorSharingViolation syscall.Errno = 32

func isRenameSharingViolation(err error) bool {
	return errors.Is(err, errorSharingViolation)
}
