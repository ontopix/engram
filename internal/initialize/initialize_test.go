package initialize

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ontopix/engram/internal/changeset"
	"github.com/ontopix/engram/internal/checker"
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

func TestRecoverInitializationAtPublicationBoundaries(t *testing.T) {
	t.Run("accepted private store is discarded before publication", func(t *testing.T) {
		root := filepath.Join(canonicalTemp(t), "memory")
		initializer := newInitializer(t)
		initializer.Fault = failAt(PhaseCleanupRequired)
		_, err := initializer.Run(t.Context(), root, Options{Identity: testIdentity()})
		assertRecoveryError(t, err)
		makeLifecycleOwnerDead(t, root)
		recovered, err := Recover(t.Context(), root)
		if err != nil || !recovered.Needed || !recovered.Performed || recovered.Accepted != nil {
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
		if err != nil || !recovered.Performed || recovered.Accepted != nil {
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
		if err != nil || !recovered.Performed || recovered.Accepted == nil || recovered.Accepted.Commit == nil || *recovered.Accepted.Commit != *result.Accepted.Commit {
			t.Fatalf("recovery = %#v, %v", recovered, err)
		}
		if got := gitOutput(t, root, "rev-parse", "HEAD"); got != *result.Accepted.Commit {
			t.Fatalf("published HEAD = %q", got)
		}
		assertNoLifecycle(t, root)
	})
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
	matches, err := filepath.Glob(target + ".engram-initialization-v1-*.stage")
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
