package pullflow

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/ontopix/engram/internal/changeset"
	"github.com/ontopix/engram/internal/gitraw"
	"github.com/ontopix/engram/internal/managedread"
	"github.com/ontopix/engram/internal/rendezvous"
)

type replayTerminalPhase string

const (
	replayFinalizing replayTerminalPhase = "finalizing"
	replayAborting   replayTerminalPhase = "aborting"
)

// replayTerminal is the single-file authority for a replay's last local
// transition. It is published before refs or checkout state can change and is
// removed only after both members of the active replay pair have been
// removed. Embedding the exact state and plan makes every partial cleanup
// independently recognizable and retryable.
type replayTerminal struct {
	Version     int                  `json:"version"`
	Phase       replayTerminalPhase  `json:"phase"`
	Owner       rendezvous.Owner     `json:"owner"`
	StateSHA256 string               `json:"state_sha256"`
	PlanSHA256  string               `json:"plan_sha256"`
	State       replayState          `json:"state"`
	Plan        replayPlan           `json:"plan"`
	HeadBefore  managedread.GitState `json:"head_before"`
	HeadAfter   managedread.GitState `json:"head_after"`
	Refs        []transitionRef      `json:"refs"`
}

type replayTerminalStage uint8

const (
	replayTerminalInconsistent replayTerminalStage = iota
	replayTerminalBefore
	replayTerminalAfter
)

var (
	errReplayTerminalChanged = errors.New("pull replay terminal state changed concurrently")
	activeReplayTerminals    sync.Map
)

func replayTerminalPath(repository *gitraw.Repository) string {
	return filepath.Join(replayDirectory(repository), "terminal-v1.json")
}

func newReplayTerminal(phase replayTerminalPhase, state replayState, plan replayPlan) (replayTerminal, error) {
	if err := validateReplayPair(nil, state, plan); err != nil {
		return replayTerminal{}, err
	}
	if state.Original.Ref == nil || state.Original.Commit == nil || state.Private.Ref == nil || state.Private.Commit == nil {
		return replayTerminal{}, errors.New("replay Git states are incomplete")
	}
	owner, err := newReplayOwner()
	if err != nil {
		return replayTerminal{}, err
	}
	after := cloneGitState(state.Original)
	switch phase {
	case replayFinalizing:
		after.Commit = cloneString(state.Private.Commit)
	case replayAborting:
	default:
		return replayTerminal{}, errors.New("unsupported replay terminal phase")
	}
	stateBytes, _ := encodeCanonical(state)
	planBytes, _ := encodeCanonical(plan)
	stateDigest := sha256.Sum256(stateBytes)
	planDigest := sha256.Sum256(planBytes)
	record := replayTerminal{
		Version: 1, Phase: phase, Owner: owner,
		StateSHA256: hex.EncodeToString(stateDigest[:]), PlanSHA256: hex.EncodeToString(planDigest[:]),
		State: state, Plan: plan, HeadBefore: cloneGitState(state.Private), HeadAfter: after,
	}
	record.Refs = terminalRefUpdates(record)
	if err := validateReplayTerminal(record); err != nil {
		return replayTerminal{}, err
	}
	return record, nil
}

func newReplayOwner() (rendezvous.Owner, error) {
	token := make([]byte, 32)
	if _, err := rand.Read(token); err != nil {
		return rendezvous.Owner{}, err
	}
	hostname, err := os.Hostname()
	if err != nil {
		return rendezvous.Owner{}, err
	}
	return rendezvous.Owner{
		Version: 1, Token: hex.EncodeToString(token), PID: os.Getpid(), Hostname: hostname,
		StartedAt: time.Now().UTC().Format(time.RFC3339Nano), Phase: rendezvous.JournalRequired,
	}, nil
}

func validateReplayOwner(owner rendezvous.Owner) error {
	if owner.Version != 1 || len(owner.Token) != 64 || !isLowerHexString(owner.Token) ||
		owner.PID <= 0 || owner.Hostname == "" || owner.StartedAt == "" || owner.Phase != rendezvous.JournalRequired {
		return errors.New("unsupported pull replay owner")
	}
	return nil
}

