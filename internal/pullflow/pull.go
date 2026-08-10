package pullflow

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"

	"github.com/ontopix/engram/internal/changeset"
	"github.com/ontopix/engram/internal/checker"
	"github.com/ontopix/engram/internal/gitraw"
	"github.com/ontopix/engram/internal/hookexec"
	"github.com/ontopix/engram/internal/managedread"
	"github.com/ontopix/engram/internal/managedwrite"
	"github.com/ontopix/engram/internal/replay"
)

func (p *Puller) Pull(ctx context.Context, store *managedread.Store, remote, branch string) (*Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if p == nil || p.Writer == nil {
		return nil, typed(ErrorCapability, "pull", errors.New("managed writer is unavailable"))
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, typed(ErrorCancelled, "pull", err)
	}
	if store == nil || store.Repository() == nil {
		return nil, typed(ErrorRepository, "select managed store", errors.New("nil managed store"))
	}
	repository, err := recapture(ctx, store.Repository())
	if err != nil {
		return nil, err
	}
	if _, present, err := readControllerFile(transitionPath(repository)); err != nil || present {
		if err == nil {
			err = ErrRecovery
		}
		return nil, typed(ErrorConflict, "inspect pull recovery", err)
	}
	if active, err := Active(repository); err != nil {
		failure := typed(ErrorConflict, "inspect active replay", errors.Join(ErrRecovery, err))
		if stateFilesExist(repository) {
			failure = replayErrorWithMutation(failure, "inspect active replay", &Mutation{LocalRefs: []RefMutation{}, RecoveryRequired: true})
		}
		return nil, failure
	} else if active != nil {
		return nil, typed(ErrorConflict, "start pull", ErrActiveReplay)
	}
	status, err := store.Status(ctx)
	if err != nil {
		return nil, classifyReadError(ctx, "inspect pull input", err)
	}
	if len(status.Staged) != 0 || len(status.Unstaged) != 0 {
		return nil, typed(ErrorConflict, "inspect pull input", ErrUnrelated)
	}
	selection, err := selectRemote(ctx, repository, remote, branch)
	if err != nil {
		return nil, err
	}
	local, err := store.AuditAccepted(ctx)
	if err != nil {
		return nil, classifyReadError(ctx, "audit local accepted lineage", err)
	}
	if err := verifyRepository(ctx, repository, local.Tip); err != nil {
		return nil, err
	}
	baseResult := &Result{
		Remote: selection.Remote, RemoteRef: selection.RemoteRef,
		Before: gitState(repository.HeadRef, local.Tip), After: gitState(repository.HeadRef, local.Tip),
		Conflicts: []string{}, Validation: cloneValidation(local.Validation), Audits: cloneAudits(local.Audits),
	}
	if local.Validation.Status != checker.StatusComplete || local.Validation.HasErrors() {
		baseResult.State = Rejected
		return baseResult, nil
	}
	if err := requireCompleteLocal(local); err != nil {
		return nil, typed(ErrorRepository, "audit local accepted lineage", err)
	}
	git, err := p.gitPath()
	if err != nil {
		return nil, typed(ErrorCapability, "locate git", err)
	}
	remoteTip, err := p.acquireTip(ctx, git, repository, selection)
	if err != nil {
		return nil, err
	}
	if err := p.checkpoint(PhaseFetched); err != nil {
		return nil, typed(ErrorIO, "fetch incoming lineage", err)
	}
	incoming, err := auditIncoming(ctx, repository, remoteTip)
	if err != nil {
		return nil, classifyReadError(ctx, "audit incoming lineage", err)
	}
	if err := p.auditIncomingAttributes(ctx, git, repository, incoming); err != nil {
		return nil, err
	}
	baseResult.Validation = cloneValidation(incoming.Validation)
	baseResult.Audits = cloneAudits(incoming.Audits)
	if incoming.Validation.Status != checker.StatusComplete || incoming.Validation.HasErrors() {
		baseResult.State = Rejected
		return baseResult, nil
	}
	if err := requireCompleteIncoming(incoming); err != nil {
		return nil, typed(ErrorRepository, "audit incoming lineage", err)
	}
	if err := verifyRepository(ctx, repository, local.Tip); err != nil {
		return nil, err
	}
	common := commonPrefix(local, incoming)
	baseResult.Fetched = len(incoming.Raw.Commits) - common
	if common == len(incoming.Raw.Commits) {
		baseResult.State = UpToDate
		return baseResult, nil
	}
	if common == len(local.Raw.Commits) {
		return p.fastForward(ctx, repository, local, incoming, baseResult)
	}
	if common == 0 {
		baseResult.State = Conflict
		return baseResult, nil
	}
	return p.startReplay(ctx, repository, local, incoming, common, baseResult)
}

