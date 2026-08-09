package syncflow

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ontopix/engram/internal/checker"
	"github.com/ontopix/engram/internal/gitraw"
	"github.com/ontopix/engram/internal/managedread"
	"github.com/ontopix/engram/internal/remoteselect"
)

func TestPushCreatesThenObservesAndAdvancesExactRemoteBranch(t *testing.T) {
	fixture := newPushFixture(t, true)
	store := openTestStore(t, fixture.local)

	marker := filepath.Join(t.TempDir(), "pre-push-ran")
	installHook(t, filepath.Join(fixture.local, ".git", "hooks", "pre-push"), "#!/bin/sh\n: >"+shellQuote(marker)+"\nexit 1\n")
	t.Setenv("GIT_DIR", filepath.Join(t.TempDir(), "poison.git"))
	t.Setenv("ENGRAM_TEST_POISON", "present")

	created, err := Push(t.Context(), store, "origin", "main")
	if err != nil {
		t.Fatalf("create remote branch: %v", err)
	}
	assertPushResult(t, created, PushPushed, true, true, 1)
	if created.Before != nil || created.After != fixture.tip(t) {
		t.Fatalf("creation before/after = %v / %q", created.Before, created.After)
	}
	if _, err := os.Stat(marker); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("native pre-push hook was not suppressed: %v", err)
	}
	if got := fixture.remoteTip(t); got != created.After {
		t.Fatalf("created remote tip = %q, want %q", got, created.After)
	}

	current, err := Push(t.Context(), openTestStore(t, fixture.local), "", "")
	if err != nil {
		t.Fatalf("observe up-to-date branch: %v", err)
	}
	assertPushResult(t, current, PushUpToDate, true, false, 0)
	if current.Before == nil || *current.Before != current.After {
		t.Fatalf("up-to-date before/after = %v / %q", current.Before, current.After)
	}

	before := current.After
	appendFile(t, filepath.Join(fixture.local, "topics", "why-files.md"), "\nA local accepted change.\n")
	fixture.commit(t, fixture.local, "advance")
	advanced, err := Push(t.Context(), openTestStore(t, fixture.local), "origin", "main")
	if err != nil {
		t.Fatalf("advance remote branch: %v", err)
	}
	assertPushResult(t, advanced, PushPushed, true, true, 1)
	if advanced.Before == nil || *advanced.Before != before || advanced.After != fixture.tip(t) {
		t.Fatalf("advance before/after = %v / %q", advanced.Before, advanced.After)
	}
	if got := fixture.remoteTip(t); got != advanced.After {
		t.Fatalf("advanced remote tip = %q, want %q", got, advanced.After)
	}
}

func TestPushRejectsRemoteTipOutsideAuditedLocalLineageWithoutUpdate(t *testing.T) {
	fixture := newPushFixture(t, true)
	first, err := Push(t.Context(), openTestStore(t, fixture.local), "origin", "main")
	if err != nil || first.State != PushPushed {
		t.Fatalf("initial push = %#v, %v", first, err)
	}

	other := filepath.Join(fixture.root, "other")
	testGit(t, fixture.root, "clone", fixture.remoteURL, other)
	configureIdentity(t, other)
	appendFile(t, filepath.Join(other, "topics", "derived-state.md"), "\nRemote-only accepted change.\n")
	fixture.commit(t, other, "remote divergence")
	testGit(t, other, "push", "origin", "main")
	remoteTip := fixture.remoteTip(t)

	appendFile(t, filepath.Join(fixture.local, "topics", "why-files.md"), "\nLocal-only accepted change.\n")
	fixture.commit(t, fixture.local, "local divergence")
	pusher := NewPusher()
	var commands [][]string
	pusher.run = recordingRunner(&commands)
	result, err := pusher.Push(t.Context(), openTestStore(t, fixture.local), "origin", "main")
	if err != nil {
		t.Fatalf("reject divergence: %v", err)
	}
	assertPushResult(t, result, PushRejected, true, false, 0)
	if result.Before == nil || *result.Before != remoteTip {
		t.Fatalf("rejected before = %v, want %q", result.Before, remoteTip)
	}
	if got := networkCommands(commands); len(got) != 1 || got[0][0] != "ls-remote" {
		t.Fatalf("network commands = %#v, want only exact observation; all commands = %#v", got, commands)
	}
	if got := fixture.remoteTip(t); got != remoteTip {
		t.Fatalf("rejected push changed remote from %q to %q", remoteTip, got)
	}
}

