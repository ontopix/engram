package main

import (
	"bytes"
	"encoding/json"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/ontopix/engram/internal/cli"
)

func TestExecutableCompleteHelpSurface(t *testing.T) {
	binary, root := buildExecutable(t)
	model := cli.DefaultModel()
	for _, specification := range model.Commands {
		specification := specification
		t.Run(string(specification.Name), func(t *testing.T) {
			arguments := append(append([]string(nil), specification.Path...), "--help", "--no-color")
			status, stdout, stderr := runExecutable(t, binary, root, arguments...)
			want := "Usage:\n  " + strings.ReplaceAll(specification.Usage, "\n", "\n  ") + "\n"
			if status != 0 || stdout != want || stderr != "" {
				t.Fatalf("status=%d stdout=%q stderr=%q, want 0, %q, empty", status, stdout, stderr, want)
			}
		})
	}
}

func TestExecutableRepresentativeClosedOutcomes(t *testing.T) {
	binary, root := buildExecutable(t)
	indeterminateBase := t.TempDir()
	if err := os.WriteFile(filepath.Join(indeterminateBase, "bad name"), []byte("boundary\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		args    []string
		status  int
		command string
		outcome string
		error   bool
	}{
		{name: "version", args: []string{"version", "--format", "json"}, status: 0, command: "version", outcome: "ok"},
		{name: "portable", args: []string{"check", filepath.Join(root, "examples", "minimal"), "--format", "json"}, status: 0, command: "check", outcome: "ok"},
		{name: "issues", args: []string{"check", t.TempDir(), "--format", "json"}, status: 1, command: "check", outcome: "issues"},
		{name: "indeterminate", args: []string{"check", "--base", indeterminateBase, "--candidate", filepath.Join(root, "examples", "minimal"), "--format", "json"}, status: 3, command: "check", outcome: "indeterminate"},
		{name: "error", args: []string{"doctor", filepath.Join(t.TempDir(), "missing"), "--format", "json"}, status: 2, command: "doctor", outcome: "error", error: true},
		{name: "usage", args: []string{"version", "unexpected", "--format", "json"}, status: 2, command: "version", outcome: "error", error: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			status, stdout, stderr := runExecutable(t, binary, root, test.args...)
			if status != test.status || stderr != "" {
				t.Fatalf("status=%d stderr=%q, want %d and empty", status, stderr, test.status)
			}
			var envelope map[string]any
			decoder := json.NewDecoder(strings.NewReader(stdout))
			decoder.UseNumber()
			if err := decoder.Decode(&envelope); err != nil {
				t.Fatalf("decode %q: %v", stdout, err)
			}
			keys := make([]string, 0, len(envelope))
			for key := range envelope {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			wantKeys := []string{"command", "error", "exit_status", "outcome", "result", "version"}
			if strings.Join(keys, "\x00") != strings.Join(wantKeys, "\x00") || envelope["version"] != json.Number("1") || envelope["command"] != test.command || envelope["outcome"] != test.outcome || envelope["exit_status"] != json.Number(strconv.Itoa(test.status)) {
				t.Fatalf("envelope = %#v", envelope)
			}
			if (envelope["error"] != nil) != test.error {
				t.Fatalf("error payload = %#v, want present=%v", envelope["error"], test.error)
			}
		})
	}

	status, stdout, stderr := runExecutable(t, binary, root, "check", filepath.Join(root, "examples", "minimal"), "--format", "text", "--no-color")
	wantText := "{\n  \"target\": \"snapshot\",\n  \"status\": \"complete\",\n  \"findings\": []\n}\n"
	if status != 0 || stdout != wantText || stderr != "" {
		t.Fatalf("text status=%d stdout=%q stderr=%q", status, stdout, stderr)
	}
}

func TestExecutableRealCommandSurface(t *testing.T) {
	binary, root := buildExecutable(t)
	environment := executableTestEnvironment(t)
	workspace := t.TempDir()
	store := filepath.Join(workspace, "store")
	covered := make(map[string]int)
	run := func(command, outcome string, status int, arguments []string, keys ...string) map[string]any {
		t.Helper()
		covered[command]++
		return assertExecutableResult(t, binary, root, environment, command, outcome, status, arguments, keys...)
	}

	run("init", "ok", 0,
		[]string{"init", store, "--format", "json"},
		"accepted", "dry_run", "files", "launcher", "root", "validation")

	// A repository-controlled fsmonitor must never become an implicit process
	// execution path for an otherwise local command.
	sentinel := filepath.Join(workspace, "fsmonitor-invoked")
	helper := gitCommand(
		os.Args[0],
		"-test.run=TestExecutableFSMonitorSentinelHelper",
		"--",
		"engram-fsmonitor-sentinel-helper-v1",
		sentinel,
	)
	runFixtureGit(t, environment, "-C", store, "config", "core.fsmonitor", helper)
	run("status", "ok", 0,
		[]string{"--store", store, "status", "--format", "json"},
		"accepted", "candidate_base", "mode", "replay", "staged", "unstaged")
	if _, err := os.Lstat(sentinel); !os.IsNotExist(err) {
		t.Fatalf("local status executed repository-controlled core.fsmonitor: %v", err)
	}
	runFixtureGit(t, environment, "-C", store, "config", "--unset", "core.fsmonitor")

	run("version", "ok", 0,
		[]string{"version", "--format", "json"},
		"annex_versions", "build", "cli_version", "core_versions", "git")
	run("schema.inventory", "ok", 0,
		[]string{"schema", "inventory", "--format", "json"}, "schemas")
	run("schema.list", "ok", 0,
		[]string{"--store", store, "schema", "list", "--format", "json"}, "schemas")
	run("schema.show", "ok", 0,
		[]string{"--store", store, "schema", "show", "note", "--format", "json"}, "content", "schema")
	run("check", "ok", 0,
		[]string{"--store", store, "check", "--accepted", "--format", "json"}, "findings", "status", "target")
	run("log", "ok", 0,
		[]string{"--store", store, "log", "-n", "1", "--format", "json"}, "commits")
	run("doctor", "ok", 0,
		[]string{"doctor", store, "--format", "json"}, "checks", "recovery")

	project := filepath.Join(workspace, "project")
	if err := os.Mkdir(project, 0o755); err != nil {
		t.Fatal(err)
	}
	run("attach", "ok", 0,
		[]string{"attach", store, "--project", project, "--format", "json"},
		"audits", "changed", "memory_file", "project", "store", "validation")
	run("detach", "ok", 0,
		[]string{"detach", store, "--project", project, "--format", "json"},
		"changed", "memory_file", "project", "store")
	run("setup", "ok", 0,
		[]string{"setup", "--harness", "codex", "--project", project, "--format", "json"},
		"attachments", "changed", "config_file", "dry_run", "entrypoint", "files", "harness", "memory_dir", "memory_file", "project", "skills_dir")

	run("hooks.list", "ok", 0,
		[]string{"--store", store, "hooks", "list", "--format", "json"},
		"changed", "hooks", "sha256", "state", "trusted")
	run("hooks.trust", "ok", 0,
		[]string{"--store", store, "hooks", "trust", "--format", "json"},
		"changed", "hooks", "sha256", "state", "trusted")
	run("hooks.revoke", "ok", 0,
		[]string{"--store", store, "hooks", "revoke", "--format", "json"},
		"changed", "revoked_sets")

	run("new", "ok", 0,
		[]string{"--store", store, "new", "note", "black-box.md", "--description", "Compiled-binary command coverage.", "--title", "Black-box", "--format", "json"},
		"catalogs", "changed", "dry_run", "record")
	run("schema.copy", "ok", 0,
		[]string{"--store", store, "schema", "copy", "person", "--format", "json"},
		"changed", "dry_run", "path", "schema")
	run("mv", "ok", 0,
		[]string{"--store", store, "mv", "black-box.md", "compiled-binary.md", "--format", "json"},
		"catalogs", "changed", "dry_run", "from", "paths", "to")
	run("fmt", "ok", 0,
		[]string{"--store", store, "fmt", "--dry-run", "--format", "json"},
		"changed", "check", "dry_run", "paths")
	run("diff", "ok", 0,
		[]string{"--store", store, "diff", "--format", "json"}, "changes", "from", "stat", "to")
	run("add", "ok", 0,
		[]string{"--store", store, "add", "--all", "--format", "json"}, "changed", "staged")
	run("check", "ok", 0,
		[]string{"--store", store, "check", "--staged", "--format", "json"}, "findings", "status", "target")
	run("commit", "ok", 0,
		[]string{"--store", store, "commit", "-m", "Exercise compiled command surface", "--format", "json"},
		"changes", "commit", "created", "dry_run", "validation")
	run("revert", "ok", 0,
		[]string{"--store", store, "revert", "HEAD", "--dry-run", "--format", "json"},
		"changes", "commit", "conflicts", "created", "dry_run", "reverted", "validation")

	remote := filepath.Join(workspace, "remote.git")
	runFixtureGit(t, environment, "init", "--bare", "--initial-branch=main", remote)
	remoteURL := fileRepositoryURL(remote)
	runFixtureGit(t, environment, "-C", store, "remote", "add", "origin", remoteURL)
	runFixtureGit(t, environment, "-C", store, "config", "branch.main.remote", "origin")
	runFixtureGit(t, environment, "-C", store, "config", "branch.main.merge", "refs/heads/main")
	run("push", "ok", 0,
		[]string{"--store", store, "push", "--format", "json"},
		"after", "audits", "before", "changed", "commits", "remote", "remote_observed", "remote_ref", "state", "validation")

	clone := filepath.Join(workspace, "clone")
	run("clone", "ok", 0,
		[]string{"clone", remoteURL, clone, "--format", "json"},
		"accepted", "audits", "launcher", "published", "remote", "reused", "root", "validation", "verified_commits")
	run("pull", "ok", 0,
		[]string{"--store", clone, "pull", "--format", "json"},
		"after", "audits", "before", "candidate_validation", "changes", "conflicts", "fetched", "remote", "remote_ref", "replayed", "state", "validation")

	// One actual handler failure complements the protocol-level 0/1/2/3
	// outcome cases above and proves a typed command error survives the binary.
	assertExecutableError(t, binary, root, environment, "add", "usage",
		[]string{"--store", store, "add", "../outside", "--format", "json"})

	model := cli.DefaultModel()
	if len(covered) != len(model.Commands) {
		t.Fatalf("real command coverage has %d command families, model has %d: %#v", len(covered), len(model.Commands), covered)
	}
	for _, specification := range model.Commands {
		if covered[string(specification.Name)] == 0 {
			t.Errorf("compiled-binary success coverage is missing %s", specification.Name)
		}
	}
}

func TestExecutableFSMonitorSentinelHelper(t *testing.T) {
	for index, argument := range os.Args {
		if argument == "engram-fsmonitor-sentinel-helper-v1" && index+1 < len(os.Args) {
			if err := os.WriteFile(os.Args[index+1], []byte("invoked\n"), 0o600); err != nil {
				os.Exit(91)
			}
			os.Exit(0)
		}
	}
}

func assertExecutableResult(t *testing.T, binary, directory string, environment []string, command, outcome string, status int, arguments []string, keys ...string) map[string]any {
	t.Helper()
	envelope := runExecutableEnvelope(t, binary, directory, environment, status, arguments...)
	if envelope["command"] != command || envelope["outcome"] != outcome || envelope["error"] != nil {
		t.Fatalf("envelope = %#v, want command=%q outcome=%q and no error", envelope, command, outcome)
	}
	result, ok := envelope["result"].(map[string]any)
	if !ok {
		t.Fatalf("result = %#v, want object", envelope["result"])
	}
	assertExecutableKeys(t, result, keys...)
	return result
}

func assertExecutableError(t *testing.T, binary, directory string, environment []string, command, kind string, arguments []string) {
	t.Helper()
	envelope := runExecutableEnvelope(t, binary, directory, environment, 2, arguments...)
	if envelope["command"] != command || envelope["outcome"] != "error" {
		t.Fatalf("error envelope = %#v", envelope)
	}
	protocolError, ok := envelope["error"].(map[string]any)
	if !ok || protocolError["kind"] != kind {
		t.Fatalf("protocol error = %#v, want kind %q", envelope["error"], kind)
	}
}

func runExecutableEnvelope(t *testing.T, binary, directory string, environment []string, wantStatus int, arguments ...string) map[string]any {
	t.Helper()
	status, stdout, stderr := runExecutableWithEnvironment(t, binary, directory, environment, arguments...)
	if status != wantStatus || stderr != "" {
		t.Fatalf("status=%d stderr=%q stdout=%q, want status=%d and empty stderr", status, stderr, stdout, wantStatus)
	}
	var envelope map[string]any
	decoder := json.NewDecoder(strings.NewReader(stdout))
	decoder.UseNumber()
	if err := decoder.Decode(&envelope); err != nil {
		t.Fatalf("decode %q: %v", stdout, err)
	}
	assertExecutableKeys(t, envelope, "command", "error", "exit_status", "outcome", "result", "version")
	if envelope["version"] != json.Number("1") || envelope["exit_status"] != json.Number(strconv.Itoa(wantStatus)) {
		t.Fatalf("protocol header = %#v", envelope)
	}
	return envelope
}

func assertExecutableKeys(t *testing.T, object map[string]any, keys ...string) {
	t.Helper()
	got := make([]string, 0, len(object))
	for key := range object {
		got = append(got, key)
	}
	sort.Strings(got)
	want := append([]string(nil), keys...)
	sort.Strings(want)
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("object keys = %q, want %q: %#v", got, want, object)
	}
}

