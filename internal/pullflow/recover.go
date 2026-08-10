package pullflow

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"strings"

	"github.com/ontopix/engram/internal/gitraw"
	"github.com/ontopix/engram/internal/rendezvous"
)

// Recover completes or cleans one recognized pull transition. Active replay
// resolution state is not itself recovery state and is deliberately retained.
func (p *Puller) Recover(ctx context.Context, root string) (*RecoveryResult, error) {
	return p.recover(ctx, root, nil)
}

// RecoverExpected binds recovery to the exact canonical pull transition
// approved by an external read-only recovery coordinator.
func (p *Puller) RecoverExpected(ctx context.Context, root string, expected RecoveryExpectation) (*RecoveryResult, error) {
	return p.recover(ctx, root, &expected)
}

func (p *Puller) recover(ctx context.Context, root string, expected *RecoveryExpectation) (*RecoveryResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if p == nil {
		return nil, typed(ErrorOperational, "recover pull", errors.New("nil pull controller"))
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	repository, err := gitraw.Discover(ctx, root)
	if err != nil {
		return nil, classifyReadError(ctx, "discover pull recovery repository", err)
	}
	raw, present, err := readControllerFile(transitionPath(repository))
	if err != nil {
		return nil, typed(ErrorConflict, "read pull recovery journal", err)
	}
	if !present {
		terminal, terminalRaw, terminalPresent, terminalErr := readReplayTerminal(repository)
		if terminalErr != nil {
			mutation := &Mutation{LocalRefs: []RefMutation{}, RecoveryRequired: true}
			err := replayErrorWithMutation(typed(ErrorConflict, "read pull replay terminal state", errors.Join(ErrRecovery, terminalErr)), "read pull replay terminal state", mutation)
			return &RecoveryResult{Needed: true, RecoveryRequired: true, Mutation: mutation}, err
		}
		if terminalPresent {
			return p.recoverReplayTerminal(ctx, repository, terminal, terminalRaw, expected)
		}
		pair, pairRaw, pairPresent, pairErr := readReplayPairJournal(repository)
		if pairErr != nil {
			mutation := replayPairMutation(false, true)
			err := replayPairError(ErrorConflict, "read pull replay pair journal", pairErr, false, true)
			return &RecoveryResult{Needed: true, RecoveryRequired: true, Mutation: mutation}, err
		}
		if pairPresent {
			return p.recoverReplayPair(ctx, repository, pair, pairRaw, expected)
		}
		if expected != nil {
			return &RecoveryResult{Needed: true, RecoveryRequired: true}, typed(ErrorConcurrency, "read approved pull recovery journal", ErrRecovery)
		}
		state, plan, replayPresent, replayErr := readReplay(repository)
		if replayErr != nil {
			mutation := &Mutation{LocalRefs: []RefMutation{}, RecoveryRequired: true}
			err := replayErrorWithMutation(typed(ErrorConflict, "read pull replay recovery state", errors.Join(ErrRecovery, replayErr)), "read pull replay recovery state", mutation)
			return &RecoveryResult{Needed: true, RecoveryRequired: true, Mutation: mutation}, err
		}
		if replayPresent {
			_, _, _, repaired, repairErr := p.repairReplayProgress(ctx, repository, state, plan)
			if repairErr != nil {
				mutation := MutationOf(repairErr)
				if mutation == nil {
					mutation = replayPairMutation(false, true)
				}
				return &RecoveryResult{
					Needed: true, Durable: mutation.Durable, CheckoutChanged: mutation.CheckoutChanged,
					RecoveryRequired: true, Mutation: mutation,
				}, replayErrorWithMutation(repairErr, "recover replay progress", mutation)
			}
			if repaired {
				mutation := replayPairMutation(true, false)
				return &RecoveryResult{
					Needed: true, Performed: true, Durable: true,
					Mutation: mutation,
				}, nil
			}
		}
		return &RecoveryResult{Needed: false, Performed: false}, nil
	}
	var record transitionRecord
	if err := decodeCanonical(raw, &record); err != nil || validateTransition(record) != nil {
		return nil, typed(ErrorConflict, "read pull recovery journal", errors.Join(err, validateTransition(record)))
	}
	if expected != nil {
		digest := sha256.Sum256(raw)
		if expected.OwnerToken == "" || expected.StateSHA256 == "" || record.OwnerToken != expected.OwnerToken ||
			hex.EncodeToString(digest[:]) != expected.StateSHA256 {
			return &RecoveryResult{Needed: true, RecoveryRequired: true}, typed(ErrorConcurrency, "read approved pull recovery journal", ErrRecovery)
		}
	}
	if _, active := activeTransitionTokens.Load(record.OwnerToken); active {
		return nil, typed(ErrorConcurrency, "recover pull", errors.New("pull transition owner is still active"))
	}
	owner, err := rendezvous.Read(rendezvous.WorktreePath(repository.GitDir))
	phaseAllowed := owner.Phase == rendezvous.JournalRequired || record.Phase == transitionPrepared && owner.Phase == rendezvous.PreJournal
	if err != nil || owner.Token != record.OwnerToken || !phaseAllowed {
		return nil, typed(ErrorConflict, "recover pull", errors.Join(ErrRecovery, err, errors.New("journal lacks its exact worktree lock owner")))
	}
	dead, err := ownerIsDead(owner)
	if err != nil || !dead {
		return nil, typed(ErrorConflict, "recover pull", errors.Join(ErrRecovery, err, errors.New("owner death is not proven")))
	}
	lease, err := rendezvous.AcquireRecovery(repository.GitDir)
	if err != nil {
		operation := "acquire pull recovery lease"
		mutation := mergePullMutations(replayPairMutation(false, true), rendezvousMutation(nil, err))
		mutation.RecoveryRequired = true
		result := &RecoveryResult{Needed: true, Durable: mutation.Durable, RecoveryRequired: true, Mutation: mutation}
		return result, replayErrorWithMutation(classifyLocal(ctx, operation, err), operation, mutation)
	}
	defer lease.Release()
	if err := p.checkpoint(PhaseRecoveryLeased); err != nil {
		mutation := replayPairMutation(false, true)
		result := &RecoveryResult{Needed: true, RecoveryRequired: true, Mutation: mutation}
		return result, replayErrorWithMutation(classifyLocal(ctx, "recheck approved pull recovery journal", err), "recheck approved pull recovery journal", mutation)
	}
	leasedRaw, leasedPresent, leasedErr := readControllerFile(transitionPath(repository))
	if leasedErr != nil || !leasedPresent || !bytes.Equal(leasedRaw, raw) {
		return &RecoveryResult{Needed: true, RecoveryRequired: true}, recoveryDivergence(expected, "recheck approved pull recovery journal", leasedErr)
	}
	refs := make([]string, len(record.Refs))
	for index, update := range record.Refs {
		refs[index] = update.Ref
	}
	lock, err := lease.AdoptWriter(repository.CommonGitDir, repository.GitDir, record.OwnerToken, owner.Phase, refs...)
	if err != nil {
		mutation := rendezvousMutation(nil, err)
		durable := mutation != nil && mutation.Durable
		result := recoveryEvidence(record, false, durable, false, false, false, true)
		result.Mutation = mergePullMutations(result.Mutation, mutation)
		result.Mutation.RecoveryRequired = true
		if errors.Is(err, rendezvous.ErrOwnership) {
			return result, replayErrorWithMutation(recoveryDivergence(expected, "adopt pull recovery locks", err), "adopt pull recovery locks", result.Mutation)
		}
		return result, replayErrorWithMutation(classifyLocal(ctx, "adopt pull recovery locks", err), "adopt pull recovery locks", result.Mutation)
	}
	adoptedResult := recoveryEvidence(record, false, true, false, false, false, true)

	switch record.Phase {
	case transitionPrepared:
		updated, bytes, err := setTransitionPhase(repository, record, raw, transitionCancelled)
		if err != nil {
			return recoveryEvidence(record, false, true, false, false, false, true), recoveryCASFailure(expected, "cancel undispatched pull transition", err)
		}
		if err := lock.Release(); err != nil {
			result := recoveryWithRendezvous(recoveryEvidence(updated, false, true, false, false, false, true), lock, err)
			return result, replayErrorWithMutation(typed(ErrorConflict, "release cancelled pull transition", errors.Join(ErrRecovery, err)), "release cancelled pull transition", result.Mutation)
		}
		if err := removeTransition(repository, updated, bytes); err != nil {
			return recoveryEvidence(updated, false, true, false, false, false, true), recoveryCASFailure(expected, "remove cancelled pull transition", err)
		}
		return recoveryEvidence(updated, true, true, false, false, false, false), nil
	case transitionCancelled:
		if err := lock.Release(); err != nil {
			result := recoveryWithRendezvous(recoveryEvidence(record, false, true, false, false, false, true), lock, err)
			return result, replayErrorWithMutation(typed(ErrorConflict, "release cancelled pull transition", errors.Join(ErrRecovery, err)), "release cancelled pull transition", result.Mutation)
		}
		if err := removeTransition(repository, record, raw); err != nil {
			return recoveryEvidence(record, false, true, false, false, false, true), recoveryCASFailure(expected, "remove cancelled pull transition", err)
		}
		return recoveryEvidence(record, true, true, false, false, false, false), nil
	case transitionComplete:
		if err := lock.Release(); err != nil {
			result := recoveryWithRendezvous(recoveryEvidence(record, false, true, false, false, false, true), lock, err)
			return result, replayErrorWithMutation(typed(ErrorConflict, "release complete pull transition", errors.Join(ErrRecovery, err)), "release complete pull transition", result.Mutation)
		}
		if err := removeTransition(repository, record, raw); err != nil {
			return recoveryEvidence(record, false, true, false, false, false, true), recoveryCASFailure(expected, "remove complete pull transition", err)
		}
		return recoveryEvidence(record, true, true, false, false, false, false), nil
	}

	refsAfter, refsBefore, refErr := observeTransitionRefs(ctx, repository, record)
	if refErr != nil {
		return adoptedResult, typed(ErrorConflict, "observe pull recovery refs", errors.Join(ErrRecovery, refErr))
	}
	if record.Phase == transitionRefDispatched {
		if refsBefore {
			return adoptedResult, typed(ErrorConflict, "recover dispatched pull transition", errors.New("ref update outcome remains ambiguous at the recorded old values"))
		}
		if !refsAfter {
			return adoptedResult, typed(ErrorConflict, "recover dispatched pull transition", errors.New("refs have a concurrent-history value"))
		}
	} else if !refsAfter {
		return adoptedResult, typed(ErrorConflict, "recover pull transition", errors.New("recorded successful refs no longer have their final values"))
	}
	git, err := p.gitPath()
	if err != nil {
		return adoptedResult, typed(ErrorCapability, "locate git for pull recovery", err)
	}
	result := recoveryEvidence(record, false, true, false, false, false, true)
	headChanged, err := recoverHead(ctx, p, git, repository, record)
	if err != nil {
		return result, typed(ErrorConflict, "recover symbolic HEAD", errors.Join(ErrRecovery, err))
	}
	if headChanged {
		markRecoveryHeadKnown(result, record)
	}
	if err := p.checkpoint(PhaseHeadUpdated); err != nil {
		return result, classifyLocal(ctx, "recover symbolic HEAD", err)
	}
	indexChanged, err := installIndex(repository, record)
	if err != nil {
		return result, typed(ErrorConflict, "recover pull index", errors.Join(ErrRecovery, err))
	}
	if indexChanged {
		result.CheckoutChanged = true
		if result.Mutation != nil {
			result.Mutation.CheckoutChanged = true
		}
	}
	if err := p.checkpoint(PhaseIndexUpdated); err != nil {
		return result, classifyLocal(ctx, "recover pull index", err)
	}
	pathsChanged, err := reconcilePaths(repository.Root, record.Paths)
	if pathsChanged {
		result.CheckoutChanged = true
		if result.Mutation != nil {
			result.Mutation.CheckoutChanged = true
		}
	}
	if err != nil {
		return result, typed(ErrorConflict, "recover pull worktree", errors.Join(ErrRecovery, err))
	}
	if err := p.checkpoint(PhaseWorktreeUpdated); err != nil {
		return result, classifyLocal(ctx, "recover pull worktree", err)
	}
	if err := verifyTransitionResult(ctx, repository.Root, record); err != nil {
		return result, typed(ErrorConflict, "verify recovered pull transition", errors.Join(ErrRecovery, err))
	}
	currentRaw, present, err := readControllerFile(transitionPath(repository))
	if err != nil || !present || !bytes.Equal(currentRaw, raw) {
		return result, recoveryDivergence(expected, "complete pull recovery journal", err)
	}
	current, completeBytes, err := setTransitionPhase(repository, record, raw, transitionComplete)
	if err != nil {
		return result, recoveryCASFailure(expected, "complete pull recovery journal", err)
	}
	if err := lock.Release(); err != nil {
		result = recoveryWithRendezvous(result, lock, err)
		return result, replayErrorWithMutation(typed(ErrorConflict, "release recovered pull transition", errors.Join(ErrRecovery, err)), "release recovered pull transition", result.Mutation)
	}
	if err := removeTransition(repository, current, completeBytes); err != nil {
		return result, recoveryCASFailure(expected, "remove recovered pull transition", err)
	}
	result.Performed = true
	result.RecoveryRequired = false
	if result.Mutation != nil {
		result.Mutation.RecoveryRequired = false
	}
	return result, nil
}

func recoveryWithRendezvous(result *RecoveryResult, handle *rendezvous.Handle, err error) *RecoveryResult {
	if result == nil {
		result = &RecoveryResult{Needed: true}
	}
	result.Mutation = mergePullMutations(result.Mutation, rendezvousMutation(handle, err))
	if result.Mutation != nil {
		result.Durable = result.Mutation.Durable
		result.CheckoutChanged = result.Mutation.CheckoutChanged
		result.RecoveryRequired = result.Mutation.RecoveryRequired
	}
	return result
}

func recoveryEvidence(record transitionRecord, performed, durable, refsKnown, headKnown, checkoutChanged, recoveryRequired bool) *RecoveryResult {
	mutation := mutationFromRecord(record, durable, checkoutChanged, recoveryRequired)
	if !refsKnown {
		mutation.LocalRefs = []RefMutation{}
	}
	if !headKnown {
		mutation.Head = nil
	}
	return &RecoveryResult{
		Needed: true, Performed: performed, Durable: durable,
		CheckoutChanged: checkoutChanged, RecoveryRequired: recoveryRequired,
		Mutation: mutation,
	}
}

func recoveryDivergence(expected *RecoveryExpectation, operation string, err error) error {
	if err == nil {
		err = errTransitionChanged
	}
	kind := ErrorConflict
	if expected != nil {
		kind = ErrorConcurrency
	}
	return typed(kind, operation, errors.Join(ErrRecovery, err))
}

func recoveryCASFailure(expected *RecoveryExpectation, operation string, err error) error {
	if expected != nil && errors.Is(err, errTransitionChanged) {
		return recoveryDivergence(expected, operation, err)
	}
	return typed(ErrorConflict, operation, errors.Join(ErrRecovery, err))
}

func markRecoveryHeadKnown(result *RecoveryResult, record transitionRecord) {
	if result == nil || result.Mutation == nil || sameGitState(record.HeadBefore, record.HeadAfter) {
		return
	}
	result.Mutation.Head = &HeadMutation{Before: cloneGitState(record.HeadBefore), After: cloneGitState(record.HeadAfter)}
}

func observeTransitionRefs(ctx context.Context, repository *gitraw.Repository, record transitionRecord) (after, before bool, err error) {
	after, before = true, true
	for _, update := range record.Refs {
		value, err := resolveRef(ctx, repository.Root, update.Ref, record.ObjectFormat)
		if err != nil {
			return false, false, err
		}
		after = after && equalString(value, update.After)
		before = before && equalString(value, update.Before)
	}
	return after, before, nil
}

func recoverHead(ctx context.Context, puller *Puller, git string, repository *gitraw.Repository, record transitionRecord) (bool, error) {
	result := puller.command(ctx, git, repository.Root, nil, "symbolic-ref", "--quiet", "HEAD")
	if result.err != nil || result.status != 0 {
		return false, errors.New(commandDetail(result))
	}
	ref := strings.TrimSuffix(string(result.stdout), "\n")
	if record.HeadAfter.Ref != nil && ref == *record.HeadAfter.Ref {
		return false, nil
	}
	if record.HeadBefore.Ref == nil || record.HeadAfter.Ref == nil || ref != *record.HeadBefore.Ref {
		return false, errors.New("symbolic HEAD has neither its recorded before nor after name")
	}
	// The before ref may already have been atomically deleted as part of a
	// finalization/abort transaction. Its symbolic name is still sufficient to
	// identify the one recorded intermediate state.
	updated := puller.command(ctx, git, repository.Root, nil, "symbolic-ref", "HEAD", *record.HeadAfter.Ref)
	if updated.err != nil || updated.status != 0 {
		return false, errors.New(commandDetail(updated))
	}
	return true, nil
}

func ownerIsDead(owner rendezvous.Owner) (bool, error) {
	if owner.PID <= 0 || owner.Hostname == "" {
		return false, errors.New("invalid recovery owner")
	}
	host, err := os.Hostname()
	if err != nil {
		return false, err
	}
	if host != owner.Hostname {
		return false, errors.New("owner belongs to another host")
	}
	if owner.PID == os.Getpid() {
		_, transitionActive := activeTransitionTokens.Load(owner.Token)
		_, terminalActive := activeReplayTerminals.Load(owner.Token)
		_, pairActive := activeReplayPairs.Load(owner.Token)
		return !transitionActive && !terminalActive && !pairActive, nil
	}
	alive, err := processAlive(owner.PID)
	if err != nil {
		return false, err
	}
	return !alive, nil
}
