package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/ontopix/engram/internal/cli"
)

type wireEnvelope struct {
	Version    int                `json:"version"`
	Command    *string            `json:"command"`
	Outcome    cli.Outcome        `json:"outcome"`
	ExitStatus int                `json:"exit_status"`
	Result     json.RawMessage    `json:"result"`
	Error      *cli.ProtocolError `json:"error"`
}

func TestCheckExplicitMinimalSnapshot(t *testing.T) {
	root := repositoryRoot(t)
	envelope := runJSON(t, context.Background(), "--format", "json", "check", filepath.Join(root, "examples", "minimal"))
	assertEnvelope(t, envelope, "check", cli.OutcomeOK, 0)
	if envelope.Error != nil {
		t.Fatalf("error = %#v", envelope.Error)
	}
	result := decodeObject(t, envelope.Result)
	assertExactKeys(t, result, "target", "status", "findings")
	if result["target"] != "snapshot" || result["status"] != "complete" {
		t.Fatalf("result = %#v", result)
	}
	if findings, ok := result["findings"].([]any); !ok || len(findings) != 0 {
		t.Fatalf("findings = %#v", result["findings"])
	}
}

func TestCheckStoreSelectionAndDiscovery(t *testing.T) {
	root := repositoryRoot(t)
	minimal := filepath.Join(root, "examples", "minimal")

	selected := runJSON(t, context.Background(), "--store", minimal, "check", "--format", "json")
	assertEnvelope(t, selected, "check", cli.OutcomeOK, 0)

	before, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(filepath.Join(minimal, "topics")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(before); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})
	discovered := runJSON(t, context.Background(), "check", "--format", "json")
	assertEnvelope(t, discovered, "check", cli.OutcomeOK, 0)
}

func TestCheckIssuesOutcome(t *testing.T) {
	empty := t.TempDir()
	envelope := runJSON(t, context.Background(), "check", empty, "--format", "json")
	assertEnvelope(t, envelope, "check", cli.OutcomeIssues, 1)
	result := decodeObject(t, envelope.Result)
	if result["target"] != "snapshot" || result["status"] != "complete" {
		t.Fatalf("result = %#v", result)
	}
	findings, ok := result["findings"].([]any)
	if !ok || len(findings) == 0 {
		t.Fatalf("findings = %#v", result["findings"])
	}
}

func TestCheckWarningsAloneRemainOK(t *testing.T) {
	root := repositoryRoot(t)
	minimal := filepath.Join(root, "examples", "minimal")
	store := filepath.Join(t.TempDir(), "store")
	if err := os.CopyFS(store, os.DirFS(minimal)); err != nil {
		t.Fatal(err)
	}
	oldName := filepath.Join(store, "topics", "why-files.md")
	newName := filepath.Join(store, "topics", "WHY-FILES.md")
	if err := os.Rename(oldName, newName); err != nil {
		t.Fatal(err)
	}
	readmeName := filepath.Join(store, "topics", "README.md")
	readme, err := os.ReadFile(readmeName)
	if err != nil {
		t.Fatal(err)
	}
	readme = bytes.Replace(readme,
		[]byte("- [derived-state](derived-state.md) — Indexes and caches are rebuildable projections; the files stay the truth.\n- [why-files](why-files.md) — Why this store is plain markdown files instead of a database.\n"),
		[]byte("- [WHY-FILES](WHY-FILES.md) — Why this store is plain markdown files instead of a database.\n- [derived-state](derived-state.md) — Indexes and caches are rebuildable projections; the files stay the truth.\n"), 1)
	if err := os.WriteFile(readmeName, readme, 0o644); err != nil {
		t.Fatal(err)
	}

	envelope := runJSON(t, context.Background(), "check", store, "--format", "json")
	assertEnvelope(t, envelope, "check", cli.OutcomeOK, 0)
	result := decodeObject(t, envelope.Result)
	findings := result["findings"].([]any)
	if len(findings) != 1 || findings[0].(map[string]any)["code"] != "W901" {
		t.Fatalf("findings = %#v", findings)
	}
}

