package replay

import (
	"reflect"
	"testing"

	"github.com/ontopix/engram/internal/changeset"
)

func TestApplySourceOperationsAtomically(t *testing.T) {
	t.Parallel()
	original := Files{"modify.md": []byte("old"), "delete.md": []byte("gone"), "same.md": []byte("same")}
	next := Files{"modify.md": []byte("new"), "add.md": []byte("added"), "same.md": []byte("same")}
	current := Files{"modify.md": []byte("old"), "delete.md": []byte("gone"), "same.md": []byte("same"), "local.md": []byte("local")}
	result := Apply(original, next, current)
	if len(result.Conflicts) != 0 || result.Satisfied {
		t.Fatalf("result = %#v", result)
	}
	wantFiles := Files{"modify.md": []byte("new"), "add.md": []byte("added"), "same.md": []byte("same"), "local.md": []byte("local")}
	if !reflect.DeepEqual(result.Files, wantFiles) {
		t.Fatalf("files = %#v, want %#v", result.Files, wantFiles)
	}
	wantChanges := []changeset.Change{
		{Operation: changeset.Added, Path: "add.md"},
		{Operation: changeset.Deleted, Path: "delete.md"},
		{Operation: changeset.Modified, Path: "modify.md"},
	}
	if !reflect.DeepEqual(result.Changes, wantChanges) {
		t.Fatalf("changes = %#v, want %#v", result.Changes, wantChanges)
	}
}

func TestApplyRecognizesAlreadySatisfiedValues(t *testing.T) {
	t.Parallel()
	original := Files{"modified.md": []byte("old"), "deleted.md": []byte("old")}
	next := Files{"modified.md": []byte("new"), "added.md": []byte("new")}
	current := Files{"modified.md": []byte("new"), "added.md": []byte("new")}
	result := Apply(original, next, current)
	if !result.Satisfied || len(result.Changes) != 0 || len(result.Conflicts) != 0 {
		t.Fatalf("result = %#v", result)
	}
}

func TestApplyReportsUnionAndPublishesNothingOnConflict(t *testing.T) {
	t.Parallel()
	original := Files{"b.md": []byte("old"), "c.md": []byte("old")}
	next := Files{"a.md": []byte("new"), "b.md": []byte("new")}
	current := Files{"a.md": []byte("different"), "b.md": []byte("different"), "c.md": []byte("different")}
	result := Apply(original, next, current)
	want := []string{"a.md", "b.md", "c.md"}
	if !reflect.DeepEqual(result.Conflicts, want) {
		t.Fatalf("conflicts = %v, want %v", result.Conflicts, want)
	}
	if !reflect.DeepEqual(result.Files, current) || result.Changes != nil || result.Satisfied {
		t.Fatalf("conflicting result = %#v", result)
	}
	result.Files["a.md"][0] = 'X'
	if string(current["a.md"]) != "different" {
		t.Fatal("result aliases caller bytes")
	}
}

func TestApplyRejectsFileDirectoryCollisionAsAWhole(t *testing.T) {
	t.Parallel()
	original := Files{}
	next := Files{"topic": []byte("file")}
	current := Files{"topic/note.md": []byte("record")}
	result := Apply(original, next, current)
	want := []string{"topic", "topic/note.md"}
	if !reflect.DeepEqual(result.Conflicts, want) {
		t.Fatalf("conflicts = %v, want %v", result.Conflicts, want)
	}
	if _, exists := result.Files["topic"]; exists {
		t.Fatal("conflicting addition was partially applied")
	}
}

func TestInputsAreNotAliased(t *testing.T) {
	t.Parallel()
	original := Files{"a": []byte("a")}
	next := Files{"a": []byte("b")}
	current := Files{"a": []byte("a")}
	result := Apply(original, next, current)
	result.Files["a"][0] = 'x'
	if string(original["a"]) != "a" || string(next["a"]) != "b" || string(current["a"]) != "a" {
		t.Fatal("returned files alias an input")
	}
}
