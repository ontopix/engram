package checker

import (
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/ontopix/engram/internal/conformance"
)

func TestSeedSnapshotConforms(t *testing.T) {
	t.Parallel()
	repository := repositoryRoot(t)
	checked, err := CheckFS(filepath.Join(repository, "examples", "minimal"))
	if err != nil {
		t.Fatal(err)
	}
	if checked.Validation.HasErrors() || len(checked.Validation.Findings) != 0 {
		t.Fatalf("minimal example findings = %#v", checked.Validation.Findings)
	}
}

func TestSnapshotConformanceManifest(t *testing.T) {
	repository := repositoryRoot(t)
	manifest, err := conformance.Load(filepath.Join(repository, "testdata", "conformance", "cases.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, fixture := range manifest.Cases {
		if fixture.Kind != conformance.KindSnapshot {
			continue
		}
		fixture := fixture
		t.Run(fixture.ID, func(t *testing.T) {
			materialized, err := manifest.MaterializeCase(repository, t.TempDir(), fixture.ID)
			if errors.Is(err, conformance.ErrPathNotRepresentable) {
				t.Skip(err)
			}
			if err != nil {
				t.Fatal(err)
			}
			checked, err := CheckFS(materialized.Snapshot)
			if err != nil {
				t.Fatal(err)
			}
			got := identities(checked.Validation.Findings)
			if !reflect.DeepEqual(got, fixture.Expected.Findings) {
				t.Fatalf("findings = %#v, want %#v", got, fixture.Expected.Findings)
			}
		})
	}
}

func TestChangesetConformanceManifest(t *testing.T) {
	repository := repositoryRoot(t)
	manifest, err := conformance.Load(filepath.Join(repository, "testdata", "conformance", "cases.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, fixture := range manifest.Cases {
		if fixture.Kind != conformance.KindChangeset {
			continue
		}
		fixture := fixture
		t.Run(fixture.ID, func(t *testing.T) {
			materialized, err := manifest.MaterializeCase(repository, t.TempDir(), fixture.ID)
			if err != nil {
				t.Fatal(err)
			}
			candidate, err := CheckFS(materialized.Candidate)
			if err != nil {
				t.Fatal(err)
			}
			var base *Snapshot
			if !materialized.BaseUnavailable {
				base, err = CheckFS(materialized.Base)
				if err != nil {
					t.Fatal(err)
				}
			}
			checked, _ := CheckTransition(base, candidate, false)
			if string(checked.Status) != string(*fixture.Expected.Status) {
				t.Errorf("status = %q, want %q", checked.Status, *fixture.Expected.Status)
			}
			got := identities(checked.Findings)
			if !reflect.DeepEqual(got, fixture.Expected.Findings) {
				t.Fatalf("findings = %#v, want %#v", got, fixture.Expected.Findings)
			}
		})
	}
}

func identities(findings []Finding) []conformance.Finding {
	result := make([]conformance.Finding, len(findings))
	for index, finding := range findings {
		result[index] = conformance.Finding{Code: finding.Code, Path: finding.Path}
	}
	return result
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}
