//go:build !windows

package doctor

import (
	"errors"
	"os"
	"syscall"

	"github.com/ontopix/engram/internal/rendezvous"
)

func ownerLiveness(owner rendezvous.Owner) ownerCondition {
	hostname, err := os.Hostname()
	if err != nil || hostname != owner.Hostname {
		return ownerUnknown
	}
	err = syscall.Kill(owner.PID, 0)
	switch {
	case err == nil, errors.Is(err, syscall.EPERM):
		return ownerAlive
	case errors.Is(err, syscall.ESRCH):
		return ownerDead
	default:
		return ownerUnknown
	}
}