func TestPushClassifiesConditionalUpdateRaceWithoutRetry(t *testing.T) {
	fixture := newPushFixture(t, true)
	if result, err := Push(t.Context(), openTestStore(t, fixture.local), "origin", "main"); err != nil || result.State != PushPushed {
		t.Fatalf("initial push = %#v, %v", result, err)
	}
	other := filepath.Join(fixture.root, "racer")
	testGit(t, fixture.root, "clone", fixture.remoteURL, other)
	configureIdentity(t, other)
	appendFile(t, filepath.Join(other, "topics", "derived-state.md"), "\nConcurrent remote change.\n")
	fixture.commit(t, other, "concurrent remote")
	remoteRaceTip := strings.TrimSpace(testGit(t, other, "rev-parse", "refs/heads/main"))

	appendFile(t, filepath.Join(fixture.local, "topics", "why-files.md"), "\nRace target.\n")
	fixture.commit(t, fixture.local, "race target")

	pusher := NewPusher()
	var commands [][]string
	var publication commandResult
	realRunner := recordingRunner(&commands)
	pusher.run = func(ctx context.Context, executable, root string, environment []string, arguments ...string) commandResult {
		got := realRunner(ctx, executable, root, environment, arguments...)
		if len(arguments) != 0 && arguments[0] == "push" {
			publication = got
		}
		return got
	}
	pusher.afterObserve = func(_ remoteselect.Selection, _ *string) {
		testGit(t, other, "push", "--no-verify", fixture.remoteURL, remoteRaceTip+":refs/heads/main")
	}
	result, err := pusher.Push(t.Context(), openTestStore(t, fixture.local), "origin", "main")
	if result != nil || KindOf(err) != ErrorConcurrency || !errors.Is(err, ErrCASRace) {
		t.Fatalf("CAS race result/error = %#v, %v; status=%d stdout=%q stderr=%q", result, err, publication.status, publication.stdout, publication.stderr)
	}
	if got := fixture.remoteTip(t); got != remoteRaceTip {
		t.Fatalf("concurrent actor remote tip = %q, want %q", got, remoteRaceTip)
	}
	pushes := 0
	for _, arguments := range commands {
		if len(arguments) != 0 && arguments[0] == "push" {
			pushes++
		}
	}
	if pushes != 1 {
		t.Fatalf("conditional push attempts = %d, want exactly one; commands = %#v", pushes, commands)
	}
}

func TestPushClassifiesConcurrentPublicationOfDesiredTipAsCASRace(t *testing.T) {
	fixture := newPushFixture(t, true)
	if result, err := Push(t.Context(), openTestStore(t, fixture.local), "origin", "main"); err != nil || result.State != PushPushed {
		t.Fatalf("initial push = %#v, %v", result, err)
	}
	appendFile(t, filepath.Join(fixture.local, "topics", "why-files.md"), "\nSame desired race target.\n")
	fixture.commit(t, fixture.local, "same desired race target")
	want := fixture.tip(t)

	pusher := NewPusher()
	pusher.afterObserve = func(_ remoteselect.Selection, _ *string) {
		testGit(t, fixture.local, "push", "--no-verify", fixture.remoteURL, want+":refs/heads/main")
	}
	result, err := pusher.Push(t.Context(), openTestStore(t, fixture.local), "origin", "main")
	if result != nil || KindOf(err) != ErrorConcurrency || !errors.Is(err, ErrCASRace) {
		t.Fatalf("same-desired CAS race result/error = %#v, %v", result, err)
	}
	if got := fixture.remoteTip(t); got != want {
		t.Fatalf("concurrent desired remote tip = %q, want %q", got, want)
	}
}

