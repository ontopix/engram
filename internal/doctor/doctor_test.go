package doctor

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ontopix/engram/internal/checker"
	"github.com/ontopix/engram/internal/gitraw"
	"github.com/ontopix/engram/internal/guard"
	"github.com/ontopix/engram/internal/journal"
	"github.com/ontopix/engram/internal/rendezvous"
	"github.com/ontopix/engram/internal/snapshot"
)

func TestInspectHealthyStoreEmitsRequiredChecksOnceInOrder(t *testing.T) {
	root := healthyStore(t)
	result, err := Inspect(context.Background(), root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Recovery.Requested || result.Recovery.Needed || result.Recovery.Performed || result.Recovery.Accepted == nil {
		t.Fatalf("recovery = %#v", result.Recovery)
	}
	if len(result.Checks) < len(requiredNames) {
		t.Fatalf("checks = %#v", result.Checks)
	}
	seen := make(map[string]int)
	for index, want := range requiredNames {
		check := result.Checks[index]
		if check.Name != want || check.Class != Required || check.Status != OK || check.Detail != nil {
			t.Fatalf("check %d = %#v, want healthy %s", index, check, want)
		}
		seen[check.Name]++
	}
	for _, want := range requiredNames {
		if seen[want] != 1 {
			t.Fatalf("required check %q count = %d", want, seen[want])
		}
	}
	if result.HasRequiredErrors() {
		t.Fatal("healthy result has required errors")
	}
}

func TestInspectSplitsSparseTransformAndCacheFailures(t *testing.T) {
	root := healthyStore(t)
	runTestGit(t, root, "config", "core.sparseCheckout", "true")
	runTestGit(t, root, "config", "core.autocrlf", "true")
	if err := os.WriteFile(filepath.Join(root, ".git", "info", "exclude"), []byte("other/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := Inspect(context.Background(), root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"presentation.sparse", "presentation.transforms", "cache.exclusion"} {
		check := findCheck(t, result, name)
		if check.Status != Error || check.Detail == nil {
			t.Fatalf("%s = %#v", name, check)
		}
	}
	if !result.HasRequiredErrors() {
		t.Fatal("presentation failures did not produce issues")
	}
}

func TestInspectLinkedWorktreeUsesCommonGuardAndCacheButOwnRecoveryState(t *testing.T) {
	root := healthyStore(t)
	linked := filepath.Join(t.TempDir(), "linked")
	runTestGit(t, root, "worktree", "add", "-b", "doctor-linked", linked)
	result, err := Inspect(context.Background(), linked, Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range requiredNames {
		if check := findCheck(t, result, name); check.Status != OK {
			detail := ""
			if check.Detail != nil {
				detail = *check.Detail
			}
			t.Fatalf("%s = %#v: %s", name, check, detail)
		}
	}
	repository := discoverTestRepository(t, linked)
	if repository.GitDir == repository.CommonGitDir {
		t.Fatal("fixture did not create a linked worktree")
	}
	if filepath.Dir(journal.Path(repository.GitDir)) == filepath.Dir(journal.Path(repository.CommonGitDir)) {
		t.Fatal("worktree recovery state was not isolated")
	}
}

func TestRecoverRemovesOnlyProvenStalePreJournalLocks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("portable Windows process-death proof is intentionally conservative")
	}
	root := healthyStore(t)
	repository := discoverTestRepository(t, root)
	owner := deadOwner(t, rendezvous.PreJournal)
	refLock := rendezvous.RefPath(repository.CommonGitDir, repository.HeadRef)
	worktreeLock := rendezvous.WorktreePath(repository.GitDir)
	writeOwner(t, refLock, owner)
	writeOwner(t, worktreeLock, owner)

	before, err := Inspect(context.Background(), root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !before.Recovery.Needed || findCheck(t, before, "recovery.state").Status != Error {
		t.Fatalf("before recovery = %#v", before.Recovery)
	}
	after, err := Inspect(context.Background(), root, Options{Recover: true})
	if err != nil {
		t.Fatal(err)
	}
	if !after.Recovery.Requested || !after.Recovery.Needed || !after.Recovery.Performed {
		t.Fatalf("after recovery = %#v", after.Recovery)
	}
	if findCheck(t, after, "recovery.state").Status != OK {
		t.Fatalf("recovery check = %#v", findCheck(t, after, "recovery.state"))
	}
	for _, name := range []string{refLock, worktreeLock} {
		if _, err := os.Lstat(name); !os.IsNotExist(err) {
			t.Fatalf("stale lock %s remains: %v", name, err)
		}
	}
}

func TestRecoverNeverGuessesMalformedJournal(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("portable Windows process-death proof is intentionally conservative")
	}
	root := healthyStore(t)
	repository := discoverTestRepository(t, root)
	owner := deadOwner(t, rendezvous.JournalRequired)
	refLock := rendezvous.RefPath(repository.CommonGitDir, repository.HeadRef)
	worktreeLock := rendezvous.WorktreePath(repository.GitDir)
	writeOwner(t, refLock, owner)
	writeOwner(t, worktreeLock, owner)
	journalPath := journal.Path(repository.GitDir)
	if err := os.MkdirAll(filepath.Dir(journalPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(journalPath, []byte("{malformed\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := Inspect(context.Background(), root, Options{Recover: true})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Recovery.Requested || !result.Recovery.Needed || result.Recovery.Performed {
		t.Fatalf("recovery = %#v", result.Recovery)
	}
	for _, name := range []string{refLock, worktreeLock, journalPath} {
		if _, err := os.Lstat(name); err != nil {
			t.Fatalf("ambiguous state was touched: %s: %v", name, err)
		}
	}
}

func TestLivePreJournalOwnerIsHealthyAndNotRecoveryNeeded(t *testing.T) {
	root := healthyStore(t)
	repository := discoverTestRepository(t, root)
	hostname, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	owner := rendezvous.Owner{
		Version: 1, Token: strings.Repeat("a", 64), PID: os.Getpid(), Hostname: hostname,
		StartedAt: time.Now().UTC().Format(time.RFC3339Nano), Phase: rendezvous.PreJournal,
	}
	writeOwner(t, rendezvous.WorktreePath(repository.GitDir), owner)
	result, err := Inspect(context.Background(), root, Options{Recover: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.Recovery.Needed || result.Recovery.Performed {
		t.Fatalf("live recovery = %#v", result.Recovery)
	}
	if check := findCheck(t, result, "recovery.state"); check.Status != OK || check.Detail == nil {
		t.Fatalf("live recovery check = %#v", check)
	}
}

func TestRecoverDoesNotRunAdapterWhileAnotherRecognizedOwnerMayBeLive(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("portable Windows process-death proof is intentionally conservative")
	}
	root := healthyStore(t)
	repository := discoverTestRepository(t, root)
	hostname, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	live := rendezvous.Owner{
		Version: 1, Token: strings.Repeat("a", 64), PID: os.Getpid(), Hostname: hostname,
		StartedAt: time.Now().UTC().Format(time.RFC3339Nano), Phase: rendezvous.PreJournal,
	}
	writeOwner(t, rendezvous.WorktreePath(repository.GitDir), live)
	stale := lifecycleState{
		Version: 1, Operation: "acquisition", Target: root,
		Owner: deadOwner(t, rendezvous.PreJournal), Phase: lifecycleCleanupRequired,
	}
	if err := os.WriteFile(lifecycleSidecar(root, "acquisition"), canonicalJSON(stale), 0o600); err != nil {
		t.Fatal(err)
	}
	called := false
	result, err := Inspect(context.Background(), root, Options{
		Recover: true,
		Recovery: RecoveryFunc(func(context.Context, RecoveryRequest) (RecoveryResponse, error) {
			called = true
			return RecoveryResponse{}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if called || !result.Recovery.Needed || result.Recovery.Performed {
		t.Fatalf("adapter called = %v, recovery = %#v", called, result.Recovery)
	}
}

func TestMissingTargetWithExactLifecycleEvidenceIsInspectable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("portable Windows process-death proof is intentionally conservative")
	}
	target := filepath.Join(t.TempDir(), "future-store")
	state := lifecycleState{
		Version: 1, Operation: "acquisition", Target: target,
		Owner: deadOwner(t, rendezvous.PreJournal), Phase: lifecycleCleanupRequired,
	}
	if err := os.WriteFile(lifecycleSidecar(target, "acquisition"), canonicalJSON(state), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := Inspect(context.Background(), target, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Recovery.Needed || findCheck(t, result, "acquisition.state").Status != Error {
		t.Fatalf("missing target result = %#v", result)
	}
	if findCheck(t, result, "initialization.state").Status != OK {
		t.Fatalf("initialization state = %#v", findCheck(t, result, "initialization.state"))
	}
}

func TestInternalLifecycleLookalikeIsNotRecognized(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("portable Windows process-death proof is intentionally conservative")
	}
	root := healthyStore(t)
	state := lifecycleState{
		Version: 1, Operation: "acquisition", Target: root,
		Owner: deadOwner(t, rendezvous.PreJournal), Phase: lifecycleCleanupRequired,
	}
	name := filepath.Join(root, ".git", "engram", "state", "acquisition-v1.json")
	if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, canonicalJSON(state), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := Inspect(context.Background(), root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Recovery.Needed || findCheck(t, result, "acquisition.state").Status != OK {
		t.Fatalf("internal lookalike was recognized: %#v", result.Recovery)
	}
}

func TestRecoveryRequestBindsAndRevalidatesExactLifecycleState(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("portable Windows process-death proof is intentionally conservative")
	}
	target := filepath.Join(t.TempDir(), "future-store")
	state := lifecycleState{
		Version: 1, Operation: "acquisition", Target: target,
		Owner: deadOwner(t, rendezvous.PreJournal), Phase: lifecycleCleanupRequired,
	}
	name := lifecycleSidecar(target, "acquisition")
	if err := os.WriteFile(name, canonicalJSON(state), 0o600); err != nil {
		t.Fatal(err)
	}
	called := false
	_, err := Inspect(context.Background(), target, Options{
		Recover: true,
		Recovery: RecoveryFunc(func(ctx context.Context, request RecoveryRequest) (RecoveryResponse, error) {
			called = true
			if request.Binding.Controller != RecoveryAcquisition || request.Binding.OwnerToken != state.Owner.Token || len(request.Binding.StateSHA256) != 64 {
				t.Fatalf("binding = %#v", request.Binding)
			}
			competing := state
			competing.Operation = "initialization"
			competing.Owner.Token = strings.Repeat("f", 64)
			competingName := lifecycleSidecar(target, "initialization")
			if writeErr := os.WriteFile(competingName, canonicalJSON(competing), 0o600); writeErr != nil {
				t.Fatal(writeErr)
			}
			if competingErr := request.Revalidate(ctx); KindOf(competingErr) != FailureConcurrency {
				t.Fatalf("competing controller revalidation = %v (%q)", competingErr, KindOf(competingErr))
			}
			if removeErr := os.Remove(competingName); removeErr != nil {
				t.Fatal(removeErr)
			}
			state.Owner.Token = strings.Repeat("e", 64)
			if writeErr := os.WriteFile(name, canonicalJSON(state), 0o600); writeErr != nil {
				t.Fatal(writeErr)
			}
			revalidateErr := request.Revalidate(ctx)
			if KindOf(revalidateErr) != FailureConcurrency {
				t.Fatalf("revalidate error = %v (%q)", revalidateErr, KindOf(revalidateErr))
			}
			return RecoveryResponse{Failure: FailureConcurrency, RecoveryRequired: true}, revalidateErr
		}),
	})
	if !called || KindOf(err) != FailureConcurrency {
		t.Fatalf("called = %v, error = %v (%q)", called, err, KindOf(err))
	}
	if mutation, present := MutationOf(err); !present || !mutation.RecoveryRequired || mutation.Durable || mutation.CheckoutChanged {
		t.Fatalf("mutation = %#v, present = %v", mutation, present)
	}
}

func TestRecoveryRequestBindsLifecyclePublicationPlan(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("portable Windows process-death proof is intentionally conservative")
	}
	target := filepath.Join(t.TempDir(), "future-store")
	state := lifecycleState{
		Version: 1, Operation: "acquisition", Target: target,
		Owner: deadOwner(t, rendezvous.PreJournal), Phase: lifecycleCleanupRequired,
	}
	name := lifecycleSidecar(target, "acquisition")
	if err := os.WriteFile(name, canonicalJSON(state), 0o600); err != nil {
		t.Fatal(err)
	}
	stage := target + ".engram-acquisition-v1-" + state.Owner.Token + ".stage"
	if err := os.Mkdir(stage, 0o700); err != nil {
		t.Fatal(err)
	}
	plan := filepath.Join(stage, "plan-v1.json")
	if err := os.WriteFile(plan, []byte("approved plan bytes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	called := false
	_, err := Inspect(context.Background(), target, Options{
		Recover: true,
		Recovery: RecoveryFunc(func(ctx context.Context, request RecoveryRequest) (RecoveryResponse, error) {
			called = true
			if err := os.WriteFile(plan, []byte("changed plan bytes\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			revalidateErr := request.Revalidate(ctx)
			if KindOf(revalidateErr) != FailureConcurrency {
				t.Fatalf("plan revalidation = %v (%q)", revalidateErr, KindOf(revalidateErr))
			}
			return RecoveryResponse{Failure: FailureConcurrency, RecoveryRequired: true}, revalidateErr
		}),
	})
	if !called || KindOf(err) != FailureConcurrency {
		t.Fatalf("called = %v, error = %v (%q)", called, err, KindOf(err))
	}
	if got, readErr := os.ReadFile(name); readErr != nil || !bytes.Equal(got, canonicalJSON(state)) {
		t.Fatalf("sidecar changed = %q, %v", got, readErr)
	}
}

func TestRecoveryFailurePreservesControllerClassAndEffects(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("portable Windows process-death proof is intentionally conservative")
	}
	target := filepath.Join(t.TempDir(), "future-store")
	state := lifecycleState{
		Version: 1, Operation: "initialization", Target: target,
		Owner: deadOwner(t, rendezvous.PreJournal), Phase: lifecycleCleanupRequired,
	}
	if err := os.WriteFile(lifecycleSidecar(target, "initialization"), canonicalJSON(state), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Inspect(context.Background(), target, Options{
		Recover: true,
		Recovery: RecoveryFunc(func(context.Context, RecoveryRequest) (RecoveryResponse, error) {
			return RecoveryResponse{
				Failure: FailureIO, Durable: true, CheckoutChanged: true, RecoveryRequired: true,
			}, os.ErrPermission
		}),
	})
	if KindOf(err) != FailureIO {
		t.Fatalf("kind = %q, error = %v", KindOf(err), err)
	}
	if mutation, present := MutationOf(err); !present || !mutation.Durable || !mutation.CheckoutChanged || !mutation.RecoveryRequired {
		t.Fatalf("mutation = %#v, present = %v", mutation, present)
	}
}

func TestRecoveryPostcheckCancellationKeepsSuccessfulEffects(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("portable Windows process-death proof is intentionally conservative")
	}
	target := filepath.Join(t.TempDir(), "future-store")
	state := lifecycleState{
		Version: 1, Operation: "acquisition", Target: target,
		Owner: deadOwner(t, rendezvous.PreJournal), Phase: lifecycleCleanupRequired,
	}
	if err := os.WriteFile(lifecycleSidecar(target, "acquisition"), canonicalJSON(state), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	_, err := Inspect(ctx, target, Options{
		Recover: true,
		Recovery: RecoveryFunc(func(context.Context, RecoveryRequest) (RecoveryResponse, error) {
			cancel()
			return RecoveryResponse{Durable: true, CheckoutChanged: true}, nil
		}),
	})
	if KindOf(err) != FailureCancelled {
		t.Fatalf("kind = %q, error = %v", KindOf(err), err)
	}
	if mutation, present := MutationOf(err); !present || !mutation.Durable || !mutation.CheckoutChanged || mutation.RecoveryRequired {
		t.Fatalf("mutation = %#v, present = %v", mutation, present)
	}
}

func TestHeuristicsRequireSnapshotEvidenceAndStayWarnings(t *testing.T) {
	description := "duplicate"
	snapshot := &checker.Snapshot{
		Tree: &snapshot.Tree{Files: map[string]snapshot.File{}},
		Validation: checker.Result{Target: checker.TargetSnapshot, Status: checker.StatusComplete, Findings: []checker.Finding{
			{Code: "W903", Path: "a.md"}, {Code: "W903", Path: "b.md"},
		}},
		Records: map[string]*checker.Record{
			"a.md": {Path: "a.md", Description: &description},
			"b.md": {Path: "b.md", Description: &description},
		},
		Maps: map[string]*checker.Map{}, Schemas: map[string]*checker.Schema{},
	}
	current := inspection{accepted: snapshot, result: Result{Checks: initialChecks()}}
	appendHeuristics(&current)
	if len(current.result.Checks) != len(requiredNames)+4 {
		t.Fatalf("heuristic checks = %#v", current.result.Checks[len(requiredNames):])
	}
	for _, check := range current.result.Checks[len(requiredNames):] {
		if check.Class != Heuristic || check.Status != Warning || check.Path == nil {
			t.Fatalf("heuristic = %#v", check)
		}
	}
	if current.result.HasRequiredErrors() {
		t.Fatal("heuristic warnings changed required outcome")
	}
}

func healthyStore(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	root := t.TempDir()
	source := filepath.Join(repositoryRoot(t), "examples", "minimal")
	if err := os.CopyFS(root, os.DirFS(source)); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, root, "init", "--initial-branch=main")
	for _, pair := range [][2]string{
		{"user.name", "Ada"}, {"user.email", "ada@example.test"}, {"commit.gpgsign", "false"},
		{"core.autocrlf", "false"}, {"core.sparseCheckout", "false"}, {"index.sparse", "false"},
	} {
		runTestGit(t, root, "config", "--local", pair[0], pair[1])
	}
	runTestGit(t, root, "add", "--all")
	runTestGit(t, root, "commit", "--no-verify", "-m", "initial")
	repository := discoverTestRepository(t, root)
	if _, err := guard.Install(context.Background(), repository); err != nil {
		t.Fatal(err)
	}
	exclude := filepath.Join(repository.CommonGitDir, "info", "exclude")
	if err := os.MkdirAll(filepath.Dir(exclude), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exclude, []byte(".engram/cache/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	directory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(directory, "go.mod")); err == nil {
			return directory
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			t.Fatal("repository root not found")
		}
		directory = parent
	}
}

func runTestGit(t *testing.T, root string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, arguments...)...)
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(arguments, " "), err, output)
	}
	return string(output)
}

func discoverTestRepository(t *testing.T, root string) *gitraw.Repository {
	t.Helper()
	repository, err := gitraw.Discover(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	return repository
}

func deadOwner(t *testing.T, phase rendezvous.Phase) rendezvous.Owner {
	t.Helper()
	hostname, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	return rendezvous.Owner{
		Version: 1, Token: strings.Repeat("d", 64), PID: 2147483647, Hostname: hostname,
		StartedAt: time.Now().UTC().Format(time.RFC3339Nano), Phase: phase,
	}
}

func writeOwner(t *testing.T, name string, owner rendezvous.Owner) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(owner)
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(name, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func findCheck(t *testing.T, result Result, name string) Check {
	t.Helper()
	for _, check := range result.Checks {
		if check.Name == name {
			return check
		}
	}
	t.Fatalf("check %q not found", name)
	return Check{}
}
