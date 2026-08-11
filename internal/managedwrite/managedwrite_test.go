package managedwrite

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ontopix/engram/internal/checker"
	"github.com/ontopix/engram/internal/gitraw"
	"github.com/ontopix/engram/internal/guard"
	"github.com/ontopix/engram/internal/hookexec"
	"github.com/ontopix/engram/internal/hooks"
	"github.com/ontopix/engram/internal/journal"
	"github.com/ontopix/engram/internal/rendezvous"
	"github.com/ontopix/engram/internal/treeimage"
)

type acceptingTrust struct {
	mu    sync.Mutex
	calls int
	sets  []hooks.Set
}

func (trust *acceptingTrust) List(_ string, set hooks.Set) (hooks.Selection, error) {
	trust.mu.Lock()
	defer trust.mu.Unlock()
	trust.calls++
	trust.sets = append(trust.sets, set)
	return hooks.Selection{
		SHA256:  set.SHA256,
		Trusted: true,
		Hooks:   append([]hooks.Hook(nil), set.Hooks...),
	}, nil
}

func (trust *acceptingTrust) callCount() int {
	trust.mu.Lock()
	defer trust.mu.Unlock()
	return trust.calls
}

func TestCommitRealAcrossObjectFormats(t *testing.T) {
	for _, format := range []gitraw.ObjectFormat{gitraw.SHA1, gitraw.SHA256} {
		format := format
		t.Run(string(format), func(t *testing.T) {
			fixture := newManagedFixture(t, format, "")
			before := gitOutput(t, fixture.root, "rev-parse", "HEAD")
			appendFile(t, filepath.Join(fixture.root, "topics", "why-files.md"), "\nAccepted by the managed writer.\n")
			gitRun(t, fixture.root, "add", "topics/why-files.md")

			result, err := fixture.engine.Commit(t.Context(), Request{Store: fixture.root, Message: "managed acceptance"})
			if err != nil {
				t.Fatalf("Commit: %v", err)
			}
			if result == nil || !result.Created || result.Commit == nil || result.Base == nil || *result.Base != before || result.Initialization || result.DryRun {
				t.Fatalf("result = %#v", result)
			}
			if result.Validation == nil || result.Validation.Findings == nil {
				t.Fatalf("validation findings must encode as a non-null array: %#v", result.Validation)
			}
			if got := gitOutput(t, fixture.root, "rev-parse", "HEAD"); got != *result.Commit {
				t.Fatalf("HEAD = %s, result commit = %s", got, *result.Commit)
			}
			if got := gitOutput(t, fixture.root, "rev-parse", "HEAD^"); got != before {
				t.Fatalf("parent = %s, want %s", got, before)
			}
			if got := strings.TrimSpace(gitOutput(t, fixture.root, "show", "-s", "--format=%B", "HEAD")); got != "managed acceptance" {
				t.Fatalf("message = %q", got)
			}
			assertNoTransactionArtifacts(t, fixture.root)
		})
	}
}

func TestCommitMessageLineFeedContract(t *testing.T) {
	if err := validateMessage("subject\nbody"); err != nil {
		t.Fatalf("internal LF rejected: %v", err)
	}
	if err := validateMessage("subject\n"); err == nil {
		t.Fatal("final LF accepted")
	}
}

func TestCreateCandidateIndexErrorCleansPrivateRootWithoutPanic(t *testing.T) {
	fixture := newManagedFixtureWithoutGuard(t, gitraw.SHA1, "")
	repository, err := gitraw.Discover(t.Context(), fixture.root)
	if err != nil {
		t.Fatal(err)
	}
	git, err := newGitClient(fixture.root)
	if err != nil {
		t.Fatal(err)
	}
	temporaryRoot := t.TempDir()
	_, _, _, cleanup, err := createCandidateIndex(t.Context(), git, repository, treeimage.Image{
		"unsupported": {Kind: treeimage.Symlink},
	}, temporaryRoot)
	if err == nil || !strings.Contains(err.Error(), "non-regular path") {
		t.Fatalf("error = %v", err)
	}
	if cleanup != nil {
		t.Fatal("failed candidate index returned a cleanup handle")
	}
	entries, err := os.ReadDir(temporaryRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("private candidate root remains: %v", entries)
	}
}

func TestCommitDryRunHasNoPersistentEffectsAndNeedsNoGuardOrMessage(t *testing.T) {
	fixture := newManagedFixtureWithoutGuard(t, gitraw.SHA1, "")
	appendFile(t, filepath.Join(fixture.root, "topics", "why-files.md"), "\nDry-run candidate.\n")
	gitRun(t, fixture.root, "add", "topics/why-files.md")

	headBefore := gitOutput(t, fixture.root, "rev-parse", "HEAD")
	indexBefore := readFile(t, filepath.Join(fixture.gitDir, "index"))
	worktreeBefore := readFile(t, filepath.Join(fixture.root, "topics", "why-files.md"))
	objectsBefore := relativeFiles(t, filepath.Join(fixture.gitDir, "objects"))

	result, err := fixture.engine.Commit(t.Context(), Request{Store: fixture.root, DryRun: true})
	if err != nil {
		t.Fatalf("dry-run Commit: %v", err)
	}
	if result == nil || !result.DryRun || result.Created || result.Commit != nil || len(result.Changes) == 0 {
		t.Fatalf("dry-run result = %#v", result)
	}
	if got := gitOutput(t, fixture.root, "rev-parse", "HEAD"); got != headBefore {
		t.Fatalf("dry-run moved HEAD: %s -> %s", headBefore, got)
	}
	assertBytes(t, filepath.Join(fixture.gitDir, "index"), indexBefore)
	assertBytes(t, filepath.Join(fixture.root, "topics", "why-files.md"), worktreeBefore)
	if got := relativeFiles(t, filepath.Join(fixture.gitDir, "objects")); !equalStrings(got, objectsBefore) {
		t.Fatalf("dry-run object store changed:\nbefore %#v\nafter  %#v", objectsBefore, got)
	}
	if _, err := os.Lstat(filepath.Join(fixture.gitDir, "hooks", "pre-commit")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dry-run created or altered guard: %v", err)
	}
	assertNoTransactionArtifacts(t, fixture.root)
}

