package acquire

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/ontopix/engram/internal/guard"
	"github.com/ontopix/engram/internal/lifecycle"
	"github.com/ontopix/engram/internal/managedread"
	"github.com/ontopix/engram/internal/rendezvous"
	"github.com/ontopix/engram/internal/testpath"
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
	missing := testpath.FileURL(filepath.Join(parent, "missing.git"))
	if _, err := Clone(context.Background(), missing, Options{Destination: destination, DestinationProvided: true}); KindOf(err) != ErrorNetwork {
		t.Fatalf("clone error = %v", err)
	}
	assertNoAcquisitionState(t, destination)
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".engram-stage-v1-") && strings.HasSuffix(entry.Name(), ".stage") {
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
	missing := testpath.FileURL(filepath.Join(filepath.Dir(destination), "missing.git"))
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
	if !ok || !mutation.Durable || !mutation.RecoveryRequired || mutation.CheckoutChanged || mutation.Accepted != nil {
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
	for _, phase := range []Phase{PhasePublished, PhaseDurable} {
		t.Run(string(phase), func(t *testing.T) {
			destination := canonicalTestDestination(t, string(phase))
			runFaultedClone(t, location, destination, phase, "")
			if _, err := os.Lstat(destination); err != nil {
				t.Fatalf("published target = %v", err)
			}
			observation, err := lifecycle.ObserveRecovery(destination, lifecycle.Acquisition)
			if err != nil {
				t.Fatal(err)
			}
			recovered, err := RecoverExpected(context.Background(), destination, observation.Expectation)
			if err != nil {
				t.Fatal(err)
			}
			if !recovered.Needed || !recovered.Performed || !recovered.Published || !recovered.Durable || recovered.CheckoutChanged || recovered.RecoveryRequired || recovered.Accepted == nil || recovered.Accepted.Ref == nil || recovered.Accepted.Commit == nil {
				t.Fatalf("recovered = %#v", recovered)
			}
			assertNoAcquisitionState(t, destination)
			if _, _, err := verifyPublishedStore(context.Background(), destination); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCloneRecoveryWithoutLifecycleStateIsANoop(t *testing.T) {
	destination := canonicalTestDestination(t, "no-recovery-state")
	result, err := Recover(context.Background(), destination)
	if err != nil || result == nil || result.Needed || result.Performed || result.Durable || result.CheckoutChanged || result.RecoveryRequired || result.Accepted != nil {
		t.Fatalf("recovery = %#v, %v", result, err)
	}
	if _, err := os.Lstat(lifecycle.Sidecar(destination, lifecycle.Acquisition) + ".lease"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("no-op recovery created a lease file: %v", err)
	}
}

func TestCloneRecoveryLeaseRejectsAConcurrentController(t *testing.T) {
	location := bareFixture(t, false)
	destination := canonicalTestDestination(t, "contended-recovery")
	runFaultedClone(t, location, destination, PhaseCleanupRequired, "")
	observation, err := lifecycle.ObserveRecovery(destination, lifecycle.Acquisition)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := lifecycle.AcquireRecovery(destination, lifecycle.Acquisition)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()

	result, recoverErr := RecoverExpected(context.Background(), destination, observation.Expectation)
	if KindOf(recoverErr) != ErrorConcurrency || !errors.Is(recoverErr, rendezvous.ErrBusy) || result == nil || !result.Needed || result.Performed || result.Durable || result.CheckoutChanged || !result.RecoveryRequired {
		t.Fatalf("contended recovery = %#v, %v", result, recoverErr)
	}
	if _, _, err := lifecycle.Read(destination, lifecycle.Acquisition); err != nil {
		t.Fatalf("contended recovery changed lifecycle state: %v", err)
	}
	cleanupAcquisitionArtifacts(t, destination)
}

func TestRecoverExpectedRejectsChangedClonePlanAndPreservesForeignTarget(t *testing.T) {
	location := bareFixture(t, false)
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, string)
	}{
		{name: "bytes", mutate: func(t *testing.T, name string) {
			t.Helper()
			if err := os.WriteFile(name, []byte("{}\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "symlink", mutate: func(t *testing.T, name string) {
			t.Helper()
			approved, err := os.ReadFile(name)
			if err != nil {
				t.Fatal(err)
			}
			other := filepath.Join(filepath.Dir(name), "replacement-plan")
			if err := os.WriteFile(other, approved, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(name); err != nil {
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
			if test.name == "symlink" && runtime.GOOS == "windows" {
				t.Skip("symlink creation is not portable on Windows")
			}
			destination := canonicalTestDestination(t, "plan-"+test.name)
			runFaultedClone(t, location, destination, PhaseCleanupRequired, "race")
			state, _, err := lifecycle.Read(destination, lifecycle.Acquisition)
			if err != nil {
				t.Fatal(err)
			}
			stage, err := lifecycle.Stage(state)
			if err != nil {
				t.Fatal(err)
			}
			observation, err := lifecycle.ObserveRecovery(destination, lifecycle.Acquisition)
			if err != nil {
				t.Fatal(err)
			}
			test.mutate(t, publicationPlanPath(stage))
			recovered, recoverErr := RecoverExpected(context.Background(), destination, observation.Expectation)
			if KindOf(recoverErr) != ErrorConcurrency || recovered == nil || !recovered.Needed {
				t.Fatalf("recovery = %#v, %v", recovered, recoverErr)
			}
			mutation, present := MutationOf(recoverErr)
			if !present || mutation.Durable || mutation.CheckoutChanged || !mutation.RecoveryRequired {
				t.Fatalf("recovery mutation = %#v, %v", mutation, present)
			}
			marker := filepath.Join(destination, "foreign")
			if data, err := os.ReadFile(marker); err != nil || string(data) != "preserve" {
				t.Fatalf("foreign target changed = %q, %v", data, err)
			}
			if _, err := os.Lstat(stage); err != nil {
				t.Fatalf("stage was removed: %v", err)
			}
			if _, _, err := lifecycle.Read(destination, lifecycle.Acquisition); err != nil {
				t.Fatalf("sidecar was removed: %v", err)
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
		if err != nil && (KindOf(err) != ErrorConcurrency || !errors.Is(err, rendezvous.ErrBusy)) {
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
		mutation, present := MutationOf(err)
		if !present || mutation.Durable || mutation.CheckoutChanged || !mutation.RecoveryRequired {
			t.Fatalf("mutation = %#v, %v", mutation, present)
		}
		if _, _, err := lifecycle.Read(destination, lifecycle.Acquisition); err != nil {
			t.Fatalf("state was removed: %v", err)
		}
		if _, err := os.Lstat(destination); err != nil {
			t.Fatalf("target was removed: %v", err)
		}
	})
}

func TestCloneFaultPhasesCarryOnlyAttributableMutationEvidence(t *testing.T) {
	location := bareFixture(t, false)
	tests := []struct {
		phase         Phase
		wantDurable   bool
		wantCheckout  bool
		wantRecovery  bool
		wantPublished bool
		wantAccepted  bool
	}{
		{phase: PhaseCleanupRequired, wantDurable: true, wantRecovery: true},
		{phase: PhasePublished, wantDurable: true, wantCheckout: true, wantRecovery: true, wantPublished: true, wantAccepted: true},
		{phase: PhaseDurable, wantDurable: true, wantCheckout: true, wantRecovery: true, wantPublished: true, wantAccepted: true},
		{phase: PhaseStageCleaned, wantDurable: true, wantCheckout: true, wantPublished: true, wantAccepted: true},
		{phase: PhaseCleaned, wantDurable: true, wantCheckout: true, wantPublished: true, wantAccepted: true},
	}
	for _, test := range tests {
		t.Run(string(test.phase), func(t *testing.T) {
			destination := canonicalTestDestination(t, "mutation-"+string(test.phase))
			cloner := &Cloner{Fault: func(phase Phase) error {
				if phase == test.phase {
					return errors.New("injected")
				}
				return nil
			}}
			result, err := cloner.Run(context.Background(), location, Options{Destination: destination, DestinationProvided: true})
			if err == nil || result.Published != test.wantPublished {
				t.Fatalf("clone = %#v, %v", result, err)
			}
			mutation, ok := MutationOf(err)
			if !ok || mutation.Durable != test.wantDurable || mutation.CheckoutChanged != test.wantCheckout || mutation.RecoveryRequired != test.wantRecovery || (mutation.Accepted != nil) != test.wantAccepted {
				t.Fatalf("mutation = %#v, %v", mutation, ok)
			}
			cleanupAcquisitionArtifacts(t, destination)
		})
	}
}

func TestClonePublicationSyncReportsVisibleAndDurableEffectsSeparately(t *testing.T) {
	location := bareFixture(t, false)
	injected := errors.New("injected publication sync failure")
	for _, test := range []struct {
		name        string
		durable     bool
		wantDurable bool
	}{
		{name: "sync failed", durable: false, wantDurable: true},
		{name: "close failed after sync", durable: true, wantDurable: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			destination := canonicalTestDestination(t, "sync-"+strings.ReplaceAll(test.name, " ", "-"))
			cloner := New()
			cloner.syncPublicationDirectory = func(string) (bool, error) { return test.durable, injected }
			result, err := cloner.Run(context.Background(), location, Options{Destination: destination, DestinationProvided: true})
			mutation, present := MutationOf(err)
			if !errors.Is(err, injected) || !result.Published || !present || mutation.Durable != test.wantDurable || !mutation.CheckoutChanged || !mutation.RecoveryRequired || mutation.Accepted == nil {
				t.Fatalf("result = %#v, error = %v, mutation = %#v, present = %t", result, err, mutation, present)
			}
			if _, err := os.Lstat(destination); err != nil {
				t.Fatalf("visible destination = %v", err)
			}
			cleanupAcquisitionArtifacts(t, destination)
		})
	}
}

func TestCloneAtomicPublicationRefusesLateDestination(t *testing.T) {
	location := bareFixture(t, false)
	destination := canonicalTestDestination(t, "late-destination")
	var appeared os.FileInfo
	cloner := New()
	cloner.renamePublication = func(oldPath, newPath string) (bool, error) {
		if err := os.Mkdir(newPath, 0o700); err != nil {
			return false, err
		}
		var err error
		appeared, err = os.Lstat(newPath)
		if err != nil {
			return false, err
		}
		return renameNoReplace(oldPath, newPath)
	}
	result, err := cloner.Run(context.Background(), location, Options{Destination: destination, DestinationProvided: true})
	if KindOf(err) != ErrorConcurrency || !errors.Is(err, os.ErrExist) || result.Published {
		t.Fatalf("clone = %#v, %v", result, err)
	}
	mutation, present := MutationOf(err)
	if !present || !mutation.Durable || mutation.CheckoutChanged || !mutation.RecoveryRequired || mutation.Accepted != nil {
		t.Fatalf("mutation = %#v, present = %t", mutation, present)
	}
	current, statErr := os.Lstat(destination)
	if statErr != nil || !current.IsDir() || !os.SameFile(appeared, current) {
		t.Fatalf("late destination = %#v, %v", current, statErr)
	}
	state, _, stateErr := lifecycle.Read(destination, lifecycle.Acquisition)
	if stateErr != nil || state.Phase != lifecycle.CleanupRequired {
		t.Fatalf("lifecycle = %#v, %v", state, stateErr)
	}
	stage, stageErr := lifecycle.Stage(state)
	if stageErr != nil {
		t.Fatal(stageErr)
	}
	if _, stageErr := os.Lstat(filepath.Join(stage, "store")); stageErr != nil {
		t.Fatalf("unpublished checkout was consumed: %v", stageErr)
	}
	cleanupAcquisitionArtifacts(t, destination)
}

func TestCloneRecoveryDoesNotAttributeObservedCheckout(t *testing.T) {
	location := bareFixture(t, false)
	injected := errors.New("injected recovery cleanup failure")
	tests := []struct {
		name          string
		phase         Phase
		removeStage   bool
		operations    func(string) recoveryOperations
		wantDurable   bool
		wantPerformed bool
		wantPublished bool
		wantRecovery  bool
	}{
		{
			name: "published sidecar durable before stage cleanup failure", phase: PhasePublished,
			operations: func(string) recoveryOperations {
				return recoveryOperations{cleanupStage: func(string) error { return injected }}
			},
			wantDurable: true, wantPerformed: true, wantPublished: true,
		},
		{
			name: "sidecar removal refused", phase: PhaseCleanupRequired, removeStage: true,
			operations: func(string) recoveryOperations {
				return recoveryOperations{removeLifecycle: func(*lifecycle.Handle) error { return injected }}
			},
			wantRecovery: true,
		},
		{
			name: "sidecar visible removal before sync", phase: PhaseCleanupRequired, removeStage: true,
			operations: func(destination string) recoveryOperations {
				return recoveryOperations{removeLifecycle: func(*lifecycle.Handle) error {
					if err := os.Remove(lifecycle.Sidecar(destination, lifecycle.Acquisition)); err != nil {
						return errors.Join(injected, err)
					}
					return injected
				}}
			},
			wantPerformed: true,
		},
		{
			name: "visible sidecar removal preserves stage plan", phase: PhasePublished,
			operations: func(destination string) recoveryOperations {
				return recoveryOperations{removeLifecycle: func(*lifecycle.Handle) error {
					if err := os.Remove(lifecycle.Sidecar(destination, lifecycle.Acquisition)); err != nil {
						return errors.Join(injected, err)
					}
					return injected
				}}
			},
			wantPerformed: true, wantPublished: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			destination := canonicalTestDestination(t, strings.ReplaceAll(test.name, " ", "-"))
			runFaultedClone(t, location, destination, test.phase, "")
			if test.removeStage {
				state, _, err := lifecycle.Read(destination, lifecycle.Acquisition)
				if err != nil {
					t.Fatal(err)
				}
				stage, err := lifecycle.Stage(state)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.RemoveAll(stage); err != nil {
					t.Fatal(err)
				}
			}
			observation, err := lifecycle.ObserveRecovery(destination, lifecycle.Acquisition)
			if err != nil {
				t.Fatal(err)
			}
			result, recoverErr := recover(context.Background(), destination, &observation.Expectation, test.operations(destination))
			if !errors.Is(recoverErr, injected) || result == nil || !result.Needed || result.Published != test.wantPublished || result.Durable != test.wantDurable || result.Performed != test.wantPerformed || result.CheckoutChanged || result.RecoveryRequired != test.wantRecovery {
				t.Fatalf("recovery = %#v, %v", result, recoverErr)
			}
			mutation, present := MutationOf(recoverErr)
			if !present || mutation.Durable != test.wantDurable || mutation.CheckoutChanged || mutation.RecoveryRequired != test.wantRecovery {
				t.Fatalf("mutation = %#v, %v", mutation, present)
			}
			if test.wantPublished {
				if _, _, err := verifyPublishedStore(context.Background(), destination); err != nil {
					t.Fatalf("published checkout changed: %v", err)
				}
			} else if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("unpublished target = %v", err)
			}
			cleanupAcquisitionArtifacts(t, destination)
		})
	}
}

func TestPublishedCloneRecoveryRetainsPlanUntilLifecycleRemovalSucceeds(t *testing.T) {
	location := bareFixture(t, false)
	destination := canonicalTestDestination(t, "published-removal-retry")
	runFaultedClone(t, location, destination, PhasePublished, "")
	state, _, err := lifecycle.Read(destination, lifecycle.Acquisition)
	if err != nil {
		t.Fatal(err)
	}
	stage, err := lifecycle.Stage(state)
	if err != nil {
		t.Fatal(err)
	}
	observation, err := lifecycle.ObserveRecovery(destination, lifecycle.Acquisition)
	if err != nil {
		t.Fatal(err)
	}
	injected := errors.New("injected lifecycle removal refusal")
	result, recoverErr := recover(context.Background(), destination, &observation.Expectation, recoveryOperations{
		removeLifecycle: func(*lifecycle.Handle) error { return injected },
	})
	if !errors.Is(recoverErr, injected) || result == nil || !result.Needed || result.Performed || !result.Published || result.Durable || !result.RecoveryRequired || result.Accepted == nil {
		t.Fatalf("first recovery = %#v, %v", result, recoverErr)
	}
	if _, err := os.Lstat(publicationPlanPath(stage)); err != nil {
		t.Fatalf("publication plan was removed before lifecycle authority: %v", err)
	}
	if _, _, err := lifecycle.Read(destination, lifecycle.Acquisition); err != nil {
		t.Fatalf("lifecycle was changed by refused removal: %v", err)
	}
	retried, err := RecoverExpected(context.Background(), destination, observation.Expectation)
	if err != nil || retried == nil || !retried.Needed || !retried.Performed || !retried.Published || !retried.Durable || retried.RecoveryRequired || retried.Accepted == nil {
		t.Fatalf("retried recovery = %#v, %v", retried, err)
	}
	if _, err := os.Lstat(stage); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stage remains after retry: %v", err)
	}
	assertNoAcquisitionState(t, destination)
}

func TestPublishedCloneRecoveryRejectsEquivalentTargetReplacement(t *testing.T) {
	location := bareFixture(t, false)
	destination := canonicalTestDestination(t, "published-identity")
	replacement := destination + "-replacement"
	displaced := destination + "-displaced"
	if result, err := Clone(context.Background(), location, Options{Destination: replacement, DestinationProvided: true}); err != nil || !result.Published {
		t.Fatalf("replacement clone = %#v, %v", result, err)
	}
	runFaultedClone(t, location, destination, PhasePublished, "")
	originalInfo, err := os.Lstat(destination)
	if err != nil {
		t.Fatal(err)
	}
	observation, err := lifecycle.ObserveRecovery(destination, lifecycle.Acquisition)
	if err != nil {
		t.Fatal(err)
	}
	verificationCalls := 0
	operations := recoveryOperations{verifyPublished: func(ctx context.Context, root string, plan publicationPlan) (*managedread.GitState, error) {
		verificationCalls++
		if verificationCalls == 2 {
			if err := os.Rename(root, displaced); err != nil {
				return nil, err
			}
			if err := os.Rename(replacement, root); err != nil {
				return nil, err
			}
		}
		return verifyPublished(ctx, root, plan)
	}}
	result, recoverErr := recover(context.Background(), destination, &observation.Expectation, operations)
	if KindOf(recoverErr) != ErrorConcurrency || result == nil || !result.Needed || result.Performed || !result.Published || result.Durable || !result.RecoveryRequired || result.Accepted == nil {
		t.Fatalf("recovery = %#v, %v", result, recoverErr)
	}
	currentInfo, err := os.Lstat(destination)
	if err != nil || os.SameFile(originalInfo, currentInfo) {
		t.Fatalf("replacement identity = %#v, %v", currentInfo, err)
	}
	displacedInfo, err := os.Lstat(displaced)
	if err != nil || !os.SameFile(originalInfo, displacedInfo) {
		t.Fatalf("displaced original = %#v, %v", displacedInfo, err)
	}
	if _, _, err := lifecycle.Read(destination, lifecycle.Acquisition); err != nil {
		t.Fatalf("lifecycle was removed after target replacement: %v", err)
	}
	cleanupAcquisitionArtifacts(t, destination)
}

func TestCloneRecoveryRefusesPublishedRepositoryWithoutExactPlan(t *testing.T) {
	location := bareFixture(t, false)
	destination := canonicalTestDestination(t, "published-without-plan")
	runFaultedClone(t, location, destination, PhasePublished, "")
	state, _, err := lifecycle.Read(destination, lifecycle.Acquisition)
	if err != nil {
		t.Fatal(err)
	}
	stage, err := lifecycle.Stage(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(stage); err != nil {
		t.Fatal(err)
	}
	observation, err := lifecycle.ObserveRecovery(destination, lifecycle.Acquisition)
	if err != nil {
		t.Fatal(err)
	}
	result, recoverErr := RecoverExpected(context.Background(), destination, observation.Expectation)
	if KindOf(recoverErr) != ErrorConflict || result == nil || !result.Needed || result.Performed || result.Published || result.Durable || result.CheckoutChanged || !result.RecoveryRequired || result.Accepted != nil {
		t.Fatalf("recovery = %#v, %v", result, recoverErr)
	}
	mutation, present := MutationOf(recoverErr)
	if !present || mutation.Durable || mutation.CheckoutChanged || !mutation.RecoveryRequired || mutation.Accepted != nil {
		t.Fatalf("mutation = %#v, %v", mutation, present)
	}
	if _, _, err := verifyPublishedStore(context.Background(), destination); err != nil {
		t.Fatalf("published checkout changed: %v", err)
	}
	if _, _, err := lifecycle.Read(destination, lifecycle.Acquisition); err != nil {
		t.Fatalf("ambiguous lifecycle was removed: %v", err)
	}
	cleanupAcquisitionArtifacts(t, destination)
}

func cleanupAcquisitionArtifacts(t *testing.T, destination string) {
	t.Helper()
	if state, _, err := lifecycle.Read(destination, lifecycle.Acquisition); err == nil {
		if stage, stageErr := lifecycle.Stage(state); stageErr == nil {
			if err := os.RemoveAll(stage); err != nil {
				t.Fatalf("clean acquisition stage: %v", err)
			}
		}
	}
	if err := os.Remove(lifecycle.Sidecar(destination, lifecycle.Acquisition)); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("clean acquisition lifecycle: %v", err)
	}
}

func TestCloneRecoveryFaultHelper(t *testing.T) {
	if os.Getenv("ENGRAM_ACQUIRE_FAULT_HELPER") != "1" {
		return
	}
	location := os.Getenv("ENGRAM_ACQUIRE_LOCATION")
	destination := os.Getenv("ENGRAM_ACQUIRE_DESTINATION")
	wanted := Phase(os.Getenv("ENGRAM_ACQUIRE_PHASE"))
	mode := os.Getenv("ENGRAM_ACQUIRE_MODE")
	reached := false
	cloner := &Cloner{Fault: func(phase Phase) error {
		if phase != wanted {
			return nil
		}
		reached = true
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
	if !reached {
		t.Fatalf("clone failed before requested phase %q: %v", wanted, err)
	}
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
	value := testpath.FileURL(bare)
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
