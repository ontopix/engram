package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ontopix/engram/internal/cli"
)

func TestConfigCommandSurfaceEditsAndShowsManifest(t *testing.T) {
	project := t.TempDir()
	app := cli.NewApp()
	RegisterConfig(app)

	added := runConfigAppJSON(t, app, "config", "attachment", "add", "project-memory", "git@github.com:ontopix/memory.git", "--project", project, "--format", "json")
	assertEnvelope(t, added, "config.attachment.add", cli.OutcomeOK, 0)
	assertConfigResult(t, added, true, "", 1)

	harness := runConfigAppJSON(t, app, "config", "harness", "codex", "--project", project, "--format", "json")
	assertEnvelope(t, harness, "config.harness", cli.OutcomeOK, 0)
	assertConfigResult(t, harness, true, "codex", 1)

	shown := runConfigAppJSON(t, app, "config", "show", "--project", project, "--format", "json")
	assertEnvelope(t, shown, "config.show", cli.OutcomeOK, 0)
	assertConfigResult(t, shown, false, "codex", 1)

	removed := runConfigAppJSON(t, app, "config", "attachment", "remove", "project-memory", "--project", project, "--format", "json")
	assertEnvelope(t, removed, "config.attachment.remove", cli.OutcomeOK, 0)
	assertConfigResult(t, removed, true, "codex", 0)

	if _, err := os.Lstat(filepath.Join(project, ".memory")); !os.IsNotExist(err) {
		t.Fatalf("config command materialized memories: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(project, "MEMORY.md")); !os.IsNotExist(err) {
		t.Fatalf("config command updated MEMORY.md: %v", err)
	}
}

func TestConfigShowTextIsYAMLAndReadOnly(t *testing.T) {
	project := t.TempDir()
	app := cli.NewApp()
	RegisterConfig(app)
	var stdout, stderr bytes.Buffer
	status := app.Run(context.Background(), []string{"config", "show", "--project", project}, &stdout, &stderr)
	if status != 0 || stderr.Len() != 0 || stdout.String() != "version: 1\nattachments: []\n" {
		t.Fatalf("status/stdout/stderr = %d/%q/%q", status, stdout.String(), stderr.String())
	}
	if _, err := os.Lstat(filepath.Join(project, "engram.yaml")); !os.IsNotExist(err) {
		t.Fatalf("show created manifest: %v", err)
	}
}

func TestConfigCommandClassifiesInvalidAndBusyManifest(t *testing.T) {
	project := t.TempDir()
	app := cli.NewApp()
	RegisterConfig(app)
	invalid := runConfigAppJSON(t, app, "config", "attachment", "add", "../bad", "git@github.com:ontopix/memory.git", "--project", project, "--format", "json")
	assertEnvelope(t, invalid, "config.attachment.add", cli.OutcomeError, 2)
	if invalid.Error == nil || invalid.Error.Kind != cli.ErrorUsage {
		t.Fatalf("invalid error = %#v", invalid.Error)
	}
	first := runConfigAppJSON(t, app, "config", "attachment", "add", "memory", "git@github.com:ontopix/memory.git", "--project", project, "--format", "json")
	assertEnvelope(t, first, "config.attachment.add", cli.OutcomeOK, 0)
	conflict := runConfigAppJSON(t, app, "config", "attachment", "add", "memory", "git@github.com:ontopix/other.git", "--project", project, "--format", "json")
	assertEnvelope(t, conflict, "config.attachment.add", cli.OutcomeError, 2)
	if conflict.Error == nil || conflict.Error.Kind != cli.ErrorConflict {
		t.Fatalf("conflict error = %#v", conflict.Error)
	}
	malformedProject := t.TempDir()
	if err := os.WriteFile(filepath.Join(malformedProject, "engram.yaml"), []byte("version: 1\nunknown: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	malformed := runConfigAppJSON(t, app, "config", "show", "--project", malformedProject, "--format", "json")
	assertEnvelope(t, malformed, "config.show", cli.OutcomeError, 2)
	if malformed.Error == nil || malformed.Error.Kind != cli.ErrorIntegration {
		t.Fatalf("malformed error = %#v", malformed.Error)
	}
	if err := os.WriteFile(filepath.Join(project, "engram.yaml.lock"), []byte("owned\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	busy := runConfigAppJSON(t, app, "config", "harness", "codex", "--project", project, "--format", "json")
	assertEnvelope(t, busy, "config.harness", cli.OutcomeError, 2)
	if busy.Error == nil || busy.Error.Kind != cli.ErrorConcurrency {
		t.Fatalf("busy error = %#v", busy.Error)
	}
}

func runConfigAppJSON(t *testing.T, app *cli.App, arguments ...string) wireEnvelope {
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

func assertConfigResult(t *testing.T, envelope wireEnvelope, changed bool, harness string, attachments int) {
	t.Helper()
	result := decodeObject(t, envelope.Result)
	assertExactKeys(t, result, "project", "config_file", "changed", "config")
	if result["changed"] != changed {
		t.Fatalf("changed = %#v, want %v", result["changed"], changed)
	}
	config := result["config"].(map[string]any)
	if config["version"] != float64(1) || len(config["attachments"].([]any)) != attachments {
		t.Fatalf("config = %#v", config)
	}
	if harness == "" {
		if _, present := config["harness"]; present {
			t.Fatalf("unexpected harness in %#v", config)
		}
	} else if config["harness"] != harness {
		t.Fatalf("harness = %#v, want %q", config["harness"], harness)
	}
	if !strings.HasSuffix(result["config_file"].(string), string(filepath.Separator)+"engram.yaml") {
		t.Fatalf("config_file = %#v", result["config_file"])
	}
}
