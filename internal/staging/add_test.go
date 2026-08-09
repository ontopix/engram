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
