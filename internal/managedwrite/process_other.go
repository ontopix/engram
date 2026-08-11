//go:build !aix && !darwin && !dragonfly && !freebsd && !illumos && !linux && !netbsd && !openbsd && !windows

package managedwrite

import "fmt"

func hostProcessAlive(pid int) (bool, error) {
	return false, fmt.Errorf("host cannot prove liveness of process %d", pid)
}
