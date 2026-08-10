// Package gitpresent installs the byte-transparent local Git presentation
// shared by initialization and acquisition workflows.
package gitpresent

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
)

// Configure writes only the repository-local keys required by the managed
// Git presentation and installs the exact cache exclusion.
func Configure(ctx context.Context, root string) error {
	for _, pair := range [][2]string{
		{"core.autocrlf", "false"},
		{"core.sparseCheckout", "false"},
		{"core.sparseCheckoutCone", "false"},
		{"index.sparse", "false"},
	} {
		if _, status, err := gitOutput(ctx, root, "config", "--local", pair[0], pair[1]); err != nil || status != 0 {
			return errors.Join(err, fmt.Errorf("configure %s exited %d", pair[0], status))
		}
	}
	repositoryGitDir, status, err := gitOutput(ctx, root, "rev-parse", "--absolute-git-dir")
	if err != nil || status != 0 {
		return errors.Join(err, fmt.Errorf("resolve Git directory exited %d", status))
	}
	gitDir := strings.TrimSuffix(string(repositoryGitDir), "\n")
	if !filepath.IsAbs(gitDir) || filepath.Clean(gitDir) != gitDir {
		return errors.New("Git returned a non-canonical administration path")
	}
	return InstallCacheExclusion(gitDir)
}

// InstallCacheExclusion preserves existing real-file bytes and appends the
// one exact repository-local exclusion when absent.
func InstallCacheExclusion(gitDir string) error {
	name := filepath.Join(gitDir, "info", "exclude")
	if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
		return err
	}
	var content []byte
	info, err := os.Lstat(name)
	switch {
	case err == nil:
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return errors.New("Git exclude file is not a real regular file")
		}
		content, err = os.ReadFile(name)
		if err != nil {
			return err
		}
	case errors.Is(err, os.ErrNotExist):
	default:
		return err
	}
	if ExclusionPresent(content) {
		return nil
	}
	if len(content) != 0 && content[len(content)-1] != '\n' {
		content = append(content, '\n')
	}
	content = append(content, []byte(".engram/cache/\n")...)
	temporary, err := os.CreateTemp(filepath.Dir(name), ".engram-exclude-")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	remove := true
	defer func() {
		if remove {
			_ = os.Remove(temporaryName)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(content); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryName, name); err != nil {
		return err
	}
	remove = false
	return syncDirectory(filepath.Dir(name))
}

// HasCacheExclusion verifies the exact repository-local line.
func HasCacheExclusion(gitDir string) (bool, error) {
	content, err := os.ReadFile(filepath.Join(gitDir, "info", "exclude"))
	if err != nil {
		return false, err
	}
	return ExclusionPresent(content), nil
}

// ExclusionPresent evaluates an already captured exclude file.
func ExclusionPresent(content []byte) bool {
	for _, line := range bytes.Split(content, []byte("\n")) {
		if bytes.Equal(line, []byte(".engram/cache/")) {
			return true
		}
	}
	return false
}

func gitOutput(ctx context.Context, root string, arguments ...string) ([]byte, int, error) {
	git, err := exec.LookPath("git")
	if err != nil {
		return nil, -1, err
	}
	global := []string{
		"--no-pager", "--no-optional-locks", "--no-replace-objects",
		"-c", "core.hooksPath=" + os.DevNull, "-c", "core.fsmonitor=false", "-c", "core.untrackedCache=false",
		"-c", "maintenance.auto=false", "-c", "gc.auto=0", "-C", root,
	}
	command := exec.CommandContext(ctx, git, append(global, arguments...)...)
	command.Env = isolatedEnvironment(os.Environ())
	output, err := command.Output()
	if err == nil {
		return output, 0, nil
	}
	if ctx.Err() != nil {
		return nil, -1, ctx.Err()
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return output, exit.ExitCode(), nil
	}
	return nil, -1, err
}

func isolatedEnvironment(environment []string) []string {
	result := make([]string, 0, len(environment)+8)
	for _, item := range environment {
		name, _, _ := strings.Cut(item, "=")
		upper := strings.ToUpper(name)
		if strings.HasPrefix(upper, "GIT_") || strings.HasPrefix(upper, "ENGRAM_") {
			continue
		}
		result = append(result, item)
	}
	return append(result,
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_COUNT=0",
		"GIT_NO_LAZY_FETCH=1", "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0", "GIT_PAGER=cat",
	)
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
