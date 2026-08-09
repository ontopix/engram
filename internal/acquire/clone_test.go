package acquire

import (
	"context"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ontopix/engram/internal/guard"
	"github.com/ontopix/engram/internal/lifecycle"
	"github.com/ontopix/engram/internal/managedread"
)

func TestClonePublishesOnlyVerifiedManagedStore(t *testing.T) {
	location := bareFixture(t, false)
	destination := filepath.Join(t.TempDir(), "memory")
	result, err := Clone(context.Background(), location, Options{Destination: destination, DestinationProvided: true})
	if err != nil {
		t.Fatal(err)
	}
	canonicalParent, err := filepath.EvalSymlinks(filepath.Dir(destination))
	if err != nil {
		t.Fatal(err)
	}
	canonicalDestination := filepath.Join(canonicalParent, filepath.Base(destination))
	if !result.Published || result.Reused || result.Root != canonicalDestination || result.Remote != "origin" || result.VerifiedCommits != 1 || result.Launcher != guard.Installed || result.Validation.HasErrors() {
		t.Fatalf("result = %#v", result)
	}
	store, err := managedread.Open(context.Background(), canonicalDestination)
	if err != nil {
		t.Fatal(err)
	}
	if state, err := guard.Inspect(context.Background(), store.Repository()); err != nil || state != guard.Unchanged {
		t.Fatalf("guard = %q, %v", state, err)
	}
	if ok, err := hasCacheExclusion(store.Repository().GitDir); err != nil || !ok {
		t.Fatalf("cache exclusion = %v, %v", ok, err)
	}
	assertNoAcquisitionState(t, canonicalDestination)
}

func TestCloneDoesNotPublishInvalidAcceptedHistory(t *testing.T) {
	location := bareFixture(t, true)
	destination := filepath.Join(t.TempDir(), "invalid")
	result, err := Clone(context.Background(), location, Options{Destination: destination, DestinationProvided: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.Published || !result.Validation.HasErrors() || result.Launcher != guard.Planned {
		t.Fatalf("result = %#v", result)
	}
	if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid destination was published: %v", err)
	}
	assertNoAcquisitionState(t, destination)
}

func TestClonePrePublicationFailureCleansExactLifecycle(t *testing.T) {
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(parent, "failed")
	missing := (&url.URL{Scheme: "file", Path: filepath.Join(parent, "missing.git")}).String()
	if _, err := Clone(context.Background(), missing, Options{Destination: destination, DestinationProvided: true}); KindOf(err) != ErrorNetwork {
		t.Fatalf("clone error = %v", err)
	}
	assertNoAcquisitionState(t, destination)
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "failed.engram-acquisition-v1-") {
			t.Fatalf("private stage remains: %s", entry.Name())
		}
	}
}

func TestCloneExistingLifecycleBlocksBeforeNetwork(t *testing.T) {
	destination := canonicalTestDestination(t, "active")
	handle, err := lifecycle.Begin(destination, lifecycle.Acquisition)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = handle.Remove() })
	missing := (&url.URL{Scheme: "file", Path: filepath.Join(filepath.Dir(destination), "missing.git")}).String()
	if _, err := Clone(context.Background(), missing, Options{Destination: destination, DestinationProvided: true}); KindOf(err) != ErrorConflict {
		t.Fatalf("clone error = %v", err)
	}
}

func TestCloneCleanupFailureRetainsRecoverableExactState(t *testing.T) {
	location := bareFixture(t, false)
	destination := canonicalTestDestination(t, "cleanup-failure")
	var stage string
	cloner := &Cloner{Fault: func(phase Phase) error {
		if phase != PhaseVerified {
			return nil
		}
		state, _, err := lifecycle.Read(destination, lifecycle.Acquisition)
		if err != nil {
			return err
		}
		stage, err = lifecycle.Stage(state)
		if err != nil {
			return err
		}
		if err := os.RemoveAll(stage); err != nil {
			return err
		}
		if err := os.WriteFile(stage, []byte("occupied"), 0o600); err != nil {
			return err
		}
		return errors.New("injected")
	}}
	_, err := cloner.Run(context.Background(), location, Options{Destination: destination, DestinationProvided: true})
	if KindOf(err) != ErrorRecovery {
		t.Fatalf("clone error = %v", err)
	}
	mutation, ok := MutationOf(err)
	if !ok || !mutation.RecoveryRequired || mutation.CheckoutChanged || mutation.Accepted != nil {
		t.Fatalf("mutation = %#v, %v", mutation, ok)
	}
	if _, _, err := lifecycle.Read(destination, lifecycle.Acquisition); err != nil {
		t.Fatalf("state was lost: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Remove(stage)
		_ = os.Remove(lifecycle.Sidecar(destination, lifecycle.Acquisition))
	})
}

