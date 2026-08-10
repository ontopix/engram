package pullflow

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/ontopix/engram/internal/checker"
	"github.com/ontopix/engram/internal/gitraw"
	"github.com/ontopix/engram/internal/rendezvous"
)

type replayPairOperation string

const (
	replayPairPublish replayPairOperation = "publish"
	replayPairUpdate  replayPairOperation = "update"
)

// replayPairJournal is the one-file authority for a multi-file replay-pair
// publication. It is installed before either pair member changes and removed
// last. A crash therefore leaves an exact, bounded roll-forward/cleanup plan
// instead of an ambiguous state-v1.json/plan-v1.json combination.
type replayPairJournal struct {
	Version     int                 `json:"version"`
	Operation   replayPairOperation `json:"operation"`
	Owner       rendezvous.Owner    `json:"owner"`
	BeforeState *replayState        `json:"before_state"`
	BeforePlan  *replayPlan         `json:"before_plan"`
	AfterState  replayState         `json:"after_state"`
	AfterPlan   replayPlan          `json:"after_plan"`
}

type replayPairStage uint8

const (
	replayPairInconsistent replayPairStage = iota
	replayPairBefore
	replayPairPlanInstalled
	replayPairAfter
)

type replayPublishStage uint8

const (
	replayPublishInconsistent replayPublishStage = iota
	replayPublishBefore
	replayPublishActivated
)

var (
	errReplayPairChanged = errors.New("pull replay pair journal changed concurrently")
	activeReplayPairs    sync.Map
)

func replayPairJournalPath(repository *gitraw.Repository) string {
	return filepath.Join(replayDirectory(repository), "pair-v1.json")
}

func newReplayPairJournal(operation replayPairOperation, beforeState *replayState, beforePlan *replayPlan, afterState replayState, afterPlan replayPlan) (replayPairJournal, error) {
	owner, err := newReplayOwner()
	if err != nil {
		return replayPairJournal{}, err
	}
	record := replayPairJournal{
		Version: 1, Operation: operation, Owner: owner,
		BeforeState: cloneReplayStatePointer(beforeState), BeforePlan: cloneReplayPlanPointer(beforePlan),
		AfterState: afterState, AfterPlan: afterPlan,
	}
	if err := validateReplayPairJournal(record); err != nil {
		return replayPairJournal{}, err
	}
	return record, nil
}

func validateReplayPairJournal(record replayPairJournal) error {
	if record.Version != 1 || record.Operation != replayPairPublish && record.Operation != replayPairUpdate {
		return errors.New("unsupported pull replay pair journal")
	}
	if err := validateReplayOwner(record.Owner); err != nil {
		return err
	}
	if err := validateReplayPair(nil, record.AfterState, record.AfterPlan); err != nil {
		return fmt.Errorf("final replay pair: %w", err)
	}
	switch record.Operation {
	case replayPairPublish:
		if record.BeforeState != nil || record.BeforePlan != nil || record.AfterPlan.Next != 0 || record.AfterPlan.Replayed != 0 || record.AfterPlan.DraftReady || record.AfterState.Reason != "rejected" || len(record.AfterState.Conflicts) != 0 || record.AfterState.Private.Commit == nil || *record.AfterState.Private.Commit != record.AfterPlan.RemoteTip {
			return errors.New("invalid initial replay pair publication")
		}
	case replayPairUpdate:
		if record.BeforeState == nil || record.BeforePlan == nil {
			return errors.New("replay pair update lacks its exact preimage")
		}
		if err := validateReplayPair(nil, *record.BeforeState, *record.BeforePlan); err != nil {
			return fmt.Errorf("initial replay pair: %w", err)
		}
		if !sameReplayPlanIdentity(*record.BeforePlan, record.AfterPlan) || !sameGitState(record.BeforeState.Original, record.AfterState.Original) || !equalString(record.BeforeState.Private.Ref, record.AfterState.Private.Ref) {
			return errors.New("replay pair update changes immutable identity")
		}
		advance := record.AfterPlan.Next - record.BeforePlan.Next
		if advance < 0 || advance > 1 {
			return errors.New("replay pair update has invalid progress")
		}
		if advance == 0 && (!sameGitState(record.BeforeState.Private, record.AfterState.Private) || !sameGitState(record.BeforeState.Base, record.AfterState.Base)) {
			return errors.New("replay pair metadata update changes Git progress")
		}
		if advance == 1 && (record.AfterPlan.DraftReady || record.AfterState.Reason != "rejected" || len(record.AfterState.Conflicts) != 0) {
			return errors.New("replay pair progress update retains a draft")
		}
	}
	return nil
}

