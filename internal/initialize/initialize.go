// Package initialize creates and recovers one managed store without exposing
// a partially accepted repository at the requested target.
package initialize

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/ontopix/engram/internal/bootstrap"
	"github.com/ontopix/engram/internal/changeset"
	"github.com/ontopix/engram/internal/checker"
	"github.com/ontopix/engram/internal/fsatomic"
	"github.com/ontopix/engram/internal/gitpresent"
	"github.com/ontopix/engram/internal/gitraw"
	"github.com/ontopix/engram/internal/guard"
	"github.com/ontopix/engram/internal/lifecycle"
	"github.com/ontopix/engram/internal/managedread"
	"github.com/ontopix/engram/internal/managedwrite"
	"github.com/ontopix/engram/internal/rendezvous"
	"github.com/ontopix/engram/internal/treeimage"
)

const initialMessage = "Initialize engram store"

type ErrorKind string

const (
	ErrorUsage       ErrorKind = "usage"
	ErrorCancelled   ErrorKind = "cancelled"
	ErrorCapability  ErrorKind = "capability"
	ErrorConflict    ErrorKind = "conflict"
	ErrorConcurrency ErrorKind = "concurrency"
	ErrorIntegration ErrorKind = "integration"
	ErrorRepository  ErrorKind = "repository"
	ErrorIO          ErrorKind = "io"
	ErrorRecovery    ErrorKind = "recovery"
)

type Error struct {
	Kind             ErrorKind
	Operation        string
	MutationKnown    bool
	Durable          bool
	Commit           *string
	CheckoutChanged  bool
	RecoveryRequired bool
	Underlying       error
}

// Mutation is the closed set of local effects known when initialization
// returns an error. Commit names the accepted or still-private main ref when
// that update completed.
type Mutation struct {
	Durable          bool
	Commit           *string
	CheckoutChanged  bool
	RecoveryRequired bool
}

func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	message := string(e.Kind)
	if e.Operation != "" {
		message += ": " + e.Operation
	}
	if e.Underlying != nil {
		message += ": " + e.Underlying.Error()
	}
	return message
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Underlying
}

func KindOf(err error) ErrorKind {
	var typed *Error
	if errors.As(err, &typed) {
		return typed.Kind
	}
	return ""
}