func TestPushRejectsLocalHeadChangeAfterRemoteObservation(t *testing.T) {
	fixture := newPushFixture(t, true)
	pusher := NewPusher()
	var commands [][]string
	pusher.run = recordingRunner(&commands)
	pusher.afterObserve = func(_ remoteselect.Selection, _ *string) {
		appendFile(t, filepath.Join(fixture.local, "topics", "why-files.md"), "\nConcurrent local accepted change.\n")
		fixture.commit(t, fixture.local, "concurrent local")
	}
	result, err := pusher.Push(t.Context(), openTestStore(t, fixture.local), "origin", "main")
	if result != nil || KindOf(err) != ErrorConcurrency || !errors.Is(err, managedread.ErrConcurrent) {
		t.Fatalf("local HEAD race result/error = %#v, %v", result, err)
	}
	if got := networkCommands(commands); len(got) != 1 || got[0][0] != "ls-remote" {
		t.Fatalf("local HEAD race network commands = %#v, want observation only", got)
	}
	if fixture.remoteRefExists(t) {
		t.Fatal("local HEAD race changed remote")
	}
}

func TestPushRejectsInvalidAuditBeforeAnyNetworkCommand(t *testing.T) {
	fixture := newPushFixture(t, false)
	if err := os.WriteFile(filepath.Join(fixture.local, ".engram", "root.yaml"), []byte("not a conforming root\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fixture.commit(t, fixture.local, "invalid accepted snapshot")
	pusher := NewPusher()
	var commands [][]string
	pusher.run = recordingRunner(&commands)
	result, err := pusher.Push(t.Context(), openTestStore(t, fixture.local), "origin", "main")
	if err != nil {
		t.Fatalf("reject invalid audit: %v", err)
	}
	if result == nil || result.State != PushRejected || result.Remote != "origin" || result.RemoteRef != "refs/heads/main" || result.RemoteObserved || result.Before != nil || result.After == "" || result.Commits != 0 || result.Changed == nil || *result.Changed {
		t.Fatalf("invalid-audit result = %#v", result)
	}
	if result.Validation.Status != checker.StatusComplete || !result.Validation.HasErrors() || len(result.Audits) == 0 || len(commands) != 0 {
		t.Fatalf("invalid audit validation/commands = %#v / %#v", result.Validation, commands)
	}
	if _, statErr := os.Stat(fixture.remote); !errors.Is(statErr, fs.ErrNotExist) {
		t.Fatalf("network-silent invalid audit touched missing remote: %v", statErr)
	}
}

func TestPushDistinguishesRemoteRejectionNetworkFailureAndIndeterminate(t *testing.T) {
	t.Run("explicit remote rejection", func(t *testing.T) {
		fixture := newPushFixture(t, true)
		installHook(t, filepath.Join(fixture.remote, "hooks", "pre-receive"), "#!/bin/sh\necho policy rejection >&2\nexit 1\n")
		result, err := Push(t.Context(), openTestStore(t, fixture.local), "origin", "main")
		if err != nil {
			t.Fatalf("remote rejection: %v", err)
		}
		assertPushResult(t, result, PushRejected, true, false, 1)
		if result.Before != nil || fixture.remoteRefExists(t) {
			t.Fatalf("rejected creation before/ref = %v / %t", result.Before, fixture.remoteRefExists(t))
		}
	})

	t.Run("observation network failure", func(t *testing.T) {
		fixture := newPushFixture(t, false)
		result, err := Push(t.Context(), openTestStore(t, fixture.local), "origin", "main")
		if result != nil || KindOf(err) != ErrorNetwork || !errors.Is(err, ErrNetwork) {
			t.Fatalf("network failure result/error = %#v, %v", result, err)
		}
	})

	t.Run("ambiguous post-dispatch failure", func(t *testing.T) {
		fixture := newPushFixture(t, true)
		pusher := NewPusher()
		realRunner := recordingRunner(nil)
		pusher.run = func(ctx context.Context, executable, root string, environment []string, arguments ...string) commandResult {
			if len(arguments) != 0 && arguments[0] == "push" {
				refspec := arguments[len(arguments)-1]
				tip, ref, _ := strings.Cut(refspec, ":")
				trace := "packet:         push> " + strings.Repeat("0", 40) + " " + tip + " " + ref + "\\0 capabilities\ntransport response lost\n"
				return commandResult{started: true, status: 1, stderr: []byte(trace)}
			}
			return realRunner(ctx, executable, root, environment, arguments...)
		}
		result, err := pusher.Push(t.Context(), openTestStore(t, fixture.local), "origin", "main")
		if err != nil {
			t.Fatalf("ambiguous publication: %v", err)
		}
		if result == nil || result.State != PushIndeterminate || !result.RemoteObserved || result.Before != nil || result.After == "" || result.Commits != 1 || result.Changed != nil || result.Validation.Status != checker.StatusComplete || result.Validation.HasErrors() || len(result.Audits) == 0 {
			t.Fatalf("indeterminate result = %#v", result)
		}
	})

	t.Run("pre-dispatch network failure", func(t *testing.T) {
		fixture := newPushFixture(t, true)
		pusher := NewPusher()
		realRunner := recordingRunner(nil)
		pusher.run = func(ctx context.Context, executable, root string, environment []string, arguments ...string) commandResult {
			if len(arguments) != 0 && arguments[0] == "push" {
				return commandResult{started: true, status: 128, stderr: []byte("fatal: authentication failed\n")}
			}
			return realRunner(ctx, executable, root, environment, arguments...)
		}
		result, err := pusher.Push(t.Context(), openTestStore(t, fixture.local), "origin", "main")
		if result != nil || KindOf(err) != ErrorNetwork || !errors.Is(err, ErrNetwork) {
			t.Fatalf("pre-dispatch network result/error = %#v, %v", result, err)
		}
	})
}

func TestPushCancellationAfterObservationDoesNotDispatchUpdate(t *testing.T) {
	fixture := newPushFixture(t, true)
	ctx, cancel := context.WithCancel(t.Context())
	pusher := NewPusher()
	var commands [][]string
	pusher.run = recordingRunner(&commands)
	pusher.afterObserve = func(_ remoteselect.Selection, _ *string) { cancel() }
	result, err := pusher.Push(ctx, openTestStore(t, fixture.local), "origin", "main")
	if result != nil || KindOf(err) != ErrorCancelled || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled result/error = %#v, %v", result, err)
	}
	remoteCommands := networkCommands(commands)
	if len(remoteCommands) != 1 || remoteCommands[0][0] != "ls-remote" || fixture.remoteRefExists(t) {
		t.Fatalf("cancelled network commands/ref = %#v / %t; all commands = %#v", remoteCommands, fixture.remoteRefExists(t), commands)
	}
}

func TestPushRefreshesStaleStoreBeforeSelectingAndAuditingBranch(t *testing.T) {
	fixture := newPushFixture(t, true)
	stale := openTestStore(t, fixture.local)
	testGit(t, fixture.local, "switch", "-c", "other")
	appendFile(t, filepath.Join(fixture.local, "topics", "why-files.md"), "\nAccepted on the switched branch.\n")
	fixture.commit(t, fixture.local, "other branch")
	want := strings.TrimSpace(testGit(t, fixture.local, "rev-parse", "refs/heads/other"))

	result, err := Push(t.Context(), stale, "origin", "")
	if err != nil {
		t.Fatalf("push through stale Store: %v", err)
	}
	if result.State != PushPushed || result.RemoteRef != "refs/heads/other" || result.After != want || result.Before != nil || result.Changed == nil || !*result.Changed {
		t.Fatalf("stale-Store push result = %#v", result)
	}
	if got := strings.TrimSpace(testGit(t, fixture.remote, "rev-parse", "refs/heads/other")); got != want {
		t.Fatalf("remote other tip = %q, want %q", got, want)
	}
	if command := testGitCommand(fixture.remote, "show-ref", "--verify", "--quiet", "refs/heads/main"); command.Run() == nil {
		t.Fatal("stale Store incorrectly published switched tip to main")
	}
}

func TestPushBlocksRepositoryURLRewritesBeforeNetwork(t *testing.T) {
	for _, variable := range []string{"insteadOf", "pushInsteadOf"} {
		t.Run(variable, func(t *testing.T) {
			fixture := newPushFixture(t, true)
			rewritten := (&url.URL{Scheme: "file", Path: filepath.Join(fixture.root, "redirected.git")}).String()
			testGit(t, fixture.local, "config", "url."+rewritten+"."+variable, fixture.remoteURL)
			testGit(t, fixture.local, "config", "protocol.ext.allow", "always")
			pusher := NewPusher()
			var commands [][]string
			pusher.run = recordingRunner(&commands)
			result, err := pusher.Push(t.Context(), openTestStore(t, fixture.local), "origin", "main")
			if result != nil || KindOf(err) != ErrorRepository {
				t.Fatalf("rewrite result/error = %#v, %v", result, err)
			}
			if got := networkCommands(commands); len(got) != 0 {
				t.Fatalf("rewrite initiated network commands: %#v", got)
			}
			if fixture.remoteRefExists(t) {
				t.Fatal("rewrite rejection changed selected remote")
			}
		})
	}
}

func TestPushFiltersLSRemoteTailMatchesAndCreatesOnlyExactRef(t *testing.T) {
	fixture := newPushFixture(t, true)
	tip := fixture.tip(t)
	testGit(t, fixture.local, "push", "--no-verify", fixture.remoteURL, tip+":refs/nested/refs/heads/main")
	result, err := Push(t.Context(), openTestStore(t, fixture.local), "origin", "main")
	if err != nil {
		t.Fatalf("push beside suffix-colliding ref: %v", err)
	}
	assertPushResult(t, result, PushPushed, true, true, 1)
	if result.Before != nil || fixture.remoteTip(t) != tip {
		t.Fatalf("exact creation before/tip = %v / %q", result.Before, fixture.remoteTip(t))
	}
}

func TestPushExplicitInvalidArgumentsAreUsageErrors(t *testing.T) {
	fixture := newPushFixture(t, true)
	store := openTestStore(t, fixture.local)
	for _, arguments := range [][2]string{{"-option", "main"}, {"origin", "bad branch"}, {"", "main"}} {
		pusher := NewPusher()
		var commands [][]string
		pusher.run = recordingRunner(&commands)
		result, err := pusher.Push(t.Context(), store, arguments[0], arguments[1])
		if result != nil || KindOf(err) != ErrorUsage || len(commands) != 0 {
			t.Fatalf("Push(%q, %q) = %#v, %v, commands %#v", arguments[0], arguments[1], result, err, commands)
		}
	}
}

func TestPushResultKeepsProtocolArraysNonNull(t *testing.T) {
	fixture := newPushFixture(t, true)
	result, err := Push(t.Context(), openTestStore(t, fixture.local), "origin", "main")
	if err != nil {
		t.Fatal(err)
	}
	if result.Validation.Findings == nil {
		t.Fatal("managed validation findings is nil")
	}
	for index, audit := range result.Audits {
		if audit.Validation.Findings == nil {
			t.Fatalf("audit %d validation findings is nil", index)
		}
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), `"findings":null`) || strings.Contains(string(encoded), `"audits":null`) {
		t.Fatalf("push result contains null protocol array: %s", encoded)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &object); err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	wantKeys := []string{"after", "audits", "before", "changed", "commits", "remote", "remote_observed", "remote_ref", "state", "validation"}
	if !slices.Equal(keys, wantKeys) {
		t.Fatalf("push result keys = %v, want %v", keys, wantKeys)
	}
}

