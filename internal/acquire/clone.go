// Package acquire implements publish-after-validation managed-repository
// acquisition. Clone is the only operation here which may initiate network or
// credential effects; reuse inspection is strictly local.
package acquire

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"unicode/utf8"

	"github.com/ontopix/engram/internal/checker"
	"github.com/ontopix/engram/internal/fileidentity"
	"github.com/ontopix/engram/internal/gitpresent"
	"github.com/ontopix/engram/internal/gitraw"
	"github.com/ontopix/engram/internal/guard"
	"github.com/ontopix/engram/internal/hooks"
	"github.com/ontopix/engram/internal/lifecycle"
	"github.com/ontopix/engram/internal/managedread"
	"github.com/ontopix/engram/internal/rendezvous"
	"github.com/ontopix/engram/internal/transport"
)

type ErrorKind string

const (
	ErrorUsage       ErrorKind = "usage"
	ErrorCancelled   ErrorKind = "cancelled"
	ErrorCapability  ErrorKind = "capability"
	ErrorNetwork     ErrorKind = "network"
	ErrorConflict    ErrorKind = "conflict"
	ErrorConcurrency ErrorKind = "concurrency"
	ErrorIntegration ErrorKind = "integration"
	ErrorRepository  ErrorKind = "repository"
	ErrorIO          ErrorKind = "io"
	ErrorRecovery    ErrorKind = "recovery"
)

type Error struct {
	Kind     ErrorKind
	Op       string
	Err      error
	Mutation *Mutation
}

// Mutation is present when an acquisition failure occurred after durable
// controller state or the destination itself may have become visible. A
// caller must not flatten such a failure into an ordinary retryable error.
type Mutation struct {
	Durable          bool
	CheckoutChanged  bool
	RecoveryRequired bool
	Accepted         *managedread.GitState
}

func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Err == nil {
		return e.Op
	}
	return e.Op + ": " + e.Err.Error()
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

// MutationOf returns the recovery-bearing acquisition evidence, when any.
func MutationOf(err error) (Mutation, bool) {
	var typed *Error
	if !errors.As(err, &typed) || typed.Mutation == nil {
		return Mutation{}, false
	}
	result := *typed.Mutation
	result.Accepted = cloneGitState(result.Accepted)
	return result, true
}

type Options struct {
	Destination         string
	DestinationProvided bool
	ValidationScope     ValidationScope
}

// ValidationScope selects the accepted-state guarantee established during
// acquisition. The zero value preserves the original full-history behavior
// for standalone clone and reuse callers.
type ValidationScope string

const (
	ValidationScopeCurrent ValidationScope = "current"
	ValidationScopeHistory ValidationScope = "history"
)

type Result struct {
	Root            string                     `json:"root"`
	Remote          string                     `json:"remote"`
	Accepted        managedread.GitState       `json:"accepted"`
	Published       bool                       `json:"published"`
	Reused          bool                       `json:"reused"`
	VerifiedCommits int                        `json:"verified_commits"`
	ValidationScope ValidationScope            `json:"validation_scope"`
	Launcher        guard.State                `json:"launcher"`
	Validation      checker.Result             `json:"validation"`
	Audits          []managedread.HistoryAudit `json:"audits"`
}

// Phase identifies an acquisition fault-injection boundary. Every phase is
// reached immediately after the named operation has completed.
type Phase string

const (
	PhaseVerified        Phase = "verified"
	PhaseCleanupRequired Phase = "cleanup-required"
	PhasePublished       Phase = "published"
	PhaseDurable         Phase = "durable"
	PhaseStageCleaned    Phase = "stage-cleaned"
	PhaseCleaned         Phase = "cleaned"
)

// Cloner owns acquisition fault seams. The package-level Clone function uses
// a Cloner without injected faults.
type Cloner struct {
	Fault                    func(Phase) error
	syncPublicationDirectory func(string) (bool, error)
	renamePublication        func(string, string) (bool, error)
}

func New() *Cloner { return &Cloner{} }

func (c *Cloner) checkpoint(phase Phase) error {
	if c == nil || c.Fault == nil {
		return nil
	}
	return c.Fault(phase)
}

func (c *Cloner) syncPublication(name string) (bool, error) {
	if c != nil && c.syncPublicationDirectory != nil {
		return c.syncPublicationDirectory(name)
	}
	return syncDirectoryEffect(name)
}

func (c *Cloner) publish(oldPath, newPath string) (bool, error) {
	if c != nil && c.renamePublication != nil {
		return c.renamePublication(oldPath, newPath)
	}
	return renameNoReplace(oldPath, newPath)
}

// Clone obtains location into an unpublished sibling staging directory,
// configures byte-transparent presentation, validates the requested accepted
// scope, installs the owned raw-Git guard, and only then atomically publishes
// the checkout at its final path.
func Clone(ctx context.Context, location string, options Options) (Result, error) {
	return New().Run(ctx, location, options)
}

// Reuse verifies that destination is an existing clone of the exact location
// and that its complete accepted lineage, upstream, guard, and presentation
// still conform. It performs no network access and never mutates the clone.
func Reuse(ctx context.Context, location, destination string) (Result, error) {
	return ReuseWithOptions(ctx, location, destination, Options{})
}

// ReuseWithOptions verifies an existing clone using the requested validation
// scope. Destination fields in options are ignored.
func ReuseWithOptions(ctx context.Context, location, destination string, options Options) (Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return Result{}, typed(ErrorCancelled, "reuse clone", err)
	}
	if err := transport.ValidateLocation(location); err != nil {
		return Result{}, typed(ErrorUsage, "validate clone location", err)
	}
	if destination == "" || !utf8.ValidString(destination) {
		return Result{}, typed(ErrorUsage, "select clone destination", errors.New("destination is empty or not UTF-8"))
	}
	absolute, err := filepath.Abs(destination)
	if err != nil {
		return Result{}, typed(ErrorUsage, "select clone destination", err)
	}
	absolute = filepath.Clean(absolute)
	parent := filepath.Dir(absolute)
	parentInfo, err := os.Lstat(parent)
	if err != nil || parentInfo.Mode()&os.ModeSymlink != 0 || !parentInfo.IsDir() {
		return Result{}, typed(ErrorUsage, "select clone destination", errors.New("destination parent is not an existing real directory"))
	}
	canonicalParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return Result{}, typed(ErrorUsage, "select clone destination", err)
	}
	return reuse(ctx, location, filepath.Join(filepath.Clean(canonicalParent), filepath.Base(absolute)), options.ValidationScope)
}

