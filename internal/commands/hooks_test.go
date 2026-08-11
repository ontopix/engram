package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ontopix/engram/internal/cli"
	"github.com/ontopix/engram/internal/hooks"
)

func TestHookFailureClassifiesInvalidRevokeNameAsUsage(t *testing.T) {
	result := hookFailure(errors.Join(hooks.ErrInvalidName, errors.New("bad name")), "revoke hook trust")
	if result.Error == nil || result.Error.Kind != cli.ErrorUsage {
		t.Fatalf("result = %#v, want usage error", result)
	}
	if result.Value != nil {
		t.Fatalf("pre-publication result = %#v, want nil", result.Value)
	}
}

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

type fakeHookRegistry struct {
	trustErr  error
	revokeErr error
}

func (*fakeHookRegistry) List(string, hooks.Set) (hooks.Selection, error) {
	return hooks.Selection{}, nil
}

func (f *fakeHookRegistry) Trust(string, hooks.Set) (hooks.Selection, error) {
	return hooks.Selection{}, f.trustErr
}

func (f *fakeHookRegistry) Revoke(string, ...string) (hooks.RevokeResult, error) {
	return hooks.RevokeResult{}, f.revokeErr
}

func TestHookMutationErrorsUseExactProtocolShape(t *testing.T) {
	root := managedFixture(t)
	tests := []struct {
		name     string
		command  string
		durable  bool
		recovery bool
		kind     cli.ErrorKind
		want     string
	}{
		{
			name: "trust renamed before sync", command: "trust", kind: cli.ErrorIO,
			want: `{"durable":false,"local_refs":[],"head":null,"checkout_changed":false,"remote":null,"recovery_required":false}`,
		},
		{
			name: "trust durably published", command: "trust", kind: cli.ErrorIO, durable: true,
			want: `{"durable":true,"local_refs":[],"head":null,"checkout_changed":false,"remote":null,"recovery_required":false}`,
		},
		{
			name: "revoke residual lock", command: "revoke", durable: true, recovery: true, kind: cli.ErrorConcurrency,
			want: `{"durable":true,"local_refs":[],"head":null,"checkout_changed":false,"remote":null,"recovery_required":true}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			underlying := errors.New("injected hook registry failure")
			if test.recovery {
				underlying = errors.Join(hooks.ErrConcurrent, underlying)
			}
			err := &hooks.EffectError{
				Effect: hooks.Effect{Durable: test.durable, RecoveryRequired: test.recovery},
				Err:    underlying,
			}
			registry := &fakeHookRegistry{}
			if test.command == "trust" {
				registry.trustErr = err
			} else {
				registry.revokeErr = err
			}
			app := cli.NewApp()
			RegisterPortable(app)
			RegisterManagedReads(app)
			registerHooksWith(app, registry)
			envelope := runHooksAppJSON(t, app, root, "hooks", test.command, "--format", "json")
			assertEnvelope(t, envelope, "hooks."+test.command, cli.OutcomeError, 2)
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

func runHooksJSON(t *testing.T, root, registry string, arguments ...string) wireEnvelope {
	t.Helper()
	app := cli.NewApp()
	RegisterPortable(app)
	RegisterManagedReads(app)
	RegisterHooksAt(app, registry)
	return runHooksAppJSON(t, app, root, arguments...)
}

func runHooksAppJSON(t *testing.T, app *cli.App, root string, arguments ...string) wireEnvelope {
	t.Helper()
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
