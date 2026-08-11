package staging

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ontopix/engram/internal/changeset"
	"github.com/ontopix/engram/internal/gitraw"
	"github.com/ontopix/engram/internal/managedread"
	"github.com/ontopix/engram/internal/rendezvous"
)

func TestAddStagesOnlyLiteralSelectionAndPreservesDraft(t *testing.T) {
	root := fixture(t, false)
	why := filepath.Join(root, "topics", "why-files.md")
	derived := filepath.Join(root, "topics", "derived-state.md")
	appendTo(t, why, "\nWhy staged.\n")
	appendTo(t, derived, "\nStill a draft.\n")
	store := open(t, root)
	result, err := Add(context.Background(), store, []string{"topics/why-files.md"}, false)
	if err != nil {
		t.Fatal(err)
	}
	wantStaged := []changeset.Change{{Operation: changeset.Modified, Path: "topics/why-files.md"}}
	if !result.Changed || !reflect.DeepEqual(result.Staged, wantStaged) {
		t.Fatalf("result = %#v", result)
	}
	if candidates, globErr := filepath.Glob(filepath.Join(root, ".git", ".engram-index-candidate-*")); globErr != nil || len(candidates) != 0 {
		t.Fatalf("prospective indexes after successful Git replacement = %v, %v", candidates, globErr)
	}
	status, err := open(t, root).Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	wantUnstaged := []changeset.Change{{Operation: changeset.Modified, Path: "topics/derived-state.md"}}
	if !reflect.DeepEqual(status.Staged, wantStaged) || !reflect.DeepEqual(status.Unstaged, wantUnstaged) {
		t.Fatalf("status = %#v", status)
	}

	result, err = Add(context.Background(), open(t, root), []string{"topics"}, false)
	if err != nil {
		t.Fatal(err)
	}
	wantBoth := []changeset.Change{
		{Operation: changeset.Modified, Path: "topics/derived-state.md"},
		{Operation: changeset.Modified, Path: "topics/why-files.md"},
	}
	if !result.Changed || !reflect.DeepEqual(result.Staged, wantBoth) {
		t.Fatalf("directory add = %#v", result)
	}
}

func TestReadIndexPinsOriginalIdentity(t *testing.T) {
	parent := t.TempDir()
	name := filepath.Join(parent, "index")
	displacedName := filepath.Join(parent, "index-displaced")
	want := []byte("same index bytes")
	if err := os.WriteFile(name, want, 0o600); err != nil {
		t.Fatal(err)
	}
	got, original, err := readIndex(name)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("readIndex = %q, %#v, %v", got, original, err)
	}
	if err := os.Rename(name, displacedName); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, want, 0o600); err != nil {
		t.Fatal(err)
	}
	replacement, err := os.Lstat(name)
	if err != nil || os.SameFile(original, replacement) {
		t.Fatalf("index identity followed replacement: %v", err)
	}
	displaced, err := os.Lstat(displacedName)
	if err != nil || !os.SameFile(original, displaced) {
		t.Fatalf("index identity lost displaced original: %v", err)
	}

	temporary, err := regularPathInfo(name)
	if err != nil {
		t.Fatal(err)
	}
	temporaryDisplaced := filepath.Join(parent, "temporary-displaced")
	if err := os.Rename(name, temporaryDisplaced); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, want, 0o600); err != nil {
		t.Fatal(err)
	}
	replacement, err = os.Lstat(name)
	if err != nil || os.SameFile(temporary, replacement) {
		t.Fatalf("regular path identity followed replacement: %v", err)
	}
	temporaryOriginal, err := os.Lstat(temporaryDisplaced)
	if err != nil || !os.SameFile(temporary, temporaryOriginal) {
		t.Fatalf("regular path identity lost displaced original: %v", err)
	}
}

