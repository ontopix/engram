package pullflow

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"

	"github.com/ontopix/engram/internal/gitraw"
	"github.com/ontopix/engram/internal/rendezvous"
)

func (p *Puller) recoverReplayTerminal(ctx context.Context, repository *gitraw.Repository, record replayTerminal, raw []byte, expected *RecoveryExpectation) (*RecoveryResult, error) {
	base := &RecoveryResult{Needed: true, RecoveryRequired: true, Mutation: replayTerminalAuthorityMutation(false, true)}
	if expected != nil {
		digest := sha256.Sum256(raw)
		if expected.OwnerToken == "" || expected.StateSHA256 == "" || record.Owner.Token != expected.OwnerToken || hex.EncodeToString(digest[:]) != expected.StateSHA256 {
			return base, replayErrorWithMutation(typed(ErrorConcurrency, "read approved replay terminal state", errReplayTerminalChanged), "read approved replay terminal state", base.Mutation)
		}
	}
	if _, active := activeReplayTerminals.Load(record.Owner.Token); active {
		return base, replayErrorWithMutation(typed(ErrorConcurrency, "recover replay terminal state", errors.New("replay terminal owner is still active")), "recover replay terminal state", base.Mutation)
	}
	dead, err := ownerIsDead(record.Owner)
	if err != nil || !dead {
		return base, replayErrorWithMutation(typed(ErrorConflict, "recover replay terminal state", errors.Join(ErrRecovery, err, errors.New("replay terminal owner death is not proven"))), "recover replay terminal state", base.Mutation)
	}
	lease, err := rendezvous.AcquireRecovery(repository.GitDir)
	if err != nil {
		return base, replayErrorWithMutation(classifyLocal(ctx, "acquire replay terminal recovery lease", err), "acquire replay terminal recovery lease", base.Mutation)
	}
	release := true
	defer func() {
		if release {
			_ = lease.Release()
		}
	}()
	if err := p.checkpoint(PhaseRecoveryLeased); err != nil {
		return base, replayErrorWithMutation(classifyLocal(ctx, "recheck replay terminal state", err), "recheck replay terminal state", base.Mutation)
	}
	currentRaw, present, err := readControllerFile(replayTerminalPath(repository))
	if err != nil || !present || !bytes.Equal(currentRaw, raw) {
		kind := ErrorConflict
		if expected != nil {
			kind = ErrorConcurrency
		}
		return base, replayErrorWithMutation(typed(kind, "recheck replay terminal state", errors.Join(err, errReplayTerminalChanged)), "recheck replay terminal state", base.Mutation)
	}
	stage, current, err := observeReplayTerminal(ctx, repository, record)
	if err != nil || stage != replayTerminalAfter {
		return base, replayErrorWithMutation(typed(ErrorConflict, "verify replay terminal cleanup", errors.Join(ErrRecovery, err)), "verify replay terminal cleanup", base.Mutation)
	}
	pairChanged, cleanupErr := p.removeReplayTerminalPair(current, record, base.Mutation)
	base.Durable = pairChanged
	base.Mutation = terminalCleanupMutation(base.Mutation, pairChanged, true)
	if cleanupErr != nil {
		if mutation := MutationOf(cleanupErr); mutation != nil {
			base.Durable = mutation.Durable
			base.CheckoutChanged = mutation.CheckoutChanged
			base.RecoveryRequired = mutation.RecoveryRequired
			base.Mutation = clonePullMutation(mutation)
		}
		return base, cleanupErr
	}
	// The terminal record remains the recovery authority until the advisory
	// lease is known released. A release failure can therefore be retried by
	// doctor or the matching explicit replay action.
	if err := p.releaseTerminalLease(lease); err != nil {
		base.RecoveryRequired = true
		base.Mutation = mergePullMutations(terminalCleanupMutation(base.Mutation, pairChanged, true), rendezvousMutation(nil, err))
		base.Mutation.RecoveryRequired = true
		base.Durable = base.Mutation.Durable
		base.CheckoutChanged = base.Mutation.CheckoutChanged
		return base, replayErrorWithMutation(typed(ErrorIO, "release replay terminal recovery lease", errors.Join(ErrRecovery, err)), "release replay terminal recovery lease", base.Mutation)
	}
	release = false
	markerRemoved, cleanupErr := p.removeReplayTerminalAuthority(current, record, raw, base.Mutation)
	if cleanupErr != nil {
		base.Durable = base.Durable || markerRemoved
		base.RecoveryRequired = !markerRemoved
		base.Mutation = terminalCleanupMutation(base.Mutation, markerRemoved, !markerRemoved)
		return base, cleanupErr
	}
	if !markerRemoved {
		return base, replayErrorWithMutation(typed(ErrorOperational, "remove replay terminal state", ErrRecovery), "remove replay terminal state", base.Mutation)
	}
	base.Performed = true
	base.Durable = true
	base.RecoveryRequired = false
	base.Mutation = terminalCleanupMutation(base.Mutation, true, false)
	return base, nil
}

