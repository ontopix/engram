//go:build windows

package lifecycle

import (
	"errors"
	"os"
	"syscall"

	"github.com/ontopix/engram/internal/rendezvous"
)

func ownerDead(owner rendezvous.Owner) (bool, error) {
	const processQueryLimitedInformation = 0x1000
	const errorInvalidParameter syscall.Errno = 87
	hostname, err := os.Hostname()
	if err != nil || hostname != owner.Hostname || uint64(owner.PID) > uint64(^uint32(0)) {
		return false, errors.Join(ErrOwnerLive, err)
	}
	handle, err := syscall.OpenProcess(processQueryLimitedInformation, false, uint32(owner.PID))
	if err == nil {
		_ = syscall.CloseHandle(handle)
		return false, ErrOwnerLive
	}
	if errors.Is(err, errorInvalidParameter) {
		return true, nil
	}
	return false, errors.Join(ErrOwnerLive, err)
}
