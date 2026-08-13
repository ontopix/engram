package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/ontopix/engram/internal/cli"
	"github.com/ontopix/engram/internal/testpath"
)

func TestSetupJSONAndDryRun(t *testing.T) {
	project := t.TempDir()
	app := cli.NewApp()
	RegisterSetup(app)
	envelope := runSetupAppJSON(t, app, "setup", "--harness", "claude-code", "--project", project, "--dry-run", "--format", "json")
	assertEnvelope(t, envelope, "setup", cli.OutcomeOK, 0)
	result := decodeObject(t, envelope.Result)
	assertExactKeys(t, result, "project", "config_file", "harness", "memory_dir", "memory_file", "entrypoint", "skills_dir", "dry_run", "changed", "attachments", "files")
	if result["harness"] != "claude-code" || result["dry_run"] != true || result["changed"] != true {
		t.Fatalf("result=%#v", result)
	}
	if result["config_file"] != nil || result["memory_dir"] != nil || len(result["attachments"].([]any)) != 0 {
		t.Fatalf("imperative setup result=%#v", result)
	}
	canonicalProject, err := filepath.EvalSymlinks(project)
	if err != nil {
		t.Fatal(err)
	}
	if result["entrypoint"] != filepath.Join(canonicalProject, "CLAUDE.md") || result["memory_file"] != filepath.Join(canonicalProject, "MEMORY.md") {
		t.Fatalf("paths=%#v", result)
	}
	if _, err := os.Lstat(filepath.Join(project, ".claude")); !os.IsNotExist(err) {
		t.Fatalf("dry-run wrote files: %v", err)
	}
}

