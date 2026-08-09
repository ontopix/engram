// Package discovery selects snapshot roots without inferring ownership from an
// enclosing project repository.
package discovery

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

var ErrNotFound = errors.New("no engram store found")

// From walks from start toward the filesystem root and returns the first
// directory containing the exact .engram/root.yaml marker path. It never
// follows a symlink supplied as start.
func From(start string) (string, error) {
	absolute, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("discovery start is a symbolic link")
	}
	if !info.IsDir() {
		absolute = filepath.Dir(absolute)
	}
	for {
		marker := filepath.Join(absolute, ".engram", "root.yaml")
		if _, err := os.Lstat(marker); err == nil {
			return absolute, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(absolute)
		if parent == absolute {
			return "", ErrNotFound
		}
		absolute = parent
	}
}

// Exact verifies that name is an existing directory and returns its absolute
// spelling. Snapshot conformance of its marker remains the checker's job.
func Exact(name string) (string, error) {
	absolute, err := filepath.Abs(name)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", fmt.Errorf("selected store is not a real directory")
	}
	return absolute, nil
}
