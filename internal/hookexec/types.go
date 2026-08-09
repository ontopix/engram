// Package hookexec implements the complete core Appendix C preparation
// executor. It selects and authorizes one immutable base hook set, invokes it
// sequentially against disposable trees, and returns only a controller-private
// stable final capture. It does not accept managed writes or update Git.
package hookexec

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ontopix/engram/internal/changeset"
	"github.com/ontopix/engram/internal/checker"
	"github.com/ontopix/engram/internal/gitraw"
	"github.com/ontopix/engram/internal/hooks"
	"github.com/ontopix/engram/internal/treeimage"
)

const (
	DefaultDiagnosticLimit  = 16 << 10
	DefaultHookTimeout      = 30 * time.Second
	DefaultProcessWaitDelay = time.Second
)

// ErrorKind is the stable preparation failure class consumed by command
// adapters without importing a CLI package.
type ErrorKind string

const (
	ErrorTrust       ErrorKind = "trust"
	ErrorHook        ErrorKind = "hook"
	ErrorCapability  ErrorKind = "capability"
	ErrorConcurrency ErrorKind = "concurrency"
)

var (
	ErrUntrusted  = errors.New("preparation-hook set is not trusted")
	ErrRejected   = errors.New("preparation-hook attempt rejected")
	ErrCapability = errors.New("preparation-hook capability unavailable")
	ErrConcurrent = errors.New("preparation tree changed concurrently")
)

// Diagnostic is bounded human-facing process output. It has no normative
// machine meaning and is never interpreted as hook protocol output.
type Diagnostic struct {
	Hook            string `json:"hook"`
	Stdout          string `json:"stdout"`
	Stderr          string `json:"stderr"`
	StdoutTruncated bool   `json:"stdout_truncated"`
	StderrTruncated bool   `json:"stderr_truncated"`
}

// Error carries one stable class, optional bounded process diagnostics, and
// optional final validation when otherwise successful hook output was
// rejected. It never exposes a hook-writable filesystem path as accepted
// state.
type Error struct {
	Kind       ErrorKind
	Hook       string
	Diagnostic *Diagnostic
	Validation *checker.Result
	Err        error
}

func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	message := string(e.Kind)
	if e.Hook != "" {
		message += ": " + e.Hook
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

// KindOf returns the preparation failure class, or the empty string when err
// did not originate from this package.
func KindOf(err error) ErrorKind {
	var typed *Error
	if errors.As(err, &typed) {
		return typed.Kind
	}
	return ""
}

// TrustState is the read-only portion of hooks.Registry used immediately
// before process execution.
type TrustState interface {
	List(store string, set hooks.Set) (hooks.Selection, error)
}

var _ TrustState = (*hooks.Registry)(nil)

// Request is one declared preparation attempt. Base is nil only for
// initialization. Mode maps contain exactly the regular logical paths of
// their corresponding snapshots; every surviving base path keeps its base
// mode and every newly added path is 100644.
type Request struct {
	StoreRoot      string
	WorktreeRoot   string
	Base           *checker.Snapshot
	Initial        *checker.Snapshot
	BaseModes      map[string]gitraw.TreeMode
	InitialModes   map[string]gitraw.TreeMode
	Initialization bool
}

// Result is derived only from the final private capture. Capture and Final do
// not alias caller input or a hook-exposed tree.
type Result struct {
	Capture     treeimage.Image
	Final       *checker.Snapshot
	Modes       map[string]gitraw.TreeMode
	Changes     []changeset.Change
	Validation  checker.Result
	SetSHA256   string
	Diagnostics []Diagnostic
}

// Executor owns process-level preparation policy. A nil Environment uses the
// current host environment; a non-nil empty slice supplies no ambient names.
// HookTimeout and DiagnosticLimit use conservative defaults when non-positive.
//
// The Go standard library has no portable network namespace or portable
// CPU/memory rlimit API. This executor therefore cannot itself enforce core's
// recommended network denial or finite non-time resources. Integrators should
// place the interpreter process in a host sandbox when available. Context and
// HookTimeout always provide a finite wall-clock limit.
type Executor struct {
	Trust           TrustState
	TempRoot        string
	Environment     []string
	HookTimeout     time.Duration
	DiagnosticLimit int
	LookPath        func(string) (string, error)

	// afterSourceCapture is a deterministic concurrency-test seam. It is not
	// exposed outside this package and runs only after the first stable source
	// observation, before private copying and the mandatory re-observation.
	afterSourceCapture func(candidateRoot string)
}

// New constructs an executor with the default finite limits.
func New(trust TrustState) *Executor {
	return &Executor{
		Trust:           trust,
		HookTimeout:     DefaultHookTimeout,
		DiagnosticLimit: DefaultDiagnosticLimit,
	}
}

// Prepare executes the complete selected set exactly once or returns a typed
// rejection. It never writes the live store.
func (e *Executor) Prepare(ctx context.Context, request Request) (*Result, error) {
	if e == nil {
		return nil, typed(ErrorCapability, "", nil, nil, fmt.Errorf("%w: nil executor", ErrCapability))
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return e.prepare(ctx, request)
}

func typed(kind ErrorKind, hook string, diagnostic *Diagnostic, validation *checker.Result, err error) error {
	if err == nil {
		err = errors.New("unknown preparation failure")
	}
	return &Error{Kind: kind, Hook: hook, Diagnostic: diagnostic, Validation: validation, Err: err}
}
