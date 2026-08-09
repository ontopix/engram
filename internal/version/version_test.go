package version

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

type staticGitProber struct {
	capability GitCapability
}

func (p staticGitProber) Probe(context.Context) GitCapability { return p.capability }

func TestProviderInfo(t *testing.T) {
	t.Parallel()
	gitVersion := "git version test"
	provider := Provider{Git: staticGitProber{capability: GitCapability{Version: &gitVersion, Supported: true}}}
	info := provider.Info(context.Background())
	if info.CLIVersion == "" || len(info.CoreVersions) != 1 || len(info.AnnexVersions) != 1 {
		t.Fatalf("info = %#v", info)
	}
	if info.CoreVersions[0].ID != "core" || info.AnnexVersions[0].ID != "git" {
		t.Fatalf("specifications = %#v %#v", info.CoreVersions, info.AnnexVersions)
	}
	if !info.Git.Supported || info.Git.Version == nil || *info.Git.Version != gitVersion {
		t.Fatalf("git = %#v", info.Git)
	}
	if info.Build.Go == "" || info.Build.OS == "" || info.Build.Arch == "" {
		t.Fatalf("build = %#v", info.Build)
	}
}

func TestSpecificationDigestsMatchAuthoritativeBytes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		path string
		want string
	}{
		{filepath.Join("..", "..", "docs", "spec", "README.md"), coreSHA256},
		{filepath.Join("..", "..", "docs", "spec", "annex-git.md"), gitSHA256},
	}
	for _, test := range tests {
		content, err := os.ReadFile(test.path)
		if err != nil {
			t.Fatal(err)
		}
		got := fmt.Sprintf("%x", sha256.Sum256(content))
		if got != test.want {
			t.Errorf("digest of %s = %s, want %s", test.path, got, test.want)
		}
	}
}
