//go:build windows

package managedwrite

// Windows exposes only the owner-write/read-only distinction for regular
// files through os.FileMode. Group/other and executable bits are not
// representable worktree evidence there.
func equivalentPathPermissions(left, right uint32) bool {
	return left&0o200 == right&0o200
}
