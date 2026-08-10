package pullflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ontopix/engram/internal/gitraw"
	"github.com/ontopix/engram/internal/guard"
	"github.com/ontopix/engram/internal/hookexec"
	"github.com/ontopix/engram/internal/hooks"
	"github.com/ontopix/engram/internal/journal"
	"github.com/ontopix/engram/internal/managedread"
	"github.com/ontopix/engram/internal/managedwrite"
	"github.com/ontopix/engram/internal/rendezvous"
	"github.com/ontopix/engram/internal/testpath"
)

func TestPullFastForwardsOnlyAfterCompleteIncomingAudit(t *testing.T) {
	fixture := newPullFixture(t)
	appendTestFile(t, filepath.Join(fixture.remoteWork, "topics", "why-files.md"), "\nRemote accepted sentence.\n")
	gitTest(t, fixture.remoteWork, "add", "topics/why-files.md")
	gitTest(t, fixture.remoteWork, "commit", "--no-verify", "-m", "remote")
	gitTest(t, fixture.remoteWork, "push", "origin", "main")
	want := testTip(t, fixture.remoteWork)

	puller := New(noopWriter{})
	result, err := puller.Pull(t.Context(), openStore(t, fixture.local), "origin", "main")
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if result.State != FastForwarded || result.Fetched != 1 || result.Replayed != 0 || result.After.Commit == nil || *result.After.Commit != want || len(result.Conflicts) != 0 || result.Validation.HasErrors() {
		t.Fatalf("result = %#v", result)
	}
	if got := testTip(t, fixture.local); got != want {
		t.Fatalf("local tip = %q, want %q", got, want)
	}
	if got := gitTest(t, fixture.local, "status", "--porcelain=v1"); got != "" {
		t.Fatalf("fast-forward left dirty checkout: %q", got)
	}

	unchanged, err := puller.Pull(t.Context(), openStore(t, fixture.local), "", "")
	if err != nil || unchanged.State != UpToDate || unchanged.Fetched != 0 || unchanged.Changes != nil {
		t.Fatalf("up-to-date = %#v, %v", unchanged, err)
	}
	encoded, err := json.Marshal(unchanged)
	if err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{`"conflicts":null`, `"findings":null`, `"audits":null`} {
		if bytes.Contains(encoded, []byte(invalid)) {
			t.Fatalf("pull result contains null protocol array %s: %s", invalid, encoded)
		}
	}
}

func TestPullPrivateNetworkContextDefeatsRewriteAfterFinalCheck(t *testing.T) {
	fixture := newPullFixture(t)
	appendTestFile(t, filepath.Join(fixture.remoteWork, "topics", "why-files.md"), "\nRemote race-safe sentence.\n")
	gitTest(t, fixture.remoteWork, "add", "topics/why-files.md")
	gitTest(t, fixture.remoteWork, "commit", "--no-verify", "-m", "remote rewrite race")
	gitTest(t, fixture.remoteWork, "push", "origin", "main")
	want := testTip(t, fixture.remoteWork)
	redirected := filepath.Join(fixture.root, "redirected.git")
	redirectedURL := testpath.FileURL(redirected)
	puller := New(noopWriter{})
	puller.afterRewriteCheck = func() {
		gitTest(t, fixture.local, "config", "url."+redirectedURL+".insteadOf", fixture.remoteURL)
	}
	result, err := puller.Pull(t.Context(), openStore(t, fixture.local), "origin", "main")
	if err != nil || result == nil || result.State != FastForwarded {
		t.Fatalf("pull after rewrite race = %#v, %v", result, err)
	}
	if got := testTip(t, fixture.local); got != want {
		t.Fatalf("selected remote tip = %q, want %q", got, want)
	}
	if _, err := os.Lstat(redirected); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rewrite target was contacted or created: %v", err)
	}
}

func TestReplayPairInitialPublicationFaultsRemainExactAndRecoverable(t *testing.T) {
	tests := []struct {
		name         string
		phase        Phase
		planPresent  bool
		statePresent bool
	}{
		{name: "plan linked", phase: PhaseReplayPlanPublished, planPresent: true},
		{name: "state linked", phase: PhaseReplayStatePublished, planPresent: true, statePresent: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newPullFixture(t)
			repository, state, plan := replayPairTestState(t, fixture.local, false)
			puller := New(noopWriter{})
			puller.Fault = func(phase Phase) error {
				if phase == test.phase {
					return errors.New("interrupt replay pair publication")
				}
				return nil
			}
			record, _, err := puller.publishReplay(repository, state, plan)
			activeReplayPairs.Delete(record.Owner.Token)
			mutation := MutationOf(err)
			if err == nil || mutation == nil || !mutation.Durable || !mutation.RecoveryRequired || mutation.LocalRefs == nil || len(mutation.LocalRefs) != 0 || mutation.Head != nil || mutation.CheckoutChanged {
				t.Fatalf("publication error = %v; mutation = %#v", err, mutation)
			}
			assertControllerPresence(t, replayPairJournalPath(repository), true)
			assertControllerPresence(t, replayPlanPath(repository), test.planPresent)
			assertControllerPresence(t, replayStatePath(repository), test.statePresent)
			inspection, inspectErr := InspectRecovery(t.Context(), repository)
			if inspectErr != nil || inspection.Disposition != RecoveryRecoverable || !inspection.CleanupOnly || inspection.OwnerToken != record.Owner.Token {
				t.Fatalf("publication inspection = %#v, %v", inspection, inspectErr)
			}
			puller.Fault = nil
			recovered, recoverErr := puller.RecoverExpected(t.Context(), fixture.local, RecoveryExpectation{OwnerToken: inspection.OwnerToken, StateSHA256: inspection.StateSHA256})
			if recoverErr != nil || recovered == nil || !recovered.Needed || !recovered.Performed || !recovered.Durable || recovered.RecoveryRequired || recovered.Mutation == nil || recovered.Mutation.RecoveryRequired {
				t.Fatalf("publication recovery = %#v, %v", recovered, recoverErr)
			}
			for _, name := range []string{replayPairJournalPath(repository), replayPlanPath(repository), replayStatePath(repository)} {
				assertControllerPresence(t, name, false)
			}
		})
	}
}

func TestReplayPairUpdateFaultsAreRollForwardRecoverable(t *testing.T) {
	tests := []struct {
		name     string
		phase    Phase
		wantNext bool
		durable  bool
	}{
		{name: "authority linked", phase: PhaseReplayUpdatePublished, durable: false},
		{name: "plan replaced", phase: PhaseReplayUpdatePlan, wantNext: true, durable: true},
		{name: "state replaced", phase: PhaseReplayUpdateState, wantNext: true, durable: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newPullFixture(t)
			repository, oldState, oldPlan := replayPairTestState(t, fixture.local, true)
			if err := createControllerFile(replayPlanPath(repository), mustCanonical(t, oldPlan)); err != nil {
				t.Fatal(err)
			}
			if err := createControllerFile(replayStatePath(repository), mustCanonical(t, oldState)); err != nil {
				t.Fatal(err)
			}
			newState, newPlan := oldState, oldPlan
			newState.Reason = "conflict"
			newState.Conflicts = []string{"topics/why-files.md"}
			newPlan.DraftReady = true
			puller := New(noopWriter{})
			puller.Fault = func(phase Phase) error {
				if phase == test.phase {
					return errors.New("interrupt replay pair update")
				}
				return nil
			}
			err := puller.updateReplay(repository, oldState, oldPlan, newState, newPlan)
			mutation := MutationOf(err)
			if err == nil || mutation == nil || mutation.Durable != test.durable || !mutation.RecoveryRequired || mutation.LocalRefs == nil || len(mutation.LocalRefs) != 0 || mutation.Head != nil || mutation.CheckoutChanged {
				t.Fatalf("update error = %v; mutation = %#v", err, mutation)
			}
			inspection, inspectErr := InspectRecovery(t.Context(), repository)
			if inspectErr != nil || inspection.Disposition != RecoveryRecoverable || !inspection.CleanupOnly {
				t.Fatalf("update inspection = %#v, %v", inspection, inspectErr)
			}
			expectation := RecoveryExpectation{OwnerToken: inspection.OwnerToken, StateSHA256: inspection.StateSHA256}
			if test.phase == PhaseReplayUpdatePlan {
				puller.Fault = func(phase Phase) error {
					if phase == PhaseReplayUpdateState {
						return errors.New("interrupt replay pair roll-forward")
					}
					return nil
				}
				partial, partialErr := puller.RecoverExpected(t.Context(), fixture.local, expectation)
				if partialErr == nil || partial == nil || !partial.Needed || partial.Performed || !partial.Durable || !partial.RecoveryRequired || partial.Mutation == nil || !partial.Mutation.Durable || !partial.Mutation.RecoveryRequired {
					t.Fatalf("partial update recovery = %#v, %v", partial, partialErr)
				}
				assertControllerPresence(t, replayPairJournalPath(repository), true)
			}
			puller.Fault = nil
			recovered, recoverErr := puller.RecoverExpected(t.Context(), fixture.local, expectation)
			if recoverErr != nil || recovered == nil || !recovered.Needed || !recovered.Performed || recovered.RecoveryRequired {
				t.Fatalf("update recovery = %#v, %v", recovered, recoverErr)
			}
			state, plan, present, readErr := readReplayFiles(repository)
			if readErr != nil || !present {
				t.Fatalf("recovered pair present=%t: %v", present, readErr)
			}
			wantState, wantPlan := oldState, oldPlan
			if test.wantNext {
				wantState, wantPlan = newState, newPlan
			}
			if !bytes.Equal(mustCanonical(t, state), mustCanonical(t, wantState)) || !bytes.Equal(mustCanonical(t, plan), mustCanonical(t, wantPlan)) {
				t.Fatalf("recovered pair does not match expected stage: state=%#v plan=%#v", state, plan)
			}
			assertControllerPresence(t, replayPairJournalPath(repository), false)
		})
	}
}

