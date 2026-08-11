package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/ontopix/engram/internal/cli"
)

func TestSetupJSONAndDryRun(t *testing.T) {
	project := t.TempDir()
	app := cli.NewApp()
	RegisterSetup(app)
	envelope := runSetupAppJSON(t, app, "setup", "--harness", "claude-code", "--project", project, "--dry-run", "--format", "json")
	assertEnvelope(t, envelope, "setup", cli.OutcomeOK, 0)
	result := decodeObject(t, envelope.Result)
	assertExactKeys(t, result, "project", "harness", "memory_file", "entrypoint", "skills_dir", "dry_run", "changed", "files")
	if result["harness"] != "claude-code" || result["dry_run"] != true || result["changed"] != true {
		t.Fatalf("result=%#v", result)
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
