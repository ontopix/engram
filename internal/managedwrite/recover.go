package managedwrite

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ontopix/engram/internal/gitraw"
	"github.com/ontopix/engram/internal/journal"
	"github.com/ontopix/engram/internal/rendezvous"
)

// Recover performs only the bounded cases in annex-git B.4. A recognized but
// ambiguous pending-old/other state returns FailureRecovery and deliberately
// retains the adopted rendezvous locks and journal.
func (e *Engine) Recover(ctx context.Context, storeRoot string) (*RecoveryResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if e == nil || storeRoot == "" {
		return nil, typed(FailureUsage, PhaseCaptured, ErrUsage)
	}
	topology, err := gitraw.DiscoverTopology(ctx, storeRoot)
	if err != nil {
		return nil, classify(PhaseCaptured, err)
	}
	journalPath := journal.Path(topology.GitDir)
	record, journalBytes, journalErr := journal.Read(journalPath)
	if errors.Is(journalErr, os.ErrNotExist) {
		return e.recoverWithoutJournal(ctx, topology)
	}
	if journalErr != nil {
		return &RecoveryResult{Needed: true}, typed(FailureRecovery, PhaseCaptured, errors.Join(ErrRecovery, journalErr))
	}
	if record.ObjectFormat != topology.Format {
		return &RecoveryResult{Needed: true}, typed(FailureRecovery, PhaseCaptured, fmt.Errorf("%w: journal object format does not match repository", ErrRecovery))
	}

	lease, err := rendezvous.AcquireRecovery(topology.GitDir)
	if err != nil {
		return &RecoveryResult{Needed: true}, classify(PhaseLocked, err)
	}
	defer lease.Release()
	// Re-read after acquiring recovery exclusion; a normal owner may have
	// completed cleanup while we waited.
	record, observedAgain, err := journal.Read(journalPath)
	if errors.Is(err, os.ErrNotExist) {
		return &RecoveryResult{Action: RecoveryNone}, nil
	}
	if err != nil || !bytes.Equal(observedAgain, journalBytes) {
		return &RecoveryResult{Needed: true}, typed(FailureRecovery, PhaseCaptured, errors.Join(ErrRecovery, err, journal.ErrChanged))
	}

	refLock := rendezvous.RefPath(topology.CommonGitDir, record.Ref.Ref)
	worktreeLock := rendezvous.WorktreePath(topology.GitDir)
	lockPaths, owner, phase, err := recognizedJournalLocks(record, refLock, worktreeLock)
	if err != nil {
		return &RecoveryResult{Needed: true}, err
	}
	if len(lockPaths) == 0 {
		owner = rendezvous.Owner{
			Version: 1, Token: record.OwnerToken, PID: record.Owner.PID,
			Hostname: record.Owner.Hostname, StartedAt: record.Owner.StartedAt,
			Phase: rendezvous.JournalRequired,
		}
	}
	alive, err := e.ownerAlive(ctx, owner)
	if err != nil {
		return &RecoveryResult{Needed: true}, typed(FailureRecovery, PhaseLocked, errors.Join(ErrRecovery, err))
	}
	if alive {
		return &RecoveryResult{Needed: true}, typed(FailureRecovery, PhaseLocked, fmt.Errorf("%w: transaction owner is still alive", ErrRecovery))
	}

	var handle *rendezvous.Handle
	if len(lockPaths) != 0 {
		if len(lockPaths) == 2 {
			handle, err = lease.AdoptWriter(topology.CommonGitDir, topology.GitDir, record.OwnerToken, phase, record.Ref.Ref)
		} else {
			handle, err = lease.AdoptPaths(record.OwnerToken, phase, lockPaths...)
		}
		if err != nil {
			return &RecoveryResult{Needed: true}, typed(FailureRecovery, PhaseLocked, errors.Join(ErrRecovery, err))
		}
		e.markActive(record.OwnerToken, true)
	}
	finishActive := func() {
		if handle != nil {
			e.markActive(record.OwnerToken, false)
		}
	}
	defer finishActive()

	switch record.State {
	case journal.Cancelled:
		if len(lockPaths) != 2 {
			return &RecoveryResult{Needed: true}, typed(FailureRecovery, PhaseLocked, fmt.Errorf("%w: cancelled journal lacks exact locks", ErrRecovery))
		}
		if err := journal.CleanupOwnedTemporaries(journalPath, journalBytes); err != nil {
			return &RecoveryResult{Needed: true}, typed(FailureRecovery, PhaseJournalRemoved, err)
		}
		if err := handle.Release(); err != nil {
			return &RecoveryResult{Needed: true}, typed(FailureRecovery, PhaseLocksReleased, err)
		}
		e.markActive(record.OwnerToken, false)
		handle = nil
		if err := journal.Remove(journalPath, journalBytes); err != nil {
			return &RecoveryResult{Needed: true}, typed(FailureRecovery, PhaseJournalRemoved, err)
		}
		return &RecoveryResult{Needed: true, Performed: true, Action: RecoveryCancelled}, nil

	case journal.Complete:
		if err := journal.CleanupOwnedTemporaries(journalPath, journalBytes); err != nil {
			return &RecoveryResult{Needed: true}, typed(FailureRecovery, PhaseJournalRemoved, err)
		}
		if handle != nil {
			if err := handle.Release(); err != nil {
				return &RecoveryResult{Needed: true}, typed(FailureRecovery, PhaseLocksReleased, err)
			}
			e.markActive(record.OwnerToken, false)
			handle = nil
		}
		if err := journal.Remove(journalPath, journalBytes); err != nil {
			return &RecoveryResult{Needed: true}, typed(FailureRecovery, PhaseJournalRemoved, err)
		}
		return &RecoveryResult{Needed: true, Performed: true, Action: RecoveryCompleted, Accepted: stringPointer(record.Ref.After)}, nil

	case journal.Pending:
		if len(lockPaths) != 2 || phase != rendezvous.JournalRequired {
			return &RecoveryResult{Needed: true}, typed(FailureRecovery, PhaseLocked, fmt.Errorf("%w: pending journal lacks exact journal-required locks", ErrRecovery))
		}
		git, err := newGitClient(topology.Root)
		if err != nil {
			return &RecoveryResult{Needed: true}, typed(FailureCapability, PhaseCaptured, err)
		}
		current, err := stableRecoveryRef(ctx, git, topology, record.Ref.Ref)
		if err != nil {
			return &RecoveryResult{Needed: true}, typed(FailureRecovery, PhaseFinalRecheck, errors.Join(ErrRecovery, err))
		}
		if current == nil || *current != record.Ref.After {
			reason := "accepted ref has an unrelated value"
			if sameOptionalString(current, record.Ref.Before) {
				reason = "accepted ref equals the old value; old-to-new-to-old remains possible"
			}
			return &RecoveryResult{Needed: true, Accepted: cloneOptionalString(current)}, typed(FailureRecovery, PhaseRefUpdated, fmt.Errorf("%w: %s", ErrRecovery, reason))
		}
		if err := verifyRecoverableInputs(ctx, git, topology.Root, record); err != nil {
			return &RecoveryResult{Needed: true, Accepted: stringPointer(record.Ref.After)}, err
		}
		if err := reconcileIndex(ctx, topology.Root, record); err != nil {
			return &RecoveryResult{Needed: true, Accepted: stringPointer(record.Ref.After)}, typed(FailureRecovery, PhaseIndexReconciled, err)
		}
		if err := reconcileWorktree(topology.Root, record); err != nil {
			return &RecoveryResult{Needed: true, Accepted: stringPointer(record.Ref.After)}, typed(FailureRecovery, PhaseWorktreeReconciled, err)
		}
		completeBytes, err := journal.SetState(journalPath, journalBytes, journal.Complete)
		if err != nil {
			return &RecoveryResult{Needed: true, Accepted: stringPointer(record.Ref.After)}, typed(FailureRecovery, PhaseJournalComplete, err)
		}
		if err := handle.Release(); err != nil {
			return &RecoveryResult{Needed: true, Accepted: stringPointer(record.Ref.After)}, typed(FailureRecovery, PhaseLocksReleased, err)
		}
		e.markActive(record.OwnerToken, false)
		handle = nil
		if err := journal.Remove(journalPath, completeBytes); err != nil {
			return &RecoveryResult{Needed: true, Accepted: stringPointer(record.Ref.After)}, typed(FailureRecovery, PhaseJournalRemoved, err)
		}
		return &RecoveryResult{Needed: true, Performed: true, Action: RecoveryReconciled, Accepted: stringPointer(record.Ref.After)}, nil
	default:
		return &RecoveryResult{Needed: true}, typed(FailureRecovery, PhaseCaptured, ErrRecovery)
	}
}