func TestParseObservedRefUsesExactNameAndRepositoryObjectFormat(t *testing.T) {
	t.Parallel()
	ref := "refs/heads/main"
	sha1 := strings.Repeat("a", 40)
	sha256 := strings.Repeat("b", 64)
	nested := "refs/nested/refs/heads/main"

	got, err := parseObservedRef([]byte(sha1+"\t"+nested+"\n"), ref, gitraw.SHA1)
	if err != nil || got != nil {
		t.Fatalf("suffix-only observation = %v, %v; want absent", got, err)
	}
	got, err = parseObservedRef([]byte(sha1+"\t"+nested+"\n"+sha1+"\t"+ref+"\n"), ref, gitraw.SHA1)
	if err != nil || got == nil || *got != sha1 {
		t.Fatalf("exact SHA-1 observation = %v, %v", got, err)
	}
	if _, err := parseObservedRef([]byte(sha256+"\t"+ref+"\n"), ref, gitraw.SHA1); err == nil {
		t.Fatal("SHA-256 remote OID accepted for SHA-1 repository")
	}
	got, err = parseObservedRef([]byte(sha256+"\t"+ref+"\n"), ref, gitraw.SHA256)
	if err != nil || got == nil || *got != sha256 {
		t.Fatalf("exact SHA-256 observation = %v, %v", got, err)
	}
}

