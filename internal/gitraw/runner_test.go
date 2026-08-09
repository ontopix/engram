package gitraw

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiscoverAndAuditSHA1AndSHA256Repositories(t *testing.T) {
	for _, format := range []ObjectFormat{SHA1, SHA256} {
		format := format
		t.Run(string(format), func(t *testing.T) {
			repository := makeRepository(t, format)
			discovered, err := Discover(context.Background(), filepath.Join(repository, "topics"))
			if err != nil {
				t.Fatal(err)
			}
			if discovered.Format != format || discovered.Head == nil || discovered.HeadRef != "refs/heads/main" {
				t.Fatalf("repository = %#v", discovered)
			}
			audit, err := discovered.Audit(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if !audit.Complete || len(audit.Findings) != 0 || len(audit.Commits) != 1 || audit.Commits[0].Snapshot == nil {
				t.Fatalf("audit = %#v", audit)
			}
			if _, ok := audit.Commits[0].Snapshot.Files[".engram/root.yaml"]; !ok {
				t.Fatalf("snapshot files = %#v", audit.Commits[0].Snapshot.Files)
			}
		})
	}
}

func TestDiscoverLinkedWorktree(t *testing.T) {
	repository := makeRepository(t, SHA1)
	linked := filepath.Join(t.TempDir(), "linked")
	result := testGit(t, repository, nil, "worktree", "add", "--quiet", "-b", "linked", linked)
	if result.status != 0 {
		t.Skipf("linked worktrees unavailable: %s", result.stderr)
	}
	mainRepository, err := Discover(context.Background(), repository)
	if err != nil {
		t.Fatal(err)
	}
	linkedRepository, err := Discover(context.Background(), linked)
	if err != nil {
		t.Fatal(err)
	}
	if mainRepository.CommonGitDir != linkedRepository.CommonGitDir || mainRepository.GitDir == linkedRepository.GitDir {
		t.Fatalf("main git=%q common=%q, linked git=%q common=%q", mainRepository.GitDir, mainRepository.CommonGitDir, linkedRepository.GitDir, linkedRepository.CommonGitDir)
	}
	if _, err := linkedRepository.Audit(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestBlackBoxMergeStopsBeforeProjection(t *testing.T) {
	repository := makeRepository(t, SHA1)
	testGitOK(t, repository, nil, "checkout", "--quiet", "-b", "side")
	writeTestFile(t, repository, "side.md", "side\n")
	testGitOK(t, repository, nil, "add", "side.md")
	testGitOK(t, repository, nil, "commit", "--quiet", "-m", "Side")
	testGitOK(t, repository, nil, "checkout", "--quiet", "main")
	writeTestFile(t, repository, "main.md", "main\n")
	testGitOK(t, repository, nil, "add", "main.md")
	testGitOK(t, repository, nil, "commit", "--quiet", "-m", "Main")
	testGitOK(t, repository, nil, "merge", "--quiet", "--no-ff", "side", "-m", "Merge")

	discovered, err := Discover(context.Background(), repository)
	if err != nil {
		t.Fatal(err)
	}
	audit, err := discovered.Audit(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !audit.Complete || !hasFinding(audit, "E602") || len(audit.Commits) != 1 || audit.Commits[0].Snapshot != nil {
		t.Fatalf("audit = %#v", audit)
	}
}

func TestBlackBoxPrunedEntryDoesNotResolveMissingTarget(t *testing.T) {
	repository := makeRepository(t, SHA1)
	discovered, err := Discover(context.Background(), repository)
	if err != nil {
		t.Fatal(err)
	}
	rawTree := appendTreeEntry(nil, SHA1, ModeRegular, ".hidden", 0xff)
	treeResult := testGit(t, repository, rawTree, "hash-object", "--literally", "-t", "tree", "-w", "--stdin")
	if treeResult.status != 0 {
		t.Skipf("literal raw tree creation unavailable: %s", treeResult.stderr)
	}
	treeID := strings.TrimSpace(string(treeResult.stdout))
	commitID := strings.TrimSpace(string(testGitOK(t, repository, []byte("Pruned\n"), "commit-tree", treeID)))
	testGitOK(t, repository, nil, "update-ref", "refs/heads/main", commitID, discovered.Head.String())

	discovered, err = Discover(context.Background(), repository)
	if err != nil {
		t.Fatal(err)
	}
	audit, err := discovered.Audit(context.Background())
	if err != nil {
		t.Fatalf("missing pruned target was resolved: %v", err)
	}
	if !audit.Complete || !hasFinding(audit, "E603") {
		t.Fatalf("audit = %#v", audit)
	}
}

func TestRunnerIgnoresHostileGitEnvironmentReplacementsAndGrafts(t *testing.T) {
	repository := makeRepository(t, SHA1)
	hostile := makeRepository(t, SHA1)
	discovered, err := Discover(context.Background(), repository)
	if err != nil {
		t.Fatal(err)
	}
	original, err := discovered.ReadObject(context.Background(), *discovered.Head)
	if err != nil {
		t.Fatal(err)
	}
	originalCommit, err := ParseCommit(SHA1, original.Data)
	if err != nil {
		t.Fatal(err)
	}

	replacementTree := strings.TrimSpace(string(testGitOK(t, repository, nil, "rev-parse", "HEAD^{tree}")))
	replacement := strings.TrimSpace(string(testGitOK(t, repository, []byte("replacement\n"), "commit-tree", replacementTree)))
	testGitOK(t, repository, nil, "replace", discovered.Head.String(), replacement)
	if err := os.MkdirAll(filepath.Join(discovered.GitDir, "info"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(discovered.GitDir, "info", "grafts"), []byte(discovered.Head.String()+" "+replacement+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("GIT_DIR", filepath.Join(hostile, ".git"))
	t.Setenv("GIT_WORK_TREE", hostile)
	t.Setenv("GIT_OBJECT_DIRECTORY", filepath.Join(hostile, ".git", "objects"))
	t.Setenv("GIT_ALTERNATE_OBJECT_DIRECTORIES", filepath.Join(hostile, ".git", "objects"))
	t.Setenv("GIT_REPLACE_REF_BASE", "refs/replace-hostile/")
	t.Setenv("ENGRAM_HOSTILE", "1")

	again, err := Discover(context.Background(), repository)
	if err != nil {
		t.Fatal(err)
	}
	gotObject, err := again.ReadObject(context.Background(), *again.Head)
	if err != nil {
		t.Fatal(err)
	}
	gotCommit, err := ParseCommit(SHA1, gotObject.Data)
	if err != nil {
		t.Fatal(err)
	}
	if gotCommit.Tree != originalCommit.Tree {
		t.Fatalf("replacement changed raw tree: got %s want %s", gotCommit.Tree, originalCommit.Tree)
	}
	audit, err := again.Audit(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(audit.Commits) != 1 || len(audit.Findings) != 0 {
		t.Fatalf("graft/replacement affected raw audit: %#v", audit)
	}
}

func TestReadObjectMissingIsTyped(t *testing.T) {
	repository := makeRepository(t, SHA1)
	discovered, err := Discover(context.Background(), repository)
	if err != nil {
		t.Fatal(err)
	}
	missing := mustOID(t, SHA1, 'f')
	_, err = discovered.ReadObject(context.Background(), missing)
	if !errors.Is(err, ErrMissingObject) {
		t.Fatalf("error = %v, want missing", err)
	}
}

func TestIsolatedGitEnvironmentRemovesGitAndEngramNamesCaseInsensitively(t *testing.T) {
	t.Parallel()
	environment := isolatedGitEnvironment([]string{"PATH=/bin", "GIT_DIR=bad", "git_work_tree=bad", "Engram_X=bad", "KEEP=yes"})
	joined := strings.Join(environment, "\n")
	if strings.Contains(strings.ToUpper(joined), "GIT_DIR=BAD") || strings.Contains(strings.ToUpper(joined), "GIT_WORK_TREE=BAD") || strings.Contains(strings.ToUpper(joined), "ENGRAM_X=BAD") {
		t.Fatalf("environment leaked hostile values: %v", environment)
	}
	if !strings.Contains(joined, "KEEP=yes") || !strings.Contains(joined, "GIT_NO_LAZY_FETCH=1") {
		t.Fatalf("environment = %v", environment)
	}
}

func makeRepository(t *testing.T, format ObjectFormat) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	repository := filepath.Join(t.TempDir(), "repository")
	result := testGit(t, "", nil, "init", "--quiet", "--initial-branch=main", "--object-format="+string(format), repository)
	if result.status != 0 {
		if format == SHA256 {
			t.Skipf("SHA-256 repositories unavailable: %s", result.stderr)
		}
		t.Fatalf("git init: %s", result.stderr)
	}
	testGitOK(t, repository, nil, "config", "user.name", "Engram Test")
	testGitOK(t, repository, nil, "config", "user.email", "engram@example.test")
	testGitOK(t, repository, nil, "config", "commit.gpgsign", "false")
	files := map[string]string{
		"README.md":               "---\ndescription: \"Test store.\"\n---\n# test\n",
		".engram/root.yaml":       "engram: 1\n",
		".engram/schemas/note.md": "---\ntype: note\nversion: 1\ndescription: \"Note.\"\nschema: {}\n---\n# note\n",
		"topics/README.md":        "---\ndescription: \"Topics.\"\n---\n# topics\n",
		"topics/one.md":           "---\ntype: note\ndescription: \"One.\"\n---\n# One\n",
	}
	for name, content := range files {
		writeTestFile(t, repository, name, content)
	}
	testGitOK(t, repository, nil, "add", "--all")
	testGitOK(t, repository, nil, "commit", "--quiet", "-m", "Initial")
	return repository
}

func writeTestFile(t *testing.T, repository, name, content string) {
	t.Helper()
	fullPath := filepath.Join(repository, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fullPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

type testCommandResult struct {
	stdout []byte
	stderr []byte
	status int
}

func testGitOK(t *testing.T, directory string, input []byte, arguments ...string) []byte {
	t.Helper()
	result := testGit(t, directory, input, arguments...)
	if result.status != 0 {
		t.Fatalf("git %s: %s", strings.Join(arguments, " "), result.stderr)
	}
	return result.stdout
}

func testGit(t *testing.T, directory string, input []byte, arguments ...string) testCommandResult {
	t.Helper()
	if directory != "" {
		arguments = append([]string{"-C", directory}, arguments...)
	}
	command := exec.Command("git", arguments...)
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_TERMINAL_PROMPT=0")
	command.Stdin = bytes.NewReader(input)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	status := 0
	if err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			status = exitError.ExitCode()
		} else {
			t.Fatal(err)
		}
	}
	return testCommandResult{stdout: stdout.Bytes(), stderr: stderr.Bytes(), status: status}
}