func sameReplayPlanIdentity(left, right replayPlan) bool {
	left.Next, left.Replayed, left.DraftReady = 0, 0, false
	right.Next, right.Replayed, right.DraftReady = 0, 0, false
	leftBytes, leftErr := encodeCanonical(left)
	rightBytes, rightErr := encodeCanonical(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftBytes, rightBytes)
}

func cloneReplayStatePointer(value *replayState) *replayState {
	if value == nil {
		return nil
	}
	result := *value
	result.Original = cloneGitState(value.Original)
	result.Private = cloneGitState(value.Private)
	result.Base = cloneGitState(value.Base)
	result.Conflicts = make([]string, len(value.Conflicts))
	copy(result.Conflicts, value.Conflicts)
	return &result
}

func cloneReplayPlanPointer(value *replayPlan) *replayPlan {
	if value == nil {
		return nil
	}
	result := *value
	result.Original = cloneGitState(value.Original)
	result.Sources = append([]sourceCommit(nil), value.Sources...)
	result.Validation = value.Validation
	if value.Validation.Findings != nil {
		result.Validation.Findings = make([]checker.Finding, len(value.Validation.Findings))
		copy(result.Validation.Findings, value.Validation.Findings)
	}
	if value.Audits != nil {
		result.Audits = cloneAudits(value.Audits)
	}
	return &result
}

func readReplayPairJournal(repository *gitraw.Repository) (replayPairJournal, []byte, bool, error) {
	if repository == nil {
		return replayPairJournal{}, nil, false, errors.New("nil replay repository")
	}
	raw, present, err := readControllerFile(replayPairJournalPath(repository))
	if err != nil || !present {
		return replayPairJournal{}, raw, present, err
	}
	var record replayPairJournal
	if err := decodeCanonical(raw, &record); err != nil {
		return replayPairJournal{}, raw, true, err
	}
	if err := validateReplayPairJournal(record); err != nil {
		return replayPairJournal{}, raw, true, err
	}
	return record, raw, true, nil
}

func observeReplayPairFiles(repository *gitraw.Repository, record replayPairJournal) (replayPairStage, error) {
	stateRaw, statePresent, stateErr := readControllerFile(replayStatePath(repository))
	if stateErr != nil {
		return replayPairInconsistent, stateErr
	}
	planRaw, planPresent, planErr := readControllerFile(replayPlanPath(repository))
	if planErr != nil {
		return replayPairInconsistent, planErr
	}
	afterState, _ := encodeCanonical(record.AfterState)
	afterPlan, _ := encodeCanonical(record.AfterPlan)
	if record.Operation == replayPairPublish {
		switch {
		case !statePresent && !planPresent:
			return replayPairBefore, nil
		case !statePresent && planPresent && bytes.Equal(planRaw, afterPlan):
			return replayPairPlanInstalled, nil
		case statePresent && planPresent && bytes.Equal(stateRaw, afterState) && bytes.Equal(planRaw, afterPlan):
			return replayPairAfter, nil
		default:
			return replayPairInconsistent, errors.New("published replay pair has neither an exact absent, partial, nor final image")
		}
	}
	beforeState, _ := encodeCanonical(*record.BeforeState)
	beforePlan, _ := encodeCanonical(*record.BeforePlan)
	switch {
	case statePresent && planPresent && bytes.Equal(stateRaw, beforeState) && bytes.Equal(planRaw, beforePlan):
		return replayPairBefore, nil
	case statePresent && planPresent && bytes.Equal(stateRaw, beforeState) && bytes.Equal(planRaw, afterPlan):
		return replayPairPlanInstalled, nil
	case statePresent && planPresent && bytes.Equal(stateRaw, afterState) && bytes.Equal(planRaw, afterPlan):
		return replayPairAfter, nil
	default:
		return replayPairInconsistent, errors.New("updated replay pair has neither its exact preimage, partial image, nor final image")
	}
}

