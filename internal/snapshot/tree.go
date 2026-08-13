// Package snapshot projects a byte-exact logical engram snapshot from a
// boundary-safe source. It applies only traversal, name, and kind precedence;
// content validation belongs to the portable checker.
package snapshot

import (
	"bytes"
	"fmt"
	"path"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/ontopix/engram/internal/unicode17"
)

// Kind is the kind observed at one boundary without following it.
type Kind uint8

const (
	KindRegular Kind = iota + 1
	KindDirectory
	KindSymlink
	KindSpecial
)

// Entry is one direct child returned by Source.ReadDir.
type Entry struct {
	Name string
	Kind Kind
}

// Source supplies exact logical tree operations. Implementations must not
// follow symbolic links and must interpret names as slash-separated paths
// rooted at their selected snapshot.
type Source interface {
	ReadDir(logicalPath string) ([]Entry, error)
	ReadFile(logicalPath string) ([]byte, error)
}

// FileRole identifies the normative parser, if any, for a traversed file.
type FileRole uint8

const (
	RoleAsset FileRole = iota
	RoleRecord
	RoleMap
	RoleRootManifest
	RoleSchema
	RoleHook
)

// File is one observed regular file in the logical validation tree.
type File struct {
	Path string
	Role FileRole
	Data []byte
}

// Issue is a boundary finding whose identity is already fully determined.
type Issue struct {
	Code string
	Path string
}

// Tree is a deterministic logical projection. Directories includes the root
// as "." and excludes all pruned boundaries.
type Tree struct {
	Directories []string
	Files       map[string]File
	Boundaries  map[string]Kind
	Issues      []Issue
}

// Load traverses source using core §2.4 precedence.
func Load(source Source) (*Tree, error) {
	if source == nil {
		return nil, fmt.Errorf("snapshot source is nil")
	}
	tree := &Tree{Files: make(map[string]File), Boundaries: make(map[string]Kind)}
	walker := walker{source: source, tree: tree, seenIssue: make(map[Issue]struct{})}
	if err := walker.contentDir(".", true); err != nil {
		return nil, err
	}
	sort.Slice(tree.Issues, func(i, j int) bool {
		if tree.Issues[i].Path != tree.Issues[j].Path {
			return bytes.Compare([]byte(tree.Issues[i].Path), []byte(tree.Issues[j].Path)) < 0
		}
		return tree.Issues[i].Code < tree.Issues[j].Code
	})
	return tree, nil
}

type walker struct {
	source    Source
	tree      *Tree
	seenIssue map[Issue]struct{}
}

func (w *walker) contentDir(directory string, root bool) error {
	w.tree.Directories = append(w.tree.Directories, directory)
	entries, err := w.entries(directory)
	if err != nil {
		return fmt.Errorf("read directory %q: %w", directory, err)
	}

	validNames := make(map[string][]string)
	for _, entry := range entries {
		if entry.Name == "README.md" {
			validNames[unicode17.CaseFoldKey(entry.Name)] = append(validNames[unicode17.CaseFoldKey(entry.Name)], entry.Name)
			continue
		}
		if strings.HasPrefix(entry.Name, ".") {
			continue
		}
		if validContentName(entry.Name) {
			validNames[unicode17.CaseFoldKey(entry.Name)] = append(validNames[unicode17.CaseFoldKey(entry.Name)], entry.Name)
		}
	}
	for _, names := range validNames {
		if len(names) > 1 {
			w.issue("E106", directory)
			break
		}
	}

	for _, entry := range entries {
		child := join(directory, entry.Name)
		w.tree.Boundaries[child] = entry.Kind
		if entry.Name != "README.md" && !strings.HasPrefix(entry.Name, ".") {
			if !validContentName(entry.Name) {
				w.issue("E107", directory)
				continue
			}
		}

		switch {
		case entry.Name == ".git":
			if !root {
				w.issue("E110", child)
			}
			continue
		case entry.Name == ".engram":
			if entry.Kind == KindSymlink {
				w.issue("E103", child)
				continue
			}
			if entry.Kind != KindDirectory {
				w.issue("E104", child)
				continue
			}
			if err := w.configDir(child, root); err != nil {
				return err
			}
			continue
		case strings.HasPrefix(entry.Name, "."):
			continue
		}

		if entry.Kind == KindSymlink {
			w.issue("E103", child)
			continue
		}
		if entry.Name == "README.md" {
			if entry.Kind != KindRegular {
				w.issue("E104", child)
				continue
			}
			if err := w.file(child, RoleMap); err != nil {
				return err
			}
			continue
		}
		switch entry.Kind {
		case KindDirectory:
			if err := w.contentDir(child, false); err != nil {
				return err
			}
		case KindRegular:
			role := RoleAsset
			if strings.HasSuffix(entry.Name, ".md") {
				role = RoleRecord
			}
			if err := w.file(child, role); err != nil {
				return err
			}
		default:
			w.issue("E104", child)
		}
	}
	return nil
}

func (w *walker) configDir(directory string, root bool) error {
	entries, err := w.entries(directory)
	if err != nil {
		return fmt.Errorf("read configuration directory %q: %w", directory, err)
	}
	for _, entry := range entries {
		child := join(directory, entry.Name)
		w.tree.Boundaries[child] = entry.Kind
		switch entry.Name {
		case "root.yaml":
			if entry.Kind == KindSymlink {
				w.issue("E103", child)
				continue
			}
			if entry.Kind != KindRegular {
				w.issue("E104", child)
				continue
			}
			if !root {
				w.issue("E102", child)
			}
			if err := w.file(child, RoleRootManifest); err != nil {
				return err
			}
		case "schemas":
			if entry.Kind == KindSymlink {
				w.issue("E103", child)
				continue
			}
			if entry.Kind != KindDirectory {
				w.issue("E303", child)
				continue
			}
			if err := w.schemaDir(child); err != nil {
				return err
			}
		case "hooks":
			if entry.Kind == KindSymlink {
				w.issue("E103", child)
				continue
			}
			if entry.Kind != KindDirectory {
				w.issue("E308", child)
				continue
			}
			if err := w.hooksDir(child); err != nil {
				return err
			}
		case "cache":
			if !root {
				w.issue("E109", child)
				continue
			}
			if entry.Kind == KindSymlink {
				w.issue("E103", child)
			} else if entry.Kind != KindDirectory {
				w.issue("E104", child)
			}
		default:
			// Tool-specific configuration is reserved and unobserved.
		}
	}
	return nil
}

