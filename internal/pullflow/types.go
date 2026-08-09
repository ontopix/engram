// Package pullflow implements verified, merge-free incoming synchronization.
// Network acquisition is kept separate from local publication and every
// divergent local changeset is accepted through the managed writer.
package pullflow

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/ontopix/engram/internal/changeset"
	"github.com/ontopix/engram/internal/checker"
	"github.com/ontopix/engram/internal/managedread"
	"github.com/ontopix/engram/internal/managedwrite"
)

type State string

const (
	UpToDate      State = "up-to-date"
	FastForwarded State = "fast-forwarded"
	Replayed      State = "replayed"
	Conflict      State = "conflict"
	Rejected      State = "rejected"
	Aborted       State = "aborted"
)

// Result is the closed pull result from CLI contract section 10.1.
type Result struct {
	State               State                      `json:"state"`
	Remote              string                     `json:"remote"`
	RemoteRef           string                     `json:"remote_ref"`
	Before              managedread.GitState       `json:"before"`
	After               managedread.GitState       `json:"after"`
	Fetched             int                        `json:"fetched"`
	Replayed            int                        `json:"replayed"`
	Conflicts           []string                   `json:"conflicts"`
	Changes             []changeset.Change         `json:"changes"`
	Validation          checker.Result             `json:"validation"`
	CandidateValidation *checker.Result            `json:"candidate_validation"`
	Audits              []managedread.HistoryAudit `json:"audits"`
}

type ErrorKind string

const (
	ErrorUsage       ErrorKind = "usage"
	ErrorCancelled   ErrorKind = "cancelled"
	ErrorRepository  ErrorKind = "repository"
	ErrorCapability  ErrorKind = "capability"
	ErrorTrust       ErrorKind = "trust"
	ErrorHook        ErrorKind = "hook"
	ErrorNetwork     ErrorKind = "network"
	ErrorConflict    ErrorKind = "conflict"
	ErrorConcurrency ErrorKind = "concurrency"
	ErrorIntegration ErrorKind = "integration"
	ErrorIO          ErrorKind = "io"
	ErrorOperational ErrorKind = "operational"
)

var (
	ErrActiveReplay     = errors.New("a pull replay is already active")
	ErrNoActiveReplay   = errors.New("no pull replay is active")
	ErrUnrelated        = errors.New("unrelated local changes prevent synchronization")
	ErrUnrelatedHistory = errors.New("local and incoming histories have no common ancestor")
	ErrRecovery         = errors.New("pull recovery is required")
)

// Mutation records known local effects when a failure occurs after workflow
// state may have been published.
type Mutation struct {
	Durable          bool
	LocalRefs        []RefMutation
	Head             *HeadMutation
	CheckoutChanged  bool
	RecoveryRequired bool
}

type RefMutation struct {
	Ref    string
	Before *string
	After  *string
}

type HeadMutation struct {
	Before managedread.GitState
	After  managedread.GitState
}

type RecoveryResult struct {
	Needed    bool      `json:"needed"`
	Performed bool      `json:"performed"`
	Mutation  *Mutation `json:"-"`
}

type Error struct {
	Kind      ErrorKind
	Operation string
	Mutation  *Mutation
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

func KindOf(err error) ErrorKind {
	var typed *Error
	if errors.As(err, &typed) {
		return typed.Kind
	}
	return ""
}

func MutationOf(err error) *Mutation {
	var typed *Error
	if errors.As(err, &typed) {
		return typed.Mutation
	}
	return nil
}

func typed(kind ErrorKind, operation string, err error) error {
	if err == nil {
		err = errors.New("unknown pull failure")
	}
	return &Error{Kind: kind, Operation: operation, Err: err}
}

// ManagedWriter is the deliberately small adapter to M4 acceptance. It keeps
// pullflow tests deterministic and prevents workflow state from depending on
// managedwrite.Engine internals.
type ManagedWriter interface {
	Commit(context.Context, managedwrite.Request) (*managedwrite.Result, error)
	CommitImage(context.Context, managedwrite.ImageRequest) (*managedwrite.Result, error)
}

type Phase string

const (
	PhaseFetched         Phase = "fetched"
	PhaseReplayActivated Phase = "replay-activated"
	PhaseDraftPublished  Phase = "draft-published"
	PhaseReplayCommitted Phase = "replay-committed"
	PhaseFinalizing      Phase = "finalizing"
	PhaseFastForwarding  Phase = "fast-forwarding"
	PhaseRefUpdated      Phase = "ref-updated"
	PhaseHeadUpdated     Phase = "head-updated"
	PhaseIndexUpdated    Phase = "index-updated"
	PhaseWorktreeUpdated Phase = "worktree-updated"
)

// Puller owns the network environment, the M4 writer, and deterministic fault
// seams. A nil environment inherits the host after stripping Git/engram
// overlays. Fault runs after a named durable boundary.
type Puller struct {
	Writer      ManagedWriter
	Environment []string
	LookPath    func(string) (string, error)
	TempRoot    string
	Fault       func(Phase) error

	run commandRunner
	mu  sync.Mutex
}

type commandRunner func(context.Context, string, string, []string, []byte, ...string) commandResult

func New(writer ManagedWriter) *Puller { return &Puller{Writer: writer} }

func (p *Puller) checkpoint(phase Phase) error {
	if p == nil || p.Fault == nil {
		return nil
	}
	if err := p.Fault(phase); err != nil {
		return fmt.Errorf("fault after %s: %w", phase, err)
	}
	return nil
}

func stringPointer(value string) *string {
	copy := value
	return &copy
}

func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	return stringPointer(*value)
}