// MutationOf merges mutation evidence across joined initialization errors.
// Cleanup failures are commonly joined to the operation error, so using a
// single errors.As result would lose whichever half appears later. Durable,
// Commit, and CheckoutChanged are monotonic; RecoveryRequired is the final
// snapshot carried by the outer mutation or last joined mutation.
func MutationOf(err error) (Mutation, bool) {
	var visit func(error) (Mutation, bool)
	visit = func(current error) (Mutation, bool) {
		if current == nil {
			return Mutation{}, false
		}
		if typedError, ok := current.(*Error); ok {
			if typedError.MutationKnown || typedError.Durable || typedError.Commit != nil || typedError.CheckoutChanged || typedError.RecoveryRequired {
				result := Mutation{
					Durable: typedError.Durable, Commit: cloneString(typedError.Commit),
					CheckoutChanged: typedError.CheckoutChanged, RecoveryRequired: typedError.RecoveryRequired,
				}
				if nested, present := visit(typedError.Underlying); present {
					result.Durable = result.Durable || nested.Durable
					result.CheckoutChanged = result.CheckoutChanged || nested.CheckoutChanged
					if result.Commit == nil {
						result.Commit = cloneString(nested.Commit)
					}
				}
				return result, true
			}
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
				if childMutation.Commit != nil {
					result.Commit = cloneString(childMutation.Commit)
				}
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

type Identity struct {
	Name  string
	Email string
}

type Options struct {
	Schemas     []string
	DryRun      bool
	Identity    *Identity
	Environment []string
}

type Result struct {
	DryRun     bool                 `json:"dry_run"`
	Root       string               `json:"root"`
	Accepted   managedread.GitState `json:"accepted"`
	Files      []changeset.Change   `json:"files"`
	Launcher   guard.State          `json:"launcher"`
	Validation checker.Result       `json:"validation"`
}

// RecoveryResult reports only effects that the bounded initialization
// recovery can establish. Accepted is populated when publication had already
// completed and the exact accepted state was re-audited.
type RecoveryResult struct {
	Needed           bool                  `json:"needed"`
	Performed        bool                  `json:"performed"`
	Durable          bool                  `json:"durable"`
	CheckoutChanged  bool                  `json:"checkout_changed"`
	RecoveryRequired bool                  `json:"recovery_required"`
	Accepted         *managedread.GitState `json:"accepted"`
}

type Writer interface {
	CommitImage(context.Context, managedwrite.ImageRequest) (*managedwrite.Result, error)
}

type Phase string

const (
	PhaseLifecycleBegun      Phase = "lifecycle-begun"
	PhaseStaged              Phase = "staged"
	PhaseAccepted            Phase = "accepted"
	PhaseCleanupRequired     Phase = "cleanup-required"
	PhaseFilesPublished      Phase = "files-published"
	PhaseRepositoryPublished Phase = "repository-published"
	PhaseStageCleaned        Phase = "stage-cleaned"
	PhaseCleaned             Phase = "cleaned"
)

type initializationLifecycle interface {
	State() lifecycle.State
	RequireCleanup() error
	Remove() error
	RecoveryRequired() bool
}

// Initializer owns fault seams and the managed writer used only inside the
// unpublished staging repository.
type Initializer struct {
	Writer Writer
	Fault  func(Phase) error

	beginLifecycle func(string, lifecycle.Operation) (initializationLifecycle, error)
	deriveStage    func(lifecycle.State) (string, error)
	cleanupStage   func(string) error
	renamePath     func(string, string) error
	syncPath       func(string) error
}

func New(writer Writer) *Initializer { return &Initializer{Writer: writer} }

func (i *Initializer) checkpoint(phase Phase) error {
	if i == nil || i.Fault == nil {
		return nil
	}
	return i.Fault(phase)
}

func (i *Initializer) begin(target string) (initializationLifecycle, error) {
	if i != nil && i.beginLifecycle != nil {
		return i.beginLifecycle(target, lifecycle.Initialization)
	}
	return lifecycle.Begin(target, lifecycle.Initialization)
}

func (i *Initializer) stage(state lifecycle.State) (string, error) {
	if i != nil && i.deriveStage != nil {
		return i.deriveStage(state)
	}
	return lifecycle.Stage(state)
}

func (i *Initializer) cleanup(stage string) error {
	if i != nil && i.cleanupStage != nil {
		return i.cleanupStage(stage)
	}
	_, err := cleanupPrivateStage(stage)
	return err
}

func (i *Initializer) rename(oldPath, newPath string) error {
	if i != nil && i.renamePath != nil {
		return i.renamePath(oldPath, newPath)
	}
	_, err := fsatomic.RenameNoReplace(oldPath, newPath)
	return err
}

func (i *Initializer) sync(name string) error {
	if i != nil && i.syncPath != nil {
		return i.syncPath(name)
	}
	return syncDirectory(name)
}

// Recover adopts one exact dead-owner lifecycle and either discards its
// unpublished private store, rolls back its known additions, or verifies an
// already-published repository. It never scans siblings, runs hooks, uses the
// network, or moves an accepted ref.
func Recover(ctx context.Context, target string) (RecoveryResult, error) {
	return recover(ctx, target, nil)
}

// RecoverExpected binds recovery to the exact lifecycle state approved by an
// external read-only inspector.
func RecoverExpected(ctx context.Context, target string, expected lifecycle.RecoveryExpectation) (RecoveryResult, error) {
	return recover(ctx, target, &expected)
}

func recover(ctx context.Context, target string, expected *lifecycle.RecoveryExpectation) (result RecoveryResult, resultErr error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return RecoveryResult{Needed: true, RecoveryRequired: true}, typed(ErrorCancelled, "recover initialization", err)
	}
	canonical, err := canonicalTarget(target)
	if err != nil {
		return RecoveryResult{Needed: true, RecoveryRequired: true}, typed(ErrorUsage, "resolve initialization recovery target", err)
	}
	if _, _, err := lifecycle.Read(canonical, lifecycle.Initialization); errors.Is(err, os.ErrNotExist) {
		return RecoveryResult{}, nil
	}
	lease, err := lifecycle.AcquireRecovery(canonical, lifecycle.Initialization)
	if err != nil {
		kind := ErrorRecovery
		if errors.Is(err, rendezvous.ErrBusy) {
			kind = ErrorConcurrency
		}
		return RecoveryResult{Needed: true, RecoveryRequired: true}, &Error{
			Kind: kind, Operation: "acquire initialization recovery lease", RecoveryRequired: true, Underlying: err,
		}
	}
	defer func() {
		if err := lease.Release(); err != nil {
			resultErr = errors.Join(resultErr, &Error{
				Kind: ErrorRecovery, Operation: "release initialization recovery lease", Durable: result.Durable,
				CheckoutChanged: result.CheckoutChanged, RecoveryRequired: result.RecoveryRequired, Underlying: err,
			})
		}
	}()
	var handle *lifecycle.Handle
	if expected == nil {
		handle, err = lifecycle.Adopt(canonical, lifecycle.Initialization)
	} else {
		handle, err = lifecycle.AdoptExpected(canonical, lifecycle.Initialization, *expected)
	}
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return RecoveryResult{}, nil
		}
		kind := ErrorRecovery
		if errors.Is(err, lifecycle.ErrOwnerLive) || errors.Is(err, lifecycle.ErrChanged) {
			kind = ErrorConcurrency
		}
		return RecoveryResult{Needed: true, RecoveryRequired: true}, typed(kind, "adopt initialization lifecycle", err)
	}
	stage, err := lifecycle.Stage(handle.State())
	if err != nil {
		return RecoveryResult{Needed: true, RecoveryRequired: true}, typed(ErrorRecovery, "derive initialization recovery staging", err)
	}
	if handle.State().Phase == lifecycle.Running {
		return finishRecovery(stage, handle, nil, false, false)
	}
	if handle.State().Phase != lifecycle.CleanupRequired {
		return RecoveryResult{Needed: true, RecoveryRequired: true}, typed(ErrorRecovery, "recognize initialization lifecycle", errors.New("unsupported initialization recovery phase"))
	}
	record, err := readPublicationPlan(stage, handle.State())
	if err != nil {
		if _, stageErr := os.Lstat(stage); errors.Is(stageErr, os.ErrNotExist) {
			return finishSidecarLastRecovery(ctx, canonical, handle)
		}
		return RecoveryResult{Needed: true, RecoveryRequired: true}, typed(ErrorRecovery, "read initialization recovery plan", err)
	}
	if err := ctx.Err(); err != nil {
		return RecoveryResult{Needed: true, RecoveryRequired: true}, typed(ErrorCancelled, "recover initialization", err)
	}

	gitPath := filepath.Join(canonical, ".git")
	if _, err := os.Lstat(gitPath); err == nil {
		publishedInfo, statErr := os.Lstat(canonical)
		if statErr != nil || publishedInfo.Mode()&os.ModeSymlink != 0 || !publishedInfo.IsDir() {
			return RecoveryResult{Needed: true, RecoveryRequired: true}, typed(ErrorConflict, "inspect published initialization target", errors.Join(statErr, errors.New("published target is not one real directory")))
		}
		accepted, verifyErr := acceptedPublishedState(ctx, canonical, record.Commit)
		if verifyErr != nil {
			return RecoveryResult{Needed: true, RecoveryRequired: true}, typed(ErrorRecovery, "verify recovered initialization", verifyErr)
		}
		return finishPublishedRecovery(ctx, stage, handle, accepted, record.Commit, publishedInfo)
	} else if !errors.Is(err, os.ErrNotExist) {
		return RecoveryResult{Needed: true, RecoveryRequired: true}, typed(ErrorRecovery, "inspect recovered initialization", err)
	}

	if !record.RootExists {
		if _, err := os.Lstat(canonical); err == nil {
			return RecoveryResult{Needed: true, RecoveryRequired: true}, typed(ErrorConflict, "recover initialization publication", errors.New("target exists without the recorded managed repository"))
		} else if !errors.Is(err, os.ErrNotExist) {
			return RecoveryResult{Needed: true, RecoveryRequired: true}, typed(ErrorRecovery, "inspect initialization target", err)
		}
		return finishRecovery(stage, handle, nil, false, false)
	}

	info, err := os.Lstat(canonical)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return RecoveryResult{Needed: true, RecoveryRequired: true}, typed(ErrorConflict, "recover initialization target", errors.Join(err, errors.New("existing initialization target is no longer one real directory")))
	}
	// Before removing a single published addition, prove that the private
	// accepted store and the recorded commit are still exact.
	if _, err := acceptedPublishedState(ctx, filepath.Join(stage, "store"), record.Commit); err != nil {
		return RecoveryResult{Needed: true, RecoveryRequired: true}, typed(ErrorRecovery, "verify private initialization store", err)
	}
	if err := handle.RevalidateRecovery(); err != nil {
		return RecoveryResult{Needed: true, RecoveryRequired: true}, typed(ErrorConcurrency, "revalidate initialization recovery plan", err)
	}
	changed, durable, err := rollbackPublishedAdditions(ctx, canonical, record)
	if err != nil {
		return RecoveryResult{Needed: true, Durable: durable, CheckoutChanged: changed, RecoveryRequired: true}, &Error{Kind: ErrorRecovery, Operation: "roll back initialization publication", Durable: durable, CheckoutChanged: changed, RecoveryRequired: true, Underlying: err}
	}
	return finishRecovery(stage, handle, nil, changed, durable)
}