func TestReplayControllerLockIsAdvisoryAndNonblocking(t *testing.T) {
	fixture := newPullFixture(t)
	repository, err := gitraw.Discover(t.Context(), fixture.local)
	if err != nil {
		t.Fatal(err)
	}
	err = withReplayLock(repository, func() error {
		result := make(chan error, 1)
		go func() {
			result <- withReplayLock(repository, func() error {
				return errors.New("second controller unexpectedly acquired the lock")
			})
		}()
		select {
		case lockErr := <-result:
			if !errors.Is(lockErr, errReplayControllerBusy) {
				return fmt.Errorf("nested controller lock = %w", lockErr)
			}
		case <-time.After(2 * time.Second):
			return errors.New("nested controller lock blocked instead of failing immediately")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := withReplayLock(repository, func() error { return nil }); err != nil {
		t.Fatalf("controller lock was not released after its holder returned: %v", err)
	}
}

func TestRendezvousReleaseEvidenceIncludesExactResidualOwnedLocks(t *testing.T) {
	root := t.TempDir()
	handle, err := rendezvous.AcquireWriter(root, root, "refs/heads/a", "refs/heads/z")
	if err != nil {
		t.Fatal(err)
	}
	// Release runs in reverse acquisition order. Removing z makes that release
	// fail after the worktree lock is gone while the exact a lock remains.
	if err := os.Remove(rendezvous.RefPath(root, "refs/heads/z")); err != nil {
		t.Fatal(err)
	}
	releaseErr := handle.Release()
	if releaseErr == nil || rendezvous.RecoveryRequiredOf(releaseErr) || !handle.RecoveryRequired() {
		t.Fatalf("release evidence = %v; encoded residual=%t handle residual=%t", releaseErr, rendezvous.RecoveryRequiredOf(releaseErr), handle.RecoveryRequired())
	}
	mutation := rendezvousMutation(handle, releaseErr)
	if mutation == nil || !mutation.Durable || !mutation.RecoveryRequired || mutation.LocalRefs == nil || len(mutation.LocalRefs) != 0 || mutation.Head != nil || mutation.CheckoutChanged {
		t.Fatalf("release mutation = %#v", mutation)
	}
}

func TestReplayActivationFaultLeavesCleanupOnlyPublicationAuthority(t *testing.T) {
	fixture := replayTerminalFixture(t, false)
	puller := fixture.puller(t)
	puller.Fault = func(phase Phase) error {
		if phase == PhaseReplayActivated {
			return errors.New("interrupt activated replay publication")
		}
		return nil
	}
	result, err := puller.Pull(t.Context(), openStore(t, fixture.local), "origin", "main")
	mutation := MutationOf(err)
	if result != nil || err == nil || mutation == nil || !mutation.Durable || !mutation.RecoveryRequired || len(mutation.LocalRefs) == 0 || mutation.Head == nil || !mutation.CheckoutChanged {
		t.Fatalf("activation fault = %#v, %v; mutation=%#v", result, err, mutation)
	}
	repository, discoverErr := gitraw.Discover(t.Context(), fixture.local)
	if discoverErr != nil {
		t.Fatal(discoverErr)
	}
	assertControllerPresence(t, replayPairJournalPath(repository), true)
	assertControllerPresence(t, replayPlanPath(repository), true)
	assertControllerPresence(t, replayStatePath(repository), true)
	inspection, inspectErr := InspectRecovery(t.Context(), repository)
	if inspectErr != nil || inspection.Disposition != RecoveryRecoverable || !inspection.CleanupOnly {
		t.Fatalf("activation inspection = %#v, %v", inspection, inspectErr)
	}
	puller.Fault = nil
	recovered, recoverErr := puller.RecoverExpected(t.Context(), fixture.local, RecoveryExpectation{OwnerToken: inspection.OwnerToken, StateSHA256: inspection.StateSHA256})
	if recoverErr != nil || recovered == nil || !recovered.Needed || !recovered.Performed || !recovered.Durable || recovered.RecoveryRequired || recovered.Mutation == nil || len(recovered.Mutation.LocalRefs) != 0 || recovered.Mutation.Head != nil || recovered.Mutation.CheckoutChanged {
		t.Fatalf("activation recovery = %#v, %v", recovered, recoverErr)
	}
	assertControllerPresence(t, replayPairJournalPath(repository), false)
	if active, activeErr := Active(openStore(t, fixture.local).Repository()); activeErr != nil || active == nil {
		t.Fatalf("active replay after publication cleanup = %#v, %v", active, activeErr)
	}
	continued, continueErr := puller.Continue(t.Context(), openStore(t, fixture.local))
	if continueErr != nil || continued == nil || continued.State != Replayed {
		t.Fatalf("continued activated replay = %#v, %v", continued, continueErr)
	}
}

func TestTransitionAndTerminalAuthorityFaultSeamsAreRecoveryBearing(t *testing.T) {
	t.Run("transition journal linked", func(t *testing.T) {
		fixture := newPullFixture(t)
		appendTestFile(t, filepath.Join(fixture.remoteWork, "topics", "derived-state.md"), "\nTransition authority fault.\n")
		gitTest(t, fixture.remoteWork, "add", "topics/derived-state.md")
		gitTest(t, fixture.remoteWork, "commit", "--no-verify", "-m", "transition authority")
		gitTest(t, fixture.remoteWork, "push", "origin", "main")
		puller := New(noopWriter{})
		puller.Fault = func(phase Phase) error {
			if phase == PhaseTransitionPublished {
				return errors.New("interrupt transition authority publication")
			}
			return nil
		}
		result, err := puller.Pull(t.Context(), openStore(t, fixture.local), "origin", "main")
		mutation := MutationOf(err)
		if result != nil || err == nil || mutation == nil || mutation.Durable || !mutation.RecoveryRequired || len(mutation.LocalRefs) != 0 || mutation.Head != nil || mutation.CheckoutChanged {
			t.Fatalf("transition publication = %#v, %v; mutation=%#v", result, err, mutation)
		}
		repository, discoverErr := gitraw.Discover(t.Context(), fixture.local)
		if discoverErr != nil {
			t.Fatal(discoverErr)
		}
		inspection, inspectErr := InspectRecovery(t.Context(), repository)
		if inspectErr != nil || inspection.Disposition != RecoveryRecoverable || inspection.CleanupOnly {
			t.Fatalf("transition inspection = %#v, %v", inspection, inspectErr)
		}
		puller.Fault = nil
		recovered, recoverErr := puller.RecoverExpected(t.Context(), fixture.local, RecoveryExpectation{OwnerToken: inspection.OwnerToken, StateSHA256: inspection.StateSHA256})
		if recoverErr != nil || recovered == nil || !recovered.Needed || !recovered.Performed || recovered.RecoveryRequired {
			t.Fatalf("transition recovery = %#v, %v", recovered, recoverErr)
		}
	})

	t.Run("terminal authority linked", func(t *testing.T) {
		fixture := replayTerminalFixture(t, false)
		puller := fixture.puller(t)
		puller.Fault = func(phase Phase) error {
			if phase == PhaseReplayTerminalPublished {
				return errors.New("interrupt terminal authority publication")
			}
			return nil
		}
		result, err := puller.Pull(t.Context(), openStore(t, fixture.local), "origin", "main")
		mutation := MutationOf(err)
		if result != nil || err == nil || mutation == nil || !mutation.Durable || !mutation.RecoveryRequired || len(mutation.LocalRefs) == 0 || mutation.Head == nil || !mutation.CheckoutChanged {
			t.Fatalf("terminal publication = %#v, %v; mutation=%#v", result, err, mutation)
		}
		repository, discoverErr := gitraw.Discover(t.Context(), fixture.local)
		if discoverErr != nil {
			t.Fatal(discoverErr)
		}
		assertControllerPresence(t, replayTerminalPath(repository), true)
		puller.Fault = nil
		continued, continueErr := puller.Continue(t.Context(), openStore(t, fixture.local))
		if continueErr != nil || continued == nil || continued.State != Replayed {
			t.Fatalf("continued terminal publication = %#v, %v", continued, continueErr)
		}
	})
}

func TestDurableTransitionPublicationDoesNotReportPlannedGitEffects(t *testing.T) {
	before := "1111111111111111111111111111111111111111"
	after := "2222222222222222222222222222222222222222"
	record := transitionRecord{
		Refs:       []transitionRef{{Ref: "refs/heads/main", Before: &before, After: &after}},
		HeadBefore: gitState("refs/heads/main", before),
		HeadAfter:  gitState("refs/heads/main", after),
	}
	err := recoveryError("publish local transition journal", record, false, false, &controllerPublicationError{
		path: transitionPath(&gitraw.Repository{GitDir: t.TempDir()}), durable: true, err: errors.New("post-link sync failure"),
	})
	mutation := MutationOf(err)
	if mutation == nil || !mutation.Durable || !mutation.RecoveryRequired || mutation.LocalRefs == nil || len(mutation.LocalRefs) != 0 || mutation.Head != nil || mutation.CheckoutChanged {
		t.Fatalf("durable controller-only mutation = %#v (%v)", mutation, err)
	}
}

func TestReplayTerminalLeaseReleaseFailureRetainsDoctorRecoveryEvidence(t *testing.T) {
	fixture := replayTerminalFixture(t, false)
	puller := fixture.puller(t)
	failed := false
	puller.releaseReplayLease = func(lease *rendezvous.RecoveryLease) error {
		if !failed {
			failed = true
			return errors.Join(lease.Release(), errors.New("simulated uncertain terminal lease release"))
		}
		return lease.Release()
	}
	result, err := puller.Pull(t.Context(), openStore(t, fixture.local), "origin", "main")
	mutation := MutationOf(err)
	if result != nil || err == nil || !failed || mutation == nil || !mutation.Durable || !mutation.RecoveryRequired || len(mutation.LocalRefs) == 0 || mutation.Head == nil || !mutation.CheckoutChanged {
		t.Fatalf("lease release = %#v, %v; mutation=%#v", result, err, mutation)
	}
	repository, discoverErr := gitraw.Discover(t.Context(), fixture.local)
	if discoverErr != nil {
		t.Fatal(discoverErr)
	}
	assertControllerPresence(t, replayStatePath(repository), false)
	assertControllerPresence(t, replayPlanPath(repository), false)
	assertControllerPresence(t, replayTerminalPath(repository), true)
	inspection, inspectErr := InspectRecovery(t.Context(), repository)
	if inspectErr != nil || inspection.Disposition != RecoveryRecoverable || !inspection.CleanupOnly {
		t.Fatalf("retained terminal inspection = %#v, %v", inspection, inspectErr)
	}
	puller.releaseReplayLease = nil
	recovered, recoverErr := puller.RecoverExpected(t.Context(), fixture.local, RecoveryExpectation{OwnerToken: inspection.OwnerToken, StateSHA256: inspection.StateSHA256})
	if recoverErr != nil || recovered == nil || !recovered.Needed || !recovered.Performed || recovered.RecoveryRequired {
		t.Fatalf("retained terminal recovery = %#v, %v", recovered, recoverErr)
	}
	assertControllerPresence(t, replayTerminalPath(repository), false)
}

func TestMalformedPreexistingReplayRecoveryIsNotReportedAsCurrentDurability(t *testing.T) {
	for _, test := range []struct {
		name string
		path func(*gitraw.Repository) string
	}{
		{name: "terminal", path: replayTerminalPath},
		{name: "pair", path: replayPairJournalPath},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newPullFixture(t)
			repository, err := gitraw.Discover(t.Context(), fixture.local)
			if err != nil {
				t.Fatal(err)
			}
			name := test.path(repository)
			if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(name, []byte("{}\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			result, recoverErr := New(noopWriter{}).Recover(t.Context(), fixture.local)
			mutation := MutationOf(recoverErr)
			if recoverErr == nil || result == nil || !result.Needed || result.Performed || result.Durable || !result.RecoveryRequired || result.Mutation == nil || result.Mutation.Durable || !result.Mutation.RecoveryRequired || mutation == nil || mutation.Durable || !mutation.RecoveryRequired {
				t.Fatalf("malformed %s recovery = %#v, %v; mutation=%#v", test.name, result, recoverErr, mutation)
			}
		})
	}
}

func TestPullUnrelatedHistoryConflictsWithoutMutatingLocalState(t *testing.T) {
	fixture := newPullFixture(t)
	unrelated := filepath.Join(fixture.root, "unrelated-work")
	if err := os.Mkdir(unrelated, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.CopyFS(unrelated, os.DirFS(filepath.Join(pullRepositoryRoot(t), "examples", "minimal"))); err != nil {
		t.Fatal(err)
	}
	gitTest(t, unrelated, "init", "--initial-branch=main")
	configureTestIdentity(t, unrelated)
	gitTest(t, unrelated, "add", "--all")
	gitTest(t, unrelated, "commit", "--no-verify", "-m", "unrelated root")
	gitTest(t, unrelated, "remote", "add", "destination", fixture.remoteURL)
	gitTest(t, unrelated, "push", "--force", "--no-verify", "destination", "main:main")

	before := capturePullObservableState(t, fixture.local)
	result, err := New(noopWriter{}).Pull(t.Context(), openStore(t, fixture.local), "origin", "main")
	if err != nil {
		t.Fatalf("unrelated pull: %v", err)
	}
	if result.State != Conflict || result.Fetched != 1 || result.Replayed != 0 || len(result.Conflicts) != 0 || result.Before.Commit == nil || result.After.Commit == nil || *result.Before.Commit != *result.After.Commit {
		t.Fatalf("unrelated result = %#v", result)
	}
	assertPullObservableState(t, fixture.local, before)
}

func TestPullRejectsUnrelatedLocalChangesBeforeNetworkOrMutation(t *testing.T) {
	for _, test := range []struct {
		name  string
		stage bool
	}{
		{name: "unstaged"},
		{name: "staged", stage: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newPullFixture(t)
			name := filepath.Join("topics", "why-files.md")
			appendTestFile(t, filepath.Join(fixture.local, name), "\nUnrelated local draft.\n")
			if test.stage {
				gitTest(t, fixture.local, "add", name)
			}
			before := capturePullObservableState(t, fixture.local)
			lookedUpGit := false
			puller := New(noopWriter{})
			puller.LookPath = func(string) (string, error) {
				lookedUpGit = true
				return "", errors.New("network capability must not be requested")
			}
			result, err := puller.Pull(t.Context(), openStore(t, fixture.local), "origin", "main")
			if result != nil || KindOf(err) != ErrorConflict || !errors.Is(err, ErrUnrelated) || MutationOf(err) != nil {
				t.Fatalf("dirty pull = %#v, %v", result, err)
			}
			if lookedUpGit {
				t.Fatal("dirty pull reached network capability lookup")
			}
			assertPullObservableState(t, fixture.local, before)
		})
	}
}

func TestPullRejectsAbsentIncomingHistoryAndObjectsWithoutLocalMutation(t *testing.T) {
	for _, test := range []struct {
		name       string
		advertised func(*testing.T, pullFixture) string
		wantKind   ErrorKind
		wantFetch  bool
	}{
		{
			name: "selected branch absent", wantKind: ErrorNetwork,
			advertised: func(*testing.T, pullFixture) string { return "" },
		},
		{
			name: "advertised tip absent after fetch", wantKind: ErrorCapability, wantFetch: true,
			advertised: func(*testing.T, pullFixture) string { return strings.Repeat("a", 40) },
		},
		{
			name: "advertised history has a missing parent", wantKind: ErrorCapability, wantFetch: true,
			advertised: func(t *testing.T, fixture pullFixture) string {
				return writeCommitWithMissingParent(t, fixture.local, strings.Repeat("b", 40))
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newPullFixture(t)
			tip := test.advertised(t, fixture)
			before := capturePullObservableState(t, fixture.local)
			fetches := 0
			puller := New(noopWriter{})
			puller.run = func(ctx context.Context, executable, root string, environment []string, input []byte, arguments ...string) commandResult {
				switch arguments[0] {
				case "ls-remote":
					stdout := []byte(nil)
					if tip != "" {
						stdout = []byte(tip + "\trefs/heads/main\n")
					}
					return commandResult{started: true, status: 0, stdout: stdout}
				case "fetch":
					fetches++
					return commandResult{started: true, status: 0}
				default:
					return runGitCommand(ctx, executable, root, environment, input, arguments...)
				}
			}
			result, err := puller.Pull(t.Context(), openStore(t, fixture.local), "origin", "main")
			if result != nil || KindOf(err) != test.wantKind || MutationOf(err) != nil {
				t.Fatalf("missing incoming data = %#v, %v", result, err)
			}
			if (fetches == 1) != test.wantFetch {
				t.Fatalf("fetch calls = %d, want fetch=%t", fetches, test.wantFetch)
			}
			assertPullObservableState(t, fixture.local, before)
		})
	}
}

func TestPullNetworkDenialAndFailurePrecedeLocalMutation(t *testing.T) {
	for _, test := range []struct {
		name     string
		wantKind ErrorKind
		run      func(string) commandResult
	}{
		{
			name: "network process denied", wantKind: ErrorCapability,
			run: func(command string) commandResult {
				if command == "ls-remote" {
					return commandResult{status: -1, err: fs.ErrPermission}
				}
				return commandResult{}
			},
		},
		{
			name: "fetch network failure", wantKind: ErrorNetwork,
			run: func(command string) commandResult {
				if command == "fetch" {
					return commandResult{started: true, status: 128, stderr: []byte("network unavailable")}
				}
				return commandResult{}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newPullFixture(t)
			before := capturePullObservableState(t, fixture.local)
			tip := testTip(t, fixture.remoteWork)
			puller := New(noopWriter{})
			puller.run = func(ctx context.Context, executable, root string, environment []string, input []byte, arguments ...string) commandResult {
				if result := test.run(arguments[0]); result.started || result.err != nil || result.status != 0 {
					return result
				}
				if arguments[0] == "ls-remote" {
					return commandResult{started: true, status: 0, stdout: []byte(tip + "\trefs/heads/main\n")}
				}
				return runGitCommand(ctx, executable, root, environment, input, arguments...)
			}
			result, err := puller.Pull(t.Context(), openStore(t, fixture.local), "origin", "main")
			if result != nil || KindOf(err) != test.wantKind || MutationOf(err) != nil {
				t.Fatalf("network failure = %#v, %v", result, err)
			}
			assertPullObservableState(t, fixture.local, before)
		})
	}
}

func TestPullDivergentConflictLeavesExactActiveStateAndAbortRestores(t *testing.T) {
	fixture := newPullFixture(t)
	name := filepath.Join("topics", "why-files.md")
	appendTestFile(t, filepath.Join(fixture.local, name), "\nLocal sentence.\n")
	gitTest(t, fixture.local, "add", name)
	gitTest(t, fixture.local, "commit", "--no-verify", "-m", "local")
	original := testTip(t, fixture.local)
	appendTestFile(t, filepath.Join(fixture.remoteWork, name), "\nRemote sentence.\n")
	gitTest(t, fixture.remoteWork, "add", name)
	gitTest(t, fixture.remoteWork, "commit", "--no-verify", "-m", "remote")
	gitTest(t, fixture.remoteWork, "push", "origin", "main")

	puller := fixture.puller(t)
	result, err := puller.Pull(t.Context(), openStore(t, fixture.local), "origin", "main")
	if err != nil {
		t.Fatalf("divergent pull: %v", err)
	}
	if result.State != Conflict || result.Replayed != 0 || len(result.Conflicts) != 1 || result.Conflicts[0] != name {
		t.Fatalf("conflict result = %#v", result)
	}
	privateStore := openStore(t, fixture.local)
	active, err := Active(privateStore.Repository())
	if err != nil || active == nil || active.Reason != "conflict" || len(active.Conflicts) != 1 || active.Original.Commit == nil || *active.Original.Commit != original || active.Private.Ref == nil || *active.Private.Ref == *active.Original.Ref {
		t.Fatalf("active state = %#v, %v", active, err)
	}
	stateBytes, err := os.ReadFile(replayStatePath(privateStore.Repository()))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"remote", "sources", "next", "validation"} {
		if bytes.Contains(stateBytes, []byte(`"`+forbidden+`"`)) {
			t.Fatalf("public replay state leaked private plan field %q: %s", forbidden, stateBytes)
		}
	}

	aborted, err := puller.Abort(t.Context(), privateStore)
	if err != nil {
		t.Fatalf("abort: %v", err)
	}
	if aborted.State != Aborted || testTip(t, fixture.local) != original || len(aborted.Conflicts) != 0 {
		t.Fatalf("abort result = %#v", aborted)
	}
	if got := gitTest(t, fixture.local, "symbolic-ref", "HEAD"); got != "refs/heads/main\n" {
		t.Fatalf("HEAD after abort = %q", got)
	}
	if _, err := Active(openStore(t, fixture.local).Repository()); err != nil {
		t.Fatalf("active after abort: %v", err)
	}
}

func TestPullReplaysNonConflictingLocalCommitsOldestFirst(t *testing.T) {
	fixture := newPullFixture(t)
	appendTestFile(t, filepath.Join(fixture.local, "topics", "why-files.md"), "\nLocal one.\n")
	gitTest(t, fixture.local, "add", "topics/why-files.md")
	gitTest(t, fixture.local, "commit", "--no-verify", "-m", "local one")
	appendTestFile(t, filepath.Join(fixture.local, "topics", "why-files.md"), "\nLocal two.\n")
	gitTest(t, fixture.local, "add", "topics/why-files.md")
	gitTest(t, fixture.local, "commit", "--no-verify", "-m", "local two")
	appendTestFile(t, filepath.Join(fixture.remoteWork, "topics", "derived-state.md"), "\nRemote sentence.\n")
	gitTest(t, fixture.remoteWork, "add", "topics/derived-state.md")
	gitTest(t, fixture.remoteWork, "commit", "--no-verify", "-m", "remote")
	gitTest(t, fixture.remoteWork, "push", "origin", "main")

	result, err := fixture.puller(t).Pull(t.Context(), openStore(t, fixture.local), "origin", "main")
	if err != nil {
		t.Fatalf("replay pull: %v", err)
	}
	if result.State != Replayed || result.Replayed != 2 || result.Fetched != 1 || len(result.Conflicts) != 0 {
		t.Fatalf("replay result = %#v", result)
	}
	why := string(readTestFile(t, filepath.Join(fixture.local, "topics", "why-files.md")))
	derived := string(readTestFile(t, filepath.Join(fixture.local, "topics", "derived-state.md")))
	if !strings.Contains(why, "Local one.") || !strings.Contains(why, "Local two.") || !strings.Contains(derived, "Remote sentence.") {
		t.Fatalf("final replay bytes missing: why=%q derived=%q", why, derived)
	}
	if got := gitTest(t, fixture.local, "status", "--porcelain=v1"); got != "" {
		t.Fatalf("replay left dirty checkout: %q", got)
	}
	if ActiveState, err := Active(openStore(t, fixture.local).Repository()); err != nil || ActiveState != nil {
		t.Fatalf("active replay after completion = %#v, %v", ActiveState, err)
	}
}

func TestPullContinueAcceptsExplicitStagedResolution(t *testing.T) {
	fixture := newPullFixture(t)
	name := filepath.Join("topics", "why-files.md")
	appendTestFile(t, filepath.Join(fixture.local, name), "\nLocal conflict.\n")
	gitTest(t, fixture.local, "add", name)
	gitTest(t, fixture.local, "commit", "--no-verify", "-m", "local")
	appendTestFile(t, filepath.Join(fixture.remoteWork, name), "\nRemote conflict.\n")
	gitTest(t, fixture.remoteWork, "add", name)
	gitTest(t, fixture.remoteWork, "commit", "--no-verify", "-m", "remote")
	gitTest(t, fixture.remoteWork, "push", "origin", "main")
	puller := fixture.puller(t)
	conflict, err := puller.Pull(t.Context(), openStore(t, fixture.local), "origin", "main")
	if err != nil || conflict.State != Conflict {
		t.Fatalf("conflict = %#v, %v", conflict, err)
	}
	appendTestFile(t, filepath.Join(fixture.local, name), "\nExplicit resolution.\n")
	gitTest(t, fixture.local, "add", name)
	resolved, err := puller.Continue(t.Context(), openStore(t, fixture.local))
	if err != nil {
		t.Fatalf("continue: %v", err)
	}
	if resolved.State != Replayed || resolved.Replayed != 1 || !strings.Contains(string(readTestFile(t, filepath.Join(fixture.local, name))), "Explicit resolution.") {
		t.Fatalf("resolved = %#v", resolved)
	}
}

func TestPullContinueFinalizesRecordedTerminalProgressWithoutReplaying(t *testing.T) {
	fixture := newPullFixture(t)
	appendTestFile(t, filepath.Join(fixture.local, "topics", "why-files.md"), "\nLocal terminal replay.\n")
	gitTest(t, fixture.local, "add", "topics/why-files.md")
	gitTest(t, fixture.local, "commit", "--no-verify", "-m", "local terminal")
	appendTestFile(t, filepath.Join(fixture.remoteWork, "topics", "derived-state.md"), "\nRemote terminal replay.\n")
	gitTest(t, fixture.remoteWork, "add", "topics/derived-state.md")
	gitTest(t, fixture.remoteWork, "commit", "--no-verify", "-m", "remote terminal")
	gitTest(t, fixture.remoteWork, "push", "origin", "main")

	puller := fixture.puller(t)
	failed := false
	puller.Fault = func(phase Phase) error {
		if phase == PhaseReplayCommitted && !failed {
			failed = true
			return errors.New("interrupt after replay progress")
		}
		return nil
	}
	if result, err := puller.Pull(t.Context(), openStore(t, fixture.local), "origin", "main"); result != nil || err == nil {
		t.Fatalf("interrupted terminal replay = %#v, %v", result, err)
	}
	if active, err := Active(openStore(t, fixture.local).Repository()); err != nil || active == nil {
		t.Fatalf("terminal replay state = %#v, %v", active, err)
	}
	puller.Fault = nil
	result, err := puller.Continue(t.Context(), openStore(t, fixture.local))
	if err != nil || result.State != Replayed || result.Replayed != 1 {
		t.Fatalf("continued terminal replay = %#v, %v", result, err)
	}
	if count := strings.Count(gitTest(t, fixture.local, "log", "--format=%s"), "Replay "); count != 1 {
		t.Fatalf("replay commit count = %d", count)
	}
}

func TestReplayTerminalCleanupSurvivesEveryDurableBoundary(t *testing.T) {
	tests := []struct {
		name             string
		phase            Phase
		statePresent     bool
		planPresent      bool
		terminalPresent  bool
		recoveryRequired bool
		transitionDone   bool
	}{
		{"terminal recorded", PhaseFinalizing, true, true, true, true, false},
		{"local transition complete", PhaseReplayTransitioned, true, true, true, true, true},
		{"public state removed", PhaseReplayStateRemoved, false, true, true, true, true},
		{"private plan removed", PhaseReplayPlanRemoved, false, false, true, true, true},
		{"terminal authority removed", PhaseReplayTerminalRemoved, false, false, false, false, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := replayTerminalFixture(t, false)
			puller := fixture.puller(t)
			failed := false
			puller.Fault = func(phase Phase) error {
				if phase == test.phase && !failed {
					failed = true
					return errors.New("interrupt replay terminal boundary")
				}
				return nil
			}
			result, err := puller.Pull(t.Context(), openStore(t, fixture.local), "origin", "main")
			mutation := MutationOf(err)
			if result != nil || err == nil || !failed || mutation == nil || !mutation.Durable || mutation.RecoveryRequired != test.recoveryRequired || len(mutation.LocalRefs) == 0 || mutation.Head == nil || !mutation.CheckoutChanged {
				t.Fatalf("terminal error = %#v, %v; mutation = %#v", result, err, mutation)
			}
			repository, discoverErr := gitraw.Discover(t.Context(), fixture.local)
			if discoverErr != nil {
				t.Fatal(discoverErr)
			}
			assertControllerPresence(t, replayStatePath(repository), test.statePresent)
			assertControllerPresence(t, replayPlanPath(repository), test.planPresent)
			assertControllerPresence(t, replayTerminalPath(repository), test.terminalPresent)
			if !test.terminalPresent {
				if _, continueErr := puller.Continue(t.Context(), openStore(t, fixture.local)); !errors.Is(continueErr, ErrNoActiveReplay) {
					t.Fatalf("continue after complete cleanup = %v", continueErr)
				}
				return
			}
			retriedTransition := false
			if test.transitionDone {
				puller.Fault = func(phase Phase) error {
					if phase == PhaseFastForwarding {
						retriedTransition = true
						return errors.New("terminal transition must not be repeated")
					}
					return nil
				}
			} else {
				puller.Fault = nil
			}
			continued, continueErr := puller.Continue(t.Context(), openStore(t, fixture.local))
			if continueErr != nil || continued == nil || continued.State != Replayed || retriedTransition {
				t.Fatalf("continued terminal replay = %#v, %v; retried=%t", continued, continueErr, retriedTransition)
			}
			assertControllerPresence(t, replayStatePath(repository), false)
			assertControllerPresence(t, replayPlanPath(repository), false)
			assertControllerPresence(t, replayTerminalPath(repository), false)
		})
	}
}

func TestReplayAbortTerminalIsIdempotentBeforeAndAfterTransition(t *testing.T) {
	for _, test := range []struct {
		name           string
		phase          Phase
		transitionDone bool
	}{
		{"terminal recorded", PhaseAborting, false},
		{"local transition complete", PhaseReplayTransitioned, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := replayTerminalFixture(t, true)
			puller := fixture.puller(t)
			conflict, err := puller.Pull(t.Context(), openStore(t, fixture.local), "origin", "main")
			if err != nil || conflict == nil || conflict.State != Conflict {
				t.Fatalf("prepare conflict = %#v, %v", conflict, err)
			}
			original := conflict.Before
			failed := false
			puller.Fault = func(phase Phase) error {
				if phase == test.phase && !failed {
					failed = true
					return errors.New("interrupt replay abort boundary")
				}
				return nil
			}
			aborted, abortErr := puller.Abort(t.Context(), openStore(t, fixture.local))
			mutation := MutationOf(abortErr)
			if aborted != nil || abortErr == nil || mutation == nil || !mutation.Durable || !mutation.RecoveryRequired {
				t.Fatalf("abort error = %#v, %v; mutation = %#v", aborted, abortErr, mutation)
			}
			if test.transitionDone {
				if len(mutation.LocalRefs) == 0 || mutation.Head == nil || !mutation.CheckoutChanged {
					t.Fatalf("completed abort transition mutation = %#v", mutation)
				}
			} else if mutation.LocalRefs == nil || len(mutation.LocalRefs) != 0 || mutation.Head != nil || mutation.CheckoutChanged {
				t.Fatalf("pre-transition abort mutation contains planned effects = %#v", mutation)
			}
			retriedTransition := false
			if test.transitionDone {
				puller.Fault = func(phase Phase) error {
					if phase == PhaseFastForwarding {
						retriedTransition = true
						return errors.New("abort transition must not be repeated")
					}
					return nil
				}
			} else {
				puller.Fault = nil
			}
			aborted, abortErr = puller.Abort(t.Context(), openStore(t, fixture.local))
			if abortErr != nil || aborted == nil || aborted.State != Aborted || retriedTransition || !sameGitState(aborted.After, original) {
				t.Fatalf("resumed abort = %#v, %v; retried=%t", aborted, abortErr, retriedTransition)
			}
		})
	}
}