// Run obtains location into the exact token-derived private stage, records a
// durable publication plan, and publishes only after the lifecycle state has
// advanced to cleanup-required.
func (c *Cloner) Run(ctx context.Context, location string, options Options) (result Result, resultErr error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return Result{}, typed(ErrorCancelled, "clone", err)
	}
	validationScope, err := normalizeValidationScope(options.ValidationScope)
	if err != nil {
		return Result{}, typed(ErrorUsage, "select clone validation scope", err)
	}
	options.ValidationScope = validationScope
	if err := transport.ValidateLocation(location); err != nil {
		return Result{}, typed(ErrorUsage, "validate clone location", err)
	}
	destination, err := cloneDestination(location, options)
	if err != nil {
		return Result{}, err
	}
	if _, statErr := os.Lstat(destination); statErr == nil {
		if options.DestinationProvided {
			return Result{}, typed(ErrorConflict, "select clone destination", errors.New("explicit destination already exists"))
		}
		return reuse(ctx, location, destination, validationScope)
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return Result{}, typed(ErrorIO, "inspect clone destination", statErr)
	}

	parent := filepath.Dir(destination)
	if !options.DestinationProvided {
		if err := os.MkdirAll(parent, 0o700); err != nil {
			return Result{}, typed(ErrorIO, "create default clone parent", err)
		}
	}
	parentInfo, err := os.Lstat(parent)
	if err != nil || parentInfo.Mode()&os.ModeSymlink != 0 || !parentInfo.IsDir() {
		return Result{}, typed(ErrorUsage, "select clone destination", errors.New("destination parent is not an existing real directory"))
	}
	canonicalParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return Result{}, typed(ErrorUsage, "select clone destination", err)
	}
	destination = filepath.Join(filepath.Clean(canonicalParent), filepath.Base(destination))
	if _, statErr := os.Lstat(destination); statErr == nil {
		if options.DestinationProvided {
			return Result{}, typed(ErrorConflict, "select clone destination", errors.New("explicit destination already exists"))
		}
		return reuse(ctx, location, destination, validationScope)
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return Result{}, typed(ErrorIO, "inspect clone destination", statErr)
	}
	handle, err := lifecycle.Begin(destination, lifecycle.Acquisition)
	if err != nil {
		if effect, present := lifecycle.MutationOf(err); present {
			kind := ErrorRecovery
			if errors.Is(err, lifecycle.ErrChanged) && !effect.RecoveryRequired {
				kind = ErrorConcurrency
			}
			return Result{}, mutationError(
				kind, "begin clone lifecycle", err,
				effect.Durable, false, effect.RecoveryRequired, nil,
			)
		}
		return Result{}, classify("begin clone lifecycle", err)
	}
	staging, err := lifecycle.Stage(handle.State())
	if err != nil {
		removeErr := handle.Remove()
		if removeErr != nil {
			return Result{}, mutationError(
				ErrorRecovery, "derive clone staging directory", errors.Join(err, removeErr),
				true, false, handle.RecoveryRequired(), nil,
			)
		}
		return Result{}, typed(ErrorRecovery, "derive clone staging directory", err)
	}
	cleanupRunning := true
	defer func() {
		if !cleanupRunning {
			return
		}
		stageErr := cleanupPrivateStage(staging)
		var stateErr error
		if stageErr == nil {
			stateErr = handle.Remove()
		}
		if stageErr != nil || stateErr != nil {
			resultErr = mutationError(
				ErrorRecovery, "clean pre-publication clone", errors.Join(resultErr, stageErr, stateErr),
				true, false, handle.RecoveryRequired(), nil,
			)
		}
	}()
	if err := os.Mkdir(staging, 0o700); err != nil {
		return Result{}, typed(ErrorIO, "create clone staging directory", err)
	}
	checkout := filepath.Join(staging, "store")
	if err := runClone(ctx, location, checkout); err != nil {
		return Result{}, err
	}
	if err := configurePresentation(ctx, checkout); err != nil {
		return Result{}, err
	}
	var repository *gitraw.Repository
	result, repository, err = verify(ctx, checkout, validationScope)
	if err != nil {
		return Result{}, err
	}
	result.Root = destination
	result.Remote = "origin"
	result.Launcher = guard.Planned
	if result.Validation.Status != checker.StatusComplete || result.Validation.HasErrors() {
		return result, nil
	}
	launcher, err := guard.Install(ctx, repository)
	if err != nil {
		return Result{}, typed(ErrorIntegration, "install managed Git guard", err)
	}
	result.Launcher = launcher
	if err := verifyOriginAndUpstream(ctx, checkout, location, repository.HeadRef); err != nil {
		return Result{}, err
	}
	if err := c.checkpoint(PhaseVerified); err != nil {
		return result, typed(ErrorIO, "fault after clone verification", err)
	}
	plan, err := makePublicationPlan(result, location)
	if err != nil {
		return result, typed(ErrorRepository, "construct clone publication plan", err)
	}
	if err := writePublicationPlan(staging, plan); err != nil {
		return result, typed(ErrorIO, "record clone publication plan", err)
	}
	if err := ctx.Err(); err != nil {
		return Result{}, typed(ErrorCancelled, "publish clone", err)
	}
	if _, err := os.Lstat(destination); err == nil {
		return Result{}, typed(ErrorConcurrency, "publish clone", errors.New("destination appeared concurrently"))
	} else if !errors.Is(err, os.ErrNotExist) {
		return Result{}, typed(ErrorIO, "publish clone", err)
	}
	if err := handle.RequireCleanup(); err != nil {
		effect, present := lifecycle.MutationOf(err)
		exact := handle.RecoveryRequired()
		// A failure before the authoritative sidecar name changes is still a
		// pre-publication attempt and can use the ordinary deferred cleanup. If
		// the cleanup-required state became visible, retain its exact stage and
		// plan for bounded recovery. Ambiguous/replaced state is also retained so
		// an obsolete handle never removes another controller's bytes.
		safeCleanup := (!present || !effect.Visible) && exact && handle.State().Phase == lifecycle.Running && !errors.Is(err, lifecycle.ErrChanged)
		if !safeCleanup {
			cleanupRunning = false
		}
		kind := ErrorRecovery
		if !exact || errors.Is(err, lifecycle.ErrChanged) {
			kind = ErrorConcurrency
		}
		return result, mutationError(kind, "advance clone lifecycle", err, true, false, !safeCleanup && exact, nil)
	}
	cleanupRunning = false
	if err := c.checkpoint(PhaseCleanupRequired); err != nil {
		return result, mutationError(ErrorRecovery, "fault before clone publication", err, true, false, handle.RecoveryRequired(), nil)
	}
	if _, err := os.Lstat(destination); err == nil {
		return result, mutationError(ErrorConcurrency, "publish clone", errors.New("destination appeared concurrently"), true, false, handle.RecoveryRequired(), nil)
	} else if !errors.Is(err, os.ErrNotExist) {
		return result, mutationError(ErrorIO, "publish clone", err, true, false, handle.RecoveryRequired(), nil)
	}
	published, err := c.publish(checkout, destination)
	if err != nil {
		kind := ErrorIO
		if errors.Is(err, os.ErrExist) {
			kind = ErrorConcurrency
		}
		return result, mutationError(kind, "publish clone", err, true, published, handle.RecoveryRequired(), nil)
	}
	if !published {
		return result, mutationError(ErrorIO, "publish clone", errors.New("clone publication reported success without moving the checkout"), true, false, handle.RecoveryRequired(), nil)
	}
	result.Published = true
	if err := c.checkpoint(PhasePublished); err != nil {
		return result, mutationError(ErrorRecovery, "fault after clone publication", err, true, true, handle.RecoveryRequired(), &result.Accepted)
	}
	durable, err := c.syncPublication(filepath.Dir(destination))
	if err != nil {
		return result, mutationError(ErrorRecovery, "durably publish clone", err, true, true, handle.RecoveryRequired(), &result.Accepted)
	}
	if !durable {
		return result, mutationError(ErrorRecovery, "durably publish clone", errors.New("publication directory sync did not establish durability"), true, true, handle.RecoveryRequired(), &result.Accepted)
	}
	if err := c.checkpoint(PhaseDurable); err != nil {
		return result, mutationError(ErrorRecovery, "fault after durable clone publication", err, true, true, handle.RecoveryRequired(), &result.Accepted)
	}
	if _, err := verifyPublished(ctx, destination, plan); err != nil {
		return result, mutationError(ErrorRecovery, "verify published clone", err, true, true, handle.RecoveryRequired(), &result.Accepted)
	}
	if err := handle.Remove(); err != nil {
		kind := ErrorRecovery
		if errors.Is(err, lifecycle.ErrChanged) && !handle.RecoveryRequired() {
			kind = ErrorConcurrency
		}
		return result, mutationError(kind, "clean clone lifecycle", err, true, true, handle.RecoveryRequired(), &result.Accepted)
	}
	if err := cleanupPrivateStage(staging); err != nil {
		return result, mutationError(ErrorIO, "clean clone staging", err, true, true, false, &result.Accepted)
	}
	if err := c.checkpoint(PhaseStageCleaned); err != nil {
		return result, mutationError(ErrorIO, "fault after clone stage cleanup", err, true, true, false, &result.Accepted)
	}
	if err := c.checkpoint(PhaseCleaned); err != nil {
		return result, mutationError(ErrorIO, "fault after clone cleanup", err, true, true, false, &result.Accepted)
	}
	return result, nil
}

