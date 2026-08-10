package managedwrite

import (
	"runtime"
	"testing"

	"github.com/ontopix/engram/internal/journal"
)

func TestEquivalentPathPermissionsMatchesHostRepresentation(t *testing.T) {
	t.Parallel()
	if !equivalentPathPermissions(0o644, 0o644) {
		t.Fatal("identical permissions are not equivalent")
	}
	if runtime.GOOS == "windows" {
		if !equivalentPathPermissions(0o644, 0o666) || !equivalentPathPermissions(0o755, 0o666) || equivalentPathPermissions(0o444, 0o666) {
			t.Fatal("Windows equivalence does not match its writable/read-only representation")
		}
	} else if equivalentPathPermissions(0o644, 0o666) || equivalentPathPermissions(0o755, 0o644) {
		t.Fatal("Unix equivalence accepted different permission bits")
	}
}

func TestSameJournalImageUsesPortableModesAndExactBytes(t *testing.T) {
	t.Parallel()
	if !sameJournalImage(&journal.Image{Kind: "directory", Mode: 0o700}, &journal.Image{Kind: "directory", Mode: 0o755}) {
		t.Fatal("directory presentation modes changed the reconciled image")
	}
	left := &journal.Image{Kind: "regular", Mode: 0o644, Data: []byte("left\n")}
	right := &journal.Image{Kind: "regular", Mode: 0o644, Data: []byte("right\n")}
	if sameJournalImage(left, right) {
		t.Fatal("different regular-file bytes were treated as equivalent")
	}
}
