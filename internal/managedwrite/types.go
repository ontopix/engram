// Package managedwrite implements the Git annex managed acceptance and
// recovery transaction. It owns the accepted-ref compare-and-swap and the
// bounded index/worktree reconciliation; callers never receive a mutable
// transaction handle.
package managedwrite

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ontopix/engram/internal/changeset"
	"github.com/ontopix/engram/internal/checker"
	"github.com/ontopix/engram/internal/gitraw"
	"github.com/ontopix/engram/internal/hookexec"
	"github.com/ontopix/engram/internal/rendezvous"
)

// Phase identifies a durable or fault-injection boundary. A Fault callback is
// invoked immediately after the named boundary has completed.
type Phase string

const (
	PhaseCaptured           Phase = "captured"
	PhaseLocked             Phase = "locked"
	PhaseAudited            Phase = "audited"
	PhasePrepared           Phase = "prepared"
	PhaseProven             Phase = "proven"
	PhaseObjectsWritten     Phase = "objects-written"
	PhaseJournalPending     Phase = "journal-pending"
	PhaseJournalRequired    Phase = "journal-required"
	PhaseFinalRecheck       Phase = "final-recheck"
	PhaseRefUpdated         Phase = "ref-updated"
	PhaseIndexReconciled    Phase = "index-reconciled"
	PhaseWorktreeReconciled Phase = "worktree-reconciled"
	PhaseJournalComplete    Phase = "journal-complete"
	PhaseLocksReleased      Phase = "locks-released"
	PhaseJournalRemoved     Phase = "journal-removed"
)

type FailureKind string

const (
	FailureUsage       FailureKind = "usage"
	FailureRepository  FailureKind = "repository"
	FailureCapability  FailureKind = "capability"
	FailureValidation  FailureKind = "validation"
	FailureTrust       FailureKind = "trust"
	FailureHook        FailureKind = "hook"
	FailureGuard       FailureKind = "guard"
	FailureConcurrency FailureKind = "concurrency"
	FailureRecovery    FailureKind = "recovery-conflict"
	FailureIO          FailureKind = "io"
)

var (
	ErrUsage      = errors.New("invalid managed transaction request")
	ErrRepository = errors.New("managed repository is unavailable")
	ErrCapability = errors.New("managed transaction capability unavailable")
	ErrValidation = errors.New("managed candidate was rejected")
	ErrGuard      = errors.New("managed Git guard is not exactly installed")
	ErrConcurrent = errors.New("managed transaction input changed concurrently")
	ErrRecovery   = errors.New("managed recovery requires explicit resolution")
	ErrPostCAS    = errors.New("accepted ref moved but reconciliation is incomplete")
	ErrCASUnknown = errors.New("accepted-ref update outcome is unknown")
)

// Error is the stable operational failure returned by Commit and Recover.
// Accepted is true only when the engine knows that the requested commit is
// already accepted. UnknownCAS is true only when it cannot safely make either
// claim; both cases deliberately retain the pending journal and locks.
type Error struct {
	Kind       FailureKind
	Phase      Phase
	Accepted   bool
	UnknownCAS bool
	Commit     string
	Paths      []string
	Validation *checker.Result
	Err        error
}

func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	message := string(e.Kind)
	if e.Phase != "" {
		message += " at " + string(e.Phase)
	}
	if len(e.Paths) != 0 {
		message += ": " + strings.Join(e.Paths, ", ")
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

func KindOf(err error) FailureKind {
	var typed *Error
	if errors.As(err, &typed) {
		return typed.Kind
	}
	return ""
}

// Request accepts the complete eligible live Git index. DryRun performs
// capture, audit, preparation, validation, and preservation proof, but never
// inspects/installs the guard and never creates objects, a journal, locks, or
// live index/worktree/ref changes.
type Request struct {
	Store   string
	Message string
	DryRun  bool
}

// ImageRequest is the integration point for workflows such as init and
// revert that construct a sealed complete candidate without first publishing
// it to the live index. The live index is still captured as the exact
// reconciliation preimage and is not changed on dry-run or rejection.
type ImageRequest struct {
	Store        string
	Message      string
	DryRun       bool
	Candidate    *checker.Snapshot
	Modes        map[string]gitraw.TreeMode
	RequireClean bool
	// RequireBase gives ExpectedBase tri-state semantics: nil requires an
	// unborn accepted ref; non-nil requires that exact full object ID. When
	// false, ExpectedBase is ignored.
	RequireBase  bool
	ExpectedBase *string
}

type Result struct {
	DryRun         bool                  `json:"dry_run"`
	Created        bool                  `json:"created"`
	Commit         *string               `json:"commit"`
	Ref            string                `json:"ref"`
	Base           *string               `json:"base"`
	Initialization bool                  `json:"initialization"`
	Changes        []changeset.Change    `json:"changes"`
	Validation     *checker.Result       `json:"validation"`
	HookSetSHA256  string                `json:"hook_set_sha256,omitempty"`
	Diagnostics    []hookexec.Diagnostic `json:"diagnostics,omitempty"`
}

type RecoveryAction string

const (
	RecoveryNone       RecoveryAction = "none"
	RecoveryCancelled  RecoveryAction = "cancelled-cleaned"
	RecoveryReconciled RecoveryAction = "reconciled"
	RecoveryCompleted  RecoveryAction = "complete-cleaned"
	RecoveryStaleLock  RecoveryAction = "stale-pre-journal-lock-cleaned"
)

type RecoveryResult struct {
	Needed    bool           `json:"needed"`
	Performed bool           `json:"performed"`
	Action    RecoveryAction `json:"action"`
	Accepted  *string        `json:"accepted"`
}

// OwnerLiveness must return (false, nil) only when it has proved that the
// recorded owner no longer controls the attempt. An error means unknown.
type OwnerLiveness func(context.Context, rendezvous.Owner) (bool, error)

// Engine owns injected boundaries. Hooks is mandatory for every non-no-op
// candidate, including the empty hook set, because hook trust binds the
// physical store identity. Fault is a deterministic test seam.
type Engine struct {
	Hooks      *hookexec.Executor
	TempRoot   string
	Clock      func() time.Time
	Fault      func(Phase) error
	OwnerAlive OwnerLiveness
}

var activeOwners sync.Map

func New(hooks *hookexec.Executor) *Engine {
	return &Engine{Hooks: hooks}
}

func (e *Engine) now() time.Time {
	if e != nil && e.Clock != nil {
		return e.Clock()
	}
	return time.Now()
}

func (e *Engine) checkpoint(phase Phase) error {
	if e == nil || e.Fault == nil {
		return nil
	}
	if err := e.Fault(phase); err != nil {
		return fmt.Errorf("fault after %s: %w", phase, err)
	}
	return nil
}

func (e *Engine) markActive(token string, active bool) {
	if token == "" {
		return
	}
	if active {
		activeOwners.Store(token, struct{}{})
	} else {
		activeOwners.Delete(token)
	}
}

func (e *Engine) isActive(token string) bool {
	_, ok := activeOwners.Load(token)
	return ok
}

func typed(kind FailureKind, phase Phase, err error) error {
	if err == nil {
		err = errors.New("unknown managed transaction failure")
	}
	return &Error{Kind: kind, Phase: phase, Err: err}
}

func typedPaths(kind FailureKind, phase Phase, paths []string, err error) error {
	paths = append([]string(nil), paths...)
	sort.Strings(paths)
	return &Error{Kind: kind, Phase: phase, Paths: paths, Err: err}
}
