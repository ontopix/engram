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

	"github.com/ontopix/engram/internal/cli"
	"github.com/ontopix/engram/internal/draft"
)

func TestDraftCommandsJSONAndStandardInput(t *testing.T) {
	root := managedFixture(t)
	record := filepath.Join(root, "topics", "derived-state.md")
	data, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	data = bytes.Replace(data,
		[]byte("description: \"Indexes and caches are rebuildable projections; the files stay the truth.\""),
		[]byte("description: \"Derived indexes remain disposable.\""), 1)
	if err := os.WriteFile(record, data, 0o644); err != nil {
		t.Fatal(err)
	}

	checked := runDraftJSON(t, root, nil, "fmt", "topics", "--check", "--format", "json")
	assertEnvelope(t, checked, "fmt", cli.OutcomeIssues, 1)
	checkResult := decodeObject(t, checked.Result)
	assertExactKeys(t, checkResult, "dry_run", "check", "changed", "paths")
	if checkResult["changed"] != true {
		t.Fatalf("fmt check = %#v", checkResult)
	}

	formatted := runDraftJSON(t, root, nil, "fmt", "topics", "--format", "json")
	assertEnvelope(t, formatted, "fmt", cli.OutcomeOK, 0)
	if decodeObject(t, formatted.Result)["changed"] != true {
		t.Fatal("fmt did not publish catalog update")
	}

	body := "# Standard input\n\nRemembered exactly.\n"
	created := runDraftJSON(t, root, strings.NewReader(body), "new", "note", "topics/stdin.md",
		"--description", "A record read from standard input.", "--body", "-", "--format", "json")
	assertEnvelope(t, created, "new", cli.OutcomeOK, 0)
	newResult := decodeObject(t, created.Result)
	assertExactKeys(t, newResult, "dry_run", "changed", "record", "catalogs")
	if newResult["record"] != "topics/stdin.md" {
		t.Fatalf("new = %#v", newResult)
	}
	createdBytes, err := os.ReadFile(filepath.Join(root, "topics", "stdin.md"))
	if err != nil || !bytes.HasSuffix(createdBytes, []byte(body)) {
		t.Fatalf("created bytes: %v %q", err, createdBytes)
	}

	copied := runDraftJSON(t, root, nil, "schema", "copy", "person", "--format", "json")
	assertEnvelope(t, copied, "schema.copy", cli.OutcomeOK, 0)
	copyResult := decodeObject(t, copied.Result)
	assertExactKeys(t, copyResult, "dry_run", "changed", "schema", "path")
	if copyResult["path"] != ".engram/schemas/person.md" {
		t.Fatalf("copy = %#v", copyResult)
	}

	moved := runDraftJSON(t, root, nil, "mv", "topics/stdin.md", "topics/stdin-moved.md", "--format", "json")
	assertEnvelope(t, moved, "mv", cli.OutcomeOK, 0)
	moveResult := decodeObject(t, moved.Result)
	assertExactKeys(t, moveResult, "dry_run", "changed", "from", "to", "paths", "catalogs")
	if moveResult["from"] != "topics/stdin.md" || moveResult["to"] != "topics/stdin-moved.md" {
		t.Fatalf("mv = %#v", moveResult)
	}
	if _, err := os.Stat(filepath.Join(root, "topics", "stdin.md")); !os.IsNotExist(err) {
		t.Fatalf("source still exists: %v", err)
	}
	if movedBytes, err := os.ReadFile(filepath.Join(root, "topics", "stdin-moved.md")); err != nil || !bytes.HasSuffix(movedBytes, []byte(body)) {
		t.Fatalf("moved bytes: %v %q", err, movedBytes)
	}
}

func TestDraftMutationFailureHasExactClosedJSON(t *testing.T) {
	result := draftFailure(&draft.Error{
		Kind: draft.ErrorConflict, Operation: "fmt", Err: errors.New("fault"),
		Mutation: &draft.Mutation{Durable: true, CheckoutChanged: true, RecoveryRequired: true},
	}, "format catalogs")
	if result.Outcome != cli.OutcomeError || result.Error == nil || result.Error.Kind != cli.ErrorConflict {
		t.Fatalf("result = %#v", result)
	}
	encoded, err := json.Marshal(result.Value)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"durable":true,"local_refs":[],"head":null,"checkout_changed":true,"remote":null,"recovery_required":true}`
	if string(encoded) != want {
		t.Fatalf("mutation JSON = %s, want %s", encoded, want)
	}
}

func TestDraftRendezvousReleaseReportsExactResidualState(t *testing.T) {
	injected := errors.New("injected release failure")
	for _, test := range []struct {
		name     string
		residual bool
	}{
		{name: "removed lock", residual: false},
		{name: "residual lock", residual: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := releaseDraftRendezvous(fakeDraftRendezvousHandle{mutated: true, recoveryRequired: test.residual, err: injected})
			mutation, present := draft.MutationOf(err)
			if !errors.Is(err, injected) || !present || !mutation.Durable || mutation.CheckoutChanged || mutation.RecoveryRequired != test.residual {
				t.Fatalf("error = %v, mutation = %#v, present = %t", err, mutation, present)
			}
			result := draftFailure(err, "format catalogs")
			closed, ok := result.Value.(cli.MutationResult)
			if !ok || !closed.Durable || closed.CheckoutChanged || closed.RecoveryRequired != test.residual {
				t.Fatalf("command result = %#v", result)
			}
		})
	}
	err := releaseDraftRendezvous(fakeDraftRendezvousHandle{mutated: true, recoveryRequired: true})
	mutation, present := draft.MutationOf(err)
	if err == nil || !present || !mutation.Durable || !mutation.RecoveryRequired {
		t.Fatalf("silent residual release error = %v, mutation = %#v, present = %t", err, mutation, present)
	}
}

type fakeDraftRendezvousHandle struct {
	mutated          bool
	recoveryRequired bool
	err              error
}

func (h fakeDraftRendezvousHandle) Release() error { return h.err }
func (h fakeDraftRendezvousHandle) Mutated() bool  { return h.mutated }
func (h fakeDraftRendezvousHandle) RecoveryRequired() bool {
	return h.recoveryRequired
}

func runDraftJSON(t *testing.T, root string, stdin *strings.Reader, arguments ...string) wireEnvelope {
	t.Helper()
	app := cli.NewApp()
	if stdin != nil {
		app.Stdin = stdin
	}
	RegisterPortable(app)
	RegisterManagedReads(app)
	RegisterDraft(app)
	arguments = append([]string{"--store", root}, arguments...)
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
