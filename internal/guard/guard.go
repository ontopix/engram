// Package guard installs and verifies the deliberately minimal raw-Git
// pre-commit guard owned by the reference CLI. The guard is guidance and a
// safety interlock, never a second preparation or acceptance engine.
package guard

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/ontopix/engram/internal/gitraw"
)

type State string

const (
	Planned   State = "planned"
	Installed State = "installed"
	Unchanged State = "unchanged"
)

// ErrConflict means the effective pre-commit hook is not owned byte-for-byte
// by this implementation and therefore cannot be overwritten or chained.
var ErrConflict = errors.New("unowned effective pre-commit hook")

var program = []byte(`#!/bin/sh
# engram managed-store guard v1
printf '%s\n' 'engram: this managed store accepts changes through ` + "`engram commit`" + `; raw git commit is not conforming.' >&2
exit 1
`)

// Program returns an independent copy of the owned launcher bytes.
func Program() []byte { return append([]byte(nil), program...) }

// Path resolves the effective default hook path. A repository-controlled
// core.hooksPath override is rejected because installing there could mutate
// an unrelated external integration or hide a pre-existing hook set.
func Path(ctx context.Context, repository *gitraw.Repository) (string, error) {
	if repository == nil {
		return "", fmt.Errorf("guard: nil repository")
	}
	git, err := exec.LookPath("git")
	if err != nil {
		return "", err
	}
	command := exec.CommandContext(ctx, git,
		"--no-pager", "--no-optional-locks", "--no-replace-objects",
		"-c", "core.hooksPath=", "-C", repository.Root,
		"config", "--local", "--get", "core.hooksPath")
	command.Env = isolatedEnvironment(os.Environ())
	output, err := command.Output()
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if err == nil && strings.TrimSuffix(string(output), "\n") != "" {
		return "", fmt.Errorf("%w: core.hooksPath is configured", ErrConflict)
	}
	var exitError *exec.ExitError
	if err != nil && (!errors.As(err, &exitError) || exitError.ExitCode() != 1) {
		return "", fmt.Errorf("inspect core.hooksPath: %w", err)
	}
	return filepath.Join(repository.CommonGitDir, "hooks", "pre-commit"), nil
}

// Inspect reports whether the effective hook is the exact owned executable.
// Absence is Planned; any other bytes or wrong kind are a conflict.
func Inspect(ctx context.Context, repository *gitraw.Repository) (State, error) {
	hook, err := Path(ctx, repository)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(hook)
	if errors.Is(err, os.ErrNotExist) {
		return Planned, nil
	}
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", ErrConflict
	}
	data, err := os.ReadFile(hook)
	if err != nil {
		return "", err
	}
	if !bytes.Equal(data, program) {
		return "", ErrConflict
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
		return Planned, nil
	}
	return Unchanged, nil
}

// Install atomically creates or repairs only the exact owned guard. It never
// replaces different bytes, a symbolic link, or another special kind.
func Install(ctx context.Context, repository *gitraw.Repository) (State, error) {
	state, err := Inspect(ctx, repository)
	if err != nil || state == Unchanged {
		return state, err
	}
	hook, err := Path(ctx, repository)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(hook), 0o700); err != nil {
		return "", err
	}
	if existing, statErr := os.Lstat(hook); statErr == nil {
		if existing.Mode()&os.ModeSymlink != 0 || !existing.Mode().IsRegular() {
			return "", ErrConflict
		}
		data, readErr := os.ReadFile(hook)
		if readErr != nil {
			return "", readErr
		}
		if !bytes.Equal(data, program) {
			return "", ErrConflict
		}
		if chmodErr := os.Chmod(hook, 0o700); chmodErr != nil {
			return "", chmodErr
		}
		return Installed, nil
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return "", statErr
	}

	temporary, err := os.CreateTemp(filepath.Dir(hook), ".engram-pre-commit-*")
	if err != nil {
		return "", err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0o700); err != nil {
		temporary.Close()
		return "", err
	}
	if _, err := temporary.Write(program); err != nil {
		temporary.Close()
		return "", err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return "", err
	}
	if err := temporary.Close(); err != nil {
		return "", err
	}
	if _, err := os.Lstat(hook); err == nil {
		return "", ErrConflict
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err := os.Rename(temporaryName, hook); err != nil {
		return "", err
	}
	if err := syncDirectory(filepath.Dir(hook)); err != nil {
		return "", err
	}
	return Installed, nil
}

func syncDirectory(name string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	directory, err := os.Open(name)
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}

func isolatedEnvironment(environment []string) []string {
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
		"GIT_NO_LAZY_FETCH=1",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_OPTIONAL_LOCKS=0",
		"GIT_PAGER=cat",
	)
}