func (w *walker) schemaDir(directory string) error {
	entries, err := w.entries(directory)
	if err != nil {
		return fmt.Errorf("read schema directory %q: %w", directory, err)
	}
	for _, entry := range entries {
		w.tree.Boundaries[join(directory, entry.Name)] = entry.Kind
		if !validSchemaFilename(entry.Name) {
			w.issue("E303", directory)
			continue
		}
		child := join(directory, entry.Name)
		if entry.Kind == KindSymlink {
			w.issue("E103", child)
			continue
		}
		if entry.Kind != KindRegular {
			w.issue("E303", directory)
			continue
		}
		if err := w.file(child, RoleSchema); err != nil {
			return err
		}
	}
	return nil
}

func (w *walker) hooksDir(directory string) error {
	entries, err := w.entries(directory)
	if err != nil {
		return fmt.Errorf("read hook directory %q: %w", directory, err)
	}
	for _, entry := range entries {
		child := join(directory, entry.Name)
		w.tree.Boundaries[child] = entry.Kind
		if entry.Name != "prepare-changeset" {
			w.issue("E308", directory)
			continue
		}
		if entry.Kind == KindSymlink {
			w.issue("E103", child)
			continue
		}
		if entry.Kind != KindDirectory {
			w.issue("E308", child)
			continue
		}
		if err := w.hookProgramDir(child); err != nil {
			return err
		}
	}
	return nil
}

func (w *walker) hookProgramDir(directory string) error {
	entries, err := w.entries(directory)
	if err != nil {
		return fmt.Errorf("read hook program directory %q: %w", directory, err)
	}
	for _, entry := range entries {
		w.tree.Boundaries[join(directory, entry.Name)] = entry.Kind
		if !validHookFilename(entry.Name) {
			w.issue("E308", directory)
			continue
		}
		child := join(directory, entry.Name)
		if entry.Kind == KindSymlink {
			w.issue("E103", child)
			continue
		}
		if entry.Kind != KindRegular {
			w.issue("E308", directory)
			continue
		}
		if err := w.file(child, RoleHook); err != nil {
			return err
		}
	}
	return nil
}

func (w *walker) entries(directory string) ([]Entry, error) {
	entries, err := w.source.ReadDir(directory)
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool {
		return bytes.Compare([]byte(entries[i].Name), []byte(entries[j].Name)) < 0
	})
	return entries, nil
}

func (w *walker) file(logicalPath string, role FileRole) error {
	data, err := w.source.ReadFile(logicalPath)
	if err != nil {
		return fmt.Errorf("read file %q: %w", logicalPath, err)
	}
	w.tree.Files[logicalPath] = File{Path: logicalPath, Role: role, Data: data}
	return nil
}

func (w *walker) issue(code, logicalPath string) {
	issue := Issue{Code: code, Path: logicalPath}
	if _, exists := w.seenIssue[issue]; exists {
		return
	}
	w.seenIssue[issue] = struct{}{}
	w.tree.Issues = append(w.tree.Issues, issue)
}

func validContentName(name string) bool {
	if name == "" || !utf8.ValidString(name) || !unicode17.IsNFC(name) || strings.HasPrefix(name, ".") {
		return false
	}
	for _, r := range name {
		if r <= 0x1f || r >= 0x7f && r <= 0x9f || r == 0x2028 || r == 0x2029 || strings.ContainsRune("[]|# ()/\\<>?%:&\"*", r) {
			return false
		}
	}
	return true
}

func validTypeSlug(value string) bool {
	if len(value) < 1 || len(value) > 64 || value[0] == '-' || value[len(value)-1] == '-' || strings.Contains(value, "--") {
		return false
	}
	for index := 0; index < len(value); index++ {
		character := value[index]
		if character != '-' && (character < 'a' || character > 'z') && (character < '0' || character > '9') {
			return false
		}
	}
	return true
}

func validSchemaFilename(name string) bool {
	return strings.HasSuffix(name, ".md") && validTypeSlug(strings.TrimSuffix(name, ".md"))
}

func validHookFilename(name string) bool {
	if len(name) < 4 || name[0] < '0' || name[0] > '9' || name[1] < '0' || name[1] > '9' || name[2] != '-' {
		return false
	}
	parts := strings.Split(name[3:], ".")
	if !validTypeSlug(parts[0]) {
		return false
	}
	for _, extension := range parts[1:] {
		if extension == "" {
			return false
		}
		for index := 0; index < len(extension); index++ {
			character := extension[index]
			if (character < 'a' || character > 'z') && (character < '0' || character > '9') {
				return false
			}
		}
	}
	return true
}

func join(directory, name string) string {
	if directory == "." {
		return name
	}
	return path.Join(directory, name)
}

// ValidContentName exposes the exact mandatory §2.6 name check for link and
// writer code. README.md is not implicitly exempt here.
func ValidContentName(name string) bool { return validContentName(name) }

// ValidTypeSlug exposes the closed v1 type-name grammar.
func ValidTypeSlug(value string) bool { return validTypeSlug(value) }
