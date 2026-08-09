package pullflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ontopix/engram/internal/gitraw"
	"github.com/ontopix/engram/internal/guard"
	"github.com/ontopix/engram/internal/hookexec"
	"github.com/ontopix/engram/internal/hooks"
	"github.com/ontopix/engram/internal/journal"
	"github.com/ontopix/engram/internal/managedread"
	"github.com/ontopix/engram/internal/managedwrite"
	"github.com/ontopix/engram/internal/rendezvous"
)

func TestPullFastForwardsOnlyAfterCompleteIncomingAudit(t *testing.T) {
	fixture := newPullFixture(t)
	appendTestFile(t, filepath.Join(fixture.remoteWork, "topics", "why-files.md"), "\nRemote accepted sentence.\n")
	gitTest(t, fixture.remoteWork, "add", "topics/why-files.md")
	gitTest(t, fixture.remoteWork, "commit", "--no-verify", "-m", "remote")
	gitTest(t, fixture.remoteWork, "push", "origin", "main")
	want := testTip(t, fixture.remoteWork)

	puller := New(noopWriter{})
	result, err := puller.Pull(t.Context(), openStore(t, fixture.local), "origin", "main")
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if result.State != FastForwarded || result.Fetched != 1 || result.Replayed != 0 || result.After.Commit == nil || *result.After.Commit != want || len(result.Conflicts) != 0 || result.Validation.HasErrors() {
		t.Fatalf("result = %#v", result)
	}
	if got := testTip(t, fixture.local); got != want {
		t.Fatalf("local tip = %q, want %q", got, want)
	}
	if got := gitTest(t, fixture.local, "status", "--porcelain=v1"); got != "" {
		t.Fatalf("fast-forward left dirty checkout: %q", got)
	}

	unchanged, err := puller.Pull(t.Context(), openStore(t, fixture.local), "", "")
	if err != nil || unchanged.State != UpToDate || unchanged.Fetched != 0 || unchanged.Changes != nil {
		t.Fatalf("up-to-date = %#v, %v", unchanged, err)
	}
	encoded, err := json.Marshal(unchanged)
	if err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{`"conflicts":null`, `"findings":null`, `"audits":null`} {
		if bytes.Contains(encoded, []byte(invalid)) {
			t.Fatalf("pull result contains null protocol array %s: %s", invalid, encoded)
		}
	}
}

func TestPullDivergentConflictLeavesExactActiveStateAndAbortRestores(t *testing.T) {
	fixture := newPullFixture(t)
	name := filepath.Join("topics", "why-files.md")
	appendTestFile(t, filepath.Join(fixture.local, name), "\nLocal sentence.\n")
	gitTest(t, fixture.local, "add", name)
	gitTest(t, fixture.local, "commit", "--no-verify", "-m", "local")
	original := testTip(t, fixture.local)
	appendTestFile(t, filepath.Join(fixture.remoteWork, name), "\nRemote sentence.\n")
	gitTest(t, fixture.remoteWork, "add", name)
	gitTest(t, fixture.remoteWork, "commit", "--no-verify", "-m", "remote")
	gitTest(t, fixture.remoteWork, "push", "origin", "main")

	puller := fixture.puller(t)
	result, err := puller.Pull(t.Context(), openStore(t, fixture.local), "origin", "main")
	if err != nil {
		t.Fatalf("divergent pull: %v", err)
	}
	if result.State != Conflict || result.Replayed != 0 || len(result.Conflicts) != 1 || result.Conflicts[0] != name {
		t.Fatalf("conflict result = %#v", result)
	}
	privateStore := openStore(t, fixture.local)
	active, err := Active(privateStore.Repository())
	if err != nil || active == nil || active.Reason != "conflict" || len(active.Conflicts) != 1 || active.Original.Commit == nil || *active.Original.Commit != original || active.Private.Ref == nil || *active.Private.Ref == *active.Original.Ref {
		t.Fatalf("active state = %#v, %v", active, err)
	}
	stateBytes, err := os.ReadFile(replayStatePath(privateStore.Repository()))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"remote", "sources", "next", "validation"} {
		if bytes.Contains(stateBytes, []byte(`"`+forbidden+`"`)) {
			t.Fatalf("public replay state leaked private plan field %q: %s", forbidden, stateBytes)
		}
	}

	aborted, err := puller.Abort(t.Context(), privateStore)
	if err != nil {
		t.Fatalf("abort: %v", err)
	}
	if aborted.State != Aborted || testTip(t, fixture.local) != original || len(aborted.Conflicts) != 0 {
		t.Fatalf("abort result = %#v", aborted)
	}
	if got := gitTest(t, fixture.local, "symbolic-ref", "HEAD"); got != "refs/heads/main\n" {
		t.Fatalf("HEAD after abort = %q", got)
	}
	if _, err := Active(openStore(t, fixture.local).Repository()); err != nil {
		t.Fatalf("active after abort: %v", err)
	}
}

