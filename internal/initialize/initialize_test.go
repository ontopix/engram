package initialize

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ontopix/engram/internal/changeset"
	"github.com/ontopix/engram/internal/checker"
	"github.com/ontopix/engram/internal/fileidentity"
	"github.com/ontopix/engram/internal/fsatomic"
	"github.com/ontopix/engram/internal/guard"
	"github.com/ontopix/engram/internal/hookexec"
	"github.com/ontopix/engram/internal/hooks"
	"github.com/ontopix/engram/internal/lifecycle"
	"github.com/ontopix/engram/internal/managedread"
	"github.com/ontopix/engram/internal/managedwrite"
)

func TestDryRunPlansAbsentTargetWithoutMutation(t *testing.T) {
	root := filepath.Join(canonicalTemp(t), "memory")
	result, err := newInitializer(t).Run(t.Context(), root, Options{Schemas: []string{"person", "person"}, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if !result.DryRun || result.Root != root || result.Accepted.Ref == nil || *result.Accepted.Ref != "refs/heads/main" || result.Accepted.Commit != nil || result.Launcher != guard.Planned || result.Validation.Status != checker.StatusComplete || result.Validation.HasErrors() || len(result.Files) != 4 {
		t.Fatalf("result = %#v", result)
	}
	if _, err := os.Lstat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dry-run created target: %v", err)
	}
	assertNoLifecycle(t, root)
}

func TestInitializeAbsentTargetPublishesOneVerifiedRootCommit(t *testing.T) {
	root := filepath.Join(canonicalTemp(t), "memory")
	result, err := newInitializer(t).Run(t.Context(), root, Options{Identity: testIdentity(), Schemas: []string{"person"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.DryRun || result.Accepted.Commit == nil || result.Accepted.Ref == nil || *result.Accepted.Ref != "refs/heads/main" || result.Launcher != guard.Installed || result.Validation.HasErrors() {
		t.Fatalf("result = %#v", result)
	}
	if got := gitOutput(t, root, "rev-list", "--count", "HEAD"); got != "1" {
		t.Fatalf("commit count = %q", got)
	}
	if got := gitOutput(t, root, "rev-list", "--parents", "-n", "1", "HEAD"); got != *result.Accepted.Commit {
		t.Fatalf("root commit line = %q", got)
	}
	if got := gitOutput(t, root, "symbolic-ref", "HEAD"); got != "refs/heads/main" {
		t.Fatalf("HEAD ref = %q", got)
	}
	store, err := managedread.Open(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	audit, err := store.AuditAccepted(t.Context())
	if err != nil || audit.Validation.HasErrors() || audit.Tip != *result.Accepted.Commit {
		t.Fatalf("audit = %#v, %v", audit, err)
	}
	assertNoLifecycle(t, root)
}

func TestInitializeExistingTargetPreservesUnrelatedAndPrunedBytes(t *testing.T) {
	root := filepath.Join(canonicalTemp(t), "memory")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.CopyFS(root, os.DirFS(filepath.Join(repositoryRoot(t), "examples", "minimal"))); err != nil {
		t.Fatal(err)
	}
	hidden := filepath.Join(root, ".private")
	cache := filepath.Join(root, ".engram", "cache", "opaque")
	if err := os.WriteFile(hidden, []byte("keep hidden\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(cache), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cache, []byte{0, 1, 2, 3}, 0o600); err != nil {
		t.Fatal(err)
	}
	readmeBefore, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil {
		t.Fatal(err)
	}

	result, err := newInitializer(t).Run(t.Context(), root, Options{Identity: testIdentity(), Schemas: []string{"person"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Accepted.Commit == nil || !hasChange(result.Files, ".engram/schemas/person.md") {
		t.Fatalf("result = %#v", result)
	}
	assertBytes(t, filepath.Join(root, "README.md"), readmeBefore)
	assertBytes(t, hidden, []byte("keep hidden\n"))
	assertBytes(t, cache, []byte{0, 1, 2, 3})
	tracked := gitOutput(t, root, "ls-tree", "-r", "--name-only", "HEAD")
	if strings.Contains(tracked, ".private") || strings.Contains(tracked, ".engram/cache/") || !strings.Contains(tracked, ".engram/schemas/person.md") {
		t.Fatalf("tracked files = %q", tracked)
	}
	assertNoLifecycle(t, root)
}

func TestInitializeRejectionAndInvalidIdentityLeaveTargetUnchanged(t *testing.T) {
	for _, test := range []struct {
		name     string
		identity *Identity
		content  []byte
	}{
		{name: "invalid-candidate", identity: testIdentity(), content: []byte("broken\n")},
		{name: "invalid-identity", identity: &Identity{}, content: nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := filepath.Join(canonicalTemp(t), "memory")
			if err := os.Mkdir(root, 0o755); err != nil {
				t.Fatal(err)
			}
			if test.content != nil {
				if err := os.WriteFile(filepath.Join(root, "README.md"), test.content, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			before, err := os.ReadDir(root)
			if err != nil {
				t.Fatal(err)
			}
			result, runErr := newInitializer(t).Run(t.Context(), root, Options{Identity: test.identity})
			if test.name == "invalid-candidate" {
				if runErr != nil || !result.Validation.HasErrors() {
					t.Fatalf("result, err = %#v, %v", result, runErr)
				}
			} else if KindOf(runErr) != ErrorUsage {
				t.Fatalf("error = %v", runErr)
			}
			after, err := os.ReadDir(root)
			if err != nil || len(after) != len(before) {
				t.Fatalf("target changed: before=%v after=%v err=%v", before, after, err)
			}
			if _, err := os.Lstat(filepath.Join(root, ".git")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("repository was published: %v", err)
			}
			assertNoLifecycle(t, root)
		})
	}
}

func TestInitializeBeginAndPrePublicationCleanupCarryExactMutationEvidence(t *testing.T) {
	t.Run("begin left closed lifecycle", func(t *testing.T) {
		root := filepath.Join(canonicalTemp(t), "memory")
		injected := errors.New("injected begin tail failure")
		initializer := newInitializer(t)
		initializer.beginLifecycle = func(target string, operation lifecycle.Operation) (initializationLifecycle, error) {
			if _, err := lifecycle.Begin(target, operation); err != nil {
				return nil, err
			}
			return nil, injected
		}
		_, err := initializer.Run(t.Context(), root, Options{Identity: testIdentity()})
		mutation, present := MutationOf(err)
		if !errors.Is(err, injected) || !present || mutation.Durable || mutation.Commit != nil || mutation.CheckoutChanged || !mutation.RecoveryRequired {
			t.Fatalf("error = %v, mutation = %#v, present = %t", err, mutation, present)
		}
		if _, _, stateErr := lifecycle.Read(root, lifecycle.Initialization); stateErr != nil {
			t.Fatalf("closed lifecycle was not retained: %v", stateErr)
		}
	})

	t.Run("operation and cleanup errors are aggregated", func(t *testing.T) {
		root := filepath.Join(canonicalTemp(t), "memory")
		operationErr := errors.New("injected operation failure")
		cleanupErr := errors.New("injected cleanup failure")
		initializer := newInitializer(t)
		initializer.Fault = func(phase Phase) error {
			if phase == PhaseLifecycleBegun {
				return operationErr
			}
			return nil
		}
		initializer.cleanupStage = func(string) error { return cleanupErr }
		_, err := initializer.Run(t.Context(), root, Options{Identity: testIdentity()})
		mutation, present := MutationOf(err)
		if !errors.Is(err, operationErr) || !errors.Is(err, cleanupErr) || !present || !mutation.Durable || mutation.Commit != nil || mutation.CheckoutChanged || !mutation.RecoveryRequired {
			t.Fatalf("error = %v, mutation = %#v, present = %t", err, mutation, present)
		}
		if _, _, stateErr := lifecycle.Read(root, lifecycle.Initialization); stateErr != nil {
			t.Fatalf("cleanup failure lost lifecycle state: %v", stateErr)
		}
	})

	t.Run("silent lifecycle residual prevents success", func(t *testing.T) {
		root := filepath.Join(canonicalTemp(t), "memory")
		injected := errors.New("injected operation failure")
		initializer := newInitializer(t)
		initializer.beginLifecycle = func(target string, operation lifecycle.Operation) (initializationLifecycle, error) {
			handle, err := lifecycle.Begin(target, operation)
			if err != nil {
				return nil, err
			}
			return silentResidualInitializationLifecycle{Handle: handle}, nil
		}
		initializer.Fault = func(phase Phase) error {
			if phase == PhaseLifecycleBegun {
				return injected
			}
			return nil
		}
		_, err := initializer.Run(t.Context(), root, Options{Identity: testIdentity()})
		mutation, present := MutationOf(err)
		if !errors.Is(err, injected) || !present || !mutation.Durable || mutation.CheckoutChanged || !mutation.RecoveryRequired {
			t.Fatalf("error = %v, mutation = %#v, present = %t", err, mutation, present)
		}
		if _, _, stateErr := lifecycle.Read(root, lifecycle.Initialization); stateErr != nil {
			t.Fatalf("owned lifecycle residual missing: %v", stateErr)
		}
	})
}

type silentResidualInitializationLifecycle struct{ *lifecycle.Handle }

func (silentResidualInitializationLifecycle) Remove() error { return nil }

func TestInitializationMutationOfUsesFinalRecoverySnapshot(t *testing.T) {
	commit := "0123456789012345678901234567890123456789"
	first := &Error{MutationKnown: true, Commit: &commit, CheckoutChanged: true, RecoveryRequired: true, Underlying: errors.New("first")}
	last := &Error{MutationKnown: true, Durable: true, Underlying: errors.New("last")}
	mutation, present := MutationOf(errors.Join(first, last))
	if !present || !mutation.Durable || mutation.Commit == nil || *mutation.Commit != commit || !mutation.CheckoutChanged || mutation.RecoveryRequired {
		t.Fatalf("joined mutation = %#v, present = %t", mutation, present)
	}
	outer := &Error{MutationKnown: true, Underlying: first}
	mutation, present = MutationOf(outer)
	if !present || mutation.Durable || mutation.Commit == nil || *mutation.Commit != commit || !mutation.CheckoutChanged || mutation.RecoveryRequired {
		t.Fatalf("outer mutation = %#v, present = %t", mutation, present)
	}
}

func TestInitializeFaultAfterStageCleanupHasPublishedEffectsAndRecovery(t *testing.T) {
	root := filepath.Join(canonicalTemp(t), "memory")
	initializer := newInitializer(t)
	initializer.Fault = failAt(PhaseStageCleaned)
	result, err := initializer.Run(t.Context(), root, Options{Identity: testIdentity()})
	mutation, present := MutationOf(err)
	if !present || !mutation.Durable || mutation.Commit == nil || result.Accepted.Commit == nil || *mutation.Commit != *result.Accepted.Commit || !mutation.CheckoutChanged || !mutation.RecoveryRequired {
		t.Fatalf("result = %#v, error = %v, mutation = %#v", result, err, mutation)
	}
	state, _, stateErr := lifecycle.Read(root, lifecycle.Initialization)
	if stateErr != nil {
		t.Fatalf("lifecycle missing: %v", stateErr)
	}
	stage, stageErr := lifecycle.Stage(state)
	if stageErr != nil {
		t.Fatal(stageErr)
	}
	if _, statErr := os.Lstat(stage); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("private stage remains after cleanup boundary: %v", statErr)
	}
}

func TestInitializePublicationFailuresCarryExactCheckoutEvidence(t *testing.T) {
	t.Run("existing target file publication sync", func(t *testing.T) {
		root := existingPortableTarget(t)
		injected := errors.New("injected file publication sync failure")
		initializer := newInitializer(t)
		initializer.syncPath = func(name string) error {
			if name == filepath.Join(root, ".engram", "schemas") {
				return injected
			}
			return syncDirectory(name)
		}
		result, err := initializer.Run(t.Context(), root, Options{Identity: testIdentity(), Schemas: []string{"person"}})
		assertInitializationMutation(t, result, err, injected, true)
		if _, statErr := os.Lstat(filepath.Join(root, ".engram", "schemas", "person.md")); statErr != nil {
			t.Fatalf("published schema missing: %v", statErr)
		}
		if _, statErr := os.Lstat(filepath.Join(root, ".git")); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("repository should not yet be published: %v", statErr)
		}
	})

	t.Run("existing target repository rename then sync", func(t *testing.T) {
		root := existingPortableTarget(t)
		injected := errors.New("injected repository publication sync failure")
		initializer := newInitializer(t)
		initializer.syncPath = func(name string) error {
			if name == root {
				return injected
			}
			return syncDirectory(name)
		}
		result, err := initializer.Run(t.Context(), root, Options{Identity: testIdentity(), Schemas: []string{"person"}})
		assertInitializationMutation(t, result, err, injected, true)
		if _, statErr := os.Lstat(filepath.Join(root, ".git")); statErr != nil {
			t.Fatalf("renamed repository missing: %v", statErr)
		}
	})

	t.Run("absent target rename failure", func(t *testing.T) {
		root := filepath.Join(canonicalTemp(t), "memory")
		injected := errors.New("injected target rename failure")
		initializer := newInitializer(t)
		initializer.renamePath = func(string, string) error { return injected }
		result, err := initializer.Run(t.Context(), root, Options{Identity: testIdentity()})
		assertInitializationMutation(t, result, err, injected, false)
		if _, statErr := os.Lstat(root); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("failed rename published target: %v", statErr)
		}
	})

	t.Run("absent target refuses a late empty directory", func(t *testing.T) {
		root := filepath.Join(canonicalTemp(t), "memory")
		initializer := newInitializer(t)
		var foreign os.FileInfo
		var publicationErr error
		initializer.renamePath = func(oldPath, newPath string) error {
			if err := os.Mkdir(newPath, 0o755); err != nil {
				return err
			}
			foreign, _ = os.Lstat(newPath)
			_, publicationErr = fsatomic.RenameNoReplace(oldPath, newPath)
			return publicationErr
		}
		result, err := initializer.Run(t.Context(), root, Options{Identity: testIdentity()})
		assertInitializationMutation(t, result, err, publicationErr, false)
		after, statErr := os.Lstat(root)
		if statErr != nil || foreign == nil || !os.SameFile(foreign, after) || !after.IsDir() {
			t.Fatalf("late target identity was replaced: before=%#v after=%#v err=%v", foreign, after, statErr)
		}
		entries, readErr := os.ReadDir(root)
		if readErr != nil || len(entries) != 0 {
			t.Fatalf("late target was not preserved as an empty directory: entries=%v err=%v", entries, readErr)
		}
	})

	t.Run("existing target refuses late Git administration", func(t *testing.T) {
		root := existingPortableTarget(t)
		initializer := newInitializer(t)
		var foreign os.FileInfo
		var publicationErr error
		initializer.renamePath = func(oldPath, newPath string) error {
			if err := os.Mkdir(newPath, 0o755); err != nil {
				return err
			}
			foreign, _ = os.Lstat(newPath)
			_, publicationErr = fsatomic.RenameNoReplace(oldPath, newPath)
			return publicationErr
		}
		result, err := initializer.Run(t.Context(), root, Options{Identity: testIdentity()})
		assertInitializationMutation(t, result, err, publicationErr, false)
		gitPath := filepath.Join(root, ".git")
		after, statErr := os.Lstat(gitPath)
		if statErr != nil || foreign == nil || !os.SameFile(foreign, after) || !after.IsDir() {
			t.Fatalf("late Git administration identity was replaced: before=%#v after=%#v err=%v", foreign, after, statErr)
		}
		entries, readErr := os.ReadDir(gitPath)
		if readErr != nil || len(entries) != 0 {
			t.Fatalf("late Git administration was not preserved: entries=%v err=%v", entries, readErr)
		}
	})

	t.Run("concurrent file before first publication", func(t *testing.T) {
		root := existingPortableTarget(t)
		injectedPath := filepath.Join(root, ".engram", "schemas", "person.md")
		initializer := newInitializer(t)
		initializer.Fault = func(phase Phase) error {
			if phase == PhaseCleanupRequired {
				return os.WriteFile(injectedPath, []byte("foreign\n"), 0o600)
			}
			return nil
		}
		result, err := initializer.Run(t.Context(), root, Options{Identity: testIdentity(), Schemas: []string{"person"}})
		if err == nil {
			t.Fatal("concurrent directory was accepted")
		}
		mutation, present := MutationOf(err)
		if !present || !mutation.Durable || mutation.Commit == nil || result.Accepted.Commit == nil || *mutation.Commit != *result.Accepted.Commit || mutation.CheckoutChanged || !mutation.RecoveryRequired {
			t.Fatalf("result = %#v, error = %v, mutation = %#v, present = %t", result, err, mutation, present)
		}
		assertBytes(t, injectedPath, []byte("foreign\n"))
	})
}

func TestRecoverInitializationAtPublicationBoundaries(t *testing.T) {
	t.Run("accepted private store is discarded before publication", func(t *testing.T) {
		root := filepath.Join(canonicalTemp(t), "memory")
		initializer := newInitializer(t)
		initializer.Fault = failAt(PhaseCleanupRequired)
		_, err := initializer.Run(t.Context(), root, Options{Identity: testIdentity()})
		assertRecoveryError(t, err)
		makeLifecycleOwnerDead(t, root)
		recovered, err := Recover(t.Context(), root)
		if err != nil || !recovered.Needed || !recovered.Performed || !recovered.Durable || recovered.CheckoutChanged || recovered.RecoveryRequired || recovered.Accepted != nil {
			t.Fatalf("recovery = %#v, %v", recovered, err)
		}
		if _, err := os.Lstat(root); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("unpublished target remains: %v", err)
		}
		assertNoLifecycle(t, root)
	})

	t.Run("known additions are rolled back before Git publication", func(t *testing.T) {
		root := filepath.Join(canonicalTemp(t), "memory")
		if err := os.Mkdir(root, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.CopyFS(root, os.DirFS(filepath.Join(repositoryRoot(t), "examples", "minimal"))); err != nil {
			t.Fatal(err)
		}
		before, err := captureTree(root)
		if err != nil {
			t.Fatal(err)
		}
		initializer := newInitializer(t)
		initializer.Fault = failAt(PhaseFilesPublished)
		_, err = initializer.Run(t.Context(), root, Options{Identity: testIdentity(), Schemas: []string{"person"}})
		assertRecoveryError(t, err)
		makeLifecycleOwnerDead(t, root)
		recovered, err := Recover(t.Context(), root)
		if err != nil || !recovered.Needed || !recovered.Performed || !recovered.Durable || !recovered.CheckoutChanged || recovered.RecoveryRequired || recovered.Accepted != nil {
			t.Fatalf("recovery = %#v, %v", recovered, err)
		}
		after, err := captureTree(root)
		if err != nil || !equalTree(before, after) {
			t.Fatalf("target not restored: before=%#v after=%#v err=%v", before, after, err)
		}
		assertNoLifecycle(t, root)
	})

	t.Run("published accepted store is verified and retained", func(t *testing.T) {
		root := filepath.Join(canonicalTemp(t), "memory")
		initializer := newInitializer(t)
		initializer.Fault = failAt(PhaseRepositoryPublished)
		result, err := initializer.Run(t.Context(), root, Options{Identity: testIdentity()})
		assertRecoveryError(t, err)
		if result.Accepted.Commit == nil {
			t.Fatalf("fault result = %#v", result)
		}
		makeLifecycleOwnerDead(t, root)
		recovered, err := Recover(t.Context(), root)
		if err != nil || !recovered.Needed || !recovered.Performed || !recovered.Durable || recovered.CheckoutChanged || recovered.RecoveryRequired || recovered.Accepted == nil || recovered.Accepted.Commit == nil || *recovered.Accepted.Commit != *result.Accepted.Commit {
			t.Fatalf("recovery = %#v, %v", recovered, err)
		}
		if got := gitOutput(t, root, "rev-parse", "HEAD"); got != *result.Accepted.Commit {
			t.Fatalf("published HEAD = %q", got)
		}
		assertNoLifecycle(t, root)
	})
}

func TestRecoverInitializationRollbackRejectsTargetReplacement(t *testing.T) {
	root := existingPortableTarget(t)
	initializer := newInitializer(t)
	initializer.Fault = failAt(PhaseFilesPublished)
	if _, err := initializer.Run(t.Context(), root, Options{Identity: testIdentity(), Schemas: []string{"person"}}); err == nil {
		t.Fatal("initialization fault did not retain recovery state")
	}
	makeLifecycleOwnerDead(t, root)
	state, _, err := lifecycle.Read(root, lifecycle.Initialization)
	if err != nil {
		t.Fatal(err)
	}
	stage, err := lifecycle.Stage(state)
	if err != nil {
		t.Fatal(err)
	}
	planRaw, err := os.ReadFile(filepath.Join(stage, "plan-v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	record, err := decodePublicationPlan(planRaw, state)
	if err != nil {
		t.Fatal(err)
	}
	if len(record.Files) == 0 {
		t.Fatal("test setup has no published files to protect")
	}
	displaced := root + ".approved"
	marker := filepath.Join(root, ".foreign-replacement")
	recovered, recoverErr := recoverWithOperations(t.Context(), root, nil, recoveryOperations{
		beforeRollback: func(name string) error {
			if err := os.Rename(name, displaced); err != nil {
				return err
			}
			if err := os.Mkdir(name, 0o755); err != nil {
				return err
			}
			for _, planned := range record.Files {
				path := filepath.Join(name, filepath.FromSlash(planned.Path))
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					return err
				}
				if err := os.WriteFile(path, planned.Data, 0o644); err != nil {
					return err
				}
			}
			return os.WriteFile(marker, []byte("preserve replacement exactly\n"), 0o600)
		},
	})
	if KindOf(recoverErr) != ErrorConcurrency || !errors.Is(recoverErr, lifecycle.ErrChanged) ||
		!recovered.Needed || recovered.Performed || recovered.Durable || recovered.CheckoutChanged || !recovered.RecoveryRequired {
		t.Fatalf("replacement recovery = %#v, %v", recovered, recoverErr)
	}
	assertBytes(t, marker, []byte("preserve replacement exactly\n"))
	for _, planned := range record.Files {
		assertBytes(t, filepath.Join(root, filepath.FromSlash(planned.Path)), planned.Data)
	}
	if _, _, err := lifecycle.Read(root, lifecycle.Initialization); err != nil {
		t.Fatalf("lifecycle was removed: %v", err)
	}
	if _, err := os.Lstat(stage); err != nil {
		t.Fatalf("stage was removed: %v", err)
	}
	if _, err := os.Lstat(displaced); err != nil {
		t.Fatalf("approved target was lost: %v", err)
	}
}

func TestRecoverInitializationRollbackSyncUsesAnchoredRoot(t *testing.T) {
	root := existingPortableTarget(t)
	initializer := newInitializer(t)
	initializer.Fault = failAt(PhaseFilesPublished)
	_, err := initializer.Run(t.Context(), root, Options{Identity: testIdentity(), Schemas: []string{"person"}})
	assertRecoveryError(t, err)
	makeLifecycleOwnerDead(t, root)
	state, _, err := lifecycle.Read(root, lifecycle.Initialization)
	if err != nil {
		t.Fatal(err)
	}
	stage, err := lifecycle.Stage(state)
	if err != nil {
		t.Fatal(err)
	}
	displaced := root + ".rolled-back"
	marker := filepath.Join(root, ".foreign-replacement")
	seamCalled := false
	recovered, recoverErr := recoverWithOperations(t.Context(), root, nil, recoveryOperations{
		beforeRollbackSync: func(name string) error {
			seamCalled = true
			if err := os.Rename(name, displaced); err != nil {
				return err
			}
			if err := os.Mkdir(name, 0o755); err != nil {
				return err
			}
			return os.WriteFile(marker, []byte("preserve replacement exactly\n"), 0o600)
		},
	})
	var typedError *Error
	if !seamCalled || KindOf(recoverErr) != ErrorConcurrency || !errors.Is(recoverErr, lifecycle.ErrChanged) ||
		!errors.As(recoverErr, &typedError) || typedError.Operation != "revalidate initialization recovery plan" ||
		!recovered.Needed || recovered.Performed || !recovered.Durable || !recovered.CheckoutChanged || !recovered.RecoveryRequired {
		t.Fatalf("anchored rollback recovery = %#v, error = %v, typed = %#v, seam = %t", recovered, recoverErr, typedError, seamCalled)
	}
	assertBytes(t, marker, []byte("preserve replacement exactly\n"))
	if _, err := os.Lstat(filepath.Join(displaced, ".engram", "schemas", "person.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("approved target was not rolled back: %v", err)
	}
	if _, _, err := lifecycle.Read(root, lifecycle.Initialization); err != nil {
		t.Fatalf("lifecycle was removed: %v", err)
	}
	if _, err := os.Lstat(stage); err != nil {
		t.Fatalf("stage was removed: %v", err)
	}
}

func TestRecoverInitializationRollbackSyncRejectsDirtyParentReplacement(t *testing.T) {
	root := existingPortableTarget(t)
	initializer := newInitializer(t)
	initializer.Fault = failAt(PhaseFilesPublished)
	_, err := initializer.Run(t.Context(), root, Options{Identity: testIdentity(), Schemas: []string{"person"}})
	assertRecoveryError(t, err)
	makeLifecycleOwnerDead(t, root)
	state, _, err := lifecycle.Read(root, lifecycle.Initialization)
	if err != nil {
		t.Fatal(err)
	}
	stage, err := lifecycle.Stage(state)
	if err != nil {
		t.Fatal(err)
	}
	dirtyParent := filepath.Join(root, ".engram", "schemas")
	displacedParent := filepath.Join(root, ".engram", "approved-schemas")
	marker := filepath.Join(dirtyParent, "foreign.md")
	seamCalls := 0
	recovered, recoverErr := recoverWithOperations(t.Context(), root, nil, recoveryOperations{
		beforeRollbackSync: func(string) error {
			seamCalls++
			if err := os.Rename(dirtyParent, displacedParent); err != nil {
				return err
			}
			if err := os.Mkdir(dirtyParent, 0o755); err != nil {
				return err
			}
			return os.WriteFile(marker, []byte("preserve replacement exactly\n"), 0o600)
		},
	})
	var typedError *Error
	if seamCalls != 1 || KindOf(recoverErr) != ErrorConcurrency || !errors.Is(recoverErr, lifecycle.ErrChanged) ||
		!errors.As(recoverErr, &typedError) || typedError.Operation != "roll back initialization publication" ||
		!recovered.Needed || recovered.Performed || !recovered.Durable || !recovered.CheckoutChanged || !recovered.RecoveryRequired {
		t.Fatalf("dirty-parent recovery = %#v, error = %v, typed = %#v, seam calls = %d", recovered, recoverErr, typedError, seamCalls)
	}
	assertBytes(t, marker, []byte("preserve replacement exactly\n"))
	if _, err := os.Lstat(filepath.Join(displacedParent, "person.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("approved dirty parent was not rolled back: %v", err)
	}
	if _, _, err := lifecycle.Read(root, lifecycle.Initialization); err != nil {
		t.Fatalf("lifecycle was removed: %v", err)
	}
	if _, err := os.Lstat(stage); err != nil {
		t.Fatalf("stage was removed: %v", err)
	}
}

func TestOpenRollbackParentRejectsReplacementShape(t *testing.T) {
	for _, replacement := range []string{"file", "symlink"} {
		t.Run(replacement, func(t *testing.T) {
			if replacement == "symlink" && runtime.GOOS == "windows" {
				t.Skip("symlink creation is not portable on Windows")
			}
			parent := canonicalTemp(t)
			name := filepath.Join(parent, "dirty")
			displaced := filepath.Join(parent, "approved-dirty")
			if err := os.Mkdir(name, 0o755); err != nil {
				t.Fatal(err)
			}
			root, err := os.OpenRoot(parent)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			if err := os.Rename(name, displaced); err != nil {
				t.Fatal(err)
			}
			switch replacement {
			case "file":
				if err := os.WriteFile(name, []byte("preserve replacement exactly\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink(displaced, name); err != nil {
					t.Fatal(err)
				}
			}
			opened, _, err := openRollbackParent(root, "dirty")
			if opened != nil {
				_ = opened.Close()
			}
			if !errors.Is(err, lifecycle.ErrChanged) {
				t.Fatalf("replacement sync = %v", err)
			}
			info, err := os.Lstat(name)
			if err != nil {
				t.Fatal(err)
			}
			if replacement == "file" {
				assertBytes(t, name, []byte("preserve replacement exactly\n"))
			} else if info.Mode()&os.ModeSymlink == 0 {
				t.Fatalf("symlink replacement changed shape: %v", info.Mode())
			}
		})
	}
}

func TestPublishedInitializationRecoveryRequiresExactTargetIdentity(t *testing.T) {
	parent := canonicalTemp(t)
	target := filepath.Join(parent, "memory")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	before, err := os.Lstat(target)
	if err != nil {
		t.Fatal(err)
	}
	if err := fileidentity.Pin(before); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(target, filepath.Join(parent, "replaced-memory")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := requireSameRealDirectory(before, target); err == nil {
		t.Fatal("semantically equivalent target replacement retained recovery authority")
	}
}

func TestRecoverInitializationLeaseExcludesConcurrentMutator(t *testing.T) {
	root := filepath.Join(canonicalTemp(t), "memory")
	initializer := newInitializer(t)
	initializer.Fault = failAt(PhaseCleanupRequired)
	if _, err := initializer.Run(t.Context(), root, Options{Identity: testIdentity()}); err == nil {
		t.Fatal("initialization fault did not retain recovery state")
	}
	makeLifecycleOwnerDead(t, root)
	stateBefore, rawBefore, err := lifecycle.Read(root, lifecycle.Initialization)
	if err != nil {
		t.Fatal(err)
	}
	stage, err := lifecycle.Stage(stateBefore)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := lifecycle.AcquireRecovery(root, lifecycle.Initialization)
	if err != nil {
		t.Fatal(err)
	}
	recovered, recoverErr := Recover(t.Context(), root)
	if KindOf(recoverErr) != ErrorConcurrency || !recovered.Needed || recovered.Performed || recovered.Durable || recovered.CheckoutChanged || !recovered.RecoveryRequired {
		t.Fatalf("contended recovery = %#v, %v", recovered, recoverErr)
	}
	stateAfter, rawAfter, err := lifecycle.Read(root, lifecycle.Initialization)
	if err != nil || stateAfter != stateBefore || !bytes.Equal(rawAfter, rawBefore) {
		t.Fatalf("contended lifecycle changed: before=%#v after=%#v err=%v", stateBefore, stateAfter, err)
	}
	if _, err := os.Lstat(stage); err != nil {
		t.Fatalf("contended stage changed: %v", err)
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
	recovered, err = Recover(t.Context(), root)
	if err != nil || !recovered.Performed || !recovered.Durable || recovered.CheckoutChanged || recovered.RecoveryRequired {
		t.Fatalf("recovery after lease release = %#v, %v", recovered, err)
	}
}

func TestRecoverInitializationWithoutStateIsNoOp(t *testing.T) {
	root := filepath.Join(canonicalTemp(t), "memory")
	recovered, err := Recover(t.Context(), root)
	if err != nil || recovered != (RecoveryResult{}) {
		t.Fatalf("recovery = %#v, %v", recovered, err)
	}
	if _, err := os.Lstat(lifecycle.Sidecar(root, lifecycle.Initialization) + ".lease"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("no-op recovery created a lease: %v", err)
	}
	expected := lifecycle.RecoveryExpectation{OwnerToken: strings.Repeat("0", 64), StateSHA256: strings.Repeat("0", 64)}
	recovered, err = RecoverExpected(t.Context(), root, expected)
	if KindOf(err) != ErrorConcurrency || !errors.Is(err, lifecycle.ErrChanged) || !recovered.Needed || recovered.Performed || recovered.RecoveryRequired {
		t.Fatalf("expected recovery = %#v, %v", recovered, err)
	}
	if _, err := os.Lstat(lifecycle.Sidecar(root, lifecycle.Initialization) + ".lease"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing expected recovery created a lease: %v", err)
	}
}

func TestRecoverInitializationConfirmsConcurrentDisappearanceUnderLease(t *testing.T) {
	for _, phase := range []string{"stable-read", "before-adopt"} {
		for _, expected := range []bool{false, true} {
			name := phase + "/unbound"
			if expected {
				name = phase + "/expected"
			}
			t.Run(name, func(t *testing.T) {
				root := filepath.Join(canonicalTemp(t), "memory")
				handle, err := lifecycle.Begin(root, lifecycle.Initialization)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = handle.Remove() })
				observation, err := lifecycle.ObserveRecovery(root, lifecycle.Initialization)
				if err != nil {
					t.Fatal(err)
				}

				operations := recoveryOperations{}
				sidecar := lifecycle.Sidecar(root, lifecycle.Initialization)
				switch phase {
				case "stable-read":
					operations.readLifecycle = func(string, lifecycle.Operation) (lifecycle.State, []byte, error) {
						if err := os.Remove(sidecar); err != nil {
							return lifecycle.State{}, nil, err
						}
						return lifecycle.State{}, nil, errors.Join(lifecycle.ErrChanged, errors.New("sidecar disappeared during stable read"))
					}
				case "before-adopt":
					operations.acquireRecovery = func(target string, operation lifecycle.Operation) (*lifecycle.RecoveryLease, error) {
						lease, err := lifecycle.AcquireRecovery(target, operation)
						if err != nil {
							return nil, err
						}
						if err := os.Remove(sidecar); err != nil {
							return nil, errors.Join(err, lease.Release())
						}
						return lease, nil
					}
				}

				var recovered RecoveryResult
				var recoverErr error
				if expected {
					recovered, recoverErr = recoverWithOperations(t.Context(), root, &observation.Expectation, operations)
				} else {
					recovered, recoverErr = recoverWithOperations(t.Context(), root, nil, operations)
				}
				if expected {
					if KindOf(recoverErr) != ErrorConcurrency || !errors.Is(recoverErr, lifecycle.ErrChanged) || !recovered.Needed || recovered.Performed || recovered.RecoveryRequired {
						t.Fatalf("expected recovery = %#v, %v", recovered, recoverErr)
					}
				} else if recoverErr != nil || recovered != (RecoveryResult{}) {
					t.Fatalf("unbound recovery = %#v, %v", recovered, recoverErr)
				}
				if _, err := os.Lstat(sidecar + ".lease"); err != nil {
					t.Fatalf("disappearance was not confirmed under a recovery lease: %v", err)
				}
			})
		}
	}
}

func TestRecoverInitializationReadFailuresRemainFailClosed(t *testing.T) {
	root := filepath.Join(canonicalTemp(t), "changed-while-adopting")
	if lifecycleAbsentAtAdopt(root, lifecycle.Initialization, errors.Join(lifecycle.ErrChanged, os.ErrNotExist)) {
		t.Fatal("non-cooperating disappearance was flattened into idempotent success")
	}
	for _, test := range []struct {
		name string
		err  error
	}{
		{name: "malformed", err: lifecycle.ErrMalformed},
		{name: "io", err: errors.New("injected lifecycle read refusal")},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := filepath.Join(canonicalTemp(t), "memory")
			recovered, recoverErr := recoverWithOperations(t.Context(), root, nil, recoveryOperations{
				readLifecycle: func(string, lifecycle.Operation) (lifecycle.State, []byte, error) {
					return lifecycle.State{}, nil, test.err
				},
			})
			if KindOf(recoverErr) != ErrorRecovery || !errors.Is(recoverErr, test.err) || !recovered.Needed || recovered.Performed || recovered.RecoveryRequired {
				t.Fatalf("recovery = %#v, %v", recovered, recoverErr)
			}
			if _, err := os.Lstat(lifecycle.Sidecar(root, lifecycle.Initialization) + ".lease"); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("failed read acquired a recovery lease: %v", err)
			}
		})
	}
}

func TestRecoverInitializationDoesNotFlattenLifecycleReplacement(t *testing.T) {
	root := filepath.Join(canonicalTemp(t), "memory")
	original, err := lifecycle.Begin(root, lifecycle.Initialization)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = original.Remove() })
	var replacement *lifecycle.Handle
	recovered, recoverErr := recoverWithOperations(t.Context(), root, nil, recoveryOperations{
		readLifecycle: func(string, lifecycle.Operation) (lifecycle.State, []byte, error) {
			if err := os.Remove(lifecycle.Sidecar(root, lifecycle.Initialization)); err != nil {
				return lifecycle.State{}, nil, err
			}
			var err error
			replacement, err = lifecycle.Begin(root, lifecycle.Initialization)
			if err != nil {
				return lifecycle.State{}, nil, err
			}
			return lifecycle.State{}, nil, lifecycle.ErrChanged
		},
	})
	if replacement != nil {
		t.Cleanup(func() { _ = replacement.Remove() })
	}
	if KindOf(recoverErr) != ErrorConcurrency || !recovered.Needed || recovered.Performed || !recovered.RecoveryRequired {
		t.Fatalf("replacement recovery = %#v, %v", recovered, recoverErr)
	}
	state, _, err := lifecycle.Read(root, lifecycle.Initialization)
	if err != nil || replacement == nil || state.Owner.Token != replacement.State().Owner.Token {
		t.Fatalf("replacement lifecycle = %#v, %v", state, err)
	}
}

func TestRecoverExpectedRejectsChangedInitializationPlanWithoutTouchingTarget(t *testing.T) {
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
			root := filepath.Join(canonicalTemp(t), "memory")
			if err := os.Mkdir(root, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.CopyFS(root, os.DirFS(filepath.Join(repositoryRoot(t), "examples", "minimal"))); err != nil {
				t.Fatal(err)
			}
			initializer := newInitializer(t)
			initializer.Fault = failAt(PhaseFilesPublished)
			_, err := initializer.Run(t.Context(), root, Options{Identity: testIdentity(), Schemas: []string{"person"}})
			assertRecoveryError(t, err)
			makeLifecycleOwnerDead(t, root)
			state, _, err := lifecycle.Read(root, lifecycle.Initialization)
			if err != nil {
				t.Fatal(err)
			}
			stage, err := lifecycle.Stage(state)
			if err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(root, ".foreign")
			if err := os.WriteFile(marker, []byte("preserve exactly\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			before, err := captureTree(root)
			if err != nil {
				t.Fatal(err)
			}
			observation, err := lifecycle.ObserveRecovery(root, lifecycle.Initialization)
			if err != nil {
				t.Fatal(err)
			}
			test.mutate(t, filepath.Join(stage, "plan-v1.json"))
			recovered, recoverErr := RecoverExpected(t.Context(), root, observation.Expectation)
			if KindOf(recoverErr) != ErrorConcurrency || !recovered.Needed || !recovered.RecoveryRequired {
				t.Fatalf("recovery = %#v, %v", recovered, recoverErr)
			}
			after, err := captureTree(root)
			if err != nil || !equalTree(before, after) {
				t.Fatalf("target changed: before=%#v after=%#v err=%v", before, after, err)
			}
			if _, _, err := lifecycle.Read(root, lifecycle.Initialization); err != nil {
				t.Fatalf("lifecycle was removed: %v", err)
			}
			if _, err := os.Lstat(stage); err != nil {
				t.Fatalf("stage was removed: %v", err)
			}
		})
	}
}

func TestRecoverExpectedInitializationSidecarLastPreservesUnrelatedBytes(t *testing.T) {
	root := filepath.Join(canonicalTemp(t), "memory")
	initializer := newInitializer(t)
	initializer.Fault = failAt(PhaseRepositoryPublished)
	result, err := initializer.Run(t.Context(), root, Options{Identity: testIdentity()})
	assertRecoveryError(t, err)
	makeLifecycleOwnerDead(t, root)
	state, _, err := lifecycle.Read(root, lifecycle.Initialization)
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
	marker := filepath.Join(root, ".foreign")
	if err := os.WriteFile(marker, []byte("preserve exactly\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	observation, err := lifecycle.ObserveRecovery(root, lifecycle.Initialization)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := RecoverExpected(t.Context(), root, observation.Expectation)
	if err != nil || !recovered.Needed || !recovered.Performed || !recovered.Durable || recovered.CheckoutChanged || recovered.RecoveryRequired || recovered.Accepted == nil || result.Accepted.Commit == nil || recovered.Accepted.Commit == nil || *recovered.Accepted.Commit != *result.Accepted.Commit {
		t.Fatalf("recovery = %#v, %v", recovered, err)
	}
	assertBytes(t, marker, []byte("preserve exactly\n"))
	assertNoLifecycle(t, root)
}

func TestInitializeRejectsConcurrentExistingTargetChangeBeforePublication(t *testing.T) {
	root := filepath.Join(canonicalTemp(t), "memory")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.CopyFS(root, os.DirFS(filepath.Join(repositoryRoot(t), "examples", "minimal"))); err != nil {
		t.Fatal(err)
	}
	initializer := newInitializer(t)
	initializer.Fault = func(phase Phase) error {
		if phase == PhaseAccepted {
			return os.WriteFile(filepath.Join(root, ".concurrent"), []byte("outside\n"), 0o600)
		}
		return nil
	}
	_, err := initializer.Run(t.Context(), root, Options{Identity: testIdentity()})
	if KindOf(err) != ErrorConcurrency {
		t.Fatalf("error = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(root, ".git")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("repository published after concurrency: %v", err)
	}
	assertBytes(t, filepath.Join(root, ".concurrent"), []byte("outside\n"))
	assertNoLifecycle(t, root)
}

func newInitializer(t *testing.T) *Initializer {
	t.Helper()
	registry, err := hooks.NewRegistry(filepath.Join(canonicalTemp(t), "trust-v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	return New(managedwrite.New(hookexec.New(registry)))
}

func existingPortableTarget(t *testing.T) string {
	t.Helper()
	root := filepath.Join(canonicalTemp(t), "memory")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.CopyFS(root, os.DirFS(filepath.Join(repositoryRoot(t), "examples", "minimal"))); err != nil {
		t.Fatal(err)
	}
	return root
}

func assertInitializationMutation(t *testing.T, result Result, err, cause error, checkoutChanged bool) {
	t.Helper()
	mutation, present := MutationOf(err)
	if !errors.Is(err, cause) || !present || !mutation.Durable || mutation.Commit == nil || result.Accepted.Commit == nil ||
		*mutation.Commit != *result.Accepted.Commit || mutation.CheckoutChanged != checkoutChanged || !mutation.RecoveryRequired {
		t.Fatalf("result = %#v, error = %v, mutation = %#v, present = %t", result, err, mutation, present)
	}
}

func testIdentity() *Identity { return &Identity{Name: "Engram Test", Email: "engram@example.test"} }

func failAt(want Phase) func(Phase) error {
	return func(got Phase) error {
		if got == want {
			return errors.New("injected failure")
		}
		return nil
	}
}

func assertRecoveryError(t *testing.T, err error) {
	t.Helper()
	var typedError *Error
	if !errors.As(err, &typedError) || !typedError.RecoveryRequired || !typedError.Durable {
		t.Fatalf("error = %#v", err)
	}
}

func makeLifecycleOwnerDead(t *testing.T, target string) {
	t.Helper()
	state, _, err := lifecycle.Read(target, lifecycle.Initialization)
	if err != nil {
		t.Fatal(err)
	}
	state.Owner.PID = 1<<30 - 1
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(state); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lifecycle.Sidecar(target, lifecycle.Initialization), output.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertNoLifecycle(t *testing.T, target string) {
	t.Helper()
	if _, err := os.Lstat(lifecycle.Sidecar(target, lifecycle.Initialization)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lifecycle remains: %v", err)
	}
	matches, err := filepath.Glob(filepath.Join(filepath.Dir(target), ".engram-stage-v1-*.stage"))
	if err != nil || len(matches) != 0 {
		t.Fatalf("staging remains: %v, %v", matches, err)
	}
}

func canonicalTemp(t *testing.T) string {
	t.Helper()
	value, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	working, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Clean(filepath.Join(working, "..", ".."))
}

func gitOutput(t *testing.T, root string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, arguments...)...)
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull, "LC_ALL=C")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", arguments, err, output)
	}
	return strings.TrimSuffix(string(output), "\n")
}

func assertBytes(t *testing.T, name string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(name)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("%s = %q, %v; want %q", name, got, err, want)
	}
}

type tree map[string][]byte

func captureTree(root string) (tree, error) {
	result := make(tree)
	err := filepath.WalkDir(root, func(name string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if name == root || entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(root, name)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		result[filepath.ToSlash(relative)] = data
		return nil
	})
	return result, err
}

func equalTree(left, right tree) bool {
	if len(left) != len(right) {
		return false
	}
	for name, data := range left {
		if !bytes.Equal(data, right[name]) {
			return false
		}
	}
	return true
}

func hasChange(changes []changeset.Change, name string) bool {
	for _, change := range changes {
		if change.Path == name {
			return true
		}
	}
	return false
}
