package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ontopix/engram/internal/cli"
	"github.com/ontopix/engram/internal/managedread"
)

func TestManagedStatusDiffCheckAndLogJSON(t *testing.T) {
	root := managedFixture(t)
	stagedPath := filepath.Join(root, "topics", "why-files.md")
	appendFile(t, stagedPath, "\nA staged sentence.\n")
	managedGit(t, root, "add", "topics/why-files.md")
	workingPath := filepath.Join(root, "topics", "derived-state.md")
	appendFile(t, workingPath, "\nAn unstaged sentence.\n")

	status := runManagedJSON(t, "--store", root, "status", "--format", "json")
	assertEnvelope(t, status, "status", cli.OutcomeOK, 0)
	statusResult := decodeObject(t, status.Result)
	assertExactKeys(t, statusResult, "mode", "accepted", "candidate_base", "staged", "unstaged", "replay")
	if statusResult["mode"] != "normal" || statusResult["replay"] != nil {
		t.Fatalf("status = %#v", statusResult)
	}
	assertChanges(t, statusResult["staged"], []string{"modified:topics/why-files.md"})
	assertChanges(t, statusResult["unstaged"], []string{"modified:topics/derived-state.md"})

	diff := runManagedJSON(t, "--store", root, "diff", "--staged", "--format", "json")
	assertEnvelope(t, diff, "diff", cli.OutcomeOK, 0)
	diffResult := decodeObject(t, diff.Result)
	assertExactKeys(t, diffResult, "from", "to", "changes", "stat")
	assertChanges(t, diffResult["changes"], []string{"modified:topics/why-files.md"})
	stat := diffResult["stat"].(map[string]any)
	if stat["added"] != float64(0) || stat["modified"] != float64(1) || stat["deleted"] != float64(0) {
		t.Fatalf("stat = %#v", stat)
	}

	accepted := runManagedJSON(t, "--store", root, "check", "--accepted", "--format", "json")
	assertEnvelope(t, accepted, "check", cli.OutcomeOK, 0)
	if result := decodeObject(t, accepted.Result); result["target"] != "managed-state" || result["status"] != "complete" {
		t.Fatalf("accepted check = %#v", result)
	}
	history := runManagedJSON(t, "--store", root, "check", "--history", "--format", "json")
	assertEnvelope(t, history, "check", cli.OutcomeOK, 0)
	if result := decodeObject(t, history.Result); result["target"] != "managed-store" || result["status"] != "complete" {
		t.Fatalf("history check = %#v", result)
	}
	staged := runManagedJSON(t, "--store", root, "check", "--staged", "--format", "json")
	assertEnvelope(t, staged, "check", cli.OutcomeOK, 0)
	if result := decodeObject(t, staged.Result); result["target"] != "changeset" || result["status"] != "complete" {
		t.Fatalf("staged check = %#v", result)
	}

	log := runManagedJSON(t, "--store", root, "log", "-n", "1", "--format", "json")
	assertEnvelope(t, log, "log", cli.OutcomeOK, 0)
	logResult := decodeObject(t, log.Result)
	assertExactKeys(t, logResult, "commits")
	commits := logResult["commits"].([]any)
	if len(commits) != 1 {
		t.Fatalf("commits = %#v", commits)
	}
	commit := commits[0].(map[string]any)
	assertExactKeys(t, commit, "id", "parents", "author", "committer", "authored_at", "committed_at", "message")
	if commit["message"] != "initial\n" || len(commit["id"].(string)) != 40 {
		t.Fatalf("commit = %#v", commit)
	}
}

func TestManagedRevisionGrammarIsTypedUsageError(t *testing.T) {
	root := managedFixture(t)
	envelope := runManagedJSON(t, "--store", root, "diff", "HEAD~1", "--format", "json")
	assertEnvelope(t, envelope, "diff", cli.OutcomeError, 2)
	if envelope.Error == nil || envelope.Error.Kind != cli.ErrorUsage {
		t.Fatalf("error = %#v", envelope.Error)
	}
}

func TestManagedFailureClassifiesChangedCaptureAsConcurrency(t *testing.T) {
	result := managedFailure(&managedread.ConcurrencyError{Operation: "status", Inputs: []string{"index"}}, "inspect")
	if result.Error == nil || result.Error.Kind != cli.ErrorConcurrency {
		t.Fatalf("managed concurrency result = %#v", result)
	}
}