func TestAddAllIncludesDeletionAndAddition(t *testing.T) {
	root := fixture(t, false)
	if err := os.Remove(filepath.Join(root, "topics", "why-files.md")); err != nil {
		t.Fatal(err)
	}
	asset := filepath.Join(root, "asset.bin")
	if err := os.WriteFile(asset, []byte("asset\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	result, err := Add(context.Background(), open(t, root), nil, true)
	if err != nil {
		t.Fatal(err)
	}
	want := []changeset.Change{
		{Operation: changeset.Added, Path: "asset.bin"},
		{Operation: changeset.Deleted, Path: "topics/why-files.md"},
	}
	if !result.Changed || !reflect.DeepEqual(result.Staged, want) {
		t.Fatalf("result = %#v", result)
	}
	entries := git(t, root, "ls-files", "--stage", "asset.bin")
	if !strings.HasPrefix(entries, "100644 ") {
		t.Fatalf("new file mode did not become deterministic regular mode: %q", entries)
	}
}

func TestAddUnchangedExistingPathIsNoOp(t *testing.T) {
	root := fixture(t, false)
	index := filepath.Join(root, ".git", "index")
	before, err := os.ReadFile(index)
	if err != nil {
		t.Fatal(err)
	}
	result, err := Add(context.Background(), open(t, root), []string{"README.md"}, false)
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(index)
	if err != nil {
		t.Fatal(err)
	}
	if result.Changed || len(result.Staged) != 0 || !bytes.Equal(before, after) {
		t.Fatalf("no-op result = %#v, index changed=%t", result, !bytes.Equal(before, after))
	}
}

func TestAddRejectsInvalidSelectionAndBusyWorktree(t *testing.T) {
	root := fixture(t, false)
	store := open(t, root)
	for _, selection := range []string{"../outside", ".hidden", "missing.md"} {
		if _, err := Add(context.Background(), store, []string{selection}, false); !errors.Is(err, ErrSelection) {
			t.Errorf("selection %q error = %v", selection, err)
		}
	}
	lock, err := rendezvous.AcquireWorktree(store.Repository().GitDir)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	if _, err := Add(context.Background(), store, nil, true); !errors.Is(err, rendezvous.ErrBusy) {
		t.Fatalf("busy error = %v", err)
	}
}

func TestAddRejectsIneligibleWorkingBoundary(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("symbolic-link fixture requires platform privileges")
	}
	root := fixture(t, false)
	name := filepath.Join(root, "topics", "why-files.md")
	if err := os.Remove(name); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("derived-state.md", name); err != nil {
		t.Fatal(err)
	}
	if _, err := Add(context.Background(), open(t, root), nil, true); !errors.Is(err, ErrSelection) {
		t.Fatalf("error = %v", err)
	}
}

func TestAddSHA256(t *testing.T) {
	root := fixture(t, true)
	if root == "" {
		t.Skip("Git SHA-256 repositories unavailable")
	}
	appendTo(t, filepath.Join(root, "topics", "why-files.md"), "\nSHA-256.\n")
	result, err := Add(context.Background(), open(t, root), nil, true)
	if err != nil || !result.Changed || len(result.Staged) != 1 {
		t.Fatalf("result = %#v, %v", result, err)
	}
	fields := strings.Fields(git(t, root, "ls-files", "--stage", "topics/why-files.md"))
	if len(fields) < 2 || len(fields[1]) != gitraw.SHA256.HexWidth() {
		t.Fatalf("index listing = %q", fields)
	}
}

func TestAddPostPublicationFaultsCarryExactEffects(t *testing.T) {
	tests := []struct {
		name        string
		phase       Phase
		wantDurable bool
	}{
		{name: "rename visible before directory sync", phase: PhaseIndexRenamed, wantDurable: false},
		{name: "index directory synced", phase: PhaseIndexSynced, wantDurable: true},
		{name: "worktree rendezvous released", phase: PhaseWorktreeReleased, wantDurable: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := fixture(t, false)
			appendTo(t, filepath.Join(root, "topics", "why-files.md"), "\nFaulted add.\n")
			injected := errors.New("injected post-publication fault")
			adder := New()
			adder.Fault = func(phase Phase) error {
				if phase == test.phase {
					return injected
				}
				return nil
			}
			result, err := adder.Add(context.Background(), open(t, root), []string{"topics/why-files.md"}, false)
			mutation, present := MutationOf(err)
			if !errors.Is(err, injected) || result.Changed || !present || mutation.Durable != test.wantDurable || !mutation.CheckoutChanged || mutation.RecoveryRequired {
				t.Fatalf("result = %#v, error = %v, mutation = %#v, present = %t", result, err, mutation, present)
			}
			status, statusErr := open(t, root).Status(context.Background())
			if statusErr != nil || !reflect.DeepEqual(status.Staged, []changeset.Change{{Operation: changeset.Modified, Path: "topics/why-files.md"}}) {
				t.Fatalf("published index status = %#v, %v", status, statusErr)
			}
			if _, statErr := os.Lstat(rendezvous.WorktreePath(filepath.Join(root, ".git"))); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("worktree rendezvous remains: %v", statErr)
			}
		})
	}
}

