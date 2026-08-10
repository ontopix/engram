//go:build !windows

package draft

import "io/fs"

func equivalentPermissions(left, right fs.FileMode) bool {
	return left.Perm() == right.Perm()
}
