// Package draft plans and safely publishes portable working-draft helpers.
//
// Planning is read-only and captures every filesystem input used by a plan.
// Publication rechecks those exact captures, writes through temporary regular
// files, and rolls back already-published files if a later write fails. The
// package deliberately does not stage files, move accepted refs, or know how
// a managed Git worktree rendezvous is implemented; callers can provide that
// rendezvous through the Locker interface.
package draft

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/ontopix/engram/internal/checker"
)

// ErrorKind is a stable, protocol-ready failure class.
type ErrorKind string

const (
	ErrorUsage       ErrorKind = "usage"
	ErrorCancelled   ErrorKind = "cancelled"
	ErrorInternal    ErrorKind = "internal"
	ErrorCapability  ErrorKind = "capability"
	ErrorConflict    ErrorKind = "conflict"
	ErrorConcurrency ErrorKind = "concurrency"
	ErrorRepository  ErrorKind = "repository"
	ErrorIO          ErrorKind = "io"
)

var (
	// ErrConcurrent is wrapped when an input captured by a plan changed before
	// publication or a cooperating writer already owns the rendezvous.
	ErrConcurrent = errors.New("draft input changed concurrently")
	// ErrRecoveryRequired is wrapped when rollback could not prove and restore
	// every already-published preimage. Callers must not report success.
	ErrRecoveryRequired = errors.New("draft recovery required")
)

// Error carries a stable class while leaving its human text non-normative.
type Error struct {
	Kind      ErrorKind
	Operation string
	Path      string
	Err       error
	Mutation  *Mutation
}

// Mutation is the closed local effect set known after a draft publication
// error. Draft helpers never update refs, HEAD, or remotes.
type Mutation struct {
	Durable          bool
	CheckoutChanged  bool
	RecoveryRequired bool
}

func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	prefix := string(e.Kind)
	if e.Operation != "" {
		prefix += ": " + e.Operation
	}
	if e.Path != "" {
		prefix += " " + fmt.Sprintf("%q", e.Path)
	}
	if e.Err != nil {
		prefix += ": " + e.Err.Error()
	}
	return prefix
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// KindOf returns the stable class carried by err, or the empty string for an
// unclassified error.
func KindOf(err error) ErrorKind {
	var typed *Error
	if errors.As(err, &typed) {
		return typed.Kind
	}
	return ""
}

// MutationOf merges effect evidence across joined or wrapped draft errors.
// RecoveryRequired is the final snapshot: an outer mutation overrides its
// causes and the last evidence-bearing joined error overrides earlier ones.
func MutationOf(err error) (Mutation, bool) {
	var visit func(error) (Mutation, bool)
	visit = func(current error) (Mutation, bool) {
		if current == nil {
			return Mutation{}, false
		}
		if typedError, ok := current.(*Error); ok && typedError.Mutation != nil {
			result := *typedError.Mutation
			if nested, present := visit(typedError.Err); present {
				result.Durable = result.Durable || nested.Durable
				result.CheckoutChanged = result.CheckoutChanged || nested.CheckoutChanged
			}
			return result, true
		}
		switch unwrapped := current.(type) {
		case interface{ Unwrap() []error }:
			result := Mutation{}
			present := false
			for _, child := range unwrapped.Unwrap() {
				childMutation, childPresent := visit(child)
				if !childPresent {
					continue
				}
				result.Durable = result.Durable || childMutation.Durable
				result.CheckoutChanged = result.CheckoutChanged || childMutation.CheckoutChanged
				result.RecoveryRequired = childMutation.RecoveryRequired
				present = true
			}
			return result, present
		case interface{ Unwrap() error }:
			return visit(unwrapped.Unwrap())
		default:
			return Mutation{}, false
		}
	}
	return visit(err)
}

// Unlock releases a caller-owned worktree rendezvous.
type Unlock func() error

