package journal

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ontopix/engram/internal/gitraw"
)

func fixture() Record {
	before := "0123456789012345678901234567890123456789"
	return Record{
		OwnerToken:   "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		Owner:        OwnerIdentity{PID: 123, Hostname: "fixture", StartedAt: "2026-01-01T00:00:00Z"},
		ObjectFormat: gitraw.SHA1,
		Ref:          RefUpdate{Ref: "refs/heads/main", Before: &before, After: "abcdefabcdefabcdefabcdefabcdefabcdefabcd"},
		IndexBefore:  RawFileImage{Present: true, Data: []byte("before-index")},
		IndexAfter:   RawFileImage{Present: true, Data: []byte("after-index")},
		Paths:        []PathUpdate{{Path: "note.md", Before: &Image{Kind: "regular", Mode: 0o644, Data: []byte("old")}, After: &Image{Kind: "regular", Mode: 0o644, Data: []byte("new")}}},
		Fingerprints: []Fingerprint{{Name: "config", Present: true, Kind: "regular", Data: []byte("value")}},
	}
}

func TestJournalLifecycleIsCanonical(t *testing.T) {
	t.Parallel()
	name := Path(t.TempDir())
	record := fixture()
	if err := WritePending(name, record); err != nil {
		t.Fatal(err)
	}
	read, pendingBytes, err := Read(name)
	if err != nil || read.State != Pending || read.Version != 1 {
		t.Fatalf("Read = %#v, %v", read, err)
	}
	completeBytes, err := SetState(name, pendingBytes, Complete)
	if err != nil {
		t.Fatal(err)
	}
	read, observed, err := Read(name)
	if err != nil || read.State != Complete || !bytes.Equal(observed, completeBytes) {
		t.Fatalf("complete Read = %#v, %v", read, err)
	}
	if err := Remove(name, completeBytes); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(name); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("journal remains: %v", err)
	}
}

func TestJournalCreateIsExclusive(t *testing.T) {
	t.Parallel()
	name := Path(t.TempDir())
	if err := WritePending(name, fixture()); err != nil {
		t.Fatal(err)
	}
	if err := WritePending(name, fixture()); !errors.Is(err, ErrExists) {
		t.Fatalf("error = %v", err)
	}
}

func TestJournalRejectsUnknownAndNonCanonicalBytes(t *testing.T) {
	t.Parallel()
	name := filepath.Join(t.TempDir(), "journal")
	for _, data := range [][]byte{
		[]byte(`{"version":1,"unknown":true}` + "\n"),
		[]byte("{}\n"),
		[]byte(" { }\n"),
	} {
		if err := os.WriteFile(name, data, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := Read(name); err == nil {
			t.Fatalf("accepted %q", data)
		}
	}
}

func TestTerminalUpdateRequiresExactObservedBytes(t *testing.T) {
	t.Parallel()
	name := Path(t.TempDir())
	if err := WritePending(name, fixture()); err != nil {
		t.Fatal(err)
	}
	_, observed, err := Read(name)
	if err != nil {
		t.Fatal(err)
	}
	wrong := append([]byte(nil), observed...)
	wrong[0] ^= 1
	if _, err := SetState(name, wrong, Cancelled); !errors.Is(err, ErrChanged) {
		t.Fatalf("error = %v", err)
	}
}

func TestJournalRejectsSymlinkedAdministrationAndFinalFile(t *testing.T) {
	gitDir := t.TempDir()
	external := t.TempDir()
	if err := os.Symlink(external, filepath.Join(gitDir, "engram")); err != nil {
		t.Skipf("symbolic links unavailable: %v", err)
	}
	if err := WritePending(Path(gitDir), fixture()); err == nil {
		t.Fatal("write followed a symbolic-link administration path")
	}
	if _, err := os.Lstat(filepath.Join(external, "recovery")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("external path was touched: %v", err)
	}

	owned := Path(t.TempDir())
	if err := WritePending(owned, fixture()); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "journal")
	if err := os.Symlink(owned, link); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Read(link); err == nil {
		t.Fatal("read followed a symbolic-link journal")
	}
}