func (e *Engine) ownerAlive(ctx context.Context, owner rendezvous.Owner) (bool, error) {
	if e.OwnerAlive != nil {
		return e.OwnerAlive(ctx, owner)
	}
	hostname, err := os.Hostname()
	if err != nil {
		return false, err
	}
	if owner.Hostname != hostname {
		return false, fmt.Errorf("owner host %q is not the current host", owner.Hostname)
	}
	if owner.PID == os.Getpid() {
		return e.isActive(owner.Token), nil
	}
	return hostProcessAlive(owner.PID)
}

func recognizedJournalLocks(record journal.Record, refLock, worktreeLock string) ([]string, rendezvous.Owner, rendezvous.Phase, error) {
	paths := []string{refLock, worktreeLock}
	present := make([]string, 0, 2)
	var owner rendezvous.Owner
	var phase rendezvous.Phase
	for _, name := range paths {
		value, err := rendezvous.Read(name)
		if errors.Is(err, os.ErrNotExist) {
			if record.State != journal.Complete {
				return nil, owner, phase, typed(FailureRecovery, PhaseLocked, fmt.Errorf("%w: required rendezvous lock is missing", ErrRecovery))
			}
			continue
		}
		if err != nil || value.Token != record.OwnerToken {
			return nil, owner, phase, typed(FailureRecovery, PhaseLocked, errors.Join(ErrRecovery, err, rendezvous.ErrOwnership))
		}
		if len(present) == 0 {
			owner, phase = value, value.Phase
		} else if value.Token != owner.Token || value.Phase != phase || value.PID != owner.PID || value.Hostname != owner.Hostname || value.StartedAt != owner.StartedAt {
			return nil, owner, phase, typed(FailureRecovery, PhaseLocked, fmt.Errorf("%w: rendezvous owners disagree", ErrRecovery))
		}
		present = append(present, name)
	}
	return present, owner, phase, nil
}

