package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/ontopix/engram/internal/cli"
	"github.com/ontopix/engram/internal/doctor"
	"github.com/ontopix/engram/internal/gitraw"
	"github.com/ontopix/engram/internal/hookexec"
	"github.com/ontopix/engram/internal/hooks"
	"github.com/ontopix/engram/internal/managedread"
	"github.com/ontopix/engram/internal/managedwrite"
	"github.com/ontopix/engram/internal/pullflow"
)

func TestPullCommandFastForwardHasClosedResult(t *testing.T) {
	local, remoteWork, registry := commandPullFixture(t)
	appendFile(t, filepath.Join(remoteWork, "topics", "derived-state.md"), "\nRemote command change.\n")
	managedGit(t, remoteWork, "add", "topics/derived-state.md")
	managedGit(t, remoteWork, "commit", "--no-verify", "-m", "remote")
	managedGit(t, remoteWork, "push", "origin", "main")

	app := cli.NewApp()
	RegisterPortable(app)
	RegisterManagedReads(app)
	RegisterAcceptanceAt(app, registry)
	RegisterPullAt(app, registry)
	envelope := runPullJSON(t, app, local, "pull", "origin", "main", "--format", "json")
	assertEnvelope(t, envelope, "pull", cli.OutcomeOK, 0)
	result := decodeObject(t, envelope.Result)
	assertExactKeys(t, result, "state", "remote", "remote_ref", "before", "after", "fetched", "replayed", "conflicts", "changes", "validation", "candidate_validation", "audits")
	if result["state"] != "fast-forwarded" || result["remote"] != "origin" || result["remote_ref"] != "refs/heads/main" || result["fetched"] != float64(1) || result["replayed"] != float64(0) || len(result["conflicts"].([]any)) != 0 {
		t.Fatalf("pull result = %#v", result)
	}
}

func TestPullConflictMakesStatusReplayAwareAndGuardsManagedWrites(t *testing.T) {
	local, remoteWork, registry := commandPullFixture(t)
	name := filepath.Join("topics", "why-files.md")
	appendFile(t, filepath.Join(local, name), "\nLocal command conflict.\n")
	managedGit(t, local, "add", name)
	managedGit(t, local, "commit", "--no-verify", "-m", "local")
	appendFile(t, filepath.Join(remoteWork, name), "\nRemote command conflict.\n")
	managedGit(t, remoteWork, "add", name)
	managedGit(t, remoteWork, "commit", "--no-verify", "-m", "remote")
	managedGit(t, remoteWork, "push", "origin", "main")
	installManagedGuard(t, local)

	app := cli.NewApp()
	RegisterPortable(app)
	RegisterManagedReads(app)
	RegisterAcceptanceAt(app, registry)
	RegisterPullAt(app, registry)
	conflict := runPullJSON(t, app, local, "pull", "origin", "main", "--format", "json")
	assertEnvelope(t, conflict, "pull", cli.OutcomeIssues, 1)
	if result := decodeObject(t, conflict.Result); result["state"] != "conflict" || len(result["conflicts"].([]any)) != 1 {
		t.Fatalf("conflict = %#v", result)
	}

	status := runPullJSON(t, app, local, "status", "--format", "json")
	assertEnvelope(t, status, "status", cli.OutcomeOK, 0)
	statusResult := decodeObject(t, status.Result)
	if statusResult["mode"] != "pull-replay" || statusResult["replay"] == nil {
		t.Fatalf("replay status = %#v", statusResult)
	}
	replay := statusResult["replay"].(map[string]any)
	assertExactKeys(t, replay, "original", "private", "base", "reason", "conflicts")
	if replay["reason"] != "conflict" {
		t.Fatalf("status replay = %#v", replay)
	}
	for _, arguments := range [][]string{{"commit", "-m", "forbidden"}, {"revert", "HEAD"}, {"pull", "origin", "main"}} {
		envelope := runPullJSON(t, app, local, append(arguments, "--format", "json")...)
		assertEnvelope(t, envelope, arguments[0], cli.OutcomeError, 2)
		if envelope.Error == nil || envelope.Error.Kind != cli.ErrorConflict {
			t.Fatalf("%v error = %#v", arguments, envelope.Error)
		}
	}

	aborted := runPullJSON(t, app, local, "pull", "--abort", "--format", "json")
	assertEnvelope(t, aborted, "pull", cli.OutcomeOK, 0)
	if result := decodeObject(t, aborted.Result); result["state"] != "aborted" {
		t.Fatalf("abort = %#v", result)
	}
}

func TestReplayGuardsCanBeReappliedAfterHandlerReplacement(t *testing.T) {
	local, remoteWork, registry := commandPullFixture(t)
	name := filepath.Join("topics", "why-files.md")
	appendFile(t, filepath.Join(local, name), "\nLocal order conflict.\n")
	managedGit(t, local, "add", name)
	managedGit(t, local, "commit", "--no-verify", "-m", "local")
	appendFile(t, filepath.Join(remoteWork, name), "\nRemote order conflict.\n")
	managedGit(t, remoteWork, "add", name)
	managedGit(t, remoteWork, "commit", "--no-verify", "-m", "remote")
	managedGit(t, remoteWork, "push", "origin", "main")
	installManagedGuard(t, local)

	app := cli.NewApp()
	RegisterPortable(app)
	RegisterManagedReads(app)
	RegisterPullAt(app, registry)
	RegisterAcceptanceAt(app, registry) // Intentionally replaces the first guard.
	RegisterReplayGuards(app)
	conflict := runPullJSON(t, app, local, "pull", "origin", "main", "--format", "json")
	assertEnvelope(t, conflict, "pull", cli.OutcomeIssues, 1)
	blocked := runPullJSON(t, app, local, "commit", "-m", "forbidden", "--format", "json")
	assertEnvelope(t, blocked, "commit", cli.OutcomeError, 2)
	if blocked.Error == nil || blocked.Error.Kind != cli.ErrorConflict {
		t.Fatalf("guard after replacement = %#v", blocked.Error)
	}
}