func terminalRefUpdates(record replayTerminal) []transitionRef {
	if record.State.Original.Ref == nil || record.State.Original.Commit == nil || record.State.Private.Ref == nil || record.State.Private.Commit == nil {
		return nil
	}
	originalAfter := cloneString(record.State.Original.Commit)
	if record.Phase == replayFinalizing {
		originalAfter = cloneString(record.State.Private.Commit)
	}
	return sortedRefUpdates([]transitionRef{
		{Ref: *record.State.Original.Ref, Before: cloneString(record.State.Original.Commit), After: originalAfter},
		{Ref: *record.State.Private.Ref, Before: cloneString(record.State.Private.Commit), After: nil},
	})
}

func validateReplayTerminal(record replayTerminal) error {
	if record.Version != 1 || record.Phase != replayFinalizing && record.Phase != replayAborting {
		return errors.New("unsupported pull replay terminal state")
	}
	if err := validateReplayOwner(record.Owner); err != nil {
		return err
	}
	if err := validateReplayPair(nil, record.State, record.Plan); err != nil {
		return fmt.Errorf("terminal replay pair: %w", err)
	}
	stateBytes, _ := encodeCanonical(record.State)
	planBytes, _ := encodeCanonical(record.Plan)
	stateDigest := sha256.Sum256(stateBytes)
	planDigest := sha256.Sum256(planBytes)
	if record.StateSHA256 != hex.EncodeToString(stateDigest[:]) || record.PlanSHA256 != hex.EncodeToString(planDigest[:]) {
		return errors.New("pull replay terminal digest does not bind its pair")
	}
	expected, err := newReplayTerminalWithoutOwner(record.Phase, record.State, record.Plan)
	if err != nil {
		return err
	}
	if !sameGitState(record.HeadBefore, expected.HeadBefore) || !sameGitState(record.HeadAfter, expected.HeadAfter) || !sameTransitionUpdates(record.Refs, expected.Refs) {
		return errors.New("pull replay terminal expectations do not match its pair")
	}
	return nil
}

func newReplayTerminalWithoutOwner(phase replayTerminalPhase, state replayState, plan replayPlan) (replayTerminal, error) {
	if state.Original.Ref == nil || state.Original.Commit == nil || state.Private.Ref == nil || state.Private.Commit == nil {
		return replayTerminal{}, errors.New("replay Git states are incomplete")
	}
	after := cloneGitState(state.Original)
	if phase == replayFinalizing {
		after.Commit = cloneString(state.Private.Commit)
	} else if phase != replayAborting {
		return replayTerminal{}, errors.New("unsupported replay terminal phase")
	}
	record := replayTerminal{Phase: phase, State: state, Plan: plan, HeadBefore: cloneGitState(state.Private), HeadAfter: after}
	record.Refs = terminalRefUpdates(record)
	return record, nil
}

func sameTransitionUpdates(left, right []transitionRef) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].Ref != right[index].Ref || !equalString(left[index].Before, right[index].Before) || !equalString(left[index].After, right[index].After) {
			return false
		}
	}
	return true
}

func isLowerHexString(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range []byte(value) {
		if character < '0' || character > '9' && character < 'a' || character > 'f' {
			return false
		}
	}
	return true
}

func publishReplayTerminalAfter(repository *gitraw.Repository, record replayTerminal, afterLink func() error) ([]byte, error) {
	if err := validateReplayTerminal(record); err != nil {
		return nil, err
	}
	raw, err := encodeCanonical(record)
	if err != nil {
		return nil, err
	}
	if err := createControllerFileAfter(replayTerminalPath(repository), raw, afterLink); err != nil {
		return nil, err
	}
	return raw, nil
}

func readReplayTerminal(repository *gitraw.Repository) (replayTerminal, []byte, bool, error) {
	if repository == nil {
		return replayTerminal{}, nil, false, errors.New("nil replay repository")
	}
	raw, present, err := readControllerFile(replayTerminalPath(repository))
	if err != nil || !present {
		return replayTerminal{}, raw, present, err
	}
	var record replayTerminal
	if err := decodeCanonical(raw, &record); err != nil {
		return replayTerminal{}, raw, true, err
	}
	if err := validateReplayTerminal(record); err != nil {
		return replayTerminal{}, raw, true, err
	}
	return record, raw, true, nil
}

