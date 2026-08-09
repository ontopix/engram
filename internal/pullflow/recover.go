package pullflow

import (
	"context"
	"errors"
	"os"
	"strings"

	"github.com/ontopix/engram/internal/gitraw"
	"github.com/ontopix/engram/internal/rendezvous"
)

// Recover completes or cleans one recognized pull transition. Active replay
// resolution state is not itself recovery state and is deliberately retained.
func (p *Puller) Recover(ctx context.Context, root string) (*RecoveryResult, error) {
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
		state, plan, replayPresent, replayErr := readReplay(repository)
		if replayErr != nil {
			return &RecoveryResult{Needed: true}, typed(ErrorConflict, "read pull replay recovery state", errors.Join(ErrRecovery, replayErr))
		}
		if replayPresent {
			_, _, _, repaired, repairErr := p.repairReplayProgress(ctx, repository, state, plan)
			if repairErr != nil {
				return &RecoveryResult{Needed: true}, repairErr
			}
			if repaired {
				return &RecoveryResult{Needed: true, Performed: true}, nil
			}
		}
		return &RecoveryResult{Needed: false, Performed: false}, nil
	}
	var record transitionRecord
	if err := decodeCanonical(raw, &record); err != nil || validateTransition(record) != nil {
		return nil, typed(ErrorConflict, "read pull recovery journal", errors.Join(err, validateTransition(record)))
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
		return nil, classifyLocal(ctx, "acquire pull recovery lease", err)
	}
	defer lease.Release()
	refs := make([]string, len(record.Refs))
	for index, update := range record.Refs {
		refs[index] = update.Ref
	}
	lock, err := lease.AdoptWriter(repository.CommonGitDir, repository.GitDir, record.OwnerToken, owner.Phase, refs...)
	if err != nil {
		return nil, typed(ErrorConflict, "adopt pull recovery locks", errors.Join(ErrRecovery, err))
	}
	release := false
	defer func() {
		if release {
			_ = lock.Release()
		}
	}()

	switch record.Phase {
	case transitionPrepared:
		updated, bytes, err := setTransitionPhase(repository, record, raw, transitionCancelled)
		if err != nil {
			return nil, typed(ErrorConflict, "cancel undispatched pull transition", errors.Join(ErrRecovery, err))
		}
		if err := lock.Release(); err != nil {
			return nil, typed(ErrorConflict, "release cancelled pull transition", errors.Join(ErrRecovery, err))
		}
		if err := removeTransition(repository, updated, bytes); err != nil {
			return nil, typed(ErrorConflict, "remove cancelled pull transition", errors.Join(ErrRecovery, err))
		}
		return &RecoveryResult{Needed: true, Performed: true}, nil
	case transitionCancelled:
		if err := lock.Release(); err != nil {
			return nil, typed(ErrorConflict, "release cancelled pull transition", errors.Join(ErrRecovery, err))
		}
		if err := removeTransition(repository, record, raw); err != nil {
			return nil, typed(ErrorConflict, "remove cancelled pull transition", errors.Join(ErrRecovery, err))
		}
		return &RecoveryResult{Needed: true, Performed: true}, nil
	case transitionComplete:
		if err := lock.Release(); err != nil {
			return nil, typed(ErrorConflict, "release complete pull transition", errors.Join(ErrRecovery, err))
		}
		if err := removeTransition(repository, record, raw); err != nil {
			return nil, typed(ErrorConflict, "remove complete pull transition", errors.Join(ErrRecovery, err))
		}
		return &RecoveryResult{Needed: true, Performed: true, Mutation: mutationFromRecord(record, true, true, false)}, nil
	}

	refsAfter, refsBefore, refErr := observeTransitionRefs(ctx, repository, record)
	if refErr != nil {
		return nil, typed(ErrorConflict, "observe pull recovery refs", errors.Join(ErrRecovery, refErr))
	}
	if record.Phase == transitionRefDispatched {
		if refsBefore {
			return nil, typed(ErrorConflict, "recover dispatched pull transition", errors.New("ref update outcome remains ambiguous at the recorded old values"))
		}
		if !refsAfter {
			return nil, typed(ErrorConflict, "recover dispatched pull transition", errors.New("refs have a concurrent-history value"))
		}
	} else if !refsAfter {
		return nil, typed(ErrorConflict, "recover pull transition", errors.New("recorded successful refs no longer have their final values"))
	}
	git, err := p.gitPath()
	if err != nil {
		return nil, typed(ErrorCapability, "locate git for pull recovery", err)
	}
	if err := recoverHead(ctx, p, git, repository, record); err != nil {
		return nil, typed(ErrorConflict, "recover symbolic HEAD", errors.Join(ErrRecovery, err))
	}
	if err := installIndex(repository, record); err != nil {
		return nil, typed(ErrorConflict, "recover pull index", errors.Join(ErrRecovery, err))
	}
	if err := reconcilePaths(repository.Root, record.Paths); err != nil {
		return nil, typed(ErrorConflict, "recover pull worktree", errors.Join(ErrRecovery, err))
	}
	if err := verifyTransitionResult(ctx, repository.Root, record); err != nil {
		return nil, typed(ErrorConflict, "verify recovered pull transition", errors.Join(ErrRecovery, err))
	}
	currentRaw, present, err := readControllerFile(transitionPath(repository))
	if err != nil || !present {
		return nil, typed(ErrorConflict, "complete pull recovery journal", errors.Join(ErrRecovery, err))
	}
	var current transitionRecord
	if err := decodeCanonical(currentRaw, &current); err != nil || current.OwnerToken != record.OwnerToken {
		return nil, typed(ErrorConflict, "complete pull recovery journal", errors.Join(ErrRecovery, err))
	}
	current, completeBytes, err := setTransitionPhase(repository, current, currentRaw, transitionComplete)
	if err != nil {
		return nil, typed(ErrorConflict, "complete pull recovery journal", errors.Join(ErrRecovery, err))
	}
	if err := lock.Release(); err != nil {
		return nil, typed(ErrorConflict, "release recovered pull transition", errors.Join(ErrRecovery, err))
	}
	if err := removeTransition(repository, current, completeBytes); err != nil {
		return nil, typed(ErrorConflict, "remove recovered pull transition", errors.Join(ErrRecovery, err))
	}
	return &RecoveryResult{Needed: true, Performed: true, Mutation: mutationFromRecord(record, true, true, false)}, nil
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

