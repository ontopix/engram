package harness

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	canonicalskills "github.com/ontopix/engram/skills"
)

func TestSetupInstallsAndReconcilesSupportedHarnesses(t *testing.T) {
	for _, harnessName := range []string{"codex", "claude-code"} {
		t.Run(harnessName, func(t *testing.T) {
			project := t.TempDir()
			profile, err := Resolve(harnessName)
			if err != nil {
				t.Fatal(err)
			}
			entrypoint := filepath.Join(project, profile.Entrypoint)
			if err := os.WriteFile(entrypoint, []byte("before\n"), 0o640); err != nil {
				t.Fatal(err)
			}

			result, err := Setup(project, harnessName, "", false)
			if err != nil || !result.Changed || result.DryRun || len(result.Files) != 7 {
				t.Fatalf("result=%#v error=%v", result, err)
			}
			entrypointData, err := os.ReadFile(entrypoint)
			if err != nil || !bytes.HasPrefix(entrypointData, []byte("before\n\n")) || bytes.Count(entrypointData, []byte(openMarker)) != 1 {
				t.Fatalf("entrypoint=%q error=%v", entrypointData, err)
			}
			manifest, err := canonicalskills.VerifiedManifest()
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range manifest.Skills {
				want, _ := fs.ReadFile(canonicalskills.FS(), entry.Path)
				got, readErr := os.ReadFile(filepath.Join(project, profile.SkillsDir, entry.Path))
				if readErr != nil || !bytes.Equal(got, want) {
					t.Fatalf("skill %s differs: %v", entry.Name, readErr)
				}
			}

			unchanged, err := Setup(project, harnessName, "", false)
			if err != nil || unchanged.Changed || len(unchanged.Files) != 0 {
				t.Fatalf("idempotent result=%#v error=%v", unchanged, err)
			}
		})
	}
}

func TestSetupDryRunDoesNotWrite(t *testing.T) {
	project := t.TempDir()
	result, err := Setup(project, "codex", "", true)
	if err != nil || !result.Changed || !result.DryRun || len(result.Files) != 7 {
		t.Fatalf("result=%#v error=%v", result, err)
	}
	if _, err := os.Lstat(filepath.Join(project, ".agents")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dry-run created skills parent: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(project, "AGENTS.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dry-run created entrypoint: %v", err)
	}
}

func TestSetupPointsToSelectedMemoryManifest(t *testing.T) {
	project := t.TempDir()
	configuration := filepath.Join(project, "config")
	if err := os.Mkdir(configuration, 0o755); err != nil {
		t.Fatal(err)
	}
	memoryFile := filepath.Join(configuration, "MEMORIES.md")
	result, err := Setup(project, "codex", memoryFile, false)
	canonicalProject, canonicalErr := filepath.EvalSymlinks(project)
	wantMemoryFile := filepath.Join(canonicalProject, "config", "MEMORIES.md")
	if err != nil || canonicalErr != nil || result.MemoryFile != wantMemoryFile {
		t.Fatalf("result=%#v error=%v", result, err)
	}
	entrypoint, err := os.ReadFile(filepath.Join(project, "AGENTS.md"))
	if err != nil || !bytes.Contains(entrypoint, []byte("`config/MEMORIES.md`")) {
		t.Fatalf("entrypoint=%q error=%v", entrypoint, err)
	}
}

func TestSetupRejectsMemoryManifestOverlappingEntrypoint(t *testing.T) {
	project := t.TempDir()
	if _, err := Setup(project, "codex", filepath.Join(project, "AGENTS.md"), true); !errors.Is(err, ErrConflict) {
		t.Fatalf("error=%v, want conflict", err)
	}
}

func TestSetupProtectsLocallyModifiedSkill(t *testing.T) {
	project := t.TempDir()
	if _, err := Setup(project, "codex", "", false); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(project, ".agents", "skills", "using-engram", "SKILL.md")
	modified := []byte("locally modified\n")
	if err := os.WriteFile(target, modified, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Setup(project, "codex", "", false); !errors.Is(err, ErrConflict) {
		t.Fatalf("error=%v, want conflict", err)
	}
	got, err := os.ReadFile(target)
	if err != nil || !bytes.Equal(got, modified) {
		t.Fatalf("modified skill was overwritten: %q %v", got, err)
	}
}

func TestSetupUpdatesSkillOwnedByPreviousManifest(t *testing.T) {
	project := t.TempDir()
	skillsDir := filepath.Join(project, ".agents", "skills")
	target := filepath.Join(skillsDir, "using-engram", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	old := []byte("old canonical bytes\n")
	if err := os.WriteFile(target, old, 0o644); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(old)
	manifest := canonicalskills.Manifest{Version: 1, Format: "agentskills.io", Skills: []canonicalskills.Entry{{
		Name: "using-engram", Path: "using-engram/SKILL.md", SHA256: hex.EncodeToString(digest[:]),
	}}}
	manifestData, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillsDir, manifestFile), manifestData, 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := Setup(project, "codex", "", false)
	if err != nil || !result.Changed {
		t.Fatalf("result=%#v error=%v", result, err)
	}
	want, _ := fs.ReadFile(canonicalskills.FS(), "using-engram/SKILL.md")
	got, err := os.ReadFile(target)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("owned skill was not updated: %v", err)
	}
}

func TestSetupRejectsMalformedEntrypointWithoutWritingSkills(t *testing.T) {
	project := t.TempDir()
	entrypoint := filepath.Join(project, "AGENTS.md")
	original := []byte(openMarker + "\nbroken\n")
	if err := os.WriteFile(entrypoint, original, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Setup(project, "codex", "", false); !errors.Is(err, ErrConflict) {
		t.Fatalf("error=%v, want conflict", err)
	}
	if _, err := os.Lstat(filepath.Join(project, ".agents")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("preflight failure wrote skills: %v", err)
	}
}

func TestSetupRejectsSymlinkedSkillsParent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symbolic-link setup requires platform privileges")
	}
	project := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(project, ".agents")); err != nil {
		t.Fatal(err)
	}
	if _, err := Setup(project, "codex", "", false); !errors.Is(err, ErrConflict) {
		t.Fatalf("error=%v, want conflict", err)
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatalf("symlink target changed: %#v %v", entries, err)
	}
}

func TestSetupMigratesLegacyAttachmentBlock(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	store := filepath.Join(root, "memory")
	for _, directory := range []string{project, store} {
		if err := os.Mkdir(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	legacy := legacyOpenMarker + "\n" + legacyIntroduction + "\n```json\n" +
		`{"stores":[` + quotedJSON(t, store) + `]}` + "\n```\n" + legacyGuidance + "\n" + legacyCloseMarker + "\n"
	entrypoint := filepath.Join(project, "AGENTS.md")
	if err := os.WriteFile(entrypoint, []byte("before\n\n"+legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := Setup(project, "codex", "", false)
	if err != nil || !result.Changed {
		t.Fatalf("result=%#v error=%v", result, err)
	}
	entrypointData, err := os.ReadFile(entrypoint)
	if err != nil || bytes.Contains(entrypointData, []byte(legacyOpenMarker)) || !bytes.Contains(entrypointData, []byte(openMarker)) {
		t.Fatalf("entrypoint=%q error=%v", entrypointData, err)
	}
	memoryData, err := os.ReadFile(filepath.Join(project, "MEMORY.md"))
	if err != nil || !bytes.Contains(memoryData, []byte(`"path": "../memory"`)) {
		t.Fatalf("memory manifest=%q error=%v", memoryData, err)
	}
}

func quotedJSON(t *testing.T, value string) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