func observeReplayPublication(ctx context.Context, repository *gitraw.Repository, record replayPairJournal) (replayPublishStage, *gitraw.Repository, error) {
	current, err := gitraw.Discover(ctx, repository.Root)
	if err != nil {
		return replayPublishInconsistent, repository, err
	}
	if !sameTopology(repository, current) || current.Head == nil || record.AfterState.Original.Ref == nil || record.AfterState.Original.Commit == nil || record.AfterState.Private.Ref == nil || record.AfterState.Private.Commit == nil {
		return replayPublishInconsistent, current, errors.New("repository topology or replay Git states changed")
	}
	private, err := resolveRef(ctx, current.Root, *record.AfterState.Private.Ref, current.Format)
	if err != nil {
		return replayPublishInconsistent, current, err
	}
	switch {
	case current.HeadRef == *record.AfterState.Original.Ref && current.Head.String() == *record.AfterState.Original.Commit && private == nil:
		return replayPublishBefore, current, nil
	case current.HeadRef == *record.AfterState.Private.Ref && current.Head.String() == *record.AfterState.Private.Commit && equalString(private, record.AfterState.Private.Commit):
		if err := validateReplayPair(current, record.AfterState, record.AfterPlan); err != nil {
			return replayPublishInconsistent, current, err
		}
		return replayPublishActivated, current, nil
	default:
		return replayPublishInconsistent, current, errors.New("repository has neither the replay publication preimage nor activated image")
	}
}

func replayPairMutation(durable, recoveryRequired bool) *Mutation {
	return &Mutation{Durable: durable, LocalRefs: []RefMutation{}, RecoveryRequired: recoveryRequired}
}

func replayPairError(kind ErrorKind, operation string, err error, durable, recoveryRequired bool) error {
	if recoveryRequired {
		err = errors.Join(ErrRecovery, err)
	}
	return &Error{Kind: kind, Operation: operation, Mutation: replayPairMutation(durable, recoveryRequired), Err: err}
}

func replayPairErrorKind(err error) ErrorKind {
	if errors.Is(err, errReplayControllerBusy) || errors.Is(err, errReplayPairChanged) {
		return ErrorConcurrency
	}
	return ErrorIO
}

func removeControllerFileExact(name string, expected []byte) (bool, error) {
	current, present, err := readControllerFile(name)
	if err != nil || !present || !bytes.Equal(current, expected) {
		return false, errors.Join(err, errReplayPairChanged)
	}
	if err := os.Remove(name); err != nil {
		return false, err
	}
	return true, syncDirectory(filepath.Dir(name))
}

// publishReplay installs durable publication authority first and deliberately
// leaves it in place until startReplay proves activation complete.
func (p *Puller) publishReplay(repository *gitraw.Repository, state replayState, plan replayPlan) (replayPairJournal, []byte, error) {
	record, err := newReplayPairJournal(replayPairPublish, nil, nil, state, plan)
	if err != nil {
		return replayPairJournal{}, nil, err
	}
	raw, err := encodeCanonical(record)
	if err != nil {
		return record, nil, err
	}
	activeReplayPairs.Store(record.Owner.Token, struct{}{})
	journalPublished, journalDurable := false, false
	err = withReplayLock(repository, func() error {
		if _, _, present, readErr := readReplayPairJournal(repository); readErr != nil || present {
			return errors.Join(readErr, errors.New("pull replay pair journal already exists"))
		}
		if _, present, readErr := readControllerFile(replayPlanPath(repository)); readErr != nil || present {
			return errors.Join(readErr, errors.New("pull replay plan already exists"))
		}
		if _, present, readErr := readControllerFile(replayStatePath(repository)); readErr != nil || present {
			return errors.Join(readErr, errors.New("pull replay state already exists"))
		}
		if createErr := createControllerFile(replayPairJournalPath(repository), raw); createErr != nil {
			journalPublished = controllerFilePublished(createErr)
			journalDurable = controllerFileDurable(createErr)
			return createErr
		}
		journalPublished, journalDurable = true, true
		planBytes, _ := encodeCanonical(plan)
		if createErr := createControllerFileAfter(replayPlanPath(repository), planBytes, func() error {
			return p.checkpoint(PhaseReplayPlanPublished)
		}); createErr != nil {
			return createErr
		}
		stateBytes, _ := encodeCanonical(state)
		return createControllerFileAfter(replayStatePath(repository), stateBytes, func() error {
			return p.checkpoint(PhaseReplayStatePublished)
		})
	})
	if err == nil {
		return record, raw, nil
	}
	if !journalPublished {
		activeReplayPairs.Delete(record.Owner.Token)
		kind := replayPairErrorKind(err)
		return record, raw, typed(kind, "publish replay pair authority", err)
	}
	return record, raw, replayPairError(replayPairErrorKind(err), "publish replay pair", err, journalDurable, true)
}