func TestManagedLogAndCheckReportMergeBoundary(t *testing.T) {
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

	log := runManagedJSON(t, "--store", root, "log", "--format", "json")
	assertEnvelope(t, log, "log", cli.OutcomeIssues, 1)
	commits := decodeObject(t, log.Result)["commits"].([]any)
	if len(commits) != 1 || len(commits[0].(map[string]any)["parents"].([]any)) != 2 {
		t.Fatalf("merge log = %#v", commits)
	}
	check := runManagedJSON(t, "--store", root, "check", "--accepted", "--format", "json")
	assertEnvelope(t, check, "check", cli.OutcomeIssues, 1)
	findings := decodeObject(t, check.Result)["findings"].([]any)
	if len(findings) != 1 || findings[0].(map[string]any)["code"] != "E602" {
		t.Fatalf("findings = %#v", findings)
	}
}

func TestManagedAcceptedCheckDoesNotAuditAncestors(t *testing.T) {
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
	appendFile(t, filepath.Join(root, "topics", "why-files.md"), "\nCurrent tip.\n")
	managedGit(t, root, "add", "topics/why-files.md")
	managedGit(t, root, "commit", "--no-verify", "-m", "after merge")

	accepted := runManagedJSON(t, "--store", root, "check", "--accepted", "--format", "json")
	assertEnvelope(t, accepted, "check", cli.OutcomeOK, 0)
	acceptedResult := decodeObject(t, accepted.Result)
	if acceptedResult["target"] != "managed-state" || len(acceptedResult["findings"].([]any)) != 0 {
		t.Fatalf("accepted state check = %#v", acceptedResult)
	}

	history := runManagedJSON(t, "--store", root, "check", "--history", "--format", "json")
	assertEnvelope(t, history, "check", cli.OutcomeIssues, 1)
	historyResult := decodeObject(t, history.Result)
	findings := historyResult["findings"].([]any)
	if historyResult["target"] != "managed-store" || len(findings) != 1 || findings[0].(map[string]any)["code"] != "E602" {
		t.Fatalf("history check = %#v", historyResult)
	}
}

func TestManagedAcceptedCheckDoesNotRequireParentObject(t *testing.T) {
	root := managedFixture(t)
	parent := strings.TrimSpace(managedGit(t, root, "rev-parse", "HEAD"))
	appendFile(t, filepath.Join(root, "topics", "why-files.md"), "\nCurrent tip.\n")
	managedGit(t, root, "add", "topics/why-files.md")
	managedGit(t, root, "commit", "--no-verify", "-m", "current tip")

	parentObject := filepath.Join(root, ".git", "objects", parent[:2], parent[2:])
	if err := os.Remove(parentObject); err != nil {
		t.Fatalf("remove parent commit object: %v", err)
	}

	store, err := managedread.Open(context.Background(), root)
	if err != nil {
		t.Fatalf("open store without parent object: %v", err)
	}
	state, err := store.CheckAcceptedState(context.Background())
	if err != nil || state.Target != "managed-state" || state.Status != "complete" || state.HasErrors() {
		t.Fatalf("CheckAcceptedState = %#v, %v", state, err)
	}

	accepted := runManagedJSON(t, "--store", root, "check", "--accepted", "--format", "json")
	assertEnvelope(t, accepted, "check", cli.OutcomeOK, 0)
	if result := decodeObject(t, accepted.Result); result["target"] != "managed-state" || len(result["findings"].([]any)) != 0 {
		t.Fatalf("accepted state check = %#v", result)
	}

	history := runManagedJSON(t, "--store", root, "check", "--history", "--format", "json")
	assertEnvelope(t, history, "check", cli.OutcomeError, 2)
	if history.Error == nil || history.Error.Kind != cli.ErrorCapability {
		t.Fatalf("history error = %#v", history.Error)
	}
}