func TestRecoverExpectedCleansExactTerminalReplayWithoutNetwork(t *testing.T) {
	fixture := replayTerminalFixture(t, false)
	puller := fixture.puller(t)
	puller.Fault = func(phase Phase) error {
		if phase == PhaseReplayTransitioned {
			return errors.New("interrupt before terminal cleanup")
		}
		return nil
	}
	if result, err := puller.Pull(t.Context(), openStore(t, fixture.local), "origin", "main"); result != nil || err == nil {
		t.Fatalf("interrupted replay = %#v, %v", result, err)
	}
	repository, err := gitraw.Discover(t.Context(), fixture.local)
	if err != nil {
		t.Fatal(err)
	}
	inspection, err := InspectRecovery(t.Context(), repository)
	if err != nil || inspection.Disposition != RecoveryRecoverable || !inspection.CleanupOnly || len(inspection.StateSHA256) != 64 {
		t.Fatalf("terminal inspection = %#v, %v", inspection, err)
	}
	puller.Fault = nil
	changed, changedErr := puller.RecoverExpected(t.Context(), fixture.local, RecoveryExpectation{OwnerToken: inspection.OwnerToken, StateSHA256: strings.Repeat("0", 64)})
	if changed == nil || !changed.Needed || !changed.RecoveryRequired || changedErr == nil || KindOf(changedErr) != ErrorConcurrency || MutationOf(changedErr) == nil {
		t.Fatalf("changed terminal approval = %#v, %v", changed, changedErr)
	}
	assertControllerPresence(t, replayTerminalPath(repository), true)
	recovered, err := puller.RecoverExpected(t.Context(), fixture.local, RecoveryExpectation{OwnerToken: inspection.OwnerToken, StateSHA256: inspection.StateSHA256})
	if err != nil || recovered == nil || !recovered.Needed || !recovered.Performed || !recovered.Durable || recovered.RecoveryRequired || recovered.CheckoutChanged || recovered.Mutation == nil || len(recovered.Mutation.LocalRefs) != 0 || recovered.Mutation.Head != nil || recovered.Mutation.CheckoutChanged {
		t.Fatalf("terminal recovery = %#v, %v", recovered, err)
	}
	assertControllerPresence(t, replayStatePath(repository), false)
	assertControllerPresence(t, replayPlanPath(repository), false)
	assertControllerPresence(t, replayTerminalPath(repository), false)
}

