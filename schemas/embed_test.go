package schemas

import (
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