func TestCommitNoOpSkipsHooksAndMessageWhilePreservingDirtyWorktree(t *testing.T) {
	counter := filepath.Join(t.TempDir(), "hook-count")
	hook := countingHook(counter, false)
	fixture := newManagedFixture(t, gitraw.SHA1, hook)
	appendFile(t, filepath.Join(fixture.root, "topics", "derived-state.md"), "\nUnstaged work remains.\n")
	writeFile(t, filepath.Join(fixture.root, "scratch.txt"), []byte("untracked\n"), 0o644)
	headBefore := gitOutput(t, fixture.root, "rev-parse", "HEAD")

	result, err := fixture.engine.Commit(t.Context(), Request{Store: fixture.root})
	if err != nil {
		t.Fatalf("no-op Commit: %v", err)
	}
	if result == nil || result.Created || result.Commit != nil || len(result.Changes) != 0 {
		t.Fatalf("no-op result = %#v", result)
	}
	if got := gitOutput(t, fixture.root, "rev-parse", "HEAD"); got != headBefore {
		t.Fatalf("no-op moved HEAD: %s -> %s", headBefore, got)
	}
	if _, err := os.Stat(counter); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("no-op ran preparation hook: %v", err)
	}
	if fixture.trust.callCount() != 0 {
		t.Fatalf("no-op consulted hook trust %d times", fixture.trust.callCount())
	}
	assertContains(t, filepath.Join(fixture.root, "topics", "derived-state.md"), "Unstaged work remains.")
	assertBytes(t, filepath.Join(fixture.root, "scratch.txt"), []byte("untracked\n"))
	assertNoTransactionArtifacts(t, fixture.root)
}

func TestCommitPreservesUnstagedAndUntrackedPathsOutsideCandidate(t *testing.T) {
	fixture := newManagedFixture(t, gitraw.SHA1, "")
	target := filepath.Join(fixture.root, "topics", "why-files.md")
	appendFile(t, target, "\nStaged candidate.\n")
	gitRun(t, fixture.root, "add", "topics/why-files.md")
	staged := readFile(t, target)
	other := filepath.Join(fixture.root, "topics", "derived-state.md")
	appendFile(t, other, "\nUnstaged and intentionally outside the candidate.\n")
	untracked := filepath.Join(fixture.root, "local-scratch.txt")
	writeFile(t, untracked, []byte("keep me\n"), 0o640)

	result, err := fixture.engine.Commit(t.Context(), Request{Store: fixture.root, Message: "preserve local edits"})
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if result == nil || !result.Created {
		t.Fatalf("result = %#v", result)
	}
	assertBytes(t, target, staged)
	assertContains(t, other, "Unstaged and intentionally outside the candidate.")
	assertBytes(t, untracked, []byte("keep me\n"))
	if status := gitOutput(t, fixture.root, "status", "--porcelain=v1"); !strings.Contains(status, " M topics/derived-state.md") || !strings.Contains(status, "?? local-scratch.txt") {
		t.Fatalf("preserved status = %q", status)
	}
	assertNoTransactionArtifacts(t, fixture.root)
}

func TestCommitImageBaseCleanRequirementsAndReconciliation(t *testing.T) {
	t.Run("reconciles exact clean expected base", func(t *testing.T) {
		fixture := newManagedFixture(t, gitraw.SHA1, "")
		base := gitOutput(t, fixture.root, "rev-parse", "HEAD")
		candidate, modes := candidateFromWorktree(t, fixture.root, func(root string) {
			appendFile(t, filepath.Join(root, "topics", "why-files.md"), "\nSealed image candidate.\n")
		})
		result, err := fixture.engine.CommitImage(t.Context(), ImageRequest{
			Store: fixture.root, Message: "accept sealed image", Candidate: candidate, Modes: modes,
			RequireClean: true, RequireBase: true, ExpectedBase: &base,
		})
		if err != nil {
			t.Fatalf("CommitImage: %v", err)
		}
		if result == nil || !result.Created || result.Commit == nil || result.Base == nil || *result.Base != base {
			t.Fatalf("result = %#v", result)
		}
		assertContains(t, filepath.Join(fixture.root, "topics", "why-files.md"), "Sealed image candidate.")
		if status := gitOutput(t, fixture.root, "status", "--porcelain=v1"); status != "" {
			t.Fatalf("reconciled store is dirty: %q", status)
		}
		assertNoTransactionArtifacts(t, fixture.root)
	})

	t.Run("rejects wrong expected base", func(t *testing.T) {
		fixture := newManagedFixture(t, gitraw.SHA1, "")
		before := gitOutput(t, fixture.root, "rev-parse", "HEAD")
		candidate, modes := candidateFromWorktree(t, fixture.root, func(root string) {
			appendFile(t, filepath.Join(root, "topics", "why-files.md"), "\nMust not land.\n")
		})
		wrong := strings.Repeat("0", 40)
		result, err := fixture.engine.CommitImage(t.Context(), ImageRequest{
			Store: fixture.root, Message: "wrong base", Candidate: candidate, Modes: modes,
			RequireBase: true, ExpectedBase: &wrong,
		})
		if result != nil || KindOf(err) != FailureConcurrency {
			t.Fatalf("result, error = %#v, %v", result, err)
		}
		if got := gitOutput(t, fixture.root, "rev-parse", "HEAD"); got != before {
			t.Fatalf("wrong-base request moved HEAD: %s -> %s", before, got)
		}
		assertNoTransactionArtifacts(t, fixture.root)
	})

	t.Run("rejects dirty worktree when clean is required", func(t *testing.T) {
		fixture := newManagedFixture(t, gitraw.SHA1, "")
		base := gitOutput(t, fixture.root, "rev-parse", "HEAD")
		candidate, modes := candidateFromWorktree(t, fixture.root, func(root string) {
			appendFile(t, filepath.Join(root, "topics", "why-files.md"), "\nMust not land.\n")
		})
		appendFile(t, filepath.Join(fixture.root, "topics", "derived-state.md"), "\nDirty worktree.\n")
		result, err := fixture.engine.CommitImage(t.Context(), ImageRequest{
			Store: fixture.root, Message: "requires clean", Candidate: candidate, Modes: modes,
			RequireClean: true, RequireBase: true, ExpectedBase: &base,
		})
		if result != nil || KindOf(err) != FailureConcurrency {
			t.Fatalf("result, error = %#v, %v", result, err)
		}
		if got := gitOutput(t, fixture.root, "rev-parse", "HEAD"); got != base {
			t.Fatalf("dirty request moved HEAD: %s -> %s", base, got)
		}
		assertContains(t, filepath.Join(fixture.root, "topics", "derived-state.md"), "Dirty worktree.")
		assertNoTransactionArtifacts(t, fixture.root)
	})
}