func TestDoctorRecognizesAndRecoversPullTransitionThroughAdapter(t *testing.T) {
	local, remoteWork, registryPath := commandPullFixture(t)
	installManagedGuard(t, local)
	healthyRepository, err := gitraw.Discover(t.Context(), local)
	if err != nil {
		t.Fatal(err)
	}
	exclude := filepath.Join(healthyRepository.CommonGitDir, "info", "exclude")
	if err := os.WriteFile(exclude, []byte(".engram/cache/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	appendFile(t, filepath.Join(remoteWork, "topics", "derived-state.md"), "\nDoctor pull recovery.\n")
	managedGit(t, remoteWork, "add", "topics/derived-state.md")
	managedGit(t, remoteWork, "commit", "--no-verify", "-m", "remote recovery")
	managedGit(t, remoteWork, "push", "origin", "main")

	registry, err := hooks.NewRegistry(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	puller := pullflow.New(managedwrite.New(hookexec.New(registry)))
	puller.Fault = func(phase pullflow.Phase) error {
		if phase == pullflow.PhaseRefUpdated {
			return errors.New("interrupt pull after ref update")
		}
		return nil
	}
	app := cli.NewApp()
	RegisterPortable(app)
	RegisterManagedReads(app)
	registerPullWith(app, puller)
	RegisterDoctorWithRecovery(app, doctor.RecoveryFunc(func(ctx context.Context, request doctor.RecoveryRequest) (doctor.RecoveryResponse, error) {
		result, err := puller.Recover(ctx, request.Target)
		response := doctor.RecoveryResponse{Durable: result != nil && result.Performed}
		if err != nil {
			return response, err
		}
		repository, discoverErr := gitraw.Discover(ctx, request.Target)
		if discoverErr != nil {
			return response, discoverErr
		}
		accepted := managedread.GitState{Ref: stringPointerForCommand(repository.HeadRef)}
		if repository.Head != nil {
			accepted.Commit = stringPointerForCommand(repository.Head.String())
		}
		response.Accepted = &accepted
		return response, nil
	}))

	failed := runPullJSON(t, app, local, "pull", "origin", "main", "--format", "json")
	assertEnvelope(t, failed, "pull", cli.OutcomeError, 2)
	puller.Fault = nil
	before := runPullJSON(t, app, local, "doctor", "--format", "json")
	assertEnvelope(t, before, "doctor", cli.OutcomeIssues, 1)
	beforeResult := decodeObject(t, before.Result)
	beforeRecovery := beforeResult["recovery"].(map[string]any)
	if beforeRecovery["needed"] != true || beforeRecovery["performed"] != false {
		t.Fatalf("pull recovery before doctor = %#v", beforeRecovery)
	}

	after := runPullJSON(t, app, local, "doctor", "--recover", "--format", "json")
	assertEnvelope(t, after, "doctor", cli.OutcomeOK, 0)
	afterResult := decodeObject(t, after.Result)
	afterRecovery := afterResult["recovery"].(map[string]any)
	if afterRecovery["needed"] != true || afterRecovery["performed"] != true {
		t.Fatalf("pull recovery after doctor = %#v", afterRecovery)
	}
	checks := afterResult["checks"].([]any)
	if len(checks) < 11 {
		t.Fatalf("doctor checks = %d", len(checks))
	}
	recoveryCheck := false
	for _, raw := range checks {
		check := raw.(map[string]any)
		if check["name"] == "recovery.state" {
			recoveryCheck = true
			if check["status"] != "ok" {
				t.Fatalf("recovery check after pull recovery = %#v", check)
			}
		}
	}
	if !recoveryCheck {
		t.Fatal("doctor omitted recovery.state")
	}
}

func commandPullFixture(t *testing.T) (local, remoteWork, registry string) {
	t.Helper()
	local = managedFixture(t)
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	command := exec.Command("git", "init", "--bare", "--initial-branch=main", remote)
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("init bare remote: %v\n%s", err, output)
	}
	location := (&url.URL{Scheme: "file", Path: remote}).String()
	managedGit(t, local, "remote", "add", "origin", location)
	managedGit(t, local, "config", "branch.main.remote", "origin")
	managedGit(t, local, "config", "branch.main.merge", "refs/heads/main")
	managedGit(t, local, "push", "--no-verify", "origin", "main")
	remoteWork = filepath.Join(root, "remote-work")
	command = exec.Command("git", "clone", location, remoteWork)
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("clone remote worktree: %v\n%s", err, output)
	}
	managedGit(t, remoteWork, "config", "user.name", "Ada")
	managedGit(t, remoteWork, "config", "user.email", "ada@example.test")
	managedGit(t, remoteWork, "config", "commit.gpgsign", "false")
	registry = filepath.Join(root, "config", "hook-trust-v1.json")
	return local, remoteWork, registry
}

func runPullJSON(t *testing.T, app *cli.App, root string, arguments ...string) wireEnvelope {
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
		t.Fatalf("process status=%d envelope=%d", status, envelope.ExitStatus)
	}
	return envelope
}