func TestPullReplaysNonConflictingLocalCommitsOldestFirst(t *testing.T) {
	fixture := newPullFixture(t)
	appendTestFile(t, filepath.Join(fixture.local, "topics", "why-files.md"), "\nLocal one.\n")
	gitTest(t, fixture.local, "add", "topics/why-files.md")
	gitTest(t, fixture.local, "commit", "--no-verify", "-m", "local one")
	appendTestFile(t, filepath.Join(fixture.local, "topics", "why-files.md"), "\nLocal two.\n")
	gitTest(t, fixture.local, "add", "topics/why-files.md")
	gitTest(t, fixture.local, "commit", "--no-verify", "-m", "local two")
	appendTestFile(t, filepath.Join(fixture.remoteWork, "topics", "derived-state.md"), "\nRemote sentence.\n")
	gitTest(t, fixture.remoteWork, "add", "topics/derived-state.md")
	gitTest(t, fixture.remoteWork, "commit", "--no-verify", "-m", "remote")
	gitTest(t, fixture.remoteWork, "push", "origin", "main")

	result, err := fixture.puller(t).Pull(t.Context(), openStore(t, fixture.local), "origin", "main")
	if err != nil {
		t.Fatalf("replay pull: %v", err)
	}
	if result.State != Replayed || result.Replayed != 2 || result.Fetched != 1 || len(result.Conflicts) != 0 {
		t.Fatalf("replay result = %#v", result)
	}
	why := string(readTestFile(t, filepath.Join(fixture.local, "topics", "why-files.md")))
	derived := string(readTestFile(t, filepath.Join(fixture.local, "topics", "derived-state.md")))
	if !strings.Contains(why, "Local one.") || !strings.Contains(why, "Local two.") || !strings.Contains(derived, "Remote sentence.") {
		t.Fatalf("final replay bytes missing: why=%q derived=%q", why, derived)
	}
	if got := gitTest(t, fixture.local, "status", "--porcelain=v1"); got != "" {
		t.Fatalf("replay left dirty checkout: %q", got)
	}
	if ActiveState, err := Active(openStore(t, fixture.local).Repository()); err != nil || ActiveState != nil {
		t.Fatalf("active replay after completion = %#v, %v", ActiveState, err)
	}
}

func TestPullContinueAcceptsExplicitStagedResolution(t *testing.T) {
	fixture := newPullFixture(t)
	name := filepath.Join("topics", "why-files.md")
	appendTestFile(t, filepath.Join(fixture.local, name), "\nLocal conflict.\n")
	gitTest(t, fixture.local, "add", name)
	gitTest(t, fixture.local, "commit", "--no-verify", "-m", "local")
	appendTestFile(t, filepath.Join(fixture.remoteWork, name), "\nRemote conflict.\n")
	gitTest(t, fixture.remoteWork, "add", name)
	gitTest(t, fixture.remoteWork, "commit", "--no-verify", "-m", "remote")
	gitTest(t, fixture.remoteWork, "push", "origin", "main")
	puller := fixture.puller(t)
	conflict, err := puller.Pull(t.Context(), openStore(t, fixture.local), "origin", "main")
	if err != nil || conflict.State != Conflict {
		t.Fatalf("conflict = %#v, %v", conflict, err)
	}
	appendTestFile(t, filepath.Join(fixture.local, name), "\nExplicit resolution.\n")
	gitTest(t, fixture.local, "add", name)
	resolved, err := puller.Continue(t.Context(), openStore(t, fixture.local))
	if err != nil {
		t.Fatalf("continue: %v", err)
	}
	if resolved.State != Replayed || resolved.Replayed != 1 || !strings.Contains(string(readTestFile(t, filepath.Join(fixture.local, name))), "Explicit resolution.") {
		t.Fatalf("resolved = %#v", resolved)
	}
}