// Run plans, accepts, and publishes one initialization. Dry-run stops after
// the portable empty-base transition and performs no write or Git operation.
func (i *Initializer) Run(ctx context.Context, target string, options Options) (result Result, resultErr error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return Result{}, typed(ErrorCancelled, "initialize", err)
	}
	plan, err := bootstrap.Build(ctx, target, options.Schemas)
	if err != nil {
		return Result{}, classify("plan initialization", err)
	}
	result = resultFromPlan(plan, options.DryRun)
	if options.DryRun || plan.Validation.Status != checker.StatusComplete || plan.Validation.HasErrors() {
		return result, nil
	}
	var original treeimage.Image
	if plan.RootExists {
		original, err = treeimage.Capture(plan.Root, true)
		if err != nil {
			return result, classify("capture initialization target", err)
		}
		verifiedPlan, rebuildErr := bootstrap.Build(ctx, plan.Root, options.Schemas)
		current, captureErr := treeimage.Capture(plan.Root, true)
		if rebuildErr != nil || captureErr != nil || !treeimage.Equal(original, current) || !samePlan(plan, verifiedPlan) {
			return result, typed(ErrorConcurrency, "stabilize initialization plan", errors.Join(rebuildErr, captureErr, errors.New("target changed while initialization was planned")))
		}
	}
	if i == nil || i.Writer == nil {
		return Result{}, typed(ErrorCapability, "select managed writer", errors.New("managed writer is unavailable"))
	}
	identity := Identity{}
	if options.Identity != nil {
		identity = *options.Identity
	} else {
		identity, err = configuredIdentity(ctx, options.Environment)
		if err != nil {
			return result, classify("read Git author identity", err)
		}
	}
	if !validIdentity(identity) {
		return result, typed(ErrorUsage, "validate Git author identity", errors.New("Git author name or email is not representable"))
	}

	handle, err := i.begin(plan.Root)
	if err != nil {
		if mutation, present := lifecycle.MutationOf(err); present {
			return result, &Error{
				Kind: ErrorRecovery, Operation: "begin initialization lifecycle", MutationKnown: true,
				Durable: mutation.Durable, RecoveryRequired: mutation.RecoveryRequired, Underlying: err,
			}
		}
		// Begin can fail after the sidecar file itself was synced but before its
		// parent-directory sync completed. If the exact closed state is visible,
		// retain it for bounded recovery and report the uncertain durability.
		if !errors.Is(err, lifecycle.ErrExists) {
			if _, _, stateErr := lifecycle.Read(plan.Root, lifecycle.Initialization); stateErr == nil {
				return result, &Error{Kind: ErrorRecovery, Operation: "begin initialization lifecycle", MutationKnown: true, RecoveryRequired: true, Underlying: err}
			}
		}
		return result, classify("begin initialization lifecycle", err)
	}
	stage, err := i.stage(handle.State())
	if err != nil {
		removeErr := removeInitializationLifecycle(handle)
		if removeErr != nil {
			return result, &Error{
				Kind: ErrorRecovery, Operation: "derive initialization staging", Durable: true,
				RecoveryRequired: lifecycleResidual(handle), Underlying: errors.Join(err, fmt.Errorf("clean initialization lifecycle: %w", removeErr)),
			}
		}
		return result, typed(ErrorRecovery, "derive initialization staging", err)
	}
	var privateCommit *string
	cleanupRunning := true
	defer func() {
		if !cleanupRunning {
			return
		}
		cleanupErr := i.cleanup(stage)
		var stateErr error
		if cleanupErr == nil {
			stateErr = removeInitializationLifecycle(handle)
		}
		if cleanupErr != nil || stateErr != nil {
			var commit *string
			if privateCommit != nil && privateAcceptedRefPresent(stage, *privateCommit) {
				commit = cloneString(privateCommit)
			}
			resultErr = errors.Join(resultErr, &Error{
				Kind: ErrorRecovery, Operation: "clean pre-publication initialization", Durable: true, Commit: commit,
				RecoveryRequired: lifecycleResidual(handle), Underlying: errors.Join(cleanupErr, stateErr),
			})
		}
	}()
	if err := i.checkpoint(PhaseLifecycleBegun); err != nil {
		return result, typed(ErrorIO, "fault after initialization lifecycle begin", err)
	}

	if err := os.Mkdir(stage, 0o700); err != nil {
		return result, classify("create initialization staging", err)
	}
	stageStore := filepath.Join(stage, "store")
	image, err := treeimage.FromSnapshot(plan.Candidate.Tree, plan.Modes)
	if err != nil {
		return result, typed(ErrorRepository, "construct initialization image", err)
	}
	if err := treeimage.Materialize(stageStore, image, false); err != nil {
		return result, classify("materialize initialization staging", err)
	}
	if err := initializeGit(ctx, stageStore, identity, options.Environment); err != nil {
		return result, err
	}
	if err := gitpresent.Configure(ctx, stageStore); err != nil {
		return result, typed(ErrorIntegration, "configure initialization presentation", err)
	}
	repository, err := gitraw.Discover(ctx, stageStore)
	if err != nil {
		return result, typed(ErrorRepository, "discover initialization staging", err)
	}
	launcher, err := guard.Install(ctx, repository)
	if err != nil {
		return result, typed(ErrorIntegration, "install initialization guard", err)
	}
	if err := stageCandidate(ctx, stageStore, options.Environment); err != nil {
		return result, err
	}
	if err := i.checkpoint(PhaseStaged); err != nil {
		return result, typed(ErrorIO, "fault after initialization staging", err)
	}
	written, err := i.Writer.CommitImage(ctx, managedwrite.ImageRequest{
		Store: stageStore, Message: initialMessage, Candidate: plan.Candidate, Modes: plan.Modes,
		RequireBase: true, ExpectedBase: nil,
	})
	if err != nil {
		if written != nil && written.Validation != nil {
			result.Validation = cloneValidation(*written.Validation)
			result.Files = cloneChanges(written.Changes)
		}
		return result, classifyManagedWrite("accept initialization candidate", err)
	}
	if written == nil || !written.Created || written.Commit == nil {
		return result, typed(ErrorRepository, "accept initialization candidate", errors.New("managed writer did not create an initialization commit"))
	}
	if written.Validation == nil {
		return result, typed(ErrorRepository, "accept initialization candidate", errors.New("managed writer omitted definitive validation"))
	}
	result.Validation = cloneValidation(*written.Validation)
	result.Files = cloneChanges(written.Changes)
	result.Accepted.Commit = cloneString(written.Commit)
	privateCommit = cloneString(written.Commit)
	result.Launcher = launcher
	if err := i.checkpoint(PhaseAccepted); err != nil {
		return result, typed(ErrorIO, "fault after initialization acceptance", err)
	}

	record, err := makePublicationPlan(plan, *written.Commit)
	if err != nil {
		return result, typed(ErrorRepository, "construct initialization publication plan", err)
	}
	if err := writePublicationPlan(stage, record); err != nil {
		return result, typed(ErrorIO, "record initialization publication plan", err)
	}
	if plan.RootExists {
		current, captureErr := treeimage.Capture(plan.Root, true)
		if captureErr != nil || !treeimage.Equal(original, current) {
			return result, typed(ErrorConcurrency, "recheck initialization target", errors.Join(captureErr, errors.New("target changed after planning")))
		}
	}
	if err := handle.RequireCleanup(); err != nil {
		// The deferred pre-publication cleanup still owns this failure path. It
		// removes the exact lifecycle inode after removing the private stage, so
		// recovery is required only if that cleanup itself reports a residual.
		return result, &Error{
			Kind: ErrorRecovery, Operation: "advance initialization lifecycle", MutationKnown: true,
			Durable: true, Commit: cloneString(written.Commit), Underlying: err,
		}
	}
	cleanupRunning = false
	if err := i.checkpoint(PhaseCleanupRequired); err != nil {
		return result, recoveryError("fault before initialization publication", *written.Commit, false, err)
	}

	checkoutChanged := false
	if plan.RootExists {
		filesChanged, err := i.publishIntoExisting(ctx, plan, record)
		checkoutChanged = checkoutChanged || filesChanged
		if err != nil {
			return result, recoveryError("publish initialization files", *written.Commit, checkoutChanged, err)
		}
		if err := i.checkpoint(PhaseFilesPublished); err != nil {
			return result, recoveryError("fault after initialization files", *written.Commit, checkoutChanged, err)
		}
		repositoryChanged, err := i.publishGitDirectory(stageStore, plan.Root)
		checkoutChanged = checkoutChanged || repositoryChanged
		if err != nil {
			return result, recoveryError("publish initialization repository", *written.Commit, checkoutChanged, err)
		}
	} else {
		if _, err := os.Lstat(plan.Root); err == nil {
			return result, recoveryError("publish initialization target", *written.Commit, false, errors.New("target appeared concurrently"))
		} else if !errors.Is(err, os.ErrNotExist) {
			return result, recoveryError("inspect initialization target", *written.Commit, false, err)
		}
		if err := i.rename(stageStore, plan.Root); err != nil {
			return result, recoveryError("publish initialization target", *written.Commit, false, err)
		}
		checkoutChanged = true
		if err := i.sync(filepath.Dir(plan.Root)); err != nil {
			return result, recoveryError("durably publish initialization target", *written.Commit, true, err)
		}
	}
	if err := i.checkpoint(PhaseRepositoryPublished); err != nil {
		return result, recoveryError("fault after initialization publication", *written.Commit, checkoutChanged, err)
	}
	if err := verifyPublished(ctx, plan.Root, *written.Commit); err != nil {
		return result, recoveryError("verify published initialization", *written.Commit, checkoutChanged, err)
	}
	if err := i.cleanup(stage); err != nil {
		return result, recoveryError("clean initialization staging", *written.Commit, checkoutChanged, err)
	}
	if err := i.checkpoint(PhaseStageCleaned); err != nil {
		return result, recoveryError("fault after initialization stage cleanup", *written.Commit, checkoutChanged, err)
	}
	if err := removeInitializationLifecycle(handle); err != nil {
		return result, &Error{
			Kind: ErrorRecovery, Operation: "clean initialization lifecycle", Durable: true, Commit: cloneString(written.Commit),
			CheckoutChanged: checkoutChanged, RecoveryRequired: lifecycleResidual(handle), Underlying: err,
		}
	}
	if err := i.checkpoint(PhaseCleaned); err != nil {
		return result, &Error{Kind: ErrorIO, Operation: "fault after initialization cleanup", Durable: true, Commit: cloneString(written.Commit), CheckoutChanged: checkoutChanged, Underlying: err}
	}
	return result, nil
}

