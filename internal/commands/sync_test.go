package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/ontopix/engram/internal/checker"
	"github.com/ontopix/engram/internal/cli"
	"github.com/ontopix/engram/internal/syncflow"
)

func TestPushCommandCreatesThenObservesRemoteBranch(t *testing.T) {
	root := managedFixture(t)
	remote := filepath.Join(t.TempDir(), "remote.git")
	command := exec.Command("git", "init", "--bare", "--initial-branch=main", remote)
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("init bare remote: %v\n%s", err, output)
	}
	location := (&url.URL{Scheme: "file", Path: remote}).String()
	managedGit(t, root, "remote", "add", "origin", location)
	managedGit(t, root, "config", "branch.main.remote", "origin")
	managedGit(t, root, "config", "branch.main.merge", "refs/heads/main")

	pushed := runSyncJSON(t, root, "push", "--format", "json")
	assertEnvelope(t, pushed, "push", cli.OutcomeOK, 0)
	result := decodeObject(t, pushed.Result)
	assertExactKeys(t, result, "state", "remote", "remote_ref", "remote_observed", "before", "after", "commits", "changed", "validation", "audits")
	if result["state"] != "pushed" || result["changed"] != true || result["before"] != nil {
		t.Fatalf("first push = %#v", result)
	}

	unchanged := runSyncJSON(t, root, "push", "origin", "main", "--format", "json")
	assertEnvelope(t, unchanged, "push", cli.OutcomeOK, 0)
	result = decodeObject(t, unchanged.Result)
	if result["state"] != "up-to-date" || result["changed"] != false || result["before"] == nil {
		t.Fatalf("second push = %#v", result)
	}
}

func TestPushIndeterminateLocalAuditUsesStatusThree(t *testing.T) {
	result := pushCommandResult(&syncflow.PushResult{
		State: syncflow.PushRejected,
		Validation: checker.Result{
			Target: checker.TargetManagedStore, Status: checker.StatusIndeterminate, Findings: []checker.Finding{},
		},
	})
	if result.Outcome != cli.OutcomeIndeterminate {
		t.Fatalf("result = %#v", result)
	}
}

func runSyncJSON(t *testing.T, root string, arguments ...string) wireEnvelope {
	t.Helper()
	app := cli.NewApp()
	RegisterPortable(app)
	RegisterManagedReads(app)
	RegisterSync(app)
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
