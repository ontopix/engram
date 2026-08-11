package checker

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/ontopix/engram/internal/snapshot"
)

func TestFindingIdentityOrderingAndErrorClass(t *testing.T) {
	t.Parallel()
	set := findingSet{}
	set.add("W901", "z.md", "warning")
	set.add("E401", "a.md", "first")
	set.add("E401", "a.md", "ignored duplicate detail")
	set.add("E301", "a.md", "schema")
	want := []Finding{
		{Code: "E301", Path: "a.md", Detail: "schema"},
		{Code: "E401", Path: "a.md", Detail: "first"},
		{Code: "W901", Path: "z.md", Detail: "warning"},
	}
	got := set.sorted()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("findings = %#v, want %#v", got, want)
	}
	if !(Result{Findings: got}).HasErrors() {
		t.Fatal("E findings must report errors")
	}
	if (Result{Findings: got[2:]}).HasErrors() {
		t.Fatal("warnings alone must not report errors")
	}
}

func TestEmptyValidationFindingsEncodeAsArray(t *testing.T) {
	result, _ := CheckTransition(nil, nil, false)
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != `{"target":"changeset","status":"indeterminate","findings":[]}` {
		t.Fatalf("encoded result = %s", encoded)
	}
}

func TestUnavailableCandidateStillReportsIndependentBasePreflightFinding(t *testing.T) {
	t.Parallel()
	base := &Snapshot{Tree: &snapshot.Tree{Issues: []snapshot.Issue{{Code: "E103", Path: "escape"}}}}
	result, _ := CheckTransition(base, nil, false)
	want := []Finding{{Code: "E103", Path: "escape"}}
	if result.Status != StatusIndeterminate || !reflect.DeepEqual(result.Findings, want) {
		t.Fatalf("result = %#v, want indeterminate with %#v", result, want)
	}
}