func TestCheckSnapshotPairCompleteAndIndeterminate(t *testing.T) {
	root := repositoryRoot(t)
	minimal := filepath.Join(root, "examples", "minimal")
	complete := runJSON(t, context.Background(), "check", "--base", minimal, "--candidate", minimal, "--format", "json")
	assertEnvelope(t, complete, "check", cli.OutcomeOK, 0)
	completeResult := decodeObject(t, complete.Result)
	if completeResult["target"] != "changeset" || completeResult["status"] != "complete" {
		t.Fatalf("complete result = %#v", completeResult)
	}

	base := t.TempDir()
	if err := os.WriteFile(filepath.Join(base, "bad name"), []byte("boundary\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	indeterminate := runJSON(t, context.Background(), "check", "--base", base, "--candidate", minimal, "--format", "json")
	assertEnvelope(t, indeterminate, "check", cli.OutcomeIndeterminate, 3)
	indeterminateResult := decodeObject(t, indeterminate.Result)
	if indeterminateResult["target"] != "changeset" || indeterminateResult["status"] != "indeterminate" {
		t.Fatalf("indeterminate result = %#v", indeterminateResult)
	}
}

func TestCheckManagedFormsAreTypedCapabilities(t *testing.T) {
	for _, option := range []string{"--accepted", "--history", "--staged"} {
		envelope := runJSON(t, context.Background(), "check", option, "--format", "json")
		assertEnvelope(t, envelope, "check", cli.OutcomeError, 2)
		if envelope.Error == nil || envelope.Error.Kind != cli.ErrorCapability {
			t.Fatalf("%s error = %#v", option, envelope.Error)
		}
		if result := decodeObject(t, envelope.Result); len(result) != 0 {
			t.Fatalf("%s result = %#v, want {}", option, result)
		}
	}
}

func TestCheckCancellationIsTyped(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	envelope := runJSON(t, ctx, "check", "--format", "json")
	assertEnvelope(t, envelope, "check", cli.OutcomeError, 2)
	if envelope.Error == nil || envelope.Error.Kind != cli.ErrorCancelled {
		t.Fatalf("error = %#v", envelope.Error)
	}
}

func TestSchemaInventoryShapeAndOrdering(t *testing.T) {
	envelope := runJSON(t, context.Background(), "schema", "inventory", "--format", "json")
	assertEnvelope(t, envelope, "schema.inventory", cli.OutcomeOK, 0)
	result := decodeObject(t, envelope.Result)
	assertExactKeys(t, result, "schemas")
	items, ok := result["schemas"].([]any)
	if !ok || len(items) != 5 {
		t.Fatalf("schemas = %#v", result["schemas"])
	}
	var types []string
	for index, item := range items {
		description, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("schema %d = %#v", index, item)
		}
		assertExactKeys(t, description, "type", "source", "path", "version", "description")
		if description["source"] != "inventory" || description["path"] != nil || description["version"] != float64(1) || description["description"] == "" {
			t.Fatalf("schema %d = %#v", index, description)
		}
		types = append(types, description["type"].(string))
	}
	want := []string{"fact", "journal-entry", "note", "person", "project"}
	if !reflect.DeepEqual(types, want) {
		t.Fatalf("types = %v, want %v", types, want)
	}
}

func TestSchemaListAndShowMinimalFixture(t *testing.T) {
	root := repositoryRoot(t)
	minimal := filepath.Join(root, "examples", "minimal")

	listed := runJSON(t, context.Background(), "--store", minimal, "schema", "list", "--at", "topics", "--format", "json")
	assertEnvelope(t, listed, "schema.list", cli.OutcomeOK, 0)
	listResult := decodeObject(t, listed.Result)
	assertExactKeys(t, listResult, "schemas")
	items := listResult["schemas"].([]any)
	if len(items) != 1 {
		t.Fatalf("schemas = %#v", items)
	}
	note := items[0].(map[string]any)
	assertExactKeys(t, note, "type", "source", "path", "version", "description")
	if note["type"] != "note" || note["source"] != "local" || note["path"] != ".engram/schemas/note.md" || note["version"] != float64(1) {
		t.Fatalf("note = %#v", note)
	}

	shown := runJSON(t, context.Background(), "--store", minimal, "schema", "show", "note", "--at", "topics", "--format", "json")
	assertEnvelope(t, shown, "schema.show", cli.OutcomeOK, 0)
	showResult := decodeObject(t, shown.Result)
	assertExactKeys(t, showResult, "schema", "content")
	if !reflect.DeepEqual(showResult["schema"], note) {
		t.Fatalf("show schema = %#v, list schema = %#v", showResult["schema"], note)
	}
	wantContent, err := os.ReadFile(filepath.Join(minimal, ".engram", "schemas", "note.md"))
	if err != nil {
		t.Fatal(err)
	}
	if showResult["content"] != string(wantContent) {
		t.Fatal("schema.show content is not byte-exact")
	}
}

func TestSchemaAtUsesLocalResolution(t *testing.T) {
	root := repositoryRoot(t)
	minimal := filepath.Join(root, "examples", "minimal")
	store := filepath.Join(t.TempDir(), "store")
	if err := os.CopyFS(store, os.DirFS(minimal)); err != nil {
		t.Fatal(err)
	}
	localDirectory := filepath.Join(store, "topics", ".engram", "schemas")
	if err := os.MkdirAll(localDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	note, err := os.ReadFile(filepath.Join(minimal, ".engram", "schemas", "note.md"))
	if err != nil {
		t.Fatal(err)
	}
	local := bytes.ReplaceAll(note, []byte("note"), []byte("local-note"))
	if err := os.WriteFile(filepath.Join(localDirectory, "local-note.md"), local, 0o644); err != nil {
		t.Fatal(err)
	}

	rootList := runJSON(t, context.Background(), "--store", store, "schema", "list", "--format", "json")
	rootTypes := schemaTypes(t, rootList)
	if !reflect.DeepEqual(rootTypes, []string{"note"}) {
		t.Fatalf("root types = %v", rootTypes)
	}
	nestedList := runJSON(t, context.Background(), "--store", store, "schema", "list", "--at", "topics", "--format", "json")
	nestedTypes := schemaTypes(t, nestedList)
	if !reflect.DeepEqual(nestedTypes, []string{"local-note", "note"}) {
		t.Fatalf("nested types = %v", nestedTypes)
	}
	shown := runJSON(t, context.Background(), "--store", store, "schema", "show", "local-note", "--at", "topics", "--format", "json")
	assertEnvelope(t, shown, "schema.show", cli.OutcomeOK, 0)
	result := decodeObject(t, shown.Result)
	description := result["schema"].(map[string]any)
	if description["path"] != "topics/.engram/schemas/local-note.md" {
		t.Fatalf("schema path = %#v", description["path"])
	}
}

func TestPortableHandlerErrorKinds(t *testing.T) {
	root := repositoryRoot(t)
	minimal := filepath.Join(root, "examples", "minimal")
	tests := []struct {
		name    string
		args    []string
		command string
		kind    cli.ErrorKind
	}{
		{"invalid schema type", []string{"--store", minimal, "schema", "show", "Bad-Type", "--format", "json"}, "schema.show", cli.ErrorUsage},
		{"missing schema", []string{"--store", minimal, "schema", "show", "missing", "--format", "json"}, "schema.show", cli.ErrorRepository},
		{"invalid at", []string{"--store", minimal, "schema", "list", "--at", "../topics", "--format", "json"}, "schema.list", cli.ErrorUsage},
		{"missing at", []string{"--store", minimal, "schema", "list", "--at", "missing", "--format", "json"}, "schema.list", cli.ErrorRepository},
		{"missing snapshot", []string{"check", filepath.Join(t.TempDir(), "absent"), "--format", "json"}, "check", cli.ErrorRepository},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			envelope := runJSON(t, context.Background(), test.args...)
			assertEnvelope(t, envelope, test.command, cli.OutcomeError, 2)
			if envelope.Error == nil || envelope.Error.Kind != test.kind {
				t.Fatalf("error = %#v, want kind %q", envelope.Error, test.kind)
			}
		})
	}
}

