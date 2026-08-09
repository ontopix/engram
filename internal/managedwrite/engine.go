package managedwrite

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/ontopix/engram/internal/changeset"
	"github.com/ontopix/engram/internal/checker"
	"github.com/ontopix/engram/internal/gitraw"
	"github.com/ontopix/engram/internal/guard"
	"github.com/ontopix/engram/internal/hookexec"
	"github.com/ontopix/engram/internal/journal"
	"github.com/ontopix/engram/internal/managedread"
	"github.com/ontopix/engram/internal/rendezvous"
	"github.com/ontopix/engram/internal/treeimage"
)

// Commit accepts the complete eligible live index.
func (e *Engine) Commit(ctx context.Context, request Request) (*Result, error) {
	return e.commit(ctx, commitInput{store: request.Store, message: request.Message, dryRun: request.DryRun})
}

// CommitImage accepts a caller-supplied sealed logical candidate while using
// the live index only as the exact reconciliation preimage. This is intended
// for bounded init/revert workflows; ordinary commit must use Commit.
func (e *Engine) CommitImage(ctx context.Context, request ImageRequest) (*Result, error) {
	if request.Candidate == nil || request.Candidate.Tree == nil {
		return nil, typed(FailureUsage, PhaseCaptured, fmt.Errorf("%w: image candidate is unavailable", ErrUsage))
	}
	return e.commit(ctx, commitInput{
		store: request.Store, message: request.Message, dryRun: request.DryRun,
		candidate: request.Candidate, modes: request.Modes, requireClean: request.RequireClean,
		requireBase: request.RequireBase, expectedBase: request.ExpectedBase,
	})
}

type commitInput struct {
	store        string
	message      string
	dryRun       bool
	candidate    *checker.Snapshot
	modes        map[string]gitraw.TreeMode
	requireClean bool
	requireBase  bool
	expectedBase *string
}

