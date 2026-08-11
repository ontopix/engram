package lockidentity

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIdentityDistinguishesOwnedChangedAndReplacementLocks(t *testing.T) {
	name := filepath.Join(t.TempDir(), "operation.lock")
	firstFile, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	first, err := Establish(firstFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := firstFile.Close(); err != nil {
		t.Fatal(err)
	}
	if state, err := first.Inspect(name); err != nil || state != Owned {
		t.Fatalf("owned state=%v error=%v", state, err)
	}
	if err := os.WriteFile(name, []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if state, err := first.Inspect(name); err != nil || state != Other {
		t.Fatalf("changed state=%v error=%v", state, err)
	}
	if err := os.Remove(name); err != nil {
		t.Fatal(err)
	}
	secondFile, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Establish(secondFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := secondFile.Close(); err != nil {
		t.Fatal(err)
	}
	if state, err := first.Inspect(name); err != nil || state != Other {
		t.Fatalf("replacement seen by first state=%v error=%v", state, err)
	}
	if state, err := second.Inspect(name); err != nil || state != Owned {
		t.Fatalf("replacement seen by second state=%v error=%v", state, err)
	}
	if err := os.Remove(name); err != nil {
		t.Fatal(err)
	}
	if state, err := second.Inspect(name); err != nil || state != Absent {
		t.Fatalf("absent state=%v error=%v", state, err)
	}
}
