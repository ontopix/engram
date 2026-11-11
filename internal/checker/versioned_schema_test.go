package checker

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func versionStore(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.CopyFS(root, os.DirFS(filepath.Join(repositoryRoot(t), "examples", "minimal"))); err != nil {
		t.Fatal(err)
	}
	versionWrite(t, root, "README.md", "---\ndescription: Versioned test store.\ncatalog: none\n---\n# Store\n")
	return root
}

func versionWrite(t *testing.T, root, name, data string) {
	t.Helper()
	filename := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(filename), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func versionSchema(version, policy string) string {
	return fmt.Sprintf("---\ntype: entry\nversion: %s\ndescription: Entry revision %s.\nschema:\n  type: object\n  required: [type, description, value]\n  maxProperties: 3\n  additionalProperties: false\n  properties:\n    type: {const: entry@v%s}\n    description: {type: string}\n    value: {type: integer}\n%s---\n# Entry\n", version, version, version, policy)
}

func versionRecord(ref, description string) string {
	return fmt.Sprintf("---\ntype: %s\ndescription: %s\nvalue: 1\n---\n", ref, description)
}

func versionCheck(t *testing.T, root string) *Snapshot {
	t.Helper()
	s, err := CheckFS(root)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func versionFinding(result Result, code, path string) bool {
	for _, f := range result.Findings {
		if f.Code == code && f.Path == path {
			return true
		}
	}
	return false
}

func TestVersionCoexistenceExactResolutionAndNumericInventory(t *testing.T) {
	root := versionStore(t)
	revisions := []string{"1", "2", "10", "999999999999999999999999999999"}
	for _, v := range revisions {
		versionWrite(t, root, ".engram/schemas/entry@v"+v+".md", versionSchema(v, ""))
		versionWrite(t, root, "entry-"+v+".md", versionRecord("entry@v"+v, "Entry "+v+"."))
	}
	checked := versionCheck(t, root)
	if checked.Validation.HasErrors() {
		t.Fatal(checked.Validation)
	}
	visible, err := checked.VisibleSchemas(".")
	if err != nil {
		t.Fatal(err)
	}
	for i, v := range revisions {
		if visible[i].Reference != "entry@v"+v || visible[i].Version.String() != v {
			t.Fatalf("inventory: %#v", visible)
		}
	}
	for _, v := range revisions {
		d, data, err := checked.ShowSchema(".", "entry@v"+v)
		if err != nil || data != versionSchema(v, "") || d.Type != "entry" {
			t.Fatalf("show %s: %#v %v", v, d, err)
		}
	}
	if _, _, err := checked.ShowSchema(".", "entry"); err == nil {
		t.Fatal("bare schema reference accepted")
	}
	versionWrite(t, root, "missing.md", versionRecord("entry@v99", "Unavailable."))
	versionWrite(t, root, "bare.md", versionRecord("entry", "Bare."))
	versionWrite(t, root, "bad-v2.md", strings.Replace(versionRecord("entry@v2", "Missing value."), "value: 1\n", "", 1))
	result := versionCheck(t, root).Validation
	for _, want := range [][2]string{{"E310", "missing.md"}, {"E203", "bare.md"}, {"E301", "bad-v2.md"}} {
		if !versionFinding(result, want[0], want[1]) {
			t.Fatalf("missing %v: %#v", want, result)
		}
	}
}

func TestVersionFamilyShadowingDoesNotFallBack(t *testing.T) {
	root := versionStore(t)
	versionWrite(t, root, ".engram/schemas/entry@v1.md", versionSchema("1", ""))
	versionWrite(t, root, "area/README.md", "---\ndescription: Nested area.\ncatalog: none\n---\n")
	versionWrite(t, root, "area/.engram/schemas/entry@v2.md", versionSchema("2", ""))
	versionWrite(t, root, "area/old.md", versionRecord("entry@v1", "Nested old."))
	result := versionCheck(t, root).Validation
	if !versionFinding(result, "E304", "area/.engram/schemas/entry@v2.md") || !versionFinding(result, "E310", "area/old.md") {
		t.Fatal(result)
	}
}

func TestVersionPublicationAndBasePolicies(t *testing.T) {
	for _, policy := range []string{"", "policy:\n  append-only: true\n", "policy:\n  immutable: true\n"} {
		t.Run(policy, func(t *testing.T) {
			root := versionStore(t)
			one := ".engram/schemas/entry@v1.md"
			two := ".engram/schemas/entry@v2.md"
			versionWrite(t, root, one, versionSchema("1", policy))
			versionWrite(t, root, "old.md", versionRecord("entry@v1", "Old entry."))
			base := versionCheck(t, root)
			versionWrite(t, root, two, versionSchema("2", ""))
			versionWrite(t, root, "new.md", versionRecord("entry@v2", "New entry."))
			candidate := versionCheck(t, root)
			result, _ := CheckTransition(base, candidate, false)
			if result.HasErrors() || result.Status != StatusComplete {
				t.Fatal(result)
			}
			// Selecting an unprotected revision never waives the base policy.
			versionWrite(t, root, "old.md", versionRecord("entry@v2", "Old entry."))
			migrated := versionCheck(t, root)
			result, _ = CheckTransition(candidate, migrated, false)
			code := ""
			if strings.Contains(policy, "append-only") {
				code = "E502"
			}
			if strings.Contains(policy, "immutable") {
				code = "E501"
			}
			if code == "" && result.HasErrors() || code != "" && !versionFinding(result, code, "old.md") {
				t.Fatal(result)
			}
			versionWrite(t, root, one, versionSchema("1", "policy:\n  append-only: false\n"))
			changed := versionCheck(t, root)
			result, _ = CheckTransition(candidate, changed, false)
			if !versionFinding(result, "E504", one) {
				t.Fatal(result)
			}
			if err := os.Remove(filepath.Join(root, one)); err != nil {
				t.Fatal(err)
			}
			result, _ = CheckTransition(candidate, versionCheck(t, root), false)
			if !versionFinding(result, "E505", one) {
				t.Fatal(result)
			}
		})
	}
	root := versionStore(t)
	versionWrite(t, root, ".engram/schemas/entry@v10.md", versionSchema("10", ""))
	base := versionCheck(t, root)
	versionWrite(t, root, ".engram/schemas/entry@v2.md", versionSchema("2", ""))
	candidate := versionCheck(t, root)
	result, _ := CheckTransition(base, candidate, false)
	if !versionFinding(result, "E506", ".engram/schemas/entry@v2.md") {
		t.Fatal(result)
	}
	initialized, _ := CheckTransition(nil, candidate, true)
	if initialized.HasErrors() {
		t.Fatal(initialized)
	}
}

func TestVersionedTypedLinks(t *testing.T) {
	root := versionStore(t)
	for _, v := range []string{"1", "2"} {
		versionWrite(t, root, ".engram/schemas/entry@v"+v+".md", versionSchema(v, ""))
		versionWrite(t, root, "entry-"+v+".md", versionRecord("entry@v"+v, "Entry "+v+"."))
	}
	schema := "---\ntype: relation\nversion: 1\ndescription: Relation.\nschema:\n  type: object\n  properties:\n    type: {const: relation@v1}\n    target:\n      type: string\n      x-engram-link:\n        types: [%s]\n---\n"
	versionWrite(t, root, "relation.md", "---\ntype: relation@v1\ndescription: Relation to entry.\ntarget: '[[entry-2]]'\n---\n")
	for _, tc := range []struct {
		constraint string
		rejected   bool
	}{{"entry", false}, {"entry@v2", false}, {"entry@v1, entry@v2", false}, {"entry@v1", true}} {
		versionWrite(t, root, ".engram/schemas/relation@v1.md", fmt.Sprintf(schema, tc.constraint))
		result := versionCheck(t, root).Validation
		if versionFinding(result, "E402", "relation.md") != tc.rejected || !tc.rejected && result.HasErrors() {
			t.Fatalf("%s: %#v", tc.constraint, result)
		}
	}
}

func TestVersionDefinitionIdentityAndEditorialChanges(t *testing.T) {
	root := versionStore(t)
	name := ".engram/schemas/entry@v1.md"
	original := versionSchema("1", "")
	versionWrite(t, root, name, original)
	base := versionCheck(t, root)
	for _, invalid := range []string{
		strings.Replace(original, "type: entry\n", "type: another\n", 1),
		strings.Replace(original, "version: 1\n", "version: 2\n", 1),
		strings.Replace(original, "const: entry@v1", "const: entry@v2", 1),
		strings.Replace(original, "type: {const: entry@v1}", "type: {type: string}", 1),
	} {
		versionWrite(t, root, name, invalid)
		if result := versionCheck(t, root).Validation; !versionFinding(result, "E303", name) {
			t.Fatal(result)
		}
	}
	versionWrite(t, root, name, strings.Replace(original, "Entry revision 1.", "Clearer schema documentation.", 1)+"\nEditorial explanation.\n")
	result, _ := CheckTransition(base, versionCheck(t, root), false)
	if result.Status != StatusComplete || result.HasErrors() {
		t.Fatal(result)
	}
	// Every note revision retains the low-ceremony floor, not just note@v1.
	note, err := os.ReadFile(filepath.Join(root, ".engram/schemas/note@v1.md"))
	if err != nil {
		t.Fatal(err)
	}
	second := strings.ReplaceAll(strings.Replace(string(note), "version: 1", "version: 2", 1), "note@v1", "note@v2")
	second = strings.Replace(second, "maxLength: 200", "maxLength: 100", 1)
	versionWrite(t, root, ".engram/schemas/note@v2.md", second)
	if result := versionCheck(t, root).Validation; !versionFinding(result, "E307", ".engram/schemas/note@v2.md") {
		t.Fatal(result)
	}
}
