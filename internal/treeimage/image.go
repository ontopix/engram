// Package treeimage captures and materializes byte-exact private filesystem
// trees for hooks and managed reconciliation. Traversal never follows a
// symbolic link.
package treeimage

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ontopix/engram/internal/gitraw"
	"github.com/ontopix/engram/internal/snapshot"
)

type Kind string

const (
	Directory Kind = "directory"
	Regular   Kind = "regular"
	Symlink   Kind = "symlink"
	Special   Kind = "special"
)

type Entry struct {
	Kind Kind
	Data []byte
	Mode fs.FileMode
}

type Image map[string]Entry

// Capture performs two no-follow observations of every path when stable is
// true. A mismatch is rejected rather than normalized or retried.
func Capture(root string, stable bool) (Image, error) {
	first, err := captureOnce(root)
	if err != nil || !stable {
		return first, err
	}
	second, err := captureOnce(root)
	if err != nil {
		return nil, err
	}
	if !Equal(first, second) {
		return nil, fmt.Errorf("filesystem tree changed during capture")
	}
	return first, nil
}

func captureOnce(root string) (Image, error) {
	info, err := os.Lstat(root)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, fmt.Errorf("capture root is not a real directory")
	}
	result := make(Image)
	var walk func(string, string) error
	walk = func(host, logical string) error {
		entries, err := os.ReadDir(host)
		if err != nil {
			return err
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		for _, directoryEntry := range entries {
			name := directoryEntry.Name()
			childHost := filepath.Join(host, name)
			childLogical := name
			if logical != "." {
				childLogical = logical + "/" + name
			}
			childInfo, err := os.Lstat(childHost)
			if err != nil {
				return err
			}
			entry := Entry{Mode: childInfo.Mode().Perm()}
			switch {
			case childInfo.Mode()&os.ModeSymlink != 0:
				entry.Kind = Symlink
				target, readErr := os.Readlink(childHost)
				if readErr != nil {
					return readErr
				}
				entry.Data = []byte(target)
			case childInfo.IsDir():
				entry.Kind = Directory
			case childInfo.Mode().IsRegular():
				entry.Kind = Regular
				before := childInfo
				entry.Data, err = os.ReadFile(childHost)
				if err != nil {
					return err
				}
				after, statErr := os.Lstat(childHost)
				if statErr != nil || !after.Mode().IsRegular() || !os.SameFile(before, after) {
					return fmt.Errorf("file %q changed while being read", childLogical)
				}
			default:
				entry.Kind = Special
			}
			result[childLogical] = entry
			if entry.Kind == Directory {
				if err := walk(childHost, childLogical); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := walk(root, "."); err != nil {
		return nil, err
	}
	return result, nil
}

// FromSnapshot builds the exact logical image of one projected snapshot.
// Regular-file modes follow annex A.4: surviving base files retain their
// accepted mode and all other regular files are 100644.
func FromSnapshot(tree *snapshot.Tree, baseModes map[string]gitraw.TreeMode) (Image, error) {
	if tree == nil {
		return nil, fmt.Errorf("snapshot tree is nil")
	}
	result := make(Image)
	for _, directory := range tree.Directories {
		if directory != "." {
			result[directory] = Entry{Kind: Directory, Mode: 0o755}
		}
	}
	for name, file := range tree.Files {
		for ancestor := filepath.ToSlash(filepath.Dir(filepath.FromSlash(name))); ancestor != "."; ancestor = filepath.ToSlash(filepath.Dir(filepath.FromSlash(ancestor))) {
			if _, exists := result[ancestor]; !exists {
				result[ancestor] = Entry{Kind: Directory, Mode: 0o755}
			}
		}
		mode := fs.FileMode(0o644)
		if baseModes[name] == gitraw.ModeExecutable {
			mode = 0o755
		}
		result[name] = Entry{Kind: Regular, Data: append([]byte(nil), file.Data...), Mode: mode}
	}
	return result, nil
}

// Materialize creates an absent destination and fills it exactly from image.
func Materialize(destination string, image Image, readOnly bool) error {
	if _, err := os.Lstat(destination); err == nil {
		return fmt.Errorf("materialization destination already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Mkdir(destination, 0o700); err != nil {
		return err
	}
	paths := SortedPaths(image)
	for _, name := range paths {
		entry := image[name]
		if entry.Kind != Directory {
			continue
		}
		if err := os.Mkdir(filepath.Join(destination, filepath.FromSlash(name)), 0o700); err != nil {
			return err
		}
	}
	for _, name := range paths {
		entry := image[name]
		if entry.Kind == Directory {
			continue
		}
		host := filepath.Join(destination, filepath.FromSlash(name))
		switch entry.Kind {
		case Regular:
			mode := entry.Mode.Perm()
			if mode == 0 {
				mode = 0o644
			}
			if err := os.WriteFile(host, entry.Data, mode); err != nil {
				return err
			}
		case Symlink:
			if err := os.Symlink(string(entry.Data), host); err != nil {
				return err
			}
		default:
			return fmt.Errorf("cannot materialize special path %q", name)
		}
	}
	if readOnly {
		for index := len(paths) - 1; index >= 0; index-- {
			name := paths[index]
			entry := image[name]
			if entry.Kind == Symlink {
				continue
			}
			mode := fs.FileMode(0o500)
			if entry.Kind == Regular {
				mode = 0o400
				if entry.Mode&0o111 != 0 {
					mode = 0o500
				}
			}
			if err := os.Chmod(filepath.Join(destination, filepath.FromSlash(name)), mode); err != nil {
				return err
			}
		}
		if err := os.Chmod(destination, 0o500); err != nil {
			return err
		}
	}
	return nil
}

// LogicalOnly proves that a hook result contains exactly paths visible to the
// supplied core projection. Any reserved/pruned path is rejected, including a
// hook-created .engram/cache boundary.
func LogicalOnly(image Image, projected *snapshot.Tree) error {
	if projected == nil {
		return fmt.Errorf("projected tree is nil")
	}
	visible := make(map[string]struct{}, len(projected.Files)+len(projected.Directories))
	for _, directory := range projected.Directories {
		if directory != "." {
			visible[directory] = struct{}{}
		}
	}
	for name := range projected.Files {
		visible[name] = struct{}{}
		for ancestor := filepath.ToSlash(filepath.Dir(filepath.FromSlash(name))); ancestor != "."; ancestor = filepath.ToSlash(filepath.Dir(filepath.FromSlash(ancestor))) {
			visible[ancestor] = struct{}{}
		}
	}
	for name := range image {
		if _, ok := visible[name]; !ok {
			return fmt.Errorf("candidate contains pruned path %q", name)
		}
	}
	return nil
}

func Equal(left, right Image) bool {
	if len(left) != len(right) {
		return false
	}
	for name, leftEntry := range left {
		rightEntry, ok := right[name]
		if !ok || leftEntry.Kind != rightEntry.Kind || !bytes.Equal(leftEntry.Data, rightEntry.Data) {
			return false
		}
	}
	return true
}

func SortedPaths(image Image) []string {
	result := make([]string, 0, len(image))
	for name := range image {
		result = append(result, name)
	}
	sort.Slice(result, func(i, j int) bool {
		leftDepth := strings.Count(result[i], "/")
		rightDepth := strings.Count(result[j], "/")
		if leftDepth != rightDepth {
			return leftDepth < rightDepth
		}
		return bytes.Compare([]byte(result[i]), []byte(result[j])) < 0
	})
	return result
}
