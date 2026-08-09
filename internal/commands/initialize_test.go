package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/ontopix/engram/internal/changeset"
	"github.com/ontopix/engram/internal/checker"
	"github.com/ontopix/engram/internal/cli"
	"github.com/ontopix/engram/internal/guard"
	"github.com/ontopix/engram/internal/initialize"
	"github.com/ontopix/engram/internal/managedread"
)

type initializationRunnerFunc func(context.Context, string, initialize.Options) (initialize.Result, error)

func (f initializationRunnerFunc) Run(ctx context.Context, target string, options initialize.Options) (initialize.Result, error) {
	return f(ctx, target, options)
}

func TestInitializeCommandClosedJSONAndRepeatedSchemas(t *testing.T) {
	app := cli.NewApp()
	registerInitializationWith(app, initializationRunnerFunc(func(_ context.Context, target string, options initialize.Options) (initialize.Result, error) {
		if target != "memory" || options.DryRun || len(options.Schemas) != 2 || options.Schemas[0] != "person" || options.Schemas[1] != "project" {
			t.Fatalf("target/options = %q, %#v", target, options)
		}
		ref, commit := "refs/heads/main", "0123456789012345678901234567890123456789"
		return initialize.Result{
			Root: "memory", Accepted: managedread.GitState{Ref: &ref, Commit: &commit},
			Files: []changeset.Change{}, Launcher: guard.Installed,
			Validation: checker.Result{Target: checker.TargetChangeset, Status: checker.StatusComplete, Findings: []checker.Finding{}},
		}, nil
	}))
	var stdout, stderr bytes.Buffer
	status := app.Run(t.Context(), []string{"init", "memory", "--schema", "person", "--schema", "project", "--format", "json"}, &stdout, &stderr)
	if status != 0 || stderr.Len() != 0 {
		t.Fatalf("status=%d stderr=%q", status, stderr.String())
	}
	var envelope wireEnvelope
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	assertEnvelope(t, envelope, "init", cli.OutcomeOK, 0)
	result := decodeObject(t, envelope.Result)
	assertExactKeys(t, result, "dry_run", "root", "accepted", "files", "launcher", "validation")
	if result["files"] == nil || result["launcher"] != "installed" {
		t.Fatalf("result = %#v", result)
	}
}

func TestInitializeCommandValidationOutcomes(t *testing.T) {
	for _, test := range []struct {
		name    string
		status  checker.Status
		finding []checker.Finding
		outcome cli.Outcome
		exit    int
	}{
		{name: "issues", status: checker.StatusComplete, finding: []checker.Finding{{Code: "E101", Path: "README.md"}}, outcome: cli.OutcomeIssues, exit: 1},
		{name: "indeterminate", status: checker.StatusIndeterminate, finding: []checker.Finding{}, outcome: cli.OutcomeIndeterminate, exit: 3},
	} {
		t.Run(test.name, func(t *testing.T) {
			app := cli.NewApp()
			registerInitializationWith(app, initializationRunnerFunc(func(context.Context, string, initialize.Options) (initialize.Result, error) {
				return initialize.Result{Files: []changeset.Change{}, Validation: checker.Result{Target: checker.TargetChangeset, Status: test.status, Findings: test.finding}}, nil
			}))
			var stdout, stderr bytes.Buffer
			status := app.Run(t.Context(), []string{"init", "--dry-run", "--format", "json"}, &stdout, &stderr)
			if status != test.exit || stderr.Len() != 0 {
				t.Fatalf("status=%d stderr=%q", status, stderr.String())
			}
			var envelope wireEnvelope
			if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			assertEnvelope(t, envelope, "init", test.outcome, test.exit)
		})
	}
}

func TestInitializeRecoveryFailureCarriesClosedMutationResult(t *testing.T) {
	commit := "0123456789012345678901234567890123456789"
	result := initializationFailure(&initialize.Error{
		Kind: initialize.ErrorRecovery, Operation: "publish", Durable: true, Commit: &commit,
		CheckoutChanged: true, RecoveryRequired: true, Underlying: errors.New("fault"),
	}, "initialize")
	if result.Outcome != cli.OutcomeError || result.Error == nil || result.Error.Kind != cli.ErrorOperational {
		t.Fatalf("result = %#v", result)
	}
	mutation, ok := result.Value.(cli.MutationResult)
	if !ok || !mutation.Durable || !mutation.CheckoutChanged || !mutation.RecoveryRequired || len(mutation.LocalRefs) != 1 || mutation.LocalRefs[0].Before != nil || mutation.LocalRefs[0].After == nil || *mutation.LocalRefs[0].After != commit || mutation.Head != nil || mutation.Remote != nil {
		t.Fatalf("mutation = %#v", result.Value)
	}
}
