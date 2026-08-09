package conformance

import (
	"encoding/base64"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestMaterializeAllRepositoryCases(t *testing.T) {
	repository := repositoryRoot(t)
	manifest, err := Load(filepath.Join(repository, "testdata", "conformance", "cases.json"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	for i := range manifest.Cases {
		fixtureCase := manifest.Cases[i]
		t.Run(fixtureCase.ID, func(t *testing.T) {
			t.Parallel()
			materialized, err := manifest.MaterializeCase(repository, t.TempDir(), fixtureCase.ID)
			if errors.Is(err, ErrPathNotRepresentable) {
				t.Skipf("host filesystem cannot represent this exact fixture: %v", err)
			}
			if err != nil {
				t.Fatalf("MaterializeCase: %v", err)
			}
			switch fixtureCase.Kind {
			case KindSnapshot:
				if materialized.Snapshot == "" || materialized.Base != "" || materialized.Candidate != "" || materialized.BaseUnavailable {
					t.Fatalf("snapshot result = %#v", materialized)
				}
				assertSeedCopied(t, materialized.Snapshot)
				assertFinalOperations(t, repository, materialized.Snapshot, manifest.Common, fixtureCase.Snapshot.Operations)
			case KindChangeset:
				if materialized.Snapshot != "" || materialized.Candidate == "" {
					t.Fatalf("changeset result = %#v", materialized)
				}
				assertSeedCopied(t, materialized.Candidate)
				assertFinalOperations(t, repository, materialized.Candidate, manifest.Common, fixtureCase.Candidate.Operations)
				if fixtureCase.Base.Unavailable {
					if materialized.Base != "" || !materialized.BaseUnavailable {
						t.Fatalf("unavailable-base result = %#v", materialized)
					}
				} else {
					if materialized.Base == "" || materialized.BaseUnavailable {
						t.Fatalf("available-base result = %#v", materialized)
					}
					assertSeedCopied(t, materialized.Base)
					assertFinalOperations(t, repository, materialized.Base, manifest.Common, fixtureCase.Base.Operations)
				}
			}
		})
	}
}

func TestMaterializedStatesAreIndependent(t *testing.T) {
	t.Parallel()
	repository := repositoryRoot(t)
	manifest, err := Load(filepath.Join(repository, "testdata", "conformance", "cases.json"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	materialized, err := manifest.MaterializeCase(repository, t.TempDir(), "append-only-compares-base-bytes")
	if err != nil {
		t.Fatalf("MaterializeCase: %v", err)
	}
	basePath := filepath.Join(materialized.Base, "topics", "derived-state.md")
	candidatePath := filepath.Join(materialized.Candidate, "topics", "derived-state.md")
	wantCandidate, err := os.ReadFile(candidatePath)
	if err != nil {
		t.Fatalf("ReadFile candidate: %v", err)
	}
	if err := os.WriteFile(basePath, []byte("changed only in base\n"), 0o644); err != nil {
		t.Fatalf("WriteFile base: %v", err)
	}
	gotCandidate, err := os.ReadFile(candidatePath)
	if err != nil {
		t.Fatalf("ReadFile candidate after base write: %v", err)
	}
	if string(gotCandidate) != string(wantCandidate) {
		t.Fatal("candidate changed after writing independent base")
	}
}

func TestMaterializeAppliesRemoveAndNestedWrite(t *testing.T) {
	t.Parallel()
	repository := newFixtureRepository(t)
	manifestJSON := `{"version":1,"seed":"seed","common":[{"operation":"remove","path":"remove.md"}],"cases":[{"id":"ops","description":"Operations.","kind":"snapshot","snapshot":{"operations":[{"operation":"write_text","path":"nested/result.md","source":"source.md"}]},"expected":{"findings":[]}}]}`
	manifest, err := Parse([]byte(manifestJSON))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	materialized, err := manifest.MaterializeCase(repository, t.TempDir(), "ops")
	if err != nil {
		t.Fatalf("MaterializeCase: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(materialized.Snapshot, "remove.md")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("removed file Lstat error = %v, want not exist", err)
	}
	got, err := os.ReadFile(filepath.Join(materialized.Snapshot, "nested", "result.md"))
	if err != nil {
		t.Fatalf("ReadFile nested result: %v", err)
	}
	if string(got) != "source bytes\n" {
		t.Fatalf("nested result = %q", got)
	}
}

func TestMaterializeRejectsMissingRemove(t *testing.T) {
	t.Parallel()
	repository := newFixtureRepository(t)
	manifestJSON := `{"version":1,"seed":"seed","common":[],"cases":[{"id":"remove","description":"Remove.","kind":"snapshot","snapshot":{"operations":[{"operation":"remove","path":"absent.md"}]},"expected":{"findings":[]}}]}`
	manifest, err := Parse([]byte(manifestJSON))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	_, err = manifest.MaterializeCase(repository, t.TempDir(), "remove")
	if err == nil || !strings.Contains(err.Error(), "absent.md") {
		t.Fatalf("MaterializeCase error = %v, want absent remove failure", err)
	}
}

func TestMaterializeRejectsSeedSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation may require Windows developer mode")
	}
	t.Parallel()
	repository := newFixtureRepository(t)
	if err := os.Symlink(filepath.Join(repository, "source.md"), filepath.Join(repository, "seed", "link.md")); err != nil {
		t.Skipf("Symlink: %v", err)
	}
	manifest, err := Parse([]byte(validSnapshotManifest))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	_, err = manifest.MaterializeCase(repository, t.TempDir(), "valid")
	if err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("MaterializeCase error = %v, want symbolic-link rejection", err)
	}
}

func TestMaterializeRejectsSourceSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation may require Windows developer mode")
	}
	t.Parallel()
	repository := newFixtureRepository(t)
	if err := os.Symlink(filepath.Join(repository, "source.md"), filepath.Join(repository, "source-link.md")); err != nil {
		t.Skipf("Symlink: %v", err)
	}
	manifest := mustParse(t, manifestWithOperation(`{"operation":"write_text","path":"result.md","source":"source-link.md"}`))
	if err := manifest.ValidateReferences(repository); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("ValidateReferences error = %v, want symbolic-link rejection", err)
	}
	_, err := manifest.MaterializeCase(repository, t.TempDir(), "valid")
	if err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("MaterializeCase error = %v, want symbolic-link rejection", err)
	}
}