const maxPublicationPlanBytes = 8 << 20

type publicationPlan struct {
	Version         int             `json:"version"`
	Target          string          `json:"target"`
	Location        string          `json:"location"`
	Remote          string          `json:"remote"`
	Ref             string          `json:"ref"`
	Commit          string          `json:"commit"`
	ValidationScope ValidationScope `json:"validation_scope,omitempty"`
}

func makePublicationPlan(result Result, location string) (publicationPlan, error) {
	if result.Accepted.Ref == nil || result.Accepted.Commit == nil {
		return publicationPlan{}, errors.New("verified clone has no accepted branch tip")
	}
	plan := publicationPlan{
		Version: 1, Target: result.Root, Location: location, Remote: result.Remote,
		Ref: *result.Accepted.Ref, Commit: *result.Accepted.Commit,
		ValidationScope: result.ValidationScope,
	}
	if err := validatePublicationPlan(plan, result.Root); err != nil {
		return publicationPlan{}, err
	}
	return plan, nil
}

func publicationPlanPath(stage string) string {
	return filepath.Join(stage, "plan-v1.json")
}

func writePublicationPlan(stage string, plan publicationPlan) error {
	data, err := encodePublicationPlan(plan)
	if err != nil {
		return err
	}
	name := publicationPlanPath(stage)
	file, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return syncDirectory(stage)
}

func decodePublicationPlan(data []byte, state lifecycle.State) (publicationPlan, error) {
	if len(data) > maxPublicationPlanBytes {
		return publicationPlan{}, errors.New("clone publication plan is too large")
	}
	var plan publicationPlan
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&plan); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return publicationPlan{}, errors.Join(err, errors.New("malformed clone publication plan"))
	}
	canonical, err := encodePublicationPlan(plan)
	if err != nil || !bytes.Equal(data, canonical) || validatePublicationPlan(plan, state.Target) != nil {
		return publicationPlan{}, errors.Join(err, errors.New("inconsistent clone publication plan"))
	}
	return plan, nil
}

