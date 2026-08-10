// Package guard installs and verifies the deliberately minimal raw-Git
// pre-commit guard owned by the reference CLI. The guard is guidance and a
// safety interlock, never a second preparation or acceptance engine.
package guard

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/ontopix/engram/internal/fileidentity"
	"github.com/ontopix/engram/internal/gitraw"
)

type State string

const (
	Planned   State = "planned"
	Installed State = "installed"
	Unchanged State = "unchanged"
)

const (
	hooksDirectory = "hooks"
	hookName       = "pre-commit"
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

// Path resolves the effective default hook path. Repository-local and
// worktree configuration (including files they include) are inspected in
// isolation from environment, global, and system configuration. Any
// repository-controlled core.hooksPath entry is rejected, including an empty
// entry, because it changes the path away from Git's default hook directory.
func Path(ctx context.Context, repository *gitraw.Repository) (string, error) {
	if repository == nil {
		return "", fmt.Errorf("guard: nil repository")
	}
	if err := rejectHooksPathOverride(ctx, repository); err != nil {
		return "", err
	}
	if _, err := validateDirectoryChain(repository.CommonGitDir); err != nil {
		return "", err
	}
	hooks := filepath.Join(repository.CommonGitDir, hooksDirectory)
	if info, err := pinnedFileInfo(os.Lstat(hooks)); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return "", conflict("the Git hooks administration path is not a real directory")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	return filepath.Join(hooks, hookName), nil
}

// Inspect reports whether the effective hook is the exact owned executable.
// Absence is Planned; any other bytes or wrong kind are a conflict.
func Inspect(ctx context.Context, repository *gitraw.Repository) (State, error) {
	location, err := openLocation(ctx, repository, false)
	if err != nil {
		return "", err
	}
	defer location.close()

	state := Planned
	if location.hooks != nil {
		state, _, err = inspectHook(location.hooks)
		if err != nil {
			return "", err
		}
	}
	if err := location.verify(); err != nil {
		return "", err
	}
	if err := rejectHooksPathOverride(ctx, repository); err != nil {
		return "", err
	}
	return state, nil
}

// Install creates or repairs only the exact owned guard. New publication uses
// a hard-link no-replace step, so a hook that appears concurrently is never
// overwritten. Every published file and directory entry is flushed before
// success is reported.
func Install(ctx context.Context, repository *gitraw.Repository) (State, error) {
	location, err := openLocation(ctx, repository, true)
	if err != nil {
		return "", err
	}
	defer location.close()

	state, present, err := inspectHook(location.hooks)
	if err != nil {
		return "", err
	}
	if state == Unchanged {
		if err := finish(ctx, repository, location); err != nil {
			return "", err
		}
		return Unchanged, nil
	}

	if present {
		if err := repairOwnedHook(location); err != nil {
			return "", err
		}
		state = Installed
	} else {
		state, err = installAbsentHook(location)
		if err != nil {
			return "", err
		}
	}
	if err := finish(ctx, repository, location); err != nil {
		return "", err
	}
	return state, nil
}

type location struct {
	commonPath string
	commonInfo os.FileInfo
	common     *os.Root
	hooksInfo  os.FileInfo
	hooks      *os.Root
}

func openLocation(ctx context.Context, repository *gitraw.Repository, create bool) (*location, error) {
	if _, err := Path(ctx, repository); err != nil {
		return nil, err
	}
	commonInfo, err := pinnedFileInfo(os.Lstat(repository.CommonGitDir))
	if err != nil {
		return nil, err
	}
	common, err := os.OpenRoot(repository.CommonGitDir)
	if err != nil {
		return nil, err
	}
	result := &location{commonPath: repository.CommonGitDir, commonInfo: commonInfo, common: common}
	openedInfo, err := pinnedFileInfo(common.Stat("."))
	if err != nil || !openedInfo.IsDir() || !os.SameFile(commonInfo, openedInfo) {
		result.close()
		if err != nil {
			return nil, err
		}
		return nil, conflict("the Git administration directory changed while it was opened")
	}

	hooksInfo, err := pinnedFileInfo(common.Lstat(hooksDirectory))
	if errors.Is(err, os.ErrNotExist) && create {
		mkdirErr := common.Mkdir(hooksDirectory, 0o700)
		if mkdirErr != nil && !errors.Is(mkdirErr, os.ErrExist) {
			result.close()
			return nil, mkdirErr
		}
		if mkdirErr == nil {
			if err := syncRoot(common); err != nil {
				result.close()
				return nil, err
			}
		}
		hooksInfo, err = pinnedFileInfo(common.Lstat(hooksDirectory))
	}
	if errors.Is(err, os.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		result.close()
		return nil, err
	}
	if hooksInfo.Mode()&os.ModeSymlink != 0 || !hooksInfo.IsDir() {
		result.close()
		return nil, conflict("the Git hooks administration path is not a real directory")
	}
	hooks, err := common.OpenRoot(hooksDirectory)
	if err != nil {
		result.close()
		return nil, err
	}
	openedHooksInfo, err := pinnedFileInfo(hooks.Stat("."))
	if err != nil || !openedHooksInfo.IsDir() || !os.SameFile(hooksInfo, openedHooksInfo) {
		hooks.Close()
		result.close()
		if err != nil {
			return nil, err
		}
		return nil, conflict("the Git hooks administration directory changed while it was opened")
	}
	result.hooksInfo = hooksInfo
	result.hooks = hooks
	if err := result.verify(); err != nil {
		result.close()
		return nil, err
	}
	return result, nil
}

func (l *location) close() {
	if l == nil {
		return
	}
	if l.hooks != nil {
		_ = l.hooks.Close()
	}
	if l.common != nil {
		_ = l.common.Close()
	}
}

func (l *location) verify() error {
	commonInfo, err := validateDirectoryChain(l.commonPath)
	if err != nil {
		return err
	}
	if !os.SameFile(l.commonInfo, commonInfo) {
		return conflict("the Git administration directory changed")
	}
	if l.hooks == nil {
		if _, err := pinnedFileInfo(l.common.Lstat(hooksDirectory)); errors.Is(err, os.ErrNotExist) {
			return nil
		} else if err != nil {
			return err
		}
		return conflict("the Git hooks administration path appeared concurrently")
	}
	hooksInfo, err := pinnedFileInfo(l.common.Lstat(hooksDirectory))
	if err != nil {
		return err
	}
	if hooksInfo.Mode()&os.ModeSymlink != 0 || !hooksInfo.IsDir() || !os.SameFile(l.hooksInfo, hooksInfo) {
		return conflict("the Git hooks administration directory changed")
	}
	return nil
}

func inspectHook(root *os.Root) (State, bool, error) {
	info, err := pinnedFileInfo(root.Lstat(hookName))
	if errors.Is(err, os.ErrNotExist) {
		return Planned, false, nil
	}
	if err != nil {
		return "", false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", true, ErrConflict
	}
	file, err := root.Open(hookName)
	if err != nil {
		return "", true, err
	}
	defer file.Close()
	openedInfo, err := pinnedFileInfo(file.Stat())
	if err != nil {
		return "", true, err
	}
	if !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) {
		return "", true, ErrConflict
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return "", true, err
	}
	if !bytes.Equal(data, program) {
		return "", true, ErrConflict
	}
	namedInfo, err := pinnedFileInfo(root.Lstat(hookName))
	if err != nil || namedInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(openedInfo, namedInfo) {
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", true, err
		}
		return "", true, ErrConflict
	}
	if runtime.GOOS != "windows" && openedInfo.Mode().Perm()&0o111 == 0 {
		return Planned, true, nil
	}
	return Unchanged, true, nil
}

