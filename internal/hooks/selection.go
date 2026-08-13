// Package hooks selects and authorizes complete preparation-hook sets. It
// deliberately does not launch hook programs; execution belongs to the
// managed-transaction layer.
package hooks

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/ontopix/engram/internal/changeset"
	"github.com/ontopix/engram/internal/documentprofile"
	"github.com/ontopix/engram/internal/snapshot"
)

const programDirectory = ".engram/hooks/prepare-changeset"

// ErrInvalidSelection identifies a base hook tree which cannot produce a
// complete applicable set.
var ErrInvalidSelection = errors.New("invalid preparation-hook selection")

// SelectionError attributes a selection failure to the normative finding
// which prevents use of the base hook tree.
type SelectionError struct {
	Code   string
	Path   string
	Detail string
}

func (e *SelectionError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Detail == "" {
		return fmt.Sprintf("%s: %s at %s", ErrInvalidSelection, e.Code, e.Path)
	}
	return fmt.Sprintf("%s: %s at %s: %s", ErrInvalidSelection, e.Code, e.Path, e.Detail)
}

func (e *SelectionError) Unwrap() error { return ErrInvalidSelection }

// Hook is one selected program. Bytes are the exact immutable base-state
// bytes and are excluded from JSON; Path, Interpreter, and SHA256 form the
// public hook description from CLI protocol v1.
type Hook struct {
	Path        string `json:"path"`
	Interpreter string `json:"interpreter"`
	SHA256      string `json:"sha256"`
	Bytes       []byte `json:"-"`
}

// Set is the complete ordered base-state hook set. SHA256 is over the stable
// identity serialization returned by CanonicalBytes.
type Set struct {
	SHA256 string `json:"sha256"`
	Hooks  []Hook `json:"hooks"`
}

type setIdentity struct {
	Version int               `json:"version"`
	Hooks   []hookSetIdentity `json:"hooks"`
}

type hookSetIdentity struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

// EmptySet returns the initialization set. It runs nothing and is trusted
// without a code-execution grant, while callers must still resolve a valid
// physical store binding before using trust operations.
func EmptySet() Set {
	set, err := buildSet(nil)
	if err != nil {
		panic(err)
	}
	return set
}

// SelectSource loads one already-bounded base source and selects its complete
// preparation-hook set. No program is executed.
func SelectSource(source snapshot.Source) (Set, error) {
	tree, err := snapshot.Load(source)
	if err != nil {
		return Set{}, err
	}
	return SelectTree(tree)
}

// SelectTree selects the complete hierarchical preparation-hook inventory,
// validates every program's normed bytes and interpreter line, and orders it
// by two-digit band then complete logical path. Hook-layout findings make the
// entire selection fail; no partial set is returned.
func SelectTree(tree *snapshot.Tree) (Set, error) {
	return selectTree(tree, nil)
}

// SelectTreeForChanges validates the complete hierarchical hook inventory and
// selects only base hooks whose scope contains at least one initial change.
// A non-nil empty changeset therefore selects the empty set.
func SelectTreeForChanges(tree *snapshot.Tree, initial []changeset.Change) (Set, error) {
	if initial == nil {
		initial = []changeset.Change{}
	}
	return selectTree(tree, initial)
}

