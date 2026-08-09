package conformance

import (
	"path/filepath"
	"reflect"
	"runtime"
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

	wantIDs := []string{
		"append-only-compares-base-bytes",
		"catalog-none-forbids-region",
		"changeset-base-unavailable",
		"finding-aggregation",
		"immutable-deletion-uses-base",
		"local-link-components",
		"local-link-invalid-paths",
		"nfd-entry-name",
		"normed-text-encoding",
		"note-baseline-normal-form",
		"readme-frontmatter-unparsable",
		"unicode-full-case-fold",
		"universal-label-scalar-types",
		"unknown-x-engram-keyword",
		"yaml-core-plain-date",
	}
	if got := manifest.SortedCaseIDs(); !reflect.DeepEqual(got, wantIDs) {
		t.Fatalf("case IDs:\n got: %q\nwant: %q", got, wantIDs)
	}

	var snapshots, changesets int
	for i := range manifest.Cases {
		switch manifest.Cases[i].Kind {
		case KindSnapshot:
			snapshots++
		case KindChangeset:
			changesets++
		}
	}
	if snapshots != 12 || changesets != 3 {
		t.Fatalf("kind counts = snapshot:%d changeset:%d, want 12 and 3", snapshots, changesets)
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
		{"snapshot base field", strings.Replace(validSnapshotManifest, `"snapshot":{"operations":[]}`, `"snapshot":{"operations":[]},"base":{"operations":[]}`, 1), "must not contain base or candidate"},
		{"snapshot null base field", strings.Replace(validSnapshotManifest, `"snapshot":{"operations":[]}`, `"snapshot":{"operations":[]},"base":null`, 1), "base must not be null"},
		{"snapshot status", strings.Replace(validSnapshotManifest, `"findings":[]`, `"status":"complete","findings":[]`, 1), "status is not allowed"},
		{"snapshot null status", strings.Replace(validSnapshotManifest, `"findings":[]`, `"status":null,"findings":[]`, 1), "status must not be null"},
		{"unknown state field", strings.Replace(validSnapshotManifest, `"operations":[]`, `"operations":[],"extra":true`, 1), "unknown field"},
		{"unknown operation field", manifestWithOperation(`{"operation":"remove","path":"old.md","extra":true}`), "unknown field"},
		{"unknown operation", manifestWithOperation(`{"operation":"copy","path":"new.md"}`), ".operation"},
		{"remove null source", manifestWithOperation(`{"operation":"remove","path":"old.md","source":null}`), "source must not be null"},
		{"write text missing source", manifestWithOperation(`{"operation":"write_text","path":"new.md"}`), "source is required"},
		{"write text content", manifestWithOperation(`{"operation":"write_text","path":"new.md","source":"source.md","content":""}`), "content is not allowed"},
		{"invalid base64", manifestWithOperation(`{"operation":"write_base64","path":"new.md","content":"***"}`), "not strict RFC 4648 base64"},
		{"unsafe operation path", manifestWithOperation(`{"operation":"remove","path":"../old.md"}`), "unsafe path segment"},
		{"unknown expected field", strings.Replace(validSnapshotManifest, `"findings":[]`, `"findings":[],"extra":true`, 1), "unknown field"},
		{"null findings", strings.Replace(validSnapshotManifest, `"findings":[]`, `"findings":null`, 1), "findings must not be null"},
		{"invalid finding code", strings.Replace(validSnapshotManifest, `"findings":[]`, `"findings":[{"code":"BAD","path":"."}]`, 1), ".code"},
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
