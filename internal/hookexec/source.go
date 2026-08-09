package hookexec

import (
	"bytes"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/ontopix/engram/internal/snapshot"
	"github.com/ontopix/engram/internal/treeimage"
)

// imageSource lets the portable checker consume an immutable private image
// without rereading a hook-exposed filesystem tree.
type imageSource struct {
	image    treeimage.Image
	children map[string][]snapshot.Entry
}

func newImageSource(image treeimage.Image) (*imageSource, error) {
	source := &imageSource{image: cloneImage(image), children: map[string][]snapshot.Entry{".": {}}}
	paths := make([]string, 0, len(image))
	for logicalPath := range image {
		paths = append(paths, logicalPath)
	}
	sort.Slice(paths, func(i, j int) bool {
		leftDepth := strings.Count(paths[i], "/")
		rightDepth := strings.Count(paths[j], "/")
		if leftDepth != rightDepth {
			return leftDepth < rightDepth
		}
		return bytes.Compare([]byte(paths[i]), []byte(paths[j])) < 0
	})
	for _, logicalPath := range paths {
		if logicalPath == "" || logicalPath == "." || strings.HasPrefix(logicalPath, "/") || path.Clean(logicalPath) != logicalPath || strings.Contains(logicalPath, "\\") {
			return nil, fmt.Errorf("invalid captured logical path %q", logicalPath)
		}
		parent := path.Dir(logicalPath)
		if parent == "" {
			parent = "."
		}
		if parent != "." {
			entry, exists := source.image[parent]
			if !exists || entry.Kind != treeimage.Directory {
				return nil, fmt.Errorf("captured path %q has no directory parent", logicalPath)
			}
		}
		kind, err := snapshotKind(source.image[logicalPath].Kind)
		if err != nil {
			return nil, fmt.Errorf("captured path %q: %w", logicalPath, err)
		}
		source.children[parent] = append(source.children[parent], snapshot.Entry{Name: path.Base(logicalPath), Kind: kind})
		if kind == snapshot.KindDirectory {
			if _, exists := source.children[logicalPath]; exists {
				return nil, fmt.Errorf("duplicate captured directory %q", logicalPath)
			}
			source.children[logicalPath] = []snapshot.Entry{}
		}
	}
	for directory := range source.children {
		sort.Slice(source.children[directory], func(i, j int) bool {
			return bytes.Compare([]byte(source.children[directory][i].Name), []byte(source.children[directory][j].Name)) < 0
		})
	}
	return source, nil
}

func (s *imageSource) ReadDir(logicalPath string) ([]snapshot.Entry, error) {
	entries, exists := s.children[logicalPath]
	if !exists {
		return nil, fmt.Errorf("unknown captured directory %q", logicalPath)
	}
	return append([]snapshot.Entry(nil), entries...), nil
}

func (s *imageSource) ReadFile(logicalPath string) ([]byte, error) {
	entry, exists := s.image[logicalPath]
	if !exists || entry.Kind != treeimage.Regular {
		return nil, fmt.Errorf("captured path %q is not a regular file", logicalPath)
	}
	return append([]byte(nil), entry.Data...), nil
}

func snapshotKind(kind treeimage.Kind) (snapshot.Kind, error) {
	switch kind {
	case treeimage.Directory:
		return snapshot.KindDirectory, nil
	case treeimage.Regular:
		return snapshot.KindRegular, nil
	case treeimage.Symlink:
		return snapshot.KindSymlink, nil
	case treeimage.Special:
		return snapshot.KindSpecial, nil
	default:
		return 0, fmt.Errorf("unknown captured kind %q", kind)
	}
}

func cloneImage(source treeimage.Image) treeimage.Image {
	result := make(treeimage.Image, len(source))
	for name, entry := range source {
		entry.Data = append([]byte(nil), entry.Data...)
		result[name] = entry
	}
	return result
}
