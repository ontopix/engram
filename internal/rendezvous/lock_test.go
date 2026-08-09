package rendezvous

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestAcquireWriterOrdersAndReleasesLocks(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	common := filepath.Join(root, "common")
	worktree := filepath.Join(root, "worktree")
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
	first, err := AcquireWorktree(worktree)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Release()
	common := filepath.Join(root, "common")
	if _, err := AcquireWriter(common, worktree, "refs/heads/main"); !errors.Is(err, ErrBusy) {
		t.Fatalf("error = %v", err)
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
