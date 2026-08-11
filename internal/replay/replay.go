// Package replay implements the byte-exact, merge-free divergent replay rule
// from the normative Git annex. It is deliberately independent of Git refs,
// worktrees, transactions, and hooks.
package replay

import (
	"bytes"
	"sort"
	"strings"

	"github.com/ontopix/engram/internal/changeset"
)

// Files is an absent-or-present map of logical regular-file bytes. Callers
// retain ownership of their input slices; all returned bytes are independent.
type Files map[string][]byte

// Result describes one all-at-once source changeset application. When
// Conflicts is non-empty, Files is an unchanged copy of the supplied current
// state and no source result has been applied.
type Result struct {
	Files     Files
	Changes   []changeset.Change
	Conflicts []string
	Satisfied bool
}

// Apply applies the original->next source transition to current exactly as
// specified by annex-git §7.2. All paths are evaluated against the same
// unchanged current state before any result is published.
func Apply(original, next, current Files) Result {
	original = clone(original)
	next = clone(next)
	current = clone(current)
	sourceChanges := diff(original, next)
	tentative := clone(current)
	conflicts := make(map[string]struct{})

	for _, change := range sourceChanges {
		oldBytes, oldPresent := original[change.Path]
		newBytes, newPresent := next[change.Path]
		currentBytes, currentPresent := current[change.Path]
		switch change.Operation {
		case changeset.Added:
			switch {
			case !currentPresent:
				tentative[change.Path] = cloneBytes(newBytes)
			case bytes.Equal(currentBytes, newBytes):
				// Already satisfied.
			default:
				conflicts[change.Path] = struct{}{}
			}
		case changeset.Modified:
			switch {
			case currentPresent && bytes.Equal(currentBytes, oldBytes):
				tentative[change.Path] = cloneBytes(newBytes)
			case currentPresent && bytes.Equal(currentBytes, newBytes):
				// Already satisfied.
			default:
				conflicts[change.Path] = struct{}{}
			}
		case changeset.Deleted:
			switch {
			case currentPresent && bytes.Equal(currentBytes, oldBytes):
				delete(tentative, change.Path)
			case !currentPresent:
				// Already satisfied.
			default:
				conflicts[change.Path] = struct{}{}
			}
		}
		_ = oldPresent
		_ = newPresent
	}

	for _, pair := range prefixCollisions(tentative) {
		conflicts[pair[0]] = struct{}{}
		conflicts[pair[1]] = struct{}{}
	}
	if len(conflicts) != 0 {
		return Result{Files: current, Conflicts: sortedSet(conflicts)}
	}
	resultChanges := diff(current, tentative)
	return Result{
		Files:     tentative,
		Changes:   resultChanges,
		Conflicts: []string{},
		Satisfied: len(resultChanges) == 0,
	}
}

func diff(base, candidate Files) []changeset.Change {
	paths := make(map[string]struct{}, len(base)+len(candidate))
	for name := range base {
		paths[name] = struct{}{}
	}
	for name := range candidate {
		paths[name] = struct{}{}
	}
	ordered := sortedSet(paths)
	result := make([]changeset.Change, 0, len(ordered))
	for _, name := range ordered {
		baseBytes, inBase := base[name]
		candidateBytes, inCandidate := candidate[name]
		switch {
		case !inBase && inCandidate:
			result = append(result, changeset.Change{Operation: changeset.Added, Path: name})
		case inBase && !inCandidate:
			result = append(result, changeset.Change{Operation: changeset.Deleted, Path: name})
		case !bytes.Equal(baseBytes, candidateBytes):
			result = append(result, changeset.Change{Operation: changeset.Modified, Path: name})
		}
	}
	return result
}

func prefixCollisions(files Files) [][2]string {
	paths := make([]string, 0, len(files))
	for name := range files {
		paths = append(paths, name)
	}
	sort.Strings(paths)
	var result [][2]string
	for index, name := range paths {
		prefix := name + "/"
		for other := index + 1; other < len(paths) && strings.HasPrefix(paths[other], prefix); other++ {
			result = append(result, [2]string{name, paths[other]})
		}
	}
	return result
}

func clone(source Files) Files {
	result := make(Files, len(source))
	for name, data := range source {
		result[name] = cloneBytes(data)
	}
	return result
}

func cloneBytes(source []byte) []byte {
	return append([]byte(nil), source...)
}

func sortedSet[T ~string](set map[T]struct{}) []string {
	result := make([]string, 0, len(set))
	for item := range set {
		result = append(result, string(item))
	}
	sort.Slice(result, func(i, j int) bool {
		return bytes.Compare([]byte(result[i]), []byte(result[j])) < 0
	})
	return result
}
