package skills

import (
	"bytes"
	"io/fs"
	"strings"
	"testing"
)

func TestCanonicalSkillsOnlyBundleIsCompleteAndRuntimeNeutral(t *testing.T) {
	manifest, err := VerifiedManifest()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"engram-evolve", "engram-find", "engram-maintain", "engram-write", "using-engram"}
	for index, entry := range manifest.Skills {
		if entry.Name != want[index] {
			t.Fatalf("skill %d = %#v", index, entry)
		}
		data, err := fs.ReadFile(FS(), entry.Path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.HasPrefix(data, []byte("---\nname: "+entry.Name+"\ndescription: ")) || !bytes.Contains(data, []byte("core specification as the sole authority")) {
			t.Fatalf("skill %s lacks canonical frontmatter or authority boundary", entry.Name)
		}
		for _, forbidden := range []string{".claude/", ".agents/", ".codex/", "mcp__", "engram commit", "engram pull", "engram push"} {
			if strings.Contains(string(data), forbidden) {
				t.Errorf("skill %s contains runtime/CLI-specific binding %q", entry.Name, forbidden)
			}
		}
	}
}

func TestBundleContainsNoUnmanifestedExecutable(t *testing.T) {
	err := fs.WalkDir(FS(), ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || name == "manifest-v1.json" || strings.HasSuffix(name, "/SKILL.md") {
			return nil
		}
		t.Fatalf("unexpected skills-only payload %q", name)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestSkillArtifactAndManifestRejectMalformedMetadata(t *testing.T) {
	valid := []byte("---\nname: example-skill\ndescription: Useful example.\n---\n\n# Example\n")
	if err := validateSkillArtifact(valid, "example-skill"); err != nil {
		t.Fatalf("valid skill rejected: %v", err)
	}
	for _, test := range []struct {
		name string
		data []byte
	}{
		{name: "directory mismatch", data: bytes.Replace(valid, []byte("example-skill"), []byte("other-skill"), 1)},
		{name: "duplicate YAML field", data: bytes.Replace(valid, []byte("description:"), []byte("name: duplicate\ndescription:"), 1)},
		{name: "unknown field", data: bytes.Replace(valid, []byte("description:"), []byte("license: MIT\ndescription:"), 1)},
		{name: "missing closing delimiter", data: bytes.Replace(valid, []byte("\n---\n"), []byte("\n"), 1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := validateSkillArtifact(test.data, "example-skill"); err == nil {
				t.Fatal("malformed canonical skill accepted")
			}
		})
	}
	if err := rejectDuplicateJSONFields([]byte(`{"version":1,"version":1}`)); err == nil {
		t.Fatal("duplicate manifest field accepted")
	}
}