func (p *Puller) beginReplayTerminal(ctx context.Context, repository *gitraw.Repository, state replayState, plan replayPlan, phase replayTerminalPhase) (*Result, error) {
	record, err := newReplayTerminal(phase, state, plan)
	if err != nil {
		return nil, typed(ErrorOperational, "prepare replay terminal state", err)
	}
	activeReplayTerminals.Store(record.Owner.Token, struct{}{})
	defer activeReplayTerminals.Delete(record.Owner.Token)
	raw, err := publishReplayTerminalAfter(repository, record, func() error {
		return p.checkpoint(PhaseReplayTerminalPublished)
	})
	if err != nil {
		if controllerFilePublished(err) {
			mutation := replayTerminalAuthorityMutation(controllerFileDurable(err), true)
			return nil, replayErrorWithMutation(typed(ErrorIO, "publish replay terminal state", err), "publish replay terminal state", mutation)
		}
		return nil, typed(ErrorIO, "publish replay terminal state", err)
	}
	checkpoint := PhaseFinalizing
	operation := "finish replay"
	if phase == replayAborting {
		checkpoint = PhaseAborting
		operation = "abort pull"
	}
	if err := p.checkpoint(checkpoint); err != nil {
		return nil, replayErrorWithMutation(typed(ErrorIO, operation, err), operation, replayTerminalAuthorityMutation(true, true))
	}
	return p.resumeReplayTerminal(ctx, repository, record, raw)
}

func (p *Puller) continueReplayTerminal(ctx context.Context, repository *gitraw.Repository, record replayTerminal, raw []byte, phase replayTerminalPhase) (*Result, error) {
	if record.Phase != phase {
		operation := "continue pull"
		if phase == replayAborting {
			operation = "abort pull"
		}
		return nil, replayErrorWithMutation(typed(ErrorConflict, operation, errors.New("the recorded replay terminal operation requires the other explicit action")), operation, replayTerminalAuthorityMutation(false, true))
	}
	dead, err := ownerIsDead(record.Owner)
	if err != nil || !dead {
		return nil, replayErrorWithMutation(typed(ErrorConcurrency, "claim replay terminal state", errors.Join(err, errors.New("replay terminal owner is still active"))), "claim replay terminal state", replayTerminalAuthorityMutation(false, true))
	}
	activeReplayTerminals.Store(record.Owner.Token, struct{}{})
	defer activeReplayTerminals.Delete(record.Owner.Token)
	return p.resumeReplayTerminal(ctx, repository, record, raw)
}