func (e *Engine) commit(ctx context.Context, input commitInput) (result *Result, resultErr error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if e == nil || input.store == "" {
		return nil, typed(FailureUsage, PhaseCaptured, ErrUsage)
	}
	opened, err := managedread.Open(ctx, input.store)
	if err != nil {
		return nil, classify(PhaseCaptured, err)
	}
	discovered := opened.Repository()
	if err := ensureNoRecovery(discovered.GitDir); err != nil {
		return nil, err
	}

	var lock *rendezvous.Handle
	if !input.dryRun {
		lock, err = rendezvous.AcquireWriter(discovered.CommonGitDir, discovered.GitDir, discovered.HeadRef)
		if err != nil {
			return nil, classify(PhaseLocked, err)
		}
		e.markActive(lock.Owner().Token, true)
		defer func() {
			if lock == nil {
				return
			}
			e.markActive(lock.Owner().Token, false)
			if releaseErr := lock.Release(); releaseErr != nil {
				result = nil
				resultErr = errors.Join(resultErr, typed(FailureIO, PhaseLocksReleased, releaseErr))
			}
		}()
		if err := e.checkpoint(PhaseLocked); err != nil {
			return nil, typed(FailureIO, PhaseLocked, err)
		}
		if err := ensureNoRecovery(discovered.GitDir); err != nil {
			return nil, err
		}
	}

	observation, err := stableRepository(ctx, discovered.Root)
	if err != nil {
		return nil, classify(PhaseCaptured, err)
	}
	if observation.repository.HeadRef != discovered.HeadRef || observation.repository.GitDir != discovered.GitDir || observation.repository.CommonGitDir != discovered.CommonGitDir {
		return nil, typed(FailureConcurrency, PhaseCaptured, ErrConcurrent)
	}
	if err := e.checkpoint(PhaseCaptured); err != nil {
		return nil, typed(FailureConcurrency, PhaseCaptured, err)
	}
	store, err := managedread.Open(ctx, observation.repository.Root)
	if err != nil {
		return nil, classify(PhaseCaptured, err)
	}
	if !sameRepository(store.Repository(), observation.repository) {
		return nil, typed(FailureConcurrency, PhaseCaptured, ErrConcurrent)
	}
	git, err := newGitClient(observation.repository.Root)
	if err != nil {
		return nil, typed(FailureCapability, PhaseCaptured, err)
	}

	base, err := store.Accepted(ctx)
	if err != nil {
		return nil, classify(PhaseAudited, err)
	}
	initialization := observation.repository.Head == nil
	if input.requireBase {
		if err := checkBaseExpectation(observation.repository, input.expectedBase); err != nil {
			return nil, err
		}
	}
	if !initialization {
		audit, err := store.AuditAccepted(ctx)
		if err != nil {
			return nil, classify(PhaseAudited, err)
		}
		if audit.Tip != observation.repository.Head.String() || audit.Validation.Status != checker.StatusComplete || audit.Validation.HasErrors() {
			validation := audit.Validation
			return resultForAudit(observation, audit.Validation), &Error{Kind: FailureValidation, Phase: PhaseAudited, Validation: &validation, Err: ErrValidation}
		}
	}
	if err := e.checkpoint(PhaseAudited); err != nil {
		return nil, typed(FailureIO, PhaseAudited, err)
	}

	var capturedIndexCleanup func() error
	var initial *managedread.SnapshotView
	var reconciliationInitial *managedread.SnapshotView
	if input.candidate == nil {
		indexPath, cleanup, err := initializeCapturedIndex(ctx, git, observation.repository, observation, e.TempRoot)
		if err != nil {
			return nil, classify(PhaseCaptured, err)
		}
		capturedIndexCleanup = cleanup
		defer func() {
			if capturedIndexCleanup != nil {
				resultErr = errors.Join(resultErr, capturedIndexCleanup())
			}
		}()
		initial, err = store.StagedFromIndex(ctx, indexPath)
		if err != nil {
			return nil, classify(PhaseCaptured, err)
		}
		for _, entry := range initial.Index {
			if entry.SkipWorktree {
				return nil, typedPaths(FailureRepository, PhaseCaptured, []string{entry.Path}, fmt.Errorf("skip-worktree index entry is not writable presentation"))
			}
		}
		reconciliationInitial = initial
	} else {
		liveIndexPath, liveIndexCleanup, err := initializeCapturedIndex(ctx, git, observation.repository, observation, e.TempRoot)
		if err != nil {
			return nil, classify(PhaseCaptured, err)
		}
		reconciliationInitial, err = store.StagedFromIndex(ctx, liveIndexPath)
		cleanupErr := liveIndexCleanup()
		if err != nil || cleanupErr != nil {
			return nil, classify(PhaseCaptured, errors.Join(err, cleanupErr))
		}
		if input.requireClean {
			if err := requireCleanState(ctx, store, base, reconciliationInitial); err != nil {
				return nil, err
			}
		}
		sealed, modes, cleanup, err := sealCandidate(input.candidate, input.modes, observation.repository.Root, e.TempRoot)
		if err != nil {
			return nil, classify(PhaseCaptured, err)
		}
		capturedIndexCleanup = cleanup
		initial = &managedread.SnapshotView{Snapshot: sealed, Modes: modes}
	}

	baseImage := make(treeimage.Image)
	if base.Snapshot != nil {
		baseImage, err = treeimage.FromSnapshot(base.Snapshot.Tree, base.Modes)
		if err != nil {
			return nil, classify(PhasePrepared, err)
		}
	}
	initialImage, err := treeimage.FromSnapshot(initial.Snapshot.Tree, initial.Modes)
	if err != nil {
		return nil, classify(PhasePrepared, err)
	}
	reconciliationInitialImage, err := treeimage.FromSnapshot(reconciliationInitial.Snapshot.Tree, reconciliationInitial.Modes)
	if err != nil {
		return nil, classify(PhaseProven, err)
	}
	if !initialization && treeimage.Equal(baseImage, initialImage) {
		if !input.dryRun {
			if err := requireGuard(ctx, observation.repository); err != nil {
				return nil, err
			}
		}
		return &Result{
			DryRun: input.dryRun, Created: false, Ref: observation.repository.HeadRef,
			Base: oidPointer(observation.repository.Head), Changes: []changeset.Change{},
		}, nil
	}

	if !input.dryRun {
		if err := requireGuard(ctx, observation.repository); err != nil {
			return nil, err
		}
		if err := validateMessage(input.message); err != nil {
			return nil, typed(FailureUsage, PhasePrepared, errors.Join(ErrUsage, err))
		}
	}
	if e.Hooks == nil {
		return nil, typed(FailureCapability, PhasePrepared, fmt.Errorf("%w: hook executor is unavailable", ErrCapability))
	}
	prepared, err := e.Hooks.Prepare(ctx, hookexec.Request{
		StoreRoot: observation.repository.Root, WorktreeRoot: observation.repository.Root,
		Base: base.Snapshot, Initial: initial.Snapshot, BaseModes: base.Modes,
		InitialModes: initial.Modes, Initialization: initialization,
	})
	if err != nil {
		return resultFromHook(observation, input.dryRun, initialization, err), classifyHook(PhasePrepared, err)
	}
	if err := e.checkpoint(PhasePrepared); err != nil {
		return resultFromPrepared(observation, input.dryRun, initialization, prepared), typed(FailureIO, PhasePrepared, err)
	}

	proof, err := provePreservation(ctx, git, observation, reconciliationInitialImage, prepared.Capture)
	if err != nil {
		return resultFromPrepared(observation, input.dryRun, initialization, prepared), classify(PhaseProven, err)
	}
	if err := e.checkpoint(PhaseProven); err != nil {
		return resultFromPrepared(observation, input.dryRun, initialization, prepared), typed(FailureIO, PhaseProven, err)
	}
	if input.requireClean {
		proof.cleanBase = baseImage
	}
	prospective := resultFromPrepared(observation, input.dryRun, initialization, prepared)
	if input.dryRun {
		if err := verifyFinalInputs(ctx, git, observation, proof); err != nil {
			return prospective, classify(PhaseFinalRecheck, err)
		}
		return prospective, nil
	}

	who, err := configuredIdentity(ctx, git)
	if err != nil {
		return prospective, typed(FailureUsage, PhaseObjectsWritten, errors.Join(ErrUsage, err))
	}
	finalIndexPath, finalIndexBytes, treeID, finalCleanup, err := createCandidateIndex(ctx, git, observation.repository, prepared.Capture, e.TempRoot)
	if err != nil {
		return prospective, classify(PhaseObjectsWritten, err)
	}
	defer func() {
		if finalCleanup != nil {
			resultErr = errors.Join(resultErr, finalCleanup())
		}
	}()
	projectedFinal, err := store.StagedFromIndex(ctx, finalIndexPath)
	if err != nil || !sameProjectedImage(projectedFinal, prepared.Capture) {
		return prospective, typed(FailureCapability, PhaseObjectsWritten, errors.Join(ErrCapability, err, errors.New("final raw index does not project to the sealed candidate")))
	}
	commitID, err := createCommit(ctx, git, observation.repository, treeID, observation.repository.Head, who, e.now(), input.message)
	if err != nil {
		return prospective, classify(PhaseObjectsWritten, err)
	}
	if err := verifyCreatedCommit(ctx, observation.repository, commitID, treeID, input.message); err != nil {
		return prospective, typed(FailureCapability, PhaseObjectsWritten, err)
	}
	if err := e.checkpoint(PhaseObjectsWritten); err != nil {
		return prospective, typed(FailureIO, PhaseObjectsWritten, err)
	}
	if capturedIndexCleanup != nil {
		if err := capturedIndexCleanup(); err != nil {
			return prospective, typed(FailureIO, PhaseObjectsWritten, err)
		}
		capturedIndexCleanup = nil
	}
	if finalCleanup != nil {
		if err := finalCleanup(); err != nil {
			return prospective, typed(FailureIO, PhaseObjectsWritten, err)
		}
		finalCleanup = nil
	}

	before := oidPointer(observation.repository.Head)
	record := journal.Record{
		OwnerToken:   lock.Owner().Token,
		Owner:        journal.OwnerIdentity{PID: lock.Owner().PID, Hostname: lock.Owner().Hostname, StartedAt: lock.Owner().StartedAt},
		ObjectFormat: observation.repository.Format,
		Ref:          journal.RefUpdate{Ref: observation.repository.HeadRef, Before: before, After: commitID},
		IndexBefore:  journal.RawFileImage{Present: observation.indexExists, Data: append([]byte(nil), observation.indexBytes...)},
		IndexAfter:   journal.RawFileImage{Present: true, Data: append([]byte(nil), finalIndexBytes...)},
		Paths:        append([]journal.PathUpdate(nil), proof.paths...), Fingerprints: append([]journal.Fingerprint(nil), proof.fingerprints...),
	}
	journal.Sort(&record)
	journalPath := journal.Path(observation.repository.GitDir)
	if err := journal.WritePending(journalPath, record); err != nil {
		return prospective, classify(PhaseJournalPending, err)
	}
	_, journalBytes, err := journal.Read(journalPath)
	if err != nil {
		return prospective, typed(FailureRecovery, PhaseJournalPending, err)
	}
	if err := e.checkpoint(PhaseJournalPending); err != nil {
		return prospective, e.cancelPending(journalPath, journalBytes, &lock, PhaseJournalPending, err)
	}
	if err := lock.SetPhase(rendezvous.JournalRequired); err != nil {
		return prospective, &Error{Kind: FailureRecovery, Phase: PhaseJournalRequired, Err: err, Commit: commitID}
	}
	if err := e.checkpoint(PhaseJournalRequired); err != nil {
		return prospective, e.cancelPending(journalPath, journalBytes, &lock, PhaseJournalRequired, err)
	}
	outcome, casErr := updateRefPreparedCAS(ctx, git, observation.repository, commitID, observation.repository.Head, func() error {
		if err := verifyFinalInputs(ctx, git, observation, proof); err != nil {
			return err
		}
		return e.checkpoint(PhaseFinalRecheck)
	})
	switch outcome {
	case casRejected:
		return prospective, e.cancelPending(journalPath, journalBytes, &lock, PhaseRefUpdated, casErr)
	case casUnknown:
		e.markActive(lock.Owner().Token, false)
		retained := lock
		lock = nil
		_ = retained
		return prospective, &Error{Kind: FailureRecovery, Phase: PhaseRefUpdated, UnknownCAS: true, Commit: commitID, Err: errors.Join(ErrCASUnknown, casErr)}
	}
	// Once CAS is known to have committed, caller cancellation must not abandon
	// the bounded local reconciliation. Use an internal deadline so cleanup is
	// persistent but can still fail into explicit recovery-required state.
	cleanupCtx, cancelCleanup := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancelCleanup()
	if err := e.checkpoint(PhaseRefUpdated); err != nil {
		return prospective, e.postCASError(&lock, PhaseRefUpdated, commitID, err)
	}
	if err := reconcileIndex(cleanupCtx, observation.repository.Root, record); err != nil {
		return prospective, e.postCASError(&lock, PhaseIndexReconciled, commitID, err)
	}
	if err := e.checkpoint(PhaseIndexReconciled); err != nil {
		return prospective, e.postCASError(&lock, PhaseIndexReconciled, commitID, err)
	}
	if err := reconcileWorktree(observation.repository.Root, record); err != nil {
		return prospective, e.postCASError(&lock, PhaseWorktreeReconciled, commitID, err)
	}
	if err := e.checkpoint(PhaseWorktreeReconciled); err != nil {
		return prospective, e.postCASError(&lock, PhaseWorktreeReconciled, commitID, err)
	}
	completeBytes, err := journal.SetState(journalPath, journalBytes, journal.Complete)
	if err != nil {
		return prospective, e.postCASError(&lock, PhaseJournalComplete, commitID, err)
	}
	if err := e.checkpoint(PhaseJournalComplete); err != nil {
		return prospective, e.postCASError(&lock, PhaseJournalComplete, commitID, err)
	}
	ownerToken := lock.Owner().Token
	if err := lock.Release(); err != nil {
		e.markActive(ownerToken, false)
		lock = nil
		return prospective, &Error{Kind: FailureRecovery, Phase: PhaseLocksReleased, Accepted: true, Commit: commitID, Err: errors.Join(ErrPostCAS, err)}
	}
	e.markActive(ownerToken, false)
	lock = nil
	if err := e.checkpoint(PhaseLocksReleased); err != nil {
		return prospective, &Error{Kind: FailureRecovery, Phase: PhaseLocksReleased, Accepted: true, Commit: commitID, Err: errors.Join(ErrPostCAS, err)}
	}
	if err := journal.Remove(journalPath, completeBytes); err != nil {
		return prospective, &Error{Kind: FailureRecovery, Phase: PhaseJournalRemoved, Accepted: true, Commit: commitID, Err: errors.Join(ErrPostCAS, err)}
	}
	if err := e.checkpoint(PhaseJournalRemoved); err != nil {
		return prospective, &Error{Kind: FailureIO, Phase: PhaseJournalRemoved, Accepted: true, Commit: commitID, Err: err}
	}
	prospective.Created = true
	prospective.Commit = stringPointer(commitID)
	return prospective, nil
}

