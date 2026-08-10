package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ontopix/engram/internal/checker"
	"github.com/ontopix/engram/internal/cli"
	"github.com/ontopix/engram/internal/gitraw"
	"github.com/ontopix/engram/internal/guard"
	"github.com/ontopix/engram/internal/managedwrite"
)

func TestCommitCommandAcceptsCandidateAndReportsClosedResult(t *testing.T) {
	root := managedFixture(t)
	installManagedGuard(t, root)
	name := filepath.Join(root, "topics", "why-files.md")
	appendFile(t, name, "\nAccepted sentence.\n")
	managedGit(t, root, "add", "topics/why-files.md")
	before := managedGit(t, root, "rev-parse", "HEAD")

	envelope := runAcceptanceJSON(t, root, "commit", "-m", "Accept update", "--format", "json")
	assertEnvelope(t, envelope, "commit", cli.OutcomeOK, 0)
	result := decodeObject(t, envelope.Result)
	assertExactKeys(t, result, "dry_run", "created", "commit", "changes", "validation")
	if result["dry_run"] != false || result["created"] != true || result["commit"] == nil {
		t.Fatalf("commit result = %#v", result)
	}
	assertChanges(t, result["changes"], []string{"modified:topics/why-files.md"})
	validation := result["validation"].(map[string]any)
	if validation["target"] != "changeset" || validation["status"] != "complete" || len(validation["findings"].([]any)) != 0 {
		t.Fatalf("validation = %#v", validation)
	}
	after := managedGit(t, root, "rev-parse", "HEAD")
	if before == after || result["commit"] != trimLF(after) {
		t.Fatalf("before=%q after=%q result=%#v", before, after, result["commit"])
	}
	if status := managedGit(t, root, "status", "--porcelain=v1"); status != "" {
		t.Fatalf("managed acceptance left dirty state: %q", status)
	}

	noop := runAcceptanceJSON(t, root, "commit", "-m", "No change", "--format", "json")
	assertEnvelope(t, noop, "commit", cli.OutcomeOK, 0)
	result = decodeObject(t, noop.Result)
	if result["created"] != false || result["commit"] != nil || result["validation"] != nil {
		t.Fatalf("no-op = %#v", result)
	}
	assertChanges(t, result["changes"], []string{})
}

func TestCommitDryRunNeedsNoGuardMessageOrMutation(t *testing.T) {
	root := managedFixture(t)
	name := filepath.Join(root, "topics", "why-files.md")
	appendFile(t, name, "\nProspective sentence.\n")
	managedGit(t, root, "add", "topics/why-files.md")
	beforeHead := managedGit(t, root, "rev-parse", "HEAD")
	beforeIndex := readFileForTest(t, filepath.Join(root, ".git", "index"))
	beforeWorking := readFileForTest(t, name)

	envelope := runAcceptanceJSON(t, root, "commit", "--dry-run", "--format", "json")
	assertEnvelope(t, envelope, "commit", cli.OutcomeOK, 0)
	result := decodeObject(t, envelope.Result)
	assertExactKeys(t, result, "dry_run", "created", "commit", "changes", "validation")
	if result["dry_run"] != true || result["created"] != false || result["commit"] != nil || result["validation"] == nil {
		t.Fatalf("dry-run = %#v", result)
	}
	if managedGit(t, root, "rev-parse", "HEAD") != beforeHead || !bytes.Equal(readFileForTest(t, filepath.Join(root, ".git", "index")), beforeIndex) || !bytes.Equal(readFileForTest(t, name), beforeWorking) {
		t.Fatal("dry-run changed HEAD, index, or worktree")
	}
	if _, err := os.Lstat(filepath.Join(root, ".git", "hooks", "pre-commit")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dry-run inspected/installed integration unexpectedly: %v", err)
	}
}

func TestRevertCommandCreatesInverseCommitWithDefaultMessage(t *testing.T) {
	root := managedFixture(t)
	name := filepath.Join(root, "topics", "why-files.md")
	original := readFileForTest(t, name)
	appendFile(t, name, "\nSource sentence.\n")
	managedGit(t, root, "add", "topics/why-files.md")
	managedGit(t, root, "commit", "--no-verify", "-m", "source")
	sourceID := trimLF(managedGit(t, root, "rev-parse", "HEAD"))
	installManagedGuard(t, root)

	envelope := runAcceptanceJSON(t, root, "revert", sourceID, "--format", "json")
	assertEnvelope(t, envelope, "revert", cli.OutcomeOK, 0)
	result := decodeObject(t, envelope.Result)
	assertExactKeys(t, result, "dry_run", "created", "commit", "changes", "validation", "reverted", "conflicts")
	if result["created"] != true || result["reverted"] != sourceID || len(result["conflicts"].([]any)) != 0 {
		t.Fatalf("revert = %#v", result)
	}
	if !bytes.Equal(readFileForTest(t, name), original) {
		t.Fatal("revert did not restore the source preimage")
	}
	if message := managedGit(t, root, "log", "-1", "--format=%s"); message != "Revert "+sourceID+"\n" {
		t.Fatalf("default message = %q", message)
	}
}