func stableRecoveryRef(ctx context.Context, git *gitClient, topology *gitraw.Topology, refname string) (*string, error) {
	first, err := observeRecoveryRef(ctx, git, topology, refname)
	if err != nil {
		return nil, err
	}
	second, err := observeRecoveryRef(ctx, git, topology, refname)
	if err != nil || !sameOptionalString(first, second) {
		return nil, errors.Join(ErrConcurrent, err)
	}
	return first, nil
}

func observeRecoveryRef(ctx context.Context, git *gitClient, topology *gitraw.Topology, refname string) (*string, error) {
	head, err := git.run(ctx, nil, nil, "symbolic-ref", "--quiet", "HEAD")
	if err != nil || head.status != 0 || strings.TrimSuffix(string(head.stdout), "\n") != refname {
		return nil, fmt.Errorf("symbolic HEAD no longer directly names %s", refname)
	}
	symbolic, err := git.run(ctx, nil, nil, "symbolic-ref", "--quiet", refname)
	if err != nil || symbolic.status != 1 {
		return nil, fmt.Errorf("accepted ref is symbolic or unavailable")
	}
	present, err := git.run(ctx, nil, nil, "show-ref", "--verify", "--quiet", refname)
	if err != nil {
		return nil, err
	}
	if present.status == 1 {
		return nil, nil
	}
	if present.status != 0 {
		return nil, fmt.Errorf("cannot inspect accepted ref")
	}
	resolved, err := git.run(ctx, nil, nil, "show-ref", "--verify", "--hash", refname)
	if err != nil || resolved.status != 0 {
		return nil, errors.Join(err, ErrConcurrent)
	}
	value := strings.TrimSuffix(string(resolved.stdout), "\n")
	if _, err := gitraw.ParseOID(topology.Format, value); err != nil {
		return nil, err
	}
	return stringPointer(value), nil
}

