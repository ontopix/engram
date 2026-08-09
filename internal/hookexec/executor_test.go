package hookexec

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/ontopix/engram/internal/checker"
	"github.com/ontopix/engram/internal/gitraw"
	"github.com/ontopix/engram/internal/hooks"
	"github.com/ontopix/engram/internal/snapshot"
)

type trustRecorder struct {
	trusted bool
	err     error
	sets    []hooks.Set
}

func (r *trustRecorder) List(_ string, set hooks.Set) (hooks.Selection, error) {
	r.sets = append(r.sets, set)
	if r.err != nil {
		return hooks.Selection{}, r.err
	}
	return hooks.Selection{
		SHA256:  set.SHA256,
		Trusted: r.trusted || len(set.Hooks) == 0,
		Hooks:   append([]hooks.Hook(nil), set.Hooks...),
	}, nil
}

func TestPrepareRunsCompleteSetInOrderWithClosedProtocolAndChainedCandidate(t *testing.T) {
	requireShell(t)
	fixture := newFixture(t, map[string]string{
		"20-second.sh": `#!/usr/bin/env hook-test-sh
set -eu
test "$PWD" = "$ENGRAM_CANDIDATE"
test -z "${git_dir+x}"
test -z "${EnGrAm_SECRET+x}"
test "${KEEP-}" = "visible"
input=$(cat)
printf '%s' "$input" | grep -F '"operation":"added","path":"topics/hook-input.tmp"' >/dev/null
grep -F 'First hook.' topics/why-files.md >/dev/null
rm topics/hook-input.tmp
printf '\nSecond hook.\n' >> topics/why-files.md
printf 'second-out'
printf 'second-err' >&2
`,
		"10-first.sh": `#!/usr/bin/env hook-test-sh
set -eu
test "$ENGRAM_HOOK_PROTOCOL" = "1"
test "$PWD" = "$ENGRAM_CANDIDATE"
test -z "${GIT_DIR+x}"
test -z "${git_dir+x}"
test -z "${EnGrAm_SECRET+x}"
test "${KEEP-}" = "visible"
cat > topics/hook-input.tmp
printf '%s\n' '{"version":1,"event":"prepare-changeset","changes":[{"operation":"modified","path":"topics/why-files.md"}]}' > topics/expected.tmp
cmp topics/hook-input.tmp topics/expected.tmp
rm topics/expected.tmp
printf '\nFirst hook.\n' >> topics/why-files.md
printf 'first-out'
printf 'first-err' >&2
`,
	})
	appendCandidate(t, fixture.candidate, "\nInitial candidate.\n")
	request := fixture.request(t, false)
	trust := &trustRecorder{trusted: true}
	executor := New(trust)
	executor.TempRoot = fixture.temp
	executor.Environment = []string{
		"PATH=" + os.Getenv("PATH"),
		"KEEP=visible",
		"GIT_DIR=forbidden",
		"git_dir=also-forbidden",
		"EnGrAm_SECRET=forbidden",
	}
	wrapper := filepath.Join(fixture.temp, "hook-test-sh")
	wrapperBytes := []byte("#!/bin/sh\nset -eu\ntest \"$#\" -eq 1\ncase \"$1\" in \"$ENGRAM_BASE\"/.engram/hooks/prepare-changeset/*) ;; *) exit 91 ;; esac\nexec /bin/sh \"$1\"\n")
	if err := os.WriteFile(wrapper, wrapperBytes, 0o700); err != nil {
		t.Fatal(err)
	}
	executor.LookPath = func(name string) (string, error) {
		if name != "hook-test-sh" {
			return "", exec.ErrNotFound
		}
		return wrapper, nil
	}

	result, err := executor.Prepare(t.Context(), request)
	if err != nil {
		var detail *Error
		errors.As(err, &detail)
		t.Fatalf("%v (%#v, diagnostic %#v)", err, detail, detail.Diagnostic)
	}
	if len(result.Diagnostics) != 2 || result.Diagnostics[0].Hook != ".engram/hooks/prepare-changeset/10-first.sh" || result.Diagnostics[1].Hook != ".engram/hooks/prepare-changeset/20-second.sh" {
		t.Fatalf("diagnostics order = %#v", result.Diagnostics)
	}
	if result.Diagnostics[0].Stdout != "first-out" || result.Diagnostics[0].Stderr != "first-err" || result.Diagnostics[1].Stdout != "second-out" || result.Diagnostics[1].Stderr != "second-err" {
		t.Fatalf("diagnostics = %#v", result.Diagnostics)
	}
	if len(trust.sets) != 1 || len(trust.sets[0].Hooks) != 2 || result.SetSHA256 != trust.sets[0].SHA256 {
		t.Fatalf("selected trust set = %#v, result digest %q", trust.sets, result.SetSHA256)
	}
	if result.Validation.Status != checker.StatusComplete || result.Validation.HasErrors() {
		t.Fatalf("validation = %#v", result.Validation)
	}
	if got, want := result.Changes, []struct {
		operation string
		path      string
	}{{"modified", "topics/why-files.md"}}; len(got) != len(want) || string(got[0].Operation) != want[0].operation || got[0].Path != want[0].path {
		t.Fatalf("definitive changes = %#v", got)
	}
	file := result.Final.Tree.Files["topics/why-files.md"]
	if !strings.Contains(string(file.Data), "Initial candidate.\n\nFirst hook.\n\nSecond hook.\n") {
		t.Fatalf("final chained content missing:\n%s", file.Data)
	}
	if _, exists := result.Final.Tree.Files["topics/hook-input.tmp"]; exists {
		t.Fatal("intermediate hook file escaped into final candidate")
	}
	if result.Modes["topics/why-files.md"] != gitraw.ModeRegular {
		t.Fatalf("final mode = %s", result.Modes["topics/why-files.md"])
	}
	if live, err := os.ReadFile(filepath.Join(fixture.store, "topics", "why-files.md")); err != nil || strings.Contains(string(live), "First hook.") {
		t.Fatalf("live store was modified: %v, %q", err, live)
	}
}