func selectTree(tree *snapshot.Tree, initial []changeset.Change) (Set, error) {
	if tree == nil {
		return Set{}, fmt.Errorf("%w: snapshot tree is nil", ErrInvalidSelection)
	}
	relevantIssues := make([]snapshot.Issue, 0)
	for _, issue := range tree.Issues {
		if hookPath(issue.Path) {
			relevantIssues = append(relevantIssues, issue)
		}
	}
	sort.Slice(relevantIssues, func(i, j int) bool {
		if relevantIssues[i].Path != relevantIssues[j].Path {
			return bytes.Compare([]byte(relevantIssues[i].Path), []byte(relevantIssues[j].Path)) < 0
		}
		return relevantIssues[i].Code < relevantIssues[j].Code
	})
	if len(relevantIssues) != 0 {
		issue := relevantIssues[0]
		return Set{}, &SelectionError{Code: issue.Code, Path: issue.Path, Detail: "invalid base hook tree"}
	}

	programs := make([]Hook, 0)
	logicalPaths := make([]string, 0, len(tree.Files))
	for logicalPath := range tree.Files {
		logicalPaths = append(logicalPaths, logicalPath)
	}
	sort.Slice(logicalPaths, func(i, j int) bool {
		return bytes.Compare([]byte(logicalPaths[i]), []byte(logicalPaths[j])) < 0
	})
	for _, logicalPath := range logicalPaths {
		file := tree.Files[logicalPath]
		if file.Role != snapshot.RoleHook {
			if strings.HasPrefix(logicalPath, programDirectory+"/") {
				return Set{}, &SelectionError{Code: "E308", Path: logicalPath, Detail: "non-hook file in hook program directory"}
			}
			continue
		}
		scope, ok := programScope(logicalPath)
		if !ok || !validFilename(path.Base(logicalPath)) {
			return Set{}, &SelectionError{Code: "E308", Path: logicalPath, Detail: "hook is not an admitted direct program"}
		}
		if err := documentprofile.ValidateText(file.Data); err != nil {
			return Set{}, &SelectionError{Code: "E108", Path: logicalPath, Detail: err.Error()}
		}
		interpreter, ok := interpreter(file.Data)
		if !ok {
			return Set{}, &SelectionError{Code: "E308", Path: logicalPath, Detail: "invalid preparation-hook interpreter line"}
		}
		digest := sha256.Sum256(file.Data)
		if initial != nil && !scopeAffected(scope, initial) {
			continue
		}
		programs = append(programs, Hook{
			Path:        logicalPath,
			Interpreter: interpreter,
			SHA256:      hex.EncodeToString(digest[:]),
			Bytes:       append([]byte(nil), file.Data...),
		})
	}
	sort.Slice(programs, func(i, j int) bool {
		leftName, rightName := path.Base(programs[i].Path), path.Base(programs[j].Path)
		if leftName[:2] != rightName[:2] {
			return leftName[:2] < rightName[:2]
		}
		return bytes.Compare([]byte(programs[i].Path), []byte(programs[j].Path)) < 0
	})
	return buildSet(programs)
}

// CanonicalBytes returns the stable set-identity serialization. It contains
// the ordered relative paths and exact program digests; interpreter identity
// is already fixed by those exact bytes.
func (s Set) CanonicalBytes() ([]byte, error) {
	if err := validateHooks(s.Hooks); err != nil {
		return nil, err
	}
	return canonicalDescriptions(s.Hooks)
}

func canonicalDescriptions(hooks []Hook) ([]byte, error) {
	identity := setIdentity{Version: 1, Hooks: make([]hookSetIdentity, len(hooks))}
	for index, hook := range hooks {
		identity.Hooks[index] = hookSetIdentity{Path: hook.Path, SHA256: hook.SHA256}
	}
	return json.Marshal(identity)
}

// Valid reports whether paths, bytes, descriptions, ordering, and set digest
// are internally consistent.
func (s Set) Valid() error {
	canonical, err := s.CanonicalBytes()
	if err != nil {
		return err
	}
	digest := sha256.Sum256(canonical)
	if s.SHA256 != hex.EncodeToString(digest[:]) {
		return fmt.Errorf("%w: inconsistent hook-set digest", ErrInvalidSelection)
	}
	return nil
}

func buildSet(programs []Hook) (Set, error) {
	result := Set{Hooks: cloneHooks(programs)}
	canonical, err := result.CanonicalBytes()
	if err != nil {
		return Set{}, err
	}
	digest := sha256.Sum256(canonical)
	result.SHA256 = hex.EncodeToString(digest[:])
	return result, nil
}

