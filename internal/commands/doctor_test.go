package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/ontopix/engram/internal/cli"
	"github.com/ontopix/engram/internal/doctor"
	"github.com/ontopix/engram/internal/gitraw"
	"github.com/ontopix/engram/internal/guard"
)

func TestDoctorCommandClosedJSONAndOutcome(t *testing.T) {
	root := managedFixture(t)
	prepareDoctorIntegration(t, root)

	app := cli.NewApp()
	RegisterDoctor(app)
	var stdout, stderr bytes.Buffer
	status := app.Run(context.Background(), []string{"doctor", root, "--format", "json"}, &stdout, &stderr)
	if status != 0 || stderr.Len() != 0 {
		t.Fatalf("status = %d, stderr = %q", status, stderr.String())
	}
	var envelope wireEnvelope
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	assertEnvelope(t, envelope, "doctor", cli.OutcomeOK, 0)
	result := decodeObject(t, envelope.Result)
	assertExactKeys(t, result, "checks", "recovery")
	checks := result["checks"].([]any)
	want := []string{
		"repository.shape", "identity.binding", "guard.ownership", "initialization.state",
		"acquisition.state", "recovery.state", "replay.state", "presentation.sparse",
		"presentation.transforms", "presentation.roundtrip", "cache.exclusion",
	}
	if len(checks) < len(want) {
		t.Fatalf("checks = %#v", checks)
	}
	for index, name := range want {
		check := checks[index].(map[string]any)
		assertExactKeys(t, check, "name", "class", "status", "path", "detail")
		if check["name"] != name || check["class"] != "required" || check["status"] != "ok" {
			t.Fatalf("check %d = %#v", index, check)
		}
	}
	recovery := result["recovery"].(map[string]any)
	assertExactKeys(t, recovery, "requested", "needed", "performed", "accepted")
	if recovery["requested"] != false || recovery["needed"] != false || recovery["performed"] != false || recovery["accepted"] == nil {
		t.Fatalf("recovery = %#v", recovery)
	}
}

func TestDoctorCommandRequiredFailureIsIssues(t *testing.T) {
	root := managedFixture(t)
	prepareDoctorIntegration(t, root)
	managedGit(t, root, "config", "core.autocrlf", "true")

	app := cli.NewApp()
	RegisterDoctor(app)
	var stdout, stderr bytes.Buffer
	status := app.Run(context.Background(), []string{"--store", root, "doctor", "--format", "json"}, &stdout, &stderr)
	if status != 1 || stderr.Len() != 0 {
		t.Fatalf("status = %d, stderr = %q", status, stderr.String())
	}
	var envelope wireEnvelope
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	assertEnvelope(t, envelope, "doctor", cli.OutcomeIssues, 1)
}

func TestDoctorCommandMissingUnrecognizedTargetIsRepositoryError(t *testing.T) {
	app := cli.NewApp()
	RegisterDoctor(app)
	missing := filepath.Join(t.TempDir(), "missing")
	var stdout, stderr bytes.Buffer
	status := app.Run(context.Background(), []string{"doctor", missing, "--format", "json"}, &stdout, &stderr)
	if status != 2 || stderr.Len() != 0 {
		t.Fatalf("status = %d, stderr = %q", status, stderr.String())
	}
	var envelope wireEnvelope
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	assertEnvelope(t, envelope, "doctor", cli.OutcomeError, 2)
	if envelope.Error == nil || envelope.Error.Kind != cli.ErrorRepository {
		t.Fatalf("error = %#v", envelope.Error)
	}
}

func TestDoctorRecoveryFailureCarriesClosedMutationResult(t *testing.T) {
	result := doctorFailure(&doctor.Failure{
		Kind: doctor.FailureConcurrency, Op: "recover", Err: os.ErrExist,
		Mutation: &doctor.Mutation{Durable: true, RecoveryRequired: true},
	}, "doctor")
	if result.Outcome != cli.OutcomeError || result.Error == nil || result.Error.Kind != cli.ErrorConcurrency {
		t.Fatalf("result = %#v", result)
	}
	mutation, ok := result.Value.(cli.MutationResult)
	if !ok || !mutation.Durable || !mutation.RecoveryRequired || mutation.LocalRefs == nil || mutation.Head != nil || mutation.Remote != nil || mutation.CheckoutChanged {
		t.Fatalf("mutation = %#v", result.Value)
	}
}

func prepareDoctorIntegration(t *testing.T, root string) {
	t.Helper()
	repository, err := gitraw.Discover(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := guard.Install(context.Background(), repository); err != nil {
		t.Fatal(err)
	}
	exclude := filepath.Join(repository.CommonGitDir, "info", "exclude")
	if err := os.MkdirAll(filepath.Dir(exclude), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exclude, []byte(".engram/cache/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}
