package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ontopix/engram/internal/attachment"
	"github.com/ontopix/engram/internal/cli"
)

func TestAttachAndDetachJSON(t *testing.T) {
	store := managedFixture(t)
	project := t.TempDir()
	attached := runAttachmentJSON(t, "attach", store, "--project", project, "--format", "json")
	assertEnvelope(t, attached, "attach", cli.OutcomeOK, 0)
	result := decodeObject(t, attached.Result)
	assertExactKeys(t, result, "project", "store", "entrypoint", "changed", "validation", "audits")
	canonicalStore, err := attachment.CanonicalStore(store)
	if err != nil {
		t.Fatal(err)
	}
	if result["changed"] != true || result["store"] != canonicalStore {
		t.Fatalf("attach result = %#v", result)
	}
	validation := result["validation"].(map[string]any)
	if validation["target"] != "managed-store" || validation["status"] != "complete" {
		t.Fatalf("validation = %#v", validation)
	}
	entrypoint := result["entrypoint"].(string)
	data, err := os.ReadFile(entrypoint)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte(attachment.OpenMarker)) || !bytes.Contains(data, []byte(canonicalStore)) {
		t.Fatalf("entrypoint = %q", data)
	}

	unchanged := runAttachmentJSON(t, "attach", store, "--project", project, "--format", "json")
	assertEnvelope(t, unchanged, "attach", cli.OutcomeOK, 0)
	if decodeObject(t, unchanged.Result)["changed"] != false {
		t.Fatal("idempotent attach reported a change")
	}

	detached := runAttachmentJSON(t, "detach", store, "--project", project, "--format", "json")
	assertEnvelope(t, detached, "detach", cli.OutcomeOK, 0)
	detachResult := decodeObject(t, detached.Result)
	assertExactKeys(t, detachResult, "project", "store", "entrypoint", "changed")
	if detachResult["changed"] != true {
		t.Fatalf("detach result = %#v", detachResult)
	}
}

func TestAttachDoesNotPublishInvalidHistory(t *testing.T) {
	store := managedFixture(t)
	managedGit(t, store, "checkout", "-b", "side")
	appendFile(t, filepath.Join(store, "topics", "why-files.md"), "\nSide.\n")
	managedGit(t, store, "add", "topics/why-files.md")
	managedGit(t, store, "commit", "--no-verify", "-m", "side")
	managedGit(t, store, "checkout", "main")
	appendFile(t, filepath.Join(store, "topics", "derived-state.md"), "\nMain.\n")
	managedGit(t, store, "add", "topics/derived-state.md")
	managedGit(t, store, "commit", "--no-verify", "-m", "main")
	managedGit(t, store, "merge", "--no-verify", "--no-ff", "side", "-m", "merge")
	project := t.TempDir()
	envelope := runAttachmentJSON(t, "attach", store, "--project", project, "--format", "json")
	assertEnvelope(t, envelope, "attach", cli.OutcomeIssues, 1)
	if _, err := os.Stat(filepath.Join(project, "AGENTS.md")); !os.IsNotExist(err) {
		t.Fatalf("invalid store published entrypoint: %v", err)
	}
}

func TestAttachMalformedBlockIsIntegrationError(t *testing.T) {
	store := managedFixture(t)
	project := t.TempDir()
	entrypoint := filepath.Join(project, "AGENTS.md")
	malformed := attachment.OpenMarker + "\nbroken\n" + attachment.CloseMarker + "\n"
	if err := os.WriteFile(entrypoint, []byte(malformed), 0o644); err != nil {
		t.Fatal(err)
	}
	envelope := runAttachmentJSON(t, "attach", store, "--project", project, "--format", "json")
	assertEnvelope(t, envelope, "attach", cli.OutcomeError, 2)
	if envelope.Error == nil || envelope.Error.Kind != cli.ErrorIntegration {
		t.Fatalf("error = %#v", envelope.Error)
	}
	data, err := os.ReadFile(entrypoint)
	if err != nil || string(data) != malformed {
		t.Fatalf("malformed bytes changed: %v %q", err, data)
	}
}

func runAttachmentJSON(t *testing.T, arguments ...string) wireEnvelope {
	t.Helper()
	app := cli.NewApp()
	RegisterPortable(app)
	RegisterManagedReads(app)
	RegisterAttachments(app)
	var stdout, stderr bytes.Buffer
	status := app.Run(context.Background(), arguments, &stdout, &stderr)
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q", stderr.String())
	}
	var envelope wireEnvelope
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatalf("decode %q: %v", stdout.String(), err)
	}
	if status != envelope.ExitStatus {
		t.Fatalf("process status = %d, envelope status = %d", status, envelope.ExitStatus)
	}
	return envelope
}

func TestDetachMissingStorePathUsesLexicalIdentity(t *testing.T) {
	store := managedFixture(t)
	project := t.TempDir()
	attached := runAttachmentJSON(t, "attach", store, "--project", project, "--format", "json")
	assertEnvelope(t, attached, "attach", cli.OutcomeOK, 0)
	if err := os.RemoveAll(store); err != nil {
		t.Fatal(err)
	}
	detached := runAttachmentJSON(t, "detach", store, "--project", project, "--format", "json")
	assertEnvelope(t, detached, "detach", cli.OutcomeOK, 0)
	if changed := decodeObject(t, detached.Result)["changed"]; changed != true {
		t.Fatalf("changed = %#v", changed)
	}
	entrypoint := filepath.Join(project, "AGENTS.md")
	data, err := os.ReadFile(entrypoint)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), store) {
		t.Fatal("stale store path remains attached")
	}
}
