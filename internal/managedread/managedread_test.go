package managedread

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ontopix/engram/internal/changeset"
	"github.com/ontopix/engram/internal/checker"
	"github.com/ontopix/engram/internal/gitraw"
)

func TestLogMaximumCountTraversesOnlyAvailableHistory(t *testing.T) {
	root := newGitRepository(t, gitraw.SHA1, true)
	store := openStore(t, root)

	log, err := store.Log(context.Background(), math.MaxInt32)
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	if len(log.Commits) != 1 {
		t.Fatalf("commits = %d, want 1", len(log.Commits))
	}
}

func TestBaseAbsentProjectsStagedInitialization(t *testing.T) {
	root := newGitRepository(t, gitraw.SHA1, false)
	copyMinimalSnapshot(t, root)
	runGit(t, root, nil, "add", "--all")

	store := openStore(t, root)
	accepted, err := store.Accepted(context.Background())
	if err != nil {
		t.Fatalf("Accepted: %v", err)
	}
	if accepted.State.Ref == nil || *accepted.State.Ref != "refs/heads/main" || accepted.State.Commit != nil || accepted.Snapshot != nil {
		t.Fatalf("unborn accepted view = %#v", accepted)
	}
	staged, err := store.Staged(context.Background())
	if err != nil {
		t.Fatalf("Staged: %v", err)
	}
	if staged.Snapshot == nil || staged.Snapshot.Validation.HasErrors() {
		t.Fatalf("staged validation = %#v", staged.Snapshot)
	}
	for _, entry := range staged.Index {
		if len(entry.Object) != gitraw.SHA1.HexWidth() {
			t.Fatalf("index object width = %d, want %d", len(entry.Object), gitraw.SHA1.HexWidth())
		}
	}

	validation, changes, err := store.CheckStaged(context.Background())
	if err != nil {
		t.Fatalf("CheckStaged: %v", err)
	}
	if validation.Status != checker.StatusComplete || validation.HasErrors() {
		t.Fatalf("CheckStaged validation = %#v", validation)
	}
	if len(changes) == 0 {
		t.Fatal("initialization changes are empty")
	}
	for _, change := range changes {
		if change.Operation != changeset.Added {
			t.Fatalf("initialization change = %#v, want added", change)
		}
	}

	status, err := store.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.Mode != StatusNormal || status.Accepted.Commit != nil || len(status.Staged) != len(changes) || len(status.Unstaged) != 0 {
		t.Fatalf("status = %#v", status)
	}
	diff, err := store.DiffStaged(context.Background())
	if err != nil {
		t.Fatalf("DiffStaged: %v", err)
	}
	if diff.Stat.Added != len(diff.Changes) || diff.Stat.Modified != 0 || diff.Stat.Deleted != 0 {
		t.Fatalf("initial diff stat = %#v", diff.Stat)
	}
	log, err := store.Log(context.Background(), 10)
	if err != nil || len(log.Commits) != 0 {
		t.Fatalf("unborn Log = %#v, %v", log, err)
	}
}