func TestClassifyWriterErrorReportsOnlyCurrentManagedEffects(t *testing.T) {
	private := "2222222222222222222222222222222222222222"
	accepted := "3333333333333333333333333333333333333333"
	privateRef := "refs/heads/engram-pull/0123456789abcdef0123456789abcdef"
	repository := &gitraw.Repository{HeadRef: privateRef}
	privateOID, err := gitraw.ParseOID(gitraw.SHA1, private)
	if err != nil {
		t.Fatal(err)
	}
	repository.Head = &privateOID
	classified := classifyWriterError(repository, nil, &managedwrite.Error{
		Kind: managedwrite.FailureRecovery, Phase: managedwrite.PhaseIndexReconciled,
		Durable: true, Accepted: true, CheckoutChanged: true, RecoveryRequired: true,
		Commit: accepted, Err: managedwrite.ErrPostCAS,
	})
	mutation := MutationOf(classified)
	if mutation == nil || !mutation.Durable || !mutation.CheckoutChanged || !mutation.RecoveryRequired || len(mutation.LocalRefs) != 1 || mutation.Head == nil || mutation.Head.After.Commit == nil || *mutation.Head.After.Commit != accepted {
		t.Fatalf("classified mutation = %#v (%v)", mutation, classified)
	}
	unknown := classifyWriterError(repository, nil, &managedwrite.Error{
		Kind: managedwrite.FailureRecovery, Phase: managedwrite.PhaseRefUpdated,
		Durable: true, UnknownCAS: true, RecoveryRequired: true, Commit: accepted, Err: managedwrite.ErrCASUnknown,
	})
	unknownMutation := MutationOf(unknown)
	if unknownMutation == nil || !unknownMutation.Durable || !unknownMutation.RecoveryRequired || len(unknownMutation.LocalRefs) != 0 || unknownMutation.Head != nil || unknownMutation.CheckoutChanged {
		t.Fatalf("unknown-CAS mutation = %#v (%v)", unknownMutation, unknown)
	}
}

