package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSessionContextReadsOneAcceptedCommit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the integration example requires a POSIX shell")
	}
	binary, root := buildExecutable(t)
	environment := executableTestEnvironment(t)
	store := filepath.Join(t.TempDir(), "memory with spaces")
	run := func(arguments ...string) {
		t.Helper()
		status, stdout, stderr := runExecutableWithEnvironment(t, binary, root, environment, arguments...)
		if status != 0 {
			t.Fatalf("engram %q: status=%d stdout=%s stderr=%s", arguments, status, stdout, stderr)
		}
	}
	write := func(name, content string) {
		t.Helper()
		path := filepath.Join(store, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run("init", store)
	run("--store", store, "new", "note@v1", "record.md", "--description", "retrieval-accepted-original", "--title", "Original")
	run("--store", store, "add", "record.md", "README.md")
	run("--store", store, "commit", "-m", "Accept original")
	original := strings.TrimSpace(runFixtureGit(t, environment, "-C", store, "rev-parse", "HEAD"))
	ref := strings.TrimSpace(runFixtureGit(t, environment, "-C", store, "symbolic-ref", "HEAD"))

	write("record.md", "---\ntype: note@v1\ndescription: retrieval-accepted-later\n---\nLater\n")
	run("--store", store, "fmt")
	run("--store", store, "add", "record.md", "README.md")
	run("--store", store, "commit", "-m", "Accept later")
	later := strings.TrimSpace(runFixtureGit(t, environment, "-C", store, "rev-parse", "HEAD"))
	// Keep the later tree as a draft while selecting the earlier accepted state.
	runFixtureGit(t, environment, "-C", store, "update-ref", ref, original, later)
	write("record.md", "retrieval-unstaged-draft\n")
	write("README.md", "retrieval-draft-map\n")
	write("untracked.md", "retrieval-untracked\n")
	write(".engram/cache/ignored.md", "retrieval-cache\n")
	// A live attribute must not change which accepted Markdown is searchable.
	write(".gitattributes", "*.md binary\n")

	script := filepath.Join(root, "examples", "integrations", "generic-json", "session-context.sh")
	baseEnvironment := replaceEnvironment(environment, map[string]string{
		"PATH": filepath.Dir(binary) + string(os.PathListSeparator) + os.Getenv("PATH"),
	})
	read := func(env []string, first, second string) string {
		t.Helper()
		status, stdout, stderr := runExecutableWithEnvironment(t, "sh", root, env, script, store, first, second)
		if status != 0 || stderr != "" {
			t.Fatalf("session context: status=%d stdout=%s stderr=%s", status, stdout, stderr)
		}
		return stdout
	}
	assertOriginal := func(output string) {
		t.Helper()
		if !strings.Contains(output, "Reading accepted commit: "+original) ||
			!strings.Contains(output, original+":record.md:") ||
			!strings.Contains(output, "retrieval-accepted-original") {
			t.Fatalf("missing original accepted record: %s", output)
		}
		for _, forbidden := range []string{"retrieval-accepted-later", "retrieval-unstaged-draft", "retrieval-draft-map", "retrieval-untracked", "retrieval-cache"} {
			if strings.Contains(output, forbidden) {
				t.Errorf("retrieval exposed %q: %s", forbidden, output)
			}
		}
	}
	assertOriginal(read(baseEnvironment, "retrieval", "memory"))
	if output := read(baseEnvironment, "no-such-phrase-1", "no-such-phrase-2"); strings.Contains(output, original+":record.md:") {
		t.Fatalf("no-match query unexpectedly returned a record: %s", output)
	}

	// Advance the accepted ref after the root blob is read. Searches must still
	// refer to the original object ID, not the now-current HEAD.
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	shimDir := t.TempDir()
	shim := "#!/bin/sh\n" + gitCommand(realGit) + " \"$@\" || exit $?\n" +
		"case \" $* \" in *' cat-file blob '*)\n" +
		gitCommand(realGit, "-C", store, "update-ref", ref, later, original) + "\n;; esac\n"
	if err := os.WriteFile(filepath.Join(shimDir, "git"), []byte(shim), 0o755); err != nil {
		t.Fatal(err)
	}
	assertOriginal(read(replaceEnvironment(baseEnvironment, map[string]string{
		"PATH": shimDir + string(os.PathListSeparator) + filepath.Dir(binary) + string(os.PathListSeparator) + os.Getenv("PATH"),
	}), "retrieval", "memory"))
	if got := strings.TrimSpace(runFixtureGit(t, environment, "-C", store, "rev-parse", "HEAD")); got != later {
		t.Fatalf("concurrent acceptance fixture did not advance: %s", got)
	}

	// Search failures must not be hidden by the bounded-output pipeline.
	shim = "#!/bin/sh\ncase \" $* \" in *' grep '*) echo 'fixture search failure' >&2; exit 128;; esac\n" +
		"exec " + gitCommand(realGit) + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(shimDir, "git"), []byte(shim), 0o755); err != nil {
		t.Fatal(err)
	}
	status, _, stderr := runExecutableWithEnvironment(t, "sh", root, replaceEnvironment(baseEnvironment, map[string]string{
		"PATH": shimDir + string(os.PathListSeparator) + filepath.Dir(binary) + string(os.PathListSeparator) + os.Getenv("PATH"),
	}), script, store, "retrieval", "memory")
	if status != 128 || !strings.Contains(stderr, "fixture search failure") {
		t.Fatalf("search failure was hidden: status=%d stderr=%s", status, stderr)
	}
}
