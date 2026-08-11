//go:build windows

package doctor

import (
	"errors"
	"os"
	"syscall"

	"github.com/ontopix/engram/internal/rendezvous"
)

func ownerLiveness(owner rendezvous.Owner) ownerCondition {
	const processQueryLimitedInformation = 0x1000
	const errorInvalidParameter syscall.Errno = 87

	hostname, err := os.Hostname()
	if err != nil || hostname != owner.Hostname || uint64(owner.PID) > uint64(^uint32(0)) {
		return ownerUnknown
	}
	handle, err := syscall.OpenProcess(processQueryLimitedInformation, false, uint32(owner.PID))
	if err == nil {
		syscall.CloseHandle(handle)
		return ownerAlive
	}
	if errors.Is(err, errorInvalidParameter) {
		return ownerDead
	}
	return ownerUnknown
}
