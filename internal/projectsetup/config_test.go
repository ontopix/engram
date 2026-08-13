package projectsetup

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestConfigCommandsCreateAndConvergeManifest(t *testing.T) {
	project := t.TempDir()
	first, err := AddConfigAttachment(project, "project-memory", "git@github.com:ontopix/memory.git")
	if err != nil || !first.Changed || first.Config.Version != 1 || len(first.Config.Attachments) != 1 {
		t.Fatalf("add = %#v, %v", first, err)
	}
	unchanged, err := AddConfigAttachment(project, "project-memory", "git@github.com:ontopix/memory.git")
	if err != nil || unchanged.Changed {
		t.Fatalf("idempotent add = %#v, %v", unchanged, err)
	}
	harnessResult, err := SetConfigHarness(project, "codex")
	if err != nil || !harnessResult.Changed || harnessResult.Config.Harness != "codex" {
		t.Fatalf("harness = %#v, %v", harnessResult, err)
	}
	shown, err := ShowConfig(project)
	if err != nil || shown.Changed || shown.Config.Harness != "codex" || len(shown.Config.Attachments) != 1 {
		t.Fatalf("show = %#v, %v", shown, err)
	}
	loaded, _, err := LoadConfig(project)
	if err != nil || loaded.Harness != "codex" || len(loaded.Attachments) != 1 {
		t.Fatalf("loaded = %#v, %v", loaded, err)
	}
	removed, err := RemoveConfigAttachment(project, "project-memory")
	if err != nil || !removed.Changed || len(removed.Config.Attachments) != 0 {
		t.Fatalf("remove = %#v, %v", removed, err)
	}
	missing, err := RemoveConfigAttachment(project, "project-memory")
	if err != nil || missing.Changed {
		t.Fatalf("idempotent remove = %#v, %v", missing, err)
	}
	if _, err := SetConfigHarness(project, "claude-code"); err != nil {
		t.Fatalf("set claude-code harness: %v", err)
	}
}

func TestConfigCommandsPreserveUnrelatedComments(t *testing.T) {
	project := t.TempDir()
	original := []byte("# project setup\nversion: 1\n# selected runtime\nharness: codex\nattachments:\n  # shared context\n  - name: shared\n    url: git@github.com:ontopix/shared.git\n")
	if err := os.WriteFile(filepath.Join(project, ConfigFileName), original, 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := AddConfigAttachment(project, "project", "git@github.com:ontopix/project.git"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(project, ConfigFileName))
	if err != nil {
		t.Fatal(err)
	}
	for _, wanted := range [][]byte{[]byte("# project setup"), []byte("# selected runtime"), []byte("# shared context")} {
		if !bytes.Contains(data, wanted) {
			t.Fatalf("updated manifest lost comment %q: %q", wanted, data)
		}
	}
	info, err := os.Stat(filepath.Join(project, ConfigFileName))
	if err != nil || info.Mode().Perm() != 0o640 {
		t.Fatalf("mode = %v, %v", info.Mode().Perm(), err)
	}
}

func TestConfigCommandsKeepAttachmentsOrderedByName(t *testing.T) {
	project := t.TempDir()
	original := []byte("version: 1\nattachments:\n  # zeta memory\n  - name: zeta\n    url: git@github.com:ontopix/zeta.git\n")
	if err := os.WriteFile(filepath.Join(project, ConfigFileName), original, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := AddConfigAttachment(project, "alpha", "git@github.com:ontopix/alpha.git"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(project, ConfigFileName))
	if err != nil {
		t.Fatal(err)
	}
	alpha := bytes.Index(data, []byte("name: alpha"))
	zeta := bytes.Index(data, []byte("name: zeta"))
	if alpha < 0 || zeta < 0 || alpha >= zeta || !bytes.Contains(data, []byte("# zeta memory")) {
		t.Fatalf("ordered manifest = %q", data)
	}
}

func TestConfigCommandsRejectConflictsWithoutWriting(t *testing.T) {
	project := t.TempDir()
	if _, err := AddConfigAttachment(project, "one", "git@github.com:ontopix/one.git"); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(project, ConfigFileName))
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, location string
	}{
		{"one", "git@github.com:ontopix/other.git"},
		{"other", "git@github.com:ontopix/one.git"},
		{"../escape", "git@github.com:ontopix/escape.git"},
		{"secret", "https://user:password@example.test/secret.git"},
	} {
		if _, err := AddConfigAttachment(project, test.name, test.location); err == nil {
			t.Fatalf("conflicting add %q/%q succeeded", test.name, test.location)
		}
	}
	after, err := os.ReadFile(filepath.Join(project, ConfigFileName))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("conflicts changed manifest: %v\nbefore=%q\nafter=%q", err, before, after)
	}
}