func TestInitializationForcesEmptySet(t *testing.T) {
	requireShell(t)
	fixture := newFixture(t, map[string]string{
		"10-must-not-run.sh": "#!/usr/bin/env sh\nprintf '\\nran\\n' >> topics/why-files.md\n",
	})
	request := fixture.request(t, true)
	trust := &trustRecorder{}
	executor := New(trust)
	executor.TempRoot = fixture.temp
	result, err := executor.Prepare(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(trust.sets) != 1 || len(trust.sets[0].Hooks) != 0 || len(result.Diagnostics) != 0 {
		t.Fatalf("initialization selected hooks: %#v, diagnostics %#v", trust.sets, result.Diagnostics)
	}
	if strings.Contains(string(result.Final.Tree.Files["topics/why-files.md"].Data), "\nran\n") {
		t.Fatal("initialization executed a candidate hook")
	}
}

func TestPrepareRejectsUntrustedSetBeforeExecution(t *testing.T) {
	requireShell(t)
	fixture := newFixture(t, map[string]string{
		"10-no.sh": "#!/usr/bin/env sh\nprintf '\\nshould-not-run\\n' >> topics/why-files.md\n",
	})
	appendCandidate(t, fixture.candidate, "\nInitial candidate.\n")
	request := fixture.request(t, false)
	trust := &trustRecorder{trusted: false}
	executor := New(trust)
	executor.TempRoot = fixture.temp
	result, err := executor.Prepare(t.Context(), request)
	if result != nil || KindOf(err) != ErrorTrust || !errors.Is(err, ErrUntrusted) {
		t.Fatalf("result, error = %#v, %v", result, err)
	}
}

func TestPrepareRejectsOriginalCandidateBoundaryBeforeMaterialization(t *testing.T) {
	fixture := newFixture(t, nil)
	request := fixture.request(t, false)
	request.Initial.Tree.Issues = append(request.Initial.Tree.Issues, snapshot.Issue{Code: "E103", Path: "escape"})
	executor := New(&trustRecorder{})
	executor.TempRoot = fixture.temp
	result, err := executor.Prepare(t.Context(), request)
	if result != nil || KindOf(err) != ErrorHook || !errors.Is(err, ErrRejected) {
		t.Fatalf("result, error = %#v, %v", result, err)
	}
}

func TestPrepareRejectsNonzeroExitWithBoundedDiagnostics(t *testing.T) {
	requireShell(t)
	fixture := newFixture(t, map[string]string{
		"10-reject.sh": `#!/usr/bin/env sh
printf 'abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ'
printf 'rejected-stderr' >&2
exit 7
`,
	})
	appendCandidate(t, fixture.candidate, "\nInitial candidate.\n")
	executor := New(&trustRecorder{trusted: true})
	executor.TempRoot = fixture.temp
	executor.DiagnosticLimit = 16
	result, err := executor.Prepare(t.Context(), fixture.request(t, false))
	if result != nil || KindOf(err) != ErrorHook || !errors.Is(err, ErrRejected) {
		t.Fatalf("result, error = %#v, %v", result, err)
	}
	var hookError *Error
	if !errors.As(err, &hookError) || hookError.Diagnostic == nil || len(hookError.Diagnostic.Stdout) != 16 || !hookError.Diagnostic.StdoutTruncated || hookError.Diagnostic.Stderr != "rejected-stderr" {
		t.Fatalf("hook diagnostic = %#v", hookError)
	}
}

func TestPrepareRejectsBaseTampering(t *testing.T) {
	requireShell(t)
	fixture := newFixture(t, map[string]string{
		"10-tamper.sh": `#!/usr/bin/env sh
set -eu
chmod u+w "$ENGRAM_BASE" "$ENGRAM_BASE/topics" "$ENGRAM_BASE/topics/why-files.md"
printf '\ntampered\n' >> "$ENGRAM_BASE/topics/why-files.md"
`,
	})
	appendCandidate(t, fixture.candidate, "\nInitial candidate.\n")
	executor := New(&trustRecorder{trusted: true})
	executor.TempRoot = fixture.temp
	result, err := executor.Prepare(t.Context(), fixture.request(t, false))
	if result != nil || KindOf(err) != ErrorHook || !strings.Contains(err.Error(), "base changed") {
		t.Fatalf("result, error = %#v, %v", result, err)
	}
}

func TestPrepareRejectsPrunedAndSymlinkHookOutput(t *testing.T) {
	requireShell(t)
	tests := map[string]string{
		"boundary": `#!/usr/bin/env sh
rm "$ENGRAM_CANDIDATE/README.md"
mkdir "$ENGRAM_CANDIDATE/README.md"
`,
		"cache": `#!/usr/bin/env sh
mkdir -p "$ENGRAM_CANDIDATE/.engram/cache"
printf cache > "$ENGRAM_CANDIDATE/.engram/cache/value"
`,
		"symlink": `#!/usr/bin/env sh
ln -s topics/why-files.md "$ENGRAM_CANDIDATE/escape"
`,
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newFixture(t, map[string]string{"10-invalid.sh": body})
			appendCandidate(t, fixture.candidate, "\nInitial candidate.\n")
			executor := New(&trustRecorder{trusted: true})
			executor.TempRoot = fixture.temp
			result, err := executor.Prepare(t.Context(), fixture.request(t, false))
			if result != nil || KindOf(err) != ErrorHook || !errors.Is(err, ErrRejected) {
				t.Fatalf("result, error = %#v, %v", result, err)
			}
		})
	}
}