func observeReplayTerminal(ctx context.Context, repository *gitraw.Repository, record replayTerminal) (replayTerminalStage, *gitraw.Repository, error) {
	current, err := gitraw.Discover(ctx, repository.Root)
	if err != nil {
		return replayTerminalInconsistent, repository, err
	}
	if !sameTopology(repository, current) {
		return replayTerminalInconsistent, current, errors.New("repository topology changed during replay terminal operation")
	}
	refsBefore, refsAfter := true, true
	for _, update := range record.Refs {
		value, err := resolveRef(ctx, current.Root, update.Ref, current.Format)
		if err != nil {
			return replayTerminalInconsistent, current, err
		}
		refsBefore = refsBefore && equalString(value, update.Before)
		refsAfter = refsAfter && equalString(value, update.After)
	}
	head := managedread.GitState{Ref: stringPointer(current.HeadRef)}
	if current.Head != nil {
		head.Commit = stringPointer(current.Head.String())
	}
	switch {
	case refsBefore && sameGitState(head, record.HeadBefore):
		return replayTerminalBefore, current, nil
	case refsAfter && sameGitState(head, record.HeadAfter):
		store, err := managedread.Open(ctx, current.Root)
		if err != nil {
			return replayTerminalInconsistent, current, err
		}
		status, err := store.Status(ctx)
		if err != nil || len(status.Staged) != 0 || len(status.Unstaged) != 0 {
			return replayTerminalInconsistent, current, errors.Join(err, errors.New("terminal replay checkout is not clean"))
		}
		return replayTerminalAfter, current, nil
	default:
		return replayTerminalInconsistent, current, errors.New("HEAD or refs have neither the exact replay preimage nor terminal state")
	}
}

func replayTerminalAuthorityMutation(durable, recoveryRequired bool) *Mutation {
	return &Mutation{Durable: durable, LocalRefs: []RefMutation{}, RecoveryRequired: recoveryRequired}
}

func terminalCleanupMutation(base *Mutation, durable, recoveryRequired bool) *Mutation {
	result := clonePullMutation(base)
	if result == nil {
		result = replayTerminalAuthorityMutation(false, recoveryRequired)
	}
	result.Durable = result.Durable || durable
	result.RecoveryRequired = recoveryRequired
	return result
}

func mergePullMutations(first, second *Mutation) *Mutation {
	if first == nil {
		return clonePullMutation(second)
	}
	if second == nil {
		return clonePullMutation(first)
	}
	result := clonePullMutation(first)
	result.Durable = result.Durable || second.Durable
	result.CheckoutChanged = result.CheckoutChanged || second.CheckoutChanged
	result.RecoveryRequired = result.RecoveryRequired || second.RecoveryRequired
	for _, update := range second.LocalRefs {
		duplicate := false
		for _, existing := range result.LocalRefs {
			if existing.Ref == update.Ref && equalString(existing.Before, update.Before) && equalString(existing.After, update.After) {
				duplicate = true
				break
			}
		}
		if !duplicate {
			result.LocalRefs = append(result.LocalRefs, RefMutation{Ref: update.Ref, Before: cloneString(update.Before), After: cloneString(update.After)})
		}
	}
	if result.Head == nil {
		if second.Head != nil {
			result.Head = &HeadMutation{Before: cloneGitState(second.Head.Before), After: cloneGitState(second.Head.After)}
		}
	} else if second.Head != nil {
		result.Head.After = cloneGitState(second.Head.After)
	}
	return result
}

func clonePullMutation(value *Mutation) *Mutation {
	if value == nil {
		return nil
	}
	result := &Mutation{Durable: value.Durable, LocalRefs: []RefMutation{}, CheckoutChanged: value.CheckoutChanged, RecoveryRequired: value.RecoveryRequired}
	for _, update := range value.LocalRefs {
		result.LocalRefs = append(result.LocalRefs, RefMutation{Ref: update.Ref, Before: cloneString(update.Before), After: cloneString(update.After)})
	}
	if value.Head != nil {
		result.Head = &HeadMutation{Before: cloneGitState(value.Head.Before), After: cloneGitState(value.Head.After)}
	}
	return result
}

func replayErrorWithMutation(err error, operation string, mutation *Mutation) error {
	if err == nil {
		err = ErrRecovery
	}
	kind := KindOf(err)
	if kind == "" {
		kind = ErrorOperational
	}
	if operation == "" {
		var detail *Error
		if errors.As(err, &detail) {
			operation = detail.Operation
		}
	}
	return &Error{Kind: kind, Operation: operation, Mutation: mergePullMutations(mutation, MutationOf(err)), Err: err}
}

