package checker

import (
	"path/filepath"
	"testing"
)

func TestSchemaQueries(t *testing.T) {
	t.Parallel()
	checked, err := CheckFS(filepath.Join(repositoryRoot(t), "examples", "minimal"))
	if err != nil {
		t.Fatal(err)
	}
	visible, err := checked.VisibleSchemas("topics")
	if err != nil {
		t.Fatal(err)
	}
	if len(visible) != 1 || visible[0].Type != "note" || visible[0].Path == nil || *visible[0].Path != ".engram/schemas/note.md" {
		t.Fatalf("visible = %#v", visible)
	}
	description, content, err := checked.ShowSchema(".", "note")
	if err != nil {
		t.Fatal(err)
	}
	if description.Type != "note" || content == "" || content[len(content)-1] != '\n' {
		t.Fatalf("show = %#v, content length %d", description, len(content))
	}
}