func TestCommitRunsPreparationExactlyOnceAndSuppressesNativeHooksAndGitEnvironment(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-hook integration is Unix-specific")
	}
	counter := filepath.Join(t.TempDir(), "hook-count")
	fixture := newManagedFixture(t, gitraw.SHA1, countingHook(counter, true))
	nativeSentinel := filepath.Join(t.TempDir(), "native-hook-ran")
	writeFile(t, filepath.Join(fixture.gitDir, "hooks", "reference-transaction"), []byte("#!/bin/sh\nprintf native >> "+shellQuote(nativeSentinel)+"\n"), 0o700)
	writeFile(t, filepath.Join(fixture.gitDir, "hooks", "post-commit"), []byte("#!/bin/sh\nprintf native >> "+shellQuote(nativeSentinel)+"\n"), 0o700)
	appendFile(t, filepath.Join(fixture.root, "topics", "why-files.md"), "\nInitial staged edit.\n")
	gitRun(t, fixture.root, "add", "topics/why-files.md")
	fixture.engine.Hooks.Environment = []string{
		"PATH=" + os.Getenv("PATH"),
		"GIT_DIR=/must/not/leak",
		"GIT_INDEX_FILE=/must/not/leak",
		"ENGRAM_SECRET=must-not-leak",
	}
	t.Setenv("GIT_DIR", "/ambient/must/not/control/repository")
	t.Setenv("GIT_INDEX_FILE", "/ambient/must/not/control/index")
	t.Setenv("GIT_AUTHOR_NAME", "Ambient Attacker")
	t.Setenv("ENGRAM_SECRET", "ambient-must-not-leak")

	result, err := fixture.engine.Commit(t.Context(), Request{Store: fixture.root, Message: "one prepared acceptance"})
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if result == nil || !result.Created || len(result.Diagnostics) != 1 {
		t.Fatalf("result = %#v", result)
	}
	assertBytes(t, counter, []byte("1\n"))
	assertContains(t, filepath.Join(fixture.root, "topics", "why-files.md"), "Prepared exactly once.")
	if _, err := os.Stat(nativeSentinel); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("native Git hook ran: %v", err)
	}
	commit := gitOutput(t, fixture.root, "show", "-s", "--format=%an <%ae>", "HEAD")
	if commit != "Ada Lovelace <ada@example.test>" {
		t.Fatalf("raw commit identity = %q", commit)
	}
	assertNoTransactionArtifacts(t, fixture.root)
}

func TestPostCASFaultRecoversWithoutRerunningHook(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-hook integration is Unix-specific")
	}
	counter := filepath.Join(t.TempDir(), "hook-count")
	fixture := newManagedFixture(t, gitraw.SHA1, countingHook(counter, false))
	appendFile(t, filepath.Join(fixture.root, "topics", "why-files.md"), "\nStaged before recoverable fault.\n")
	gitRun(t, fixture.root, "add", "topics/why-files.md")
	fixture.engine.Fault = failOnceAt(PhaseRefUpdated)

	result, err := fixture.engine.Commit(t.Context(), Request{Store: fixture.root, Message: "recover me"})
	if result == nil || err == nil {
		t.Fatalf("post-CAS result, error = %#v, %v", result, err)
	}
	var detail *Error
	if !errors.As(err, &detail) || !detail.Accepted || detail.Commit == "" || KindOf(err) != FailureRecovery {
		t.Fatalf("post-CAS error = %#v (%v)", detail, err)
	}
	if got := gitOutput(t, fixture.root, "rev-parse", "HEAD"); got != detail.Commit {
		t.Fatalf("accepted ref = %s, fault commit = %s", got, detail.Commit)
	}
	assertBytes(t, counter, []byte("1\n"))
	assertTransactionArtifacts(t, fixture.root)

	recovery := &Engine{OwnerAlive: func(context.Context, rendezvous.Owner) (bool, error) { return false, nil }}
	recovered, recoverErr := recovery.Recover(t.Context(), fixture.root)
	if recoverErr != nil {
		t.Fatalf("Recover: %v", recoverErr)
	}
	if recovered == nil || !recovered.Needed || !recovered.Performed || recovered.Action != RecoveryReconciled || recovered.Accepted == nil || *recovered.Accepted != detail.Commit || !recovered.CheckoutChanged {
		t.Fatalf("recovery result = %#v", recovered)
	}
	assertBytes(t, counter, []byte("1\n"))
	assertContains(t, filepath.Join(fixture.root, "topics", "why-files.md"), "Prepared exactly once.")
	if status := gitOutput(t, fixture.root, "status", "--porcelain=v1"); status != "" {
		t.Fatalf("recovered store is dirty: %q", status)
	}
	assertNoTransactionArtifacts(t, fixture.root)
}

func TestPreCASFaultCancelsAndCleansTransaction(t *testing.T) {
	fixture := newManagedFixture(t, gitraw.SHA1, "")
	before := gitOutput(t, fixture.root, "rev-parse", "HEAD")
	appendFile(t, filepath.Join(fixture.root, "topics", "why-files.md"), "\nPrepared but never accepted.\n")
	gitRun(t, fixture.root, "add", "topics/why-files.md")
	fixture.engine.Fault = failOnceAt(PhaseFinalRecheck)

	result, err := fixture.engine.Commit(t.Context(), Request{Store: fixture.root, Message: "must abort"})
	if result == nil || err == nil {
		t.Fatalf("pre-CAS result, error = %#v, %v", result, err)
	}
	var detail *Error
	if !errors.As(err, &detail) || !detail.Durable || detail.Accepted || detail.CheckoutChanged || detail.RecoveryRequired {
		t.Fatalf("cleaned pre-CAS effects = %#v (%v)", detail, err)
	}
	if got := gitOutput(t, fixture.root, "rev-parse", "HEAD"); got != before {
		t.Fatalf("pre-CAS fault moved HEAD: %s -> %s", before, got)
	}
	assertNoTransactionArtifacts(t, fixture.root)
}

