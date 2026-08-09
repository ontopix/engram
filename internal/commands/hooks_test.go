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

func TestHooksListTrustAndRevokeJSON(t *testing.T) {
	root := managedFixture(t)
	directory := filepath.Join(root, ".engram", "hooks", "prepare-changeset")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	program := []byte("#!/usr/bin/env sh\nexit 0\n")
	if err := os.WriteFile(filepath.Join(directory, "20-test.sh"), program, 0o755); err != nil {
		t.Fatal(err)
	}
	managedGit(t, root, "add", ".engram/hooks/prepare-changeset/20-test.sh")
	managedGit(t, root, "commit", "--no-verify", "-m", "add hook")
	registry := filepath.Join(t.TempDir(), "controller", "hook-trust-v1.json")

	listed := runHooksJSON(t, root, registry, "hooks", "list", "--format", "json")
	assertEnvelope(t, listed, "hooks.list", cli.OutcomeOK, 0)
	listResult := decodeObject(t, listed.Result)
	assertExactKeys(t, listResult, "state", "changed", "sha256", "trusted", "hooks")
	if listResult["trusted"] != false || len(listResult["hooks"].([]any)) != 1 {
		t.Fatalf("list = %#v", listResult)
	}

	trusted := runHooksJSON(t, root, registry, "hooks", "trust", "--format", "json")
	assertEnvelope(t, trusted, "hooks.trust", cli.OutcomeOK, 0)
	if result := decodeObject(t, trusted.Result); result["changed"] != true || result["trusted"] != true {
		t.Fatalf("trust = %#v", result)
	}

	revoked := runHooksJSON(t, root, registry, "hooks", "revoke", "20-test.sh", "--format", "json")
	assertEnvelope(t, revoked, "hooks.revoke", cli.OutcomeOK, 0)
	revokeResult := decodeObject(t, revoked.Result)
	assertExactKeys(t, revokeResult, "changed", "revoked_sets")
	if revokeResult["changed"] != true || len(revokeResult["revoked_sets"].([]any)) != 1 {
		t.Fatalf("revoke = %#v", revokeResult)
	}
}

func runHooksJSON(t *testing.T, root, registry string, arguments ...string) wireEnvelope {
	t.Helper()
	app := cli.NewApp()
	RegisterPortable(app)
	RegisterManagedReads(app)
	RegisterHooksAt(app, registry)
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
