package schemas

import (
	"bytes"
	"os"
	"reflect"
	"sort"
	"testing"
)

func TestInventoryIsCompleteSortedAndByteExact(t *testing.T) {
	t.Parallel()
	entries, err := Inventory()
	if err != nil {
		t.Fatal(err)
	}
	gotTypes := make([]string, len(entries))
	for index, entry := range entries {
		gotTypes[index] = entry.Type
		data, err := os.ReadFile(entry.Type + ".md")
		if err != nil {
			t.Fatal(err)
		}
		if entry.Content != string(data) {
			t.Errorf("embedded %s bytes differ from source", entry.Type)
		}
	}
	wantTypes := []string{"fact", "journal-entry", "note", "person", "project"}
	sort.Strings(wantTypes)
	if !reflect.DeepEqual(gotTypes, wantTypes) {
		t.Fatalf("types = %#v, want %#v", gotTypes, wantTypes)
	}
}

func TestNoteSchemaIsByteIdenticalToNormativeAppendix(t *testing.T) {
	t.Parallel()
	specification, err := os.ReadFile("../docs/spec/README.md")
	if err != nil {
		t.Fatal(err)
	}
	heading := []byte("### A.3 `.engram/schemas/note.md`")
	if bytes.Count(specification, heading) != 1 {
		t.Fatal("core specification must contain exactly one Appendix A.3 note-schema heading")
	}
	section := specification[bytes.Index(specification, heading)+len(heading):]
	opening := []byte("````markdown\n")
	start := bytes.Index(section, opening)
	if start < 0 {
		t.Fatal("Appendix A.3 has no markdown payload fence")
	}
	payload := section[start+len(opening):]
	closing := []byte("\n````\n")
	end := bytes.Index(payload, closing)
	if end < 0 {
		t.Fatal("Appendix A.3 markdown payload fence is not closed")
	}
	want := payload[:end+1]
	got, err := os.ReadFile("note.md")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("schemas/note.md differs byte-for-byte from core Appendix A.3")
	}
}