func TestPullContinueRepairsAcceptedCommitBeforeControllerProgress(t *testing.T) {
	fixture := newPullFixture(t)
	appendTestFile(t, filepath.Join(fixture.local, "topics", "why-files.md"), "\nLocal recoverable replay.\n")
	gitTest(t, fixture.local, "add", "topics/why-files.md")
	gitTest(t, fixture.local, "commit", "--no-verify", "-m", "local recoverable")
	appendTestFile(t, filepath.Join(fixture.remoteWork, "topics", "derived-state.md"), "\nRemote recoverable replay.\n")
	gitTest(t, fixture.remoteWork, "add", "topics/derived-state.md")
	gitTest(t, fixture.remoteWork, "commit", "--no-verify", "-m", "remote recoverable")
	gitTest(t, fixture.remoteWork, "push", "origin", "main")

	base := fixture.puller(t)
	writer := &commitThenErrorWriter{inner: base.Writer, failImage: true}
	puller := New(writer)
	if result, err := puller.Pull(t.Context(), openStore(t, fixture.local), "origin", "main"); result != nil || err == nil {
		t.Fatalf("lost controller progress = %#v, %v", result, err)
	}
	if writer.imageCalls != 1 {
		t.Fatalf("image calls = %d", writer.imageCalls)
	}
	writer.failImage = false
	result, err := puller.Continue(t.Context(), openStore(t, fixture.local))
	if err != nil || result.State != Replayed || result.Replayed != 1 {
		t.Fatalf("repaired replay = %#v, %v", result, err)
	}
	if writer.imageCalls != 1 {
		t.Fatalf("accepted source was replayed again; calls = %d", writer.imageCalls)
	}
}

