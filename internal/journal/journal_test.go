package journal

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func fixture() Record {
	before := "0123456789012345678901234567890123456789"
	return Record{
		OwnerToken:   "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		Ref:          RefUpdate{Ref: "refs/heads/main", Before: &before, After: "abcdefabcdefabcdefabcdefabcdefabcdefabcd"},
		IndexBefore:  []byte("before-index"),
		IndexAfter:   []byte("after-index"),
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
