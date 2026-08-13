package changeset

import (
	"reflect"
	"testing"

	"github.com/ontopix/engram/internal/snapshot"
)

func TestDiffIsByteExactAndOrdered(t *testing.T) {
	t.Parallel()
	base := &snapshot.Tree{Files: map[string]snapshot.File{
		"z.md":                      {Path: "z.md", Data: []byte("same")},
		"b.md":                      {Path: "b.md", Data: []byte("old")},
		"d.md":                      {Path: "d.md", Data: []byte("gone")},
		".engram/routines/daily.md": {Path: ".engram/routines/daily.md", Role: snapshot.RoleRoutine, Data: []byte("old routine")},
	}}
	candidate := &snapshot.Tree{Files: map[string]snapshot.File{
		"z.md":                      {Path: "z.md", Data: []byte("same")},
		"b.md":                      {Path: "b.md", Data: []byte("new")},
		"a.md":                      {Path: "a.md", Data: []byte("added")},
		".engram/routines/daily.md": {Path: ".engram/routines/daily.md", Role: snapshot.RoleRoutine, Data: []byte("new routine")},
	}}
	want := []Change{
		{Operation: Modified, Path: ".engram/routines/daily.md"},
		{Operation: Added, Path: "a.md"},
		{Operation: Modified, Path: "b.md"},
		{Operation: Deleted, Path: "d.md"},
	}
	if got := Diff(base, candidate); !reflect.DeepEqual(got, want) {
		t.Fatalf("Diff = %#v, want %#v", got, want)
	}
}

func TestPreflight(t *testing.T) {
	t.Parallel()
	if !PreflightOK(&snapshot.Tree{}) {
		t.Fatal("empty tree should pass preflight")
	}
	if !PreflightOK(&snapshot.Tree{Issues: []snapshot.Issue{{Code: "E102", Path: "nested/.engram/root.yaml"}}}) {
		t.Fatal("repairable nested-root finding should not fail preflight")
	}
	if PreflightOK(&snapshot.Tree{Issues: []snapshot.Issue{{Code: "E107", Path: "."}}}) {
		t.Fatal("boundary issue should fail preflight")
	}
	if PreflightOK(&snapshot.Tree{Issues: []snapshot.Issue{{Code: "E309", Path: ".engram/routines"}}}) {
		t.Fatal("routine-tree layout issue should fail preflight")
	}
}
