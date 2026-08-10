package managedread

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/ontopix/engram/internal/checker"
	"github.com/ontopix/engram/internal/gitraw"
	"github.com/ontopix/engram/internal/snapshot"
)

const (
	indexFlagIntentToAdd  = uint32(0x20000000)
	indexFlagSkipWorktree = uint32(0x40000000)
)

func (s *Store) readIndex(ctx context.Context, absoluteIndexPath string) ([]IndexEntry, error) {
	arguments := []string{
		"-c", "core.longpaths=true",
		"--no-pager", "--no-optional-locks", "--no-replace-objects",
		"-c", "core.hooksPath=" + os.DevNull, "-c", "core.fsmonitor=false", "-c", "core.untrackedCache=false",
		"-c", "maintenance.auto=false", "-c", "gc.auto=0",
		"-C", s.repository.Root,
		"ls-files", "--stage", "--debug", "--full-name", "-z",
		"--abbrev=" + strconv.Itoa(s.repository.Format.HexWidth()),
	}
	command := exec.CommandContext(ctx, s.git, arguments...)
	command.Env = isolatedGitEnvironment(os.Environ())
	if absoluteIndexPath != "" {
		command.Env = append(command.Env, "GIT_INDEX_FILE="+absoluteIndexPath)
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &IndexError{Kind: IndexMalformed, Detail: "Git could not resolve the index", Err: errors.Join(err, stderrError(stderr.Bytes()))}
	}
	entries, err := parseIndexListing(s.repository.Format, output)
	if err != nil {
		return nil, &IndexError{Kind: IndexMalformed, Detail: "invalid Git index listing", Err: err}
	}
	return entries, nil
}

func prunedIndexPaths(entries map[string]IndexEntry) []string {
	pruned := make([]string, 0)
	for _, name := range sortedStringKeys(entries) {
		entry := entries[name]
		parts := strings.Split(name, "/")
		directory := "."
		for index, component := range parts {
			mode := gitraw.ModeDirectory
			if index == len(parts)-1 {
				mode = entry.Mode
			}
			if indexPrunedWithoutCoreFinding(directory, component, mode) {
				pruned = append(pruned, name)
				break
			}
			if directory == "." {
				directory = component
			} else {
				directory += "/" + component
			}
		}
	}
	return pruned
}

