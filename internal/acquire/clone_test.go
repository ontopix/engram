package acquire

import (
	"context"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ontopix/engram/internal/guard"
	"github.com/ontopix/engram/internal/managedread"
)

func TestClonePublishesOnlyVerifiedManagedStore(t *testing.T) {
	location := bareFixture(t, false)
	destination := filepath.Join(t.TempDir(), "memory")
	result, err := Clone(context.Background(), location, Options{Destination: destination, DestinationProvided: true})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Published || result.Reused || result.Root != destination || result.Remote != "origin" || result.VerifiedCommits != 1 || result.Launcher != guard.Installed || result.Validation.HasErrors() {
		t.Fatalf("result = %#v", result)
	}
	store, err := managedread.Open(context.Background(), destination)
	if err != nil {
		t.Fatal(err)
	}
	if state, err := guard.Inspect(context.Background(), store.Repository()); err != nil || state != guard.Unchanged {
		t.Fatalf("guard = %q, %v", state, err)
	}
	if ok, err := hasCacheExclusion(store.Repository().GitDir); err != nil || !ok {
		t.Fatalf("cache exclusion = %v, %v", ok, err)
	}
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
