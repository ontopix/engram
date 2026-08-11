package gitpresent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestConfigureInstallsExactLocalPresentation(t *testing.T) {
	root := t.TempDir()
	runGit(t, root, "init", "--initial-branch=main")
	if err := Configure(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"core.autocrlf", "core.sparseCheckout", "core.sparseCheckoutCone", "index.sparse"} {
		command := exec.Command("git", "-C", root, "config", "--local", "--get", key)
		command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
		output, err := command.Output()
		if err != nil || string(output) != "false\n" {
			t.Fatalf("%s = %q, %v", key, output, err)
		}
	}
	if ok, err := HasCacheExclusion(filepath.Join(root, ".git")); err != nil || !ok {
		t.Fatalf("cache exclusion = %v, %v", ok, err)
	}
}

func TestInstallCacheExclusionPreservesExistingBytes(t *testing.T) {
	gitDir := t.TempDir()
	name := filepath.Join(gitDir, "info", "exclude")
	if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte("existing/"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := InstallCacheExclusion(gitDir); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "existing/\n.engram/cache/\n" {
		t.Fatalf("exclude = %q", content)
	}
	if err := InstallCacheExclusion(gitDir); err != nil {
		t.Fatal(err)
	}
	again, _ := os.ReadFile(name)
	if string(again) != string(content) {
		t.Fatalf("second installation changed bytes: %q", again)
	}
}

func runGit(t *testing.T, root string, arguments ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, arguments...)...)
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git: %v\n%s", err, output)
	}
}
