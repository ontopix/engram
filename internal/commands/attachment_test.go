package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	stores := decodeAttachmentStores(t, data)
	if len(stores) != 1 || stores[0] != canonicalStore {
		t.Fatalf("attached stores = %#v, want [%q]", stores, canonicalStore)
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

func decodeAttachmentStores(t *testing.T, data []byte) []string {
	t.Helper()
	open := []byte(attachment.OpenMarker + "\n")
	close := []byte(attachment.CloseMarker)
	if bytes.Count(data, open) != 1 || bytes.Count(data, close) != 1 {
		t.Fatalf("entrypoint has invalid owned markers: %q", data)
	}
	blockStart := bytes.Index(data, open) + len(open)
	blockEnd := bytes.Index(data[blockStart:], close)
	if blockEnd < 0 {
		t.Fatalf("entrypoint has no complete owned block: %q", data)
	}
	block := data[blockStart : blockStart+blockEnd]
	const fenceOpen = "```json\n"
	const fenceClose = "\n```\n"
	payloadStart := bytes.Index(block, []byte(fenceOpen))
	if payloadStart < 0 {
		t.Fatalf("owned block has no JSON fence: %q", block)
	}
	payloadStart += len(fenceOpen)
	payloadEnd := bytes.Index(block[payloadStart:], []byte(fenceClose))
	if payloadEnd < 0 {
		t.Fatalf("owned block has no complete JSON fence: %q", block)
	}

	var document map[string]json.RawMessage
	if err := json.Unmarshal(block[payloadStart:payloadStart+payloadEnd], &document); err != nil {
		t.Fatalf("decode owned block JSON: %v", err)
	}
	storesJSON, ok := document["stores"]
	if !ok || len(document) != 1 {
		t.Fatalf("owned block JSON keys = %#v, want only stores", document)
	}
	var stores []string
	if err := json.Unmarshal(storesJSON, &stores); err != nil || stores == nil {
		t.Fatalf("decode owned block stores: %v", err)
	}
	return stores
}

func TestDecodeAttachmentStoresDecodesEscapedWindowsPath(t *testing.T) {
	const want = `C:\Users\Ada\engram`
	data := []byte(attachment.OpenMarker + "\n```json\n{\"stores\":[\"C:\\\\Users\\\\Ada\\\\engram\"]}\n```\n" + attachment.CloseMarker + "\n")
	stores := decodeAttachmentStores(t, data)
	if len(stores) != 1 || stores[0] != want {
		t.Fatalf("stores = %#v, want [%q]", stores, want)
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
	if result := decodeObject(t, envelope.Result); len(result) != 0 {
		t.Fatalf("pre-publication error result = %#v, want {}", result)
	}
	data, err := os.ReadFile(entrypoint)
	if err != nil || string(data) != malformed {
		t.Fatalf("malformed bytes changed: %v %q", err, data)
	}
}

type fakeAttachmentUpdater struct {
	attachErr error
	detachErr error
}

func (f *fakeAttachmentUpdater) Attach(string, string, string) (attachment.Result, error) {
	return attachment.Result{}, f.attachErr
}

func (f *fakeAttachmentUpdater) Detach(string, string, string) (attachment.Result, error) {
	return attachment.Result{}, f.detachErr
}

func TestAttachmentMutationErrorsUseExactProtocolShape(t *testing.T) {
	store := managedFixture(t)
	project := t.TempDir()
	tests := []struct {
		name     string
		command  string
		kind     cli.ErrorKind
		durable  bool
		recovery bool
		want     string
	}{
		{
			name: "attach renamed before sync", command: "attach", kind: cli.ErrorIO,
			want: `{"durable":false,"local_refs":[],"head":null,"checkout_changed":false,"remote":null,"recovery_required":false}`,
		},
		{
			name: "attach durably published", command: "attach", kind: cli.ErrorIO, durable: true,
			want: `{"durable":true,"local_refs":[],"head":null,"checkout_changed":false,"remote":null,"recovery_required":false}`,
		},
		{
			name: "detach residual lock", command: "detach", kind: cli.ErrorConcurrency, durable: true, recovery: true,
			want: `{"durable":true,"local_refs":[],"head":null,"checkout_changed":false,"remote":null,"recovery_required":true}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cause := errors.New("injected attachment failure")
			if test.recovery {
				cause = errors.Join(attachment.ErrBusy, cause)
			}
			err := &attachment.EffectError{
				Effect: attachment.Effect{Durable: test.durable, RecoveryRequired: test.recovery},
				Err:    cause,
			}
			updater := &fakeAttachmentUpdater{}
			if test.command == "attach" {
				updater.attachErr = err
			} else {
				updater.detachErr = err
			}
			app := cli.NewApp()
			RegisterPortable(app)
			RegisterManagedReads(app)
			registerAttachmentsWith(app, updater)
			envelope := runAttachmentAppJSON(t, app, test.command, store, "--project", project, "--format", "json")
			assertEnvelope(t, envelope, test.command, cli.OutcomeError, 2)
			if envelope.Error == nil || envelope.Error.Kind != test.kind {
				t.Fatalf("error = %#v", envelope.Error)
			}
			if got := string(envelope.Result); got != test.want {
				t.Fatalf("result = %s, want %s", got, test.want)
			}
			result := decodeObject(t, envelope.Result)
			assertExactKeys(t, result, "durable", "local_refs", "head", "checkout_changed", "remote", "recovery_required")
			if result["durable"] != test.durable || result["checkout_changed"] != false || result["recovery_required"] != test.recovery || result["head"] != nil || result["remote"] != nil {
				t.Fatalf("mutation = %#v", result)
			}
			if refs := result["local_refs"].([]any); len(refs) != 0 {
				t.Fatalf("local_refs = %#v", refs)
			}
		})
	}
}

func runAttachmentJSON(t *testing.T, arguments ...string) wireEnvelope {
	t.Helper()
	app := cli.NewApp()
	RegisterPortable(app)
	RegisterManagedReads(app)
	RegisterAttachments(app)
	return runAttachmentAppJSON(t, app, arguments...)
}

func runAttachmentAppJSON(t *testing.T, app *cli.App, arguments ...string) wireEnvelope {
	t.Helper()
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