func checkBaseExpectation(repository *gitraw.Repository, expected *string) error {
	if repository == nil {
		return typed(FailureRepository, PhaseCaptured, ErrRepository)
	}
	if expected == nil {
		if repository.Head != nil {
			return typed(FailureConcurrency, PhaseCaptured, fmt.Errorf("%w: expected an unborn accepted ref", ErrConcurrent))
		}
		return nil
	}
	if _, err := gitraw.ParseOID(repository.Format, *expected); err != nil {
		return typed(FailureUsage, PhaseCaptured, errors.Join(ErrUsage, err))
	}
	if repository.Head == nil || repository.Head.String() != *expected {
		return typed(FailureConcurrency, PhaseCaptured, fmt.Errorf("%w: accepted base does not match expectation", ErrConcurrent))
	}
	return nil
}

func requireCleanState(ctx context.Context, store *managedread.Store, base, staged *managedread.SnapshotView) error {
	if staged == nil || staged.Snapshot == nil || !changeset.PreflightOK(staged.Snapshot.Tree) {
		return typed(FailureRepository, PhaseCaptured, fmt.Errorf("clean precondition has an ineligible index"))
	}
	baseImage := make(treeimage.Image)
	var err error
	if base != nil && base.Snapshot != nil {
		baseImage, err = treeimage.FromSnapshot(base.Snapshot.Tree, base.Modes)
		if err != nil {
			return classify(PhaseCaptured, err)
		}
	}
	stagedImage, err := treeimage.FromSnapshot(staged.Snapshot.Tree, staged.Modes)
	if err != nil || !treeimage.Equal(baseImage, stagedImage) {
		return typed(FailureConcurrency, PhaseCaptured, fmt.Errorf("%w: logical index is not clean", ErrConcurrent))
	}
	working, err := store.Working(ctx)
	if err != nil {
		return classify(PhaseCaptured, err)
	}
	if working.Snapshot == nil || working.Snapshot.Validation.Status != checker.StatusComplete || working.Snapshot.Validation.HasErrors() || !changeset.PreflightOK(working.Snapshot.Tree) {
		return typed(FailureConcurrency, PhaseCaptured, fmt.Errorf("%w: logical worktree is not clean", ErrConcurrent))
	}
	workingImage, err := treeimage.FromSnapshot(working.Snapshot.Tree, base.Modes)
	if err != nil || !treeimage.Equal(baseImage, workingImage) {
		return typed(FailureConcurrency, PhaseCaptured, fmt.Errorf("%w: logical worktree is not clean", ErrConcurrent))
	}
	return nil
}

