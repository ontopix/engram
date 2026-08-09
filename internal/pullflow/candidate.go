package pullflow

import (
	"bytes"
	"fmt"
	"path"
	"sort"

	"github.com/ontopix/engram/internal/checker"
	"github.com/ontopix/engram/internal/gitraw"
	"github.com/ontopix/engram/internal/replay"
	"github.com/ontopix/engram/internal/snapshot"
)

type fileSource struct {
	children map[string][]snapshot.Entry
	files    replay.Files
}

func analyzeFiles(files replay.Files) (*checker.Snapshot, error) {
	source := &fileSource{children: map[string][]snapshot.Entry{".": {}}, files: make(replay.Files, len(files))}
	directorySet := map[string]struct{}{".": {}}
	for name, data := range files {
		if !validLogicalPath(name) {
			return nil, fmt.Errorf("invalid candidate path %q", name)
		}
		source.files[name] = append([]byte(nil), data...)
		for directory := path.Dir(name); directory != "."; directory = path.Dir(directory) {
			directorySet[directory] = struct{}{}
		}
	}
	directories := make([]string, 0, len(directorySet))
	for directory := range directorySet {
		directories = append(directories, directory)
		source.children[directory] = []snapshot.Entry{}
	}
	sort.Slice(directories, func(i, j int) bool { return bytes.Compare([]byte(directories[i]), []byte(directories[j])) < 0 })
	for _, directory := range directories {
		if directory == "." {
			continue
		}
		parent := path.Dir(directory)
		source.children[parent] = append(source.children[parent], snapshot.Entry{Name: path.Base(directory), Kind: snapshot.KindDirectory})
	}
	for name := range source.files {
		directory := path.Dir(name)
		source.children[directory] = append(source.children[directory], snapshot.Entry{Name: path.Base(name), Kind: snapshot.KindRegular})
	}
	for directory := range source.children {
		sort.Slice(source.children[directory], func(i, j int) bool {
			return bytes.Compare([]byte(source.children[directory][i].Name), []byte(source.children[directory][j].Name)) < 0
		})
	}
	return checker.CheckSource(source)
}

func (s *fileSource) ReadDir(logical string) ([]snapshot.Entry, error) {
	entries, ok := s.children[logical]
	if !ok {
		return nil, fmt.Errorf("unknown candidate directory %q", logical)
	}
	return append([]snapshot.Entry(nil), entries...), nil
}

func (s *fileSource) ReadFile(logical string) ([]byte, error) {
	data, ok := s.files[logical]
	if !ok {
		return nil, fmt.Errorf("unknown candidate file %q", logical)
	}
	return append([]byte(nil), data...), nil
}

func candidateModes(snapshot *checker.Snapshot, current map[string]gitraw.TreeMode) map[string]gitraw.TreeMode {
	result := make(map[string]gitraw.TreeMode, len(snapshot.Tree.Files))
	for name := range snapshot.Tree.Files {
		mode := gitraw.ModeRegular
		if existing, ok := current[name]; ok {
			mode = existing
		}
		result[name] = mode
	}
	return result
}