func encodePublicationPlan(plan publicationPlan) ([]byte, error) {
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(plan); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func validatePublicationPlan(plan publicationPlan, target string) error {
	if plan.Version != 1 || plan.Target != target || !filepath.IsAbs(plan.Target) || filepath.Clean(plan.Target) != plan.Target || plan.Remote != "origin" {
		return errors.New("invalid clone publication plan header")
	}
	if err := transport.ValidateLocation(plan.Location); err != nil {
		return errors.New("invalid clone publication location")
	}
	if _, err := normalizeValidationScope(plan.ValidationScope); err != nil {
		return errors.New("invalid clone validation scope")
	}
	if !strings.HasPrefix(plan.Ref, "refs/heads/") || strings.TrimPrefix(plan.Ref, "refs/heads/") == "" {
		return errors.New("invalid clone publication ref")
	}
	if _, err := gitraw.ParseOID(gitraw.SHA1, plan.Commit); err != nil {
		if _, sha256Err := gitraw.ParseOID(gitraw.SHA256, plan.Commit); sha256Err != nil {
			return errors.New("invalid clone publication commit")
		}
	}
	return nil
}

// RecoveryResult records only local lifecycle reconciliation. Recover never
// fetches, invokes a store hook, changes an accepted ref, or deletes target.
type RecoveryResult struct {
	Needed           bool                  `json:"needed"`
	Performed        bool                  `json:"performed"`
	Published        bool                  `json:"published"`
	Durable          bool                  `json:"durable"`
	CheckoutChanged  bool                  `json:"checkout_changed"`
	RecoveryRequired bool                  `json:"recovery_required"`
	Accepted         *managedread.GitState `json:"accepted"`
}

type recoveryOperations struct {
	readLifecycle   func(string, lifecycle.Operation) (lifecycle.State, []byte, error)
	acquireRecovery func(string, lifecycle.Operation) (*lifecycle.RecoveryLease, error)
	cleanupStage    func(string) error
	removeLifecycle func(*lifecycle.Handle) error
	verifyPublished func(context.Context, string, publicationPlan) (*managedread.GitState, error)
}

func (operations recoveryOperations) withDefaults() recoveryOperations {
	if operations.readLifecycle == nil {
		operations.readLifecycle = lifecycle.Read
	}
	if operations.acquireRecovery == nil {
		operations.acquireRecovery = lifecycle.AcquireRecovery
	}
	if operations.cleanupStage == nil {
		operations.cleanupStage = cleanupPrivateStage
	}
	if operations.removeLifecycle == nil {
		operations.removeLifecycle = func(handle *lifecycle.Handle) error { return handle.Remove() }
	}
	if operations.verifyPublished == nil {
		operations.verifyPublished = verifyPublished
	}
	return operations
}

// Recover reconciles one exact target-derived acquisition state. Running
// state can only own private staging. Cleanup-required state either owns an
// unpublished exact stage or names an already-published verified checkout.
func Recover(ctx context.Context, target string) (*RecoveryResult, error) {
	return recover(ctx, target, nil, recoveryOperations{})
}

// RecoverExpected binds recovery to the exact lifecycle state approved by an
// external read-only inspector.
func RecoverExpected(ctx context.Context, target string, expected lifecycle.RecoveryExpectation) (*RecoveryResult, error) {
	return recover(ctx, target, &expected, recoveryOperations{})
}

func acquisitionRecoveryFailure(target string, result *RecoveryResult, kind ErrorKind, operation string, err error, accepted *managedread.GitState, owned ...*lifecycle.Handle) (*RecoveryResult, error) {
	if result == nil {
		result = &RecoveryResult{}
	}
	result.Needed = true
	result.CheckoutChanged = false
	result.RecoveryRequired = acquisitionResidual(target)
	if len(owned) != 0 && owned[0] != nil {
		result.RecoveryRequired = owned[0].RecoveryRequired()
	}
	if result.Accepted == nil {
		result.Accepted = cloneGitState(accepted)
	}
	return result, mutationError(
		kind, operation, err, result.Durable, false, result.RecoveryRequired, accepted,
	)
}

func recover(ctx context.Context, target string, expected *lifecycle.RecoveryExpectation, operations recoveryOperations) (result *RecoveryResult, resultErr error) {
	operations = operations.withDefaults()
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, typed(ErrorCancelled, "recover clone", err)
	}
	canonical, err := canonicalRecoveryTarget(target)
	if err != nil {
		return nil, typed(ErrorUsage, "resolve clone recovery target", err)
	}
	state, _, err := operations.readLifecycle(canonical, lifecycle.Acquisition)
	if err != nil {
		switch {
		case errors.Is(err, lifecycle.ErrChanged):
			// A stable read can lose the sidecar while another controller is
			// finishing. Confirm that outcome only after acquiring the recovery
			// lease; a replacement must instead be adopted and revalidated.
		case errors.Is(err, os.ErrNotExist):
			if expected == nil {
				return &RecoveryResult{}, nil
			}
			return acquisitionRecoveryFailure(canonical, nil, ErrorConcurrency, "read clone lifecycle", errors.Join(lifecycle.ErrChanged, err), nil)
		default:
			return acquisitionRecoveryFailure(canonical, nil, ErrorRecovery, "read clone lifecycle", err, nil)
		}
	}
	lease, err := operations.acquireRecovery(canonical, lifecycle.Acquisition)
	if err != nil {
		kind := ErrorRecovery
		if errors.Is(err, rendezvous.ErrBusy) {
			kind = ErrorConcurrency
		}
		return acquisitionRecoveryFailure(canonical, nil, kind, "acquire clone recovery lease", err, nil)
	}
	defer func() {
		if releaseErr := lease.Release(); releaseErr != nil {
			effect := Mutation{}
			if result != nil {
				effect.Durable = result.Durable
				effect.CheckoutChanged = result.CheckoutChanged
				effect.RecoveryRequired = result.RecoveryRequired
				effect.Accepted = cloneGitState(result.Accepted)
			}
			if existing, present := MutationOf(resultErr); present {
				effect.Durable = effect.Durable || existing.Durable
				effect.CheckoutChanged = effect.CheckoutChanged || existing.CheckoutChanged
				effect.RecoveryRequired = effect.RecoveryRequired || existing.RecoveryRequired
				if effect.Accepted == nil {
					effect.Accepted = cloneGitState(existing.Accepted)
				}
			}
			kind := KindOf(resultErr)
			if kind == "" {
				kind = ErrorIO
			}
			resultErr = mutationError(
				kind, "release clone recovery lease", errors.Join(resultErr, releaseErr),
				effect.Durable, effect.CheckoutChanged, effect.RecoveryRequired, effect.Accepted,
			)
		}
	}()
	var handle *lifecycle.Handle
	if expected == nil {
		handle, err = lifecycle.Adopt(canonical, lifecycle.Acquisition)
	} else {
		handle, err = lifecycle.AdoptExpected(canonical, lifecycle.Acquisition, *expected)
	}
	if err != nil {
		// Another recovery controller may have completed after our initial
		// read and released the nonblocking lease before this attempt acquired
		// it. An unbound recovery is idempotent in that case; an explicitly
		// observed recovery keeps reporting the changed input as concurrency.
		if expected == nil && lifecycleAbsentAtAdopt(canonical, lifecycle.Acquisition, err) {
			return &RecoveryResult{}, nil
		}
		kind := ErrorRecovery
		if errors.Is(err, lifecycle.ErrOwnerLive) || errors.Is(err, lifecycle.ErrChanged) || errors.Is(err, os.ErrNotExist) {
			kind = ErrorConcurrency
		}
		return acquisitionRecoveryFailure(canonical, nil, kind, "adopt clone lifecycle", err, nil)
	}
	state = handle.State()
	stage, err := lifecycle.Stage(state)
	if err != nil {
		return acquisitionRecoveryFailure(canonical, nil, ErrorRecovery, "derive clone recovery stage", err, nil, handle)
	}
	if state.Phase == lifecycle.Running {
		if err := ctx.Err(); err != nil {
			return acquisitionRecoveryFailure(canonical, nil, ErrorCancelled, "cancel clone recovery", err, nil, handle)
		}
		if err := handle.RevalidateRecovery(); err != nil {
			return acquisitionRecoveryFailure(canonical, nil, ErrorConcurrency, "revalidate running clone recovery", err, nil, handle)
		}
		stagePresent := pathPresent(stage)
		if err := operations.cleanupStage(stage); err != nil {
			return acquisitionRecoveryFailure(canonical, nil, ErrorRecovery, "clean running clone stage", err, nil, handle)
		}
		result := &RecoveryResult{Needed: true, Durable: stagePresent}
		if err := operations.removeLifecycle(handle); err != nil {
			if effect, present := lifecycle.MutationOf(err); present {
				result.Durable = result.Durable || effect.Durable
			}
			result.Performed = !handle.RecoveryRequired()
			kind := ErrorRecovery
			if errors.Is(err, lifecycle.ErrChanged) && !handle.RecoveryRequired() {
				kind = ErrorConcurrency
			}
			return acquisitionRecoveryFailure(canonical, result, kind, "clean running clone lifecycle", err, nil, handle)
		}
		return &RecoveryResult{Needed: true, Performed: true, Durable: true}, nil
	}
	stageInfo, stageErr := os.Lstat(stage)
	if errors.Is(stageErr, os.ErrNotExist) {
		return finishSidecarLastRecovery(ctx, canonical, handle, operations)
	}
	if stageErr != nil || stageInfo.Mode()&os.ModeSymlink != 0 || !stageInfo.IsDir() {
		return acquisitionRecoveryFailure(canonical, nil, ErrorRecovery, "inspect clone recovery stage", errors.Join(stageErr, errors.New("clone stage is not one real directory")), nil, handle)
	}

	stageStorePresent, err := inspectStagedStore(stage)
	if err != nil {
		return acquisitionRecoveryFailure(canonical, nil, ErrorRecovery, "inspect clone recovery stage", err, nil, handle)
	}
	if stageStorePresent {
		// Atomic rename has not consumed the exact source. The target is never
		// touched, even when an unrelated path appeared concurrently.
		if err := ctx.Err(); err != nil {
			return acquisitionRecoveryFailure(canonical, nil, ErrorCancelled, "cancel clone recovery", err, nil, handle)
		}
		if err := handle.RevalidateRecovery(); err != nil {
			return acquisitionRecoveryFailure(canonical, nil, ErrorConcurrency, "revalidate unpublished clone recovery", err, nil, handle)
		}
		if err := operations.cleanupStage(stage); err != nil {
			return acquisitionRecoveryFailure(canonical, nil, ErrorRecovery, "cancel unpublished clone", err, nil, handle)
		}
		result := &RecoveryResult{Needed: true, Durable: true}
		if err := operations.removeLifecycle(handle); err != nil {
			if effect, present := lifecycle.MutationOf(err); present {
				result.Durable = result.Durable || effect.Durable
			}
			result.Performed = !handle.RecoveryRequired()
			kind := ErrorRecovery
			if errors.Is(err, lifecycle.ErrChanged) && !handle.RecoveryRequired() {
				kind = ErrorConcurrency
			}
			return acquisitionRecoveryFailure(canonical, result, kind, "clean unpublished clone lifecycle", err, nil, handle)
		}
		return &RecoveryResult{Needed: true, Performed: true, Durable: true}, nil
	}

	info, statErr := os.Lstat(canonical)
	if statErr == nil {
		statErr = fileidentity.Pin(info)
	}
	if errors.Is(statErr, os.ErrNotExist) {
		if err := ctx.Err(); err != nil {
			return acquisitionRecoveryFailure(canonical, nil, ErrorCancelled, "cancel clone recovery", err, nil, handle)
		}
		if err := handle.RevalidateRecovery(); err != nil {
			return acquisitionRecoveryFailure(canonical, nil, ErrorConcurrency, "revalidate absent clone recovery target", err, nil, handle)
		}
		if err := operations.cleanupStage(stage); err != nil {
			return acquisitionRecoveryFailure(canonical, nil, ErrorRecovery, "clean unpublished clone stage", err, nil, handle)
		}
		result := &RecoveryResult{Needed: true, Durable: true}
		if err := operations.removeLifecycle(handle); err != nil {
			if effect, present := lifecycle.MutationOf(err); present {
				result.Durable = result.Durable || effect.Durable
			}
			result.Performed = !handle.RecoveryRequired()
			kind := ErrorRecovery
			if errors.Is(err, lifecycle.ErrChanged) && !handle.RecoveryRequired() {
				kind = ErrorConcurrency
			}
			return acquisitionRecoveryFailure(canonical, result, kind, "clean unpublished clone lifecycle", err, nil, handle)
		}
		return &RecoveryResult{Needed: true, Performed: true, Durable: true}, nil
	}
	if statErr != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return acquisitionRecoveryFailure(canonical, nil, ErrorConflict, "inspect published clone", errors.Join(statErr, errors.New("clone target is not one real directory")), nil, handle)
	}
	planRaw, planPresent := handle.RecoveryPlanRaw()
	if !planPresent {
		return acquisitionRecoveryFailure(canonical, nil, ErrorRecovery, "read clone publication plan", errors.New("approved clone publication plan is unavailable"), nil, handle)
	}
	plan, planErr := decodePublicationPlan(planRaw, state)
	if planErr != nil {
		return acquisitionRecoveryFailure(canonical, nil, ErrorRecovery, "read clone publication plan", planErr, nil, handle)
	}
	accepted, err := operations.verifyPublished(ctx, canonical, plan)
	if err != nil {
		if ctx.Err() != nil {
			return acquisitionRecoveryFailure(canonical, nil, ErrorCancelled, "cancel clone recovery", ctx.Err(), nil, handle)
		}
		return acquisitionRecoveryFailure(canonical, nil, ErrorConflict, "verify published clone", err, nil, handle)
	}
	if err := ctx.Err(); err != nil {
		return acquisitionRecoveryFailure(canonical, &RecoveryResult{Needed: true, Published: true, Accepted: cloneGitState(accepted)}, ErrorCancelled, "cancel clone recovery", err, accepted, handle)
	}
	if err := handle.RevalidateRecovery(); err != nil {
		return acquisitionRecoveryFailure(canonical, &RecoveryResult{Needed: true, Published: true, Accepted: cloneGitState(accepted)}, ErrorConcurrency, "revalidate published clone recovery", err, accepted, handle)
	}
	beforeRecheck, statErr := os.Lstat(canonical)
	if statErr != nil || beforeRecheck.Mode()&os.ModeSymlink != 0 || !beforeRecheck.IsDir() || !os.SameFile(info, beforeRecheck) {
		return acquisitionRecoveryFailure(canonical, &RecoveryResult{Needed: true, Published: true, Accepted: cloneGitState(accepted)}, ErrorConcurrency, "recheck published clone identity", errors.Join(statErr, errors.New("published clone identity changed during recovery")), accepted, handle)
	}
	accepted, err = operations.verifyPublished(ctx, canonical, plan)
	if err != nil {
		return acquisitionRecoveryFailure(canonical, &RecoveryResult{Needed: true, Published: true}, ErrorConcurrency, "recheck published clone", err, nil, handle)
	}
	afterRecheck, statErr := os.Lstat(canonical)
	if statErr != nil || afterRecheck.Mode()&os.ModeSymlink != 0 || !afterRecheck.IsDir() || !os.SameFile(info, afterRecheck) || !os.SameFile(beforeRecheck, afterRecheck) {
		return acquisitionRecoveryFailure(canonical, &RecoveryResult{Needed: true, Published: true, Accepted: cloneGitState(accepted)}, ErrorConcurrency, "recheck published clone identity", errors.Join(statErr, errors.New("published clone identity changed during recovery")), accepted, handle)
	}
	if err := handle.RevalidateRecovery(); err != nil {
		return acquisitionRecoveryFailure(canonical, &RecoveryResult{Needed: true, Published: true, Accepted: cloneGitState(accepted)}, ErrorConcurrency, "finalize published clone recovery authority", err, accepted, handle)
	}
	// The plan is the only exact authority which binds a published repository to
	// this acquisition. Keep it until sidecar removal is known durable; otherwise
	// a failed removal would strand cleanup-required state with no recoverable
	// publication proof.
	result = &RecoveryResult{Needed: true, Published: true, Accepted: cloneGitState(accepted)}
	if err := operations.removeLifecycle(handle); err != nil {
		if effect, present := lifecycle.MutationOf(err); present {
			result.Durable = result.Durable || effect.Durable
		}
		result.Performed = !handle.RecoveryRequired()
		kind := ErrorRecovery
		if errors.Is(err, lifecycle.ErrChanged) && !handle.RecoveryRequired() {
			kind = ErrorConcurrency
		}
		return acquisitionRecoveryFailure(canonical, result, kind, "clean published clone lifecycle", err, accepted, handle)
	}
	result.Performed = true
	result.Durable = true
	if err := handle.RevalidateRecoveryStage(); err != nil {
		return acquisitionRecoveryFailure(canonical, result, ErrorConcurrency, "revalidate published clone stage cleanup", err, accepted, handle)
	}
	if err := operations.cleanupStage(stage); err != nil {
		return acquisitionRecoveryFailure(canonical, result, ErrorIO, "clean published clone stage", err, accepted, handle)
	}
	return result, nil
}