func ensureNoRecovery(gitDir string) error {
	name := journal.Path(gitDir)
	if _, err := os.Lstat(name); err == nil {
		return typed(FailureRecovery, PhaseCaptured, ErrRecovery)
	} else if !errors.Is(err, os.ErrNotExist) {
		return typed(FailureRecovery, PhaseCaptured, err)
	}
	return nil
}

func requireGuard(ctx context.Context, repository *gitraw.Repository) error {
	state, err := guard.Inspect(ctx, repository)
	if err != nil || state != guard.Unchanged {
		return typed(FailureGuard, PhasePrepared, errors.Join(ErrGuard, err))
	}
	return nil
}

func sealCandidate(candidate *checker.Snapshot, modes map[string]gitraw.TreeMode, worktree, tempRoot string) (*checker.Snapshot, map[string]gitraw.TreeMode, func() error, error) {
	image, err := treeimage.FromSnapshot(candidate.Tree, modes)
	if err != nil {
		return nil, nil, nil, err
	}
	parent, err := privateTempDir(tempRoot, worktree, "engram-managed-overlay-")
	if err != nil {
		return nil, nil, nil, err
	}
	cleanup := func() error { return os.RemoveAll(parent) }
	root := filepath.Join(parent, "candidate")
	if err := treeimage.Materialize(root, image, false); err != nil {
		_ = cleanup()
		return nil, nil, nil, err
	}
	sealedImage, err := treeimage.Capture(root, true)
	if err != nil || !treeimage.Equal(image, sealedImage) {
		_ = cleanup()
		return nil, nil, nil, errors.Join(err, ErrConcurrent)
	}
	sealed, err := checker.CheckFS(root)
	if err != nil {
		_ = cleanup()
		return nil, nil, nil, err
	}
	modeCopy := make(map[string]gitraw.TreeMode, len(modes))
	for name, mode := range modes {
		modeCopy[name] = mode
	}
	return sealed, modeCopy, cleanup, nil
}