func TestPullContinueFinalizesRecordedTerminalProgressWithoutReplaying(t *testing.T) {
	fixture := newPullFixture(t)
	appendTestFile(t, filepath.Join(fixture.local, "topics", "why-files.md"), "\nLocal terminal replay.\n")
	gitTest(t, fixture.local, "add", "topics/why-files.md")
	gitTest(t, fixture.local, "commit", "--no-verify", "-m", "local terminal")
	appendTestFile(t, filepath.Join(fixture.remoteWork, "topics", "derived-state.md"), "\nRemote terminal replay.\n")
	gitTest(t, fixture.remoteWork, "add", "topics/derived-state.md")
	gitTest(t, fixture.remoteWork, "commit", "--no-verify", "-m", "remote terminal")
	gitTest(t, fixture.remoteWork, "push", "origin", "main")

	puller := fixture.puller(t)
	failed := false
	puller.Fault = func(phase Phase) error {
		if phase == PhaseReplayCommitted && !failed {
			failed = true
			return errors.New("interrupt after replay progress")
		}
		return nil
	}
	if result, err := puller.Pull(t.Context(), openStore(t, fixture.local), "origin", "main"); result != nil || err == nil {
		t.Fatalf("interrupted terminal replay = %#v, %v", result, err)
	}
	if active, err := Active(openStore(t, fixture.local).Repository()); err != nil || active == nil {
		t.Fatalf("terminal replay state = %#v, %v", active, err)
	}
	puller.Fault = nil
	result, err := puller.Continue(t.Context(), openStore(t, fixture.local))
	if err != nil || result.State != Replayed || result.Replayed != 1 {
		t.Fatalf("continued terminal replay = %#v, %v", result, err)
	}
	if count := strings.Count(gitTest(t, fixture.local, "log", "--format=%s"), "Replay "); count != 1 {
		t.Fatalf("replay commit count = %d", count)
	}
}

func TestPullContinueRepairsAcceptedCommitBeforeControllerProgress(t *testing.T) {
	fixture := newPullFixture(t)
	appendTestFile(t, filepath.Join(fixture.local, "topics", "why-files.md"), "\nLocal recoverable replay.\n")
	gitTest(t, fixture.local, "add", "topics/why-files.md")
	gitTest(t, fixture.local, "commit", "--no-verify", "-m", "local recoverable")
	appendTestFile(t, filepath.Join(fixture.remoteWork, "topics", "derived-state.md"), "\nRemote recoverable replay.\n")
	gitTest(t, fixture.remoteWork, "add", "topics/derived-state.md")
	gitTest(t, fixture.remoteWork, "commit", "--no-verify", "-m", "remote recoverable")
	gitTest(t, fixture.remoteWork, "push", "origin", "main")

	base := fixture.puller(t)
	writer := &commitThenErrorWriter{inner: base.Writer, failImage: true}
	puller := New(writer)
	if result, err := puller.Pull(t.Context(), openStore(t, fixture.local), "origin", "main"); result != nil || err == nil {
		t.Fatalf("lost controller progress = %#v, %v", result, err)
	}
	if writer.imageCalls != 1 {
		t.Fatalf("image calls = %d", writer.imageCalls)
	}
	writer.failImage = false
	result, err := puller.Continue(t.Context(), openStore(t, fixture.local))
	if err != nil || result.State != Replayed || result.Replayed != 1 {
		t.Fatalf("repaired replay = %#v, %v", result, err)
	}
	if writer.imageCalls != 1 {
		t.Fatalf("accepted source was replayed again; calls = %d", writer.imageCalls)
	}
}