func (p *Puller) completeReplayPublication(ctx context.Context, repository *gitraw.Repository, record replayPairJournal, raw []byte) error {
	removed := false
	err := withReplayLock(repository, func() error {
		currentRaw, present, readErr := readControllerFile(replayPairJournalPath(repository))
		if readErr != nil || !present || !bytes.Equal(currentRaw, raw) {
			return errors.Join(readErr, errReplayPairChanged)
		}
		stage, stageErr := observeReplayPairFiles(repository, record)
		if stageErr != nil || stage != replayPairAfter {
			return errors.Join(stageErr, errors.New("replay pair publication is incomplete"))
		}
		activation, _, activationErr := observeReplayPublication(ctx, repository, record)
		if activationErr != nil || activation != replayPublishActivated {
			return errors.Join(activationErr, errors.New("replay pair publication is not activated"))
		}
		var removeErr error
		removed, removeErr = removeControllerFileExact(replayPairJournalPath(repository), raw)
		return removeErr
	})
	if err == nil {
		return nil
	}
	return replayPairError(replayPairErrorKind(err), "complete replay pair publication", err, true, !removed)
}

func (p *Puller) updateReplay(repository *gitraw.Repository, oldState replayState, oldPlan replayPlan, state replayState, plan replayPlan) error {
	record, err := newReplayPairJournal(replayPairUpdate, &oldState, &oldPlan, state, plan)
	if err != nil {
		return err
	}
	raw, err := encodeCanonical(record)
	if err != nil {
		return err
	}
	activeReplayPairs.Store(record.Owner.Token, struct{}{})
	defer activeReplayPairs.Delete(record.Owner.Token)
	journalPublished, journalDurable, markerRemoved := false, false, false
	err = withReplayLock(repository, func() error {
		if _, _, present, readErr := readReplayPairJournal(repository); readErr != nil || present {
			return errors.Join(readErr, errors.New("pull replay pair journal already exists"))
		}
		oldStateBytes, _ := encodeCanonical(oldState)
		oldPlanBytes, _ := encodeCanonical(oldPlan)
		currentState, present, readErr := readControllerFile(replayStatePath(repository))
		if readErr != nil || !present || !bytes.Equal(currentState, oldStateBytes) {
			return errors.Join(readErr, errors.New("pull replay state changed concurrently"))
		}
		currentPlan, present, readErr := readControllerFile(replayPlanPath(repository))
		if readErr != nil || !present || !bytes.Equal(currentPlan, oldPlanBytes) {
			return errors.Join(readErr, errors.New("pull replay plan changed concurrently"))
		}
		if createErr := createControllerFileAfter(replayPairJournalPath(repository), raw, func() error {
			return p.checkpoint(PhaseReplayUpdatePublished)
		}); createErr != nil {
			journalPublished = controllerFilePublished(createErr)
			journalDurable = controllerFileDurable(createErr)
			return createErr
		}
		journalPublished, journalDurable = true, true
		planBytes, _ := encodeCanonical(plan)
		if replaceErr := replaceControllerFile(replayPlanPath(repository), planBytes); replaceErr != nil {
			return replaceErr
		}
		if checkpointErr := p.checkpoint(PhaseReplayUpdatePlan); checkpointErr != nil {
			return checkpointErr
		}
		stateBytes, _ := encodeCanonical(state)
		if replaceErr := replaceControllerFile(replayStatePath(repository), stateBytes); replaceErr != nil {
			return replaceErr
		}
		if checkpointErr := p.checkpoint(PhaseReplayUpdateState); checkpointErr != nil {
			return checkpointErr
		}
		var removeErr error
		markerRemoved, removeErr = removeControllerFileExact(replayPairJournalPath(repository), raw)
		return removeErr
	})
	if err == nil {
		return nil
	}
	if !journalPublished {
		return typed(replayPairErrorKind(err), "publish replay pair update authority", err)
	}
	return replayPairError(replayPairErrorKind(err), "update replay pair", err, journalDurable, !markerRemoved)
}

