//go:build windows

package hooks

import "os"

// Go's synthetic Windows permission bits do not express ACL ownership or
// sharing. Atomic files are created without requesting sharing access; the
// controller directory's ACL remains the authority boundary.
func privateRegistryFileMode(_ os.FileMode) bool { return true }

func safeRegistryDirectoryMode(_ os.FileMode) bool { return true }

// Windows does not expose a portable directory-fsync operation through os;
// the file itself is flushed before the atomic replacement.
func syncRegistryDirectory(_ string) error { return nil }
