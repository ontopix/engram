//go:build !windows

package lifecycle

import (
	"errors"
	"os"
	"syscall"

	"github.com/ontopix/engram/internal/rendezvous"
)

func ownerDead(owner rendezvous.Owner) (bool, error) {
	hostname, err := os.Hostname()
	if err != nil || hostname != owner.Hostname {
		return false, errors.Join(ErrOwnerLive, err)
	}
	err = syscall.Kill(owner.PID, 0)
	switch {
	case errors.Is(err, syscall.ESRCH):
		return true, nil
	case err == nil, errors.Is(err, syscall.EPERM):
		return false, ErrOwnerLive
	default:
		return false, errors.Join(ErrOwnerLive, err)
	}
}