func canonicalTarget(target string) (string, error) {
	if target == "" || !utf8.ValidString(target) {
		return "", errors.New("initialization recovery target is empty or not UTF-8")
	}
	absolute, err := filepath.Abs(target)
	if err != nil {
		return "", err
	}
	absolute = filepath.Clean(absolute)
	parent, err := filepath.EvalSymlinks(filepath.Dir(absolute))
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Clean(parent), filepath.Base(absolute)), nil
}

func acceptedPublishedState(ctx context.Context, root, commit string) (*managedread.GitState, error) {
	if err := verifyPublished(ctx, root, commit); err != nil {
		return nil, err
	}
	ref := "refs/heads/main"
	return &managedread.GitState{Ref: &ref, Commit: cloneString(&commit)}, nil
}

func finishRecovery(stage string, handle *lifecycle.Handle, accepted *managedread.GitState, checkoutChanged, durableEffect bool) (RecoveryResult, error) {
	if err := handle.RevalidateRecovery(); err != nil {
		return RecoveryResult{Needed: true, Durable: durableEffect, CheckoutChanged: checkoutChanged, RecoveryRequired: true, Accepted: accepted}, &Error{
			Kind: ErrorConcurrency, Operation: "revalidate initialization recovery plan", Durable: durableEffect,
			CheckoutChanged: checkoutChanged, RecoveryRequired: true, Underlying: err,
		}
	}
	cleanupDurable, err := cleanupPrivateStage(stage)
	durable := durableEffect || cleanupDurable
	if err != nil {
		return RecoveryResult{Needed: true, Durable: durable, CheckoutChanged: checkoutChanged, RecoveryRequired: true, Accepted: accepted}, &Error{
			Kind: ErrorRecovery, Operation: "clean initialization recovery staging", Durable: durable,
			CheckoutChanged: checkoutChanged, RecoveryRequired: true, Underlying: err,
		}
	}
	if err := removeInitializationLifecycle(handle); err != nil {
		// A concurrent successful recovery is idempotent. Do not attribute a
		// replacement owner's sidecar to this recovery attempt.
		if !lifecycleResidual(handle) {
			return RecoveryResult{Needed: true, Performed: true, Durable: durable, CheckoutChanged: checkoutChanged, Accepted: accepted}, nil
		}
		return RecoveryResult{Needed: true, Durable: durable, CheckoutChanged: checkoutChanged, RecoveryRequired: true, Accepted: accepted}, &Error{
			Kind: ErrorRecovery, Operation: "clean initialization recovery lifecycle", Durable: durable,
			CheckoutChanged: checkoutChanged, RecoveryRequired: true, Underlying: err,
		}
	}
	return RecoveryResult{Needed: true, Performed: true, Durable: true, CheckoutChanged: checkoutChanged, Accepted: accepted}, nil
}

func finishPublishedRecovery(ctx context.Context, stage string, handle *lifecycle.Handle, accepted *managedread.GitState, commit string, publishedInfo os.FileInfo) (RecoveryResult, error) {
	if err := handle.RevalidateRecovery(); err != nil {
		return RecoveryResult{Needed: true, RecoveryRequired: true, Accepted: accepted}, &Error{
			Kind: ErrorConcurrency, Operation: "revalidate published initialization recovery",
			RecoveryRequired: true, Underlying: err,
		}
	}
	cleanupDurable, err := cleanupPrivateStage(stage)
	if err != nil {
		return RecoveryResult{Needed: true, Durable: cleanupDurable, RecoveryRequired: true, Accepted: accepted}, &Error{
			Kind: ErrorRecovery, Operation: "clean published initialization recovery staging", Durable: cleanupDurable,
			RecoveryRequired: true, Underlying: err,
		}
	}
	if err := requireSameRealDirectory(publishedInfo, handle.State().Target); err != nil {
		return RecoveryResult{Needed: true, Durable: cleanupDurable, RecoveryRequired: true}, &Error{
			Kind: ErrorConcurrency, Operation: "recheck published initialization target", Durable: cleanupDurable,
			RecoveryRequired: true, Underlying: err,
		}
	}
	rechecked, err := acceptedPublishedState(ctx, handle.State().Target, commit)
	if err != nil || !reflect.DeepEqual(accepted, rechecked) {
		return RecoveryResult{Needed: true, Durable: cleanupDurable, RecoveryRequired: true}, &Error{
			Kind: ErrorConcurrency, Operation: "recheck published initialization recovery", Durable: cleanupDurable,
			RecoveryRequired: true, Underlying: errors.Join(err, errors.New("published initialization changed during recovery")),
		}
	}
	if err := requireSameRealDirectory(publishedInfo, handle.State().Target); err != nil {
		return RecoveryResult{Needed: true, Durable: cleanupDurable, RecoveryRequired: true}, &Error{
			Kind: ErrorConcurrency, Operation: "finalize published initialization recovery", Durable: cleanupDurable,
			RecoveryRequired: true, Underlying: err,
		}
	}
	if err := removeInitializationLifecycle(handle); err != nil {
		if !lifecycleResidual(handle) {
			return RecoveryResult{Needed: true, Performed: true, Durable: cleanupDurable, Accepted: rechecked}, nil
		}
		return RecoveryResult{Needed: true, Durable: cleanupDurable, RecoveryRequired: true, Accepted: rechecked}, &Error{
			Kind: ErrorRecovery, Operation: "clean published initialization recovery lifecycle", Durable: cleanupDurable,
			RecoveryRequired: true, Underlying: err,
		}
	}
	return RecoveryResult{Needed: true, Performed: true, Durable: true, Accepted: rechecked}, nil
}