// replayErrorWithObservedRecovery merges per-invocation effects like the
// ordinary wrapper, then lets an exact final filesystem observation override
// stale recovery_required values carried by inner errors. Recovery state can
// be cleaned later in the same invocation, so that field is not monotonic.
func replayErrorWithObservedRecovery(err error, operation string, mutation *Mutation, recoveryRequired bool) error {
	wrapped := replayErrorWithMutation(err, operation, mutation)
	if detail, ok := wrapped.(*Error); ok && detail.Mutation != nil {
		detail.Mutation.RecoveryRequired = recoveryRequired
	}
	return wrapped
}

func (p *Puller) removeReplayTerminalPair(repository *gitraw.Repository, record replayTerminal, mutation *Mutation) (bool, error) {
	mutated := false
	removePairMember := func(name string, expected []byte, phase Phase) error {
		current, present, err := readControllerFile(name)
		if err != nil {
			return err
		}
		if !present {
			return nil
		}
		if !bytes.Equal(current, expected) {
			return errReplayTerminalChanged
		}
		if err := os.Remove(name); err != nil {
			return err
		}
		mutated = true
		if err := syncDirectory(replayDirectory(repository)); err != nil {
			return err
		}
		return p.checkpoint(phase)
	}
	stateBytes, _ := encodeCanonical(record.State)
	planBytes, _ := encodeCanonical(record.Plan)
	if err := removePairMember(replayStatePath(repository), stateBytes, PhaseReplayStateRemoved); err != nil {
		return mutated, replayErrorWithMutation(classifyReplayTerminalError("remove terminal replay state", err), "remove terminal replay state", terminalCleanupMutation(mutation, mutated, true))
	}
	if err := removePairMember(replayPlanPath(repository), planBytes, PhaseReplayPlanRemoved); err != nil {
		return mutated, replayErrorWithMutation(classifyReplayTerminalError("remove terminal replay plan", err), "remove terminal replay plan", terminalCleanupMutation(mutation, mutated, true))
	}
	return mutated, nil
}

func (p *Puller) removeReplayTerminalAuthority(repository *gitraw.Repository, record replayTerminal, raw []byte, mutation *Mutation) (bool, error) {
	removed := false
	err := withReplayLock(repository, func() error {
		current, present, readErr := readControllerFile(replayTerminalPath(repository))
		if readErr != nil || !present || !bytes.Equal(current, raw) {
			return errors.Join(readErr, errReplayTerminalChanged)
		}
		if removeErr := os.Remove(replayTerminalPath(repository)); removeErr != nil {
			return removeErr
		}
		removed = true
		if syncErr := syncDirectory(replayDirectory(repository)); syncErr != nil {
			return syncErr
		}
		return p.checkpoint(PhaseReplayTerminalRemoved)
	})
	if err != nil {
		return removed, replayErrorWithMutation(classifyReplayTerminalError("remove terminal replay authority", err), "remove terminal replay authority", terminalCleanupMutation(mutation, removed, !removed))
	}
	return true, nil
}

func classifyReplayTerminalError(operation string, err error) error {
	if errors.Is(err, errReplayTerminalChanged) {
		return typed(ErrorConcurrency, operation, err)
	}
	return typed(ErrorIO, operation, err)
}

func terminalResult(ctx context.Context, repository *gitraw.Repository, record replayTerminal) (*Result, error) {
	switch record.Phase {
	case replayAborting:
		result := replayResult(repository, record.State, record.Plan, Aborted, nil, nil)
		result.After = cloneGitState(record.HeadAfter)
		result.Conflicts = []string{}
		return result, nil
	case replayFinalizing:
		original, err := snapshotAt(ctx, repository, *record.State.Original.Commit)
		if err != nil {
			return nil, classifyReadError(ctx, "read original replay snapshot", err)
		}
		current, err := snapshotAt(ctx, repository, *record.State.Private.Commit)
		if err != nil {
			return nil, classifyReadError(ctx, "read completed private replay", err)
		}
		result := replayResult(repository, record.State, record.Plan, Replayed, changeset.Diff(original.Tree, current.Tree), nil)
		result.After = cloneGitState(record.HeadAfter)
		return result, nil
	default:
		return nil, typed(ErrorOperational, "finish replay", errors.New("unsupported replay terminal phase"))
	}
}