// lifecycleAbsentAtAdopt recognizes only a sidecar that was already absent
// when adoption began. ErrChanged means a non-cooperating mutation happened
// while this controller held the lease and therefore remains fail-closed. A
// replacement at the exact name also prevents idempotent success.
func lifecycleAbsentAtAdopt(target string, operation lifecycle.Operation, cause error) bool {
	if !errors.Is(cause, os.ErrNotExist) || errors.Is(cause, lifecycle.ErrChanged) {
		return false
	}
	_, err := os.Lstat(lifecycle.Sidecar(target, operation))
	return errors.Is(err, os.ErrNotExist)
}

// finishSidecarLastRecovery handles a missing exact private stage. Absence or
// a stable foreign non-repository directory proves that publication did not
// leave a managed checkout. A repository at the target is intentionally
// ambiguous without the plan and remains recovery-required.
func finishSidecarLastRecovery(ctx context.Context, target string, handle *lifecycle.Handle, operations recoveryOperations) (*RecoveryResult, error) {
	if err := ctx.Err(); err != nil {
		return acquisitionRecoveryFailure(target, nil, ErrorCancelled, "cancel sidecar-last clone recovery", err, nil, handle)
	}
	before, statErr := os.Lstat(target)
	if statErr == nil {
		statErr = fileidentity.Pin(before)
	}
	if errors.Is(statErr, os.ErrNotExist) {
		if err := handle.RevalidateRecovery(); err != nil {
			return acquisitionRecoveryFailure(target, nil, ErrorConcurrency, "revalidate absent sidecar-last clone target", err, nil, handle)
		}
		if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
			return acquisitionRecoveryFailure(target, nil, ErrorConcurrency, "recheck absent sidecar-last clone target", errors.Join(err, errors.New("clone target appeared during recovery")), nil, handle)
		}
		return finishAcquisitionSidecarOnly(handle, target, nil, false, operations)
	}
	if statErr != nil || before.Mode()&os.ModeSymlink != 0 || !before.IsDir() {
		return acquisitionRecoveryFailure(target, nil, ErrorConflict, "inspect sidecar-last clone target", errors.Join(statErr, errors.New("clone target is not absent or one real directory")), nil, handle)
	}

	gitPath := filepath.Join(target, ".git")
	_, gitErr := os.Lstat(gitPath)
	if errors.Is(gitErr, os.ErrNotExist) {
		if err := handle.RevalidateRecovery(); err != nil {
			return acquisitionRecoveryFailure(target, nil, ErrorConcurrency, "revalidate foreign sidecar-last clone target", err, nil, handle)
		}
		after, err := os.Lstat(target)
		if err != nil || after.Mode()&os.ModeSymlink != 0 || !after.IsDir() || !os.SameFile(before, after) {
			return acquisitionRecoveryFailure(target, nil, ErrorConcurrency, "recheck foreign sidecar-last clone target", errors.Join(err, errors.New("clone target changed during recovery")), nil, handle)
		}
		if _, err := os.Lstat(gitPath); !errors.Is(err, os.ErrNotExist) {
			return acquisitionRecoveryFailure(target, nil, ErrorConcurrency, "recheck unpublished sidecar-last clone target", errors.Join(err, errors.New("repository appeared during recovery")), nil, handle)
		}
		return finishAcquisitionSidecarOnly(handle, target, nil, false, operations)
	}
	if gitErr != nil {
		return acquisitionRecoveryFailure(target, nil, ErrorRecovery, "inspect sidecar-last clone repository", gitErr, nil, handle)
	}
	return acquisitionRecoveryFailure(
		target, nil, ErrorConflict, "recover clone without publication plan",
		errors.New("clone target repository cannot be bound to the missing publication plan"), nil, handle,
	)
}

