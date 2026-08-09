package bootstrap

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestBuildAbsentTargetIsValidAndReadOnly(t *testing.T) {
	target := filepath.Join(t.TempDir(), "memory")
	plan, err := Build(context.Background(), target, []string{"note", "note"})
	if err != nil {
		t.Fatal(err)
	}
	canonicalParent, err := filepath.EvalSymlinks(filepath.Dir(target))
	if err != nil {
		t.Fatal(err)
	}
	wantRoot := filepath.Join(canonicalParent, filepath.Base(target))
	if plan.RootExists || plan.Root != wantRoot || plan.Validation.HasErrors() || len(plan.Files) != 3 || len(plan.Changes) != 3 {
		t.Fatalf("plan = %#v, validation = %#v", plan, plan.Validation)
	}
	for _, name := range []string{"README.md", ".engram/root.yaml", ".engram/schemas/note.md"} {
		if len(plan.Files[name]) == 0 {
			t.Errorf("missing bootstrap %s", name)
		}
	}
	if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("planning mutated target: %v", err)
	}
}

func TestBuildExistingSnapshotPreservesBytesAndAddsRequestedSchema(t *testing.T) {
	root := t.TempDir()
	minimal := filepath.Join(repositoryRoot(t), "examples", "minimal")
	if err := os.CopyFS(root, os.DirFS(minimal)); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := Build(context.Background(), root, []string{"person", "person"})
	if err != nil {
		t.Fatal(err)
	}
	if !plan.RootExists || plan.Validation.HasErrors() || len(plan.Files) != 1 || len(plan.Files[".engram/schemas/person.md"]) == 0 {
		t.Fatalf("plan = %#v, validation = %#v", plan, plan.Validation)
	}
	after, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil || string(after) != string(before) {
		t.Fatalf("existing bytes changed: %v", err)
	}
	if _, exists := plan.Candidate.Tree.Files["topics/why-files.md"]; !exists {
		t.Fatal("existing logical input omitted from candidate")
	}
}

func TestBuildReportsInvalidPreservedCandidateWithoutMutation(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("broken"), 0o644); err != nil {
		t.Fatal(err)
	}
	plan, err := Build(context.Background(), root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Validation.HasErrors() {
		t.Fatalf("invalid preserved README passed: %#v", plan.Validation)
	}
	data, _ := os.ReadFile(filepath.Join(root, "README.md"))
	if string(data) != "broken" {
		t.Fatal("planning rewrote existing README")
	}
}

func TestBuildRejectsRequestedSchemaConflictAndGitOwner(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".engram", "schemas"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".engram", "schemas", "person.md"), []byte("different\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Build(context.Background(), root, []string{"person"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("schema conflict error = %v", err)
	}
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Build(context.Background(), root, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("Git owner error = %v", err)
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	working, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Clean(filepath.Join(working, "..", ".."))
}