func TestManagedAcceptedCheckReportsTipRawPaths(t *testing.T) {
	root := managedFixture(t)
	if err := os.WriteFile(filepath.Join(root, ".private"), []byte("hidden\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	managedGit(t, root, "add", ".private")
	managedGit(t, root, "commit", "--no-verify", "-m", "hidden raw path")

	accepted := runManagedJSON(t, "--store", root, "check", "--accepted", "--format", "json")
	assertEnvelope(t, accepted, "check", cli.OutcomeIssues, 1)
	result := decodeObject(t, accepted.Result)
	findings := result["findings"].([]any)
	if result["target"] != "managed-state" || len(findings) != 1 || findings[0].(map[string]any)["code"] != "E603" {
		t.Fatalf("accepted state check = %#v", result)
	}
}

func TestManagedAcceptedChecksRejectUnbornRepository(t *testing.T) {
	root := t.TempDir()
	managedGit(t, root, "init", "--initial-branch=main")

	for _, mode := range []struct {
		flag   string
		target string
	}{
		{flag: "--accepted", target: "managed-state"},
		{flag: "--history", target: "managed-store"},
	} {
		t.Run(mode.flag, func(t *testing.T) {
			envelope := runManagedJSON(t, "--store", root, "check", mode.flag, "--format", "json")
			assertEnvelope(t, envelope, "check", cli.OutcomeIssues, 1)
			result := decodeObject(t, envelope.Result)
			findings := result["findings"].([]any)
			if result["target"] != mode.target || len(findings) != 1 || findings[0].(map[string]any)["code"] != "E601" {
				t.Fatalf("unborn %s check = %#v", mode.flag, result)
			}
		})
	}
}

func TestManagedAcceptedCheckAttributesInvalidTopologyToE601(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, string) string
	}{
		{
			name: "not a worktree",
			mutate: func(t *testing.T, _ string) string {
				return t.TempDir()
			},
		},
		{
			name: "detached HEAD",
			mutate: func(t *testing.T, root string) string {
				managedGit(t, root, "checkout", "--detach")
				return root
			},
		},
		{
			name: "selected subdirectory",
			mutate: func(_ *testing.T, root string) string {
				return filepath.Join(root, "topics")
			},
		},
		{
			name: "selected symlink",
			mutate: func(t *testing.T, root string) string {
				link := filepath.Join(t.TempDir(), "store-link")
				if err := os.Symlink(root, link); err != nil {
					t.Skipf("symlink unavailable: %v", err)
				}
				return link
			},
		},
		{
			name: "malformed loose accepted ref",
			mutate: func(t *testing.T, root string) string {
				name := filepath.Join(root, ".git", "refs", "heads", "main")
				if err := os.WriteFile(name, []byte("not-an-object-id\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				return root
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := managedFixture(t)
			target := test.mutate(t, root)
			envelope := runManagedJSON(t, "--store", target, "check", "--accepted", "--format", "json")
			assertEnvelope(t, envelope, "check", cli.OutcomeIssues, 1)
			result := decodeObject(t, envelope.Result)
			if result["target"] != "managed-state" || result["status"] != "complete" {
				t.Fatalf("validation = %#v", result)
			}
			findings := result["findings"].([]any)
			if len(findings) != 1 || findings[0].(map[string]any)["code"] != "E601" || findings[0].(map[string]any)["path"] != "." {
				t.Fatalf("findings = %#v", findings)
			}
			if _, err := managedread.Open(context.Background(), target); err == nil {
				t.Fatal("strict managedread.Open accepted invalid check target")
			}
		})
	}
}

func runManagedJSON(t *testing.T, arguments ...string) wireEnvelope {
	t.Helper()
	app := cli.NewApp()
	RegisterPortable(app)
	RegisterManagedReads(app)
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

func managedFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	minimal := filepath.Join(repositoryRoot(t), "examples", "minimal")
	if err := os.CopyFS(root, os.DirFS(minimal)); err != nil {
		t.Fatal(err)
	}
	managedGit(t, root, "init", "--initial-branch=main")
	managedGit(t, root, "config", "user.name", "Ada")
	managedGit(t, root, "config", "user.email", "ada@example.test")
	managedGit(t, root, "config", "commit.gpgsign", "false")
	managedGit(t, root, "add", "--all")
	managedGit(t, root, "commit", "--no-verify", "-m", "initial")
	return root
}

func managedGit(t *testing.T, root string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, arguments...)...)
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(arguments, " "), err, output)
	}
	return string(output)
}

func appendFile(t *testing.T, name, suffix string) {
	t.Helper()
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, append(data, suffix...), 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertChanges(t *testing.T, value any, want []string) {
	t.Helper()
	items, ok := value.([]any)
	if !ok {
		t.Fatalf("changes = %#v", value)
	}
	got := make([]string, len(items))
	for index, item := range items {
		change := item.(map[string]any)
		got[index] = change["operation"].(string) + ":" + change["path"].(string)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("changes = %v, want %v", got, want)
	}
}
