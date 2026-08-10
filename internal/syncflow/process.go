package syncflow

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

func runGitCommand(ctx context.Context, executable, root string, environment []string, arguments ...string) commandResult {
	global := []string{
		"--no-pager",
		"--no-optional-locks",
		"--no-replace-objects",
		"-c", "core.askPass=",
		"-c", "core.hooksPath=" + os.DevNull,
		"-c", "core.fsmonitor=false",
		"-c", "core.untrackedCache=false",
		"-c", "maintenance.auto=false",
		"-c", "gc.auto=0",
		"-c", "core.sshCommand=ssh",
		"-c", "credential.helper=",
		"-c", "protocol.ext.allow=never",
		"-c", "push.followTags=false",
		"-c", "push.gpgSign=false",
		"-c", "push.negotiate=false",
		"-c", "push.recurseSubmodules=no",
		"-C", root,
	}
	command := exec.CommandContext(ctx, executable, append(global, arguments...)...)
	command.Env = isolatedEnvironment(environment)
	if len(arguments) != 0 && arguments[0] == "push" {
		// Packet tracing supplies the causal boundary between a failure before
		// the update command was emitted and a transport loss after dispatch.
		// Inherited tracing remains stripped; this controller-owned trace is
		// captured privately with stderr and is never protocol output.
		command.Env = append(command.Env, "GIT_TRACE_PACKET=1")
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
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
		result.status = -1
		result.err = ctx.Err()
		return result
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		result.status = exitError.ExitCode()
		return result
	}
	result.status = -1
	result.err = err
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