func executableTestEnvironment(t *testing.T) []string {
	t.Helper()
	configuration := t.TempDir()
	home := filepath.Join(configuration, "home")
	if err := os.Mkdir(home, 0o755); err != nil {
		t.Fatal(err)
	}
	global := "[user]\n\tname = Binary Test\n\temail = binary@example.test\n[commit]\n\tgpgsign = false\n"
	if err := os.WriteFile(filepath.Join(home, ".gitconfig"), []byte(global), 0o600); err != nil {
		t.Fatal(err)
	}
	return replaceEnvironment(os.Environ(), map[string]string{
		"APPDATA":         filepath.Join(configuration, "appdata"),
		"HOME":            home,
		"LOCALAPPDATA":    filepath.Join(configuration, "localappdata"),
		"USERPROFILE":     home,
		"XDG_CONFIG_HOME": filepath.Join(configuration, "config"),
		"XDG_DATA_HOME":   filepath.Join(configuration, "data"),
	})
}

func replaceEnvironment(environment []string, replacements map[string]string) []string {
	result := make([]string, 0, len(environment)+len(replacements))
	for _, item := range environment {
		name, _, _ := strings.Cut(item, "=")
		if _, replaced := replacements[strings.ToUpper(name)]; !replaced {
			result = append(result, item)
		}
	}
	keys := make([]string, 0, len(replacements))
	for key := range replacements {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		result = append(result, key+"="+replacements[key])
	}
	return result
}