func TestConfigShowAbsentIsReadOnly(t *testing.T) {
	project := t.TempDir()
	result, err := ShowConfig(project)
	if err != nil || result.Changed || result.Config.Version != 1 {
		t.Fatalf("show = %#v, %v", result, err)
	}
	if _, err := os.Lstat(filepath.Join(project, ConfigFileName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("show created manifest: %v", err)
	}
}

func TestConfigCommandRejectsBusyManifest(t *testing.T) {
	project := t.TempDir()
	lock := filepath.Join(project, ConfigFileName+".lock")
	if err := os.WriteFile(lock, []byte("owned\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := SetConfigHarness(project, "codex"); !errors.Is(err, ErrConfigBusy) {
		t.Fatalf("busy error = %v", err)
	}
}

func TestConfigCommandRejectsMalformedManifestWithoutWriting(t *testing.T) {
	project := t.TempDir()
	name := filepath.Join(project, ConfigFileName)
	malformed := []byte("version: 1\nunknown: true\n")
	if err := os.WriteFile(name, malformed, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := SetConfigHarness(project, "codex"); !errors.Is(err, ErrConfig) {
		t.Fatalf("malformed error = %v", err)
	}
	data, err := os.ReadFile(name)
	if err != nil || !bytes.Equal(data, malformed) {
		t.Fatalf("malformed manifest changed: %q, %v", data, err)
	}
}

func TestConfigCommandRejectsChangeImmediatelyBeforePublication(t *testing.T) {
	project := t.TempDir()
	if _, err := SetConfigHarness(project, "codex"); err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(project, ConfigFileName)
	foreign := []byte("version: 1\nharness: claude-code\nattachments: []\n")
	editor := configEditor{beforePublish: func(string) error {
		return os.WriteFile(name, foreign, 0o644)
	}}
	if _, err := editor.addAttachment(project, "project", "git@github.com:ontopix/project.git"); !errors.Is(err, ErrConfigBusy) {
		t.Fatalf("concurrent error = %v", err)
	}
	data, err := os.ReadFile(name)
	if err != nil || !bytes.Equal(data, foreign) {
		t.Fatalf("foreign manifest = %q, %v", data, err)
	}
}

func TestConfigCommandReportsPublicationEffects(t *testing.T) {
	project := t.TempDir()
	injected := errors.New("injected publication failure")
	for _, test := range []struct {
		name        string
		editor      configEditor
		wantDurable bool
	}{
		{name: "after rename", editor: configEditor{afterRename: func(string) error { return injected }}},
		{name: "after sync", editor: configEditor{afterSync: func(string) error { return injected }}, wantDurable: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			caseProject := filepath.Join(project, test.name)
			if err := os.Mkdir(caseProject, 0o755); err != nil {
				t.Fatal(err)
			}
			_, err := test.editor.setHarness(caseProject, "codex")
			effect, present := ConfigEffectOf(err)
			if !errors.Is(err, injected) || !present || effect.Durable != test.wantDurable || effect.RecoveryRequired {
				t.Fatalf("effect/error = %#v/%v", effect, err)
			}
			loaded, _, loadErr := LoadConfig(caseProject)
			if loadErr != nil || loaded == nil || loaded.Harness != "codex" {
				t.Fatalf("published config = %#v, %v", loaded, loadErr)
			}
		})
	}
}
