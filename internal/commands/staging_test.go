package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/ontopix/engram/internal/cli"
)

func TestAddCommandJSON(t *testing.T) {
	root := managedFixture(t)
	appendFile(t, filepath.Join(root, "topics", "why-files.md"), "\nCLI add.\n")
	envelope := runStagingJSON(t, "--store", root, "add", "topics/why-files.md", "--format", "json")
	assertEnvelope(t, envelope, "add", cli.OutcomeOK, 0)
	result := decodeObject(t, envelope.Result)
	assertExactKeys(t, result, "changed", "staged")
	if result["changed"] != true {
		t.Fatalf("result = %#v", result)
	}
	assertChanges(t, result["staged"], []string{"modified:topics/why-files.md"})
}

func TestAddCommandInvalidSelectionIsUsage(t *testing.T) {
	root := managedFixture(t)
	envelope := runStagingJSON(t, "--store", root, "add", "../outside", "--format", "json")
	assertEnvelope(t, envelope, "add", cli.OutcomeError, 2)
	if envelope.Error == nil || envelope.Error.Kind != cli.ErrorUsage {
		t.Fatalf("error = %#v", envelope.Error)
	}
}

func runStagingJSON(t *testing.T, arguments ...string) wireEnvelope {
	t.Helper()
	app := cli.NewApp()
	RegisterPortable(app)
	RegisterManagedReads(app)
	RegisterStaging(app)
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