func TestPullFastForwardInterruptionRetainsAndRecoversExactTransition(t *testing.T) {
	fixture := newPullFixture(t)
	appendTestFile(t, filepath.Join(fixture.remoteWork, "topics", "derived-state.md"), "\nRecovery target.\n")
	gitTest(t, fixture.remoteWork, "add", "topics/derived-state.md")
	gitTest(t, fixture.remoteWork, "commit", "--no-verify", "-m", "remote recovery")
	gitTest(t, fixture.remoteWork, "push", "origin", "main")
	want := testTip(t, fixture.remoteWork)

	puller := New(noopWriter{})
	puller.Fault = func(phase Phase) error {
		if phase == PhaseRefUpdated {
			return errors.New("interrupt after CAS")
		}
		return nil
	}
	result, err := puller.Pull(t.Context(), openStore(t, fixture.local), "origin", "main")
	if result != nil || err == nil || MutationOf(err) == nil || !MutationOf(err).RecoveryRequired || !MutationOf(err).Durable {
		t.Fatalf("interrupted result/error = %#v, %v", result, err)
	}
	repository, discoverErr := gitraw.Discover(t.Context(), fixture.local)
	if discoverErr != nil {
		t.Fatal(discoverErr)
	}
	journalBytes, present, readErr := readControllerFile(transitionPath(repository))
	if readErr != nil || !present {
		t.Fatalf("recovery journal present=%t err=%v", present, readErr)
	}
	var record transitionRecord
	if err := decodeCanonical(journalBytes, &record); err != nil {
		t.Fatal(err)
	}
	activeTransitionTokens.Store(record.OwnerToken, struct{}{})
	active, inspectErr := InspectRecovery(t.Context(), repository)
	activeTransitionTokens.Delete(record.OwnerToken)
	if inspectErr != nil || active.Disposition != RecoveryActive {
		t.Fatalf("active recovery inspection = %#v, %v", active, inspectErr)
	}
	stale, inspectErr := InspectRecovery(t.Context(), repository)
	if inspectErr != nil || stale.Disposition != RecoveryRecoverable || stale.OwnerToken != record.OwnerToken || len(stale.StateSHA256) != 64 || len(stale.RefNames) != 1 || stale.RefNames[0] != repository.HeadRef {
		t.Fatalf("stale recovery inspection = %#v, %v", stale, inspectErr)
	}
	wrong := RecoveryExpectation{OwnerToken: stale.OwnerToken, StateSHA256: strings.Repeat("0", 64)}
	changed, changedErr := puller.RecoverExpected(t.Context(), fixture.local, wrong)
	if changed == nil || !changed.Needed || !changed.RecoveryRequired || changedErr == nil || KindOf(changedErr) != ErrorConcurrency {
		t.Fatalf("changed approval = %#v, %v", changed, changedErr)
	}
	puller.Fault = func(phase Phase) error {
		if phase == PhaseIndexUpdated {
			return errors.New("interrupt recovery after index")
		}
		return nil
	}
	expected := RecoveryExpectation{OwnerToken: stale.OwnerToken, StateSHA256: stale.StateSHA256}
	partial, partialErr := puller.RecoverExpected(t.Context(), fixture.local, expected)
	if partial == nil || !partial.Needed || partial.Performed || !partial.Durable || !partial.CheckoutChanged || !partial.RecoveryRequired || partialErr == nil || KindOf(partialErr) != ErrorIO {
		t.Fatalf("partial recovery = %#v, %v", partial, partialErr)
	}
	puller.Fault = nil
	recovered, recoverErr := puller.RecoverExpected(t.Context(), fixture.local, expected)
	if recoverErr != nil {
		t.Fatalf("recover: %v", recoverErr)
	}
	if !recovered.Needed || !recovered.Performed || testTip(t, fixture.local) != want || gitTest(t, fixture.local, "status", "--porcelain=v1") != "" {
		t.Fatalf("recovered = %#v tip=%q", recovered, testTip(t, fixture.local))
	}
	if _, present, readErr := readControllerFile(transitionPath(openStore(t, fixture.local).Repository())); readErr != nil || present {
		t.Fatalf("journal after recovery present=%t err=%v", present, readErr)
	}
}

func TestRecoverExpectedKeepsApprovedJournalByteExact(t *testing.T) {
	t.Run("recheck under lease before adoption", func(t *testing.T) {
		fixture, puller, repository, expected, approved, record := interruptedPullRecovery(t)
		lockPaths := []string{
			rendezvous.RefPath(repository.CommonGitDir, repository.HeadRef),
			rendezvous.WorktreePath(repository.GitDir),
		}
		lockBytes := [][]byte{readTestFile(t, lockPaths[0]), readTestFile(t, lockPaths[1])}
		indexBefore := readTestFile(t, filepath.Join(repository.GitDir, "index"))
		headBefore := gitTest(t, fixture.local, "symbolic-ref", "HEAD")
		var changed []byte
		puller.Fault = func(phase Phase) error {
			if phase != PhaseRecoveryLeased {
				return nil
			}
			altered := record
			altered.Phase = transitionHeadUpdated
			var err error
			changed, err = encodeCanonical(altered)
			if err != nil {
				return err
			}
			return replaceControllerFile(transitionPath(repository), changed)
		}
		result, err := puller.RecoverExpected(t.Context(), fixture.local, expected)
		if result == nil || !result.Needed || result.Durable || !result.RecoveryRequired || err == nil || KindOf(err) != ErrorConcurrency {
			t.Fatalf("raced recovery = %#v, %v", result, err)
		}
		if bytes.Equal(approved, changed) || !bytes.Equal(readTestFile(t, transitionPath(repository)), changed) {
			t.Fatal("recovery overwrote the raced journal")
		}
		for index, name := range lockPaths {
			if !bytes.Equal(readTestFile(t, name), lockBytes[index]) {
				t.Fatalf("lock %s changed before exact journal recheck", name)
			}
		}
		if !bytes.Equal(readTestFile(t, filepath.Join(repository.GitDir, "index")), indexBefore) || gitTest(t, fixture.local, "symbolic-ref", "HEAD") != headBefore {
			t.Fatal("checkout changed before exact journal recheck")
		}
	})

	t.Run("terminal progression uses approved bytes", func(t *testing.T) {
		fixture, puller, repository, expected, approved, record := interruptedPullRecovery(t)
		var changed []byte
		puller.Fault = func(phase Phase) error {
			if phase != PhaseWorktreeUpdated {
				return nil
			}
			altered := record
			altered.Phase = transitionHeadUpdated
			var err error
			changed, err = encodeCanonical(altered)
			if err != nil {
				return err
			}
			return replaceControllerFile(transitionPath(repository), changed)
		}
		result, err := puller.RecoverExpected(t.Context(), fixture.local, expected)
		if result == nil || !result.Needed || result.Performed || !result.Durable || !result.CheckoutChanged || !result.RecoveryRequired || err == nil || KindOf(err) != ErrorConcurrency {
			t.Fatalf("terminal CAS race = %#v, %v", result, err)
		}
		if result.Mutation == nil || len(result.Mutation.LocalRefs) != 0 || result.Mutation.Head != nil || !result.Mutation.CheckoutChanged {
			t.Fatalf("known mutation evidence = %#v", result.Mutation)
		}
		if bytes.Equal(approved, changed) || !bytes.Equal(readTestFile(t, transitionPath(repository)), changed) {
			t.Fatal("terminal CAS replaced non-approved journal bytes")
		}
	})
}

