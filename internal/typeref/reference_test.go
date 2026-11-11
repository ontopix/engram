package typeref

import "testing"

func TestExactReferences(t *testing.T) {
	for _, value := range []string{"note@v1", "journal-entry@v12", "x@v999999999999999999999999999999999999"} {
		r, ok := Parse(value)
		if !ok || r.String() != value {
			t.Fatalf("reference %q: %+v %v", value, r, ok)
		}
	}
	for _, value := range []string{"note", "note@latest", "note@v01", "note@v0", "note@v-1", "note@v1.0", "note@v1@v2", "../note@v1", "NOTE@v1", "note@V1", "note@v١"} {
		if Valid(value) {
			t.Errorf("accepted %q", value)
		}
	}
	if CompareVersions("2", "10") >= 0 || CompareVersions("99999999999999999999999", "100000000000000000000000") >= 0 {
		t.Fatal("numeric ordering")
	}
	if !Matches("note", "note@v2") || !Matches("note@v2", "note@v2") || Matches("note@v1", "note@v2") || Matches("note", "note-other@v2") {
		t.Fatal("link matching")
	}
}