func requireSameRealDirectory(before os.FileInfo, name string) error {
	after, err := os.Lstat(name)
	if err != nil || before == nil || after.Mode()&os.ModeSymlink != 0 || !after.IsDir() || !os.SameFile(before, after) {
		return errors.Join(err, errors.New("published initialization target changed during recovery"))
	}
	return nil
}

// finishSidecarLastRecovery handles the sole legitimate cleanup tail where
// the exact private stage (and therefore its plan) has already been removed.
// It never removes target bytes or changes Git; it only revalidates the target
// around the approved absent-stage observation and then removes the sidecar.
func finishSidecarLastRecovery(ctx context.Context, target string, handle *lifecycle.Handle) (RecoveryResult, error) {
	if err := ctx.Err(); err != nil {
		return RecoveryResult{Needed: true, RecoveryRequired: true}, typed(ErrorCancelled, "recover sidecar-last initialization", err)
	}
	before, statErr := os.Lstat(target)
	if errors.Is(statErr, os.ErrNotExist) {
		if err := handle.RevalidateRecovery(); err != nil {
			return RecoveryResult{Needed: true, RecoveryRequired: true}, typed(ErrorConcurrency, "revalidate absent initialization target", err)
		}
		if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
			return RecoveryResult{Needed: true, RecoveryRequired: true}, typed(ErrorConcurrency, "recheck absent initialization target", errors.Join(err, errors.New("initialization target appeared during recovery")))
		}
		return finishSidecarOnly(handle, nil)
	}
	if statErr != nil || before.Mode()&os.ModeSymlink != 0 || !before.IsDir() {
		return RecoveryResult{Needed: true, RecoveryRequired: true}, typed(ErrorConflict, "inspect sidecar-last initialization target", errors.Join(statErr, errors.New("initialization target is not absent or one real directory")))
	}

	gitPath := filepath.Join(target, ".git")
	_, gitErr := os.Lstat(gitPath)
	if errors.Is(gitErr, os.ErrNotExist) {
		if err := handle.RevalidateRecovery(); err != nil {
			return RecoveryResult{Needed: true, RecoveryRequired: true}, typed(ErrorConcurrency, "revalidate existing initialization target", err)
		}
		after, err := os.Lstat(target)
		if err != nil || after.Mode()&os.ModeSymlink != 0 || !after.IsDir() || !os.SameFile(before, after) {
			return RecoveryResult{Needed: true, RecoveryRequired: true}, typed(ErrorConcurrency, "recheck existing initialization target", errors.Join(err, errors.New("initialization target changed during recovery")))
		}
		if _, err := os.Lstat(gitPath); !errors.Is(err, os.ErrNotExist) {
			return RecoveryResult{Needed: true, RecoveryRequired: true}, typed(ErrorConcurrency, "recheck unpublished initialization target", errors.Join(err, errors.New("repository appeared during recovery")))
		}
		return finishSidecarOnly(handle, nil)
	}
	if gitErr != nil {
		return RecoveryResult{Needed: true, RecoveryRequired: true}, typed(ErrorRecovery, "inspect sidecar-last initialization repository", gitErr)
	}
	accepted, err := acceptedPublishedStateWithoutPlan(ctx, target)
	if err != nil {
		return RecoveryResult{Needed: true, RecoveryRequired: true}, typed(ErrorConflict, "verify sidecar-last initialization repository", err)
	}
	if err := handle.RevalidateRecovery(); err != nil {
		return RecoveryResult{Needed: true, RecoveryRequired: true, Accepted: accepted}, &Error{
			Kind: ErrorConcurrency, Operation: "revalidate sidecar-last initialization repository",
			RecoveryRequired: true, Underlying: err,
		}
	}
	after, err := os.Lstat(target)
	if err != nil || after.Mode()&os.ModeSymlink != 0 || !after.IsDir() || !os.SameFile(before, after) {
		return RecoveryResult{Needed: true, RecoveryRequired: true}, &Error{
			Kind: ErrorConcurrency, Operation: "recheck sidecar-last initialization target", RecoveryRequired: true,
			Underlying: errors.Join(err, errors.New("initialization target changed during recovery")),
		}
	}
	rechecked, err := acceptedPublishedStateWithoutPlan(ctx, target)
	if err != nil || !reflect.DeepEqual(accepted, rechecked) {
		return RecoveryResult{Needed: true, RecoveryRequired: true}, &Error{
			Kind: ErrorConcurrency, Operation: "recheck sidecar-last initialization repository", RecoveryRequired: true,
			Underlying: errors.Join(err, errors.New("accepted initialization state changed during recovery")),
		}
	}
	return finishSidecarOnly(handle, rechecked)
}

func finishSidecarOnly(handle *lifecycle.Handle, accepted *managedread.GitState) (RecoveryResult, error) {
	if err := removeInitializationLifecycle(handle); err != nil {
		if !lifecycleResidual(handle) {
			return RecoveryResult{Needed: true, Performed: true, Accepted: accepted}, nil
		}
		return RecoveryResult{Needed: true, RecoveryRequired: true, Accepted: accepted}, &Error{
			Kind: ErrorRecovery, Operation: "clean sidecar-last initialization lifecycle",
			RecoveryRequired: true, Underlying: err,
		}
	}
	return RecoveryResult{Needed: true, Performed: true, Durable: true, Accepted: accepted}, nil
}

func acceptedPublishedStateWithoutPlan(ctx context.Context, root string) (*managedread.GitState, error) {
	store, err := managedread.Open(ctx, root)
	if err != nil {
		return nil, err
	}
	repository := store.Repository()
	if repository.HeadRef != "refs/heads/main" || repository.Head == nil {
		return nil, errors.New("published initialization has no accepted main state")
	}
	return acceptedPublishedState(ctx, root, repository.Head.String())
}

