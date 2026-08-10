package lifecycle

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/ontopix/engram/internal/rendezvous"
)

func TestBeginTransitionAndRemoveExactState(t *testing.T) {
	target := canonicalTarget(t, "store")
	handle, err := Begin(target, Initialization)
	if err != nil {
		t.Fatal(err)
	}
	state, raw, err := Read(target, Initialization)
	if err != nil {
		t.Fatal(err)
	}
	if state.Phase != Running || state.Operation != Initialization || state.Target != target || len(state.Owner.Token) != 64 || raw[len(raw)-1] != '\n' {
		t.Fatalf("state = %#v, raw = %q", state, raw)
	}
	stage, err := Stage(state)
	wantStage := target + ".engram-initialization-v1-" + state.Owner.Token[:20] + ".stage"
	if err != nil || stage != wantStage || filepath.Dir(stage) != filepath.Dir(target) || bytes.Contains([]byte(filepath.Base(stage)), []byte("..")) || len(filepath.Base(stage)) >= len(filepath.Base(target))+64 {
		t.Fatalf("stage = %q, %v", stage, err)
	}
	if err := handle.RequireCleanup(); err != nil {
		t.Fatal(err)
	}
	state, _, err = Read(target, Initialization)
	if err != nil || state.Phase != CleanupRequired {
		t.Fatalf("cleanup state = %#v, %v", state, err)
	}
	if err := handle.RequireCleanup(); !errors.Is(err, ErrChanged) {
		t.Fatalf("second transition = %v", err)
	}
	if err := handle.Remove(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(Sidecar(target, Initialization)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("state remains: %v", err)
	}
}

func TestBeginNeverReusesExistingState(t *testing.T) {
	target := canonicalTarget(t, "store")
	first, err := Begin(target, Acquisition)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Remove()
	if _, err := Begin(target, Acquisition); !errors.Is(err, ErrExists) {
		t.Fatalf("second begin = %v", err)
	}
}

func TestBeginFaultBoundariesCarryExactMutationEvidence(t *testing.T) {
	injected := errors.New("injected lifecycle failure")
	for _, test := range []struct {
		name       string
		operations func() lifecycleOperations
	}{
		{name: "write", operations: func() lifecycleOperations {
			return lifecycleOperations{write: func(*os.File, []byte) (int, error) { return 0, injected }}
		}},
		{name: "file sync", operations: func() lifecycleOperations {
			return lifecycleOperations{syncFile: func(*os.File) error { return injected }}
		}},
		{name: "close tail", operations: func() lifecycleOperations {
			return lifecycleOperations{closeFile: func(file *os.File) error { return errors.Join(file.Close(), injected) }}
		}},
		{name: "directory sync", operations: func() lifecycleOperations {
			calls := 0
			return lifecycleOperations{syncDirectory: func(name string) (bool, error) {
				calls++
				if calls == 1 {
					return false, injected
				}
				err := syncDirectory(name)
				return err == nil, err
			}}
		}},
		{name: "directory sync tail", operations: func() lifecycleOperations {
			calls := 0
			return lifecycleOperations{syncDirectory: func(name string) (bool, error) {
				calls++
				syncErr := syncDirectory(name)
				if calls == 1 {
					return syncErr == nil, errors.Join(syncErr, injected)
				}
				return syncErr == nil, syncErr
			}}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			target := canonicalTarget(t, "store")
			_, err := beginWithOperations(target, Initialization, test.operations())
			mutation, present := MutationOf(err)
			if !errors.Is(err, injected) || !present || !mutation.Visible || !mutation.Durable || mutation.RecoveryRequired {
				t.Fatalf("error = %v, mutation = %#v, present = %t", err, mutation, present)
			}
			if _, statErr := os.Lstat(Sidecar(target, Initialization)); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("cleaned sidecar remains: %v", statErr)
			}
		})
	}
}

