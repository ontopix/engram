package networkgit

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ontopix/engram/internal/gitraw"
)

func TestPrivateContextReadsAlternateAndPromotesExactObjects(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("system Git is unavailable")
	}
	for _, format := range []gitraw.ObjectFormat{gitraw.SHA1, gitraw.SHA256} {
		t.Run(string(format), func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "repository.git")
			arguments := []string{"init", "--bare", "--quiet"}
			if format == gitraw.SHA256 {
				arguments = append(arguments, "--object-format=sha256")
			}
			arguments = append(arguments, root)
			runGit(t, "", nil, arguments...)

			localData := []byte("already local\n")
			localOID := strings.TrimSpace(runGit(t, root, localData, "hash-object", "-w", "--stdin"))
			private, err := New(root, format)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = private.Close() })
			if got := runGit(t, private.Root(), nil, "cat-file", "blob", localOID); got != string(localData) {
				t.Fatalf("alternate object bytes = %q", got)
			}

			fetchedData := []byte("fetched privately\n")
			fetchedOID := strings.TrimSpace(runGit(t, private.Root(), fetchedData, "hash-object", "-w", "--stdin"))
			runGit(t, private.Root(), nil, "update-ref", "refs/tags/private-data", fetchedOID)
			runGit(t, private.Root(), nil, "repack", "-ad")
			if command := gitCommand(root, nil, "cat-file", "-e", fetchedOID); command.Run() == nil {
				t.Fatal("private object was visible before promotion")
			}
			if err := private.Promote(); err != nil {
				t.Fatal(err)
			}
			if got := runGit(t, root, nil, "cat-file", "blob", fetchedOID); got != string(fetchedData) {
				t.Fatalf("promoted object bytes = %q", got)
			}
			if command := gitCommand(root, nil, "show-ref", "--verify", "--quiet", "refs/tags/private-data"); command.Run() == nil {
				t.Fatal("private ref escaped object-only promotion")
			}
			if err := private.Promote(); err != nil {
				t.Fatalf("idempotent promotion: %v", err)
			}
		})
	}
}

func runGit(t *testing.T, root string, input []byte, arguments ...string) string {
	t.Helper()
	command := gitCommand(root, input, arguments...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(arguments, " "), err, output)
	}
	return string(output)
}

func gitCommand(root string, input []byte, arguments ...string) *exec.Cmd {
	gitArguments := []string{"--no-pager", "--no-optional-locks", "--no-replace-objects"}
	if root != "" {
		gitArguments = append(gitArguments, "-C", root)
	}
	command := exec.Command("git", append(gitArguments, arguments...)...)
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_COUNT=0")
	command.Stdin = bytes.NewReader(input)
	return command
}