func rollbackPublishedAdditions(ctx context.Context, rootPath string, record publicationPlan) (changed, durable bool, resultErr error) {
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return false, false, err
	}
	defer func() { resultErr = errors.Join(resultErr, root.Close()) }()
	type ownedFile struct {
		path string
		info os.FileInfo
		data []byte
	}
	present := make([]ownedFile, 0, len(record.Files))
	for _, planned := range record.Files {
		if err := ctx.Err(); err != nil {
			return false, false, err
		}
		logical := filepath.FromSlash(planned.Path)
		info, err := root.Lstat(logical)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return false, false, errors.Join(err, fmt.Errorf("published path %s no longer has its owned shape", planned.Path))
		}
		file, err := root.Open(logical)
		if err != nil {
			return false, false, err
		}
		data, readErr := io.ReadAll(io.LimitReader(file, int64(len(planned.Data))+1))
		opened, statErr := file.Stat()
		closeErr := file.Close()
		after, afterErr := root.Lstat(logical)
		if readErr != nil || statErr != nil || closeErr != nil || afterErr != nil || !os.SameFile(info, opened) || !os.SameFile(opened, after) || !bytes.Equal(data, planned.Data) {
			return false, false, errors.Join(readErr, statErr, closeErr, afterErr, fmt.Errorf("published path %s differs from its recovery record", planned.Path))
		}
		present = append(present, ownedFile{path: planned.Path, info: after, data: append([]byte(nil), planned.Data...)})
	}
	dirtyDirectories := make(map[string]struct{})
	for index := len(present) - 1; index >= 0; index-- {
		if err := ctx.Err(); err != nil {
			return changed, durable, err
		}
		owned := present[index]
		logical := filepath.FromSlash(owned.path)
		before, err := root.Lstat(logical)
		if err != nil || before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() || !os.SameFile(owned.info, before) {
			return changed, durable, errors.Join(err, fmt.Errorf("published path %s changed after recovery preflight", owned.path))
		}
		file, err := root.Open(logical)
		if err != nil {
			return changed, durable, err
		}
		data, readErr := io.ReadAll(io.LimitReader(file, int64(len(owned.data))+1))
		opened, statErr := file.Stat()
		closeErr := file.Close()
		after, afterErr := root.Lstat(logical)
		if readErr != nil || statErr != nil || closeErr != nil || afterErr != nil ||
			!os.SameFile(owned.info, opened) || !os.SameFile(opened, after) || !bytes.Equal(data, owned.data) {
			return changed, durable, errors.Join(readErr, statErr, closeErr, afterErr, fmt.Errorf("published path %s changed immediately before rollback", owned.path))
		}
		if err := root.Remove(logical); err != nil {
			return changed, durable, err
		}
		changed = true
		dirtyDirectories[filepath.Dir(filepath.Join(rootPath, logical))] = struct{}{}
	}
	for index := len(record.Directories) - 1; index >= 0; index-- {
		logical := filepath.FromSlash(record.Directories[index])
		info, err := root.Lstat(logical)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return changed, durable, errors.Join(err, fmt.Errorf("publication directory %s changed shape", record.Directories[index]))
		}
		directory, err := root.Open(logical)
		if err != nil {
			return changed, durable, err
		}
		entries, readErr := directory.ReadDir(1)
		opened, statErr := directory.Stat()
		closeErr := directory.Close()
		after, afterErr := root.Lstat(logical)
		if readErr != nil && !errors.Is(readErr, io.EOF) || statErr != nil || closeErr != nil || afterErr != nil ||
			after.Mode()&os.ModeSymlink != 0 || !after.IsDir() || !os.SameFile(info, opened) || !os.SameFile(opened, after) {
			return changed, durable, errors.Join(readErr, statErr, closeErr, afterErr, fmt.Errorf("publication directory %s changed immediately before rollback", record.Directories[index]))
		}
		if len(entries) != 0 {
			continue // Preserve a directory now containing unrelated bytes.
		}
		removeErr := root.Remove(logical)
		if removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return changed, durable, removeErr
		}
		if removeErr == nil {
			changed = true
			dirtyDirectories[filepath.Dir(filepath.Join(rootPath, logical))] = struct{}{}
		}
	}
	if changed {
		var err error
		durable, err = syncChangedDirectoriesWithEvidence(dirtyDirectories, syncDirectory)
		if err != nil {
			return true, durable, err
		}
	}
	return changed, durable, nil
}

func resultFromPlan(plan *bootstrap.Plan, dryRun bool) Result {
	ref := "refs/heads/main"
	return Result{
		DryRun: dryRun, Root: plan.Root,
		Accepted: managedread.GitState{Ref: &ref}, Files: cloneChanges(plan.Changes),
		Launcher: guard.Planned, Validation: cloneValidation(plan.Validation),
	}
}

func samePlan(left, right *bootstrap.Plan) bool {
	return left != nil && right != nil && left.Root == right.Root && left.RootExists == right.RootExists &&
		reflect.DeepEqual(left.Files, right.Files) && reflect.DeepEqual(left.Candidate, right.Candidate) &&
		reflect.DeepEqual(left.Modes, right.Modes) && reflect.DeepEqual(left.Changes, right.Changes) &&
		reflect.DeepEqual(left.Validation, right.Validation)
}

type publicationFile struct {
	Path string `json:"path"`
	Data []byte `json:"data"`
}

type publicationPlan struct {
	Version     int               `json:"version"`
	Target      string            `json:"target"`
	RootExists  bool              `json:"root_exists"`
	Commit      string            `json:"commit"`
	Files       []publicationFile `json:"files"`
	Directories []string          `json:"directories"`
}

func makePublicationPlan(plan *bootstrap.Plan, commit string) (publicationPlan, error) {
	record := publicationPlan{Version: 1, Target: plan.Root, RootExists: plan.RootExists, Commit: commit, Files: []publicationFile{}, Directories: []string{}}
	paths := make([]string, 0, len(plan.Files))
	for name := range plan.Files {
		paths = append(paths, name)
	}
	sort.Strings(paths)
	directories := make(map[string]struct{})
	for _, name := range paths {
		record.Files = append(record.Files, publicationFile{Path: name, Data: append([]byte(nil), plan.Files[name]...)})
		if !plan.RootExists {
			continue
		}
		for directory := path.Dir(name); directory != "."; directory = path.Dir(directory) {
			host := filepath.Join(plan.Root, filepath.FromSlash(directory))
			info, err := os.Lstat(host)
			if errors.Is(err, os.ErrNotExist) {
				directories[directory] = struct{}{}
				continue
			}
			if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
				return publicationPlan{}, errors.Join(err, fmt.Errorf("publication parent %s is not a real directory", directory))
			}
		}
	}
	for directory := range directories {
		record.Directories = append(record.Directories, directory)
	}
	sort.Slice(record.Directories, func(left, right int) bool {
		leftDepth, rightDepth := strings.Count(record.Directories[left], "/"), strings.Count(record.Directories[right], "/")
		if leftDepth != rightDepth {
			return leftDepth < rightDepth
		}
		return record.Directories[left] < record.Directories[right]
	})
	return record, nil
}

func writePublicationPlan(stage string, record publicationPlan) error {
	data, err := encodePlan(record)
	if err != nil {
		return err
	}
	name := filepath.Join(stage, "plan-v1.json")
	file, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		return errors.Join(err, file.Close())
	}
	if err := file.Sync(); err != nil {
		return errors.Join(err, file.Close())
	}
	if err := file.Close(); err != nil {
		return err
	}
	return syncDirectory(stage)
}