func TestPrepareDetectsCandidateChangeDuringPrivateCapture(t *testing.T) {
	requireShell(t)
	fixture := newFixture(t, map[string]string{
		"10-ok.sh": "#!/usr/bin/env sh\nprintf '\\nHook output.\\n' >> topics/why-files.md\n",
	})
	appendCandidate(t, fixture.candidate, "\nInitial candidate.\n")
	executor := New(&trustRecorder{trusted: true})
	executor.TempRoot = fixture.temp
	executor.afterSourceCapture = func(candidateRoot string) {
		name := filepath.Join(candidateRoot, "topics", "why-files.md")
		content, err := os.ReadFile(name)
		if err != nil {
			t.Errorf("read concurrent candidate: %v", err)
			return
		}
		if err := os.WriteFile(name, append(content, []byte("\nlate change\n")...), 0o644); err != nil {
			t.Errorf("write concurrent candidate: %v", err)
		}
	}
	result, err := executor.Prepare(t.Context(), fixture.request(t, false))
	if result != nil || KindOf(err) != ErrorConcurrency || !errors.Is(err, ErrConcurrent) {
		t.Fatalf("result, error = %#v, %v", result, err)
	}
}

func TestPrepareClassifiesUnavailableInterpreterAsCapability(t *testing.T) {
	fixture := newFixture(t, map[string]string{
		"10-missing.sh": "#!/usr/bin/env definitely-not-an-engram-interpreter\n",
	})
	appendCandidate(t, fixture.candidate, "\nInitial candidate.\n")
	executor := New(&trustRecorder{trusted: true})
	executor.TempRoot = fixture.temp
	executor.LookPath = func(string) (string, error) { return "", exec.ErrNotFound }
	result, err := executor.Prepare(t.Context(), fixture.request(t, false))
	if result != nil || KindOf(err) != ErrorCapability || !errors.Is(err, ErrCapability) {
		t.Fatalf("result, error = %#v, %v", result, err)
	}
}