func (p *Puller) recoverReplayPair(ctx context.Context, repository *gitraw.Repository, record replayPairJournal, raw []byte, expected *RecoveryExpectation) (*RecoveryResult, error) {
	base := &RecoveryResult{Needed: true, RecoveryRequired: true, Mutation: replayPairMutation(false, true)}
	if expected != nil {
		digest := sha256.Sum256(raw)
		if expected.OwnerToken == "" || expected.StateSHA256 == "" || record.Owner.Token != expected.OwnerToken || hex.EncodeToString(digest[:]) != expected.StateSHA256 {
			return base, replayPairError(ErrorConcurrency, "read approved replay pair journal", errReplayPairChanged, false, true)
		}
	}
	if _, active := activeReplayPairs.Load(record.Owner.Token); active {
		return base, replayPairError(ErrorConcurrency, "recover replay pair", errors.New("replay pair owner is still active"), false, true)
	}
	dead, err := ownerIsDead(record.Owner)
	if err != nil || !dead {
		return base, replayPairError(ErrorConflict, "recover replay pair", errors.Join(err, errors.New("replay pair owner death is not proven")), false, true)
	}
	activeReplayPairs.Store(record.Owner.Token, struct{}{})
	defer activeReplayPairs.Delete(record.Owner.Token)
	mutated, markerRemoved := false, false
	err = withReplayLock(repository, func() error {
		currentRaw, present, readErr := readControllerFile(replayPairJournalPath(repository))
		if readErr != nil || !present || !bytes.Equal(currentRaw, raw) {
			return errors.Join(readErr, errReplayPairChanged)
		}
		stage, stageErr := observeReplayPairFiles(repository, record)
		if stageErr != nil {
			return stageErr
		}
		if record.Operation == replayPairPublish {
			if _, transitionPresent, transitionErr := readControllerFile(transitionPath(repository)); transitionErr != nil || transitionPresent {
				return errors.Join(transitionErr, errors.New("replay activation transition still requires recovery"))
			}
			publication, _, publicationErr := observeReplayPublication(ctx, repository, record)
			if publicationErr != nil {
				return publicationErr
			}
			switch publication {
			case replayPublishBefore:
				if stage == replayPairAfter {
					stateBytes, _ := encodeCanonical(record.AfterState)
					removed, removeErr := removeControllerFileExact(replayStatePath(repository), stateBytes)
					mutated = mutated || removed
					if removeErr != nil {
						return removeErr
					}
				}
				if stage == replayPairAfter || stage == replayPairPlanInstalled {
					planBytes, _ := encodeCanonical(record.AfterPlan)
					removed, removeErr := removeControllerFileExact(replayPlanPath(repository), planBytes)
					mutated = mutated || removed
					if removeErr != nil {
						return removeErr
					}
				}
			case replayPublishActivated:
				if stage != replayPairAfter {
					return errors.New("activated replay lacks its exact complete pair")
				}
			default:
				return errors.New("replay publication is not safely recoverable")
			}
		} else {
			switch stage {
			case replayPairBefore:
			case replayPairPlanInstalled:
				stateBytes, _ := encodeCanonical(record.AfterState)
				if replaceErr := replaceControllerFile(replayStatePath(repository), stateBytes); replaceErr != nil {
					return replaceErr
				}
				mutated = true
				if checkpointErr := p.checkpoint(PhaseReplayUpdateState); checkpointErr != nil {
					return checkpointErr
				}
			case replayPairAfter:
			default:
				return errors.New("replay pair update is not safely recoverable")
			}
			current, discoverErr := gitraw.Discover(ctx, repository.Root)
			if discoverErr != nil || !sameTopology(repository, current) {
				return errors.Join(discoverErr, errors.New("repository topology changed during replay pair recovery"))
			}
			if stage != replayPairBefore {
				if validateErr := validateReplayPair(current, record.AfterState, record.AfterPlan); validateErr != nil {
					return validateErr
				}
			}
		}
		var removeErr error
		markerRemoved, removeErr = removeControllerFileExact(replayPairJournalPath(repository), raw)
		mutated = mutated || markerRemoved
		return removeErr
	})
	if err != nil {
		base.Durable = mutated
		base.RecoveryRequired = !markerRemoved
		base.Mutation = replayPairMutation(mutated, !markerRemoved)
		return base, replayPairError(replayPairErrorKind(err), "recover replay pair", err, mutated, !markerRemoved)
	}
	base.Performed = true
	base.Durable = mutated
	base.RecoveryRequired = false
	base.Mutation = replayPairMutation(mutated, false)
	return base, nil
}