func TestSetupFromManifestClonesReusesAndDetaches(t *testing.T) {
	location, remote := setupRemoteFixture(t)
	project := t.TempDir()
	config := fmt.Sprintf("version: 1\nharness: codex\nattachments:\n  - name: primary\n    url: %s\n", strconv.Quote(location))
	if err := os.WriteFile(filepath.Join(project, "engram.yaml"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	app := cli.NewApp()
	RegisterSetup(app)
	first := runSetupAppJSON(t, app, "setup", "--project", project, "--format", "json")
	assertEnvelope(t, first, "setup", cli.OutcomeOK, 0)
	firstResult := decodeObject(t, first.Result)
	if firstResult["harness"] != "codex" || firstResult["changed"] != true {
		t.Fatalf("first result=%#v", firstResult)
	}
	attachments := firstResult["attachments"].([]any)
	if len(attachments) != 1 || attachments[0].(map[string]any)["action"] != "cloned" {
		t.Fatalf("first attachments=%#v", attachments)
	}
	for _, name := range []string{".gitignore", "MEMORY.md", "AGENTS.md", filepath.Join(".agents", "skills", "using-engram", "SKILL.md"), filepath.Join(".memory", "primary", "README.md")} {
		if _, err := os.Stat(filepath.Join(project, name)); err != nil {
			t.Fatalf("setup did not materialize %s: %v", name, err)
		}
	}

	if err := os.Rename(remote, remote+".offline"); err != nil {
		t.Fatal(err)
	}
	second := runSetupAppJSON(t, app, "setup", "--project", project, "--format", "json")
	assertEnvelope(t, second, "setup", cli.OutcomeOK, 0)
	secondResult := decodeObject(t, second.Result)
	attachments = secondResult["attachments"].([]any)
	if secondResult["changed"] != false || len(attachments) != 1 || attachments[0].(map[string]any)["action"] != "reused" {
		t.Fatalf("second result=%#v", secondResult)
	}

	if err := os.WriteFile(filepath.Join(project, "engram.yaml"), []byte("version: 1\nharness: codex\nattachments: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	third := runSetupAppJSON(t, app, "setup", "--project", project, "--format", "json")
	assertEnvelope(t, third, "setup", cli.OutcomeOK, 0)
	if decodeObject(t, third.Result)["changed"] != true {
		t.Fatalf("detachment result=%#v", decodeObject(t, third.Result))
	}
	if _, err := os.Stat(filepath.Join(project, ".memory", "primary")); err != nil {
		t.Fatalf("detachment deleted clone: %v", err)
	}
	memory, err := os.ReadFile(filepath.Join(project, "MEMORY.md"))
	if err != nil || bytes.Contains(memory, []byte(`"path": ".memory/primary"`)) {
		t.Fatalf("detachment did not reconcile MEMORY.md: %q, %v", memory, err)
	}
}

func TestSetupWithoutManifestOrHarnessIsUsageError(t *testing.T) {
	project := t.TempDir()
	app := cli.NewApp()
	RegisterSetup(app)
	envelope := runSetupAppJSON(t, app, "setup", "--project", project, "--format", "json")
	assertEnvelope(t, envelope, "setup", cli.OutcomeError, 2)
	if envelope.Error == nil || envelope.Error.Kind != cli.ErrorUsage {
		t.Fatalf("error=%#v", envelope.Error)
	}
}

func TestSetupReportsCompletedLocalConvergenceBeforeCloneFailure(t *testing.T) {
	project := t.TempDir()
	missing := testpath.FileURL(filepath.Join(project, "missing.git"))
	config := fmt.Sprintf("version: 1\nharness: codex\nattachments:\n  - name: primary\n    url: %s\n", strconv.Quote(missing))
	if err := os.WriteFile(filepath.Join(project, "engram.yaml"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	app := cli.NewApp()
	RegisterSetup(app)
	envelope := runSetupAppJSON(t, app, "setup", "--project", project, "--format", "json")
	assertEnvelope(t, envelope, "setup", cli.OutcomeError, 2)
	if envelope.Error == nil || envelope.Error.Kind != cli.ErrorNetwork {
		t.Fatalf("error=%#v", envelope.Error)
	}
	result := decodeObject(t, envelope.Result)
	if result["changed"] != true || len(result["attachments"].([]any)) != 0 {
		t.Fatalf("partial setup result=%#v", result)
	}
	for _, name := range []string{".gitignore", ".memory"} {
		if _, err := os.Stat(filepath.Join(project, name)); err != nil {
			t.Fatalf("completed setup action %s is missing: %v", name, err)
		}
	}
}

func TestSetupConflictIsIntegrationError(t *testing.T) {
	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "AGENTS.md"), []byte("<!-- engram:harness:v1 -->\nbroken\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	app := cli.NewApp()
	RegisterSetup(app)
	envelope := runSetupAppJSON(t, app, "setup", "--harness", "codex", "--project", project, "--format", "json")
	assertEnvelope(t, envelope, "setup", cli.OutcomeError, 2)
	if envelope.Error == nil || envelope.Error.Kind != cli.ErrorIntegration {
		t.Fatalf("error=%#v", envelope.Error)
	}
}

func runSetupAppJSON(t *testing.T, app *cli.App, arguments ...string) wireEnvelope {
	t.Helper()
	var stdout, stderr bytes.Buffer
	status := app.Run(context.Background(), arguments, &stdout, &stderr)
	if stderr.Len() != 0 {
		t.Fatalf("stderr=%q", stderr.String())
	}
	var envelope wireEnvelope
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatalf("decode %q: %v", stdout.String(), err)
	}
	if status != envelope.ExitStatus {
		t.Fatalf("status=%d envelope=%d", status, envelope.ExitStatus)
	}
	return envelope
}

func setupRemoteFixture(t *testing.T) (string, string) {
	t.Helper()
	store := managedFixture(t)
	bare := filepath.Join(t.TempDir(), "memory.git")
	command := exec.Command("git", "clone", "--bare", "--", store, bare)
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("bare clone: %v\n%s", err, output)
	}
	return testpath.FileURL(bare), bare
}