// Locker coordinates publication with other managed worktree writers. The
// portable draft package performs exact compare-and-swap-style rechecks even
// when no Locker is supplied, but a managed CLI should always supply its
// annex-defined worktree rendezvous.
type Locker interface {
	LockDraft(ctx context.Context, root string) (Unlock, error)
}

// LockerFunc adapts a function to Locker.
type LockerFunc func(ctx context.Context, root string) (Unlock, error)

func (f LockerFunc) LockDraft(ctx context.Context, root string) (Unlock, error) {
	if f == nil {
		return nil, errors.New("draft locker function is nil")
	}
	return f(ctx, root)
}

// FileChange is an immutable copy of one planned logical file update. Before
// is nil when the path must be absent. After is nil for a deletion. Create and
// Delete are mutually exclusive.
type FileChange struct {
	Path   string
	Before []byte
	After  []byte
	Create bool
	Delete bool
}

// FmtOptions controls literal README selection and read-only modes. With no
// Paths, every logical content-directory README is selected.
type FmtOptions struct {
	Paths      []string
	Check      bool
	DryRun     bool
	Rendezvous Locker
}

type FmtResult struct {
	DryRun  bool     `json:"dry_run"`
	Check   bool     `json:"check"`
	Changed bool     `json:"changed"`
	Paths   []string `json:"paths"`
}

// NewOptions contains already-read option inputs. BodyProvided distinguishes
// an explicit body from generated-body mode; a non-nil Body also counts as
// provided. Fields is nil when --fields was absent.
type NewOptions struct {
	Description   string
	Fields        []byte
	Body          []byte
	BodyProvided  bool
	Title         string
	TitleProvided bool
	DryRun        bool
	Rendezvous    Locker
}

type NewResult struct {
	DryRun   bool     `json:"dry_run"`
	Changed  bool     `json:"changed"`
	Record   string   `json:"record"`
	Catalogs []string `json:"catalogs"`
}

// MoveOptions controls publication of one literal record move.
type MoveOptions struct {
	DryRun     bool
	Rendezvous Locker
}

// MoveResult is the stable result shape of mv. Paths contains final logical
// paths of documents whose non-catalog link bytes changed; Catalogs contains
// README paths whose generated regions changed.
type MoveResult struct {
	DryRun   bool     `json:"dry_run"`
	Changed  bool     `json:"changed"`
	From     string   `json:"from"`
	To       string   `json:"to"`
	Paths    []string `json:"paths"`
	Catalogs []string `json:"catalogs"`
}

// SchemaCopyOptions selects an existing logical content-directory scope.
// The empty Scope denotes the store root, matching the CLI default.
type SchemaCopyOptions struct {
	Scope         string
	ScopeProvided bool
	DryRun        bool
	Rendezvous    Locker
}

type SchemaCopyResult struct {
	DryRun  bool                      `json:"dry_run"`
	Changed bool                      `json:"changed"`
	Schema  checker.SchemaDescription `json:"schema"`
	Path    string                    `json:"path"`
}

func inventoryDescription(typeName, description string, version int64) checker.SchemaDescription {
	return checker.SchemaDescription{
		Type: typeName, Source: "inventory", Path: nil,
		Version: json.Number(fmt.Sprintf("%d", version)), Description: description,
	}
}

func typed(kind ErrorKind, operation, logicalPath string, err error) error {
	if err == nil {
		err = errors.New("unknown failure")
	}
	return &Error{Kind: kind, Operation: operation, Path: logicalPath, Err: err}
}

func mutationError(kind ErrorKind, operation string, err error, mutation Mutation) error {
	if err == nil {
		err = errors.New("unknown mutation failure")
	}
	return &Error{Kind: kind, Operation: operation, Err: err, Mutation: &mutation}
}

func cancelled(ctx context.Context, operation string) error {
	if ctx == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return typed(ErrorCancelled, operation, "", ctx.Err())
	default:
		return nil
	}
}