func TestMaterializeDoesNotReuseExistingDestination(t *testing.T) {
	t.Parallel()
	repository := newFixtureRepository(t)
	manifest := mustParse(t, validSnapshotManifest)
	temporary := t.TempDir()
	if err := os.Mkdir(filepath.Join(temporary, "snapshot"), 0o755); err != nil {
		t.Fatalf("Mkdir snapshot: %v", err)
	}
	_, err := manifest.MaterializeCase(repository, temporary, "valid")
	if err == nil {
		t.Fatal("MaterializeCase unexpectedly reused an existing destination")
	}
}

func TestDestinationOperationsRejectSymlinkParent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation may require Windows developer mode")
	}
	t.Parallel()
	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "outside.md")
	if err := os.WriteFile(outsideFile, []byte("unchanged\n"), 0o644); err != nil {
		t.Fatalf("WriteFile outside: %v", err)
	}
	destination := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(destination, "linked")); err != nil {
		t.Skipf("Symlink: %v", err)
	}
	root, err := os.OpenRoot(destination)
	if err != nil {
		t.Fatalf("OpenRoot: %v", err)
	}
	defer root.Close()
	if err := writeRegular(root, "linked/new.md", []byte("new\n"), 0o644); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("writeRegular error = %v, want symbolic-link rejection", err)
	}
	if err := removeRegular(root, "linked/outside.md"); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("removeRegular error = %v, want symbolic-link rejection", err)
	}
	got, err := os.ReadFile(outsideFile)
	if err != nil {
		t.Fatalf("ReadFile outside: %v", err)
	}
	if string(got) != "unchanged\n" {
		t.Fatalf("outside file changed to %q", got)
	}
	if _, err := os.Stat(filepath.Join(outside, "new.md")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("outside new file Stat error = %v, want not exist", err)
	}
}

