package managedwrite

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// openLogicalParent resolves every existing ancestor through directory
// handles. It rejects symbolic-link ancestors and verifies that the pathname
// still names the object that was opened before continuing. The returned Root
// is owned by the caller.
func openLogicalParent(root *os.Root, logical string) (*os.Root, string, error) {
	if root == nil || logical == "" || logical == "." || strings.HasPrefix(logical, "/") || strings.Contains(logical, "\\") {
		return nil, "", fmt.Errorf("invalid managed logical path %q", logical)
	}
	parts := strings.Split(logical, "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return nil, "", fmt.Errorf("invalid managed logical path %q", logical)
		}
	}
	current, err := root.OpenRoot(".")
	if err != nil {
		return nil, "", err
	}
	for _, part := range parts[:len(parts)-1] {
		before, err := current.Lstat(filepath.FromSlash(part))
		if err != nil {
			_ = current.Close()
			return nil, "", err
		}
		if before.Mode()&os.ModeSymlink != 0 || !before.IsDir() {
			_ = current.Close()
			return nil, "", fmt.Errorf("logical ancestor %q is not a real directory", part)
		}
		next, err := current.OpenRoot(filepath.FromSlash(part))
		if err != nil {
			_ = current.Close()
			return nil, "", err
		}
		opened, openErr := next.Stat(".")
		after, statErr := current.Lstat(filepath.FromSlash(part))
		if openErr != nil || statErr != nil || after.Mode()&os.ModeSymlink != 0 || !after.IsDir() || !os.SameFile(before, opened) || !os.SameFile(opened, after) {
			_ = next.Close()
			_ = current.Close()
			return nil, "", fmt.Errorf("logical ancestor %q changed while being resolved", part)
		}
		_ = current.Close()
		current = next
	}
	return current, filepath.FromSlash(parts[len(parts)-1]), nil
}

// openExactDirectory opens one logical directory without following a
// symbolic-link component and returns a stable physical handle plus its
// identity. The returned Root is owned by the caller.
func openExactDirectory(root *os.Root, logical string) (*os.Root, os.FileInfo, error) {
	if logical == "." {
		directory, err := root.OpenRoot(".")
		if err != nil {
			return nil, nil, err
		}
		info, err := directory.Stat(".")
		if err != nil || !info.IsDir() {
			_ = directory.Close()
			return nil, nil, errors.Join(err, fmt.Errorf("managed root is not a directory"))
		}
		return directory, info, nil
	}
	parent, base, err := openLogicalParent(root, logical)
	if err != nil {
		return nil, nil, err
	}
	defer parent.Close()
	before, err := parent.Lstat(base)
	if err != nil {
		return nil, nil, err
	}
	if before.Mode()&os.ModeSymlink != 0 || !before.IsDir() {
		return nil, nil, fmt.Errorf("directory boundary %q is not a real directory", logical)
	}
	directory, err := parent.OpenRoot(base)
	if err != nil {
		return nil, nil, err
	}
	opened, openErr := directory.Stat(".")
	after, statErr := parent.Lstat(base)
	if openErr != nil || statErr != nil || after.Mode()&os.ModeSymlink != 0 || !after.IsDir() || !os.SameFile(before, opened) || !os.SameFile(opened, after) {
		_ = directory.Close()
		return nil, nil, fmt.Errorf("directory boundary %q changed while being opened", logical)
	}
	return directory, opened, nil
}

func stableOpenedIdentity(file *os.File, before, after os.FileInfo, logical string) (string, error) {
	opened, err := file.Stat()
	if err != nil || !os.SameFile(before, opened) || !os.SameFile(opened, after) {
		return "", errors.Join(err, fmt.Errorf("filesystem object %q changed while its identity was captured", logical))
	}
	identity, ok := persistentFileID(file, opened)
	if !ok {
		return "", fmt.Errorf("persistent filesystem identity is unavailable for %q", logical)
	}
	return identity, nil
}
