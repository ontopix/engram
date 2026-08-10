//go:build !windows

package managedwrite

func equivalentPathPermissions(left, right uint32) bool {
	return left == right
}
