// Package syncflow implements the network-facing synchronization primitives
// used by command adapters. It owns no CLI protocol policy and never mutates a
// local accepted ref, index, or worktree.
package syncflow

import (
	"errors"
	"fmt"

	"github.com/ontopix/engram/internal/checker"
	"github.com/ontopix/engram/internal/managedread"
)

// PushState is the closed set of complete push outcomes from CLI contract
// section 10.2. Operational failures are represented by Error instead.
type PushState string

const (
	PushUpToDate      PushState = "up-to-date"
	PushPushed        PushState = "pushed"
	PushRejected      PushState = "rejected"
	PushIndeterminate PushState = "indeterminate"
)

// PushResult is the complete, protocol-ready result of one push attempt.
// Changed is nil only when the remote update outcome cannot be established.
// Before is nil only when the selected remote branch was observed absent or
// when RemoteObserved is false because local validation rejected publication.
type PushResult struct {
	State          PushState                  `json:"state"`
	Remote         string                     `json:"remote"`
	RemoteRef      string                     `json:"remote_ref"`
	RemoteObserved bool                       `json:"remote_observed"`
	Before         *string                    `json:"before"`
	After          string                     `json:"after"`
	Commits        int                        `json:"commits"`
	Changed        *bool                      `json:"changed"`
	Validation     checker.Result             `json:"validation"`
	Audits         []managedread.HistoryAudit `json:"audits"`
}

// ErrorKind is a stable failure class for command adapters. A rejected local
// lineage or explicit remote policy rejection is a PushRejected result, not an
// Error. An uncertain post-dispatch outcome is a PushIndeterminate result.
type ErrorKind string

const (
	ErrorUsage       ErrorKind = "usage"
	ErrorCancelled   ErrorKind = "cancelled"
	ErrorRepository  ErrorKind = "repository"
	ErrorCapability  ErrorKind = "capability"
	ErrorConcurrency ErrorKind = "concurrency"
	ErrorNetwork     ErrorKind = "network"
)

var (
	// ErrCASRace identifies a conditional remote-ref update whose observed old
	// object ID was no longer current when the server evaluated the update.
	ErrCASRace = errors.New("remote ref changed after observation")
	// ErrNetwork identifies a transport or credential failure before any
	// remote update was dispatched, most commonly exact-ref observation.
	ErrNetwork = errors.New("remote network operation failed")
)

// Error carries a stable class while retaining the underlying diagnostic.
type Error struct {
	Kind      ErrorKind
	Operation string
	Err       error
}

func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	message := string(e.Kind)
	if e.Operation != "" {
		message += ": " + e.Operation
	}
	if e.Err != nil {
		message += ": " + e.Err.Error()
	}
	return message
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// KindOf returns the synchronization failure class, or the empty string for
// an error not produced by this package.
func KindOf(err error) ErrorKind {
	var typed *Error
	if errors.As(err, &typed) {
		return typed.Kind
	}
	return ""
}

func typed(kind ErrorKind, operation string, err error) error {
	if err == nil {
		err = errors.New("unknown synchronization failure")
	}
	return &Error{Kind: kind, Operation: operation, Err: err}
}

func casFailure(detail string) error {
	if detail == "" {
		return typed(ErrorConcurrency, "conditionally publish remote ref", ErrCASRace)
	}
	return typed(ErrorConcurrency, "conditionally publish remote ref", fmt.Errorf("%w: %s", ErrCASRace, detail))
}