func runFixtureGit(t *testing.T, environment []string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Env = replaceEnvironment(environment, map[string]string{
		"GIT_CONFIG_COUNT":    "0",
		"GIT_CONFIG_GLOBAL":   os.DevNull,
		"GIT_CONFIG_NOSYSTEM": "1",
		"GIT_OPTIONAL_LOCKS":  "0",
		"GIT_TERMINAL_PROMPT": "0",
	})
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %q: %v: %s", arguments, err, output)
	}
	return string(output)
}

func fileRepositoryURL(name string) string {
	path := filepath.ToSlash(name)
	if runtime.GOOS == "windows" && !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return (&url.URL{Scheme: "file", Path: path}).String()
}

func gitCommand(arguments ...string) string {
	quoted := make([]string, len(arguments))
	for index, argument := range arguments {
		quoted[index] = "'" + strings.ReplaceAll(filepath.ToSlash(argument), "'", "'\\''") + "'"
	}
	return strings.Join(quoted, " ")
}

func buildExecutable(t *testing.T) (string, string) {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate executable test source")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(source), "..", ".."))
	name := "engram"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	binary := filepath.Join(t.TempDir(), name)
	command := exec.Command("go", "build", "-trimpath", "-o", binary, "./cmd/engram")
	command.Dir = root
	command.Env = append(os.Environ(), "GOWORK=off")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build executable: %v: %s", err, output)
	}
	return binary, root
}

func runExecutable(t *testing.T, binary, directory string, arguments ...string) (int, string, string) {
	t.Helper()
	return runExecutableWithEnvironment(t, binary, directory, nil, arguments...)
}

func runExecutableWithEnvironment(t *testing.T, binary, directory string, environment []string, arguments ...string) (int, string, string) {
	t.Helper()
	command := exec.Command(binary, arguments...)
	command.Dir = directory
	command.Env = environment
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	if err == nil {
		return 0, stdout.String(), stderr.String()
	}
	exit, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("run executable: %v", err)
	}
	return exit.ExitCode(), stdout.String(), stderr.String()
}
