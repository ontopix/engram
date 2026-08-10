// Package gitpath translates path output from Git's command protocol into
// the host-native representation used by the Go filesystem APIs.
package gitpath

import (
	"errors"
	"path/filepath"
	"unicode/utf8"
)

// Absolute accepts one newline-free absolute path emitted by Git. Git for
// Windows deliberately prints forward slashes even though filepath and the
// rest of engram use backslashes there. Separator translation is a protocol
// boundary, not pathname normalization: dot components remain forbidden.
func Absolute(value string) (string, error) {
	if value == "" || !utf8.ValidString(value) {
		return "", errors.New("Git returned an empty or non-UTF-8 path")
	}
	native := filepath.FromSlash(value)
	if !filepath.IsAbs(native) || filepath.Clean(native) != native {
		return "", errors.New("Git returned a non-canonical absolute path")
	}
	return native, nil
}