func TestWritePendingPostPublicationErrorRetainsRecoverableEvidence(t *testing.T) {
	fixture := newManagedFixture(t, gitraw.SHA1, "")
	appendFile(t, filepath.Join(fixture.root, "topics", "why-files.md"), "\nPublished journal with failed acknowledgement.\n")
	gitRun(t, fixture.root, "add", "topics/why-files.md")
	injected := errors.New("journal publication acknowledgement failed")
	fixture.engine.WritePending = func(name string, record journal.Record) error {
		if err := journal.WritePending(name, record); err != nil {
			return err
		}
		return &journal.EffectError{Effect: journal.Effect{Visible: true, Durable: true}, Err: injected}
	}
	result, err := fixture.engine.Commit(t.Context(), Request{Store: fixture.root, Message: "retain pre-CAS journal"})
	var detail *Error
	if result == nil || !errors.Is(err, injected) || !errors.As(err, &detail) || !detail.Durable || detail.Accepted || detail.CheckoutChanged || !detail.RecoveryRequired || detail.Phase != PhaseJournalPending {
		t.Fatalf("post-publication error = %#v, %v; result = %#v", detail, err, result)
	}
	assertTransactionArtifacts(t, fixture.root)
	recovery := &Engine{OwnerAlive: func(context.Context, rendezvous.Owner) (bool, error) { return false, nil }}
	recovered, recoverErr := recovery.Recover(t.Context(), fixture.root)
	if recoverErr != nil || recovered == nil || !recovered.Needed || !recovered.Performed || recovered.Action != RecoveryCancelled || recovered.RecoveryRequired {
		t.Fatalf("pre-CAS journal recovery = %#v, %v", recovered, recoverErr)
	}
	assertNoTransactionArtifacts(t, fixture.root)
}

func TestWritePendingVisiblePreSyncErrorDoesNotClaimDurability(t *testing.T) {
	fixture := newManagedFixture(t, gitraw.SHA1, "")
	appendFile(t, filepath.Join(fixture.root, "topics", "why-files.md"), "\nVisible journal without durability acknowledgement.\n")
	gitRun(t, fixture.root, "add", "topics/why-files.md")
	injected := errors.New("journal directory sync failed")
	fixture.engine.WritePending = func(name string, record journal.Record) error {
		if err := journal.WritePending(name, record); err != nil {
			return err
		}
		return &journal.EffectError{Effect: journal.Effect{Visible: true, Durable: false}, Err: injected}
	}
	result, err := fixture.engine.Commit(t.Context(), Request{Store: fixture.root, Message: "retain visible pre-sync journal"})
	var detail *Error
	if result == nil || !errors.Is(err, injected) || !errors.As(err, &detail) || detail.Durable || detail.Accepted || detail.CheckoutChanged || !detail.RecoveryRequired || detail.Phase != PhaseJournalPending {
		t.Fatalf("visible pre-sync error = %#v, %v; result = %#v", detail, err, result)
	}
	assertTransactionArtifacts(t, fixture.root)
}

func TestReleaseFailureRetainsPreJournalDurabilityEvidence(t *testing.T) {
	fixture := newManagedFixture(t, gitraw.SHA1, "")
	appendFile(t, filepath.Join(fixture.root, "topics", "why-files.md"), "\nRelease failure before journal.\n")
	gitRun(t, fixture.root, "add", "topics/why-files.md")
	fixture.engine.Fault = failOnceAt(PhaseLocked)
	injected := errors.New("rendezvous release failed")
	fixture.engine.ReleaseLock = func(*rendezvous.Handle) error { return injected }
	_, err := fixture.engine.Commit(t.Context(), Request{Store: fixture.root, Message: "retain pre-journal locks"})
	var detail *Error
	if !errors.Is(err, injected) || !errors.As(err, &detail) || !detail.Durable || detail.Accepted || detail.CheckoutChanged || !detail.RecoveryRequired {
		t.Fatalf("release error = %#v, %v", detail, err)
	}
	if _, err := os.Lstat(rendezvous.WorktreePath(fixture.gitDir)); err != nil {
		t.Fatalf("worktree lock was not retained: %v", err)
	}
	recovery := &Engine{OwnerAlive: func(context.Context, rendezvous.Owner) (bool, error) { return false, nil }}
	recovered, recoverErr := recovery.Recover(t.Context(), fixture.root)
	if recoverErr != nil || recovered == nil || !recovered.Performed || recovered.Action != RecoveryStaleLock {
		t.Fatalf("pre-journal lock recovery = %#v, %v", recovered, recoverErr)
	}
}

func TestRecoverCancelledJournalAfterPartialLockRelease(t *testing.T) {
	fixture := newManagedFixture(t, gitraw.SHA1, "")
	appendFile(t, filepath.Join(fixture.root, "topics", "why-files.md"), "\nPartial cancelled-lock cleanup.\n")
	gitRun(t, fixture.root, "add", "topics/why-files.md")
	fixture.engine.Fault = failOnceAt(PhaseFinalRecheck)
	fixture.engine.ReleaseLock = func(*rendezvous.Handle) error {
		return errors.New("interrupt cancelled lock release")
	}
	_, err := fixture.engine.Commit(t.Context(), Request{Store: fixture.root, Message: "retain cancelled journal"})
	var detail *Error
	if !errors.As(err, &detail) || !detail.Durable || !detail.RecoveryRequired || detail.Accepted {
		t.Fatalf("cancelled release error = %#v, %v", detail, err)
	}
	record, _, err := journal.Read(journal.Path(fixture.gitDir))
	if err != nil || record.State != journal.Cancelled {
		t.Fatalf("cancelled journal = %#v, %v", record, err)
	}
	// A production Release can remove the worktree lock and then fail while
	// syncing its directory. Recovery must adopt and remove the exact remaining
	// ref-lock subset instead of demanding that both original files survive.
	if err := os.Remove(rendezvous.WorktreePath(fixture.gitDir)); err != nil {
		t.Fatal(err)
	}
	recovery := &Engine{OwnerAlive: func(context.Context, rendezvous.Owner) (bool, error) { return false, nil }}
	result, recoverErr := recovery.Recover(t.Context(), fixture.root)
	if recoverErr != nil || result == nil || !result.Needed || !result.Performed || result.Action != RecoveryCancelled || result.RecoveryRequired {
		t.Fatalf("partial cancelled cleanup = %#v, %v", result, recoverErr)
	}
	assertNoTransactionArtifacts(t, fixture.root)
}

