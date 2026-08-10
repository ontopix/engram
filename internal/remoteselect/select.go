// Package remoteselect resolves the closed remote/branch configuration used
// by pull and push without initiating network access.
package remoteselect

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"unicode/utf8"

	"github.com/ontopix/engram/internal/transport"
)

type Direction string

const (
	Fetch Direction = "fetch"
	Push  Direction = "push"
)

type Selection struct {
	Remote    string
	Branch    string
	RemoteRef string
	URL       string
}

// Select resolves zero, one, or two positional arguments. With no arguments
// it requires the accepted branch's configured upstream. With only a remote,
// it selects the accepted branch's short name there.
func Select(ctx context.Context, root, headRef string, arguments []string, direction Direction) (Selection, error) {
	if len(arguments) > 2 {
		return Selection{}, fmt.Errorf("too many remote-selection arguments")
	}
	short, ok := strings.CutPrefix(headRef, "refs/heads/")
	if !ok || !ValidBranch(short) {
		return Selection{}, fmt.Errorf("HEAD does not name a valid local branch")
	}
	remote, branch := "", ""
	switch len(arguments) {
	case 0:
		var err error
		remote, err = configOne(ctx, root, "branch."+short+".remote", false)
		if err != nil {
			return Selection{}, fmt.Errorf("resolve configured upstream remote: %w", err)
		}
		merge, err := configOne(ctx, root, "branch."+short+".merge", false)
		if err != nil {
			return Selection{}, fmt.Errorf("resolve configured upstream branch: %w", err)
		}
		branch, ok = strings.CutPrefix(merge, "refs/heads/")
		if !ok {
			return Selection{}, fmt.Errorf("configured upstream merge is not a branch ref")
		}
	case 1:
		remote, branch = arguments[0], short
	case 2:
		remote, branch = arguments[0], arguments[1]
	}
	if !validRemoteName(remote) {
		return Selection{}, fmt.Errorf("invalid remote name %q", remote)
	}
	if !ValidBranch(branch) {
		return Selection{}, fmt.Errorf("invalid branch name %q", branch)
	}
	remotes, err := listRemotes(ctx, root)
	if err != nil {
		return Selection{}, err
	}
	if _, exists := remotes[remote]; !exists {
		return Selection{}, fmt.Errorf("remote %q is not configured", remote)
	}

	urls, err := configAll(ctx, root, "remote."+remote+".url")
	if err != nil {
		return Selection{}, fmt.Errorf("read remote URL: %w", err)
	}
	selectedURLs := urls
	if direction == Push {
		pushURLs, pushErr := configAll(ctx, root, "remote."+remote+".pushurl")
		if pushErr != nil {
			return Selection{}, fmt.Errorf("read remote push URL: %w", pushErr)
		}
		if len(pushURLs) != 0 {
			selectedURLs = pushURLs
		}
	} else if direction != Fetch {
		return Selection{}, fmt.Errorf("unknown remote direction %q", direction)
	}
	if len(selectedURLs) != 1 {
		return Selection{}, fmt.Errorf("remote %q must resolve to exactly one %s URL", remote, direction)
	}
	if err := transport.ValidateLocation(selectedURLs[0]); err != nil {
		return Selection{}, fmt.Errorf("remote %q URL: %w", remote, err)
	}
	return Selection{Remote: remote, Branch: branch, RemoteRef: "refs/heads/" + branch, URL: selectedURLs[0]}, nil
}

// ValidBranch implements the Git refname restrictions needed by the CLI's
// relative refs/heads/<branch> grammar without accepting revision syntax.
func ValidBranch(value string) bool {
	if value == "" || !utf8.ValidString(value) || strings.HasPrefix(value, "-") || strings.HasPrefix(value, "/") || strings.HasSuffix(value, "/") ||
		strings.HasPrefix(value, ".") || strings.HasSuffix(value, ".") || strings.Contains(value, "..") || strings.Contains(value, "@{") || strings.HasSuffix(value, ".lock") || value == "@" {
		return false
	}
	for _, character := range value {
		if character <= 0x20 || character == 0x7f || strings.ContainsRune("~^:?*[\\", character) {
			return false
		}
	}
	for _, component := range strings.Split(value, "/") {
		if component == "" || component == "." || component == ".." || strings.HasPrefix(component, ".") || strings.HasSuffix(component, ".lock") {
			return false
		}
	}
	return true
}

func validRemoteName(value string) bool {
	if value == "" || !utf8.ValidString(value) || strings.HasPrefix(value, "-") || strings.ContainsAny(value, "\x00\r\n") {
		return false
	}
	return true
}

func listRemotes(ctx context.Context, root string) (map[string]struct{}, error) {
	output, status, err := gitOutput(ctx, root, "remote")
	if err != nil || status != 0 {
		return nil, fmt.Errorf("list configured remotes: %w", joinStatus(err, status))
	}
	result := make(map[string]struct{})
	for _, line := range bytes.Split(bytes.TrimSuffix(output, []byte("\n")), []byte("\n")) {
		if len(line) != 0 {
			result[string(line)] = struct{}{}
		}
	}
	return result, nil
}

func configOne(ctx context.Context, root, key string, allowMissing bool) (string, error) {
	values, err := configAll(ctx, root, key)
	if err != nil {
		return "", err
	}
	if len(values) == 0 && allowMissing {
		return "", nil
	}
	if len(values) != 1 || values[0] == "" {
		return "", fmt.Errorf("configuration %q must have exactly one non-empty value", key)
	}
	return values[0], nil
}

func configAll(ctx context.Context, root, key string) ([]string, error) {
	output, status, err := gitOutput(ctx, root, "config", "--local", "--get-all", key)
	if err != nil {
		return nil, err
	}
	if status == 1 {
		return []string{}, nil
	}
	if status != 0 {
		return nil, fmt.Errorf("git config exited with status %d", status)
	}
	if len(output) == 0 {
		return []string{""}, nil
	}
	lines := bytes.Split(bytes.TrimSuffix(output, []byte("\n")), []byte("\n"))
	result := make([]string, len(lines))
	for index, line := range lines {
		if !utf8.Valid(line) || bytes.ContainsAny(line, "\x00\r") {
			return nil, fmt.Errorf("configuration %q has an unrepresentable value", key)
		}
		result[index] = string(line)
	}
	return result, nil
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
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		return output, exitError.ExitCode(), nil
	}
	return nil, -1, err
}

func joinStatus(err error, status int) error {
	if err != nil {
		return err
	}
	return fmt.Errorf("git exited with status %d", status)
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