func finishAcquisitionSidecarOnly(handle *lifecycle.Handle, target string, accepted *managedread.GitState, published bool, operations recoveryOperations) (*RecoveryResult, error) {
	result := &RecoveryResult{Needed: true, Published: published, Accepted: cloneGitState(accepted)}
	if err := operations.removeLifecycle(handle); err != nil {
		if effect, present := lifecycle.MutationOf(err); present {
			result.Durable = result.Durable || effect.Durable
		}
		result.Performed = !handle.RecoveryRequired()
		kind := ErrorRecovery
		if errors.Is(err, lifecycle.ErrChanged) && !handle.RecoveryRequired() {
			kind = ErrorConcurrency
		}
		return acquisitionRecoveryFailure(target, result, kind, "clean sidecar-last clone lifecycle", err, accepted, handle)
	}
	return &RecoveryResult{Needed: true, Performed: true, Published: published, Durable: true, Accepted: cloneGitState(accepted)}, nil
}

func canonicalRecoveryTarget(target string) (string, error) {
	if target == "" {
		return "", errors.New("clone recovery target is empty")
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
	info, err := os.Lstat(parent)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", errors.Join(err, errors.New("clone recovery parent is not a real directory"))
	}
	return filepath.Join(parent, filepath.Base(absolute)), nil
}

func inspectStagedStore(stage string) (bool, error) {
	stageInfo, err := os.Lstat(stage)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil || stageInfo.Mode()&os.ModeSymlink != 0 || !stageInfo.IsDir() {
		return false, errors.Join(err, errors.New("clone stage is not one real directory"))
	}
	store := filepath.Join(stage, "store")
	info, err := os.Lstat(store)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return false, errors.Join(err, errors.New("staged clone is not one real directory"))
	}
	return true, nil
}

func pathPresent(name string) bool {
	_, err := os.Lstat(name)
	return !errors.Is(err, os.ErrNotExist)
}

func cloneDestination(location string, options Options) (string, error) {
	if options.DestinationProvided {
		if options.Destination == "" || !utf8.ValidString(options.Destination) {
			return "", typed(ErrorUsage, "select clone destination", errors.New("destination is empty or not UTF-8"))
		}
		absolute, err := filepath.Abs(options.Destination)
		if err != nil {
			return "", typed(ErrorUsage, "select clone destination", err)
		}
		return filepath.Clean(absolute), nil
	}
	if options.Destination != "" {
		if !filepath.IsAbs(options.Destination) || filepath.Clean(options.Destination) != options.Destination {
			return "", typed(ErrorUsage, "select clone destination", errors.New("injected default destination is not absolute and clean"))
		}
		return options.Destination, nil
	}
	value, err := transport.DefaultDestination(location)
	if err != nil {
		return "", typed(ErrorCapability, "select default clone destination", err)
	}
	return value, nil
}