func TestEveryAcceptanceBoundaryHasCrashSafeOutcome(t *testing.T) {
	preCAS := []Phase{
		PhaseLocked, PhaseCaptured, PhaseAudited, PhasePrepared, PhaseProven,
		PhaseObjectsWritten, PhaseJournalPending, PhaseJournalRequired, PhaseFinalRecheck,
	}
	for _, phase := range preCAS {
		phase := phase
		t.Run("pre-cas/"+string(phase), func(t *testing.T) {
			fixture := newManagedFixture(t, gitraw.SHA1, "")
			before := gitOutput(t, fixture.root, "rev-parse", "HEAD")
			appendFile(t, filepath.Join(fixture.root, "topics", "why-files.md"), "\nBoundary fault.\n")
			gitRun(t, fixture.root, "add", "topics/why-files.md")
			fixture.engine.Fault = failOnceAt(phase)

			if _, err := fixture.engine.Commit(t.Context(), Request{Store: fixture.root, Message: "boundary fault"}); err == nil {
				t.Fatalf("fault at %s returned success", phase)
			}
			if got := gitOutput(t, fixture.root, "rev-parse", "HEAD"); got != before {
				t.Fatalf("fault at %s moved HEAD: %s -> %s", phase, before, got)
			}
			assertNoTransactionArtifacts(t, fixture.root)
		})
	}

	postCAS := []Phase{
		PhaseRefUpdated, PhaseIndexReconciled, PhaseWorktreeReconciled,
		PhaseJournalComplete, PhaseLocksReleased, PhaseJournalRemoved,
	}
	for _, phase := range postCAS {
		phase := phase
		t.Run("post-cas/"+string(phase), func(t *testing.T) {
			fixture := newManagedFixture(t, gitraw.SHA1, "")
			appendFile(t, filepath.Join(fixture.root, "topics", "why-files.md"), "\nBoundary fault.\n")
			gitRun(t, fixture.root, "add", "topics/why-files.md")
			fixture.engine.Fault = failOnceAt(phase)

			_, commitErr := fixture.engine.Commit(t.Context(), Request{Store: fixture.root, Message: "boundary fault"})
			var detail *Error
			if !errors.As(commitErr, &detail) || !detail.Accepted || detail.Commit == "" {
				t.Fatalf("fault at %s = %#v (%v), want known accepted outcome", phase, detail, commitErr)
			}
			// Even without a preparation hook, the final raw index can differ
			// from the captured index through controller-owned stat-cache bytes.
			// The reconciliation primitive, not the phase name, supplies this
			// exact mutation evidence.
			wantCheckout := phase != PhaseRefUpdated
			if detail.CheckoutChanged != wantCheckout || !detail.Durable || detail.RecoveryRequired != (phase != PhaseJournalRemoved) {
				t.Fatalf("fault at %s effects = %#v, want checkout=%t", phase, detail, wantCheckout)
			}
			if got := gitOutput(t, fixture.root, "rev-parse", "HEAD"); got != detail.Commit {
				t.Fatalf("fault at %s left HEAD %s, want %s", phase, got, detail.Commit)
			}
			recovery := &Engine{OwnerAlive: func(context.Context, rendezvous.Owner) (bool, error) { return false, nil }}
			recovered, recoverErr := recovery.Recover(t.Context(), fixture.root)
			if recoverErr != nil {
				t.Fatalf("Recover after %s: %v", phase, recoverErr)
			}
			if phase == PhaseJournalRemoved {
				if recovered == nil || recovered.Needed || recovered.Performed {
					t.Fatalf("recovery after removed journal = %#v", recovered)
				}
			} else if recovered == nil || !recovered.Needed || !recovered.Performed {
				t.Fatalf("recovery after %s = %#v", phase, recovered)
			}
			if status := gitOutput(t, fixture.root, "status", "--porcelain=v1"); status != "" {
				t.Fatalf("recovered store after %s is dirty: %q", phase, status)
			}
			assertNoTransactionArtifacts(t, fixture.root)
		})
	}
}

func TestRecoverPendingOldRemainsBlocked(t *testing.T) {
	fixture := newManagedFixture(t, gitraw.SHA1, "")
	old := gitOutput(t, fixture.root, "rev-parse", "HEAD")
	appendFile(t, filepath.Join(fixture.root, "topics", "why-files.md"), "\nAccepted then deliberately rolled back.\n")
	gitRun(t, fixture.root, "add", "topics/why-files.md")
	fixture.engine.Fault = failOnceAt(PhaseRefUpdated)
	_, commitErr := fixture.engine.Commit(t.Context(), Request{Store: fixture.root, Message: "pending new"})
	var detail *Error
	if !errors.As(commitErr, &detail) || !detail.Accepted || detail.Commit == "" {
		t.Fatalf("post-CAS error = %#v (%v)", detail, commitErr)
	}
	gitRun(t, fixture.root, "update-ref", "refs/heads/main", old, detail.Commit)
	journalName := journal.Path(fixture.gitDir)
	journalBefore := readFile(t, journalName)

	recovery := &Engine{OwnerAlive: func(context.Context, rendezvous.Owner) (bool, error) { return false, nil }}
	result, err := recovery.Recover(t.Context(), fixture.root)
	if result == nil || !result.Needed || err == nil || KindOf(err) != FailureRecovery {
		t.Fatalf("pending-old result, error = %#v, %v", result, err)
	}
	if got := gitOutput(t, fixture.root, "rev-parse", "HEAD"); got != old {
		t.Fatalf("recovery changed pending-old ref: %s -> %s", old, got)
	}
	assertBytes(t, journalName, journalBefore)
	assertTransactionArtifacts(t, fixture.root)
}