func repairOwnedHook(location *location) error {
	if err := location.verify(); err != nil {
		return err
	}
	info, err := pinnedFileInfo(location.hooks.Lstat(hookName))
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return ErrConflict
	}
	file, err := location.hooks.Open(hookName)
	if err != nil {
		return err
	}
	defer file.Close()
	openedInfo, err := pinnedFileInfo(file.Stat())
	if err != nil || !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) {
		if err != nil {
			return err
		}
		return ErrConflict
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return err
	}
	if !bytes.Equal(data, program) {
		return ErrConflict
	}
	if err := file.Chmod(0o700); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	namedInfo, err := pinnedFileInfo(location.hooks.Lstat(hookName))
	if err != nil || namedInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(openedInfo, namedInfo) {
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return ErrConflict
	}
	return location.verify()
}

func installAbsentHook(location *location) (State, error) {
	if err := location.verify(); err != nil {
		return "", err
	}
	temporaryName, temporary, err := createTemporary(location.hooks)
	if err != nil {
		return "", err
	}
	temporaryPresent := true
	defer func() {
		if temporaryPresent {
			_ = location.hooks.Remove(temporaryName)
		}
	}()
	if err := temporary.Chmod(0o700); err != nil {
		temporary.Close()
		return "", err
	}
	if written, err := temporary.Write(program); err != nil || written != len(program) {
		temporary.Close()
		if err != nil {
			return "", err
		}
		return "", io.ErrShortWrite
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return "", err
	}
	if err := temporary.Close(); err != nil {
		return "", err
	}
	if err := location.verify(); err != nil {
		return "", err
	}
	if err := location.hooks.Link(temporaryName, hookName); err != nil {
		if errors.Is(err, os.ErrExist) {
			state, _, inspectErr := inspectHook(location.hooks)
			if inspectErr == nil && state == Unchanged {
				return Unchanged, nil
			}
			return "", ErrConflict
		}
		return "", err
	}
	if err := syncRoot(location.hooks); err != nil {
		return "", err
	}
	if err := location.hooks.Remove(temporaryName); err != nil {
		return "", err
	}
	temporaryPresent = false
	if err := syncRoot(location.hooks); err != nil {
		return "", err
	}
	return Installed, nil
}

