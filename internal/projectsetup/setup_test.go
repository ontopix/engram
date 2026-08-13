package projectsetup

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ontopix/engram/internal/acquire"
	"github.com/ontopix/engram/internal/attachment"
	"github.com/ontopix/engram/internal/checker"
	"github.com/ontopix/engram/internal/managedread"
)

func TestDecodeConfigStrictSurface(t *testing.T) {
	valid := []byte("version: 1\nharness: codex\nattachments:\n  - name: primary-memory\n    url: git@github.com:ontopix/memory.git\n")
	config, err := decodeConfig(valid)
	if err != nil || config.Version != 1 || config.Harness != "codex" || len(config.Attachments) != 1 || config.Attachments[0].Name != "primary-memory" {
		t.Fatalf("config = %#v, %v", config, err)
	}

	tests := []struct {
		name string
		data string
	}{
		{"version", "version: 2\nharness: codex\n"},
		{"unknown field", "version: 1\nharness: codex\nextra: true\n"},
		{"null harness", "version: 1\nharness: null\n"},
		{"null attachments", "version: 1\nharness: codex\nattachments: null\n"},
		{"duplicate key", "version: 1\nversion: 1\nharness: codex\n"},
		{"unsupported harness", "version: 1\nharness: other\n"},
		{"invalid name", "version: 1\nharness: codex\nattachments:\n  - name: ../escape\n    url: git@example.test:memory.git\n"},
		{"reserved Windows name", "version: 1\nharness: codex\nattachments:\n  - name: con\n    url: git@example.test:memory.git\n"},
		{"duplicate name", "version: 1\nharness: codex\nattachments:\n  - name: same\n    url: git@example.test:one.git\n  - name: same\n    url: git@example.test:two.git\n"},
		{"duplicate URL", "version: 1\nharness: codex\nattachments:\n  - name: one\n    url: git@example.test:same.git\n  - name: two\n    url: git@example.test:same.git\n"},
		{"password", "version: 1\nharness: codex\nattachments:\n  - name: private\n    url: https://user:secret@example.test/memory.git\n"},
		{"alias", "version: 1\nharness: &runtime codex\nattachments:\n  - name: memory\n    url: git@example.test:memory.git\n  - name: second\n    url: *runtime\n"},
		{"documents", "version: 1\nharness: codex\n---\nversion: 1\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := decodeConfig([]byte(test.data)); err == nil {
				t.Fatalf("decodeConfig(%q) succeeded", test.data)
			}
		})
	}
}