func TestRevertConflictReportsAllPathsAndChangesNothing(t *testing.T) {
	root := managedFixture(t)
	first := filepath.Join(root, "topics", "why-files.md")
	second := filepath.Join(root, "topics", "derived-state.md")
	appendFile(t, first, "\nSource one.\n")
	appendFile(t, second, "\nSource two.\n")
	managedGit(t, root, "add", "topics/why-files.md", "topics/derived-state.md")
	managedGit(t, root, "commit", "--no-verify", "-m", "source")
	sourceID := trimLF(managedGit(t, root, "rev-parse", "HEAD"))
	appendFile(t, first, "\nLater one.\n")
	appendFile(t, second, "\nLater two.\n")
	managedGit(t, root, "add", "topics/why-files.md", "topics/derived-state.md")
	managedGit(t, root, "commit", "--no-verify", "-m", "later")
	installManagedGuard(t, root)
	beforeHead := managedGit(t, root, "rev-parse", "HEAD")
	beforeFirst := readFileForTest(t, first)
	beforeSecond := readFileForTest(t, second)

	envelope := runAcceptanceJSON(t, root, "revert", sourceID, "--format", "json")
	assertEnvelope(t, envelope, "revert", cli.OutcomeIssues, 1)
	result := decodeObject(t, envelope.Result)
	assertExactKeys(t, result, "dry_run", "created", "commit", "changes", "validation", "reverted", "conflicts")
	if result["changes"] != nil || result["validation"] != nil || result["commit"] != nil || result["created"] != false {
		t.Fatalf("conflict result = %#v", result)
	}
	want := []any{"topics/derived-state.md", "topics/why-files.md"}
	got := result["conflicts"].([]any)
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("conflicts = %#v", got)
	}
	if managedGit(t, root, "rev-parse", "HEAD") != beforeHead || !bytes.Equal(readFileForTest(t, first), beforeFirst) || !bytes.Equal(readFileForTest(t, second), beforeSecond) {
		t.Fatal("conflicting revert changed accepted or working state")
	}
}

func TestManagedWriteRecoveryErrorUsesMutationResult(t *testing.T) {
	base := "1111111111111111111111111111111111111111"
	commit := "2222222222222222222222222222222222222222"
	writer := &fakeAcceptanceWriter{
		commitResult: &managedwrite.Result{Ref: "refs/heads/main", Base: &base},
		commitErr:    &managedwrite.Error{Kind: managedwrite.FailureRecovery, Phase: managedwrite.PhaseRefUpdated, Accepted: true, Commit: commit, Err: managedwrite.ErrPostCAS},
	}
	app := cli.NewApp()
	registerAcceptanceWith(app, writer)
	root := managedFixture(t)
	var stdout, stderr bytes.Buffer
	status := app.Run(context.Background(), []string{"--store", root, "commit", "-m", "message", "--format", "json"}, &stdout, &stderr)
	if status != 2 || stderr.Len() != 0 {
		t.Fatalf("status=%d stderr=%q", status, stderr.String())
	}
	var envelope wireEnvelope
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	assertEnvelope(t, envelope, "commit", cli.OutcomeError, 2)
	if envelope.Error == nil || envelope.Error.Kind != cli.ErrorConflict {
		t.Fatalf("error = %#v", envelope.Error)
	}
	result := decodeObject(t, envelope.Result)
	assertExactKeys(t, result, "durable", "local_refs", "head", "checkout_changed", "remote", "recovery_required")
	if result["durable"] != true || result["recovery_required"] != true || result["checkout_changed"] != false || result["head"] == nil {
		t.Fatalf("mutation = %#v", result)
	}
	refs := result["local_refs"].([]any)
	if len(refs) != 1 || refs[0].(map[string]any)["after"] != commit {
		t.Fatalf("local refs = %#v", refs)
	}
}

func TestManagedWritePreCASDurabilityUsesMutationResult(t *testing.T) {
	result := managedWriteFailure(&managedwrite.Error{
		Kind: managedwrite.FailureRecovery, Phase: managedwrite.PhaseJournalPending,
		Durable: true, RecoveryRequired: true, Err: managedwrite.ErrRecovery,
	}, &managedwrite.Result{Ref: "refs/heads/main"}, "accept candidate")
	if result.Outcome != cli.OutcomeError || result.Error == nil || result.Error.Kind != cli.ErrorConflict {
		t.Fatalf("result = %#v", result)
	}
	mutation, ok := result.Value.(cli.MutationResult)
	if !ok || !mutation.Durable || !mutation.RecoveryRequired || mutation.CheckoutChanged || len(mutation.LocalRefs) != 0 || mutation.Head != nil || mutation.Remote != nil {
		t.Fatalf("mutation = %#v", result.Value)
	}
}