func TestDefaultCloneReuseIsStrictAndNetworkSilent(t *testing.T) {
	location := bareFixture(t, false)
	destination := filepath.Join(t.TempDir(), "controller", "stores", "fixed")
	options := Options{Destination: destination}
	first, err := Clone(context.Background(), location, options)
	if err != nil || !first.Published || first.Reused {
		t.Fatalf("first = %#v, %v", first, err)
	}
	second, err := Clone(context.Background(), location, options)
	if err != nil || second.Published || !second.Reused || second.Launcher != guard.Unchanged {
		t.Fatalf("second = %#v, %v", second, err)
	}
	runGit(t, destination, "remote", "set-url", "origin", "file:///different.git")
	if _, err := Clone(context.Background(), location, options); KindOf(err) != ErrorConflict {
		t.Fatalf("drift error = %v", err)
	}
}

func TestDefaultCloneReuseRefusesLifecycleState(t *testing.T) {
	location := bareFixture(t, false)
	destination := canonicalTestDestination(t, "reuse-state")
	options := Options{Destination: destination}
	if result, err := Clone(context.Background(), location, options); err != nil || !result.Published {
		t.Fatalf("initial clone = %#v, %v", result, err)
	}
	handle, err := lifecycle.Begin(destination, lifecycle.Acquisition)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = handle.Remove() })
	if _, err := Clone(context.Background(), location, options); KindOf(err) != ErrorConflict {
		t.Fatalf("reuse error = %v", err)
	}
}

func TestCloneCleanupRequiredFaultRecoversUnpublishedStage(t *testing.T) {
	location := bareFixture(t, false)
	destination := canonicalTestDestination(t, "cleanup-required")
	runFaultedClone(t, location, destination, PhaseCleanupRequired, "")

	state, _, err := lifecycle.Read(destination, lifecycle.Acquisition)
	if err != nil || state.Phase != lifecycle.CleanupRequired {
		t.Fatalf("state = %#v, %v", state, err)
	}
	stage, err := lifecycle.Stage(state)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(stage, "store")); err != nil {
		t.Fatalf("staged checkout = %v", err)
	}
	if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("destination = %v", err)
	}

	recovered, err := Recover(context.Background(), destination)
	if err != nil {
		t.Fatal(err)
	}
	if !recovered.Needed || !recovered.Performed || recovered.Published || !recovered.Durable || recovered.Accepted != nil {
		t.Fatalf("recovered = %#v", recovered)
	}
	if _, err := os.Lstat(stage); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stage remains: %v", err)
	}
	assertNoAcquisitionState(t, destination)
	second, err := Recover(context.Background(), destination)
	if err != nil || second.Needed || second.Performed {
		t.Fatalf("second recovery = %#v, %v", second, err)
	}
}

