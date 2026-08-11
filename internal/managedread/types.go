// Package managedread composes raw Git projections with the portable checker
// for read-only inspection of Git-managed engram stores.
package managedread

import (
	"errors"
	"fmt"
	"strings"

	"github.com/ontopix/engram/internal/checker"
	"github.com/ontopix/engram/internal/gitraw"
)

// GitState is the protocol-facing accepted/ref state. Either member can be
// nil when that part of the state does not exist (for example an unborn
// accepted branch has a ref and no commit).
type GitState struct {
	Ref    *string `json:"ref"`
	Commit *string `json:"commit"`
}

type SelectorKind string

const (
	SelectorAccepted SelectorKind = "accepted"
	SelectorIndex    SelectorKind = "index"
	SelectorWorking  SelectorKind = "working"
	SelectorRevision SelectorKind = "revision"
)

// StateSelector is the closed selector shape used by diff results. Value is
// non-nil only for a resolved revision and is always its full object ID.
type StateSelector struct {
	Kind  SelectorKind `json:"kind"`
	Value *string      `json:"value"`
}

func AcceptedSelector() StateSelector { return StateSelector{Kind: SelectorAccepted} }
func IndexSelector() StateSelector    { return StateSelector{Kind: SelectorIndex} }
func WorkingSelector() StateSelector  { return StateSelector{Kind: SelectorWorking} }

// RevisionSelector constructs a revision request. Diff resolves HEAD and
// verifies full object IDs against the current accepted lineage before use.
func RevisionSelector(value string) StateSelector {
	return StateSelector{Kind: SelectorRevision, Value: stringPointer(value)}
}

// SnapshotView is a projected state together with its complete portable
// analysis. Index is populated only for the staged/index projection.
type SnapshotView struct {
	State    GitState
	Snapshot *checker.Snapshot
	Index    []IndexEntry
	Modes    map[string]gitraw.TreeMode

	oid    *gitraw.OID
	commit *gitraw.Commit
	// unprojected records exact source paths that did not survive as logical
	// regular files. It closes changeset preflight for raw/index entries that
	// the portable walker intentionally prunes without necessarily emitting a
	// core boundary issue.
	unprojected []string
}

// IndexEntry is one exact resolved entry reported by the real Git index.
// Object is a full lowercase object ID in the repository's object format.
type IndexEntry struct {
	Path         string          `json:"path"`
	Mode         gitraw.TreeMode `json:"mode"`
	Object       string          `json:"object"`
	Stage        uint8           `json:"stage"`
	IntentToAdd  bool            `json:"intent_to_add"`
	SkipWorktree bool            `json:"skip_worktree"`

	oid   gitraw.OID
	flags uint32
}

type IndexFailure string

const (
	IndexMalformed IndexFailure = "malformed"
	IndexConflict  IndexFailure = "conflict"
	IndexMode      IndexFailure = "mode"
)

// IndexError means the index cannot identify one complete initial candidate.
// Paths are deduplicated and sorted by UTF-8 bytes.
type IndexError struct {
	Kind   IndexFailure
	Paths  []string
	Detail string
	Err    error
}

func (e *IndexError) Error() string {
	if e == nil {
		return ""
	}
	message := "index " + string(e.Kind)
	if len(e.Paths) != 0 {
		message += " at " + strings.Join(e.Paths, ", ")
	}
	if e.Detail != "" {
		message += ": " + e.Detail
	}
	if e.Err != nil {
		message += ": " + e.Err.Error()
	}
	return message
}

func (e *IndexError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// BoundaryError prevents status/diff from publishing a partial logical
// changeset when a selected projection fails core §8.1 preflight.
type BoundaryError struct {
	Selector StateSelector
	Issues   []string
}

func (e *BoundaryError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("%s projection fails changeset boundary preflight: %s", e.Selector.Kind, strings.Join(e.Issues, ", "))
}

// RevisionError reports a syntactically invalid or unaudited diff revision.
type RevisionError struct {
	Value  string
	Detail string
}

// ErrConcurrent is wrapped whenever a managed read cannot prove that every
// input used for one result remained unchanged through its final observation.
var ErrConcurrent = errors.New("managed read inputs changed concurrently")

// ConcurrencyError identifies the inputs that changed during one read-only
// operation. Inputs are deduplicated and sorted by UTF-8 bytes.
type ConcurrencyError struct {
	Operation string
	Inputs    []string
	Err       error
}

func (e *ConcurrencyError) Error() string {
	if e == nil {
		return ""
	}
	message := "managed read changed concurrently"
	if e.Operation != "" {
		message = e.Operation + ": " + message
	}
	if len(e.Inputs) != 0 {
		message += ": " + strings.Join(e.Inputs, ", ")
	}
	if e.Err != nil {
		message += ": " + e.Err.Error()
	}
	return message
}

func (e *ConcurrencyError) Unwrap() error {
	if e == nil {
		return nil
	}
	if e.Err == nil {
		return ErrConcurrent
	}
	return errors.Join(ErrConcurrent, e.Err)
}

func (e *RevisionError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("invalid revision %q: %s", e.Value, e.Detail)
}

func stringPointer(value string) *string {
	copy := value
	return &copy
}