func createTemporary(root *os.Root) (string, *os.File, error) {
	for range 16 {
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			return "", nil, err
		}
		name := ".engram-pre-commit-" + hex.EncodeToString(random[:])
		file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o700)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		return name, file, err
	}
	return "", nil, fmt.Errorf("guard: cannot allocate a private temporary hook")
}

func finish(ctx context.Context, repository *gitraw.Repository, location *location) error {
	if err := location.verify(); err != nil {
		return err
	}
	state, _, err := inspectHook(location.hooks)
	if err != nil {
		return err
	}
	if state != Unchanged {
		return ErrConflict
	}
	return rejectHooksPathOverride(ctx, repository)
}

func rejectHooksPathOverride(ctx context.Context, repository *gitraw.Repository) error {
	git, err := exec.LookPath("git")
	if err != nil {
		return err
	}
	if _, present, err := queryConfig(ctx, git, repository.Root, "--local", "--includes", "--get-all", "core.hooksPath"); err != nil {
		return fmt.Errorf("inspect local core.hooksPath: %w", err)
	} else if present {
		return conflict("core.hooksPath is configured in repository-controlled configuration")
	}

	// Asking Git for --worktree configuration is itself a fatal error in a
	// linked worktree unless extensions.worktreeConfig is enabled in the common
	// repository config. Establish that local switch first; an absent or false
	// switch means config.worktree is not an effective source.
	value, present, err := queryConfig(ctx, git, repository.Root,
		"--local", "--includes", "--type=bool", "--get", "extensions.worktreeConfig")
	if err != nil {
		return fmt.Errorf("inspect extensions.worktreeConfig: %w", err)
	}
	if !present || string(bytes.TrimSpace(value)) != "true" {
		return nil
	}
	if _, present, err := queryConfig(ctx, git, repository.Root, "--worktree", "--includes", "--get-all", "core.hooksPath"); err != nil {
		return fmt.Errorf("inspect worktree core.hooksPath: %w", err)
	} else if present {
		return conflict("core.hooksPath is configured in repository-controlled configuration")
	}
	return nil
}

func queryConfig(ctx context.Context, git, root string, arguments ...string) ([]byte, bool, error) {
	command := exec.CommandContext(ctx, git,
		append([]string{"-c", "core.longpaths=true", "--no-pager", "--no-optional-locks", "--no-replace-objects", "-C", root, "config"}, arguments...)...)
	command.Env = isolatedEnvironment(os.Environ())
	output, err := command.Output()
	if ctx.Err() != nil {
		return nil, false, ctx.Err()
	}
	if err == nil {
		return output, true, nil
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) && exitError.ExitCode() == 1 {
		return nil, false, nil
	}
	return nil, false, err
}

func validateDirectoryChain(name string) (os.FileInfo, error) {
	if !filepath.IsAbs(name) || filepath.Clean(name) != name {
		return nil, conflict("the Git administration directory is not a clean absolute path")
	}
	chain := []string{name}
	for current := name; ; {
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		chain = append(chain, parent)
		current = parent
	}
	var final os.FileInfo
	for index := len(chain) - 1; index >= 0; index-- {
		info, err := pinnedFileInfo(os.Lstat(chain[index]))
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return nil, conflict("the Git administration path contains a symbolic link or non-directory ancestor")
		}
		if index == 0 {
			final = info
		}
	}
	return final, nil
}

func pinnedFileInfo(info os.FileInfo, err error) (os.FileInfo, error) {
	if err != nil {
		return nil, err
	}
	if err := fileidentity.Pin(info); err != nil {
		return nil, err
	}
	return info, nil
}

func conflict(detail string) error {
	return fmt.Errorf("%w: %s", ErrConflict, detail)
}

func syncRoot(root *os.Root) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	directory, err := root.Open(".")
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