func TestRegisterPortableIsIdempotentAndHandlesNilMap(t *testing.T) {
	RegisterPortable(nil)
	app := &cli.App{}
	RegisterPortable(app)
	RegisterPortable(app)
	for _, command := range []cli.CommandName{cli.CommandCheck, cli.CommandSchemaInventory, cli.CommandSchemaList, cli.CommandSchemaShow} {
		if app.Handlers[command] == nil {
			t.Errorf("handler %q is not registered", command)
		}
	}
}

func runJSON(t *testing.T, ctx context.Context, arguments ...string) wireEnvelope {
	t.Helper()
	app := cli.NewApp()
	RegisterPortable(app)
	var stdout, stderr bytes.Buffer
	status := app.Run(ctx, arguments, &stdout, &stderr)
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

func assertEnvelope(t *testing.T, envelope wireEnvelope, command string, outcome cli.Outcome, status int) {
	t.Helper()
	if envelope.Version != cli.ProtocolVersion || envelope.Command == nil || *envelope.Command != command || envelope.Outcome != outcome || envelope.ExitStatus != status {
		t.Fatalf("envelope = %#v", envelope)
	}
	if outcome != cli.OutcomeError && envelope.Error != nil {
		t.Fatalf("non-error envelope error = %#v", envelope.Error)
	}
}

func decodeObject(t *testing.T, source json.RawMessage) map[string]any {
	t.Helper()
	var result map[string]any
	if err := json.Unmarshal(source, &result); err != nil {
		t.Fatalf("decode result %q: %v", source, err)
	}
	return result
}

func assertExactKeys(t *testing.T, object map[string]any, keys ...string) {
	t.Helper()
	got := make([]string, 0, len(object))
	for key := range object {
		got = append(got, key)
	}
	sort.Strings(got)
	want := append([]string(nil), keys...)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("keys = %v, want %v", got, want)
	}
}

func schemaTypes(t *testing.T, envelope wireEnvelope) []string {
	t.Helper()
	assertEnvelope(t, envelope, "schema.list", cli.OutcomeOK, 0)
	result := decodeObject(t, envelope.Result)
	items := result["schemas"].([]any)
	types := make([]string, len(items))
	for index := range items {
		types[index] = items[index].(map[string]any)["type"].(string)
	}
	return types
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fs.Stat(os.DirFS(root), "examples/minimal/.engram/root.yaml"); err != nil {
		t.Fatalf("locate repository root: %v", err)
	}
	return root
}