func verifyCreatedCommit(ctx context.Context, repository *gitraw.Repository, commitID, treeID, message string) error {
	oid, err := gitraw.ParseOID(repository.Format, commitID)
	if err != nil {
		return err
	}
	object, err := repository.ReadObject(ctx, oid)
	if err != nil {
		return err
	}
	if object.Type != gitraw.TypeCommit {
		return fmt.Errorf("created object has type %s", object.Type)
	}
	commit, err := gitraw.ParseCommit(repository.Format, object.Data)
	if err != nil {
		return err
	}
	if commit.Tree.String() != treeID || len(commit.Parents) > 1 || repository.Head == nil != (len(commit.Parents) == 0) || repository.Head != nil && !commit.Parents[0].Equal(*repository.Head) || !bytes.Equal(commit.Message, []byte(message+"\n")) {
		return fmt.Errorf("created commit does not exactly encode the managed candidate")
	}
	return nil
}

func (e *Engine) cancelPending(name string, pending []byte, lock **rendezvous.Handle, phase Phase, cause error) error {
	cancelled, err := journal.SetState(name, pending, journal.Cancelled)
	if err != nil {
		if *lock != nil {
			e.markActive((*lock).Owner().Token, false)
			*lock = nil
		}
		return &Error{Kind: FailureRecovery, Phase: phase, Err: errors.Join(cause, err)}
	}
	if *lock == nil {
		return &Error{Kind: FailureRecovery, Phase: phase, Err: errors.Join(cause, ErrRecovery)}
	}
	token := (*lock).Owner().Token
	if err := (*lock).Release(); err != nil {
		e.markActive(token, false)
		*lock = nil
		return &Error{Kind: FailureRecovery, Phase: phase, Err: errors.Join(cause, err)}
	}
	e.markActive(token, false)
	*lock = nil
	if err := journal.Remove(name, cancelled); err != nil {
		return &Error{Kind: FailureRecovery, Phase: phase, Err: errors.Join(cause, err)}
	}
	return classify(phase, cause)
}

