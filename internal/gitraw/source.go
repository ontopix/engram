package gitraw

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strings"
	"sync"

	"github.com/ontopix/engram/internal/snapshot"
)

type treeNode struct {
	mode TreeMode
	oid  OID
}

// TreeSource lazily exposes one raw tree through snapshot.Source. Child
// targets are resolved only when ReadDir or ReadFile asks for them.
type TreeSource struct {
	ctx    context.Context
	reader Reader
	format ObjectFormat

	mu           sync.Mutex
	nodes        map[string]treeNode
	prunedNoCore map[string]struct{}
}

func NewTreeSource(ctx context.Context, reader Reader, root OID) *TreeSource {
	if ctx == nil {
		ctx = context.Background()
	}
	return &TreeSource{
		ctx:          ctx,
		reader:       reader,
		format:       root.Format(),
		nodes:        map[string]treeNode{".": {mode: ModeDirectory, oid: root}},
		prunedNoCore: make(map[string]struct{}),
	}
}

var _ snapshot.Source = (*TreeSource)(nil)

func (s *TreeSource) ReadDir(logicalPath string) ([]snapshot.Entry, error) {
	node, err := s.node(logicalPath)
	if err != nil {
		return nil, err
	}
	if node.mode != ModeDirectory {
		return nil, fmt.Errorf("%s is not a directory", logicalPath)
	}
	object, err := s.reader.ReadObject(s.ctx, node.oid)
	if err != nil {
		return nil, err
	}
	if object.Type != TypeTree {
		return nil, wrongType("read-tree", node.oid, object.Type, TypeTree)
	}
	entries, err := ParseTree(s.format, object.Data)
	if err != nil {
		return nil, err
	}
	result := make([]snapshot.Entry, 0, len(entries))
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, entry := range entries {
		name := string(entry.Name)
		child := logicalJoin(logicalPath, name)
		s.nodes[child] = treeNode{mode: entry.Mode, oid: entry.OID}
		if prunedWithoutCoreFinding(logicalPath, name, entry.Mode) {
			s.prunedNoCore[child] = struct{}{}
		}
		result = append(result, snapshot.Entry{Name: name, Kind: snapshotKind(entry.Mode)})
	}
	return result, nil
}

func (s *TreeSource) ReadFile(logicalPath string) ([]byte, error) {
	node, err := s.node(logicalPath)
	if err != nil {
		return nil, err
	}
	if !node.mode.IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", logicalPath)
	}
	object, err := s.reader.ReadObject(s.ctx, node.oid)
	if err != nil {
		return nil, err
	}
	if object.Type != TypeBlob {
		return nil, wrongType("read-blob", node.oid, object.Type, TypeBlob)
	}
	return append([]byte(nil), object.Data...), nil
}

func (s *TreeSource) PrunedWithoutCoreFinding() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	paths := make([]string, 0, len(s.prunedNoCore))
	for logicalPath := range s.prunedNoCore {
		paths = append(paths, logicalPath)
	}
	sort.Strings(paths)
	return paths
}

func (s *TreeSource) node(logicalPath string) (treeNode, error) {
	if logicalPath == "" || (logicalPath != "." && (path.Clean(logicalPath) != logicalPath || strings.HasPrefix(logicalPath, "../") || strings.HasPrefix(logicalPath, "/"))) {
		return treeNode{}, fmt.Errorf("unsafe logical path %q", logicalPath)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	node, ok := s.nodes[logicalPath]
	if !ok {
		return treeNode{}, fmt.Errorf("unknown logical path %q", logicalPath)
	}
	return node, nil
}

func snapshotKind(mode TreeMode) snapshot.Kind {
	switch mode {
	case ModeDirectory:
		return snapshot.KindDirectory
	case ModeRegular, ModeExecutable:
		return snapshot.KindRegular
	case ModeSymlink:
		return snapshot.KindSymlink
	default:
		return snapshot.KindSpecial
	}
}

func logicalJoin(directory, name string) string {
	if directory == "." {
		return name
	}
	return path.Join(directory, name)
}

func prunedWithoutCoreFinding(directory, name string, mode TreeMode) bool {
	if directory == "." {
		return name == ".git" || strings.HasPrefix(name, ".") && name != ".engram"
	}
	if strings.HasSuffix(directory, "/.engram") || directory == ".engram" {
		switch name {
		case "root.yaml", "schemas":
			return false
		case "hooks", "routines", "cache":
			return directory == ".engram" && name == "cache" && mode == ModeDirectory
		default:
			return true
		}
	}
	if !strings.Contains(directory, "/.engram/") && !strings.HasPrefix(directory, ".engram/") {
		if name == ".git" {
			return false
		}
		return strings.HasPrefix(name, ".") && name != ".engram"
	}
	return false
}
