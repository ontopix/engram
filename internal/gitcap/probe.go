// Package gitcap probes the system Git executable for the capabilities used by
// engram. Decisions are based on exercised behavior rather than version text.
package gitcap

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Report is the locally observed Git capability set.
type Report struct {
	Executable            string `json:"executable"`
	Version               string `json:"version"`
	SHA1Objects           bool   `json:"sha1_objects"`
	SHA256Objects         bool   `json:"sha256_objects"`
	UpdateRefTransactions bool   `json:"update_ref_transactions"`
	Supported             bool   `json:"supported"`
}

// Probe locates Git and exercises the object formats and transactional ref
// update primitive required by the managed-store binding.
func Probe(ctx context.Context) (Report, error) {
	path, err := exec.LookPath("git")
	if err != nil {
		return Report{}, fmt.Errorf("locate git: %w", err)
	}
	report := Report{Executable: path}

	version, err := run(ctx, path, "", nil, "--version")
	if err != nil {
		return report, fmt.Errorf("git version: %w", err)
	}
	report.Version = strings.TrimSpace(string(version))

	root, err := os.MkdirTemp("", "engram-git-capabilities-")
	if err != nil {
		return report, err
	}
	defer os.RemoveAll(root)

	report.SHA1Objects, err = probeObjectFormat(ctx, path, root, "sha1", 40)
	if err != nil {
		return report, err
	}
	report.SHA256Objects, err = probeObjectFormat(ctx, path, root, "sha256", 64)
	if err != nil {
		return report, err
	}
	report.UpdateRefTransactions, err = probeUpdateRefTransaction(ctx, path, root)
	if err != nil {
		return report, err
	}
	report.Supported = report.SHA1Objects && report.SHA256Objects && report.UpdateRefTransactions
	return report, nil
}

func probeObjectFormat(ctx context.Context, git, root, format string, width int) (bool, error) {
	repository := filepath.Join(root, format)
	if _, err := run(ctx, git, "", nil, "init", "--quiet", "--object-format="+format, repository); err != nil {
		return false, nil
	}
	oidBytes, err := run(ctx, git, repository, []byte("engram capability probe\n"), "-c", "core.fsync=loose-object", "hash-object", "-w", "--no-filters", "--stdin")
	if err != nil {
		return false, fmt.Errorf("probe Git %s objects: %w", format, err)
	}
	oid := strings.TrimSpace(string(oidBytes))
	if len(oid) != width || !isLowerHex(oid) {
		return false, nil
	}
	typeBytes, err := run(ctx, git, repository, nil, "cat-file", "-t", oid)
	if err != nil || strings.TrimSpace(string(typeBytes)) != "blob" {
		return false, nil
	}
	return true, nil
}

func probeUpdateRefTransaction(ctx context.Context, git, root string) (bool, error) {
	repository := filepath.Join(root, "sha1")
	oidBytes, err := run(ctx, git, repository, []byte("ref transaction probe\n"), "-c", "core.fsync=loose-object", "hash-object", "-w", "--no-filters", "--stdin")
	if err != nil {
		return false, err
	}
	oid := strings.TrimSpace(string(oidBytes))
	input := "start\ncreate refs/engram/capability-probe " + oid + "\nprepare\ncommit\n"
	response, err := run(ctx, git, repository, []byte(input), "-c", "core.fsync=reference", "update-ref", "--no-deref", "--stdin")
	if err != nil || string(response) != "start: ok\nprepare: ok\ncommit: ok\n" {
		return false, nil
	}
	got, err := run(ctx, git, repository, nil, "rev-parse", "refs/engram/capability-probe")
	if err != nil || strings.TrimSpace(string(got)) != oid {
		return false, nil
	}
	if _, err := run(ctx, git, repository, nil, "-c", "core.fsync=reference", "update-ref", "--no-deref", "-d", "refs/engram/capability-probe", oid); err != nil {
		return false, err
	}
	return true, nil
}

func run(ctx context.Context, git, repository string, input []byte, args ...string) ([]byte, error) {
	global := []string{
		"-c", "core.longpaths=true",
		"--no-pager", "--no-optional-locks", "--no-replace-objects",
		"-c", "core.hooksPath=" + os.DevNull,
		"-c", "core.fsmonitor=false",
		"-c", "core.untrackedCache=false",
		"-c", "maintenance.auto=false",
		"-c", "gc.auto=0",
	}
	if repository != "" {
		global = append(global, "-C", repository)
	}
	args = append(global, args...)
	command := exec.CommandContext(ctx, git, args...)
	command.Env = isolatedEnvironment(os.Environ())
	command.Stdin = bytes.NewReader(input)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		if errors.Is(ctx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
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
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_CONFIG_COUNT=0",
		"GIT_NO_LAZY_FETCH=1",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_OPTIONAL_LOCKS=0",
		"GIT_PAGER=cat",
		"LC_ALL=C",
	)
}

func isLowerHex(value string) bool {
	for _, r := range value {
		if r < '0' || r > '9' {
			if r < 'a' || r > 'f' {
				return false
			}
		}
	}
	return value != ""
}