func TestParsePushReportRequiresOneExactConditionalRefStatus(t *testing.T) {
	t.Parallel()
	tip := strings.Repeat("a", 40)
	ref := "refs/heads/main"
	line := func(flag byte, summary string) string {
		return string(flag) + "\t" + tip + ":" + ref + "\t" + summary + "\n"
	}
	tests := []struct {
		name   string
		output string
		want   pushReport
	}{
		{"published", line(' ', "ok"), pushReport{published: true, summary: "ok"}},
		{"creation", line('*', "[new branch]"), pushReport{published: true, summary: "[new branch]"}},
		{"same desired race", line('=', "[up to date]"), pushReport{upToDate: true, summary: "[up to date]"}},
		{"client CAS race", line('!', "[rejected] (stale info)"), pushReport{casRace: true, summary: "[rejected] (stale info)"}},
		{"server CAS race", line('!', "[remote rejected] (failed to update ref)"), pushReport{casRace: true, summary: "[remote rejected] (failed to update ref)"}},
		{"policy rejection", line('!', "[remote rejected] (pre-receive hook declined)"), pushReport{rejected: true, summary: "[remote rejected] (pre-receive hook declined)"}},
		{"unexpected second ref", line(' ', "ok") + "*\t" + tip + ":refs/heads/other\t[new branch]\n", pushReport{}},
		{"unexpected source", " \t" + strings.Repeat("b", 40) + ":" + ref + "\tok\n", pushReport{}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := parsePushReport([]byte(test.output), tip, ref); got != test.want {
				t.Fatalf("parsePushReport() = %#v, want %#v", got, test.want)
			}
		})
	}
}