func TestBeginCleanupFailureRetainsOnlyExactOwnedSidecar(t *testing.T) {
	t.Run("owned residual", func(t *testing.T) {
		target := canonicalTarget(t, "store")
		operationErr := errors.New("injected write failure")
		cleanupErr := errors.New("injected cleanup failure")
		_, err := beginWithOperations(target, Initialization, lifecycleOperations{
			write:      func(*os.File, []byte) (int, error) { return 0, operationErr },
			removePath: func(string) (bool, error) { return false, cleanupErr },
		})
		mutation, present := MutationOf(err)
		if !errors.Is(err, operationErr) || !errors.Is(err, cleanupErr) || !present || !mutation.Visible || mutation.Durable || !mutation.RecoveryRequired {
			t.Fatalf("error = %v, mutation = %#v, present = %t", err, mutation, present)
		}
		if _, statErr := os.Lstat(Sidecar(target, Initialization)); statErr != nil {
			t.Fatalf("owned residual missing: %v", statErr)
		}
	})

	t.Run("replacement is preserved", func(t *testing.T) {
		target := canonicalTarget(t, "store")
		injected := errors.New("injected write tail failure")
		replacementBytes := []byte("foreign replacement\n")
		_, err := beginWithOperations(target, Initialization, lifecycleOperations{
			write: func(file *os.File, data []byte) (int, error) {
				written, writeErr := file.Write(data)
				replacement := file.Name() + ".replacement"
				if err := os.WriteFile(replacement, replacementBytes, 0o600); err != nil {
					return written, errors.Join(writeErr, err)
				}
				// Windows cannot replace this still-open pathname with Rename. The
				// created handle permits delete sharing, so unlinking the name first
				// models the same foreign inode substitution tested on Unix.
				if runtime.GOOS == "windows" {
					if err := os.Remove(file.Name()); err != nil {
						return written, errors.Join(writeErr, err)
					}
				}
				if err := os.Rename(replacement, file.Name()); err != nil {
					return written, errors.Join(writeErr, err)
				}
				return written, errors.Join(writeErr, injected)
			},
		})
		mutation, present := MutationOf(err)
		if !errors.Is(err, injected) || !errors.Is(err, ErrChanged) || !present || !mutation.Visible || mutation.Durable || mutation.RecoveryRequired {
			t.Fatalf("error = %v, mutation = %#v, present = %t", err, mutation, present)
		}
		data, readErr := os.ReadFile(Sidecar(target, Initialization))
		if readErr != nil || !bytes.Equal(data, replacementBytes) {
			t.Fatalf("replacement = %q, %v", data, readErr)
		}
	})

	t.Run("removed sidecar with failed cleanup sync", func(t *testing.T) {
		target := canonicalTarget(t, "store")
		operationErr := errors.New("injected write failure")
		cleanupErr := errors.New("injected cleanup sync failure")
		_, err := beginWithOperations(target, Initialization, lifecycleOperations{
			write:         func(*os.File, []byte) (int, error) { return 0, operationErr },
			syncDirectory: func(string) (bool, error) { return false, cleanupErr },
		})
		mutation, present := MutationOf(err)
		if !errors.Is(err, operationErr) || !errors.Is(err, cleanupErr) || !present || !mutation.Visible || mutation.Durable || mutation.RecoveryRequired {
			t.Fatalf("error = %v, mutation = %#v, present = %t", err, mutation, present)
		}
		if _, statErr := os.Lstat(Sidecar(target, Initialization)); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("removed sidecar remains: %v", statErr)
		}
	})

	t.Run("cleanup remove tail remains visible and durable", func(t *testing.T) {
		target := canonicalTarget(t, "store")
		operationErr := errors.New("injected write failure")
		cleanupErr := errors.New("injected cleanup remove tail")
		_, err := beginWithOperations(target, Initialization, lifecycleOperations{
			write: func(*os.File, []byte) (int, error) { return 0, operationErr },
			removePath: func(name string) (bool, error) {
				removeErr := os.Remove(name)
				return removeErr == nil, errors.Join(removeErr, cleanupErr)
			},
		})
		mutation, present := MutationOf(err)
		if !errors.Is(err, operationErr) || !errors.Is(err, cleanupErr) || !present || !mutation.Visible || !mutation.Durable || mutation.RecoveryRequired {
			t.Fatalf("error = %v, mutation = %#v, present = %t", err, mutation, present)
		}
	})
}