func (e *Engine) postCASError(lock **rendezvous.Handle, phase Phase, commitID string, cause error) error {
	if *lock != nil {
		e.markActive((*lock).Owner().Token, false)
		*lock = nil
	}
	return &Error{Kind: FailureRecovery, Phase: phase, Accepted: true, Commit: commitID, Err: errors.Join(ErrPostCAS, cause)}
}

func classify(phase Phase, err error) error {
	if err == nil {
		return nil
	}
	var typedError *Error
	if errors.As(err, &typedError) {
		return err
	}
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return typed(FailureCapability, phase, err)
	case errors.Is(err, rendezvous.ErrBusy), errors.Is(err, managedread.ErrConcurrent), errors.Is(err, ErrConcurrent):
		return typed(FailureConcurrency, phase, errors.Join(ErrConcurrent, err))
	case errors.Is(err, journal.ErrExists), errors.Is(err, journal.ErrChanged):
		return typed(FailureRecovery, phase, errors.Join(ErrRecovery, err))
	}
	var indexError *managedread.IndexError
	if errors.As(err, &indexError) {
		return typedPaths(FailureRepository, phase, indexError.Paths, err)
	}
	var rawError *gitraw.Error
	if errors.As(err, &rawError) {
		if rawError.Kind == gitraw.FailureCapability || rawError.Kind == gitraw.FailureMissing {
			return typed(FailureCapability, phase, err)
		}
		return typed(FailureRepository, phase, err)
	}
	return typed(FailureIO, phase, err)
}

