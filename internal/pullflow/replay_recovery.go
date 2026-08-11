package pullflow

import (
	"bytes"
	"context"
	"errors"

	"github.com/ontopix/engram/internal/checker"
	"github.com/ontopix/engram/internal/gitraw"
	"github.com/ontopix/engram/internal/managedread"
)

// repairReplayProgress closes the narrow boundary between a successfully
// accepted managed replay commit and the controller's progress update. It
// advances at most one source and only when the current private tip proves to
// be the exact, clean, completely audited child that managedwrite was asked to
// create. Anything else remains recovery-required instead of being guessed.
func (p *Puller) repairReplayProgress(ctx context.Context, repository *gitraw.Repository, state replayState, plan replayPlan) (*gitraw.Repository, replayState, replayPlan, bool, error) {
	if err := validateReplayPair(nil, state, plan); err != nil {
		return repository, state, plan, false, replayRecoveryError("validate replay progress", err)
	}
	if repository == nil || repository.Head == nil || repository.HeadRef != plan.PrivateRef {
		return repository, state, plan, false, replayRecoveryError("observe replay progress", errors.New("HEAD does not name the recorded private replay branch"))
	}
	if state.Private.Commit != nil && repository.Head.String() == *state.Private.Commit {
		return repository, state, plan, false, nil
	}
	if plan.Next >= len(plan.Sources) || state.Private.Commit == nil {
		return repository, state, plan, false, replayRecoveryError("observe replay progress", errors.New("private replay tip changed after all sources were recorded"))
	}

	store, err := managedread.Open(ctx, repository.Root)
	if err != nil {
		return repository, state, plan, false, classifyReadError(ctx, "open replay progress recovery", err)
	}
	status, err := store.Status(ctx)
	if err != nil {
		return repository, state, plan, false, classifyReadError(ctx, "inspect replay progress recovery", err)
	}
	if len(status.Staged) != 0 || len(status.Unstaged) != 0 {
		return repository, state, plan, false, replayRecoveryError("inspect replay progress recovery", errors.New("private replay checkout is not clean"))
	}

	current := repository.Head.String()
	currentOID, err := gitraw.ParseOID(repository.Format, current)
	if err != nil {
		return repository, state, plan, false, replayRecoveryError("read replay progress commit", err)
	}
	object, err := repository.ReadObject(ctx, currentOID)
	if err != nil {
		return repository, state, plan, false, classifyReadError(ctx, "read replay progress commit", err)
	}
	if object.Type != gitraw.TypeCommit {
		return repository, state, plan, false, replayRecoveryError("read replay progress commit", errors.New("private replay tip is not a commit"))
	}
	commit, err := gitraw.ParseCommit(repository.Format, object.Data)
	if err != nil {
		return repository, state, plan, false, replayRecoveryError("read replay progress commit", err)
	}
	parent, err := gitraw.ParseOID(repository.Format, *state.Private.Commit)
	if err != nil {
		return repository, state, plan, false, replayRecoveryError("read replay progress parent", err)
	}
	source := plan.Sources[plan.Next]
	if len(commit.Parents) != 1 || !commit.Parents[0].Equal(parent) || !bytes.Equal(commit.Message, []byte(source.Message+"\n")) {
		return repository, state, plan, false, replayRecoveryError("prove replay progress", errors.New("private replay tip is not the recorded source's managed child"))
	}
	audit, err := store.AuditAccepted(ctx)
	if err != nil {
		return repository, state, plan, false, classifyReadError(ctx, "audit replay progress", err)
	}
	if audit.Tip != current || audit.Raw == nil || !audit.Raw.Complete || audit.Validation.Status != checker.StatusComplete || audit.Validation.HasErrors() {
		return repository, state, plan, false, replayRecoveryError("audit replay progress", errors.New("private replay tip is not a complete conforming managed lineage"))
	}

	oldState, oldPlan := state, plan
	state.Private = gitState(plan.PrivateRef, current)
	state.Reason, state.Conflicts = "rejected", []string{}
	plan.Next++
	plan.Replayed++
	plan.DraftReady = false
	if plan.Next < len(plan.Sources) {
		state.Base = managedread.GitState{Commit: stringPointer(plan.Sources[plan.Next].Base)}
	}
	if err := p.updateReplay(repository, oldState, oldPlan, state, plan); err != nil {
		return repository, oldState, oldPlan, false, replayRecoveryError("record proven replay progress", err)
	}
	refreshed, err := gitraw.Discover(ctx, repository.Root)
	if err != nil {
		return repository, state, plan, true, classifyReadError(ctx, "recapture repaired replay", err)
	}
	return refreshed, state, plan, true, nil
}

func replayRecoveryError(operation string, err error) error {
	return typed(ErrorConflict, operation, errors.Join(ErrRecovery, err))
}