func recoverHead(ctx context.Context, puller *Puller, git string, repository *gitraw.Repository, record transitionRecord) error {
	result := puller.command(ctx, git, repository.Root, nil, "symbolic-ref", "--quiet", "HEAD")
	if result.err != nil || result.status != 0 {
		return errors.New(commandDetail(result))
	}
	ref := strings.TrimSuffix(string(result.stdout), "\n")
	if record.HeadAfter.Ref != nil && ref == *record.HeadAfter.Ref {
		return nil
	}
	if record.HeadBefore.Ref == nil || record.HeadAfter.Ref == nil || ref != *record.HeadBefore.Ref {
		return errors.New("symbolic HEAD has neither its recorded before nor after name")
	}
	// The before ref may already have been atomically deleted as part of a
	// finalization/abort transaction. Its symbolic name is still sufficient to
	// identify the one recorded intermediate state.
	updated := puller.command(ctx, git, repository.Root, nil, "symbolic-ref", "HEAD", *record.HeadAfter.Ref)
	if updated.err != nil || updated.status != 0 {
		return errors.New(commandDetail(updated))
	}
	return nil
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
		_, active := activeTransitionTokens.Load(owner.Token)
		return !active, nil
	}
	alive, err := processAlive(owner.PID)
	if err != nil {
		return false, err
	}
	return !alive, nil
}
