package remoteselect

import (
	"context"
	"os"
	"os/exec"
	"reflect"
	"testing"
)

func TestValidBranch(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"main", "feature/topic", "release-1.0", "mémoire"} {
		if !ValidBranch(value) {
			t.Errorf("ValidBranch(%q) = false", value)
		}
	}
	for _, value := range []string{"", "-option", "/main", "main/", ".hidden", "a//b", "a..b", "a@{b", "a.lock", "a/.b", "a b", "a:b", "a?b", "a\\b", "@"} {
		if ValidBranch(value) {
			t.Errorf("ValidBranch(%q) = true", value)
		}
	}
}

func TestSelectConfiguredUpstreamAndExplicitForms(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	root := t.TempDir()
	runGit(t, root, "init", "--initial-branch=main")
	runGit(t, root, "remote", "add", "origin", "https://example.test/fetch.git")
	runGit(t, root, "remote", "set-url", "--add", "--push", "origin", "ssh://git@example.test/push.git")
	runGit(t, root, "config", "branch.main.remote", "origin")
	runGit(t, root, "config", "branch.main.merge", "refs/heads/trunk")

	fetch, err := Select(context.Background(), root, "refs/heads/main", nil, Fetch)
	if err != nil {
		t.Fatal(err)
	}
	wantFetch := Selection{Remote: "origin", Branch: "trunk", RemoteRef: "refs/heads/trunk", URL: "https://example.test/fetch.git"}
	if !reflect.DeepEqual(fetch, wantFetch) {
		t.Fatalf("fetch = %#v, want %#v", fetch, wantFetch)
	}
	push, err := Select(context.Background(), root, "refs/heads/main", []string{"origin"}, Push)
	if err != nil {
		t.Fatal(err)
	}
	wantPush := Selection{Remote: "origin", Branch: "main", RemoteRef: "refs/heads/main", URL: "ssh://git@example.test/push.git"}
	if !reflect.DeepEqual(push, wantPush) {
		t.Fatalf("push = %#v, want %#v", push, wantPush)
	}
}

func TestSelectRejectsMultipleEffects(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	root := t.TempDir()
	runGit(t, root, "init", "--initial-branch=main")
	runGit(t, root, "remote", "add", "origin", "https://example.test/one.git")
	runGit(t, root, "remote", "set-url", "--add", "origin", "https://example.test/two.git")
	if _, err := Select(context.Background(), root, "refs/heads/main", []string{"origin"}, Fetch); err == nil {
		t.Fatal("multiple fetch URLs accepted")
	}
}

func runGit(t *testing.T, root string, arguments ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, arguments...)...)
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", arguments, err, output)
	}
}