func TestManagedWriteCheckoutEvidenceIsExplicit(t *testing.T) {
	base := "1111111111111111111111111111111111111111"
	commit := "2222222222222222222222222222222222222222"
	result := managedWriteFailure(&managedwrite.Error{
		Kind: managedwrite.FailureRecovery, Phase: managedwrite.PhaseIndexReconciled,
		Durable: true, Accepted: true, CheckoutChanged: true, RecoveryRequired: true,
		Commit: commit, Err: managedwrite.ErrPostCAS,
	}, &managedwrite.Result{Ref: "refs/heads/main", Base: &base}, "accept candidate")
	mutation, ok := result.Value.(cli.MutationResult)
	if !ok || !mutation.Durable || !mutation.CheckoutChanged || !mutation.RecoveryRequired || len(mutation.LocalRefs) != 1 || mutation.Head == nil {
		t.Fatalf("mutation = %#v", result.Value)
	}
}

func TestCommitIndeterminateValidationUsesStatusThreeAndCommandShape(t *testing.T) {
	validation := checker.Result{Target: checker.TargetChangeset, Status: checker.StatusIndeterminate, Findings: []checker.Finding{}}
	writer := &fakeAcceptanceWriter{
		commitResult: &managedwrite.Result{DryRun: true, Changes: nil, Validation: &validation},
		commitErr:    &managedwrite.Error{Kind: managedwrite.FailureValidation, Phase: managedwrite.PhasePrepared, Validation: &validation, Err: managedwrite.ErrValidation},
	}
	app := cli.NewApp()
	registerAcceptanceWith(app, writer)
	root := managedFixture(t)
	var stdout, stderr bytes.Buffer
	status := app.Run(context.Background(), []string{"--store", root, "commit", "--dry-run", "--format", "json"}, &stdout, &stderr)
	if status != 3 || stderr.Len() != 0 {
		t.Fatalf("status=%d stderr=%q", status, stderr.String())
	}
	var envelope wireEnvelope
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	assertEnvelope(t, envelope, "commit", cli.OutcomeIndeterminate, 3)
	result := decodeObject(t, envelope.Result)
	assertExactKeys(t, result, "dry_run", "created", "commit", "changes", "validation")
	if result["validation"].(map[string]any)["findings"] == nil {
		t.Fatalf("validation findings = %#v", result["validation"])
	}
}

func TestRevertInvalidAcceptedAuditStillUsesClosedRevertResult(t *testing.T) {
	root := managedFixture(t)
	managedGit(t, root, "checkout", "-b", "side")
	appendFile(t, filepath.Join(root, "topics", "why-files.md"), "\nSide.\n")
	managedGit(t, root, "add", "topics/why-files.md")
	managedGit(t, root, "commit", "--no-verify", "-m", "side")
	managedGit(t, root, "checkout", "main")
	appendFile(t, filepath.Join(root, "topics", "derived-state.md"), "\nMain.\n")
	managedGit(t, root, "add", "topics/derived-state.md")
	managedGit(t, root, "commit", "--no-verify", "-m", "main")
	managedGit(t, root, "merge", "--no-verify", "--no-ff", "side", "-m", "merge")

	envelope := runAcceptanceJSON(t, root, "revert", "HEAD", "--format", "json")
	assertEnvelope(t, envelope, "revert", cli.OutcomeIssues, 1)
	result := decodeObject(t, envelope.Result)
	assertExactKeys(t, result, "dry_run", "created", "commit", "changes", "validation", "reverted", "conflicts")
	if result["validation"] == nil || result["reverted"] != trimLF(managedGit(t, root, "rev-parse", "HEAD")) || len(result["conflicts"].([]any)) != 0 {
		t.Fatalf("revert audit rejection = %#v", result)
	}
}

func runAcceptanceJSON(t *testing.T, root string, arguments ...string) wireEnvelope {
	t.Helper()
	app := cli.NewApp()
	RegisterPortable(app)
	RegisterManagedReads(app)
	RegisterAcceptanceAt(app, filepath.Join(t.TempDir(), "hook-trust-v1.json"))
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

type fakeAcceptanceWriter struct {
	commitResult *managedwrite.Result
	commitErr    error
	imageResult  *managedwrite.Result
	imageErr     error
}

func (f *fakeAcceptanceWriter) Commit(context.Context, managedwrite.Request) (*managedwrite.Result, error) {
	return f.commitResult, f.commitErr
}

func (f *fakeAcceptanceWriter) CommitImage(context.Context, managedwrite.ImageRequest) (*managedwrite.Result, error) {
	return f.imageResult, f.imageErr
}

func installManagedGuard(t *testing.T, root string) {
	t.Helper()
	repository, err := gitraw.Discover(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := guard.Install(context.Background(), repository); err != nil {
		t.Fatal(err)
	}
}

func readFileForTest(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func trimLF(value string) string {
	return string(bytes.TrimSuffix([]byte(value), []byte("\n")))
}