func (p *Puller) resumeReplayTerminal(ctx context.Context, repository *gitraw.Repository, record replayTerminal, raw []byte) (*Result, error) {
	operationMutation := replayTerminalAuthorityMutation(false, true)
	lease, err := rendezvous.AcquireRecovery(repository.GitDir)
	if err != nil {
		return nil, replayErrorWithMutation(classifyLocal(ctx, "acquire replay terminal lease", err), "acquire replay terminal lease", operationMutation)
	}
	release := true
	defer func() {
		if release {
			_ = lease.Release()
		}
	}()
	observed, present, err := readControllerFile(replayTerminalPath(repository))
	if err != nil || !present || !bytes.Equal(observed, raw) {
		return nil, replayErrorWithMutation(typed(ErrorConcurrency, "recheck replay terminal state", errors.Join(err, errReplayTerminalChanged)), "recheck replay terminal state", operationMutation)
	}
	stage, current, err := observeReplayTerminal(ctx, repository, record)
	if err != nil {
		return nil, replayErrorWithMutation(typed(ErrorConflict, "observe replay terminal state", errors.Join(ErrRecovery, err)), "observe replay terminal state", operationMutation)
	}
	if stage == replayTerminalBefore {
		if err := verifyReplayTerminalPair(current, record); err != nil {
			return nil, replayErrorWithMutation(typed(ErrorConcurrency, "recheck replay terminal pair", err), "recheck replay terminal pair", operationMutation)
		}
		target, err := snapshotAt(ctx, current, *record.HeadAfter.Commit)
		if err != nil {
			return nil, replayErrorWithMutation(classifyReadError(ctx, "read replay terminal snapshot", err), "read replay terminal snapshot", operationMutation)
		}
		modes, err := modesAt(ctx, current, *record.HeadAfter.Commit)
		if err != nil {
			return nil, replayErrorWithMutation(classifyReadError(ctx, "read replay terminal modes", err), "read replay terminal modes", operationMutation)
		}
		transitionMutation, transitionErr := p.transition(ctx, transitionRequest{
			repository: current, refs: append([]transitionRef(nil), record.Refs...),
			headAfter: cloneGitState(record.HeadAfter), snapshot: target, modes: modes,
			allowDraft: record.Phase == replayAborting,
		})
		if transitionErr != nil {
			return nil, replayErrorWithMutation(transitionErr, "apply replay terminal transition", operationMutation)
		}
		operationMutation = mergePullMutations(operationMutation, transitionMutation)
		operationMutation.RecoveryRequired = true
		if err := p.checkpoint(PhaseReplayTransitioned); err != nil {
			return nil, replayErrorWithMutation(typed(ErrorIO, "apply replay terminal transition", err), "apply replay terminal transition", operationMutation)
		}
		stage, current, err = observeReplayTerminal(ctx, current, record)
		if err != nil || stage != replayTerminalAfter {
			return nil, replayErrorWithMutation(typed(ErrorConflict, "verify replay terminal transition", errors.Join(ErrRecovery, err)), "verify replay terminal transition", operationMutation)
		}
	}
	if stage != replayTerminalAfter {
		return nil, replayErrorWithMutation(typed(ErrorConflict, "observe replay terminal state", ErrRecovery), "observe replay terminal state", operationMutation)
	}
	result, resultErr := terminalResult(ctx, current, record)
	if resultErr != nil {
		return nil, replayErrorWithMutation(resultErr, "construct replay terminal result", operationMutation)
	}
	pairChanged, cleanupErr := p.removeReplayTerminalPair(current, record, operationMutation)
	operationMutation = terminalCleanupMutation(operationMutation, pairChanged, true)
	if cleanupErr != nil {
		return nil, cleanupErr
	}
	if err := p.releaseTerminalLease(lease); err != nil {
		operationMutation = mergePullMutations(operationMutation, rendezvousMutation(nil, err))
		operationMutation.RecoveryRequired = true
		return nil, replayErrorWithMutation(typed(ErrorIO, "release replay terminal lease", errors.Join(ErrRecovery, err)), "release replay terminal lease", operationMutation)
	}
	release = false
	markerRemoved, cleanupErr := p.removeReplayTerminalAuthority(current, record, raw, operationMutation)
	if cleanupErr != nil {
		return nil, cleanupErr
	}
	if !markerRemoved {
		return nil, replayErrorWithMutation(typed(ErrorOperational, "remove replay terminal state", ErrRecovery), "remove replay terminal state", operationMutation)
	}
	return result, nil
}

func verifyReplayTerminalPair(repository *gitraw.Repository, record replayTerminal) error {
	stateBytes, statePresent, err := readControllerFile(replayStatePath(repository))
	if err != nil || !statePresent {
		return errors.Join(err, errReplayTerminalChanged)
	}
	planBytes, planPresent, err := readControllerFile(replayPlanPath(repository))
	if err != nil || !planPresent {
		return errors.Join(err, errReplayTerminalChanged)
	}
	expectedState, _ := encodeCanonical(record.State)
	expectedPlan, _ := encodeCanonical(record.Plan)
	if !bytes.Equal(stateBytes, expectedState) || !bytes.Equal(planBytes, expectedPlan) {
		return errReplayTerminalChanged
	}
	return nil
}