func TestRecoverExpectedRejectsChangedApprovalAndReportsPartialCheckout(t *testing.T) {
	fixture := newManagedFixture(t, gitraw.SHA1, "")
	appendFile(t, filepath.Join(fixture.root, "topics", "why-files.md"), "\nApproved recovery evidence.\n")
	gitRun(t, fixture.root, "add", "topics/why-files.md")
	fixture.engine.Fault = failOnceAt(PhaseRefUpdated)
	_, commitErr := fixture.engine.Commit(t.Context(), Request{Store: fixture.root, Message: "approved recovery"})
	if commitErr == nil {
		t.Fatal("commit did not retain recovery state")
	}
	record, raw, err := journal.Read(journal.Path(fixture.gitDir))
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(raw)
	expected := RecoveryExpectation{OwnerToken: record.OwnerToken, StateSHA256: hex.EncodeToString(digest[:])}

	wrong := expected
	wrong.StateSHA256 = strings.Repeat("0", 64)
	recovery := &Engine{OwnerAlive: func(context.Context, rendezvous.Owner) (bool, error) { return false, nil }}
	result, err := recovery.RecoverExpected(t.Context(), fixture.root, wrong)
	if result == nil || !result.Needed || !result.RecoveryRequired || err == nil || KindOf(err) != FailureConcurrency {
		t.Fatalf("changed approval = %#v, %v", result, err)
	}
	assertBytes(t, journal.Path(fixture.gitDir), raw)
	refLock := rendezvous.RefPath(fixture.gitDir, record.Ref.Ref)
	worktreeLock := rendezvous.WorktreePath(fixture.gitDir)
	refOwnerBefore := readFile(t, refLock)
	worktreeOwnerBefore := readFile(t, worktreeLock)
	recovery.Fault = func(phase Phase) error {
		if phase == PhaseRecoveryLeased {
			return os.Remove(journal.Path(fixture.gitDir))
		}
		return nil
	}
	result, err = recovery.RecoverExpected(t.Context(), fixture.root, expected)
	if result == nil || !result.Needed || result.Durable || !result.RecoveryRequired || err == nil || KindOf(err) != FailureConcurrency {
		t.Fatalf("lease recheck = %#v, %v", result, err)
	}
	assertBytes(t, refLock, refOwnerBefore)
	assertBytes(t, worktreeLock, worktreeOwnerBefore)
	writeFile(t, journal.Path(fixture.gitDir), raw, 0o600)

	recovery.Fault = failOnceAt(PhaseIndexReconciled)
	result, err = recovery.RecoverExpected(t.Context(), fixture.root, expected)
	if result == nil || !result.Needed || result.Performed || !result.Durable || !result.CheckoutChanged || !result.RecoveryRequired || err == nil || KindOf(err) != FailureRecovery {
		t.Fatalf("partial recovery = %#v, %v", result, err)
	}
	recovery.Fault = nil
	result, err = recovery.RecoverExpected(t.Context(), fixture.root, expected)
	if err != nil || result == nil || !result.Performed || !result.Durable || result.CheckoutChanged || result.RecoveryRequired {
		t.Fatalf("completed recovery = %#v, %v", result, err)
	}
}

func TestRecoveryDoesNotInventCheckoutMutationForAlreadyReconciledImages(t *testing.T) {
	fixture := newManagedFixture(t, gitraw.SHA1, "")
	appendFile(t, filepath.Join(fixture.root, "topics", "why-files.md"), "\nAlready reconciled recovery image.\n")
	gitRun(t, fixture.root, "add", "topics/why-files.md")
	fixture.engine.Fault = failOnceAt(PhaseIndexReconciled)
	_, commitErr := fixture.engine.Commit(t.Context(), Request{Store: fixture.root, Message: "stop after index reconciliation"})
	var commitDetail *Error
	if !errors.As(commitErr, &commitDetail) || !commitDetail.CheckoutChanged || !commitDetail.RecoveryRequired {
		t.Fatalf("initial reconciliation effects = %#v, %v", commitDetail, commitErr)
	}

	recovery := &Engine{
		OwnerAlive: func(context.Context, rendezvous.Owner) (bool, error) { return false, nil },
		Fault:      failOnceAt(PhaseIndexReconciled),
	}
	result, err := recovery.Recover(t.Context(), fixture.root)
	if result == nil || !result.Needed || result.Performed || result.CheckoutChanged || !result.RecoveryRequired || err == nil {
		t.Fatalf("already-reconciled fault effects = %#v, %v", result, err)
	}
	recovery.Fault = nil
	result, err = recovery.Recover(t.Context(), fixture.root)
	if err != nil || result == nil || !result.Performed || result.CheckoutChanged || result.RecoveryRequired {
		t.Fatalf("already-reconciled completion effects = %#v, %v", result, err)
	}
}

func TestRecoverLeavesForeignOrMalformedStateUntouched(t *testing.T) {
	t.Run("malformed journal", func(t *testing.T) {
		fixture := newManagedFixture(t, gitraw.SHA1, "")
		name := journal.Path(fixture.gitDir)
		foreign := []byte("{not an engram journal}\n")
		writeFile(t, name, foreign, 0o600)
		result, err := fixture.engine.Recover(t.Context(), fixture.root)
		if result == nil || !result.Needed || err == nil || KindOf(err) != FailureRecovery {
			t.Fatalf("result, error = %#v, %v", result, err)
		}
		assertBytes(t, name, foreign)
	})

	t.Run("malformed worktree lock", func(t *testing.T) {
		fixture := newManagedFixture(t, gitraw.SHA1, "")
		name := rendezvous.WorktreePath(fixture.gitDir)
		foreign := []byte("not-owned-lock-bytes\n")
		writeFile(t, name, foreign, 0o600)
		result, err := fixture.engine.Recover(t.Context(), fixture.root)
		if result == nil || !result.Needed || err == nil || KindOf(err) != FailureRecovery {
			t.Fatalf("result, error = %#v, %v", result, err)
		}
		assertBytes(t, name, foreign)
	})

	t.Run("foreign ref-lock entry", func(t *testing.T) {
		fixture := newManagedFixture(t, gitraw.SHA1, "")
		name := filepath.Join(fixture.gitDir, "engram", "locks", "refs", "foreign")
		foreign := []byte("leave me\n")
		writeFile(t, name, foreign, 0o600)
		result, err := fixture.engine.Recover(t.Context(), fixture.root)
		if result == nil || !result.Needed || err == nil || KindOf(err) != FailureRecovery {
			t.Fatalf("result, error = %#v, %v", result, err)
		}
		assertBytes(t, name, foreign)
	})
}