func TestPullFastForwardInterruptionRetainsAndRecoversExactTransition(t *testing.T) {
	fixture := newPullFixture(t)
	appendTestFile(t, filepath.Join(fixture.remoteWork, "topics", "derived-state.md"), "\nRecovery target.\n")
	gitTest(t, fixture.remoteWork, "add", "topics/derived-state.md")
	gitTest(t, fixture.remoteWork, "commit", "--no-verify", "-m", "remote recovery")
	gitTest(t, fixture.remoteWork, "push", "origin", "main")
	want := testTip(t, fixture.remoteWork)

	puller := New(noopWriter{})
	puller.Fault = func(phase Phase) error {
		if phase == PhaseRefUpdated {
			return errors.New("interrupt after CAS")
		}
		return nil
	}
	result, err := puller.Pull(t.Context(), openStore(t, fixture.local), "origin", "main")
	if result != nil || err == nil || MutationOf(err) == nil || !MutationOf(err).RecoveryRequired || !MutationOf(err).Durable {
		t.Fatalf("interrupted result/error = %#v, %v", result, err)
	}
	repository, discoverErr := gitraw.Discover(t.Context(), fixture.local)
	if discoverErr != nil {
		t.Fatal(discoverErr)
	}
	journalBytes, present, readErr := readControllerFile(transitionPath(repository))
	if readErr != nil || !present {
		t.Fatalf("recovery journal present=%t err=%v", present, readErr)
	}
	var record transitionRecord
	if err := decodeCanonical(journalBytes, &record); err != nil {
		t.Fatal(err)
	}
	activeTransitionTokens.Store(record.OwnerToken, struct{}{})
	active, inspectErr := InspectRecovery(t.Context(), repository)
	activeTransitionTokens.Delete(record.OwnerToken)
	if inspectErr != nil || active.Disposition != RecoveryActive {
		t.Fatalf("active recovery inspection = %#v, %v", active, inspectErr)
	}
	stale, inspectErr := InspectRecovery(t.Context(), repository)
	if inspectErr != nil || stale.Disposition != RecoveryRecoverable || stale.OwnerToken != record.OwnerToken || len(stale.RefNames) != 1 || stale.RefNames[0] != repository.HeadRef {
		t.Fatalf("stale recovery inspection = %#v, %v", stale, inspectErr)
	}
	puller.Fault = nil
	recovered, recoverErr := puller.Recover(t.Context(), fixture.local)
	if recoverErr != nil {
		t.Fatalf("recover: %v", recoverErr)
	}
	if !recovered.Needed || !recovered.Performed || testTip(t, fixture.local) != want || gitTest(t, fixture.local, "status", "--porcelain=v1") != "" {
		t.Fatalf("recovered = %#v tip=%q", recovered, testTip(t, fixture.local))
	}
	if _, present, readErr := readControllerFile(transitionPath(openStore(t, fixture.local).Repository())); readErr != nil || present {
		t.Fatalf("journal after recovery present=%t err=%v", present, readErr)
	}
}

