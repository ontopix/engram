package conformance

import (
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
)

func TestLoadRepositoryManifest(t *testing.T) {
	t.Parallel()
	repository := repositoryRoot(t)
	manifest, err := Load(filepath.Join(repository, "testdata", "conformance", "cases.json"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := manifest.ValidateReferences(repository); err != nil {
		t.Fatalf("ValidateReferences: %v", err)
	}

	ids := manifest.SortedCaseIDs()
	if len(ids) != 43 || !sort.StringsAreSorted(ids) {
		t.Fatalf("case IDs = %q, want 43 sorted IDs", ids)
	}

	var snapshots, changesets, managed int
	for i := range manifest.Cases {
		switch manifest.Cases[i].Kind {
		case KindSnapshot:
			snapshots++
		case KindChangeset:
			changesets++
		case KindManaged:
			managed++
		}
	}
	if snapshots != 35 || changesets != 5 || managed != 3 {
		t.Fatalf("kind counts = snapshot:%d changeset:%d managed:%d, want 35, 5, and 3", snapshots, changesets, managed)
	}
}

func TestManifestCoversEveryEmittableFindingAndRetiredW902(t *testing.T) {
	t.Parallel()
	repository := repositoryRoot(t)
	manifest, err := Load(filepath.Join(repository, "testdata", "conformance", "cases.json"))
	if err != nil {
		t.Fatal(err)
	}

	want := []string{
		"E101", "E102", "E103", "E104", "E105", "E106", "E107", "E108", "E109", "E110",
		"E201", "E202", "E203", "E204", "E205", "E206", "E207", "E208", "E209",
		"E301", "E302", "E303", "E304", "E305", "E306", "E307", "E308",
		"E401", "E402", "E403", "E404", "E405",
		"E501", "E502", "E503", "E504",
		"E601", "E602", "E603",
		"W901", "W903", "W904",
	}
	covered := make(map[string]struct{}, len(want))
	retiredCases := 0
	for _, fixture := range manifest.Cases {
		for _, finding := range fixture.Expected.Findings {
			covered[finding.Code] = struct{}{}
			if finding.Code == "W902" {
				t.Fatalf("case %q expects retired W902 to be emitted", fixture.ID)
			}
		}
		for _, code := range fixture.Expected.NotFindings {
			if code == "W902" {
				retiredCases++
			}
		}
	}
	got := make([]string, 0, len(covered))
	for code := range covered {
		got = append(got, code)
	}
	sort.Strings(got)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("manifest finding coverage:\n got: %q\nwant: %q", got, want)
	}
	if retiredCases != 1 {
		t.Fatalf("W902 non-emission cases = %d, want exactly 1", retiredCases)
	}
}

func TestParseRejectsInvalidManifest(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		json string
		want string
	}{
		{"syntax", `{`, "invalid JSON"},
		{"duplicate field", `{"version":1,"version":1,"seed":"seed","common":[],"cases":[]}`, "duplicate JSON field"},
		{"unknown root field", `{"version":1,"seed":"seed","common":[],"cases":[],"extra":true}`, "unknown field"},
		{"unsupported version", strings.Replace(validSnapshotManifest, `"version":1`, `"version":2`, 1), "unsupported manifest version"},
		{"unsafe seed", strings.Replace(validSnapshotManifest, `"seed":"seed"`, `"seed":"../seed"`, 1), "unsafe path segment"},
		{"common null", strings.Replace(validSnapshotManifest, `"common":[]`, `"common":null`, 1), "common must be an array"},
		{"empty cases", strings.Replace(validSnapshotManifest, `"cases":[`+validSnapshotCase+`]`, `"cases":[]`, 1), "cases must be a non-empty array"},
		{"duplicate id", strings.Replace(validSnapshotManifest, `"cases":[`+validSnapshotCase+`]`, `"cases":[`+validSnapshotCase+`,`+validSnapshotCase+`]`, 1), "is duplicated"},
		{"unknown kind", strings.Replace(validSnapshotManifest, `"kind":"snapshot"`, `"kind":"other"`, 1), ".kind"},
		{"snapshot base field", strings.Replace(validSnapshotManifest, `"snapshot":{"operations":[]}`, `"snapshot":{"operations":[]},"base":{"operations":[]}`, 1), "must not contain base, candidate, or managed"},
		{"snapshot null base field", strings.Replace(validSnapshotManifest, `"snapshot":{"operations":[]}`, `"snapshot":{"operations":[]},"base":null`, 1), "base must not be null"},
		{"snapshot status", strings.Replace(validSnapshotManifest, `"findings":[]`, `"status":"complete","findings":[]`, 1), "status is not allowed"},
		{"snapshot null status", strings.Replace(validSnapshotManifest, `"findings":[]`, `"status":null,"findings":[]`, 1), "status must not be null"},
		{"managed missing state", strings.Replace(validManagedManifest, `,"managed":{"operations":[],"scenario":"merge-tip"}`, ``, 1), ".managed is required"},
		{"managed unknown scenario", strings.Replace(validManagedManifest, `"scenario":"merge-tip"`, `"scenario":"unknown"`, 1), ".scenario"},
		{"managed non-complete status", strings.Replace(validManagedManifest, `"status":"complete"`, `"status":"indeterminate"`, 1), "must be \"complete\""},
		{"unknown state field", strings.Replace(validSnapshotManifest, `"operations":[]`, `"operations":[],"extra":true`, 1), "unknown field"},
		{"unknown operation field", manifestWithOperation(`{"operation":"remove","path":"old.md","extra":true}`), "unknown field"},
		{"unknown operation", manifestWithOperation(`{"operation":"copy","path":"new.md"}`), ".operation"},
		{"remove null source", manifestWithOperation(`{"operation":"remove","path":"old.md","source":null}`), "source must not be null"},
		{"write text missing source", manifestWithOperation(`{"operation":"write_text","path":"new.md"}`), "source is required"},
		{"write text content", manifestWithOperation(`{"operation":"write_text","path":"new.md","source":"source.md","content":""}`), "content is not allowed"},
		{"invalid base64", manifestWithOperation(`{"operation":"write_base64","path":"new.md","content":"***"}`), "not strict RFC 4648 base64"},
		{"symlink missing target", manifestWithOperation(`{"operation":"write_symlink","path":"new.md"}`), "target is required"},
		{"symlink unsafe target", manifestWithOperation(`{"operation":"write_symlink","path":"new.md","target":"../outside"}`), "unsafe path segment"},
		{"symlink source", manifestWithOperation(`{"operation":"write_symlink","path":"new.md","target":"target","source":"source.md"}`), "must contain only"},
		{"unsafe operation path", manifestWithOperation(`{"operation":"remove","path":"../old.md"}`), "unsafe path segment"},
		{"unknown expected field", strings.Replace(validSnapshotManifest, `"findings":[]`, `"findings":[],"extra":true`, 1), "unknown field"},
		{"null findings", strings.Replace(validSnapshotManifest, `"findings":[]`, `"findings":null`, 1), "findings must not be null"},
		{"null not findings", strings.Replace(validSnapshotManifest, `"findings":[]`, `"findings":[],"not_findings":null`, 1), "not_findings must not be null"},
		{"invalid finding code", strings.Replace(validSnapshotManifest, `"findings":[]`, `"findings":[{"code":"BAD","path":"."}]`, 1), ".code"},
		{"invalid not finding code", strings.Replace(validSnapshotManifest, `"findings":[]`, `"findings":[],"not_findings":["BAD"]`, 1), "not_findings"},
		{"finding both expected and forbidden", strings.Replace(validSnapshotManifest, `"findings":[]`, `"findings":[{"code":"E101","path":"README.md"}],"not_findings":["E101"]`, 1), "both expected and forbidden"},
		{"duplicate not finding", strings.Replace(validSnapshotManifest, `"findings":[]`, `"findings":[],"not_findings":["W902","W902"]`, 1), "duplicates"},
		{"unordered not findings", strings.Replace(validSnapshotManifest, `"findings":[]`, `"findings":[],"not_findings":["W904","W902"]`, 1), "ordered"},
		{"duplicate finding", strings.Replace(validSnapshotManifest, `"findings":[]`, `"findings":[{"code":"E101","path":"."},{"code":"E101","path":"."}]`, 1), "duplicates"},
		{"unordered findings", strings.Replace(validSnapshotManifest, `"findings":[]`, `"findings":[{"code":"E101","path":"z"},{"code":"E101","path":"a"}]`, 1), "must be ordered"},
		{"invalid base literal", strings.Replace(validChangesetManifest, `"base":"unavailable"`, `"base":"missing"`, 1), "base literal must be"},
		{"changeset missing status", strings.Replace(validChangesetManifest, `"status":"indeterminate",`, ``, 1), "status is required"},
		{"unavailable base complete", strings.Replace(validChangesetManifest, `"status":"indeterminate"`, `"status":"complete"`, 1), "when base is unavailable"},
		{"available base indeterminate", strings.Replace(validChangesetManifest, `"base":"unavailable"`, `"base":{"operations":[]}`, 1), "when base is available"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := Parse([]byte(test.json))
			if err == nil {
				t.Fatal("Parse unexpectedly succeeded")
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Parse error = %q, want substring %q", err, test.want)
			}
		})
	}
}