func TestRunDryRunPlansWithoutWritingOrAcquiring(t *testing.T) {
	project := t.TempDir()
	writeConfig(t, project, "version: 1\nharness: codex\nattachments:\n  - name: primary\n    url: git@example.test:memory.git\n")
	cloneCalled := false
	result, err := Run(context.Background(), Options{
		Project: project, DryRun: true,
		AcquireClone: func(context.Context, string, acquire.Options) (acquire.Result, error) {
			cloneCalled = true
			return acquire.Result{}, errors.New("unexpected clone")
		},
	})
	if err != nil || cloneCalled || !result.Changed || result.ConfigFile == nil || result.MemoryDir == nil || len(result.Attachments) != 1 || result.Attachments[0].Action != "clone" {
		t.Fatalf("result = %#v, clone=%v, error=%v", result, cloneCalled, err)
	}
	for _, name := range []string{".memory", ".gitignore", "MEMORY.md", "AGENTS.md", ".agents"} {
		if _, err := os.Lstat(filepath.Join(project, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("dry-run created %s: %v", name, err)
		}
	}
	paths := changePaths(result.Files)
	for _, wanted := range []string{".gitignore", "MEMORY.md", "AGENTS.md", ".agents/skills/using-engram/SKILL.md"} {
		if !paths[wanted] {
			t.Fatalf("planned files %v do not include %s", paths, wanted)
		}
	}
}

func TestRunConvergesReusesAndDetachesWithoutDeleting(t *testing.T) {
	project := t.TempDir()
	writeConfig(t, project, "version: 1\nharness: codex\nattachments:\n  - name: first\n    url: git@example.test:first.git\n  - name: second\n    url: git@example.test:second.git\n")
	cloneCount := 0
	reuseCount := 0
	clone := func(_ context.Context, _ string, options acquire.Options) (acquire.Result, error) {
		cloneCount++
		if err := os.Mkdir(options.Destination, 0o755); err != nil {
			return acquire.Result{}, err
		}
		return validAcquisition(options.Destination, true), nil
	}
	reuse := func(_ context.Context, _ string, destination string) (acquire.Result, error) {
		reuseCount++
		return validAcquisition(destination, false), nil
	}
	first, err := Run(context.Background(), Options{Project: project, AcquireClone: clone, AcquireReuse: reuse})
	if err != nil || !first.Changed || cloneCount != 2 || reuseCount != 0 {
		t.Fatalf("first = %#v, clone=%d reuse=%d error=%v", first, cloneCount, reuseCount, err)
	}
	ignore, err := os.ReadFile(filepath.Join(project, ".gitignore"))
	if err != nil || !bytes.Contains(ignore, []byte("/.memory/")) {
		t.Fatalf("gitignore = %q, %v", ignore, err)
	}
	memory, err := os.ReadFile(filepath.Join(project, "MEMORY.md"))
	if err != nil || !bytes.Contains(memory, []byte(`"path": ".memory/first"`)) || !bytes.Contains(memory, []byte(`"path": ".memory/second"`)) {
		t.Fatalf("memory = %q, %v", memory, err)
	}

	second, err := Run(context.Background(), Options{Project: project, AcquireClone: clone, AcquireReuse: reuse})
	if err != nil || second.Changed || cloneCount != 2 || reuseCount != 2 {
		t.Fatalf("second = %#v, clone=%d reuse=%d error=%v", second, cloneCount, reuseCount, err)
	}

	external := filepath.Join(t.TempDir(), "external")
	if err := os.Mkdir(external, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := attachment.Attach(project, "", external); err != nil {
		t.Fatal(err)
	}
	writeConfig(t, project, "version: 1\nharness: codex\nattachments:\n  - name: first\n    url: git@example.test:first.git\n")
	third, err := Run(context.Background(), Options{Project: project, AcquireClone: clone, AcquireReuse: reuse})
	if err != nil || !third.Changed || cloneCount != 2 || reuseCount != 3 {
		t.Fatalf("third = %#v, clone=%d reuse=%d error=%v", third, cloneCount, reuseCount, err)
	}
	memory, err = os.ReadFile(filepath.Join(project, "MEMORY.md"))
	externalRelative, relativeErr := filepath.Rel(project, external)
	externalJSONPath := `"path": "` + filepath.ToSlash(externalRelative) + `"`
	if err != nil || relativeErr != nil || bytes.Contains(memory, []byte(`"path": ".memory/second"`)) || !bytes.Contains(memory, []byte(externalJSONPath)) {
		t.Fatalf("reconciled memory = %q, %v", memory, err)
	}
	if _, err := os.Stat(filepath.Join(project, ".memory", "second")); err != nil {
		t.Fatalf("detachment deleted clone: %v", err)
	}
}

func TestRunHarnessOptionOverridesManifest(t *testing.T) {
	project := t.TempDir()
	writeConfig(t, project, "version: 1\nharness: claude-code\nattachments: []\n")
	result, err := Run(context.Background(), Options{Project: project, Harness: "codex", DryRun: true})
	if err != nil || result.Harness != "codex" || filepath.Base(result.Entrypoint) != "AGENTS.md" {
		t.Fatalf("result = %#v, %v", result, err)
	}
	config, _, err := LoadConfig(project)
	if err != nil || config.Harness != "claude-code" {
		t.Fatalf("override modified config: %#v, %v", config, err)
	}
}

func TestRunWithoutManifestRetainsImperativeSetup(t *testing.T) {
	project := t.TempDir()
	if _, err := Run(context.Background(), Options{Project: project, DryRun: true}); !errors.Is(err, ErrUsage) {
		t.Fatalf("missing harness error = %v", err)
	}
	result, err := Run(context.Background(), Options{Project: project, Harness: "codex", DryRun: true})
	if err != nil || result.ConfigFile != nil || result.MemoryDir != nil || !result.Changed {
		t.Fatalf("imperative result = %#v, %v", result, err)
	}
}

func writeConfig(t *testing.T, project, data string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(project, ConfigFileName), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func validAcquisition(root string, published bool) acquire.Result {
	return acquire.Result{
		Root: root, Published: published, Reused: !published,
		Validation: checker.Result{Target: checker.TargetManagedStore, Status: checker.StatusComplete, Findings: []checker.Finding{}},
		Audits:     []managedread.HistoryAudit{},
	}
}

func changePaths(changes []FileChange) map[string]bool {
	result := make(map[string]bool, len(changes))
	for _, change := range changes {
		result[change.Path] = true
	}
	return result
}

func TestPlanGitignoreRejectsModifiedOwnedBlock(t *testing.T) {
	project := t.TempDir()
	data := strings.Join([]string{gitignoreOpen, "/other/", gitignoreClose, ""}, "\n")
	if err := os.WriteFile(filepath.Join(project, ".gitignore"), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := planGitignore(project); !errors.Is(err, ErrConflict) {
		t.Fatalf("error = %v, want conflict", err)
	}
}

func TestPlanGitignorePreservesCRLFAndRecognizesManualRule(t *testing.T) {
	project := t.TempDir()
	name := filepath.Join(project, ".gitignore")
	if err := os.WriteFile(name, []byte("/vendor/\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	plan, err := planGitignore(project)
	if err != nil || plan == nil {
		t.Fatalf("plan = %#v, %v", plan, err)
	}
	if bytes.Contains(bytes.ReplaceAll(plan.updated, []byte("\r\n"), nil), []byte("\n")) {
		t.Fatalf("plan introduced bare LF: %q", plan.updated)
	}
	if err := publishFile(plan); err != nil {
		t.Fatal(err)
	}
	if next, err := planGitignore(project); err != nil || next != nil {
		t.Fatalf("idempotent plan = %#v, %v", next, err)
	}

	manual := t.TempDir()
	if err := os.WriteFile(filepath.Join(manual, ".gitignore"), []byte("/.memory/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if next, err := planGitignore(manual); err != nil || next != nil {
		t.Fatalf("manual rule plan = %#v, %v", next, err)
	}
}