func (p *Puller) Continue(ctx context.Context, store *managedread.Store) (result *Result, resultErr error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if p == nil || p.Writer == nil {
		return nil, typed(ErrorCapability, "continue pull", errors.New("managed writer is unavailable"))
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	var operationMutation *Mutation
	defer func() {
		if resultErr != nil && operationMutation != nil {
			resultErr = replayErrorWithMutation(resultErr, "", operationMutation)
		}
	}()
	if store == nil || store.Repository() == nil {
		return nil, typed(ErrorRepository, "continue pull", errors.New("nil managed store"))
	}
	repository, err := recapture(ctx, store.Repository())
	if err != nil {
		return nil, err
	}
	terminal, terminalRaw, terminalPresent, terminalErr := readReplayTerminal(repository)
	if terminalErr != nil {
		return nil, replayErrorWithMutation(typed(ErrorConflict, "read replay terminal state", errors.Join(ErrRecovery, terminalErr)), "read replay terminal state", &Mutation{LocalRefs: []RefMutation{}, RecoveryRequired: true})
	}
	if terminalPresent {
		return p.continueReplayTerminal(ctx, repository, terminal, terminalRaw, replayFinalizing)
	}
	state, plan, present, err := readReplay(repository)
	if err != nil {
		return nil, replayErrorWithMutation(typed(ErrorConflict, "read pull replay", errors.Join(ErrRecovery, err)), "read pull replay", &Mutation{LocalRefs: []RefMutation{}, RecoveryRequired: true})
	}
	if !present {
		return nil, typed(ErrorConflict, "continue pull", ErrNoActiveReplay)
	}
	var repaired bool
	repository, state, plan, repaired, err = p.repairReplayProgress(ctx, repository, state, plan)
	if err != nil {
		return nil, err
	}
	if repaired {
		operationMutation = replayPairMutation(true, false)
	}
	if err := validateReplayPair(repository, state, plan); err != nil {
		return nil, typed(ErrorConflict, "continue pull", errors.Join(ErrRecovery, err))
	}
	if plan.Next == len(plan.Sources) {
		return p.finishReplay(ctx, repository, state, plan, nil)
	}
	if !plan.DraftReady {
		return p.processAutomatic(ctx, repository.Root, state, plan)
	}
	status, err := store.Status(ctx)
	if err != nil {
		return nil, classifyReadError(ctx, "inspect replay resolution", err)
	}
	if len(status.Unstaged) != 0 {
		return nil, typed(ErrorConflict, "inspect replay resolution", ErrUnrelated)
	}
	source := plan.Sources[plan.Next]
	accepted, writeErr := p.Writer.Commit(ctx, managedwrite.Request{Store: repository.Root, Message: source.Message})
	if writeErr != nil {
		if isCandidateRejection(writeErr) {
			oldState, oldPlan := state, plan
			state.Reason, state.Conflicts = "rejected", []string{}
			if updateErr := p.updateReplay(repository, oldState, oldPlan, state, plan); updateErr != nil {
				return nil, typed(ErrorConcurrency, "retain rejected resolution", updateErr)
			}
			return replayResult(repository, state, plan, Rejected, resultChanges(accepted), resultValidation(accepted)), nil
		}
		return nil, classifyWriterError(repository, accepted, writeErr)
	}
	return p.advanceReplay(ctx, repository.Root, state, plan, accepted)
}

func (p *Puller) Abort(ctx context.Context, store *managedread.Store) (result *Result, resultErr error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if p == nil {
		return nil, typed(ErrorOperational, "abort pull", errors.New("nil pull controller"))
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	var operationMutation *Mutation
	defer func() {
		if resultErr != nil && operationMutation != nil {
			resultErr = replayErrorWithMutation(resultErr, "", operationMutation)
		}
	}()
	if store == nil || store.Repository() == nil {
		return nil, typed(ErrorRepository, "abort pull", errors.New("nil managed store"))
	}
	repository, err := recapture(ctx, store.Repository())
	if err != nil {
		return nil, err
	}
	terminal, terminalRaw, terminalPresent, terminalErr := readReplayTerminal(repository)
	if terminalErr != nil {
		return nil, replayErrorWithMutation(typed(ErrorConflict, "read replay terminal state", errors.Join(ErrRecovery, terminalErr)), "read replay terminal state", &Mutation{LocalRefs: []RefMutation{}, RecoveryRequired: true})
	}
	if terminalPresent {
		return p.continueReplayTerminal(ctx, repository, terminal, terminalRaw, replayAborting)
	}
	state, plan, present, err := readReplay(repository)
	if err != nil {
		return nil, replayErrorWithMutation(typed(ErrorConflict, "read pull replay", errors.Join(ErrRecovery, err)), "read pull replay", &Mutation{LocalRefs: []RefMutation{}, RecoveryRequired: true})
	}
	if !present {
		return nil, typed(ErrorConflict, "abort pull", ErrNoActiveReplay)
	}
	var repaired bool
	repository, state, plan, repaired, err = p.repairReplayProgress(ctx, repository, state, plan)
	if err != nil {
		return nil, err
	}
	if repaired {
		operationMutation = replayPairMutation(true, false)
	}
	if err := validateReplayPair(repository, state, plan); err != nil {
		return nil, typed(ErrorConflict, "abort pull", errors.Join(ErrRecovery, err))
	}
	status, err := store.Status(ctx)
	if err != nil {
		return nil, classifyReadError(ctx, "inspect replay draft", err)
	}
	if len(status.Unstaged) != 0 {
		return nil, typed(ErrorConflict, "abort pull", ErrUnrelated)
	}
	if state.Original.Ref == nil || state.Original.Commit == nil || state.Private.Ref == nil || state.Private.Commit == nil {
		return nil, typed(ErrorOperational, "abort pull", errors.New("replay Git states are incomplete"))
	}
	return p.beginReplayTerminal(ctx, repository, state, plan, replayAborting)
}

func (p *Puller) fastForward(ctx context.Context, repository *gitraw.Repository, local *managedread.AcceptedAudit, incoming *lineageAudit, result *Result) (*Result, error) {
	target := incoming.Snapshots[incoming.Tip]
	modes, err := modesAt(ctx, repository, incoming.Tip)
	if err != nil {
		return nil, classifyReadError(ctx, "read incoming modes", err)
	}
	update := transitionRef{Ref: repository.HeadRef, Before: stringPointer(local.Tip), After: stringPointer(incoming.Tip)}
	_, err = p.transition(ctx, transitionRequest{
		repository: repository, refs: []transitionRef{update},
		headAfter: gitState(repository.HeadRef, incoming.Tip), snapshot: target, modes: modes,
	})
	if err != nil {
		return nil, err
	}
	result.State = FastForwarded
	result.After = gitState(repository.HeadRef, incoming.Tip)
	result.Changes = changeset.Diff(local.Snapshots[local.Tip].Tree, target.Tree)
	return result, nil
}

func (p *Puller) startReplay(ctx context.Context, repository *gitraw.Repository, local *managedread.AcceptedAudit, incoming *lineageAudit, common int, result *Result) (out *Result, resultErr error) {
	var state replayState
	var plan replayPlan
	replayPublished := false
	var workflowMutation *Mutation
	defer func() {
		if resultErr == nil || !replayPublished {
			return
		}
		recoveryRequired := pullRecoveryRequired(repository)
		mutation := terminalCleanupMutation(workflowMutation, replayPublished, recoveryRequired)
		resultErr = replayErrorWithObservedRecovery(resultErr, "", mutation, recoveryRequired)
	}()
	sources, err := sourceRecords(local, common)
	if err != nil || len(sources) == 0 {
		return nil, typed(ErrorRepository, "construct divergent replay", errors.Join(err, errors.New("divergent replay has no local source commits")))
	}
	privateRef, err := randomPrivateRef()
	if err != nil {
		return nil, typed(ErrorIO, "allocate private replay ref", err)
	}
	privateState := gitState(privateRef, incoming.Tip)
	state = replayState{
		Version: 1, Original: gitState(repository.HeadRef, local.Tip), Private: privateState,
		Base: managedread.GitState{Commit: stringPointer(sources[0].Base)}, Reason: "rejected", Conflicts: []string{},
	}
	plan = replayPlan{
		Version: 1, Remote: result.Remote, RemoteRef: result.RemoteRef, Original: cloneGitState(state.Original),
		PrivateRef: privateRef, RemoteTip: incoming.Tip, Sources: sources, Next: 0, DraftReady: false,
		Fetched: result.Fetched, Replayed: 0, Validation: cloneValidation(incoming.Validation), Audits: cloneAudits(incoming.Audits),
	}
	publication, publicationRaw, err := p.publishReplay(repository, state, plan)
	if publication.Owner.Token != "" {
		defer activeReplayPairs.Delete(publication.Owner.Token)
	}
	if err != nil {
		return nil, typed(ErrorIO, "publish replay plan", err)
	}
	replayPublished = true
	workflowMutation = replayPairMutation(true, true)
	target := incoming.Snapshots[incoming.Tip]
	modes, err := modesAt(ctx, repository, incoming.Tip)
	if err != nil {
		return nil, classifyReadError(ctx, "read incoming replay modes", err)
	}
	refs := sortedRefUpdates([]transitionRef{
		{Ref: repository.HeadRef, Before: stringPointer(local.Tip), After: stringPointer(local.Tip)},
		{Ref: privateRef, Before: nil, After: stringPointer(incoming.Tip)},
	})
	activationMutation, transitionErr := p.transition(ctx, transitionRequest{repository: repository, refs: refs, headAfter: privateState, snapshot: target, modes: modes})
	if transitionErr != nil {
		return nil, transitionErr
	}
	workflowMutation = mergePullMutations(workflowMutation, activationMutation)
	workflowMutation.RecoveryRequired = true
	if err := p.checkpoint(PhaseReplayActivated); err != nil {
		return nil, &Error{Kind: ErrorOperational, Operation: "activate pull replay", Mutation: &Mutation{Durable: true, LocalRefs: []RefMutation{{Ref: privateRef, After: stringPointer(incoming.Tip)}}, Head: &HeadMutation{Before: state.Original, After: privateState}, CheckoutChanged: true}, Err: err}
	}
	if err := p.completeReplayPublication(ctx, repository, publication, publicationRaw); err != nil {
		return nil, err
	}
	return p.processAutomatic(ctx, repository.Root, state, plan)
}

func (p *Puller) processAutomatic(ctx context.Context, root string, state replayState, plan replayPlan) (result *Result, resultErr error) {
	store, err := managedread.Open(ctx, root)
	if err != nil {
		return nil, classifyReadError(ctx, "open private replay branch", err)
	}
	repository := store.Repository()
	if err := validateReplayPair(repository, state, plan); err != nil {
		return nil, typed(ErrorConflict, "validate private replay", errors.Join(ErrRecovery, err))
	}
	status, err := store.Status(ctx)
	if err != nil {
		return nil, classifyReadError(ctx, "inspect private replay", err)
	}
	if len(status.Staged) != 0 || len(status.Unstaged) != 0 {
		return nil, typed(ErrorConflict, "inspect private replay", ErrUnrelated)
	}
	source := plan.Sources[plan.Next]
	original, err := snapshotAt(ctx, repository, source.Base)
	if err != nil {
		return nil, classifyReadError(ctx, "read replay source base", err)
	}
	next, err := snapshotAt(ctx, repository, source.ID)
	if err != nil {
		return nil, classifyReadError(ctx, "read replay source candidate", err)
	}
	current, err := store.Accepted(ctx)
	if err != nil {
		return nil, classifyReadError(ctx, "read current replay base", err)
	}
	applied := replay.Apply(snapshotFiles(original), snapshotFiles(next), snapshotFiles(current.Snapshot))
	if len(applied.Conflicts) != 0 {
		oldState, oldPlan := state, plan
		state.Reason, state.Conflicts = "conflict", append([]string(nil), applied.Conflicts...)
		plan.DraftReady = true
		if err := p.updateReplay(repository, oldState, oldPlan, state, plan); err != nil {
			return nil, typed(ErrorConcurrency, "publish replay conflict", err)
		}
		return replayResult(repository, state, plan, Conflict, nil, nil), nil
	}
	if applied.Satisfied {
		return p.advanceReplay(ctx, root, state, plan, nil)
	}
	candidate, err := analyzeFiles(applied.Files)
	if err != nil {
		return nil, typed(ErrorRepository, "analyze replay candidate", err)
	}
	modes := candidateModes(candidate, current.Modes)
	validation, _ := checker.CheckTransition(current.Snapshot, candidate, false)
	accepted, writeErr := p.Writer.CommitImage(ctx, managedwrite.ImageRequest{
		Store: root, Message: source.Message, Candidate: candidate, Modes: modes,
		RequireClean: true, RequireBase: true, ExpectedBase: stringPointer(repository.Head.String()),
	})
	if writeErr != nil {
		if !isCandidateRejection(writeErr) {
			return nil, classifyWriterError(repository, accepted, writeErr)
		}
		git, locateErr := p.gitPath()
		if locateErr != nil {
			return nil, typed(ErrorCapability, "locate git", locateErr)
		}
		index, indexErr := p.indexForSnapshot(ctx, git, repository, candidate, modes)
		if indexErr != nil {
			return nil, indexErr
		}
		verify := transitionRef{Ref: repository.HeadRef, Before: stringPointer(repository.Head.String()), After: stringPointer(repository.Head.String())}
		draftMutation, draftErr := p.transition(ctx, transitionRequest{repository: repository, refs: []transitionRef{verify}, headAfter: gitState(repository.HeadRef, repository.Head.String()), snapshot: candidate, modes: modes, index: index})
		if draftErr != nil {
			return nil, draftErr
		}
		oldState, oldPlan := state, plan
		state.Reason, state.Conflicts = "rejected", []string{}
		plan.DraftReady = true
		if err := p.updateReplay(repository, oldState, oldPlan, state, plan); err != nil {
			return nil, replayErrorWithMutation(typed(ErrorConcurrency, "publish rejected replay candidate", err), "publish rejected replay candidate", terminalCleanupMutation(draftMutation, false, true))
		}
		if err := p.checkpoint(PhaseDraftPublished); err != nil {
			return nil, replayErrorWithMutation(typed(ErrorIO, "publish rejected replay candidate", err), "publish rejected replay candidate", terminalCleanupMutation(draftMutation, true, true))
		}
		return replayResult(repository, state, plan, Rejected, applied.Changes, &validation), nil
	}
	return p.advanceReplay(ctx, root, state, plan, accepted)
}

func (p *Puller) advanceReplay(ctx context.Context, root string, state replayState, plan replayPlan, accepted *managedwrite.Result) (result *Result, resultErr error) {
	var repository *gitraw.Repository
	defer func() {
		if resultErr == nil || accepted == nil || !accepted.Created || accepted.Commit == nil || state.Private.Ref == nil || state.Private.Commit == nil {
			return
		}
		recoveryRequired := pullRecoveryRequired(repository)
		mutation := &Mutation{
			Durable:         true,
			LocalRefs:       []RefMutation{{Ref: *state.Private.Ref, Before: cloneString(state.Private.Commit), After: cloneString(accepted.Commit)}},
			Head:            &HeadMutation{Before: cloneGitState(state.Private), After: gitState(*state.Private.Ref, *accepted.Commit)},
			CheckoutChanged: false, RecoveryRequired: recoveryRequired,
		}
		resultErr = replayErrorWithObservedRecovery(resultErr, "", mutation, recoveryRequired)
	}()
	store, err := managedread.Open(ctx, root)
	if err != nil {
		return nil, classifyReadError(ctx, "observe replay progress", err)
	}
	repository = store.Repository()
	if repository.Head == nil {
		return nil, typed(ErrorRepository, "observe replay progress", errors.New("private replay branch is unborn"))
	}
	oldState, oldPlan := state, plan
	state.Private = gitState(repository.HeadRef, repository.Head.String())
	state.Reason, state.Conflicts = "rejected", []string{}
	plan.Replayed++
	plan.Next++
	plan.DraftReady = false
	if plan.Next < len(plan.Sources) {
		state.Base = managedread.GitState{Commit: stringPointer(plan.Sources[plan.Next].Base)}
		state.Reason, state.Conflicts = "rejected", []string{}
		if err := p.updateReplay(repository, oldState, oldPlan, state, plan); err != nil {
			return nil, typed(ErrorConcurrency, "advance replay state", err)
		}
		if err := p.checkpoint(PhaseReplayCommitted); err != nil {
			return nil, replayPairError(ErrorIO, "advance replay state", err, true, true)
		}
		return p.processAutomatic(ctx, root, state, plan)
	}
	if err := p.updateReplay(repository, oldState, oldPlan, state, plan); err != nil {
		return nil, typed(ErrorConcurrency, "record final replay commit", err)
	}
	if err := p.checkpoint(PhaseReplayCommitted); err != nil {
		return nil, replayPairError(ErrorIO, "record final replay commit", err, true, true)
	}
	return p.finishReplay(ctx, repository, state, plan, accepted)
}

func (p *Puller) finishReplay(ctx context.Context, repository *gitraw.Repository, state replayState, plan replayPlan, _ *managedwrite.Result) (*Result, error) {
	if state.Original.Ref == nil || state.Original.Commit == nil || state.Private.Ref == nil || state.Private.Commit == nil {
		return nil, typed(ErrorOperational, "finish replay", errors.New("replay Git states are incomplete"))
	}
	return p.beginReplayTerminal(ctx, repository, state, plan, replayFinalizing)
}

func replayResult(repository *gitraw.Repository, state replayState, plan replayPlan, outcome State, changes []changeset.Change, candidate *checker.Result) *Result {
	after := cloneGitState(state.Private)
	if repository != nil && repository.Head != nil {
		after = gitState(repository.HeadRef, repository.Head.String())
	}
	conflicts := make([]string, len(state.Conflicts))
	copy(conflicts, state.Conflicts)
	return &Result{
		State: outcome, Remote: plan.Remote, RemoteRef: plan.RemoteRef,
		Before: cloneGitState(plan.Original), After: after, Fetched: plan.Fetched, Replayed: plan.Replayed,
		Conflicts: conflicts, Changes: append([]changeset.Change(nil), changes...),
		Validation: cloneValidation(plan.Validation), CandidateValidation: cloneValidationPointer(candidate), Audits: cloneAudits(plan.Audits),
	}
}

func recapture(ctx context.Context, opened *gitraw.Repository) (*gitraw.Repository, error) {
	if opened == nil {
		return nil, typed(ErrorRepository, "capture repository", errors.New("nil repository"))
	}
	repository, err := gitraw.Discover(ctx, opened.Root)
	if err != nil {
		return nil, classifyReadError(ctx, "capture repository", err)
	}
	if !sameTopology(opened, repository) {
		return nil, typed(ErrorConcurrency, "capture repository", errors.New("repository topology changed"))
	}
	return repository, nil
}

func verifyRepository(ctx context.Context, repository *gitraw.Repository, tip string) error {
	current, err := gitraw.Discover(ctx, repository.Root)
	if err != nil {
		return classifyReadError(ctx, "verify repository", err)
	}
	if !sameTopology(repository, current) || current.Head == nil || current.HeadRef != repository.HeadRef || current.Head.String() != tip {
		return typed(ErrorConcurrency, "verify repository", errors.New("accepted HEAD/ref changed during pull"))
	}
	return nil
}

func requireCompleteLocal(audit *managedread.AcceptedAudit) error {
	if audit == nil || audit.Raw == nil || !audit.Raw.Complete || len(audit.Raw.Commits) == 0 || len(audit.Audits) != len(audit.Raw.Commits) || len(audit.Snapshots) != len(audit.Raw.Commits) {
		return errors.New("local accepted audit is incomplete")
	}
	for index, commit := range audit.Raw.Commits {
		if commit.Commit == nil || commit.Snapshot == nil || audit.Audits[index].Candidate != commit.ID.String() || audit.Audits[index].Validation.Status != checker.StatusComplete {
			return errors.New("local accepted audit lacks a complete transition")
		}
	}
	return nil
}

func gitState(ref, commit string) managedread.GitState {
	return managedread.GitState{Ref: stringPointer(ref), Commit: stringPointer(commit)}
}

func cloneValidation(value checker.Result) checker.Result {
	result := value
	result.Findings = make([]checker.Finding, len(value.Findings))
	copy(result.Findings, value.Findings)
	return result
}

func cloneValidationPointer(value *checker.Result) *checker.Result {
	if value == nil {
		return nil
	}
	copy := cloneValidation(*value)
	return &copy
}

func cloneAudits(values []managedread.HistoryAudit) []managedread.HistoryAudit {
	result := make([]managedread.HistoryAudit, len(values))
	for index, value := range values {
		result[index] = managedread.HistoryAudit{Base: cloneString(value.Base), Candidate: value.Candidate, Validation: cloneValidation(value.Validation)}
	}
	return result
}

func sortedRefUpdates(values []transitionRef) []transitionRef {
	result := append([]transitionRef(nil), values...)
	sort.Slice(result, func(i, j int) bool { return result[i].Ref < result[j].Ref })
	return result
}

func isCandidateRejection(err error) bool {
	if errors.Is(err, hookexec.ErrRejected) || managedwrite.KindOf(err) == managedwrite.FailureValidation {
		return true
	}
	var managed *managedwrite.Error
	return errors.As(err, &managed) && managed.Validation != nil && managed.Kind == managedwrite.FailureHook
}

func classifyWriterError(repository *gitraw.Repository, accepted *managedwrite.Result, err error) error {
	kind := ErrorOperational
	switch managedwrite.KindOf(err) {
	case managedwrite.FailureUsage:
		kind = ErrorUsage
	case managedwrite.FailureRepository:
		kind = ErrorRepository
	case managedwrite.FailureCapability:
		kind = ErrorCapability
	case managedwrite.FailureValidation:
		kind = ErrorRepository
	case managedwrite.FailureTrust:
		kind = ErrorTrust
	case managedwrite.FailureHook:
		kind = ErrorHook
	case managedwrite.FailureGuard:
		kind = ErrorIntegration
	case managedwrite.FailureConcurrency:
		kind = ErrorConcurrency
	case managedwrite.FailureRecovery:
		kind = ErrorConflict
	case managedwrite.FailureIO:
		kind = ErrorIO
	}
	result := &Error{Kind: kind, Operation: "accept replay candidate", Err: err, Mutation: &Mutation{LocalRefs: []RefMutation{}}}
	var managed *managedwrite.Error
	knownCommit := ""
	checkoutKnown := false
	if errors.As(err, &managed) {
		if managed.Accepted {
			knownCommit = managed.Commit
		}
		checkoutKnown = managed.CheckoutChanged
		result.Mutation.Durable = result.Mutation.Durable || managed.Durable
		result.Mutation.CheckoutChanged = result.Mutation.CheckoutChanged || managed.CheckoutChanged
		result.Mutation.RecoveryRequired = result.Mutation.RecoveryRequired || managed.RecoveryRequired
	}
	if accepted != nil && accepted.Created && accepted.Commit != nil {
		knownCommit = *accepted.Commit
		checkoutKnown = true
	}
	if repository != nil && repository.Head != nil && knownCommit != "" {
		acceptedMutation := &Mutation{
			Durable: true, LocalRefs: []RefMutation{{Ref: repository.HeadRef, Before: stringPointer(repository.Head.String()), After: stringPointer(knownCommit)}},
			Head: &HeadMutation{
				Before: gitState(repository.HeadRef, repository.Head.String()),
				After:  gitState(repository.HeadRef, knownCommit),
			},
			CheckoutChanged: checkoutKnown, RecoveryRequired: true,
		}
		result.Mutation = mergePullMutations(result.Mutation, acceptedMutation)
	}
	return result
}

func resultChanges(value *managedwrite.Result) []changeset.Change {
	if value == nil {
		return nil
	}
	return append([]changeset.Change(nil), value.Changes...)
}

func resultValidation(value *managedwrite.Result) *checker.Result {
	if value == nil {
		return nil
	}
	return cloneValidationPointer(value.Validation)
}

func stateFilesExist(repository *gitraw.Repository) bool {
	if repository == nil {
		return false
	}
	_, stateErr := os.Lstat(replayStatePath(repository))
	_, planErr := os.Lstat(replayPlanPath(repository))
	_, terminalErr := os.Lstat(replayTerminalPath(repository))
	_, pairErr := os.Lstat(replayPairJournalPath(repository))
	return stateErr == nil || planErr == nil || terminalErr == nil || pairErr == nil
}

func pullRecoveryRequired(repository *gitraw.Repository) bool {
	if repository == nil {
		return true
	}
	if stateFilesExist(repository) {
		return true
	}
	_, present, err := readControllerFile(transitionPath(repository))
	return err != nil || present
}

func (r *Result) String() string {
	return fmt.Sprintf("pull %s (%d fetched, %d replayed)", r.State, r.Fetched, r.Replayed)
}
