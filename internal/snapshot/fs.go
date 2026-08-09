package snapshot

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// FSSource reads beneath a Go 1.25 os.Root so logical traversal cannot escape
// its selected root through path spelling or symbolic-link components.
type FSSource struct {
	root *os.Root
}

// OpenFS opens an existing real directory as a boundary-safe source.
func OpenFS(name string) (*FSSource, error) {
	info, err := os.Lstat(name)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("snapshot root is a symbolic link")
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("snapshot root is not a directory")
	}
	root, err := os.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	return &FSSource{root: root}, nil
}

// Close releases the underlying root handle.
func (s *FSSource) Close() error {
	if s == nil || s.root == nil {
		return nil
	}
	return s.root.Close()
}

func (s *FSSource) ReadDir(logicalPath string) ([]Entry, error) {
	file, err := s.root.Open(native(logicalPath))
	if err != nil {
		return nil, err
	}
	entries, readErr := file.ReadDir(-1)
	closeErr := file.Close()
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	result := make([]Entry, 0, len(entries))
	for _, entry := range entries {
		child := entry.Name()
		if logicalPath != "." {
			child = filepath.Join(native(logicalPath), child)
		}
		info, err := s.root.Lstat(child)
		if err != nil {
			return nil, err
		}
		result = append(result, Entry{Name: entry.Name(), Kind: fileKind(info.Mode())})
	}
	return result, nil
}

func (s *FSSource) ReadFile(logicalPath string) ([]byte, error) {
	name := native(logicalPath)
	before, err := s.root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() {
		return nil, fmt.Errorf("not a real regular file")
	}
	data, err := s.root.ReadFile(name)
	if err != nil {
		return nil, err
	}
	after, err := s.root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if after.Mode()&os.ModeSymlink != 0 || !after.Mode().IsRegular() || !os.SameFile(before, after) {
		return nil, fmt.Errorf("file changed while being read")
	}
	return data, nil
}

func native(logicalPath string) string {
	if logicalPath == "." {
		return "."
	}
	return filepath.FromSlash(logicalPath)
}

func fileKind(mode fs.FileMode) Kind {
	switch {
	case mode&os.ModeSymlink != 0:
		return KindSymlink
	case mode.IsRegular():
		return KindRegular
	case mode.IsDir():
		return KindDirectory
	default:
		return KindSpecial
	}
}