func validateHooks(programs []Hook) error {
	for index, hook := range programs {
		if _, ok := programScope(hook.Path); !ok || !validFilename(path.Base(hook.Path)) {
			return fmt.Errorf("%w: invalid hook path %q", ErrInvalidSelection, hook.Path)
		}
		if index != 0 && !hookLess(programs[index-1], hook) {
			return fmt.Errorf("%w: hooks are not in strict ASCII order", ErrInvalidSelection)
		}
		if err := documentprofile.ValidateText(hook.Bytes); err != nil {
			return fmt.Errorf("%w: %s: %v", ErrInvalidSelection, hook.Path, err)
		}
		selectedInterpreter, ok := interpreter(hook.Bytes)
		if !ok || selectedInterpreter != hook.Interpreter {
			return fmt.Errorf("%w: inconsistent interpreter for %s", ErrInvalidSelection, hook.Path)
		}
		digest := sha256.Sum256(hook.Bytes)
		if hook.SHA256 != hex.EncodeToString(digest[:]) {
			return fmt.Errorf("%w: inconsistent program digest for %s", ErrInvalidSelection, hook.Path)
		}
	}
	return nil
}

func hookPath(logicalPath string) bool {
	return logicalPath == ".engram/hooks" || strings.HasPrefix(logicalPath, ".engram/hooks/") ||
		strings.Contains(logicalPath, "/.engram/hooks")
}

func programScope(logicalPath string) (string, bool) {
	if logicalPath == "" || path.Clean(logicalPath) != logicalPath || strings.HasPrefix(logicalPath, "/") || strings.Contains(logicalPath, "\\") {
		return "", false
	}
	directory := path.Dir(logicalPath)
	if directory == programDirectory {
		return ".", true
	}
	const suffix = "/.engram/hooks/prepare-changeset"
	if !strings.HasSuffix(directory, suffix) {
		return "", false
	}
	scope := strings.TrimSuffix(directory, suffix)
	if scope == "" || path.Clean(scope) != scope || strings.HasPrefix(scope, "/") {
		return "", false
	}
	for _, segment := range strings.Split(scope, "/") {
		if !snapshot.ValidContentName(segment) || segment == "README.md" {
			return "", false
		}
	}
	return scope, true
}

func scopeAffected(scope string, initial []changeset.Change) bool {
	if scope == "." {
		return len(initial) != 0
	}
	prefix := scope + "/"
	for _, change := range initial {
		if strings.HasPrefix(change.Path, prefix) {
			return true
		}
	}
	return false
}

func hookLess(left, right Hook) bool {
	leftName, rightName := path.Base(left.Path), path.Base(right.Path)
	if leftName[:2] != rightName[:2] {
		return leftName[:2] < rightName[:2]
	}
	return bytes.Compare([]byte(left.Path), []byte(right.Path)) < 0
}

func interpreter(data []byte) (string, bool) {
	lineEnd := bytes.IndexByte(data, '\n')
	if lineEnd < 0 {
		return "", false
	}
	const prefix = "#!/usr/bin/env "
	line := string(data[:lineEnd])
	if !strings.HasPrefix(line, prefix) || len(line) == len(prefix) {
		return "", false
	}
	token := line[len(prefix):]
	for index := 0; index < len(token); index++ {
		character := token[index]
		if character != '.' && character != '_' && character != '+' && character != '-' &&
			(character < 'a' || character > 'z') && (character < 'A' || character > 'Z') &&
			(character < '0' || character > '9') {
			return "", false
		}
	}
	return token, true
}

func validFilename(name string) bool {
	if len(name) < 4 || name[0] < '0' || name[0] > '9' || name[1] < '0' || name[1] > '9' || name[2] != '-' {
		return false
	}
	parts := strings.Split(name[3:], ".")
	if !validSlug(parts[0]) {
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

func validSlug(value string) bool {
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

func validDigest(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && hex.EncodeToString(decoded) == value
}

func cloneHooks(source []Hook) []Hook {
	if len(source) == 0 {
		return []Hook{}
	}
	result := make([]Hook, len(source))
	for index, hook := range source {
		result[index] = hook
		result[index].Bytes = append([]byte(nil), hook.Bytes...)
	}
	return result
}