func reuse(ctx context.Context, location, destination string, requestedScope ValidationScope) (Result, error) {
	validationScope, err := normalizeValidationScope(requestedScope)
	if err != nil {
		return Result{}, typed(ErrorUsage, "select clone validation scope", err)
	}
	if _, _, err := lifecycle.Read(destination, lifecycle.Acquisition); err == nil {
		return Result{}, typed(ErrorConflict, "reuse clone", errors.New("existing clone has active or recoverable acquisition state"))
	} else if !errors.Is(err, os.ErrNotExist) {
		return Result{}, typed(ErrorConflict, "reuse clone", errors.New("existing clone acquisition state is inconsistent"))
	}
	result, repository, err := verify(ctx, destination, validationScope)
	if err != nil {
		return Result{}, typed(ErrorConflict, "reuse clone", err)
	}
	if result.Validation.Status != checker.StatusComplete || result.Validation.HasErrors() {
		return Result{}, typed(ErrorConflict, "reuse clone", errors.New("existing clone no longer conforms for the requested validation scope"))
	}
	if err := verifyOriginAndUpstream(ctx, destination, location, repository.HeadRef); err != nil {
		return Result{}, typed(ErrorConflict, "reuse clone", err)
	}
	if _, err := hooks.ResolveStoreIdentity(destination); err != nil {
		return Result{}, typed(ErrorConflict, "reuse clone identity", err)
	}
	launcher, err := guard.Inspect(ctx, repository)
	if err != nil || launcher != guard.Unchanged {
		return Result{}, typed(ErrorConflict, "reuse clone guard", err)
	}
	if ok, err := hasCacheExclusion(repository.GitDir); err != nil || !ok {
		return Result{}, typed(ErrorConflict, "reuse clone cache exclusion", err)
	}
	result.Root = destination
	result.Remote = "origin"
	result.Launcher = launcher
	result.Reused = true
	return result, nil
}

func verify(ctx context.Context, root string, requestedScope ValidationScope) (Result, *gitraw.Repository, error) {
	validationScope, err := normalizeValidationScope(requestedScope)
	if err != nil {
		return Result{}, nil, typed(ErrorUsage, "select clone validation scope", err)
	}
	store, err := managedread.Open(ctx, root)
	if err != nil {
		return Result{}, nil, typed(ErrorRepository, "open cloned managed store", err)
	}
	repository := store.Repository()
	var validation checker.Result
	var audits []managedread.HistoryAudit
	verifiedCommits := 0
	if validationScope == ValidationScopeHistory {
		audit, err := store.AuditAccepted(ctx)
		if err != nil {
			return Result{}, nil, typed(ErrorRepository, "audit cloned managed store", err)
		}
		validation = audit.Validation
		audits = append([]managedread.HistoryAudit(nil), audit.Audits...)
		verifiedCommits = len(audit.Audits)
	} else {
		validation, err = store.CheckAcceptedState(ctx)
		if err != nil {
			return Result{}, nil, typed(ErrorRepository, "check cloned managed state", err)
		}
		audits = []managedread.HistoryAudit{}
		if repository.Head != nil {
			verifiedCommits = 1
		}
	}
	accepted := managedread.GitState{Ref: stringPtr(repository.HeadRef)}
	if repository.Head != nil {
		accepted.Commit = stringPtr(repository.Head.String())
	}
	result := Result{
		Root: repository.Root, Remote: "origin", Accepted: accepted,
		VerifiedCommits: verifiedCommits, ValidationScope: validationScope,
		Validation: validation, Audits: audits,
	}
	return result, repository, nil
}

func normalizeValidationScope(scope ValidationScope) (ValidationScope, error) {
	switch scope {
	case "", ValidationScopeHistory:
		return ValidationScopeHistory, nil
	case ValidationScopeCurrent:
		return ValidationScopeCurrent, nil
	default:
		return "", fmt.Errorf("unsupported validation scope %q", scope)
	}
}

func runClone(ctx context.Context, location, destination string) error {
	git, err := exec.LookPath("git")
	if err != nil {
		return typed(ErrorCapability, "locate Git", err)
	}
	arguments := []string{
		"-c", "core.longpaths=true",
		"--no-pager", "--no-optional-locks", "--no-replace-objects",
		"-c", "core.hooksPath=" + os.DevNull,
		"-c", "core.fsmonitor=false", "-c", "core.untrackedCache=false",
		"-c", "maintenance.auto=false", "-c", "gc.auto=0",
		"clone", "--origin", "origin", "--no-tags", "--no-recurse-submodules", "--single-branch", "--template=", "--", location, destination,
	}
	command := exec.CommandContext(ctx, git, arguments...)
	command.Env = isolatedEnvironment(os.Environ())
	var stderr bytes.Buffer
	command.Stdout = &stderr
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return typed(ErrorCancelled, "clone repository", ctx.Err())
		}
		return typed(ErrorNetwork, "clone repository", fmt.Errorf("%w: %s", err, bounded(stderr.Bytes())))
	}
	return nil
}

func configurePresentation(ctx context.Context, root string) error {
	if err := gitpresent.Configure(ctx, root); err != nil {
		return typed(ErrorIntegration, "configure byte-transparent presentation", err)
	}
	return nil
}

func verifyOriginAndUpstream(ctx context.Context, root, location, headRef string) error {
	urls, err := gitConfigAll(ctx, root, "remote.origin.url")
	if err != nil || len(urls) != 1 || urls[0] != location {
		return typed(ErrorRepository, "verify origin URL", errors.New("origin does not contain the exact requested URL"))
	}
	short, ok := strings.CutPrefix(headRef, "refs/heads/")
	if !ok {
		return typed(ErrorRepository, "verify clone upstream", errors.New("HEAD is not a local branch"))
	}
	remote, err := gitConfigAll(ctx, root, "branch."+short+".remote")
	if err != nil || len(remote) != 1 || remote[0] != "origin" {
		return typed(ErrorRepository, "verify clone upstream", errors.New("accepted branch does not track origin"))
	}
	merge, err := gitConfigAll(ctx, root, "branch."+short+".merge")
	if err != nil || len(merge) != 1 || merge[0] != headRef {
		return typed(ErrorRepository, "verify clone upstream", errors.New("accepted branch upstream differs"))
	}
	return nil
}

func verifyPublished(ctx context.Context, root string, plan publicationPlan) (*managedread.GitState, error) {
	result, repository, err := verifyPublishedStoreWithScope(ctx, root, plan.ValidationScope)
	if err != nil {
		return nil, err
	}
	if result.Accepted.Ref == nil || result.Accepted.Commit == nil ||
		*result.Accepted.Ref != plan.Ref || *result.Accepted.Commit != plan.Commit {
		return nil, errors.New("published clone names a different accepted state")
	}
	if err := verifyOriginAndUpstream(ctx, root, plan.Location, repository.HeadRef); err != nil {
		return nil, err
	}
	return cloneGitState(&result.Accepted), nil
}

