// Package acquire implements verified, publish-after-audit managed-store
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
	"sync"
	"unicode/utf8"

	"github.com/ontopix/engram/internal/checker"
	"github.com/ontopix/engram/internal/gitpresent"
	"github.com/ontopix/engram/internal/gitraw"
	"github.com/ontopix/engram/internal/guard"
	"github.com/ontopix/engram/internal/hooks"
	"github.com/ontopix/engram/internal/lifecycle"
	"github.com/ontopix/engram/internal/managedread"
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
}

type Result struct {
	Root            string                     `json:"root"`
	Remote          string                     `json:"remote"`
	Accepted        managedread.GitState       `json:"accepted"`
	Published       bool                       `json:"published"`
	Reused          bool                       `json:"reused"`
	VerifiedCommits int                        `json:"verified_commits"`
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
	Fault func(Phase) error
}

func New() *Cloner { return &Cloner{} }

func (c *Cloner) checkpoint(phase Phase) error {
	if c == nil || c.Fault == nil {
		return nil
	}
	return c.Fault(phase)
}

// Clone obtains location into an unpublished sibling staging directory,
// configures byte-transparent presentation, audits the complete accepted
// lineage, installs the owned raw-Git guard, and only then atomically publishes
// the checkout at its final path.
func Clone(ctx context.Context, location string, options Options) (Result, error) {
	return New().Run(ctx, location, options)
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
		return reuse(ctx, location, destination)
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
		return reuse(ctx, location, destination)
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return Result{}, typed(ErrorIO, "inspect clone destination", statErr)
	}
	handle, err := lifecycle.Begin(destination, lifecycle.Acquisition)
	if err != nil {
		if !errors.Is(err, lifecycle.ErrExists) {
			if _, _, stateErr := lifecycle.Read(destination, lifecycle.Acquisition); stateErr == nil {
				return Result{}, mutationError(ErrorRecovery, "begin clone lifecycle", err, false, false, true, nil)
			}
		}
		return Result{}, classify("begin clone lifecycle", err)
	}
	staging, err := lifecycle.Stage(handle.State())
	if err != nil {
		_ = handle.Remove()
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
			resultErr = mutationError(ErrorRecovery, "clean pre-publication clone", errors.Join(resultErr, stageErr, stateErr), false, false, true, nil)
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
	result, repository, err = verify(ctx, checkout)
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
		// RequireCleanup may have durably replaced the state before its final
		// directory sync failed. Retain the exact stage in every error case so
		// recovery can make the observation again without guessing.
		cleanupRunning = false
		return result, mutationError(ErrorRecovery, "advance clone lifecycle", err, false, false, true, nil)
	}
	cleanupRunning = false
	if err := c.checkpoint(PhaseCleanupRequired); err != nil {
		return result, mutationError(ErrorRecovery, "fault before clone publication", err, true, false, true, nil)
	}
	if _, err := os.Lstat(destination); err == nil {
		return result, mutationError(ErrorConcurrency, "publish clone", errors.New("destination appeared concurrently"), true, false, true, nil)
	} else if !errors.Is(err, os.ErrNotExist) {
		return result, mutationError(ErrorIO, "publish clone", err, true, false, true, nil)
	}
	if err := os.Rename(checkout, destination); err != nil {
		return result, mutationError(ErrorIO, "publish clone", err, true, false, true, nil)
	}
	result.Published = true
	if err := c.checkpoint(PhasePublished); err != nil {
		return result, mutationError(ErrorRecovery, "fault after clone publication", err, false, true, true, &result.Accepted)
	}
	if err := syncDirectory(filepath.Dir(destination)); err != nil {
		return result, mutationError(ErrorRecovery, "durably publish clone", err, false, true, true, &result.Accepted)
	}
	if err := c.checkpoint(PhaseDurable); err != nil {
		return result, mutationError(ErrorRecovery, "fault after durable clone publication", err, true, true, true, &result.Accepted)
	}
	if _, err := verifyPublished(ctx, destination, plan); err != nil {
		return result, mutationError(ErrorRecovery, "verify published clone", err, true, true, true, &result.Accepted)
	}
	if err := cleanupPrivateStage(staging); err != nil {
		return result, mutationError(ErrorRecovery, "clean clone staging", err, true, true, true, &result.Accepted)
	}
	if err := c.checkpoint(PhaseStageCleaned); err != nil {
		return result, mutationError(ErrorRecovery, "fault after clone stage cleanup", err, true, true, true, &result.Accepted)
	}
	if err := handle.Remove(); err != nil {
		return result, mutationError(ErrorRecovery, "clean clone lifecycle", err, true, true, true, &result.Accepted)
	}
	if err := c.checkpoint(PhaseCleaned); err != nil {
		return result, mutationError(ErrorIO, "fault after clone cleanup", err, true, true, false, &result.Accepted)
	}
	return result, nil
}

