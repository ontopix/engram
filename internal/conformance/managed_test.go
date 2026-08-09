package conformance_test

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"github.com/ontopix/engram/internal/conformance"
	"github.com/ontopix/engram/internal/managedread"
)

func TestManagedConformanceManifest(t *testing.T) {
	repository := repositoryRoot(t)
	manifest, err := conformance.Load(filepath.Join(repository, "testdata", "conformance", "cases.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, fixture := range manifest.Cases {
		if fixture.Kind != conformance.KindManaged {
			continue
		}
		fixture := fixture
		t.Run(fixture.ID, func(t *testing.T) {
			materialized, err := manifest.MaterializeCase(repository, t.TempDir(), fixture.ID)
			if errors.Is(err, conformance.ErrFixtureCapability) || errors.Is(err, conformance.ErrPathNotRepresentable) {
				t.Skip(err)
			}
			if err != nil {
				t.Fatal(err)
			}
			store, err := managedread.Open(context.Background(), materialized.Managed)
			if err != nil {
				t.Fatal(err)
			}
			audit, err := store.AuditAccepted(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if string(audit.Validation.Status) != string(*fixture.Expected.Status) {
				t.Errorf("status = %q, want %q", audit.Validation.Status, *fixture.Expected.Status)
			}
			got := make([]conformance.Finding, len(audit.Validation.Findings))
			for i, finding := range audit.Validation.Findings {
				got[i] = conformance.Finding{Code: finding.Code, Path: finding.Path}
			}
			if !reflect.DeepEqual(got, fixture.Expected.Findings) {
				t.Fatalf("findings = %#v, want %#v", got, fixture.Expected.Findings)
			}
			for _, finding := range got {
				for _, code := range fixture.Expected.NotFindings {
					if finding.Code == code {
						t.Fatalf("finding %s was explicitly forbidden by the fixture", code)
					}
				}
			}
		})
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}
