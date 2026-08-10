//go:build !windows

package initialize

func isRenameSharingViolation(error) bool { return false }