const maxPublicationPlanBytes = 8 << 20

type publicationPlan struct {
	Version  int    `json:"version"`
	Target   string `json:"target"`
	Location string `json:"location"`
	Remote   string `json:"remote"`
	Ref      string `json:"ref"`
	Commit   string `json:"commit"`
}

func makePublicationPlan(result Result, location string) (publicationPlan, error) {
	if result.Accepted.Ref == nil || result.Accepted.Commit == nil {
		return publicationPlan{}, errors.New("verified clone has no accepted branch tip")
	}
	plan := publicationPlan{
		Version: 1, Target: result.Root, Location: location, Remote: result.Remote,
		Ref: *result.Accepted.Ref, Commit: *result.Accepted.Commit,
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

func readPublicationPlan(stage string, state lifecycle.State) (publicationPlan, error) {
	name := publicationPlanPath(stage)
	before, err := os.Lstat(name)
	if err != nil || before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() {
		return publicationPlan{}, errors.Join(err, errors.New("clone publication plan is unavailable"))
	}
	file, err := os.Open(name)
	if err != nil {
		return publicationPlan{}, err
	}
	data, readErr := io.ReadAll(io.LimitReader(file, maxPublicationPlanBytes+1))
	opened, statErr := file.Stat()
	closeErr := file.Close()
	if readErr != nil || statErr != nil || closeErr != nil {
		return publicationPlan{}, errors.Join(readErr, statErr, closeErr)
	}
	after, lstatErr := os.Lstat(name)
	if lstatErr != nil || after.Mode()&os.ModeSymlink != 0 || !after.Mode().IsRegular() || !os.SameFile(before, opened) || !os.SameFile(opened, after) {
		return publicationPlan{}, errors.Join(lstatErr, errors.New("clone publication plan changed concurrently"))
	}
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
	Needed    bool                  `json:"needed"`
	Performed bool                  `json:"performed"`
	Published bool                  `json:"published"`
	Durable   bool                  `json:"durable"`
	Accepted  *managedread.GitState `json:"accepted"`
}

var recoveryLocks sync.Map

// Recover reconciles one exact target-derived acquisition state. Running
// state can only own private staging. Cleanup-required state either owns an
// unpublished exact stage or names an already-published verified checkout.
func Recover(ctx context.Context, target string) (*RecoveryResult, error) {
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
	lockValue, _ := recoveryLocks.LoadOrStore(canonical, &sync.Mutex{})
	lock := lockValue.(*sync.Mutex)
	lock.Lock()
	defer lock.Unlock()
	state, _, err := lifecycle.Read(canonical, lifecycle.Acquisition)
	if errors.Is(err, os.ErrNotExist) {
		return &RecoveryResult{}, nil
	}
	if err != nil {
		return &RecoveryResult{Needed: true}, mutationError(ErrorRecovery, "read clone lifecycle", err, false, false, true, nil)
	}
	handle, err := lifecycle.Adopt(canonical, lifecycle.Acquisition)
	if err != nil {
		kind := ErrorRecovery
		if errors.Is(err, lifecycle.ErrOwnerLive) {
			kind = ErrorConcurrency
		}
		return &RecoveryResult{Needed: true}, mutationError(kind, "adopt clone lifecycle", err, false, false, true, nil)
	}
	stage, err := lifecycle.Stage(state)
	if err != nil {
		return &RecoveryResult{Needed: true}, mutationError(ErrorRecovery, "derive clone recovery stage", err, false, false, true, nil)
	}
	if state.Phase == lifecycle.Running {
		if err := ctx.Err(); err != nil {
			return &RecoveryResult{Needed: true}, mutationError(ErrorCancelled, "cancel clone recovery", err, false, false, true, nil)
		}
		if err := cleanupPrivateStage(stage); err != nil {
			return &RecoveryResult{Needed: true}, mutationError(ErrorRecovery, "clean running clone stage", err, false, false, true, nil)
		}
		if err := removeRecoveredLifecycle(handle, canonical); err != nil {
			return &RecoveryResult{Needed: true}, mutationError(ErrorRecovery, "clean running clone lifecycle", err, true, false, true, nil)
		}
		return &RecoveryResult{Needed: true, Performed: true, Durable: true}, nil
	}

	stageStorePresent, err := inspectStagedStore(stage)
	if err != nil {
		return &RecoveryResult{Needed: true}, mutationError(ErrorRecovery, "inspect clone recovery stage", err, false, false, true, nil)
	}
	if stageStorePresent {
		// Atomic rename has not consumed the exact source. The target is never
		// touched, even when an unrelated path appeared concurrently.
		if err := ctx.Err(); err != nil {
			return &RecoveryResult{Needed: true}, mutationError(ErrorCancelled, "cancel clone recovery", err, false, false, true, nil)
		}
		if err := cleanupPrivateStage(stage); err != nil {
			return &RecoveryResult{Needed: true}, mutationError(ErrorRecovery, "cancel unpublished clone", err, false, false, true, nil)
		}
		if err := removeRecoveredLifecycle(handle, canonical); err != nil {
			return &RecoveryResult{Needed: true}, mutationError(ErrorRecovery, "clean unpublished clone lifecycle", err, true, false, true, nil)
		}
		return &RecoveryResult{Needed: true, Performed: true, Durable: true}, nil
	}

	info, statErr := os.Lstat(canonical)
	if errors.Is(statErr, os.ErrNotExist) {
		if err := ctx.Err(); err != nil {
			return &RecoveryResult{Needed: true}, mutationError(ErrorCancelled, "cancel clone recovery", err, false, false, true, nil)
		}
		if err := cleanupPrivateStage(stage); err != nil {
			return &RecoveryResult{Needed: true}, mutationError(ErrorRecovery, "clean unpublished clone stage", err, false, false, true, nil)
		}
		if err := removeRecoveredLifecycle(handle, canonical); err != nil {
			return &RecoveryResult{Needed: true}, mutationError(ErrorRecovery, "clean unpublished clone lifecycle", err, true, false, true, nil)
		}
		return &RecoveryResult{Needed: true, Performed: true, Durable: true}, nil
	}
	if statErr != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return &RecoveryResult{Needed: true}, mutationError(ErrorConflict, "inspect published clone", errors.Join(statErr, errors.New("clone target is not one real directory")), false, true, true, nil)
	}
	plan, planErr := readPublicationPlan(stage, state)
	var accepted *managedread.GitState
	if planErr == nil {
		accepted, err = verifyPublished(ctx, canonical, plan)
	} else {
		// A successfully swept stage can leave the sidecar as the last cleanup
		// action. In that exact case the target itself is sufficient evidence.
		if _, stageErr := os.Lstat(stage); errors.Is(stageErr, os.ErrNotExist) {
			accepted, err = verifyPublishedWithoutPlan(ctx, canonical)
		} else {
			return &RecoveryResult{Needed: true}, mutationError(ErrorRecovery, "read clone publication plan", planErr, false, true, true, nil)
		}
	}
	if err != nil {
		if ctx.Err() != nil {
			return &RecoveryResult{Needed: true}, mutationError(ErrorCancelled, "cancel clone recovery", ctx.Err(), false, true, true, nil)
		}
		return &RecoveryResult{Needed: true}, mutationError(ErrorConflict, "verify published clone", err, false, true, true, nil)
	}
	if err := ctx.Err(); err != nil {
		return &RecoveryResult{Needed: true, Published: true, Accepted: cloneGitState(accepted)}, mutationError(ErrorCancelled, "cancel clone recovery", err, false, true, true, accepted)
	}
	if err := cleanupPrivateStage(stage); err != nil {
		return &RecoveryResult{Needed: true, Published: true, Accepted: cloneGitState(accepted)}, mutationError(ErrorRecovery, "clean published clone stage", err, true, true, true, accepted)
	}
	// Re-observe after private cleanup. A concurrently replaced target remains
	// untouched and retains the lifecycle record for explicit resolution.
	if planErr == nil {
		accepted, err = verifyPublished(ctx, canonical, plan)
	} else {
		accepted, err = verifyPublishedWithoutPlan(ctx, canonical)
	}
	if err != nil {
		return &RecoveryResult{Needed: true, Published: true}, mutationError(ErrorConcurrency, "recheck published clone", err, true, true, true, nil)
	}
	if err := removeRecoveredLifecycle(handle, canonical); err != nil {
		return &RecoveryResult{Needed: true, Published: true, Accepted: cloneGitState(accepted)}, mutationError(ErrorRecovery, "clean published clone lifecycle", err, true, true, true, accepted)
	}
	return &RecoveryResult{Needed: true, Performed: true, Published: true, Durable: true, Accepted: cloneGitState(accepted)}, nil
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

func removeRecoveredLifecycle(handle *lifecycle.Handle, target string) error {
	err := handle.Remove()
	if err == nil {
		return nil
	}
	if _, _, readErr := lifecycle.Read(target, lifecycle.Acquisition); errors.Is(readErr, os.ErrNotExist) {
		// A concurrent recovery completed the exact deletion. The operation is
		// idempotently satisfied; an unexpected replacement remains an error.
		return nil
	}
	return err
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

func reuse(ctx context.Context, location, destination string) (Result, error) {
	if _, _, err := lifecycle.Read(destination, lifecycle.Acquisition); err == nil {
		return Result{}, typed(ErrorConflict, "reuse default clone", errors.New("existing clone has active or recoverable acquisition state"))
	} else if !errors.Is(err, os.ErrNotExist) {
		return Result{}, typed(ErrorConflict, "reuse default clone", errors.New("existing clone acquisition state is inconsistent"))
	}
	result, repository, err := verify(ctx, destination)
	if err != nil {
		return Result{}, typed(ErrorConflict, "reuse default clone", err)
	}
	if result.Validation.Status != checker.StatusComplete || result.Validation.HasErrors() {
		return Result{}, typed(ErrorConflict, "reuse default clone", errors.New("existing clone no longer has a conforming accepted lineage"))
	}
	if err := verifyOriginAndUpstream(ctx, destination, location, repository.HeadRef); err != nil {
		return Result{}, typed(ErrorConflict, "reuse default clone", err)
	}
	if _, err := hooks.ResolveStoreIdentity(destination); err != nil {
		return Result{}, typed(ErrorConflict, "reuse default clone identity", err)
	}
	launcher, err := guard.Inspect(ctx, repository)
	if err != nil || launcher != guard.Unchanged {
		return Result{}, typed(ErrorConflict, "reuse default clone guard", err)
	}
	if ok, err := hasCacheExclusion(repository.GitDir); err != nil || !ok {
		return Result{}, typed(ErrorConflict, "reuse default clone cache exclusion", err)
	}
	result.Root = destination
	result.Remote = "origin"
	result.Launcher = launcher
	result.Reused = true
	return result, nil
}

func verify(ctx context.Context, root string) (Result, *gitraw.Repository, error) {
	store, err := managedread.Open(ctx, root)
	if err != nil {
		return Result{}, nil, typed(ErrorRepository, "open cloned managed store", err)
	}
	audit, err := store.AuditAccepted(ctx)
	if err != nil {
		return Result{}, nil, typed(ErrorRepository, "audit cloned managed store", err)
	}
	repository := store.Repository()
	accepted := managedread.GitState{Ref: stringPtr(repository.HeadRef)}
	if repository.Head != nil {
		accepted.Commit = stringPtr(repository.Head.String())
	}
	result := Result{
		Root: repository.Root, Remote: "origin", Accepted: accepted,
		VerifiedCommits: len(audit.Audits), Validation: audit.Validation,
		Audits: append([]managedread.HistoryAudit(nil), audit.Audits...),
	}
	return result, repository, nil
}

func runClone(ctx context.Context, location, destination string) error {
	git, err := exec.LookPath("git")
	if err != nil {
		return typed(ErrorCapability, "locate Git", err)
	}
	arguments := []string{
		"--no-pager", "--no-optional-locks", "--no-replace-objects",
		"-c", "core.hooksPath=" + os.DevNull,
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
	result, repository, err := verifyPublishedStore(ctx, root)
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

func verifyPublishedWithoutPlan(ctx context.Context, root string) (*managedread.GitState, error) {
	result, repository, err := verifyPublishedStore(ctx, root)
	if err != nil {
		return nil, err
	}
	urls, err := gitConfigAll(ctx, root, "remote.origin.url")
	if err != nil || len(urls) != 1 || urls[0] == "" {
		return nil, errors.Join(err, errors.New("published clone origin is unavailable"))
	}
	if err := verifyOriginAndUpstream(ctx, root, urls[0], repository.HeadRef); err != nil {
		return nil, err
	}
	return cloneGitState(&result.Accepted), nil
}

func verifyPublishedStore(ctx context.Context, root string) (Result, *gitraw.Repository, error) {
	result, repository, err := verify(ctx, root)
	if err != nil {
		return Result{}, nil, err
	}
	if result.Validation.Status != checker.StatusComplete || result.Validation.HasErrors() || result.Accepted.Ref == nil || result.Accepted.Commit == nil {
		return Result{}, nil, errors.New("published clone accepted history is not definitively valid")
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
	global := []string{"--no-pager", "--no-optional-locks", "--no-replace-objects", "-c", "core.hooksPath=" + os.DevNull, "-C", root}
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
	if runtime.GOOS == "windows" {
		return nil
	}
	directory, err := os.Open(name)
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
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
