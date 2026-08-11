// Package doctor implements the local, read-only integration diagnostics and
// bounded recovery surface of the reference CLI. It never fetches, executes
// store hooks, or changes an accepted ref.
package doctor

import (
	"context"
	"errors"

	"github.com/ontopix/engram/internal/gitraw"
	"github.com/ontopix/engram/internal/managedread"
)

type Class string

const (
	Required  Class = "required"
	Heuristic Class = "heuristic"
)

type Status string

const (
	OK      Status = "ok"
	Warning Status = "warning"
	Error   Status = "error"
)

// Check is the closed protocol-v1 diagnostic row. Path and Detail are
// pointers so their required JSON null representation is not lost.
type Check struct {
	Name   string  `json:"name"`
	Class  Class   `json:"class"`
	Status Status  `json:"status"`
	Path   *string `json:"path"`
	Detail *string `json:"detail"`
}

type Recovery struct {
	Requested bool                  `json:"requested"`
	Needed    bool                  `json:"needed"`
	Performed bool                  `json:"performed"`
	Accepted  *managedread.GitState `json:"accepted"`
}

type Result struct {
	Checks   []Check  `json:"checks"`
	Recovery Recovery `json:"recovery"`
}

// RecoveryController is the closed set of state owners doctor may authorize.
// It is deliberately about controller state, not CLI command routing.
type RecoveryController string

const (
	RecoveryInitialization  RecoveryController = "initialization"
	RecoveryAcquisition     RecoveryController = "acquisition"
	RecoverySynchronization RecoveryController = "synchronization"
	RecoveryManagedWrite    RecoveryController = "managed-write"
)

// RecoveryBinding identifies the exact controller state approved by the
// read-only inspection. StateSHA256 is the digest of the controller's
// canonical state bytes; the private proof also binds every participating
// rendezvous record.
type RecoveryBinding struct {
	Controller  RecoveryController
	OwnerToken  string
	StateSHA256 string
}

// RecoveryRequest carries a closed binding to the exact state doctor proved
// safe. Revalidate must succeed immediately before an adapter mutates state.
// The unexported proof prevents callers from manufacturing an approval.
type RecoveryRequest struct {
	Target     string
	Repository *gitraw.Repository
	Binding    RecoveryBinding
	proof      *recoveryProof
}

type RecoveryResponse struct {
	Accepted         *managedread.GitState
	Failure          ErrorKind
	Durable          bool
	LocalRefs        []RefMutation
	Head             *HeadMutation
	CheckoutChanged  bool
	RecoveryRequired bool
}

// RecoveryAdapter connects doctor to the transaction/synchronization engines
// without making their private journal formats part of doctor's public result.
// RecoveryFunc is the convenient adapter for engines whose concrete Recover
// result differs.
type RecoveryAdapter interface {
	Recover(context.Context, RecoveryRequest) (RecoveryResponse, error)
}

type RecoveryFunc func(context.Context, RecoveryRequest) (RecoveryResponse, error)

func (f RecoveryFunc) Recover(ctx context.Context, request RecoveryRequest) (RecoveryResponse, error) {
	return f(ctx, request)
}

type Options struct {
	Recover  bool
	Recovery RecoveryAdapter
}

type ErrorKind string

const (
	FailureCancelled   ErrorKind = "cancelled"
	FailureCapability  ErrorKind = "capability"
	FailureConcurrency ErrorKind = "concurrency"
	FailureRepository  ErrorKind = "repository"
	FailureIO          ErrorKind = "io"
	FailureTrust       ErrorKind = "trust"
	FailureHook        ErrorKind = "hook"
	FailureNetwork     ErrorKind = "network"
	FailureConflict    ErrorKind = "conflict"
	FailureIntegration ErrorKind = "integration"
	FailureOperational ErrorKind = "operational"
)

type Failure struct {
	Kind     ErrorKind
	Op       string
	Err      error
	Mutation *Mutation
}

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

func (e *Failure) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Err == nil {
		return e.Op
	}
	return e.Op + ": " + e.Err.Error()
}

func (e *Failure) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func KindOf(err error) ErrorKind {
	var failure *Failure
	if errors.As(err, &failure) {
		return failure.Kind
	}
	return ""
}

func MutationOf(err error) (Mutation, bool) {
	var failure *Failure
	if !errors.As(err, &failure) || failure.Mutation == nil {
		return Mutation{}, false
	}
	return *failure.Mutation, true
}

func fail(kind ErrorKind, operation string, err error) error {
	if err == nil {
		err = errors.New("unknown doctor failure")
	}
	return &Failure{Kind: kind, Op: operation, Err: err}
}

func failMutation(kind ErrorKind, operation string, err error, mutation Mutation) error {
	if err == nil {
		err = errors.New("unknown doctor recovery failure")
	}
	return &Failure{Kind: kind, Op: operation, Err: err, Mutation: &mutation}
}

var requiredNames = [...]string{
	"repository.shape",
	"identity.binding",
	"guard.ownership",
	"initialization.state",
	"acquisition.state",
	"recovery.state",
	"replay.state",
	"presentation.sparse",
	"presentation.transforms",
	"presentation.roundtrip",
	"cache.exclusion",
}

func initialChecks() []Check {
	checks := make([]Check, len(requiredNames))
	for index, name := range requiredNames {
		checks[index] = Check{Name: name, Class: Required, Status: OK}
	}
	return checks
}

func detail(value string) *string {
	copy := value
	return &copy
}

func pathPointer(value string) *string {
	copy := value
	return &copy
}