func (e *Engine) recoverWithoutJournal(ctx context.Context, topology *gitraw.Topology) (*RecoveryResult, error) {
	worktreePath := rendezvous.WorktreePath(topology.GitDir)
	worktreeOwner, err := rendezvous.Read(worktreePath)
	if errors.Is(err, os.ErrNotExist) {
		refPaths, scanErr := scanRefLocks(topology.CommonGitDir)
		if scanErr != nil {
			return &RecoveryResult{Needed: true}, scanErr
		}
		if len(refPaths) == 0 {
			return &RecoveryResult{Action: RecoveryNone}, nil
		}
		return &RecoveryResult{Needed: true}, typed(FailureRecovery, PhaseLocked, fmt.Errorf("%w: ref lock exists without worktree lock or journal", ErrRecovery))
	}
	if err != nil {
		return &RecoveryResult{Needed: true}, typed(FailureRecovery, PhaseLocked, errors.Join(ErrRecovery, err))
	}
	if worktreeOwner.Phase != rendezvous.PreJournal {
		return &RecoveryResult{Needed: true}, typed(FailureRecovery, PhaseLocked, fmt.Errorf("%w: journal-required owner has no journal", ErrRecovery))
	}
	refPaths, err := scanRefLocks(topology.CommonGitDir)
	if err != nil || len(refPaths) == 0 {
		return &RecoveryResult{Needed: true}, typed(FailureRecovery, PhaseLocked, errors.Join(ErrRecovery, err))
	}
	paths := append([]string(nil), refPaths...)
	paths = append(paths, worktreePath)
	for _, name := range refPaths {
		owner, readErr := rendezvous.Read(name)
		if readErr != nil || owner.Token != worktreeOwner.Token || owner.Phase != rendezvous.PreJournal || owner.PID != worktreeOwner.PID || owner.Hostname != worktreeOwner.Hostname || owner.StartedAt != worktreeOwner.StartedAt {
			return &RecoveryResult{Needed: true}, typed(FailureRecovery, PhaseLocked, errors.Join(ErrRecovery, readErr))
		}
	}
	lease, err := rendezvous.AcquireRecovery(topology.GitDir)
	if err != nil {
		return &RecoveryResult{Needed: true}, classify(PhaseLocked, err)
	}
	defer lease.Release()
	alive, err := e.ownerAlive(ctx, worktreeOwner)
	if err != nil || alive {
		return &RecoveryResult{Needed: true}, typed(FailureRecovery, PhaseLocked, errors.Join(ErrRecovery, err))
	}
	handle, err := lease.AdoptPaths(worktreeOwner.Token, rendezvous.PreJournal, paths...)
	if err != nil {
		return &RecoveryResult{Needed: true}, typed(FailureRecovery, PhaseLocked, errors.Join(ErrRecovery, err))
	}
	e.markActive(worktreeOwner.Token, true)
	defer e.markActive(worktreeOwner.Token, false)
	if err := cleanupUnpublishedJournalTemporary(topology.GitDir, worktreeOwner.Token); err != nil {
		return &RecoveryResult{Needed: true}, typed(FailureRecovery, PhaseJournalRemoved, err)
	}
	if err := handle.Release(); err != nil {
		return &RecoveryResult{Needed: true}, typed(FailureRecovery, PhaseLocksReleased, err)
	}
	e.markActive(worktreeOwner.Token, false)
	return &RecoveryResult{Needed: true, Performed: true, Action: RecoveryStaleLock}, nil
}

func scanRefLocks(commonGitDir string) ([]string, error) {
	directory := filepath.Join(commonGitDir, "engram", "locks", "refs")
	entries, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if entry.Type()&os.ModeSymlink != 0 || entry.IsDir() || len(name) != 64+len(".lock") || !strings.HasSuffix(name, ".lock") || !isLowerHex(strings.TrimSuffix(name, ".lock")) {
			return nil, typed(FailureRecovery, PhaseLocked, fmt.Errorf("%w: foreign ref-lock entry %q", ErrRecovery, name))
		}
		paths = append(paths, filepath.Join(directory, name))
	}
	sort.Strings(paths)
	return paths, nil
}

func cleanupUnpublishedJournalTemporary(gitDir, token string) error {
	name := journal.Path(gitDir) + ".pending-" + token
	record, _, err := journal.Read(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || record.OwnerToken != token || record.State != journal.Pending {
		return errors.Join(ErrRecovery, err)
	}
	if err := os.Remove(name); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(name))
}

func sameOptionalString(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func cloneOptionalString(value *string) *string {
	if value == nil {
		return nil
	}
	return stringPointer(*value)
}

func isLowerHex(value string) bool {
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