func TestCommitRejectsFinalInputRaces(t *testing.T) {
	for _, race := range []string{"ref", "index", "worktree", "config", "attributes"} {
		race := race
		t.Run(race, func(t *testing.T) {
			fixture := newManagedFixture(t, gitraw.SHA1, "")
			base := gitOutput(t, fixture.root, "rev-parse", "HEAD")
			candidate, modes := candidateFromWorktree(t, fixture.root, func(root string) {
				appendFile(t, filepath.Join(root, "topics", "why-files.md"), "\nCandidate subject to a race.\n")
			})
			var competitor string
			if race == "ref" {
				tree := gitOutput(t, fixture.root, "rev-parse", "HEAD^{tree}")
				competitor = gitOutput(t, fixture.root, "commit-tree", tree, "-p", base, "-m", "competing commit")
			}
			indexName := filepath.Join(fixture.gitDir, "index")
			indexBefore := readFile(t, indexName)
			thirdWorktree := []byte("third-party worktree bytes\n")
			fixture.engine.Fault = mutateOnceAt(t, PhaseProven, func() {
				switch race {
				case "ref":
					gitRun(t, fixture.root, "update-ref", "refs/heads/main", competitor, base)
				case "index":
					writeFile(t, indexName, append(append([]byte(nil), indexBefore...), 0xff), 0o600)
				case "worktree":
					writeFile(t, filepath.Join(fixture.root, "topics", "why-files.md"), thirdWorktree, 0o644)
				case "config":
					gitRun(t, fixture.root, "config", "user.name", "Concurrent Writer")
				case "attributes":
					writeFile(t, filepath.Join(fixture.root, ".gitattributes"), []byte("*.md text\n"), 0o644)
				}
			})
			result, err := fixture.engine.CommitImage(t.Context(), ImageRequest{
				Store: fixture.root, Message: "race must abort", Candidate: candidate, Modes: modes,
				RequireBase: true, ExpectedBase: &base,
			})
			if result == nil || err == nil {
				t.Fatalf("race result, error = %#v, %v", result, err)
			}
			if KindOf(err) != FailureConcurrency {
				t.Fatalf("race kind = %q, error = %v", KindOf(err), err)
			}
			wantHead := base
			if race == "ref" {
				wantHead = competitor
			}
			if got := gitOutput(t, fixture.root, "rev-parse", "HEAD"); got != wantHead {
				t.Fatalf("race %s changed accepted ref to %s, want %s", race, got, wantHead)
			}
			if race == "worktree" {
				assertBytes(t, filepath.Join(fixture.root, "topics", "why-files.md"), thirdWorktree)
			}
			assertNoTransactionArtifacts(t, fixture.root)
		})
	}
}

func TestCommitInitializesParentlessRoot(t *testing.T) {
	fixture := newUnbornFixture(t, gitraw.SHA1)
	result, err := fixture.engine.Commit(t.Context(), Request{Store: fixture.root, Message: "initialize managed root"})
	if err != nil {
		t.Fatalf("initial Commit: %v", err)
	}
	if result == nil || !result.Created || !result.Initialization || result.Base != nil || result.Commit == nil {
		t.Fatalf("initial result = %#v", result)
	}
	fields := strings.Fields(gitOutput(t, fixture.root, "rev-list", "--parents", "-n", "1", "HEAD"))
	if len(fields) != 1 || fields[0] != *result.Commit {
		t.Fatalf("root commit ancestry = %#v", fields)
	}
	if status := gitOutput(t, fixture.root, "status", "--porcelain=v1"); status != "" {
		t.Fatalf("initialized store is dirty: %q", status)
	}
	assertNoTransactionArtifacts(t, fixture.root)
}

type managedFixture struct {
	root   string
	gitDir string
	engine *Engine
	trust  *acceptingTrust
}

func newManagedFixture(t *testing.T, format gitraw.ObjectFormat, baseHook string) *managedFixture {
	t.Helper()
	fixture := newManagedFixtureWithoutGuard(t, format, baseHook)
	repository, err := gitraw.Discover(t.Context(), fixture.root)
	if err != nil {
		t.Fatalf("discover guard repository: %v", err)
	}
	if _, err := guard.Install(t.Context(), repository); err != nil {
		t.Fatalf("install guard: %v", err)
	}
	return fixture
}

func newManagedFixtureWithoutGuard(t *testing.T, format gitraw.ObjectFormat, baseHook string) *managedFixture {
	t.Helper()
	root := t.TempDir()
	arguments := []string{"init", "--initial-branch=main"}
	if format == gitraw.SHA256 {
		arguments = append(arguments, "--object-format=sha256")
	}
	if output, err := gitCombined(root, arguments...); err != nil {
		if format == gitraw.SHA256 {
			t.Skipf("Git SHA-256 repositories unavailable: %v: %s", err, output)
		}
		t.Fatalf("git init: %v\n%s", err, output)
	}
	configureGit(t, root)
	copyMinimal(t, root)
	if baseHook != "" {
		writeFile(t, filepath.Join(root, ".engram", "hooks", "prepare-changeset", "10-test.sh"), []byte(baseHook), 0o700)
	}
	gitRun(t, root, "add", "--all")
	gitRunEnv(t, root, []string{
		"GIT_AUTHOR_DATE=2026-08-08T12:00:00+02:00",
		"GIT_COMMITTER_DATE=2026-08-08T12:00:00+02:00",
	}, "commit", "--no-gpg-sign", "-m", "root")
	repository, err := gitraw.Discover(t.Context(), root)
	if err != nil {
		t.Fatalf("discover fixture: %v", err)
	}
	trust := &acceptingTrust{}
	executor := hookexec.New(trust)
	temporary := t.TempDir()
	executor.TempRoot = temporary
	return &managedFixture{
		root: root, gitDir: repository.GitDir, trust: trust,
		engine: &Engine{Hooks: executor, TempRoot: temporary, Clock: func() time.Time {
			return time.Date(2026, 8, 9, 10, 11, 12, 0, time.FixedZone("CEST", 2*60*60))
		}},
	}
}

func newUnbornFixture(t *testing.T, format gitraw.ObjectFormat) *managedFixture {
	t.Helper()
	root := t.TempDir()
	arguments := []string{"init", "--initial-branch=main"}
	if format == gitraw.SHA256 {
		arguments = append(arguments, "--object-format=sha256")
	}
	gitRun(t, root, arguments...)
	configureGit(t, root)
	copyMinimal(t, root)
	gitRun(t, root, "add", "--all")
	repository, err := gitraw.Discover(t.Context(), root)
	if err != nil {
		t.Fatalf("discover unborn fixture: %v", err)
	}
	if _, err := guard.Install(t.Context(), repository); err != nil {
		t.Fatalf("install guard: %v", err)
	}
	trust := &acceptingTrust{}
	executor := hookexec.New(trust)
	temporary := t.TempDir()
	executor.TempRoot = temporary
	return &managedFixture{root: root, gitDir: repository.GitDir, trust: trust, engine: &Engine{
		Hooks: executor, TempRoot: temporary,
		Clock: func() time.Time { return time.Date(2026, 8, 9, 10, 11, 12, 0, time.UTC) },
	}}
}

func configureGit(t *testing.T, root string) {
	t.Helper()
	gitRun(t, root, "config", "user.name", "Ada Lovelace")
	gitRun(t, root, "config", "user.email", "ada@example.test")
	gitRun(t, root, "config", "commit.gpgsign", "false")
}

