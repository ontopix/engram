package version

import (
	"context"
	"runtime"
	"runtime/debug"

	"github.com/ontopix/engram/internal/gitcap"
)

var (
	CLIVersion    = "1.0.0-rc.1"
	BuildRevision = ""
)

const (
	coreRevision = "2026-08-11"
	coreSHA256   = "19ae8dc527c9e7519b202d0eef3a23ab08fe99e1733a49f52304bcc380d587ad"
	gitRevision  = "2026-08-11"
	gitSHA256    = "7912f9f77ee5d87be06f1680c6924c8ad86453e809a5be0e7c05c21b41414193"
)

type Specification struct {
	ID       string `json:"id"`
	Version  string `json:"version"`
	Revision string `json:"revision"`
	SHA256   string `json:"sha256"`
}

type GitCapability struct {
	Version   *string `json:"version"`
	Supported bool    `json:"supported"`
}

type Build struct {
	Go       string  `json:"go"`
	OS       string  `json:"os"`
	Arch     string  `json:"arch"`
	Revision *string `json:"revision"`
}

type Info struct {
	CLIVersion    string          `json:"cli_version"`
	CoreVersions  []Specification `json:"core_versions"`
	AnnexVersions []Specification `json:"annex_versions"`
	Git           GitCapability   `json:"git"`
	Build         Build           `json:"build"`
}

type GitProber interface {
	Probe(context.Context) GitCapability
}

type systemGitProber struct{}

func (systemGitProber) Probe(ctx context.Context) GitCapability {
	report, err := gitcap.Probe(ctx)
	var version *string
	if report.Version != "" {
		value := report.Version
		version = &value
	}
	return GitCapability{Version: version, Supported: err == nil && report.Supported}
}

type Provider struct {
	Git GitProber
}

func NewProvider() Provider {
	return Provider{Git: systemGitProber{}}
}

func (p Provider) Info(ctx context.Context) Info {
	revision := BuildRevision
	if revision == "" {
		revision = vcsRevision()
	}
	var revisionPointer *string
	if revision != "" {
		revisionPointer = &revision
	}
	git := GitCapability{}
	if p.Git != nil {
		git = p.Git.Probe(ctx)
	}
	return Info{
		CLIVersion: CLIVersion,
		CoreVersions: []Specification{{
			ID: "core", Version: "v1", Revision: coreRevision, SHA256: coreSHA256,
		}},
		AnnexVersions: []Specification{{
			ID: "git", Version: "v1", Revision: gitRevision, SHA256: gitSHA256,
		}},
		Git: git,
		Build: Build{
			Go: runtime.Version(), OS: runtime.GOOS, Arch: runtime.GOARCH, Revision: revisionPointer,
		},
	}
}

func vcsRevision() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	for _, setting := range info.Settings {
		if setting.Key == "vcs.revision" {
			return setting.Value
		}
	}
	return ""
}