func verifyPublishedStore(ctx context.Context, root string) (Result, *gitraw.Repository, error) {
	return verifyPublishedStoreWithScope(ctx, root, ValidationScopeHistory)
}

func verifyPublishedStoreWithScope(ctx context.Context, root string, validationScope ValidationScope) (Result, *gitraw.Repository, error) {
	result, repository, err := verify(ctx, root, validationScope)
	if err != nil {
		return Result{}, nil, err
	}
	if result.Validation.Status != checker.StatusComplete || result.Validation.HasErrors() || result.Accepted.Ref == nil || result.Accepted.Commit == nil {
		return Result{}, nil, errors.New("published clone accepted state is not definitively valid for the requested validation scope")
	}
	if _, err := hooks.ResolveStoreIdentity(root); err != nil {
		return Result{}, nil, errors.Join(err, errors.New("published clone identity cannot be proven"))
	}
	launcher, err := guard.Inspect(ctx, repository)
	if err != nil || launcher != guard.Unchanged {
		return Result{}, nil, errors.Join(err, errors.New("published clone guard differs"))
	}
	if excluded, err := hasCacheExclusion(repository.GitDir); err != nil || !excluded {
		return Result{}, nil, errors.Join(err, errors.New("published clone cache exclusion differs"))
	}
	return result, repository, nil
}

func installCacheExclusion(gitDirectory string) error {
	return gitpresent.InstallCacheExclusion(gitDirectory)
}

func hasCacheExclusion(gitDirectory string) (bool, error) {
	return gitpresent.HasCacheExclusion(gitDirectory)
}

func exclusionPresent(content []byte) bool {
	return gitpresent.ExclusionPresent(content)
}

func gitConfigAll(ctx context.Context, root, key string) ([]string, error) {
	output, status, err := gitOutputStatus(ctx, root, "config", "--local", "--get-all", key)
	if err != nil {
		return nil, err
	}
	if status == 1 {
		return []string{}, nil
	}
	if status != 0 {
		return nil, fmt.Errorf("git config exited %d", status)
	}
	if len(output) == 0 {
		return []string{""}, nil
	}
	lines := bytes.Split(bytes.TrimSuffix(output, []byte("\n")), []byte("\n"))
	values := make([]string, len(lines))
	for index, line := range lines {
		values[index] = string(line)
	}
	return values, nil
}

func gitOutput(ctx context.Context, root string, arguments ...string) ([]byte, error) {
	output, status, err := gitOutputStatus(ctx, root, arguments...)
	if err != nil {
		return nil, err
	}
	if status != 0 {
		return nil, fmt.Errorf("git exited %d", status)
	}
	return output, nil
}

func gitOutputStatus(ctx context.Context, root string, arguments ...string) ([]byte, int, error) {
	git, err := exec.LookPath("git")
	if err != nil {
		return nil, -1, err
	}
	global := []string{
		"-c", "core.longpaths=true",
		"--no-pager", "--no-optional-locks", "--no-replace-objects",
		"-c", "core.hooksPath=" + os.DevNull, "-c", "core.fsmonitor=false", "-c", "core.untrackedCache=false",
		"-c", "maintenance.auto=false", "-c", "gc.auto=0", "-C", root,
	}
	command := exec.CommandContext(ctx, git, append(global, arguments...)...)
	command.Env = isolatedEnvironment(os.Environ())
	output, err := command.Output()
	if err == nil {
		return output, 0, nil
	}
	if ctx.Err() != nil {
		return nil, -1, ctx.Err()
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return output, exit.ExitCode(), nil
	}
	return nil, -1, err
}

func isolatedEnvironment(environment []string) []string {
	result := make([]string, 0, len(environment)+8)
	for _, item := range environment {
		name, _, _ := strings.Cut(item, "=")
		upper := strings.ToUpper(name)
		if strings.HasPrefix(upper, "GIT_") || strings.HasPrefix(upper, "ENGRAM_") {
			continue
		}
		result = append(result, item)
	}
	return append(result,
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_COUNT=0",
		"GIT_NO_LAZY_FETCH=1", "GIT_OPTIONAL_LOCKS=0", "GIT_PAGER=cat",
	)
}

func bounded(value []byte) string {
	const limit = 16 << 10
	if len(value) > limit {
		value = value[:limit]
	}
	return strings.TrimSpace(string(value))
}

func cleanupPrivateStage(stage string) error {
	info, err := os.Lstat(stage)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.Join(err, errors.New("clone staging is not one real directory"))
	}
	parent, err := os.OpenRoot(filepath.Dir(stage))
	if err != nil {
		return err
	}
	removeErr := parent.RemoveAll(filepath.Base(stage))
	closeErr := parent.Close()
	if removeErr != nil || closeErr != nil {
		return errors.Join(removeErr, closeErr)
	}
	return syncDirectory(filepath.Dir(stage))
}

func syncDirectory(name string) error {
	_, err := syncDirectoryEffect(name)
	return err
}

// syncDirectoryEffect distinguishes a visible rename from one whose parent
// directory entry is known durable. Once Sync succeeds, a later Close error
// does not erase that durability evidence.
func syncDirectoryEffect(name string) (bool, error) {
	if runtime.GOOS == "windows" {
		return true, nil
	}
	directory, err := os.Open(name)
	if err != nil {
		return false, err
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	if syncErr != nil {
		return false, errors.Join(syncErr, closeErr)
	}
	return true, closeErr
}

func acquisitionResidual(target string) bool {
	_, err := os.Lstat(lifecycle.Sidecar(target, lifecycle.Acquisition))
	return !errors.Is(err, os.ErrNotExist)
}

func typed(kind ErrorKind, operation string, err error) error {
	if err == nil {
		err = errors.New("unknown acquisition failure")
	}
	return &Error{Kind: kind, Op: operation, Err: err}
}

func mutationError(kind ErrorKind, operation string, err error, durable, checkoutChanged, recoveryRequired bool, accepted *managedread.GitState) error {
	if err == nil {
		err = errors.New("unknown acquisition mutation failure")
	}
	return &Error{
		Kind: kind, Op: operation, Err: err,
		Mutation: &Mutation{
			Durable: durable, CheckoutChanged: checkoutChanged,
			RecoveryRequired: recoveryRequired, Accepted: cloneGitState(accepted),
		},
	}
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
	case errors.Is(err, lifecycle.ErrExists):
		return typed(ErrorConflict, operation, err)
	case errors.Is(err, lifecycle.ErrChanged):
		return typed(ErrorConcurrency, operation, err)
	case errors.Is(err, lifecycle.ErrMalformed):
		return typed(ErrorRecovery, operation, err)
	default:
		return typed(ErrorIO, operation, err)
	}
}

func cloneGitState(value *managedread.GitState) *managedread.GitState {
	if value == nil {
		return nil
	}
	result := *value
	result.Ref = stringPointer(value.Ref)
	result.Commit = stringPointer(value.Commit)
	return &result
}

func stringPointer(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func stringPtr(value string) *string {
	copy := value
	return &copy
}