func TestClonePublishedFaultRecoversAcceptedCheckout(t *testing.T) {
	location := bareFixture(t, false)
	for _, phase := range []Phase{PhasePublished, PhaseDurable, PhaseStageCleaned} {
		t.Run(string(phase), func(t *testing.T) {
			destination := canonicalTestDestination(t, string(phase))
			runFaultedClone(t, location, destination, phase, "")
			if _, err := os.Lstat(destination); err != nil {
				t.Fatalf("published target = %v", err)
			}
			recovered, err := Recover(context.Background(), destination)
			if err != nil {
				t.Fatal(err)
			}
			if !recovered.Needed || !recovered.Performed || !recovered.Published || !recovered.Durable || recovered.Accepted == nil || recovered.Accepted.Ref == nil || recovered.Accepted.Commit == nil {
				t.Fatalf("recovered = %#v", recovered)
			}
			assertNoAcquisitionState(t, destination)
			if _, _, err := verifyPublishedStore(context.Background(), destination); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCloneConcurrentDestinationIsPreservedByRecovery(t *testing.T) {
	location := bareFixture(t, false)
	destination := canonicalTestDestination(t, "concurrent")
	runFaultedClone(t, location, destination, PhaseCleanupRequired, "race")
	marker := filepath.Join(destination, "foreign")
	if data, err := os.ReadFile(marker); err != nil || string(data) != "preserve" {
		t.Fatalf("foreign target = %q, %v", data, err)
	}
	recovered, err := Recover(context.Background(), destination)
	if err != nil {
		t.Fatal(err)
	}
	if !recovered.Needed || !recovered.Performed || recovered.Published || recovered.Accepted != nil {
		t.Fatalf("recovered = %#v", recovered)
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "preserve" {
		t.Fatalf("foreign target after recovery = %q, %v", data, err)
	}
	assertNoAcquisitionState(t, destination)
}

func TestConcurrentCloneRecoveryIsIdempotent(t *testing.T) {
	location := bareFixture(t, false)
	destination := canonicalTestDestination(t, "parallel-recovery")
	runFaultedClone(t, location, destination, PhaseCleanupRequired, "")

	const workers = 8
	results := make(chan *RecoveryResult, workers)
	errorsSeen := make(chan error, workers)
	var wait sync.WaitGroup
	for range workers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			result, err := Recover(context.Background(), destination)
			results <- result
			errorsSeen <- err
		}()
	}
	wait.Wait()
	close(results)
	close(errorsSeen)
	for err := range errorsSeen {
		if err != nil {
			t.Fatalf("parallel recovery: %v", err)
		}
	}
	performed := 0
	for result := range results {
		if result == nil {
			t.Fatal("nil parallel recovery result")
		}
		if result.Performed {
			performed++
		}
	}
	if performed != 1 {
		t.Fatalf("performed = %d, want 1", performed)
	}
	assertNoAcquisitionState(t, destination)
}

func TestCloneRecoveryRefusesLiveOwnerAndMalformedPlan(t *testing.T) {
	t.Run("live", func(t *testing.T) {
		destination := canonicalTestDestination(t, "live")
		handle, err := lifecycle.Begin(destination, lifecycle.Acquisition)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = handle.Remove() })
		result, err := Recover(context.Background(), destination)
		if KindOf(err) != ErrorConcurrency || result == nil || !result.Needed {
			t.Fatalf("recover = %#v, %v", result, err)
		}
		if _, _, err := lifecycle.Read(destination, lifecycle.Acquisition); err != nil {
			t.Fatalf("live state changed: %v", err)
		}
	})

	t.Run("malformed-plan", func(t *testing.T) {
		location := bareFixture(t, false)
		destination := canonicalTestDestination(t, "malformed")
		runFaultedClone(t, location, destination, PhasePublished, "")
		state, _, err := lifecycle.Read(destination, lifecycle.Acquisition)
		if err != nil {
			t.Fatal(err)
		}
		stage, err := lifecycle.Stage(state)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(publicationPlanPath(stage), []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		result, err := Recover(context.Background(), destination)
		if KindOf(err) != ErrorRecovery || result == nil || !result.Needed {
			t.Fatalf("recover = %#v, %v", result, err)
		}
		if _, _, err := lifecycle.Read(destination, lifecycle.Acquisition); err != nil {
			t.Fatalf("state was removed: %v", err)
		}
		if _, err := os.Lstat(destination); err != nil {
			t.Fatalf("target was removed: %v", err)
		}
	})
}

func TestClonePublishedFailureCarriesMutationEvidence(t *testing.T) {
	location := bareFixture(t, false)
	destination := canonicalTestDestination(t, "mutation")
	cloner := &Cloner{Fault: func(phase Phase) error {
		if phase == PhasePublished {
			return errors.New("injected")
		}
		return nil
	}}
	result, err := cloner.Run(context.Background(), location, Options{Destination: destination, DestinationProvided: true})
	if err == nil || !result.Published {
		t.Fatalf("clone = %#v, %v", result, err)
	}
	mutation, ok := MutationOf(err)
	if !ok || mutation.Durable || !mutation.CheckoutChanged || !mutation.RecoveryRequired || mutation.Accepted == nil || mutation.Accepted.Commit == nil {
		t.Fatalf("mutation = %#v, %v", mutation, ok)
	}
	state, _, stateErr := lifecycle.Read(destination, lifecycle.Acquisition)
	if stateErr != nil {
		t.Fatal(stateErr)
	}
	stage, stageErr := lifecycle.Stage(state)
	if stageErr != nil {
		t.Fatal(stageErr)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(stage)
		_ = os.Remove(lifecycle.Sidecar(destination, lifecycle.Acquisition))
	})
}

func TestCloneRecoveryFaultHelper(t *testing.T) {
	if os.Getenv("ENGRAM_ACQUIRE_FAULT_HELPER") != "1" {
		return
	}
	location := os.Getenv("ENGRAM_ACQUIRE_LOCATION")
	destination := os.Getenv("ENGRAM_ACQUIRE_DESTINATION")
	wanted := Phase(os.Getenv("ENGRAM_ACQUIRE_PHASE"))
	mode := os.Getenv("ENGRAM_ACQUIRE_MODE")
	cloner := &Cloner{Fault: func(phase Phase) error {
		if phase != wanted {
			return nil
		}
		if mode == "race" {
			if err := os.Mkdir(destination, 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(destination, "foreign"), []byte("preserve"), 0o644); err != nil {
				return err
			}
			return nil
		}
		return errors.New("injected helper fault")
	}}
	_, err := cloner.Run(context.Background(), location, Options{Destination: destination, DestinationProvided: true})
	if err == nil {
		t.Fatal("faulted clone unexpectedly succeeded")
	}
}

func runFaultedClone(t *testing.T, location, destination string, phase Phase, mode string) {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.run=^TestCloneRecoveryFaultHelper$")
	command.Env = append(os.Environ(),
		"ENGRAM_ACQUIRE_FAULT_HELPER=1",
		"ENGRAM_ACQUIRE_LOCATION="+location,
		"ENGRAM_ACQUIRE_DESTINATION="+destination,
		"ENGRAM_ACQUIRE_PHASE="+string(phase),
		"ENGRAM_ACQUIRE_MODE="+mode,
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("fault helper: %v\n%s", err, output)
	}
}

func canonicalTestDestination(t *testing.T, name string) string {
	t.Helper()
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(parent, name)
}

func assertNoAcquisitionState(t *testing.T, destination string) {
	t.Helper()
	if _, err := os.Lstat(lifecycle.Sidecar(destination, lifecycle.Acquisition)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("acquisition state remains: %v", err)
	}
}

func bareFixture(t *testing.T, invalid bool) string {
	t.Helper()
	root := t.TempDir()
	minimal := filepath.Join(repositoryRoot(t), "examples", "minimal")
	if err := os.CopyFS(root, os.DirFS(minimal)); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "init", "--initial-branch=main")
	runGit(t, root, "config", "user.name", "Test")
	runGit(t, root, "config", "user.email", "test@example.test")
	runGit(t, root, "config", "commit.gpgsign", "false")
	if invalid {
		if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("invalid"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runGit(t, root, "add", "--all")
	runGit(t, root, "commit", "--no-verify", "-m", "initial")
	bare := filepath.Join(t.TempDir(), "remote.git")
	command := exec.Command("git", "clone", "--bare", "--", root, bare)
	command.Env = testGitEnvironment()
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("bare clone: %v\n%s", err, output)
	}
	value := (&url.URL{Scheme: "file", Path: bare}).String()
	return value
}

func runGit(t *testing.T, root string, arguments ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, arguments...)...)
	command.Env = testGitEnvironment()
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(arguments, " "), err, output)
	}
}

func testGitEnvironment() []string {
	return append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	working, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Clean(filepath.Join(working, "..", ".."))
}
