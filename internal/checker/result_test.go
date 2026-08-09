package checker

import (
	"encoding/json"
	"reflect"
	"testing"
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
