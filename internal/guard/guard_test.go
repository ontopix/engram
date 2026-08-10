package guard

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/ontopix/engram/internal/gitraw"
)

func TestInstallInspectAndRawCommitRejection(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	repository := newRepository(t)
	ctx := context.Background()
	state, err := Inspect(ctx, repository)
	if err != nil || state != Planned {
		t.Fatalf("initial inspect = %q, %v", state, err)
	}
	state, err = Install(ctx, repository)
	if err != nil || state != Installed {
		t.Fatalf("install = %q, %v", state, err)
	}
	state, err = Inspect(ctx, repository)
	if err != nil || state != Unchanged {
		t.Fatalf("final inspect = %q, %v", state, err)
	}
	hook, err := Path(ctx, repository)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(hook)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, Program()) {
		t.Fatal("installed bytes differ")
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(hook)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm()&0o111 == 0 {
			t.Fatal("guard is not executable")
		}
	}

	command := exec.Command("git", "-C", repository.Root, "commit", "--allow-empty", "-m", "raw")
	command.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.test", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.test")
	if output, err := command.CombinedOutput(); err == nil || !bytes.Contains(output, []byte("engram commit")) {
		t.Fatalf("raw commit = %v, %q", err, output)
	}
}

func TestInstallNeverOverwritesUnownedHook(t *testing.T) {
	repository := newRepository(t)
	hook, err := Path(context.Background(), repository)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(hook), 0o755); err != nil {
		t.Fatal(err)
	}
	original := []byte("#!/bin/sh\nexit 0\n")
	if err := os.WriteFile(hook, original, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(context.Background(), repository); !errors.Is(err, ErrConflict) {
		t.Fatalf("error = %v", err)
	}
	got, err := os.ReadFile(hook)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, original) {
		t.Fatal("unowned hook was overwritten")
	}
}

func TestConfiguredHooksPathIsConflict(t *testing.T) {
	repository := newRepository(t)
	runGit(t, repository.Root, "config", "core.hooksPath", "custom-hooks")
	if _, err := Inspect(context.Background(), repository); !errors.Is(err, ErrConflict) {
		t.Fatalf("error = %v", err)
	}
}

func TestEmptyConfiguredHooksPathIsConflict(t *testing.T) {
	repository := newRepository(t)
	runGit(t, repository.Root, "config", "core.hooksPath", "")
	if _, err := Inspect(context.Background(), repository); !errors.Is(err, ErrConflict) {
		t.Fatalf("error = %v", err)
	}
}

func TestIncludedHooksPathIsConflict(t *testing.T) {
	repository := newRepository(t)
	included := filepath.Join(repository.CommonGitDir, "engram-guard-include")
	if err := os.WriteFile(included, []byte("[core]\n\thooksPath = included-hooks\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, repository.Root, "config", "include.path", "engram-guard-include")
	if _, err := Inspect(context.Background(), repository); !errors.Is(err, ErrConflict) {
		t.Fatalf("error = %v", err)
	}
}

func TestWorktreeHooksPathIsConflict(t *testing.T) {
	repository := newRepository(t)
	runGit(t, repository.Root, "config", "extensions.worktreeConfig", "true")
	runGit(t, repository.Root, "config", "--worktree", "core.hooksPath", "worktree-hooks")
	if _, err := Inspect(context.Background(), repository); !errors.Is(err, ErrConflict) {
		t.Fatalf("error = %v", err)
	}
}

func TestGlobalAndEnvironmentConfigCannotInfluenceInspection(t *testing.T) {
	repository := newRepository(t)
	global := filepath.Join(t.TempDir(), "global.gitconfig")
	if err := os.WriteFile(global, []byte("[core]\n\thooksPath = global-hooks\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "core.hooksPath")
	t.Setenv("GIT_CONFIG_VALUE_0", "environment-hooks")
	if state, err := Inspect(context.Background(), repository); err != nil || state != Planned {
		t.Fatalf("inspect = %q, %v", state, err)
	}
}

func TestSymlinkedHooksAdministrationPathIsConflict(t *testing.T) {
	repository := newRepository(t)
	hooks := filepath.Join(repository.CommonGitDir, hooksDirectory)
	if err := os.RemoveAll(hooks); err != nil {
		t.Fatal(err)
	}
	external := t.TempDir()
	if err := os.Symlink(external, hooks); err != nil {
		t.Skipf("symbolic links unavailable: %v", err)
	}
	if _, err := Install(context.Background(), repository); !errors.Is(err, ErrConflict) {
		t.Fatalf("error = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(external, hookName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("external hook was touched: %v", err)
	}
}

func TestSymlinkedGitAdministrationPathIsConflict(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symbolic-link setup is not generally available on Windows")
	}
	repository := newRepository(t)
	gitDirectory := repository.CommonGitDir
	realDirectory := gitDirectory + "-real"
	if err := os.Rename(gitDirectory, realDirectory); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realDirectory, gitDirectory); err != nil {
		t.Fatal(err)
	}
	if _, err := Inspect(context.Background(), repository); !errors.Is(err, ErrConflict) {
		t.Fatalf("error = %v", err)
	}
}

func TestPinnedDirectoryIdentityRejectsRenameAndReplacement(t *testing.T) {
	parent := t.TempDir()
	target := filepath.Join(parent, "target")
	displaced := filepath.Join(parent, "displaced")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	original, err := pinnedFileInfo(os.Lstat(target))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(target, displaced); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	replacement, err := pinnedFileInfo(os.Lstat(target))
	if err != nil {
		t.Fatal(err)
	}
	displacedInfo, err := pinnedFileInfo(os.Lstat(displaced))
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(original, replacement) {
		t.Fatal("pinned Git administration identity followed the replacement path")
	}
	if !os.SameFile(original, displacedInfo) {
		t.Fatal("pinned Git administration identity no longer identifies the displaced directory")
	}
}

func TestOwnedNonExecutableHookIsRepairedOnlyByInstall(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("executable mode is not portable on Windows")
	}
	repository := newRepository(t)
	hook, err := Path(context.Background(), repository)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(hook), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hook, Program(), 0o600); err != nil {
		t.Fatal(err)
	}
	if state, err := Inspect(context.Background(), repository); err != nil || state != Planned {
		t.Fatalf("inspect = %q, %v", state, err)
	}
	if state, err := Install(context.Background(), repository); err != nil || state != Installed {
		t.Fatalf("install = %q, %v", state, err)
	}
}

func newRepository(t *testing.T) *gitraw.Repository {
	t.Helper()
	root := t.TempDir()
	runGit(t, root, "init", "--initial-branch=main")
	runGit(t, root, "-c", "user.name=Test", "-c", "user.email=test@example.test", "commit", "--allow-empty", "-m", "initial")
	repository, err := gitraw.Discover(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	return repository
}

func runGit(t *testing.T, directory string, arguments ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, arguments...)...)
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", arguments, err, output)
	}
}