func encodePlan(record publicationPlan) ([]byte, error) {
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(record); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func readPublicationPlan(stage string, state lifecycle.State) (publicationPlan, error) {
	name := filepath.Join(stage, "plan-v1.json")
	before, err := os.Lstat(name)
	if err != nil || before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() {
		return publicationPlan{}, errors.Join(err, errors.New("initialization publication plan is unavailable"))
	}
	file, err := os.Open(name)
	if err != nil {
		return publicationPlan{}, err
	}
	data, readErr := io.ReadAll(io.LimitReader(file, (16<<20)+1))
	opened, statErr := file.Stat()
	closeErr := file.Close()
	if readErr != nil || statErr != nil || closeErr != nil {
		return publicationPlan{}, errors.Join(readErr, statErr, closeErr)
	}
	after, lstatErr := os.Lstat(name)
	if lstatErr != nil || after.Mode()&os.ModeSymlink != 0 || !after.Mode().IsRegular() ||
		!os.SameFile(before, opened) || !os.SameFile(opened, after) {
		return publicationPlan{}, errors.Join(lstatErr, errors.New("initialization publication plan changed concurrently"))
	}
	if len(data) > 16<<20 {
		return publicationPlan{}, errors.New("initialization publication plan is too large")
	}
	var record publicationPlan
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return publicationPlan{}, errors.Join(err, errors.New("malformed initialization publication plan"))
	}
	canonical, err := encodePlan(record)
	if err != nil || !bytes.Equal(data, canonical) || validatePlan(record, state) != nil {
		return publicationPlan{}, errors.Join(err, errors.New("inconsistent initialization publication plan"))
	}
	return record, nil
}

func validatePlan(record publicationPlan, state lifecycle.State) error {
	if record.Version != 1 || record.Target != state.Target || record.Files == nil || record.Directories == nil {
		return errors.New("invalid plan header")
	}
	if _, err := gitraw.ParseOID(gitraw.SHA1, record.Commit); err != nil {
		if _, sha256Err := gitraw.ParseOID(gitraw.SHA256, record.Commit); sha256Err != nil {
			return errors.New("invalid plan commit")
		}
	}
	previous := ""
	files := make(map[string]struct{}, len(record.Files))
	for _, file := range record.Files {
		if !validLogicalPath(file.Path) || previous != "" && previous >= file.Path {
			return errors.New("invalid or unordered plan files")
		}
		files[file.Path] = struct{}{}
		previous = file.Path
	}
	previous = ""
	previousDepth := -1
	directories := make(map[string]struct{}, len(record.Directories))
	for _, directory := range record.Directories {
		depth := strings.Count(directory, "/")
		if !validLogicalPath(directory) || depth < previousDepth || depth == previousDepth && previous >= directory {
			return errors.New("invalid or unordered plan directories")
		}
		if _, collision := files[directory]; collision {
			return errors.New("plan file and directory collide")
		}
		directories[directory] = struct{}{}
		previous, previousDepth = directory, depth
	}
	for name := range files {
		for ancestor := path.Dir(name); ancestor != "."; ancestor = path.Dir(ancestor) {
			if _, collision := files[ancestor]; collision {
				return errors.New("plan file is an ancestor of another file")
			}
		}
	}
	if !record.RootExists && len(directories) != 0 {
		return errors.New("absent-target publication records directories")
	}
	return nil
}

func (i *Initializer) publishIntoExisting(ctx context.Context, plan *bootstrap.Plan, record publicationPlan) (changed bool, resultErr error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if _, err := os.Lstat(filepath.Join(plan.Root, ".git")); err == nil {
		return false, errors.New("target acquired Git administration concurrently")
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	root, err := os.OpenRoot(plan.Root)
	if err != nil {
		return false, err
	}
	defer func() { resultErr = errors.Join(resultErr, root.Close()) }()
	dirtyDirectories := make(map[string]struct{})
	for _, directory := range record.Directories {
		if err := root.Mkdir(filepath.FromSlash(directory), 0o755); err != nil {
			return changed, err
		}
		changed = true
		dirtyDirectories[filepath.Dir(filepath.Join(plan.Root, filepath.FromSlash(directory)))] = struct{}{}
		info, err := root.Lstat(filepath.FromSlash(directory))
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return changed, errors.Join(err, errors.New("publication directory changed"))
		}
	}
	for _, planned := range record.Files {
		if err := ctx.Err(); err != nil {
			return changed, err
		}
		file, err := root.OpenFile(filepath.FromSlash(planned.Path), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			return changed, err
		}
		changed = true
		dirtyDirectories[filepath.Dir(filepath.Join(plan.Root, filepath.FromSlash(planned.Path)))] = struct{}{}
		if _, err := file.Write(planned.Data); err != nil {
			return changed, errors.Join(err, file.Close())
		}
		if err := file.Sync(); err != nil {
			return changed, errors.Join(err, file.Close())
		}
		if err := file.Close(); err != nil {
			return changed, err
		}
	}
	if err := syncChangedDirectories(dirtyDirectories, i.sync); err != nil {
		return changed, err
	}
	return changed, nil
}

func syncChangedDirectories(dirty map[string]struct{}, syncer func(string) error) error {
	_, err := syncChangedDirectoriesWithEvidence(dirty, syncer)
	return err
}

func syncChangedDirectoriesWithEvidence(dirty map[string]struct{}, syncer func(string) error) (durable bool, resultErr error) {
	directories := make([]string, 0, len(dirty))
	for directory := range dirty {
		directories = append(directories, directory)
	}
	sort.Slice(directories, func(left, right int) bool {
		leftDepth := strings.Count(filepath.Clean(directories[left]), string(filepath.Separator))
		rightDepth := strings.Count(filepath.Clean(directories[right]), string(filepath.Separator))
		if leftDepth != rightDepth {
			return leftDepth > rightDepth
		}
		return directories[left] < directories[right]
	})
	for _, directory := range directories {
		if err := syncer(directory); err != nil {
			return durable, err
		}
		durable = true
	}
	return durable, nil
}