func TestLinkedWorktreeStatusAndDiff(t *testing.T) {
	primary := newGitRepository(t, gitraw.SHA1, true)
	linked := filepath.Join(t.TempDir(), "linked")
	runGit(t, primary, nil, "worktree", "add", "-b", "linked", linked)

	store := openStore(t, linked)
	if store.repository.GitDir == store.repository.CommonGitDir {
		t.Fatalf("linked worktree GitDir = CommonGitDir = %q", store.repository.GitDir)
	}
	modifyFile(t, filepath.Join(linked, "topics", "why-files.md"), "\nStaged sentence.\n")
	runGit(t, linked, nil, "add", "topics/why-files.md")
	modifyFile(t, filepath.Join(linked, "topics", "derived-state.md"), "\nWorking sentence.\n")

	status, err := store.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	wantStaged := []changeset.Change{{Operation: changeset.Modified, Path: "topics/why-files.md"}}
	wantUnstaged := []changeset.Change{{Operation: changeset.Modified, Path: "topics/derived-state.md"}}
	if !reflect.DeepEqual(status.Staged, wantStaged) || !reflect.DeepEqual(status.Unstaged, wantUnstaged) {
		t.Fatalf("status staged/unstaged = %#v / %#v", status.Staged, status.Unstaged)
	}
	if status.Accepted.Ref == nil || *status.Accepted.Ref != "refs/heads/linked" || !reflect.DeepEqual(status.Accepted, status.CandidateBase) {
		t.Fatalf("linked states = accepted %#v, base %#v", status.Accepted, status.CandidateBase)
	}

	stagedDiff, err := store.DiffStaged(context.Background())
	if err != nil {
		t.Fatalf("DiffStaged: %v", err)
	}
	if !reflect.DeepEqual(stagedDiff.Changes, wantStaged) || stagedDiff.Stat.Modified != 1 {
		t.Fatalf("staged diff = %#v", stagedDiff)
	}
	workingDiff, err := store.DiffWorking(context.Background())
	if err != nil {
		t.Fatalf("DiffWorking: %v", err)
	}
	if !reflect.DeepEqual(workingDiff.Changes, wantUnstaged) || workingDiff.Stat.Modified != 1 {
		t.Fatalf("working diff = %#v", workingDiff)
	}
}

func TestConflictedAndIntentToAddIndexRejected(t *testing.T) {
	t.Run("conflicted stages", func(t *testing.T) {
		root := newGitRepository(t, gitraw.SHA1, true)
		runGit(t, root, nil, "checkout", "-b", "side")
		replaceOnce(t, filepath.Join(root, "topics", "why-files.md"), "# Why files", "# Side version")
		commitAll(t, root, "side")
		runGit(t, root, nil, "checkout", "main")
		replaceOnce(t, filepath.Join(root, "topics", "why-files.md"), "# Why files", "# Main version")
		commitAll(t, root, "main")
		result := runGitStatus(t, root, nil, "merge", "side")
		if result == 0 {
			t.Fatal("merge unexpectedly succeeded without conflict")
		}

		store := openStore(t, root)
		_, err := store.Staged(context.Background())
		assertIndexError(t, err, IndexConflict, "topics/why-files.md")
		if _, err := store.Status(context.Background()); err == nil {
			t.Fatal("Status succeeded with conflicted index")
		}
		if _, err := store.DiffStaged(context.Background()); err == nil {
			t.Fatal("DiffStaged succeeded with conflicted index")
		}
	})

	t.Run("intent to add", func(t *testing.T) {
		root := newGitRepository(t, gitraw.SHA1, true)
		writeFile(t, filepath.Join(root, "draft.bin"), []byte("draft\n"), 0o644)
		runGit(t, root, nil, "add", "--intent-to-add", "draft.bin")
		store := openStore(t, root)
		_, err := store.Staged(context.Background())
		assertIndexError(t, err, IndexConflict, "draft.bin")
	})
}