func TestAddIndexDirectorySyncFailureCarriesVisibleNonDurableEffect(t *testing.T) {
	root := fixture(t, false)
	appendTo(t, filepath.Join(root, "topics", "why-files.md"), "\nFaulted fsync.\n")
	injected := errors.New("injected index directory sync failure")
	adder := New()
	adder.syncIndexDirectory = func(string) (bool, error) { return false, injected }
	result, err := adder.Add(context.Background(), open(t, root), []string{"topics/why-files.md"}, false)
	mutation, present := MutationOf(err)
	if !errors.Is(err, injected) || result.Changed || !present || mutation.Durable || !mutation.CheckoutChanged || mutation.RecoveryRequired {
		t.Fatalf("result = %#v, error = %v, mutation = %#v, present = %t", result, err, mutation, present)
	}
	status, statusErr := open(t, root).Status(context.Background())
	if statusErr != nil || !reflect.DeepEqual(status.Staged, []changeset.Change{{Operation: changeset.Modified, Path: "topics/why-files.md"}}) {
		t.Fatalf("published index status = %#v, %v", status, statusErr)
	}
}

func TestAddIndexDirectorySyncTailCarriesDurableEffect(t *testing.T) {
	root := fixture(t, false)
	appendTo(t, filepath.Join(root, "topics", "why-files.md"), "\nFaulted fsync close tail.\n")
	injected := errors.New("injected index directory close tail")
	adder := New()
	adder.syncIndexDirectory = func(string) (bool, error) { return true, injected }
	result, err := adder.Add(context.Background(), open(t, root), []string{"topics/why-files.md"}, false)
	mutation, present := MutationOf(err)
	if !errors.Is(err, injected) || result.Changed || !present || !mutation.Durable || !mutation.CheckoutChanged || mutation.RecoveryRequired {
		t.Fatalf("result = %#v, error = %v, mutation = %#v, present = %t", result, err, mutation, present)
	}
}

func TestAddIndexRenameTailCarriesPublishedEffect(t *testing.T) {
	root := fixture(t, false)
	appendTo(t, filepath.Join(root, "topics", "why-files.md"), "\nFaulted rename tail.\n")
	injected := errors.New("injected index rename tail")
	adder := New()
	adder.renamePath = func(oldPath, newPath string) (bool, error) {
		renameErr := os.Rename(oldPath, newPath)
		return renameErr == nil, errors.Join(renameErr, injected)
	}
	result, err := adder.Add(context.Background(), open(t, root), []string{"topics/why-files.md"}, false)
	mutation, present := MutationOf(err)
	if !errors.Is(err, injected) || result.Changed || !present || !mutation.Durable || !mutation.CheckoutChanged || mutation.RecoveryRequired {
		t.Fatalf("result = %#v, error = %v, mutation = %#v, present = %t", result, err, mutation, present)
	}
	status, statusErr := open(t, root).Status(context.Background())
	if statusErr != nil || !reflect.DeepEqual(status.Staged, []changeset.Change{{Operation: changeset.Modified, Path: "topics/why-files.md"}}) {
		t.Fatalf("published index status = %#v, %v", status, statusErr)
	}
}