func TestExactPathAliasesNeverOverwrite(t *testing.T) {
	t.Parallel()
	destination := t.TempDir()
	root, err := os.OpenRoot(destination)
	if err != nil {
		t.Fatalf("OpenRoot: %v", err)
	}
	defer root.Close()
	if err := writeRegular(root, "STRASSE.md", []byte("upper\n"), 0o644); err != nil {
		t.Fatalf("writeRegular first spelling: %v", err)
	}
	err = writeRegular(root, "straße.md", []byte("sharp-s\n"), 0o644)
	if err == nil {
		gotUpper, readUpperErr := os.ReadFile(filepath.Join(destination, "STRASSE.md"))
		gotSharpS, readSharpSErr := os.ReadFile(filepath.Join(destination, "straße.md"))
		if readUpperErr != nil || readSharpSErr != nil {
			t.Fatalf("case-sensitive host reads: upper=%v sharp-s=%v", readUpperErr, readSharpSErr)
		}
		if string(gotUpper) != "upper\n" || string(gotSharpS) != "sharp-s\n" {
			t.Fatalf("distinct spellings not preserved: upper=%q sharp-s=%q", gotUpper, gotSharpS)
		}
		return
	}
	if !errors.Is(err, ErrPathNotRepresentable) {
		t.Fatalf("writeRegular second spelling error = %v, want ErrPathNotRepresentable", err)
	}
	got, readErr := os.ReadFile(filepath.Join(destination, "STRASSE.md"))
	if readErr != nil {
		t.Fatalf("ReadFile first spelling: %v", readErr)
	}
	if string(got) != "upper\n" {
		t.Fatalf("aliased first spelling was overwritten: %q", got)
	}
}

func assertSeedCopied(t *testing.T, stateRoot string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(stateRoot, "topics", "derived-state.md")); err != nil {
		t.Fatalf("seed record missing: %v", err)
	}
}

func assertFinalOperations(t *testing.T, repository, stateRoot string, operationSets ...[]Operation) {
	t.Helper()
	final := make(map[string]Operation)
	for _, operations := range operationSets {
		for i := range operations {
			final[operations[i].Path] = operations[i]
		}
	}
	for name, operation := range final {
		target := filepath.Join(stateRoot, filepath.FromSlash(name))
		switch operation.Kind {
		case OperationWriteText:
			want, err := os.ReadFile(filepath.Join(repository, filepath.FromSlash(*operation.Source)))
			if err != nil {
				t.Fatalf("ReadFile source %q: %v", *operation.Source, err)
			}
			got, err := os.ReadFile(target)
			if err != nil {
				t.Fatalf("ReadFile target %q: %v", name, err)
			}
			if string(got) != string(want) {
				t.Fatalf("target %q differs from source %q", name, *operation.Source)
			}
		case OperationWriteBase64:
			want, err := base64.StdEncoding.Strict().DecodeString(*operation.Content)
			if err != nil {
				t.Fatalf("DecodeString: %v", err)
			}
			got, err := os.ReadFile(target)
			if err != nil {
				t.Fatalf("ReadFile target %q: %v", name, err)
			}
			if string(got) != string(want) {
				t.Fatalf("target %q differs from decoded content", name)
			}
		case OperationRemove:
			if _, err := os.Lstat(target); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("removed target %q Lstat error = %v, want not exist", name, err)
			}
		}
	}
}

func newFixtureRepository(t *testing.T) string {
	t.Helper()
	repository := t.TempDir()
	seed := filepath.Join(repository, "seed")
	if err := os.Mkdir(seed, 0o755); err != nil {
		t.Fatalf("Mkdir seed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(seed, "remove.md"), []byte("remove me\n"), 0o644); err != nil {
		t.Fatalf("WriteFile seed: %v", err)
	}
	if err := os.Mkdir(filepath.Join(seed, "topics"), 0o755); err != nil {
		t.Fatalf("Mkdir topics: %v", err)
	}
	if err := os.WriteFile(filepath.Join(seed, "topics", "derived-state.md"), []byte("seed bytes\n"), 0o644); err != nil {
		t.Fatalf("WriteFile seed record: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repository, "source.md"), []byte("source bytes\n"), 0o644); err != nil {
		t.Fatalf("WriteFile source: %v", err)
	}
	return repository
}

func mustParse(t *testing.T, data string) *Manifest {
	t.Helper()
	manifest, err := Parse([]byte(data))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return manifest
}