func interruptedPullRecovery(t *testing.T) (pullFixture, *Puller, *gitraw.Repository, RecoveryExpectation, []byte, transitionRecord) {
	t.Helper()
	fixture := newPullFixture(t)
	appendTestFile(t, filepath.Join(fixture.remoteWork, "topics", "derived-state.md"), "\nExact recovery target.\n")
	gitTest(t, fixture.remoteWork, "add", "topics/derived-state.md")
	gitTest(t, fixture.remoteWork, "commit", "--no-verify", "-m", "exact recovery")
	gitTest(t, fixture.remoteWork, "push", "origin", "main")
	puller := New(noopWriter{})
	puller.Fault = func(phase Phase) error {
		if phase == PhaseRefUpdated {
			return errors.New("interrupt after CAS")
		}
		return nil
	}
	if result, err := puller.Pull(t.Context(), openStore(t, fixture.local), "origin", "main"); result != nil || err == nil {
		t.Fatalf("interrupted pull = %#v, %v", result, err)
	}
	repository, err := gitraw.Discover(t.Context(), fixture.local)
	if err != nil {
		t.Fatal(err)
	}
	raw, present, err := readControllerFile(transitionPath(repository))
	if err != nil || !present {
		t.Fatalf("approved journal present=%v, error=%v", present, err)
	}
	var record transitionRecord
	if err := decodeCanonical(raw, &record); err != nil {
		t.Fatal(err)
	}
	inspection, err := InspectRecovery(t.Context(), repository)
	if err != nil || inspection.Disposition != RecoveryRecoverable {
		t.Fatalf("inspection = %#v, %v", inspection, err)
	}
	return fixture, puller, repository, RecoveryExpectation{
		OwnerToken: inspection.OwnerToken, StateSHA256: inspection.StateSHA256,
	}, raw, record
}

func TestPullRecoveryCancelsPreparedPreJournalTransition(t *testing.T) {
	fixture := newPullFixture(t)
	repository, err := gitraw.Discover(t.Context(), fixture.local)
	if err != nil {
		t.Fatal(err)
	}
	lock, err := rendezvous.AcquireWriter(repository.CommonGitDir, repository.GitDir, repository.HeadRef)
	if err != nil {
		t.Fatal(err)
	}
	index, present, err := readOptionalFile(filepath.Join(repository.GitDir, "index"))
	if err != nil || !present {
		t.Fatalf("read index: present=%t err=%v", present, err)
	}
	tip := repository.Head.String()
	state := gitState(repository.HeadRef, tip)
	record := transitionRecord{
		Version: 1, Phase: transitionPrepared, OwnerToken: lock.Owner().Token, ObjectFormat: repository.Format,
		Refs:        []transitionRef{{Ref: repository.HeadRef, Before: stringPointer(tip), After: stringPointer(tip)}},
		HeadBefore:  state,
		HeadAfter:   cloneGitState(state),
		IndexBefore: journal.RawFileImage{Present: true, Data: append([]byte(nil), index...)},
		IndexAfter:  journal.RawFileImage{Present: true, Data: append([]byte(nil), index...)},
		Paths:       []pathTransition{},
	}
	encoded, err := encodeCanonical(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := createControllerFile(transitionPath(repository), encoded); err != nil {
		t.Fatal(err)
	}
	inspection, err := InspectRecovery(t.Context(), repository)
	if err != nil || inspection.Disposition != RecoveryRecoverable {
		t.Fatalf("prepared inspection = %#v, %v", inspection, err)
	}
	result, err := New(noopWriter{}).Recover(t.Context(), fixture.local)
	if err != nil || result == nil || !result.Needed || !result.Performed {
		t.Fatalf("prepared recovery = %#v, %v", result, err)
	}
	for _, name := range []string{transitionPath(repository), rendezvous.RefPath(repository.CommonGitDir, repository.HeadRef), rendezvous.WorktreePath(repository.GitDir)} {
		if _, err := os.Lstat(name); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("prepared recovery retained %s: %v", name, err)
		}
	}
}

func TestPullCandidateRejectionLeavesUnpreparedStagedDraft(t *testing.T) {
	fixture := newPullFixture(t)
	why := filepath.Join(fixture.local, "topics", "why-files.md")
	if err := os.Remove(why); err != nil {
		t.Fatal(err)
	}
	catalog := filepath.Join(fixture.local, "topics", "README.md")
	catalogBytes := readTestFile(t, catalog)
	line := []byte("- [why-files](why-files.md) — Why this store is plain markdown files instead of a database.\n")
	catalogBytes = bytes.Replace(catalogBytes, line, nil, 1)
	if bytes.Contains(catalogBytes, line) {
		t.Fatal("catalog fixture contains duplicate target line")
	}
	if err := os.WriteFile(catalog, catalogBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, fixture.local, "add", "--all")
	gitTest(t, fixture.local, "commit", "--no-verify", "-m", "remove why")
	appendTestFile(t, filepath.Join(fixture.remoteWork, "topics", "derived-state.md"), "\nSee [[topics/why-files]].\n")
	gitTest(t, fixture.remoteWork, "add", "topics/derived-state.md")
	gitTest(t, fixture.remoteWork, "commit", "--no-verify", "-m", "link why")
	gitTest(t, fixture.remoteWork, "push", "origin", "main")

	puller := fixture.puller(t)
	result, err := puller.Pull(t.Context(), openStore(t, fixture.local), "origin", "main")
	if err != nil {
		t.Fatalf("rejected pull: %v", err)
	}
	if result.State != Rejected || result.CandidateValidation == nil || !result.CandidateValidation.HasErrors() || len(result.Conflicts) != 0 || result.Changes == nil {
		t.Fatalf("rejected result = %#v", result)
	}
	store := openStore(t, fixture.local)
	status, err := store.Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Staged) == 0 || len(status.Unstaged) != 0 {
		t.Fatalf("rejected staged/unstaged = %#v / %#v", status.Staged, status.Unstaged)
	}
	active, err := Active(store.Repository())
	if err != nil || active == nil || active.Reason != "rejected" || len(active.Conflicts) != 0 {
		t.Fatalf("rejected active state = %#v, %v", active, err)
	}
	activeJSON, err := json.Marshal(active)
	if err != nil || bytes.Contains(activeJSON, []byte(`"conflicts":null`)) {
		t.Fatalf("active replay conflicts must be an array: %s, %v", activeJSON, err)
	}
	if _, err := puller.Abort(t.Context(), store); err != nil {
		t.Fatalf("abort rejected draft: %v", err)
	}
}

type noopWriter struct{}

func (noopWriter) Commit(context.Context, managedwrite.Request) (*managedwrite.Result, error) {
	return nil, errors.New("unexpected managed commit")
}
func (noopWriter) CommitImage(context.Context, managedwrite.ImageRequest) (*managedwrite.Result, error) {
	return nil, errors.New("unexpected managed image commit")
}

type commitThenErrorWriter struct {
	inner      ManagedWriter
	failImage  bool
	imageCalls int
}

func (w *commitThenErrorWriter) Commit(ctx context.Context, request managedwrite.Request) (*managedwrite.Result, error) {
	return w.inner.Commit(ctx, request)
}

func (w *commitThenErrorWriter) CommitImage(ctx context.Context, request managedwrite.ImageRequest) (*managedwrite.Result, error) {
	w.imageCalls++
	result, err := w.inner.CommitImage(ctx, request)
	if err == nil && w.failImage {
		return result, errors.New("simulated loss before controller progress")
	}
	return result, err
}