func TestParseUnavailableBase(t *testing.T) {
	t.Parallel()
	manifest, err := Parse([]byte(validChangesetManifest))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	base := manifest.Cases[0].Base
	if base == nil || !base.Unavailable || base.Operations != nil {
		t.Fatalf("base = %#v, want unavailable", base)
	}
}

const validSnapshotCase = `{"id":"valid","description":"Valid fixture.","kind":"snapshot","snapshot":{"operations":[]},"expected":{"findings":[]}}`

const validSnapshotManifest = `{"version":1,"seed":"seed","common":[],"cases":[` + validSnapshotCase + `]}`

const validChangesetManifest = `{"version":1,"seed":"seed","common":[],"cases":[{"id":"valid-change","description":"Valid changeset fixture.","kind":"changeset","base":"unavailable","candidate":{"operations":[]},"expected":{"status":"indeterminate","findings":[]}}]}`

const validManagedManifest = `{"version":1,"seed":"seed","common":[],"cases":[{"id":"valid-managed","description":"Valid managed fixture.","kind":"managed","managed":{"operations":[],"scenario":"merge-tip"},"expected":{"status":"complete","findings":[]}}]}`

func manifestWithOperation(operation string) string {
	return strings.Replace(validSnapshotManifest, `"operations":[]`, `"operations":[`+operation+`]`, 1)
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}
