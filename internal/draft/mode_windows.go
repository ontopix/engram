//go:build windows

package draft

import "io/fs"

// os.Chmod on Windows can represent only the owner-write/read-only
// distinction. Go reports writable regular files as 0666 and read-only files
// as 0444, irrespective of the requested POSIX group/other bits.
func equivalentPermissions(left, right fs.FileMode) bool {
	return left.Perm()&0o200 == right.Perm()&0o200
}