type pullFixture struct {
	root       string
	local      string
	remote     string
	remoteWork string
	remoteURL  string
}

func newPullFixture(t *testing.T) pullFixture {
	t.Helper()
	root := t.TempDir()
	local := filepath.Join(root, "local")
	if err := os.Mkdir(local, 0o755); err != nil {
		t.Fatal(err)
	}
	example := filepath.Join(pullRepositoryRoot(t), "examples", "minimal")
	if err := os.CopyFS(local, os.DirFS(example)); err != nil {
		t.Fatal(err)
	}
	gitTest(t, local, "init", "--initial-branch=main")
	configureTestIdentity(t, local)
	gitTest(t, local, "add", "--all")
	gitTest(t, local, "commit", "--no-verify", "-m", "initial")
	remote := filepath.Join(root, "remote.git")
	gitTest(t, root, "init", "--bare", "--initial-branch=main", remote)
	remoteURL := testpath.FileURL(remote)
	gitTest(t, local, "remote", "add", "origin", remoteURL)
	gitTest(t, local, "config", "branch.main.remote", "origin")
	gitTest(t, local, "config", "branch.main.merge", "refs/heads/main")
	gitTest(t, local, "push", "--no-verify", "origin", "main")
	remoteWork := filepath.Join(root, "remote-work")
	gitTest(t, root, "clone", remoteURL, remoteWork)
	configureTestIdentity(t, remoteWork)
	return pullFixture{root: root, local: local, remote: remote, remoteWork: remoteWork, remoteURL: remoteURL}
}

func (f pullFixture) puller(t *testing.T) *Puller {
	t.Helper()
	repository, err := gitraw.Discover(t.Context(), f.local)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := guard.Install(t.Context(), repository); err != nil {
		t.Fatal(err)
	}
	registry, err := hooks.NewRegistry(filepath.Join(f.root, "config", "hook-trust-v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	return New(managedwrite.New(hookexec.New(registry)))
}

func replayTerminalFixture(t *testing.T, conflict bool) pullFixture {
	t.Helper()
	fixture := newPullFixture(t)
	localName := filepath.Join(fixture.local, "topics", "why-files.md")
	remoteName := filepath.Join(fixture.remoteWork, "topics", "derived-state.md")
	if conflict {
		remoteName = filepath.Join(fixture.remoteWork, "topics", "why-files.md")
	}
	appendTestFile(t, localName, "\nLocal terminal replay.\n")
	gitTest(t, fixture.local, "add", "topics/why-files.md")
	gitTest(t, fixture.local, "commit", "--no-verify", "-m", "local terminal")
	appendTestFile(t, remoteName, "\nRemote terminal replay.\n")
	remoteLogical := "topics/derived-state.md"
	if conflict {
		remoteLogical = "topics/why-files.md"
	}
	gitTest(t, fixture.remoteWork, "add", remoteLogical)
	gitTest(t, fixture.remoteWork, "commit", "--no-verify", "-m", "remote terminal")
	gitTest(t, fixture.remoteWork, "push", "origin", "main")
	return fixture
}

func replayPairTestState(t *testing.T, root string, activated bool) (*gitraw.Repository, replayState, replayPlan) {
	t.Helper()
	repository, err := gitraw.Discover(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	tip := repository.Head.String()
	privateRef := "refs/heads/engram-pull/00000000000000000000000000000001"
	state := replayState{
		Version:   1,
		Original:  gitState(repository.HeadRef, tip),
		Private:   gitState(privateRef, tip),
		Base:      managedread.GitState{Commit: stringPointer(tip)},
		Reason:    "rejected",
		Conflicts: []string{},
	}
	plan := replayPlan{
		Version: 1, Remote: "origin", RemoteRef: "refs/heads/main",
		Original: cloneGitState(state.Original), PrivateRef: privateRef, RemoteTip: tip,
		Sources: []sourceCommit{{ID: tip, Base: tip, Message: "replay pair test"}},
		Next:    0, DraftReady: false, Fetched: 0, Replayed: 0,
		Audits: []managedread.HistoryAudit{},
	}
	if activated {
		gitTest(t, root, "update-ref", privateRef, tip)
		gitTest(t, root, "symbolic-ref", "HEAD", privateRef)
		repository, err = gitraw.Discover(t.Context(), root)
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := validateReplayPair(repositoryIf(activated, repository), state, plan); err != nil {
		t.Fatalf("invalid replay pair fixture: %v", err)
	}
	return repository, state, plan
}

func repositoryIf(condition bool, repository *gitraw.Repository) *gitraw.Repository {
	if condition {
		return repository
	}
	return nil
}

func mustCanonical(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := encodeCanonical(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func assertControllerPresence(t *testing.T, name string, want bool) {
	t.Helper()
	_, err := os.Lstat(name)
	if want && err != nil {
		t.Fatalf("controller %s is absent: %v", name, err)
	}
	if !want && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("controller %s is present or unreadable: %v", name, err)
	}
}

func openStore(t *testing.T, root string) *managedread.Store {
	t.Helper()
	store, err := managedread.Open(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func configureTestIdentity(t *testing.T, root string) {
	t.Helper()
	gitTest(t, root, "config", "user.name", "Ada")
	gitTest(t, root, "config", "user.email", "ada@example.test")
	gitTest(t, root, "config", "commit.gpgsign", "false")
}

func gitTest(t *testing.T, root string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, arguments...)...)
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(arguments, " "), err, output)
	}
	return string(output)
}

func testTip(t *testing.T, root string) string {
	t.Helper()
	return strings.TrimSpace(gitTest(t, root, "rev-parse", "HEAD"))
}

func appendTestFile(t *testing.T, name, suffix string) {
	t.Helper()
	data := readTestFile(t, name)
	if err := os.WriteFile(name, append(data, suffix...), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readTestFile(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func writeCommitWithMissingParent(t *testing.T, root, parent string) string {
	t.Helper()
	tree := strings.TrimSpace(gitTest(t, root, "rev-parse", "HEAD^{tree}"))
	content := fmt.Sprintf("tree %s\nparent %s\nauthor Ada <ada@example.test> 0 +0000\ncommitter Ada <ada@example.test> 0 +0000\n\nmissing incoming parent\n", tree, parent)
	name := filepath.Join(t.TempDir(), "commit")
	if err := os.WriteFile(name, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(gitTest(t, root, "hash-object", "-t", "commit", "-w", name))
}

type pullObservableState struct {
	HeadRef            string
	Head               string
	Refs               string
	Status             string
	Index              string
	Worktree           map[string]pullTestFileState
	CommonController   map[string]pullTestFileState
	WorktreeController map[string]pullTestFileState
}

type pullTestFileState struct {
	Mode fs.FileMode
	Data string
}

func capturePullObservableState(t *testing.T, root string) pullObservableState {
	t.Helper()
	repository, err := gitraw.Discover(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	status := gitTest(t, root, "status", "--porcelain=v1", "--untracked-files=all")
	index, err := os.ReadFile(filepath.Join(repository.GitDir, "index"))
	if err != nil {
		t.Fatal(err)
	}
	return pullObservableState{
		HeadRef:            strings.TrimSpace(gitTest(t, root, "symbolic-ref", "HEAD")),
		Head:               testTip(t, root),
		Refs:               gitTest(t, root, "for-each-ref", "--format=%(refname)%00%(objectname)"),
		Status:             status,
		Index:              string(index),
		Worktree:           capturePullTestTree(t, root, true),
		CommonController:   capturePullTestTree(t, filepath.Join(repository.CommonGitDir, "engram"), false),
		WorktreeController: capturePullTestTree(t, filepath.Join(repository.GitDir, "engram"), false),
	}
}

func assertPullObservableState(t *testing.T, root string, before pullObservableState) {
	t.Helper()
	after := capturePullObservableState(t, root)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("pull changed observable local state\nbefore: head=%s ref=%s refs=%q status=%q\nafter:  head=%s ref=%s refs=%q status=%q", before.Head, before.HeadRef, before.Refs, before.Status, after.Head, after.HeadRef, after.Refs, after.Status)
	}
}

func capturePullTestTree(t *testing.T, root string, skipGit bool) map[string]pullTestFileState {
	t.Helper()
	result := make(map[string]pullTestFileState)
	err := filepath.WalkDir(root, func(name string, entry fs.DirEntry, walkErr error) error {
		if errors.Is(walkErr, os.ErrNotExist) && name == root {
			return nil
		}
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, name)
		if err != nil || relative == "." {
			return err
		}
		if skipGit && relative == ".git" {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		state := pullTestFileState{Mode: info.Mode()}
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			state.Data, err = os.Readlink(name)
		case info.Mode().IsRegular():
			var data []byte
			data, err = os.ReadFile(name)
			state.Data = string(data)
		}
		if err != nil {
			return err
		}
		result[filepath.ToSlash(relative)] = state
		return nil
	})
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	return result
}

func pullRepositoryRoot(t *testing.T) string {
	t.Helper()
	directory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Clean(filepath.Join(directory, "..", ".."))
}