func TestPullRecoveryCancelsPreparedPreJournalTransition(t *testing.T) {
	fixture := newPullFixture(t)
	repository, err := gitraw.Discover(t.Context(), fixture.local)
	if err != nil {
		t.Fatal(err)
	}
	lock, err := rendezvous.AcquireWriter(repository.CommonGitDir, repository.GitDir, repository.HeadRef)
	if err != nil {
		t.Fatal(err)
	}
	index, present, err := readOptionalFile(filepath.Join(repository.GitDir, "index"))
	if err != nil || !present {
		t.Fatalf("read index: present=%t err=%v", present, err)
	}
	tip := repository.Head.String()
	state := gitState(repository.HeadRef, tip)
	record := transitionRecord{
		Version: 1, Phase: transitionPrepared, OwnerToken: lock.Owner().Token, ObjectFormat: repository.Format,
		Refs:        []transitionRef{{Ref: repository.HeadRef, Before: stringPointer(tip), After: stringPointer(tip)}},
		HeadBefore:  state,
		HeadAfter:   cloneGitState(state),
		IndexBefore: journal.RawFileImage{Present: true, Data: append([]byte(nil), index...)},
		IndexAfter:  journal.RawFileImage{Present: true, Data: append([]byte(nil), index...)},
		Paths:       []pathTransition{},
	}
	encoded, err := encodeCanonical(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := createControllerFile(transitionPath(repository), encoded); err != nil {
		t.Fatal(err)
	}
	inspection, err := InspectRecovery(t.Context(), repository)
	if err != nil || inspection.Disposition != RecoveryRecoverable {
		t.Fatalf("prepared inspection = %#v, %v", inspection, err)
	}
	result, err := New(noopWriter{}).Recover(t.Context(), fixture.local)
	if err != nil || result == nil || !result.Needed || !result.Performed {
		t.Fatalf("prepared recovery = %#v, %v", result, err)
	}
	for _, name := range []string{transitionPath(repository), rendezvous.RefPath(repository.CommonGitDir, repository.HeadRef), rendezvous.WorktreePath(repository.GitDir)} {
		if _, err := os.Lstat(name); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("prepared recovery retained %s: %v", name, err)
		}
	}
}

func TestPullCandidateRejectionLeavesUnpreparedStagedDraft(t *testing.T) {
	fixture := newPullFixture(t)
	why := filepath.Join(fixture.local, "topics", "why-files.md")
	if err := os.Remove(why); err != nil {
		t.Fatal(err)
	}
	catalog := filepath.Join(fixture.local, "topics", "README.md")
	catalogBytes := readTestFile(t, catalog)
	line := []byte("- [why-files](why-files.md) — Why this store is plain markdown files instead of a database.\n")
	catalogBytes = bytes.Replace(catalogBytes, line, nil, 1)
	if bytes.Contains(catalogBytes, line) {
		t.Fatal("catalog fixture contains duplicate target line")
	}
	if err := os.WriteFile(catalog, catalogBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, fixture.local, "add", "--all")
	gitTest(t, fixture.local, "commit", "--no-verify", "-m", "remove why")
	appendTestFile(t, filepath.Join(fixture.remoteWork, "topics", "derived-state.md"), "\nSee [[topics/why-files]].\n")
	gitTest(t, fixture.remoteWork, "add", "topics/derived-state.md")
	gitTest(t, fixture.remoteWork, "commit", "--no-verify", "-m", "link why")
	gitTest(t, fixture.remoteWork, "push", "origin", "main")

	puller := fixture.puller(t)
	result, err := puller.Pull(t.Context(), openStore(t, fixture.local), "origin", "main")
	if err != nil {
		t.Fatalf("rejected pull: %v", err)
	}
	if result.State != Rejected || result.CandidateValidation == nil || !result.CandidateValidation.HasErrors() || len(result.Conflicts) != 0 || result.Changes == nil {
		t.Fatalf("rejected result = %#v", result)
	}
	store := openStore(t, fixture.local)
	status, err := store.Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Staged) == 0 || len(status.Unstaged) != 0 {
		t.Fatalf("rejected staged/unstaged = %#v / %#v", status.Staged, status.Unstaged)
	}
	active, err := Active(store.Repository())
	if err != nil || active == nil || active.Reason != "rejected" || len(active.Conflicts) != 0 {
		t.Fatalf("rejected active state = %#v, %v", active, err)
	}
	activeJSON, err := json.Marshal(active)
	if err != nil || bytes.Contains(activeJSON, []byte(`"conflicts":null`)) {
		t.Fatalf("active replay conflicts must be an array: %s, %v", activeJSON, err)
	}
	if _, err := puller.Abort(t.Context(), store); err != nil {
		t.Fatalf("abort rejected draft: %v", err)
	}
}