type pushFixture struct {
	root      string
	local     string
	remote    string
	remoteURL string
}

func newPushFixture(t *testing.T, createRemote bool) pushFixture {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	root := t.TempDir()
	local := filepath.Join(root, "local")
	remote := filepath.Join(root, "remote.git")
	if err := os.Mkdir(local, 0o755); err != nil {
		t.Fatal(err)
	}
	testGit(t, local, "init", "--initial-branch=main")
	configureIdentity(t, local)
	copyMinimal(t, local)
	testGit(t, local, "add", "--all")
	testGit(t, local, "commit", "-m", "initial")
	if createRemote {
		testGit(t, root, "init", "--bare", "--initial-branch=main", remote)
	}
	remoteURL := (&url.URL{Scheme: "file", Path: remote}).String()
	testGit(t, local, "remote", "add", "origin", remoteURL)
	testGit(t, local, "config", "branch.main.remote", "origin")
	testGit(t, local, "config", "branch.main.merge", "refs/heads/main")
	return pushFixture{root: root, local: local, remote: remote, remoteURL: remoteURL}
}

func (f pushFixture) commit(t *testing.T, root, message string) {
	t.Helper()
	testGit(t, root, "add", "--all")
	testGit(t, root, "commit", "-m", message)
}

func (f pushFixture) tip(t *testing.T) string {
	t.Helper()
	return strings.TrimSpace(testGit(t, f.local, "rev-parse", "refs/heads/main"))
}

