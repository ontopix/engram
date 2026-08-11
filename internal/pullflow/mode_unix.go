//go:build !windows

package pullflow

func equivalentPathPermissions(left, right uint32) bool {
	return left == right
}
