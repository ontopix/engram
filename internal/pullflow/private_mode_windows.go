//go:build windows

package pullflow

import "os"

// Windows does not expose POSIX group/other permission bits through FileMode:
// a private Create/OpenFile(0600) is observed as 0666. Identity, regular-file
// and no-symlink checks remain enforceable and are performed by the caller.
func privateControllerMode(mode os.FileMode) bool {
	return mode.IsRegular()
}
