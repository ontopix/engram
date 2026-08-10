package pullflow

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
)

type commandResult struct {
	stdout  []byte
	stderr  []byte
	status  int
	started bool
	err     error
}

func runGitCommand(ctx context.Context, executable, root string, environment []string, input []byte, arguments ...string) commandResult {
	global := []string{
		"--no-pager", "--no-optional-locks", "--no-replace-objects",
		"-c", "core.askPass=", "-c", "core.hooksPath=" + os.DevNull,
		"-c", "core.fsmonitor=false", "-c", "core.untrackedCache=false",
		"-c", "maintenance.auto=false", "-c", "gc.auto=0",
		"-c", "core.sshCommand=ssh", "-c", "credential.helper=",
		"-c", "protocol.ext.allow=never", "-c", "fetch.writeCommitGraph=false",
		"-c", "fetch.recurseSubmodules=no", "-C", root,
	}
	command := exec.CommandContext(ctx, executable, append(global, arguments...)...)
	command.Env = isolatedEnvironment(environment)
	command.Stdin = bytes.NewReader(input)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		return commandResult{status: -1, err: err}
	}
	result := commandResult{started: true, status: 0}
	err := command.Wait()
	result.stdout = append([]byte(nil), stdout.Bytes()...)
	result.stderr = append([]byte(nil), stderr.Bytes()...)
	if err == nil {
		return result
	}
	if ctx.Err() != nil {
		result.status, result.err = -1, ctx.Err()
		return result
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		result.status = exitError.ExitCode()
		return result
	}
	result.status, result.err = -1, err
	return result
}

func isolatedEnvironment(environment []string) []string {
	if environment == nil {
		environment = os.Environ()
	}
	result := make([]string, 0, len(environment)+8)
	for _, item := range environment {
		name, _, _ := strings.Cut(item, "=")
		upper := strings.ToUpper(name)
		if strings.HasPrefix(upper, "GIT_") || strings.HasPrefix(upper, "ENGRAM_") || upper == "LC_ALL" {
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

func (p *Puller) command(ctx context.Context, executable, root string, input []byte, arguments ...string) commandResult {
	environment := []string(nil)
	if p != nil {
		environment = p.Environment
		if p.run != nil {
			return p.run(ctx, executable, root, environment, input, arguments...)
		}
	}
	return runGitCommand(ctx, executable, root, environment, input, arguments...)
}

func commandDetail(result commandResult) string {
	detail := strings.TrimSpace(string(result.stderr))
	if detail == "" {
		detail = strings.TrimSpace(string(result.stdout))
	}
	if detail == "" {
		if result.err != nil {
			return result.err.Error()
		}
		return "git exited unsuccessfully"
	}
	return detail
}