func candidateFromWorktree(t *testing.T, source string, mutate func(string)) (*checker.Snapshot, map[string]gitraw.TreeMode) {
	t.Helper()
	root := t.TempDir()
	err := filepath.WalkDir(source, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, name)
		if err != nil || relative == "." {
			return err
		}
		if relative == ".git" {
			return filepath.SkipDir
		}
		target := filepath.Join(root, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if entry.Type()&os.ModeSymlink != 0 {
			targetValue, err := os.Readlink(name)
			if err != nil {
				return err
			}
			return os.Symlink(targetValue, target)
		}
		data, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode().Perm())
	})
	if err != nil {
		t.Fatalf("copy candidate: %v", err)
	}
	mutate(root)
	snapshot, err := checker.CheckFS(root)
	if err != nil {
		t.Fatalf("check candidate: %v", err)
	}
	if snapshot.Validation.Status != checker.StatusComplete || snapshot.Validation.HasErrors() {
		t.Fatalf("candidate validation = %#v", snapshot.Validation)
	}
	modes := make(map[string]gitraw.TreeMode, len(snapshot.Tree.Files))
	for name := range snapshot.Tree.Files {
		mode := gitraw.ModeRegular
		if info, err := os.Stat(filepath.Join(root, filepath.FromSlash(name))); err != nil {
			t.Fatalf("candidate mode %s: %v", name, err)
		} else if info.Mode().Perm()&0o111 != 0 {
			mode = gitraw.ModeExecutable
		}
		modes[name] = mode
	}
	return snapshot, modes
}

func countingHook(counter string, inspectEnvironment bool) string {
	checks := ""
	if inspectEnvironment {
		checks = `test -z "${GIT_DIR+x}"
test -z "${GIT_INDEX_FILE+x}"
test -z "${ENGRAM_SECRET+x}"
test -n "${ENGRAM_BASE-}"
test -n "${ENGRAM_CANDIDATE-}"
`
	}
	return "#!/usr/bin/env sh\nset -eu\n" + checks +
		"count=0\nif test -f " + shellQuote(counter) + "; then count=$(cat " + shellQuote(counter) + "); fi\n" +
		"count=$((count + 1))\nprintf '%s\\n' \"$count\" > " + shellQuote(counter) + "\n" +
		"printf '\\nPrepared exactly once.\\n' >> topics/why-files.md\n"
}

func failOnceAt(want Phase) func(Phase) error {
	var once sync.Once
	return func(got Phase) error {
		var err error
		if got == want {
			once.Do(func() { err = errors.New("injected fault") })
		}
		return err
	}
}

func mutateOnceAt(t *testing.T, want Phase, mutate func()) func(Phase) error {
	t.Helper()
	var once sync.Once
	return func(got Phase) error {
		if got == want {
			once.Do(mutate)
		}
		return nil
	}
}

func assertNoTransactionArtifacts(t *testing.T, root string) {
	t.Helper()
	repository, err := gitraw.Discover(t.Context(), root)
	if err != nil {
		t.Fatalf("discover artifacts: %v", err)
	}
	for _, name := range []string{
		journal.Path(repository.GitDir),
		rendezvous.RefPath(repository.CommonGitDir, repository.HeadRef),
		rendezvous.WorktreePath(repository.GitDir),
	} {
		if _, err := os.Lstat(name); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("transaction artifact remains at %s: %v", name, err)
		}
	}
}

func assertTransactionArtifacts(t *testing.T, root string) {
	t.Helper()
	repository, err := gitraw.Discover(t.Context(), root)
	if err != nil {
		t.Fatalf("discover artifacts: %v", err)
	}
	for _, name := range []string{
		journal.Path(repository.GitDir),
		rendezvous.RefPath(repository.CommonGitDir, repository.HeadRef),
		rendezvous.WorktreePath(repository.GitDir),
	} {
		if _, err := os.Lstat(name); err != nil {
			t.Fatalf("transaction artifact missing at %s: %v", name, err)
		}
	}
}

func copyMinimal(t *testing.T, destination string) {
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

func gitRun(t *testing.T, root string, arguments ...string) {
	t.Helper()
	gitRunEnv(t, root, nil, arguments...)
}

func gitRunEnv(t *testing.T, root string, extraEnvironment []string, arguments ...string) {
	t.Helper()
	output, err := gitCombinedEnv(root, extraEnvironment, arguments...)
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(arguments, " "), err, output)
	}
}

func gitOutput(t *testing.T, root string, arguments ...string) string {
	t.Helper()
	output, err := gitCombined(root, arguments...)
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(arguments, " "), err, output)
	}
	return strings.TrimSuffix(string(output), "\n")
}

func gitCombined(root string, arguments ...string) ([]byte, error) {
	return gitCombinedEnv(root, nil, arguments...)
}

func gitCombinedEnv(root string, extraEnvironment []string, arguments ...string) ([]byte, error) {
	command := exec.Command("git", append([]string{"-C", root}, arguments...)...)
	command.Env = append(cleanTestEnvironment(os.Environ()), extraEnvironment...)
	return command.CombinedOutput()
}

func cleanTestEnvironment(environment []string) []string {
	result := make([]string, 0, len(environment)+7)
	for _, item := range environment {
		name, _, _ := strings.Cut(item, "=")
		upper := strings.ToUpper(name)
		if strings.HasPrefix(upper, "GIT_") || strings.HasPrefix(upper, "ENGRAM_") {
			continue
		}
		result = append(result, item)
	}
	return append(result,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_CONFIG_COUNT=0",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_OPTIONAL_LOCKS=0",
		"LC_ALL=C",
	)
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

func appendFile(t *testing.T, name, value string) {
	t.Helper()
	file, err := os.OpenFile(name, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(value); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func assertBytes(t *testing.T, name string, want []byte) {
	t.Helper()
	got := readFile(t, name)
	if !bytes.Equal(got, want) {
		t.Fatalf("%s bytes differ:\ngot  %q\nwant %q", name, got, want)
	}
}

func assertContains(t *testing.T, name, want string) {
	t.Helper()
	if got := string(readFile(t, name)); !strings.Contains(got, want) {
		t.Fatalf("%s does not contain %q:\n%s", name, want, got)
	}
}

func relativeFiles(t *testing.T, root string) []string {
	t.Helper()
	var result []string
	err := filepath.WalkDir(root, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(root, name)
		if err != nil {
			return err
		}
		result = append(result, filepath.ToSlash(relative))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(result)
	return result
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