func classifyHook(phase Phase, err error) error {
	kind := FailureHook
	switch hookexec.KindOf(err) {
	case hookexec.ErrorTrust:
		kind = FailureTrust
	case hookexec.ErrorCapability:
		kind = FailureCapability
	case hookexec.ErrorConcurrency:
		kind = FailureConcurrency
	}
	var hookError *hookexec.Error
	if errors.As(err, &hookError) {
		return &Error{Kind: kind, Phase: phase, Validation: hookError.Validation, Err: err}
	}
	return typed(kind, phase, err)
}

func resultFromPrepared(observation *repositoryObservation, dryRun, initialization bool, prepared *hookexec.Result) *Result {
	validation := prepared.Validation
	validation.Findings = append([]checker.Finding{}, prepared.Validation.Findings...)
	return &Result{
		DryRun: dryRun, Ref: observation.repository.HeadRef, Base: oidPointer(observation.repository.Head),
		Initialization: initialization, Changes: append([]changeset.Change{}, prepared.Changes...),
		Validation: &validation, HookSetSHA256: prepared.SetSHA256,
		Diagnostics: append([]hookexec.Diagnostic(nil), prepared.Diagnostics...),
	}
}

func resultFromHook(observation *repositoryObservation, dryRun, initialization bool, err error) *Result {
	result := &Result{DryRun: dryRun, Ref: observation.repository.HeadRef, Base: oidPointer(observation.repository.Head), Initialization: initialization}
	var hookError *hookexec.Error
	if errors.As(err, &hookError) && hookError.Validation != nil {
		validation := *hookError.Validation
		validation.Findings = append([]checker.Finding{}, hookError.Validation.Findings...)
		result.Validation = &validation
	}
	return result
}

func resultForAudit(observation *repositoryObservation, validation checker.Result) *Result {
	copy := validation
	copy.Findings = append([]checker.Finding{}, validation.Findings...)
	return &Result{Ref: observation.repository.HeadRef, Base: oidPointer(observation.repository.Head), Validation: &copy}
}

func oidPointer(value *gitraw.OID) *string {
	if value == nil {
		return nil
	}
	return stringPointer(value.String())
}

func stringPointer(value string) *string {
	copy := value
	return &copy
}