func (f pushFixture) remoteTip(t *testing.T) string {
	t.Helper()
	return strings.TrimSpace(testGit(t, f.remote, "rev-parse", "refs/heads/main"))
}

func (f pushFixture) remoteRefExists(t *testing.T) bool {
	t.Helper()
	command := testGitCommand(f.remote, "show-ref", "--verify", "--quiet", "refs/heads/main")
	err := command.Run()
	if err == nil {
		return true
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) && exitError.ExitCode() == 1 {
		return false
	}
	t.Fatalf("inspect remote ref: %v", err)
	return false
}

func openTestStore(t *testing.T, root string) *managedread.Store {
	t.Helper()
	store, err := managedread.Open(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func configureIdentity(t *testing.T, root string) {
	t.Helper()
	testGit(t, root, "config", "user.name", "Engram Test")
	testGit(t, root, "config", "user.email", "engram@example.invalid")
}

func testGit(t *testing.T, root string, arguments ...string) string {
	t.Helper()
	command := testGitCommand(root, arguments...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(arguments, " "), err, output)
	}
	return string(output)
}

func testGitCommand(root string, arguments ...string) *exec.Cmd {
	command := exec.Command("git", append([]string{"-C", root}, arguments...)...)
	command.Env = testEnvironment()
	return command
}

func testEnvironment() []string {
	result := make([]string, 0, len(os.Environ())+4)
	for _, item := range os.Environ() {
		name, _, _ := strings.Cut(item, "=")
		upper := strings.ToUpper(name)
		if strings.HasPrefix(upper, "GIT_") || strings.HasPrefix(upper, "ENGRAM_") {
			continue
		}
		result = append(result, item)
	}
	return append(result, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
}

func copyMinimal(t *testing.T, destination string) {
	t.Helper()
	source := filepath.Join("..", "..", "examples", "minimal")
	err := filepath.WalkDir(source, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, name)
		if err != nil || relative == "." {
			return err
		}
		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		content, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		return os.WriteFile(target, content, 0o644)
	})
	if err != nil {
		t.Fatalf("copy minimal snapshot: %v", err)
	}
}

func appendFile(t *testing.T, name, suffix string) {
	t.Helper()
	file, err := os.OpenFile(name, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(suffix); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func installHook(t *testing.T, name, script string) {
	t.Helper()
	if err := os.WriteFile(name, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func recordingRunner(commands *[][]string) func(context.Context, string, string, []string, ...string) commandResult {
	return func(ctx context.Context, executable, root string, environment []string, arguments ...string) commandResult {
		if commands != nil {
			*commands = append(*commands, slices.Clone(arguments))
		}
		return runGitCommand(ctx, executable, root, environment, arguments...)
	}
}

func networkCommands(commands [][]string) [][]string {
	result := make([][]string, 0, len(commands))
	for _, arguments := range commands {
		if len(arguments) != 0 && (arguments[0] == "ls-remote" || arguments[0] == "push") {
			result = append(result, arguments)
		}
	}
	return result
}

func assertPushResult(t *testing.T, result *PushResult, state PushState, observed, changed bool, commits int) {
	t.Helper()
	if result == nil {
		t.Fatal("nil push result")
	}
	if result.State != state || result.Remote != "origin" || result.RemoteRef != "refs/heads/main" || result.RemoteObserved != observed || result.After == "" || result.Commits != commits {
		t.Fatalf("push result = %#v", result)
	}
	if result.Changed == nil || *result.Changed != changed {
		t.Fatalf("push changed = %v, want %t", result.Changed, changed)
	}
	if result.Validation.Status != checker.StatusComplete || result.Validation.HasErrors() || len(result.Audits) == 0 {
		t.Fatalf("push audit = %#v / %#v", result.Validation, result.Audits)
	}
}
