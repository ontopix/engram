package rendezvous

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
)

func TestAcquireWriterOrdersAndReleasesLocks(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	common := filepath.Join(root, "common")
	worktree := filepath.Join(root, "worktree")
	if err := os.Mkdir(common, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(worktree, 0o700); err != nil {
		t.Fatal(err)
	}
	handle, err := AcquireWriter(common, worktree, "refs/heads/z", "refs/heads/a", "refs/heads/a")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{RefPath(common, "refs/heads/a"), RefPath(common, "refs/heads/z"), WorktreePath(worktree)}
	if !reflect.DeepEqual(handle.paths, want) {
		t.Fatalf("paths = %#v, want %#v", handle.paths, want)
	}
	for _, name := range want {
		owner, err := Read(name)
		if err != nil || owner.Token != handle.Owner().Token || owner.Phase != PreJournal {
			t.Fatalf("Read(%s) = %#v, %v", name, owner, err)
		}
	}
	if err := handle.Release(); err != nil {
		t.Fatal(err)
	}
	for _, name := range want {
		if _, err := os.Stat(name); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("lock remains at %s: %v", name, err)
		}
	}
}

func TestBusyAcquisitionDoesNotLeakEarlierLocks(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	worktree := filepath.Join(root, "worktree")
	if err := os.Mkdir(worktree, 0o700); err != nil {
		t.Fatal(err)
	}
	first, err := AcquireWorktree(worktree)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Release()
	common := filepath.Join(root, "common")
	if err := os.Mkdir(common, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := AcquireWriter(common, worktree, "refs/heads/main"); !errors.Is(err, ErrBusy) || !DurableMutationOf(err) || RecoveryRequiredOf(err) {
		t.Fatalf("error = %v, durable=%t recovery=%t", err, DurableMutationOf(err), RecoveryRequiredOf(err))
	}
	if _, err := os.Stat(RefPath(common, "refs/heads/main")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ref lock leaked: %v", err)
	}
}

func TestPhaseAdvanceIsDurableAndOneWay(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	handle, err := AcquireWriter(root, root, "refs/heads/main")
	if err != nil {
		t.Fatal(err)
	}
	if err := handle.SetPhase(JournalRequired); err != nil {
		t.Fatal(err)
	}
	for _, name := range handle.paths {
		owner, err := Read(name)
		if err != nil || owner.Phase != JournalRequired {
			t.Fatalf("owner = %#v, %v", owner, err)
		}
	}
	if err := handle.SetPhase(PreJournal); err == nil {
		t.Fatal("phase moved backward")
	}
	if err := handle.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestPhaseAdvanceRollsForwardAfterVisibleSyncFailure(t *testing.T) {
	root := t.TempDir()
	handle, err := AcquireWriter(root, root, "refs/heads/main")
	if err != nil {
		t.Fatal(err)
	}
	injected := errors.New("injected phase directory sync failure")
	calls := 0
	handle.syncDirectory = func(name string) (bool, error) {
		calls++
		if calls == 1 {
			return false, injected
		}
		syncErr := syncLockDirectory(name)
		return syncErr == nil, syncErr
	}
	err = handle.SetPhase(JournalRequired)
	if !errors.Is(err, injected) || !DurableMutationOf(err) || !RecoveryRequiredOf(err) {
		t.Fatalf("phase error = %v, durable=%t recovery=%t", err, DurableMutationOf(err), RecoveryRequiredOf(err))
	}
	if handle.Owner().Phase != JournalRequired || !handle.RecoveryRequired() {
		t.Fatalf("handle owner = %#v, recovery=%t", handle.Owner(), handle.RecoveryRequired())
	}
	for _, name := range handle.paths {
		owner, readErr := Read(name)
		if readErr != nil || owner.Phase != JournalRequired || owner != handle.ownerAt(name) {
			t.Fatalf("rolled-forward owner at %s = %#v, %v", name, owner, readErr)
		}
	}
	handle.syncDirectory = nil
	if err := handle.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestPhaseAdvanceSyncTailIsDurable(t *testing.T) {
	root := t.TempDir()
	handle, err := AcquireWorktree(root)
	if err != nil {
		t.Fatal(err)
	}
	injected := errors.New("injected phase sync close tail")
	handle.syncDirectory = func(name string) (bool, error) {
		syncErr := syncLockDirectory(name)
		return syncErr == nil, errors.Join(syncErr, injected)
	}
	err = handle.SetPhase(JournalRequired)
	if !errors.Is(err, injected) || !DurableMutationOf(err) || !RecoveryRequiredOf(err) || handle.Owner().Phase != JournalRequired {
		t.Fatalf("phase error = %v, durable=%t recovery=%t owner=%#v", err, DurableMutationOf(err), RecoveryRequiredOf(err), handle.Owner())
	}
	handle.syncDirectory = nil
	if err := handle.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestReleaseNeverRemovesForeignOwnership(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	handle, err := AcquireWorktree(root)
	if err != nil {
		t.Fatal(err)
	}
	name := WorktreePath(root)
	foreign := handle.Owner()
	foreign.Token = "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	data, err := json.Marshal(foreign)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := handle.Release(); !errors.Is(err, ErrOwnership) {
		t.Fatalf("error = %v", err)
	}
	if _, err := os.Stat(name); err != nil {
		t.Fatalf("foreign lock removed: %v", err)
	}
}

func TestReleaseReportsRemovalDurableOnlyAfterDirectorySync(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	handle, err := AcquireWorktree(root)
	if err != nil {
		t.Fatal(err)
	}
	handle.mutated = false
	injected := errors.New("lock directory sync failed")
	handle.syncDirectory = func(string) (bool, error) { return false, injected }
	if err := handle.Release(); !errors.Is(err, injected) {
		t.Fatalf("release error = %v", err)
	}
	if handle.Mutated() {
		t.Fatal("lock removal was reported durable before directory sync")
	}
	if _, err := os.Lstat(WorktreePath(root)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("removed lock is still named: %v", err)
	}
	handle.syncDirectory = nil
	if err := handle.Release(); err != nil {
		t.Fatal(err)
	}
	if !handle.Mutated() {
		t.Fatal("successful retry did not record durable lock removal")
	}
}

func TestRecoveryAdoptionReportsPartialDurableMutation(t *testing.T) {
	root := t.TempDir()
	handle, err := AcquireWriter(root, root, "refs/heads/main")
	if err != nil {
		t.Fatal(err)
	}
	original := make([]Owner, len(handle.paths))
	for index, name := range handle.paths {
		original[index], err = Read(name)
		if err != nil {
			t.Fatal(err)
		}
		defer os.Remove(name)
	}
	lease, err := AcquireRecovery(root)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	injected := errors.New("stop after first adopted lock")
	lease.adoptFault = func(index int, _ string) error {
		if index == 0 {
			return injected
		}
		return nil
	}
	if _, err := lease.AdoptPaths(handle.owner.Token, handle.owner.Phase, handle.paths...); !errors.Is(err, injected) || !DurableMutationOf(err) {
		t.Fatalf("adoption error = %v, durable = %v", err, DurableMutationOf(err))
	}
	first, err := Read(handle.paths[0])
	if err != nil || first == original[0] {
		t.Fatalf("first owner = %#v, original = %#v, error = %v", first, original[0], err)
	}
	second, err := Read(handle.paths[1])
	if err != nil || second != original[1] {
		t.Fatalf("second owner = %#v, original = %#v, error = %v", second, original[1], err)
	}
}

func TestRendezvousMutationHelpersUseFinalRecoverySnapshot(t *testing.T) {
	first := &mutationError{recoveryRequired: true, err: errors.New("first")}
	last := &mutationError{durable: true, err: errors.New("last")}
	joined := errors.Join(first, last)
	if !DurableMutationOf(joined) || RecoveryRequiredOf(joined) {
		t.Fatalf("joined mutation: durable=%t recovery=%t", DurableMutationOf(joined), RecoveryRequiredOf(joined))
	}
	outer := &mutationError{recoveryRequired: true, err: joined}
	if !DurableMutationOf(outer) || !RecoveryRequiredOf(outer) {
		t.Fatalf("outer mutation: durable=%t recovery=%t", DurableMutationOf(outer), RecoveryRequiredOf(outer))
	}
}

func TestConcurrentFirstRecoveryLeaseCreationUsesOnePersistentInode(t *testing.T) {
	t.Parallel()
	name := filepath.Join(t.TempDir(), "recovery.lease")
	const contenders = 64
	type outcome struct {
		lease *RecoveryLease
		err   error
	}
	start := make(chan struct{})
	release := make(chan struct{})
	results := make(chan outcome, contenders)
	var workers sync.WaitGroup
	workers.Add(contenders)
	for range contenders {
		go func() {
			defer workers.Done()
			<-start
			lease, err := AcquireRecoveryPath(name)
			results <- outcome{lease: lease, err: err}
			if lease != nil {
				<-release
				if err := lease.Release(); err != nil {
					t.Errorf("release recovery lease: %v", err)
				}
			}
		}()
	}
	close(start)
	winners := 0
	for range contenders {
		result := <-results
		switch {
		case result.err == nil && result.lease != nil:
			winners++
		case errors.Is(result.err, ErrBusy) && result.lease == nil:
		default:
			t.Errorf("concurrent acquisition = %#v, %v", result.lease, result.err)
		}
	}
	if winners != 1 {
		t.Errorf("successful concurrent acquisitions = %d, want 1", winners)
	}
	created, err := os.Lstat(name)
	if err != nil || created.Mode()&os.ModeSymlink != 0 || !created.Mode().IsRegular() {
		t.Fatalf("created lease = %#v, %v", created, err)
	}
	close(release)
	workers.Wait()

	next, err := AcquireRecoveryPath(name)
	if err != nil {
		t.Fatalf("reacquire persistent lease: %v", err)
	}
	after, statErr := os.Lstat(name)
	if statErr != nil || !os.SameFile(created, after) {
		t.Fatalf("persistent lease identity changed: before=%#v after=%#v error=%v", created, after, statErr)
	}
	if err := next.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestRendezvousRejectsSymlinkedAdministrationAndFinalFile(t *testing.T) {
	gitDir := t.TempDir()
	external := t.TempDir()
	if err := os.Symlink(external, filepath.Join(gitDir, "engram")); err != nil {
		t.Skipf("symbolic links unavailable: %v", err)
	}
	if _, err := AcquireWorktree(gitDir); err == nil {
		t.Fatal("acquisition followed a symbolic-link administration path")
	}
	if _, err := os.Lstat(filepath.Join(external, "locks")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("external path was touched: %v", err)
	}

	ownedDir := t.TempDir()
	handle, err := AcquireWorktree(ownedDir)
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Release()
	link := filepath.Join(t.TempDir(), "lock")
	if err := os.Symlink(WorktreePath(ownedDir), link); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(link); err == nil {
		t.Fatal("read followed a symbolic-link lock")
	}
}