func TestLifecycleTransitionAndRemovalFaultEvidence(t *testing.T) {
	t.Run("transition rename tail", func(t *testing.T) {
		target := canonicalTarget(t, "store")
		handle, err := Begin(target, Initialization)
		if err != nil {
			t.Fatal(err)
		}
		injected := errors.New("injected transition rename tail")
		handle.operations.renamePath = func(oldPath, newPath string) (bool, error) {
			renameErr := os.Rename(oldPath, newPath)
			return renameErr == nil, errors.Join(renameErr, injected)
		}
		err = handle.RequireCleanup()
		mutation, present := MutationOf(err)
		if !errors.Is(err, injected) || !present || !mutation.Visible || mutation.Durable || !mutation.RecoveryRequired || handle.State().Phase != CleanupRequired {
			t.Fatalf("error = %v, mutation = %#v, present = %t, state = %#v", err, mutation, present, handle.State())
		}
		handle.operations.renamePath = nil
		if err := handle.Remove(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("transition rename then sync failure", func(t *testing.T) {
		target := canonicalTarget(t, "store")
		handle, err := Begin(target, Initialization)
		if err != nil {
			t.Fatal(err)
		}
		injected := errors.New("injected transition sync failure")
		handle.operations.syncDirectory = func(string) (bool, error) { return false, injected }
		err = handle.RequireCleanup()
		mutation, present := MutationOf(err)
		if !errors.Is(err, injected) || !present || !mutation.Visible || mutation.Durable || !mutation.RecoveryRequired || handle.State().Phase != CleanupRequired {
			t.Fatalf("error = %v, mutation = %#v, present = %t, state = %#v", err, mutation, present, handle.State())
		}
		handle.operations.syncDirectory = nil
		if err := handle.Remove(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("transition sync tail is durable", func(t *testing.T) {
		target := canonicalTarget(t, "store")
		handle, err := Begin(target, Initialization)
		if err != nil {
			t.Fatal(err)
		}
		injected := errors.New("injected transition sync tail")
		handle.operations.syncDirectory = func(name string) (bool, error) {
			syncErr := syncDirectory(name)
			return syncErr == nil, errors.Join(syncErr, injected)
		}
		err = handle.RequireCleanup()
		mutation, present := MutationOf(err)
		if !errors.Is(err, injected) || !present || !mutation.Visible || !mutation.Durable || !mutation.RecoveryRequired {
			t.Fatalf("error = %v, mutation = %#v, present = %t", err, mutation, present)
		}
		handle.operations.syncDirectory = nil
		if err := handle.Remove(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("transition sync failure recomputes residual", func(t *testing.T) {
		target := canonicalTarget(t, "store")
		handle, err := Begin(target, Initialization)
		if err != nil {
			t.Fatal(err)
		}
		injected := errors.New("injected transition sync failure")
		handle.operations.syncDirectory = func(string) (bool, error) {
			removeErr := os.Remove(Sidecar(target, Initialization))
			return false, errors.Join(removeErr, injected)
		}
		err = handle.RequireCleanup()
		mutation, present := MutationOf(err)
		if !errors.Is(err, injected) || !present || !mutation.Visible || mutation.Durable || mutation.RecoveryRequired || handle.RecoveryRequired() {
			t.Fatalf("error = %v, mutation = %#v, present = %t", err, mutation, present)
		}
	})

	t.Run("removal sync failure has no residual", func(t *testing.T) {
		target := canonicalTarget(t, "store")
		handle, err := Begin(target, Initialization)
		if err != nil {
			t.Fatal(err)
		}
		injected := errors.New("injected removal sync failure")
		handle.operations.syncDirectory = func(string) (bool, error) { return false, injected }
		err = handle.Remove()
		mutation, present := MutationOf(err)
		if !errors.Is(err, injected) || !present || !mutation.Visible || mutation.Durable || mutation.RecoveryRequired {
			t.Fatalf("error = %v, mutation = %#v, present = %t", err, mutation, present)
		}
		if _, statErr := os.Lstat(Sidecar(target, Initialization)); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("removed sidecar remains: %v", statErr)
		}
	})

	t.Run("removal sync failure recomputes restored exact residual", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("hard-link replacement while the lifecycle inode is active is Unix coverage")
		}
		target := canonicalTarget(t, "store")
		handle, err := Begin(target, Initialization)
		if err != nil {
			t.Fatal(err)
		}
		name := Sidecar(target, Initialization)
		backup := name + ".backup"
		if err := os.Link(name, backup); err != nil {
			t.Fatal(err)
		}
		defer os.Remove(backup)
		injected := errors.New("injected removal sync failure")
		handle.operations.syncDirectory = func(string) (bool, error) {
			linkErr := os.Link(backup, name)
			return false, errors.Join(linkErr, injected)
		}
		err = handle.Remove()
		mutation, present := MutationOf(err)
		if !errors.Is(err, injected) || !present || !mutation.Visible || mutation.Durable || !mutation.RecoveryRequired || !handle.RecoveryRequired() {
			t.Fatalf("error = %v, mutation = %#v, present = %t", err, mutation, present)
		}
		handle.operations.syncDirectory = nil
		if err := handle.Remove(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("removal sync tail is durable", func(t *testing.T) {
		target := canonicalTarget(t, "store")
		handle, err := Begin(target, Initialization)
		if err != nil {
			t.Fatal(err)
		}
		injected := errors.New("injected removal sync tail")
		handle.operations.syncDirectory = func(name string) (bool, error) {
			syncErr := syncDirectory(name)
			return syncErr == nil, errors.Join(syncErr, injected)
		}
		err = handle.Remove()
		mutation, present := MutationOf(err)
		if !errors.Is(err, injected) || !present || !mutation.Visible || !mutation.Durable || mutation.RecoveryRequired {
			t.Fatalf("error = %v, mutation = %#v, present = %t", err, mutation, present)
		}
	})

	t.Run("removal tail is visible and durable", func(t *testing.T) {
		target := canonicalTarget(t, "store")
		handle, err := Begin(target, Initialization)
		if err != nil {
			t.Fatal(err)
		}
		injected := errors.New("injected removal tail")
		handle.operations.removePath = func(name string) (bool, error) {
			removeErr := os.Remove(name)
			return removeErr == nil, errors.Join(removeErr, injected)
		}
		err = handle.Remove()
		mutation, present := MutationOf(err)
		if !errors.Is(err, injected) || !present || !mutation.Visible || !mutation.Durable || mutation.RecoveryRequired {
			t.Fatalf("error = %v, mutation = %#v, present = %t", err, mutation, present)
		}
	})

	t.Run("failed removal retains owned state", func(t *testing.T) {
		target := canonicalTarget(t, "store")
		handle, err := Begin(target, Initialization)
		if err != nil {
			t.Fatal(err)
		}
		injected := errors.New("injected removal failure")
		handle.operations.removePath = func(string) (bool, error) { return false, injected }
		err = handle.Remove()
		mutation, present := MutationOf(err)
		if !errors.Is(err, injected) || !present || mutation.Visible || mutation.Durable || !mutation.RecoveryRequired {
			t.Fatalf("error = %v, mutation = %#v, present = %t", err, mutation, present)
		}
	})

	t.Run("silent removal no-op is an owned residual", func(t *testing.T) {
		target := canonicalTarget(t, "store")
		handle, err := Begin(target, Initialization)
		if err != nil {
			t.Fatal(err)
		}
		handle.operations.removePath = func(string) (bool, error) { return false, nil }
		err = handle.Remove()
		mutation, present := MutationOf(err)
		if !errors.Is(err, ErrChanged) || !present || mutation.Visible || mutation.Durable || !mutation.RecoveryRequired || !handle.RecoveryRequired() {
			t.Fatalf("error = %v, mutation = %#v, present = %t", err, mutation, present)
		}
	})
}

func TestHandleRecoveryRequiredUsesExactIdentityAndBytes(t *testing.T) {
	t.Run("in-place bytes changed", func(t *testing.T) {
		target := canonicalTarget(t, "store")
		handle, err := Begin(target, Initialization)
		if err != nil || !handle.RecoveryRequired() {
			t.Fatalf("begin = %#v, %v", handle, err)
		}
		if err := os.WriteFile(Sidecar(target, Initialization), []byte("foreign\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if handle.RecoveryRequired() {
			t.Fatal("changed bytes were attributed to the handle")
		}
	})

	t.Run("identical replacement inode", func(t *testing.T) {
		target := canonicalTarget(t, "store")
		handle, err := Begin(target, Initialization)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(Sidecar(target, Initialization))
		if err != nil {
			t.Fatal(err)
		}
		replacement := Sidecar(target, Initialization) + ".replacement"
		if err := os.WriteFile(replacement, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		if runtime.GOOS == "windows" {
			if err := os.Remove(Sidecar(target, Initialization)); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.Rename(replacement, Sidecar(target, Initialization)); err != nil {
			t.Fatal(err)
		}
		if handle.RecoveryRequired() {
			t.Fatal("replacement inode was attributed to the handle")
		}
	})
}

func TestMutationOfUsesFinalRecoverySnapshot(t *testing.T) {
	first := mutationFailure(errors.New("first"), Mutation{Visible: true, RecoveryRequired: true})
	last := mutationFailure(errors.New("last"), Mutation{Durable: true})
	mutation, present := MutationOf(errors.Join(first, last))
	if !present || !mutation.Visible || !mutation.Durable || mutation.RecoveryRequired {
		t.Fatalf("joined mutation = %#v, present = %t", mutation, present)
	}
	outer := mutationFailure(first, Mutation{})
	mutation, present = MutationOf(outer)
	if !present || !mutation.Visible || mutation.Durable || mutation.RecoveryRequired {
		t.Fatalf("outer mutation = %#v, present = %t", mutation, present)
	}
}

func TestRecoveryLeaseIsExclusiveNonblockingAndPersistent(t *testing.T) {
	target := canonicalTarget(t, "store")
	first, err := AcquireRecovery(target, Initialization)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AcquireRecovery(target, Initialization); !errors.Is(err, rendezvous.ErrBusy) {
		t.Fatalf("competing recovery lease = %v", err)
	}
	name := Sidecar(target, Initialization) + ".lease"
	info, err := os.Lstat(name)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		t.Fatalf("lease file = %#v, %v", info, err)
	}
	if err := first.Release(); err != nil {
		t.Fatal(err)
	}
	if err := first.Release(); err != nil {
		t.Fatalf("idempotent release = %v", err)
	}
	third, err := AcquireRecovery(target, Initialization)
	if err != nil {
		t.Fatalf("lease after release = %v", err)
	}
	if err := third.Release(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(name); err != nil {
		t.Fatalf("persistent lease disappeared: %v", err)
	}
}

func TestRecoveryLeaseExcludesAnotherProcess(t *testing.T) {
	if target := os.Getenv("ENGRAM_LIFECYCLE_LEASE_HELPER_TARGET"); target != "" {
		lease, err := AcquireRecovery(target, Initialization)
		if err != nil {
			t.Fatal(err)
		}
		defer lease.Release()
		if _, err := os.Stdout.WriteString("ENGRAM_LEASE_READY\n"); err != nil {
			t.Fatal(err)
		}
		var release [1]byte
		_, _ = os.Stdin.Read(release[:])
		return
	}

	target := canonicalTarget(t, "store")
	command := exec.Command(os.Args[0], "-test.run=^TestRecoveryLeaseExcludesAnotherProcess$")
	command.Env = append(os.Environ(), "ENGRAM_LIFECYCLE_LEASE_HELPER_TARGET="+target)
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	ready := false
	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		if scanner.Text() == "ENGRAM_LEASE_READY" {
			ready = true
			break
		}
	}
	if !ready {
		_ = stdin.Close()
		_ = command.Wait()
		t.Fatalf("lease helper did not become ready: scan=%v stderr=%s", scanner.Err(), stderr.String())
	}
	if lease, err := AcquireRecovery(target, Initialization); !errors.Is(err, rendezvous.ErrBusy) {
		if lease != nil {
			_ = lease.Release()
		}
		_ = stdin.Close()
		_ = command.Wait()
		t.Fatalf("cross-process competing lease = %v", err)
	}
	if err := stdin.Close(); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err != nil {
		t.Fatalf("lease helper = %v, stderr=%s", err, stderr.String())
	}
	lease, err := AcquireRecovery(target, Initialization)
	if err != nil {
		t.Fatalf("lease after helper exit = %v", err)
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestRecoveryLeaseRejectsSymlinkStorage(t *testing.T) {
	target := canonicalTarget(t, "store")
	other := filepath.Join(filepath.Dir(target), "other")
	if err := os.WriteFile(other, []byte("preserve\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	name := Sidecar(target, Initialization) + ".lease"
	if err := os.Symlink(other, name); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlink unavailable: %v", err)
		}
		t.Fatal(err)
	}
	if lease, err := AcquireRecovery(target, Initialization); err == nil {
		lease.Release()
		t.Fatal("symlink lease storage was accepted")
	}
	data, err := os.ReadFile(other)
	if err != nil || string(data) != "preserve\n" {
		t.Fatalf("symlink target changed: %q, %v", data, err)
	}
}

func TestReadRejectsUnknownAndNonCanonicalState(t *testing.T) {
	target := canonicalTarget(t, "store")
	handle, err := Begin(target, Initialization)
	if err != nil {
		t.Fatal(err)
	}
	name := Sidecar(target, Initialization)
	state := handle.State()
	encoded, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	if err := os.WriteFile(name, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Read(target, Initialization); !errors.Is(err, ErrMalformed) {
		t.Fatalf("non-canonical read = %v", err)
	}
	if err := handle.Remove(); !errors.Is(err, ErrChanged) {
		t.Fatalf("changed owner removal = %v", err)
	}
}

func TestAdoptExpectedRejectsChangedLifecycleApproval(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("portable Windows process-death proof is intentionally conservative")
	}
	target := canonicalTarget(t, "store")
	handle, err := Begin(target, Acquisition)
	if err != nil {
		t.Fatal(err)
	}
	state := handle.State()
	state.Owner.PID = 2147483647
	raw, err := encode(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Sidecar(target, Acquisition), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	observation, err := ObserveRecovery(target, Acquisition)
	if err != nil {
		t.Fatal(err)
	}
	expected := observation.Expectation
	changed := expected
	changed.StateSHA256 = "0000000000000000000000000000000000000000000000000000000000000000"
	if _, err := AdoptExpected(target, Acquisition, changed); !errors.Is(err, ErrChanged) {
		t.Fatalf("changed approval error = %v", err)
	}
	adopted, err := AdoptExpected(target, Acquisition, expected)
	if err != nil {
		t.Fatal(err)
	}
	if err := adopted.Remove(); err != nil {
		t.Fatal(err)
	}
}

func TestAdoptExpectedRejectsChangedPublicationPlan(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("portable Windows process-death proof is intentionally conservative")
	}
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, string)
	}{
		{name: "bytes", mutate: func(t *testing.T, name string) {
			t.Helper()
			if err := os.WriteFile(name, []byte("changed\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "same-bytes-replacement", mutate: func(t *testing.T, name string) {
			t.Helper()
			approved, err := os.ReadFile(name)
			if err != nil {
				t.Fatal(err)
			}
			replacement := filepath.Join(filepath.Dir(name), "replacement-plan")
			if err := os.WriteFile(replacement, approved, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(replacement, name); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "symlink", mutate: func(t *testing.T, name string) {
			t.Helper()
			if err := os.Remove(name); err != nil {
				t.Fatal(err)
			}
			other := filepath.Join(filepath.Dir(name), "other-plan")
			if err := os.WriteFile(other, []byte("approved\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(other, name); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "disappeared", mutate: func(t *testing.T, name string) {
			t.Helper()
			if err := os.Remove(name); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			target := canonicalTarget(t, "store")
			handle, err := Begin(target, Acquisition)
			if err != nil {
				t.Fatal(err)
			}
			stage, err := Stage(handle.State())
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(stage, 0o700); err != nil {
				t.Fatal(err)
			}
			plan := filepath.Join(stage, "plan-v1.json")
			if err := os.WriteFile(plan, []byte("approved\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := handle.RequireCleanup(); err != nil {
				t.Fatal(err)
			}
			state := handle.State()
			state.Owner.PID = 2147483647
			raw, err := encode(state)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(Sidecar(target, Acquisition), raw, 0o600); err != nil {
				t.Fatal(err)
			}
			observation, err := ObserveRecovery(target, Acquisition)
			if err != nil {
				t.Fatal(err)
			}
			test.mutate(t, plan)
			if _, err := AdoptExpected(target, Acquisition, observation.Expectation); !errors.Is(err, ErrChanged) {
				t.Fatalf("changed plan adoption = %v", err)
			}
			if _, _, err := Read(target, Acquisition); err != nil {
				t.Fatalf("sidecar changed after rejection: %v", err)
			}
		})
	}
}

func canonicalTarget(t *testing.T, base string) string {
	t.Helper()
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(parent, base)
}
