//go:build !windows

package pullflow

import "os"

func privateControllerMode(mode os.FileMode) bool {
	return mode.IsRegular() && mode.Perm()&0o077 == 0
}