func TestAddAggregatesNativeLockAndCandidateCleanupFailures(t *testing.T) {
	t.Run("native index lock remains after rename failure", func(t *testing.T) {
		root := fixture(t, false)
		appendTo(t, filepath.Join(root, "topics", "why-files.md"), "\nFaulted rename.\n")
		renameErr := errors.New("injected index rename failure")
		cleanupErr := errors.New("injected index lock cleanup failure")
		adder := New()
		adder.renamePath = func(oldPath, _ string) (bool, error) {
			if strings.HasSuffix(oldPath, "index.lock") {
				return false, renameErr
			}
			return false, errors.New("unexpected rename")
		}
		adder.removePath = func(name string) (bool, error) {
			if strings.HasSuffix(name, "index.lock") {
				return false, cleanupErr
			}
			err := os.Remove(name)
			return err == nil, err
		}
		result, err := adder.Add(context.Background(), open(t, root), []string{"topics/why-files.md"}, false)
		mutation, present := MutationOf(err)
		if !errors.Is(err, renameErr) || !errors.Is(err, cleanupErr) || result.Changed || !present || mutation.Durable || mutation.CheckoutChanged || !mutation.RecoveryRequired {
			t.Fatalf("result = %#v, error = %v, mutation = %#v, present = %t", result, err, mutation, present)
		}
		if _, statErr := os.Lstat(filepath.Join(root, ".git", "index.lock")); statErr != nil {
			t.Fatalf("residual native lock missing: %v", statErr)
		}
	})

	t.Run("silent native index lock cleanup", func(t *testing.T) {
		root := fixture(t, false)
		appendTo(t, filepath.Join(root, "topics", "why-files.md"), "\nSilent lock cleanup.\n")
		renameErr := errors.New("injected index rename failure")
		adder := New()
		adder.renamePath = func(string, string) (bool, error) { return false, renameErr }
		adder.removePath = func(name string) (bool, error) {
			if strings.HasSuffix(name, "index.lock") {
				return false, nil
			}
			err := os.Remove(name)
			return err == nil, err
		}
		result, err := adder.Add(context.Background(), open(t, root), []string{"topics/why-files.md"}, false)
		mutation, present := MutationOf(err)
		if !errors.Is(err, renameErr) || result.Changed || !present || mutation.Durable || mutation.CheckoutChanged || !mutation.RecoveryRequired {
			t.Fatalf("result = %#v, error = %v, mutation = %#v, present = %t", result, err, mutation, present)
		}
	})

	t.Run("prospective index cleanup prevents success", func(t *testing.T) {
		root := fixture(t, false)
		cleanupErr := errors.New("injected candidate cleanup failure")
		adder := New()
		adder.removePath = func(name string) (bool, error) {
			if strings.Contains(filepath.Base(name), ".engram-index-candidate-") {
				return false, cleanupErr
			}
			err := os.Remove(name)
			return err == nil, err
		}
		result, err := adder.Add(context.Background(), open(t, root), []string{"README.md"}, false)
		mutation, present := MutationOf(err)
		if !errors.Is(err, cleanupErr) || result.Changed || !present || mutation.Durable || mutation.CheckoutChanged || mutation.RecoveryRequired {
			t.Fatalf("result = %#v, error = %v, mutation = %#v, present = %t", result, err, mutation, present)
		}
		matches, globErr := filepath.Glob(filepath.Join(root, ".git", ".engram-index-candidate-*"))
		if globErr != nil || len(matches) != 1 {
			t.Fatalf("residual candidates = %v, %v", matches, globErr)
		}
	})

	t.Run("silent prospective index cleanup", func(t *testing.T) {
		root := fixture(t, false)
		adder := New()
		adder.removePath = func(name string) (bool, error) {
			if strings.Contains(filepath.Base(name), ".engram-index-candidate-") {
				return false, nil
			}
			err := os.Remove(name)
			return err == nil, err
		}
		result, err := adder.Add(context.Background(), open(t, root), []string{"README.md"}, false)
		mutation, present := MutationOf(err)
		if err == nil || result.Changed || !present || mutation.Durable || mutation.CheckoutChanged || mutation.RecoveryRequired {
			t.Fatalf("result = %#v, error = %v, mutation = %#v, present = %t", result, err, mutation, present)
		}
	})
}

func TestAddReleaseFailurePreservesPriorRecoveryEvidence(t *testing.T) {
	root := fixture(t, false)
	appendTo(t, filepath.Join(root, "topics", "why-files.md"), "\nFaulted cleanup and release.\n")
	renameErr := errors.New("injected index rename failure")
	cleanupErr := errors.New("injected native lock cleanup failure")
	releaseErr := errors.New("injected rendezvous release failure")
	adder := New()
	adder.acquire = func(string) (worktreeHandle, error) { return failingWorktreeHandle{err: releaseErr}, nil }
	adder.renamePath = func(string, string) (bool, error) { return false, renameErr }
	adder.removePath = func(name string) (bool, error) {
		if strings.HasSuffix(name, "index.lock") {
			return false, cleanupErr
		}
		err := os.Remove(name)
		return err == nil, err
	}
	result, err := adder.Add(context.Background(), open(t, root), []string{"topics/why-files.md"}, false)
	mutation, present := MutationOf(err)
	if !errors.Is(err, renameErr) || !errors.Is(err, cleanupErr) || !errors.Is(err, releaseErr) || result.Changed || !present || !mutation.Durable || mutation.CheckoutChanged || !mutation.RecoveryRequired {
		t.Fatalf("result = %#v, error = %v, mutation = %#v, present = %t", result, err, mutation, present)
	}
}