func TestMalformedIndexIsRejectedAsTypedInputFailure(t *testing.T) {
	for _, operation := range []struct {
		name string
		run  func(context.Context, *Store) error
	}{
		{name: "staged", run: func(ctx context.Context, store *Store) error { _, err := store.Staged(ctx); return err }},
		{name: "status", run: func(ctx context.Context, store *Store) error { _, err := store.Status(ctx); return err }},
		{name: "diff", run: func(ctx context.Context, store *Store) error { _, err := store.DiffStaged(ctx); return err }},
	} {
		t.Run(operation.name, func(t *testing.T) {
			root := newGitRepository(t, gitraw.SHA1, true)
			store := openStore(t, root)
			if err := os.WriteFile(filepath.Join(store.repository.GitDir, "index"), []byte("not a Git index\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			err := operation.run(context.Background(), store)
			var indexError *IndexError
			if !errors.As(err, &indexError) || indexError.Kind != IndexMalformed {
				t.Fatalf("error = %T %v, want malformed IndexError", err, err)
			}
		})
	}
}

func TestCandidateModeRules(t *testing.T) {
	t.Run("new executable", func(t *testing.T) {
		root := newGitRepository(t, gitraw.SHA1, true)
		writeFile(t, filepath.Join(root, "asset.bin"), []byte("asset\n"), 0o755)
		runGit(t, root, nil, "add", "asset.bin")
		runGit(t, root, nil, "update-index", "--chmod=+x", "asset.bin")
		store := openStore(t, root)
		_, err := store.Staged(context.Background())
		assertIndexError(t, err, IndexMode, "asset.bin")
	})

	t.Run("surviving mode changes", func(t *testing.T) {
		root := newGitRepository(t, gitraw.SHA1, true)
		runGit(t, root, nil, "update-index", "--chmod=+x", "topics/why-files.md")
		store := openStore(t, root)
		_, err := store.Staged(context.Background())
		assertIndexError(t, err, IndexMode, "topics/why-files.md")
	})

	t.Run("surviving configuration executable", func(t *testing.T) {
		root := newGitRepository(t, gitraw.SHA1, true)
		runGit(t, root, nil, "update-index", "--chmod=+x", ".engram/root.yaml")
		runGit(t, root, nil, "commit", "--no-gpg-sign", "-m", "preserve configuration mode")

		staged, err := openStore(t, root).Staged(context.Background())
		if err != nil {
			t.Fatalf("Staged with preserved configuration mode: %v", err)
		}
		if staged.Modes[".engram/root.yaml"] != gitraw.ModeExecutable {
			t.Fatalf("configuration mode = %q, want %q", staged.Modes[".engram/root.yaml"], gitraw.ModeExecutable)
		}
	})
}

func TestDiffRejectsPrunedIndexEntry(t *testing.T) {
	root := newGitRepository(t, gitraw.SHA1, true)
	writeFile(t, filepath.Join(root, ".hidden"), []byte("not logical\n"), 0o644)
	runGit(t, root, nil, "add", "-f", ".hidden")

	store := openStore(t, root)
	_, err := store.Staged(context.Background())
	assertIndexError(t, err, IndexConflict, ".hidden")
}

func TestStagedFromAlternateIndexDoesNotUseLiveIndex(t *testing.T) {
	root := newGitRepository(t, gitraw.SHA1, true)
	store := openStore(t, root)
	liveIndex := filepath.Join(store.repository.GitDir, "index")
	data, err := os.ReadFile(liveIndex)
	if err != nil {
		t.Fatal(err)
	}
	alternate := filepath.Join(t.TempDir(), "prospective.index")
	writeFile(t, alternate, data, 0o600)

	modifyFile(t, filepath.Join(root, "topics", "why-files.md"), "\nProspective edit.\n")
	runGit(t, root, []string{"GIT_INDEX_FILE=" + alternate}, "add", "topics/why-files.md")

	projected, err := store.StagedFromIndex(context.Background(), alternate)
	if err != nil {
		t.Fatalf("StagedFromIndex: %v", err)
	}
	accepted, err := store.Accepted(context.Background())
	if err != nil {
		t.Fatalf("Accepted: %v", err)
	}
	want := []changeset.Change{{Operation: changeset.Modified, Path: "topics/why-files.md"}}
	if got := changeset.Diff(snapshotTree(accepted), snapshotTree(projected)); !reflect.DeepEqual(got, want) {
		t.Fatalf("alternate index changes = %#v, want %#v", got, want)
	}
	live, err := store.DiffStaged(context.Background())
	if err != nil || len(live.Changes) != 0 {
		t.Fatalf("live index changed: diff %#v, error %v", live, err)
	}
}

func TestInvalidAcceptedHistoryAggregatesCheckerResults(t *testing.T) {
	root := newGitRepository(t, gitraw.SHA1, true)
	rootID := strings.TrimSpace(runGit(t, root, nil, "rev-parse", "HEAD"))
	if err := os.Remove(filepath.Join(root, ".engram", "root.yaml")); err != nil {
		t.Fatal(err)
	}
	commitAll(t, root, "invalid snapshot")
	tipID := strings.TrimSpace(runGit(t, root, nil, "rev-parse", "HEAD"))

	store := openStore(t, root)
	audit, err := store.AuditAccepted(context.Background())
	if err != nil {
		t.Fatalf("AuditAccepted: %v", err)
	}
	if audit.Tip != tipID || audit.Validation.Target != checker.TargetManagedStore || audit.Validation.Status != checker.StatusComplete || !audit.Validation.HasErrors() {
		t.Fatalf("managed validation = %#v", audit.Validation)
	}
	if !hasFinding(audit.Validation.Findings, "E105", ".engram/root.yaml") {
		t.Fatalf("managed findings = %#v, want E105", audit.Validation.Findings)
	}
	if len(audit.Audits) != 2 || audit.Audits[0].Base != nil || audit.Audits[0].Candidate != rootID || audit.Audits[1].Base == nil || *audit.Audits[1].Base != rootID || audit.Audits[1].Candidate != tipID {
		t.Fatalf("history audits = %#v", audit.Audits)
	}

	log, err := store.Log(context.Background(), 10)
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	if len(log.Commits) != 2 || log.Commits[0].ID != tipID || log.Commits[1].ID != rootID || log.Commits[0].Message != "invalid snapshot\n" {
		t.Fatalf("log = %#v", log)
	}
	if log.Commits[0].Author == nil || log.Commits[0].Author.Name != "Ada" || log.Commits[0].CommittedAt == nil {
		t.Fatalf("decoded commit metadata = %#v", log.Commits[0])
	}

	revision, err := store.ResolveRevision(context.Background(), rootID)
	if err != nil || revision.Value == nil || *revision.Value != rootID {
		t.Fatalf("ResolveRevision = %#v, %v", revision, err)
	}
	diff, err := store.Diff(context.Background(), RevisionSelector(rootID), RevisionSelector(tipID))
	if err != nil {
		t.Fatalf("revision Diff: %v", err)
	}
	want := []changeset.Change{{Operation: changeset.Deleted, Path: ".engram/root.yaml"}}
	if !reflect.DeepEqual(diff.Changes, want) || diff.Stat.Deleted != 1 {
		t.Fatalf("revision diff = %#v", diff)
	}
	if _, err := store.ResolveRevision(context.Background(), rootID[:12]); err == nil {
		t.Fatal("abbreviated revision accepted")
	}
}

func TestSHA256IndexObjectWidth(t *testing.T) {
	root, ok := tryNewSHA256Repository(t)
	if !ok {
		t.Skip("Git does not support SHA-256 repositories")
	}
	copyMinimalSnapshot(t, root)
	commitAll(t, root, "root")
	modifyFile(t, filepath.Join(root, "topics", "why-files.md"), "\nSHA-256 edit.\n")
	runGit(t, root, nil, "add", "topics/why-files.md")

	store := openStore(t, root)
	if store.repository.Format != gitraw.SHA256 {
		t.Fatalf("object format = %q", store.repository.Format)
	}
	staged, err := store.Staged(context.Background())
	if err != nil {
		t.Fatalf("Staged: %v", err)
	}
	for _, entry := range staged.Index {
		if len(entry.Object) != gitraw.SHA256.HexWidth() {
			t.Fatalf("SHA-256 object %q has width %d", entry.Object, len(entry.Object))
		}
	}
	diff, err := store.DiffStaged(context.Background())
	if err != nil || diff.Stat.Modified != 1 {
		t.Fatalf("SHA-256 diff = %#v, %v", diff, err)
	}
}

func TestOpenRequiresExactWorktreeRoot(t *testing.T) {
	root := newGitRepository(t, gitraw.SHA1, true)
	_, err := Open(context.Background(), filepath.Join(root, "topics"))
	if !errors.Is(err, gitraw.ErrRepository) {
		t.Fatalf("Open(subdirectory) error = %T %v, want repository", err, err)
	}
}

func TestManagedOperationsRejectAcceptedRefChangeAfterCapture(t *testing.T) {
	operations := []struct {
		name string
		run  func(context.Context, *Store) error
	}{
		{name: operationStatus, run: func(ctx context.Context, store *Store) error {
			_, err := store.Status(ctx)
			return err
		}},
		{name: operationDiff, run: func(ctx context.Context, store *Store) error {
			_, err := store.DiffWorking(ctx)
			return err
		}},
		{name: operationCheckStaged, run: func(ctx context.Context, store *Store) error {
			_, _, err := store.CheckStaged(ctx)
			return err
		}},
		{name: operationAudit, run: func(ctx context.Context, store *Store) error {
			_, err := store.AuditAccepted(ctx)
			return err
		}},
	}
	for _, operation := range operations {
		t.Run(operation.name, func(t *testing.T) {
			root := newGitRepository(t, gitraw.SHA1, true)
			oldID := strings.TrimSpace(runGit(t, root, nil, "rev-parse", "HEAD"))
			treeID := strings.TrimSpace(runGit(t, root, nil, "rev-parse", "HEAD^{tree}"))
			newID := strings.TrimSpace(runGit(t, root, nil, "commit-tree", treeID, "-p", oldID, "-m", "concurrent ref move"))
			store := openStore(t, root)
			fired := false
			store.afterCapture = func(got string) {
				if fired || got != operation.name {
					return
				}
				fired = true
				runGit(t, root, nil, "update-ref", "refs/heads/main", newID, oldID)
			}

			err := operation.run(context.Background(), store)
			assertConcurrencyError(t, err, "HEAD/ref")
			if !fired {
				t.Fatal("capture fault seam did not run")
			}
		})
	}
}

func TestStatusRejectsSymbolicHeadChangeAtSameCommit(t *testing.T) {
	root := newGitRepository(t, gitraw.SHA1, true)
	runGit(t, root, nil, "branch", "other", "HEAD")
	store := openStore(t, root)
	store.afterCapture = func(operation string) {
		if operation == operationStatus {
			runGit(t, root, nil, "symbolic-ref", "HEAD", "refs/heads/other")
		}
	}
	_, err := store.Status(context.Background())
	assertConcurrencyError(t, err, "HEAD/ref")
}

func TestStatusAndAuditStartFromFreshAcceptedTip(t *testing.T) {
	root := newGitRepository(t, gitraw.SHA1, true)
	store := openStore(t, root)
	modifyFile(t, filepath.Join(root, "topics", "why-files.md"), "\nCommitted after opening the handle.\n")
	commitAll(t, root, "new accepted tip")
	tip := strings.TrimSpace(runGit(t, root, nil, "rev-parse", "HEAD"))

	status, err := store.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.Accepted.Commit == nil || *status.Accepted.Commit != tip || len(status.Staged) != 0 || len(status.Unstaged) != 0 {
		t.Fatalf("fresh status = %#v", status)
	}
	audit, err := store.AuditAccepted(context.Background())
	if err != nil {
		t.Fatalf("AuditAccepted: %v", err)
	}
	if audit.Tip != tip {
		t.Fatalf("fresh audit tip = %q, want %q", audit.Tip, tip)
	}
}

func TestAuditAcceptedIncludesAndReobservesPresentationE601(t *testing.T) {
	t.Run("finding", func(t *testing.T) {
		root := newGitRepository(t, gitraw.SHA1, true)
		runGit(t, root, nil, "config", "core.autocrlf", "true")
		audit, err := openStore(t, root).AuditAccepted(context.Background())
		if err != nil {
			t.Fatalf("AuditAccepted: %v", err)
		}
		if !hasFinding(audit.Validation.Findings, "E601", ".") {
			t.Fatalf("managed findings = %#v, want presentation E601", audit.Validation.Findings)
		}
	})

	t.Run("concurrent presentation", func(t *testing.T) {
		root := newGitRepository(t, gitraw.SHA1, true)
		store := openStore(t, root)
		store.afterCapture = func(operation string) {
			if operation == operationAudit {
				runGit(t, root, nil, "config", "core.autocrlf", "true")
			}
		}
		_, err := store.AuditAccepted(context.Background())
		assertConcurrencyError(t, err, "presentation")
	})
}

func TestStatusReobservesIndexAndWorkingInputs(t *testing.T) {
	t.Run("index", func(t *testing.T) {
		root := newGitRepository(t, gitraw.SHA1, true)
		store := openStore(t, root)
		store.afterCapture = func(operation string) {
			if operation == operationStatus {
				runGit(t, root, nil, "update-index", "--chmod=+x", "topics/why-files.md")
			}
		}
		_, err := store.Status(context.Background())
		assertConcurrencyError(t, err, "index")
	})

	t.Run("working", func(t *testing.T) {
		root := newGitRepository(t, gitraw.SHA1, true)
		store := openStore(t, root)
		store.afterCapture = func(operation string) {
			if operation == operationStatus {
				modifyFile(t, filepath.Join(root, "topics", "why-files.md"), "\nConcurrent working edit.\n")
			}
		}
		_, err := store.Status(context.Background())
		assertConcurrencyError(t, err, "working")
	})
}

func TestManagedOperationsIgnoreInputsOutsideTheirScope(t *testing.T) {
	t.Run("check staged ignores working", func(t *testing.T) {
		root := newGitRepository(t, gitraw.SHA1, true)
		store := openStore(t, root)
		store.afterCapture = func(operation string) {
			if operation == operationCheckStaged {
				modifyFile(t, filepath.Join(root, "topics", "why-files.md"), "\nUnstaged after capture.\n")
			}
		}
		validation, _, err := store.CheckStaged(context.Background())
		if err != nil || validation.Status != checker.StatusComplete {
			t.Fatalf("CheckStaged = %#v, %v", validation, err)
		}
	})

	t.Run("revision diff ignores index and working", func(t *testing.T) {
		root := newGitRepository(t, gitraw.SHA1, true)
		head := strings.TrimSpace(runGit(t, root, nil, "rev-parse", "HEAD"))
		store := openStore(t, root)
		store.afterCapture = func(operation string) {
			if operation != operationDiff {
				return
			}
			modifyFile(t, filepath.Join(root, "topics", "why-files.md"), "\nDraft outside revision diff.\n")
			runGit(t, root, nil, "add", "topics/why-files.md")
		}
		diff, err := store.Diff(context.Background(), RevisionSelector(head), RevisionSelector(head))
		if err != nil || len(diff.Changes) != 0 {
			t.Fatalf("revision diff = %#v, %v", diff, err)
		}
	})
}

func assertConcurrencyError(t *testing.T, err error, input string) {
	t.Helper()
	var concurrent *ConcurrencyError
	if !errors.Is(err, ErrConcurrent) || !errors.As(err, &concurrent) {
		t.Fatalf("error = %T %v, want managed concurrency error", err, err)
	}
	if !contains(concurrent.Inputs, input) {
		t.Fatalf("concurrency inputs = %#v, want %q", concurrent.Inputs, input)
	}
}

func assertIndexError(t *testing.T, err error, kind IndexFailure, path string) {
	t.Helper()
	var indexError *IndexError
	if !errors.As(err, &indexError) {
		t.Fatalf("error = %T %v, want *IndexError", err, err)
	}
	if indexError.Kind != kind || !contains(indexError.Paths, path) {
		t.Fatalf("index error = %#v, want kind %q path %q", indexError, kind, path)
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func hasFinding(findings []checker.Finding, code, path string) bool {
	for _, finding := range findings {
		if finding.Code == code && finding.Path == path {
			return true
		}
	}
	return false
}

func openStore(t *testing.T, root string) *Store {
	t.Helper()
	store, err := Open(context.Background(), root)
	if err != nil {
		t.Fatalf("Open(%s): %v", root, err)
	}
	return store
}

func newGitRepository(t *testing.T, format gitraw.ObjectFormat, withCommit bool) string {
	t.Helper()
	root := t.TempDir()
	arguments := []string{"init", "--initial-branch=main"}
	if format == gitraw.SHA256 {
		arguments = append(arguments, "--object-format=sha256")
	}
	runGit(t, root, nil, arguments...)
	configureRepository(t, root)
	if withCommit {
		copyMinimalSnapshot(t, root)
		commitAll(t, root, "root")
	}
	return root
}

func tryNewSHA256Repository(t *testing.T) (string, bool) {
	t.Helper()
	root := t.TempDir()
	command := exec.Command("git", "-C", root, "init", "--initial-branch=main", "--object-format=sha256")
	if output, err := command.CombinedOutput(); err != nil {
		t.Logf("git init --object-format=sha256 unavailable: %v: %s", err, output)
		return "", false
	}
	configureRepository(t, root)
	return root, true
}

func configureRepository(t *testing.T, root string) {
	t.Helper()
	runGit(t, root, nil, "config", "core.autocrlf", "false")
	runGit(t, root, nil, "config", "user.name", "Ada")
	runGit(t, root, nil, "config", "user.email", "ada@example.test")
	runGit(t, root, nil, "config", "commit.gpgsign", "false")
}

func commitAll(t *testing.T, root, message string) {
	t.Helper()
	runGit(t, root, nil, "add", "--all")
	environment := []string{
		"GIT_AUTHOR_DATE=2026-08-07T10:00:00+02:00",
		"GIT_COMMITTER_DATE=2026-08-07T10:00:00+02:00",
	}
	runGit(t, root, environment, "commit", "--no-gpg-sign", "-m", message)
}

func runGit(t *testing.T, root string, environment []string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, arguments...)...)
	command.Env = append(os.Environ(), environment...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(arguments, " "), err, output)
	}
	return string(output)
}

func runGitStatus(t *testing.T, root string, environment []string, arguments ...string) int {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, arguments...)...)
	command.Env = append(os.Environ(), environment...)
	output, err := command.CombinedOutput()
	if err == nil {
		return 0
	}
	var exitError *exec.ExitError
	if !errors.As(err, &exitError) {
		t.Fatalf("git %s: %v\n%s", strings.Join(arguments, " "), err, output)
	}
	return exitError.ExitCode()
}

func copyMinimalSnapshot(t *testing.T, destination string) {
	t.Helper()
	source := filepath.Join("..", "..", "examples", "minimal")
	err := filepath.WalkDir(source, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, name)
		if err != nil || relative == "." {
			return err
		}
		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		t.Fatalf("copy minimal snapshot: %v", err)
	}
}

func writeFile(t *testing.T, name string, data []byte, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, data, mode); err != nil {
		t.Fatal(err)
	}
}

func modifyFile(t *testing.T, name, suffix string) {
	t.Helper()
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, suffix...)
	if err := os.WriteFile(name, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func replaceOnce(t *testing.T, name, old, replacement string) {
	t.Helper()
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte(old)) {
		t.Fatalf("%s does not contain %q", name, old)
	}
	data = bytes.Replace(data, []byte(old), []byte(replacement), 1)
	if err := os.WriteFile(name, data, 0o644); err != nil {
		t.Fatal(err)
	}
}
