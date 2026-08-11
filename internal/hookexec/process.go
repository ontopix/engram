package hookexec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/ontopix/engram/internal/hookprotocol"
	"github.com/ontopix/engram/internal/hooks"
	"github.com/ontopix/engram/internal/treeimage"
)

type boundedBuffer struct {
	limit     int
	data      []byte
	truncated bool
}

func (b *boundedBuffer) Write(value []byte) (int, error) {
	originalLength := len(value)
	remaining := b.limit - len(b.data)
	if remaining > 0 {
		if len(value) > remaining {
			value = value[:remaining]
		}
		b.data = append(b.data, value...)
	}
	if originalLength > remaining {
		b.truncated = true
	}
	return originalLength, nil
}

func (e *Executor) invoke(ctx context.Context, hook hooks.Hook, input []byte, baseRoot, candidateRoot string) (Diagnostic, error) {
	diagnostic := Diagnostic{Hook: hook.Path}
	interpreter, err := hookprotocol.Interpreter(hook.Bytes)
	if err != nil || interpreter != hook.Interpreter {
		return diagnostic, typed(ErrorCapability, hook.Path, nil, nil, fmt.Errorf("%w: selected interpreter is inconsistent", ErrCapability))
	}
	lookPath := e.LookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	executable, err := lookPath(interpreter)
	if err != nil {
		return diagnostic, typed(ErrorCapability, hook.Path, nil, nil, fmt.Errorf("%w: interpreter %q: %v", ErrCapability, interpreter, err))
	}
	if !filepath.IsAbs(executable) {
		executable, err = filepath.Abs(executable)
		if err != nil {
			return diagnostic, typed(ErrorCapability, hook.Path, nil, nil, fmt.Errorf("%w: resolve interpreter: %v", ErrCapability, err))
		}
	}

	hookHostPath := filepath.Join(baseRoot, filepath.FromSlash(hook.Path))
	hookHostPath, err = filepath.Abs(hookHostPath)
	if err != nil {
		return diagnostic, typed(ErrorCapability, hook.Path, nil, nil, fmt.Errorf("%w: resolve selected hook: %v", ErrCapability, err))
	}
	if err := verifySelectedProgram(hookHostPath, hook.Bytes); err != nil {
		return diagnostic, typed(ErrorConcurrency, hook.Path, nil, nil, fmt.Errorf("%w: %v", ErrConcurrent, err))
	}

	inherited := e.Environment
	if inherited == nil {
		inherited = os.Environ()
	}
	environment, err := hookprotocol.Environment(inherited, baseRoot, candidateRoot, runtime.GOOS == "windows")
	if err != nil {
		return diagnostic, typed(ErrorCapability, hook.Path, nil, nil, fmt.Errorf("%w: environment: %v", ErrCapability, err))
	}
	limit := e.DiagnosticLimit
	if limit <= 0 {
		limit = DefaultDiagnosticLimit
	}
	stdout := &boundedBuffer{limit: limit}
	stderr := &boundedBuffer{limit: limit}
	timeout := e.HookTimeout
	if timeout <= 0 {
		timeout = DefaultHookTimeout
	}
	hookContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	command := exec.CommandContext(hookContext, executable, hookHostPath)
	command.Dir = candidateRoot
	command.Env = environment
	command.Stdin = bytes.NewReader(input)
	command.Stdout = stdout
	command.Stderr = stderr
	// Bound pipe-drain waits when a hook leaves descendants holding inherited
	// stdout/stderr descriptors after the main process exits.
	command.WaitDelay = DefaultProcessWaitDelay
	err = command.Run()
	diagnostic.Stdout = string(stdout.data)
	diagnostic.Stderr = string(stderr.data)
	diagnostic.StdoutTruncated = stdout.truncated
	diagnostic.StderrTruncated = stderr.truncated
	if err != nil {
		cause := err
		if hookContext.Err() != nil {
			cause = errors.Join(err, hookContext.Err())
		}
		return diagnostic, typed(ErrorHook, hook.Path, &diagnostic, nil, fmt.Errorf("%w: process failed: %v", ErrRejected, cause))
	}
	return diagnostic, nil
}

func verifySelectedProgram(name string, selected []byte) error {
	before, err := os.Lstat(name)
	if err != nil {
		return err
	}
	if before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() {
		return fmt.Errorf("selected hook is not a real regular file")
	}
	content, err := os.ReadFile(name)
	if err != nil {
		return err
	}
	after, err := os.Lstat(name)
	if err != nil || after.Mode()&os.ModeSymlink != 0 || !after.Mode().IsRegular() || !os.SameFile(before, after) {
		return fmt.Errorf("selected hook changed while being read")
	}
	if !bytes.Equal(content, selected) {
		return fmt.Errorf("materialized hook differs from selected base bytes")
	}
	return nil
}

func (e *Executor) temporaryDirectory(worktreeRoot, prefix string) (string, error) {
	base := e.TempRoot
	if base == "" {
		base = os.TempDir()
	}
	canonicalBase, err := realDirectory(base)
	if err != nil {
		return "", err
	}
	if err := outsideWorktrees(canonicalBase, worktreeRoot); err != nil {
		return "", err
	}
	directory, err := os.MkdirTemp(canonicalBase, prefix)
	if err != nil {
		return "", err
	}
	if err := outsideWorktrees(directory, worktreeRoot); err != nil {
		_ = cleanupTree(directory)
		return "", err
	}
	return directory, nil
}

func realDirectory(name string) (string, error) {
	absolute, err := filepath.Abs(name)
	if err != nil {
		return "", err
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(canonical)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", fmt.Errorf("path is not a real directory")
	}
	return filepath.Clean(canonical), nil
}

func outsideWorktrees(candidate, liveWorktree string) error {
	candidate, err := filepath.Abs(candidate)
	if err != nil {
		return err
	}
	if liveWorktree != "" {
		liveWorktree, err = realDirectory(liveWorktree)
		if err != nil {
			return err
		}
		inside, err := pathWithin(liveWorktree, candidate)
		if err != nil {
			return err
		}
		if inside {
			return fmt.Errorf("disposable tree would be inside the live worktree")
		}
	}
	for cursor := filepath.Clean(candidate); ; cursor = filepath.Dir(cursor) {
		gitMarker := filepath.Join(cursor, ".git")
		if _, err := os.Lstat(gitMarker); err == nil {
			return fmt.Errorf("disposable tree would be inside another Git worktree")
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		parent := filepath.Dir(cursor)
		if parent == cursor {
			break
		}
	}
	return nil
}

func pathWithin(parent, child string) (bool, error) {
	relative, err := filepath.Rel(parent, child)
	if err != nil {
		return false, err
	}
	return relative == "." || relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)), nil
}

func cleanupTree(root string) error {
	if root == "" {
		return nil
	}
	absolute, err := filepath.Abs(root)
	if err != nil || absolute == string(filepath.Separator) || filepath.Clean(absolute) != absolute {
		return fmt.Errorf("refuse unsafe temporary cleanup target %q", root)
	}
	if _, err := os.Lstat(absolute); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	_ = os.Chmod(absolute, 0o700)
	_ = filepath.WalkDir(absolute, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if entry.IsDir() {
			return os.Chmod(name, 0o700)
		}
		return os.Chmod(name, 0o600)
	})
	return os.RemoveAll(absolute)
}

func verifyImageRoot(root string, expected treeimage.Image) error {
	observed, err := treeimage.Capture(root, true)
	if err != nil {
		return err
	}
	if !treeimage.Equal(observed, expected) {
		return fmt.Errorf("materialized tree differs from immutable image")
	}
	return nil
}