func TestAddReleaseFailureNeverReportsSuccess(t *testing.T) {
	root := fixture(t, false)
	injected := errors.New("injected residual rendezvous")
	adder := New()
	adder.acquire = func(gitDirectory string) (worktreeHandle, error) {
		name := rendezvous.WorktreePath(gitDirectory)
		if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
			return nil, err
		}
		if err := os.WriteFile(name, []byte("residual\n"), 0o600); err != nil {
			return nil, err
		}
		return failingWorktreeHandle{err: injected}, nil
	}
	result, err := adder.Add(context.Background(), open(t, root), []string{"README.md"}, false)
	mutation, present := MutationOf(err)
	if !errors.Is(err, injected) || result.Changed || !present || !mutation.Durable || mutation.CheckoutChanged || !mutation.RecoveryRequired {
		t.Fatalf("result = %#v, error = %v, mutation = %#v, present = %t", result, err, mutation, present)
	}
}

func TestAddSilentResidualRendezvousNeverReportsSuccess(t *testing.T) {
	root := fixture(t, false)
	adder := New()
	adder.acquire = func(string) (worktreeHandle, error) { return silentResidualWorktreeHandle{}, nil }
	result, err := adder.Add(context.Background(), open(t, root), []string{"README.md"}, false)
	mutation, present := MutationOf(err)
	if err == nil || result.Changed || !present || !mutation.Durable || mutation.CheckoutChanged || !mutation.RecoveryRequired {
		t.Fatalf("result = %#v, error = %v, mutation = %#v, present = %t", result, err, mutation, present)
	}
}

type failingWorktreeHandle struct{ err error }

func (h failingWorktreeHandle) Release() error { return h.err }

type silentResidualWorktreeHandle struct{}

func (silentResidualWorktreeHandle) Release() error         { return nil }
func (silentResidualWorktreeHandle) RecoveryRequired() bool { return true }

func TestStagingMutationOfUsesFinalRecoverySnapshot(t *testing.T) {
	first := mutationError("first", errors.New("first"), Mutation{CheckoutChanged: true, RecoveryRequired: true})
	last := mutationError("last", errors.New("last"), Mutation{Durable: true})
	mutation, present := MutationOf(errors.Join(first, last))
	if !present || !mutation.Durable || !mutation.CheckoutChanged || mutation.RecoveryRequired {
		t.Fatalf("joined mutation = %#v, present = %t", mutation, present)
	}
	outer := mutationError("outer", first, Mutation{})
	mutation, present = MutationOf(outer)
	if !present || mutation.Durable || !mutation.CheckoutChanged || mutation.RecoveryRequired {
		t.Fatalf("outer mutation = %#v, present = %t", mutation, present)
	}
}

func fixture(t *testing.T, sha256 bool) string {
	t.Helper()
	root := t.TempDir()
	arguments := []string{"init", "--initial-branch=main"}
	if sha256 {
		arguments = append(arguments, "--object-format=sha256")
	}
	command := exec.Command("git", append([]string{"-C", root}, arguments...)...)
	if output, err := command.CombinedOutput(); err != nil {
		if sha256 {
			t.Logf("SHA-256 init unavailable: %v: %s", err, output)
			return ""
		}
		t.Fatalf("git init: %v\n%s", err, output)
	}
	minimal := filepath.Join("..", "..", "examples", "minimal")
	if err := filepath.WalkDir(minimal, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(minimal, name)
		if err != nil || relative == "." {
			return err
		}
		target := filepath.Join(root, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	}); err != nil {
		t.Fatal(err)
	}
	git(t, root, "config", "user.name", "Ada")
	git(t, root, "config", "user.email", "ada@example.test")
	git(t, root, "config", "commit.gpgsign", "false")
	git(t, root, "add", "--all")
	git(t, root, "commit", "--no-verify", "-m", "initial")
	return root
}

func open(t *testing.T, root string) *managedread.Store {
	t.Helper()
	store, err := managedread.Open(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func git(t *testing.T, root string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, arguments...)...)
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(arguments, " "), err, output)
	}
	return string(output)
}

func appendTo(t *testing.T, name, suffix string) {
	t.Helper()
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, append(data, suffix...), 0o644); err != nil {
		t.Fatal(err)
	}
}