// indexPrunedWithoutCoreFinding mirrors the raw projection boundary rule for
// the directory components synthesized from flat stage-zero index entries.
// A matching leaf cannot silently disappear from an initial candidate.
func indexPrunedWithoutCoreFinding(directory, name string, mode gitraw.TreeMode) bool {
	if directory == "." {
		return name == ".git" || strings.HasPrefix(name, ".") && name != ".engram"
	}
	if strings.HasSuffix(directory, "/.engram") || directory == ".engram" {
		switch name {
		case "root.yaml", "schemas":
			return false
		case "hooks", "cache":
			return directory == ".engram" && name == "cache" && mode == gitraw.ModeDirectory
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

func stderrError(value []byte) error {
	value = bytes.TrimSpace(value)
	if len(value) == 0 {
		return nil
	}
	return errors.New(string(value))
}

func isolatedGitEnvironment(environment []string) []string {
	result := make([]string, 0, len(environment)+8)
	for _, item := range environment {
		name, _, _ := strings.Cut(item, "=")
		upper := strings.ToUpper(name)
		if strings.HasPrefix(upper, "GIT_") || strings.HasPrefix(upper, "ENGRAM_") {
			continue
		}
		result = append(result, item)
	}
	return append(result,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_CONFIG_COUNT=0",
		"GIT_NO_LAZY_FETCH=1",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_OPTIONAL_LOCKS=0",
		"GIT_PAGER=cat",
	)
}

func parseIndexListing(format gitraw.ObjectFormat, output []byte) ([]IndexEntry, error) {
	entries := make([]IndexEntry, 0)
	for offset := 0; offset < len(output); {
		nul := bytes.IndexByte(output[offset:], 0)
		if nul < 0 {
			return nil, fmt.Errorf("record at byte %d has no path terminator", offset)
		}
		nul += offset
		header := output[offset:nul]
		firstSpace := bytes.IndexByte(header, ' ')
		if firstSpace <= 0 {
			return nil, fmt.Errorf("record at byte %d has no mode separator", offset)
		}
		secondSpaceRelative := bytes.IndexByte(header[firstSpace+1:], ' ')
		if secondSpaceRelative <= 0 {
			return nil, fmt.Errorf("record at byte %d has no object separator", offset)
		}
		secondSpace := firstSpace + 1 + secondSpaceRelative
		tabRelative := bytes.IndexByte(header[secondSpace+1:], '\t')
		if tabRelative <= 0 {
			return nil, fmt.Errorf("record at byte %d has no stage separator", offset)
		}
		tab := secondSpace + 1 + tabRelative

		mode := gitraw.TreeMode(header[:firstSpace])
		oid, err := gitraw.ParseOID(format, string(header[firstSpace+1:secondSpace]))
		if err != nil {
			return nil, fmt.Errorf("record at byte %d: %w", offset, err)
		}
		stageValue := header[secondSpace+1 : tab]
		if len(stageValue) != 1 || stageValue[0] < '0' || stageValue[0] > '3' {
			return nil, fmt.Errorf("record at byte %d has invalid stage", offset)
		}
		name := string(header[tab+1:])
		if !validIndexPath(name) {
			return nil, fmt.Errorf("record at byte %d has unsafe or non-UTF-8 path", offset)
		}

		debugStart := nul + 1
		flagsMarker := bytes.Index(output[debugStart:], []byte("\tflags: "))
		if flagsMarker < 0 {
			return nil, fmt.Errorf("record for %q has no debug flags", name)
		}
		flagsStart := debugStart + flagsMarker + len("\tflags: ")
		lineEndRelative := bytes.IndexByte(output[flagsStart:], '\n')
		if lineEndRelative < 0 {
			return nil, fmt.Errorf("record for %q has unterminated debug flags", name)
		}
		lineEnd := flagsStart + lineEndRelative
		flags64, err := strconv.ParseUint(string(output[flagsStart:lineEnd]), 16, 32)
		if err != nil {
			return nil, fmt.Errorf("record for %q has invalid debug flags", name)
		}
		flags := uint32(flags64)
		entries = append(entries, IndexEntry{
			Path:         name,
			Mode:         mode,
			Object:       oid.String(),
			Stage:        uint8(stageValue[0] - '0'),
			IntentToAdd:  flags&indexFlagIntentToAdd != 0,
			SkipWorktree: flags&indexFlagSkipWorktree != 0,
			oid:          oid,
			flags:        flags,
		})
		offset = lineEnd + 1
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].Path != entries[j].Path {
			return bytes.Compare([]byte(entries[i].Path), []byte(entries[j].Path)) < 0
		}
		return entries[i].Stage < entries[j].Stage
	})
	return entries, nil
}

func validIndexPath(name string) bool {
	return name != "" && utf8.ValidString(name) && !strings.HasPrefix(name, "/") &&
		path.Clean(name) == name && name != "." && !strings.HasPrefix(name, "../")
}

func resolveIndex(entries []IndexEntry) (map[string]IndexEntry, error) {
	byPath := make(map[string][]IndexEntry)
	for _, entry := range entries {
		byPath[entry.Path] = append(byPath[entry.Path], entry)
	}
	conflicts := make([]string, 0)
	resolved := make(map[string]IndexEntry, len(byPath))
	for _, name := range sortedStringKeys(byPath) {
		pathEntries := byPath[name]
		eligible := len(pathEntries) == 1 && pathEntries[0].Stage == 0 && !pathEntries[0].IntentToAdd
		if !eligible {
			conflicts = append(conflicts, name)
			continue
		}
		entry := pathEntries[0]
		switch entry.Mode {
		case gitraw.ModeRegular, gitraw.ModeExecutable, gitraw.ModeSymlink, gitraw.ModeGitlink:
			resolved[name] = entry
		default:
			return nil, &IndexError{Kind: IndexMode, Paths: []string{name}, Detail: fmt.Sprintf("unadmitted stage-zero mode %q", entry.Mode)}
		}
	}
	if len(conflicts) != 0 {
		return nil, &IndexError{Kind: IndexConflict, Paths: conflicts, Detail: "each path must have exactly one non-intent stage-zero entry and no higher stage"}
	}
	return resolved, nil
}

func validateCandidateModes(baseModes map[string]gitraw.TreeMode, entries map[string]IndexEntry, candidate *checker.Snapshot) error {
	for _, name := range sortedSnapshotFiles(candidate) {
		entry, ok := entries[name]
		if !ok || !entry.Mode.IsRegular() {
			continue
		}
		want := gitraw.ModeRegular
		if baseMode, survives := baseModes[name]; survives {
			want = baseMode
		}
		if entry.Mode != want {
			return &IndexError{
				Kind:   IndexMode,
				Paths:  []string{name},
				Detail: fmt.Sprintf("candidate mode %s does not match required mode %s", entry.Mode, want),
			}
		}
	}
	return nil
}

func sortedSnapshotFiles(value *checker.Snapshot) []string {
	if value == nil || value.Tree == nil {
		return nil
	}
	result := make([]string, 0, len(value.Tree.Files))
	for name := range value.Tree.Files {
		result = append(result, name)
	}
	sort.Slice(result, func(i, j int) bool { return bytes.Compare([]byte(result[i]), []byte(result[j])) < 0 })
	return result
}

func cloneIndexEntries(entries []IndexEntry) []IndexEntry {
	return append([]IndexEntry(nil), entries...)
}

type indexSource struct {
	ctx      context.Context
	reader   gitraw.Reader
	entries  map[string]IndexEntry
	children map[string][]snapshot.Entry
}

func newIndexSource(ctx context.Context, reader gitraw.Reader, entries map[string]IndexEntry) (*indexSource, error) {
	source := &indexSource{
		ctx:      ctx,
		reader:   reader,
		entries:  make(map[string]IndexEntry, len(entries)),
		children: map[string][]snapshot.Entry{".": nil},
	}
	type nodeKind uint8
	const (
		nodeDirectory nodeKind = iota + 1
		nodeFile
	)
	nodes := map[string]nodeKind{".": nodeDirectory}
	for _, name := range sortedStringKeys(entries) {
		entry := entries[name]
		parts := strings.Split(name, "/")
		directory := "."
		for index, component := range parts {
			childPath := component
			if directory != "." {
				childPath = directory + "/" + component
			}
			last := index == len(parts)-1
			wantKind := nodeDirectory
			snapshotEntryKind := snapshot.KindDirectory
			if last {
				wantKind = nodeFile
				snapshotEntryKind = indexSnapshotKind(entry.Mode)
			}
			if existing, exists := nodes[childPath]; exists && existing != wantKind {
				return nil, &IndexError{Kind: IndexConflict, Paths: []string{childPath}, Detail: "file/directory index collision"}
			}
			if _, exists := nodes[childPath]; !exists {
				nodes[childPath] = wantKind
				source.children[directory] = append(source.children[directory], snapshot.Entry{Name: component, Kind: snapshotEntryKind})
				if wantKind == nodeDirectory {
					source.children[childPath] = nil
				}
			}
			if last {
				source.entries[childPath] = entry
			} else {
				directory = childPath
			}
		}
	}
	return source, nil
}

func indexSnapshotKind(mode gitraw.TreeMode) snapshot.Kind {
	switch mode {
	case gitraw.ModeRegular, gitraw.ModeExecutable:
		return snapshot.KindRegular
	case gitraw.ModeSymlink:
		return snapshot.KindSymlink
	default:
		return snapshot.KindSpecial
	}
}

func (s *indexSource) ReadDir(logicalPath string) ([]snapshot.Entry, error) {
	entries, ok := s.children[logicalPath]
	if !ok {
		return nil, fmt.Errorf("unknown index directory %q", logicalPath)
	}
	return append([]snapshot.Entry(nil), entries...), nil
}

func (s *indexSource) ReadFile(logicalPath string) ([]byte, error) {
	entry, ok := s.entries[logicalPath]
	if !ok || !entry.Mode.IsRegular() {
		return nil, fmt.Errorf("%s is not an index regular file", logicalPath)
	}
	object, err := s.reader.ReadObject(s.ctx, entry.oid)
	if err != nil {
		return nil, err
	}
	if object.Type != gitraw.TypeBlob {
		return nil, &gitraw.Error{
			Kind:   gitraw.FailureWrongType,
			Op:     "read-index-blob",
			OID:    entry.oid,
			Detail: fmt.Sprintf("got %s, want %s", object.Type, gitraw.TypeBlob),
		}
	}
	return append([]byte(nil), object.Data...), nil
}

func sortedStringKeys[V any](values map[string]V) []string {
	result := make([]string, 0, len(values))
	for name := range values {
		result = append(result, name)
	}
	sort.Slice(result, func(i, j int) bool { return bytes.Compare([]byte(result[i]), []byte(result[j])) < 0 })
	return result
}