type fixture struct {
	store     string
	candidate string
	temp      string
}

func newFixture(t *testing.T, programs map[string]string) fixture {
	t.Helper()
	root := t.TempDir()
	store := filepath.Join(root, "store")
	candidate := filepath.Join(root, "candidate")
	temp := filepath.Join(root, "controller-temp")
	if err := copyTree(filepath.Join(repositoryRoot(t), "examples", "minimal"), store); err != nil {
		t.Fatal(err)
	}
	hookDirectory := filepath.Join(store, ".engram", "hooks", "prepare-changeset")
	if err := os.MkdirAll(hookDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, program := range programs {
		if err := os.WriteFile(filepath.Join(hookDirectory, name), []byte(program), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := copyTree(store, candidate); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(temp, 0o700); err != nil {
		t.Fatal(err)
	}
	return fixture{store: store, candidate: candidate, temp: temp}
}

func (f fixture) request(t *testing.T, initialization bool) Request {
	t.Helper()
	initial, err := checker.CheckFS(f.candidate)
	if err != nil {
		t.Fatal(err)
	}
	request := Request{
		StoreRoot:      f.store,
		WorktreeRoot:   f.store,
		Initial:        initial,
		InitialModes:   regularModes(initial),
		Initialization: initialization,
	}
	if !initialization {
		base, err := checker.CheckFS(f.store)
		if err != nil {
			t.Fatal(err)
		}
		request.Base = base
		request.BaseModes = regularModes(base)
	}
	return request
}

func regularModes(value *checker.Snapshot) map[string]gitraw.TreeMode {
	result := make(map[string]gitraw.TreeMode, len(value.Tree.Files))
	for name := range value.Tree.Files {
		result[name] = gitraw.ModeRegular
	}
	return result
}

func appendCandidate(t *testing.T, candidate, value string) {
	t.Helper()
	name := filepath.Join(candidate, "topics", "why-files.md")
	file, err := os.OpenFile(name, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(value); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func requireShell(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell hook fixture")
	}
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skipf("sh unavailable: %v", err)
	}
}

func copyTree(source, destination string) error {
	return filepath.WalkDir(source, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, name)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if entry.Type()&os.ModeSymlink != 0 {
			link, err := os.Readlink(name)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		}
		content, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		return os.WriteFile(target, content, info.Mode().Perm())
	})
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func TestResultDoesNotAliasInputSnapshots(t *testing.T) {
	requireShell(t)
	fixture := newFixture(t, nil)
	appendCandidate(t, fixture.candidate, "\nInitial candidate.\n")
	request := fixture.request(t, false)
	executor := New(&trustRecorder{})
	executor.TempRoot = fixture.temp
	result, err := executor.Prepare(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	before := append([]byte(nil), result.Final.Tree.Files["topics/why-files.md"].Data...)
	input := request.Initial.Tree.Files["topics/why-files.md"]
	input.Data[0] ^= 0xff
	request.Initial.Tree.Files["topics/why-files.md"] = input
	if !reflect.DeepEqual(before, result.Final.Tree.Files["topics/why-files.md"].Data) {
		t.Fatal("result aliases caller snapshot bytes")
	}
}

func ExampleErrorKind() {
	fmt.Println(ErrorTrust, ErrorHook, ErrorCapability, ErrorConcurrency)
	// Output: trust hook capability concurrency
}
