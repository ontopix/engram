// Package changeset constructs deterministic logical net differences between
// portable snapshots.
package changeset

import (
	"bytes"
	"sort"

	"github.com/ontopix/engram/internal/snapshot"
)

type Operation string

const (
	Added    Operation = "added"
	Modified Operation = "modified"
	Deleted  Operation = "deleted"
)

type Change struct {
	Operation Operation `json:"operation"`
	Path      string    `json:"path"`
}

// Diff returns one normalized entry for every regular logical file whose
// presence or bytes differ. Pruned and derived state is absent from Tree and
// therefore absent from the changeset by construction.
func Diff(base, candidate *snapshot.Tree) []Change {
	baseFiles := map[string]snapshot.File{}
	candidateFiles := map[string]snapshot.File{}
	if base != nil {
		baseFiles = base.Files
	}
	if candidate != nil {
		candidateFiles = candidate.Files
	}
	paths := make(map[string]struct{}, len(baseFiles)+len(candidateFiles))
	for name := range baseFiles {
		paths[name] = struct{}{}
	}
	for name := range candidateFiles {
		paths[name] = struct{}{}
	}
	ordered := make([]string, 0, len(paths))
	for name := range paths {
		ordered = append(ordered, name)
	}
	sort.Slice(ordered, func(i, j int) bool {
		return bytes.Compare([]byte(ordered[i]), []byte(ordered[j])) < 0
	})

	result := make([]Change, 0, len(ordered))
	for _, name := range ordered {
		baseFile, inBase := baseFiles[name]
		candidateFile, inCandidate := candidateFiles[name]
		switch {
		case !inBase && inCandidate:
			result = append(result, Change{Operation: Added, Path: name})
		case inBase && !inCandidate:
			result = append(result, Change{Operation: Deleted, Path: name})
		case !bytes.Equal(baseFile.Data, candidateFile.Data):
			result = append(result, Change{Operation: Modified, Path: name})
		}
	}
	return result
}

// PreflightOK reports whether traversal produced no boundary/layout finding
// that forbids changeset serialization under core §8.1.
func PreflightOK(tree *snapshot.Tree) bool {
	if tree == nil {
		return false
	}
	for _, issue := range tree.Issues {
		switch issue.Code {
		case "E102", "E103", "E104", "E106", "E107", "E109", "E110", "E303", "E308":
			return false
		}
	}
	return true
}