func (i *Initializer) publishGitDirectory(stageStore, target string) (bool, error) {
	source := filepath.Join(stageStore, ".git")
	destination := filepath.Join(target, ".git")
	if _, err := os.Lstat(destination); err == nil {
		return false, errors.New("target Git administration appeared concurrently")
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	if err := i.rename(source, destination); err != nil {
		return false, err
	}
	return true, i.sync(target)
}

func verifyPublished(ctx context.Context, root, commit string) error {
	store, err := managedread.Open(ctx, root)
	if err != nil {
		return err
	}
	if store.Repository().HeadRef != "refs/heads/main" || store.Repository().Head == nil || store.Repository().Head.String() != commit {
		return errors.New("published initialization names a different accepted state")
	}
	audit, err := store.AuditAccepted(ctx)
	if err != nil || audit.Tip != commit || audit.Validation.Status != checker.StatusComplete || audit.Validation.HasErrors() {
		return errors.Join(err, errors.New("published initialization audit failed"))
	}
	state, err := guard.Inspect(ctx, store.Repository())
	if err != nil || state != guard.Unchanged {
		return errors.Join(err, errors.New("published initialization guard differs"))
	}
	return nil
}

func initializeGit(ctx context.Context, root string, identity Identity, environment []string) error {
	if err := runGit(ctx, root, environment, true, "init", "--initial-branch=main"); err != nil {
		return typed(ErrorCapability, "initialize Git repository", err)
	}
	for _, pair := range [][2]string{{"user.name", identity.Name}, {"user.email", identity.Email}, {"commit.gpgsign", "false"}} {
		if err := runGit(ctx, root, environment, true, "config", "--local", pair[0], pair[1]); err != nil {
			return typed(ErrorRepository, "configure initialization repository", err)
		}
	}
	return nil
}

func stageCandidate(ctx context.Context, root string, environment []string) error {
	if err := runGit(ctx, root, environment, true, "add", "--all", "--"); err != nil {
		return typed(ErrorRepository, "stage initialization candidate", err)
	}
	return nil
}

func configuredIdentity(ctx context.Context, environment []string) (Identity, error) {
	read := func(key string) (string, error) {
		git, err := exec.LookPath("git")
		if err != nil {
			return "", err
		}
		command := exec.CommandContext(ctx, git, "--no-pager", "config", "--get", key)
		command.Env = gitEnvironment(environment, false)
		output, err := command.Output()
		if err != nil {
			return "", err
		}
		return strings.TrimSuffix(string(output), "\n"), nil
	}
	name, err := read("user.name")
	if err != nil {
		return Identity{}, err
	}
	email, err := read("user.email")
	if err != nil {
		return Identity{}, err
	}
	return Identity{Name: name, Email: email}, nil
}

func runGit(ctx context.Context, root string, environment []string, isolateGlobal bool, arguments ...string) error {
	git, err := exec.LookPath("git")
	if err != nil {
		return err
	}
	global := []string{
		"--no-pager", "--no-optional-locks", "--no-replace-objects",
		"-c", "core.hooksPath=" + os.DevNull, "-c", "core.fsmonitor=false", "-c", "core.untrackedCache=false",
		"-c", "maintenance.auto=false", "-c", "gc.auto=0", "-C", root,
	}
	command := exec.CommandContext(ctx, git, append(global, arguments...)...)
	command.Env = gitEnvironment(environment, isolateGlobal)
	var stderr bytes.Buffer
	command.Stdout = io.Discard
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("git %s: %w: %s", arguments[0], err, bounded(stderr.String()))
	}
	return nil
}

func gitEnvironment(environment []string, isolateGlobal bool) []string {
	if environment == nil {
		environment = os.Environ()
	}
	result := make([]string, 0, len(environment)+8)
	for _, item := range environment {
		name, _, _ := strings.Cut(item, "=")
		upper := strings.ToUpper(name)
		if strings.HasPrefix(upper, "GIT_") || strings.HasPrefix(upper, "ENGRAM_") || upper == "LC_ALL" {
			continue
		}
		result = append(result, item)
	}
	result = append(result, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_COUNT=0", "GIT_NO_LAZY_FETCH=1", "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0", "GIT_PAGER=cat", "LC_ALL=C")
	if isolateGlobal {
		result = append(result, "GIT_CONFIG_GLOBAL="+os.DevNull)
	}
	return result
}

func validIdentity(identity Identity) bool {
	return validIdentityPart(identity.Name, false) && validIdentityPart(identity.Email, true)
}

func validIdentityPart(value string, email bool) bool {
	if value == "" || !utf8.ValidString(value) || strings.ContainsAny(value, "\x00\r\n<>") {
		return false
	}
	return !email || strings.TrimSpace(value) == value
}

func cleanupPrivateStage(stage string) (bool, error) {
	info, err := os.Lstat(stage)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return false, errors.Join(err, errors.New("initialization staging is not one real directory"))
	}
	if err := os.RemoveAll(stage); err != nil {
		return false, err
	}
	if err := syncDirectory(filepath.Dir(stage)); err != nil {
		return false, err
	}
	return true, nil
}

func cloneValidation(value checker.Result) checker.Result {
	value.Findings = append([]checker.Finding{}, value.Findings...)
	return value
}

func cloneChanges(value []changeset.Change) []changeset.Change {
	if value == nil {
		return nil
	}
	result := make([]changeset.Change, len(value))
	copy(result, value)
	return result
}

func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func validLogicalPath(value string) bool {
	if value == "" || value == "." || !utf8.ValidString(value) || strings.HasPrefix(value, "/") || strings.Contains(value, "\\") {
		return false
	}
	for _, component := range strings.Split(value, "/") {
		if component == "" || component == "." || component == ".." {
			return false
		}
	}
	return true
}

func bounded(value string) string {
	if len(value) > 16<<10 {
		value = value[:16<<10]
	}
	return strings.TrimSpace(value)
}

func privateAcceptedRefPresent(stage, commit string) bool {
	repository, err := gitraw.Discover(context.Background(), filepath.Join(stage, "store"))
	return err == nil && repository.HeadRef == "refs/heads/main" && repository.Head != nil && repository.Head.String() == commit
}

func lifecycleResidual(handle initializationLifecycle) bool {
	return handle != nil && handle.RecoveryRequired()
}

func removeInitializationLifecycle(handle initializationLifecycle) error {
	if handle == nil {
		return nil
	}
	if err := handle.Remove(); err != nil {
		return err
	}
	if lifecycleResidual(handle) {
		return errors.New("initialization lifecycle removal retained its owned sidecar")
	}
	return nil
}

func recoveryError(operation, commit string, checkout bool, err error) error {
	return &Error{Kind: ErrorRecovery, Operation: operation, Durable: true, Commit: &commit, CheckoutChanged: checkout, RecoveryRequired: true, Underlying: err}
}

func typed(kind ErrorKind, operation string, err error) error {
	if err == nil {
		err = errors.New("unknown initialization failure")
	}
	return &Error{Kind: kind, Operation: operation, Underlying: err}
}

func classify(operation string, err error) error {
	if err == nil {
		return nil
	}
	var typedError *Error
	if errors.As(err, &typedError) {
		return err
	}
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return typed(ErrorCancelled, operation, err)
	case errors.Is(err, bootstrap.ErrTarget):
		return typed(ErrorUsage, operation, err)
	case errors.Is(err, bootstrap.ErrConflict), errors.Is(err, lifecycle.ErrExists):
		return typed(ErrorConflict, operation, err)
	case errors.Is(err, lifecycle.ErrChanged):
		return typed(ErrorConcurrency, operation, err)
	case errors.Is(err, os.ErrPermission):
		return typed(ErrorIO, operation, err)
	default:
		return typed(ErrorIO, operation, err)
	}
}

func classifyManagedWrite(operation string, err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return typed(ErrorCancelled, operation, err)
	}
	kind := ErrorRepository
	switch managedwrite.KindOf(err) {
	case managedwrite.FailureUsage:
		kind = ErrorUsage
	case managedwrite.FailureCapability:
		kind = ErrorCapability
	case managedwrite.FailureTrust, managedwrite.FailureHook, managedwrite.FailureGuard:
		kind = ErrorIntegration
	case managedwrite.FailureConcurrency:
		kind = ErrorConcurrency
	case managedwrite.FailureRecovery:
		kind = ErrorRecovery
	case managedwrite.FailureIO:
		kind = ErrorIO
	}
	return typed(kind, operation, err)
}

func syncDirectory(name string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	directory, err := os.Open(name)
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}