type noopWriter struct{}

func (noopWriter) Commit(context.Context, managedwrite.Request) (*managedwrite.Result, error) {
	return nil, errors.New("unexpected managed commit")
}
func (noopWriter) CommitImage(context.Context, managedwrite.ImageRequest) (*managedwrite.Result, error) {
	return nil, errors.New("unexpected managed image commit")
}

type commitThenErrorWriter struct {
	inner      ManagedWriter
	failImage  bool
	imageCalls int
}

func (w *commitThenErrorWriter) Commit(ctx context.Context, request managedwrite.Request) (*managedwrite.Result, error) {
	return w.inner.Commit(ctx, request)
}

func (w *commitThenErrorWriter) CommitImage(ctx context.Context, request managedwrite.ImageRequest) (*managedwrite.Result, error) {
	w.imageCalls++
	result, err := w.inner.CommitImage(ctx, request)
	if err == nil && w.failImage {
		return result, errors.New("simulated loss before controller progress")
	}
	return result, err
}

type pullFixture struct {
	root       string
	local      string
	remote     string
	remoteWork string
	remoteURL  string
}

func newPullFixture(t *testing.T) pullFixture {
	t.Helper()
	root := t.TempDir()
	local := filepath.Join(root, "local")
	if err := os.Mkdir(local, 0o755); err != nil {
		t.Fatal(err)
	}
	example := filepath.Join(pullRepositoryRoot(t), "examples", "minimal")
	if err := os.CopyFS(local, os.DirFS(example)); err != nil {
		t.Fatal(err)
	}
	gitTest(t, local, "init", "--initial-branch=main")
	configureTestIdentity(t, local)
	gitTest(t, local, "add", "--all")
	gitTest(t, local, "commit", "--no-verify", "-m", "initial")
	remote := filepath.Join(root, "remote.git")
	gitTest(t, root, "init", "--bare", "--initial-branch=main", remote)
	remoteURL := (&url.URL{Scheme: "file", Path: remote}).String()
	gitTest(t, local, "remote", "add", "origin", remoteURL)
	gitTest(t, local, "config", "branch.main.remote", "origin")
	gitTest(t, local, "config", "branch.main.merge", "refs/heads/main")
	gitTest(t, local, "push", "--no-verify", "origin", "main")
	remoteWork := filepath.Join(root, "remote-work")
	gitTest(t, root, "clone", remoteURL, remoteWork)
	configureTestIdentity(t, remoteWork)
	return pullFixture{root: root, local: local, remote: remote, remoteWork: remoteWork, remoteURL: remoteURL}
}

func (f pullFixture) puller(t *testing.T) *Puller {
	t.Helper()
	repository, err := gitraw.Discover(t.Context(), f.local)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := guard.Install(t.Context(), repository); err != nil {
		t.Fatal(err)
	}
	registry, err := hooks.NewRegistry(filepath.Join(f.root, "config", "hook-trust-v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	return New(managedwrite.New(hookexec.New(registry)))
}

func openStore(t *testing.T, root string) *managedread.Store {
	t.Helper()
	store, err := managedread.Open(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func configureTestIdentity(t *testing.T, root string) {
	t.Helper()
	gitTest(t, root, "config", "user.name", "Ada")
	gitTest(t, root, "config", "user.email", "ada@example.test")
	gitTest(t, root, "config", "commit.gpgsign", "false")
}

func gitTest(t *testing.T, root string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, arguments...)...)
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(arguments, " "), err, output)
	}
	return string(output)
}

func testTip(t *testing.T, root string) string {
	t.Helper()
	return strings.TrimSpace(gitTest(t, root, "rev-parse", "HEAD"))
}

func appendTestFile(t *testing.T, name, suffix string) {
	t.Helper()
	data := readTestFile(t, name)
	if err := os.WriteFile(name, append(data, suffix...), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readTestFile(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func pullRepositoryRoot(t *testing.T) string {
	t.Helper()
	directory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Clean(filepath.Join(directory, "..", ".."))
}
