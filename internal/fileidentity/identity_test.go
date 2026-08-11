package fileidentity

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestPinSurvivesPathRenameAndReplacement(t *testing.T) {
	parent := t.TempDir()
	target := filepath.Join(parent, "target")
	displaced := filepath.Join(parent, "displaced")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	original, err := os.Lstat(target)
	if err != nil {
		t.Fatal(err)
	}
	if err := Pin(original); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(target, displaced); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	replacement, err := os.Lstat(target)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(original, replacement) {
		t.Fatal("pinned original identity followed its replaced path")
	}
	displacedInfo, err := os.Lstat(displaced)
	if err != nil || !os.SameFile(original, displacedInfo) {
		t.Fatalf("pinned original no longer identifies displaced directory: %v", err)
	}
}

func TestPinRejectsNil(t *testing.T) {
	if err := Pin(nil); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Pin(nil) = %v", err)
	}
}

func TestPersistentIDComesFromOpenDescriptor(t *testing.T) {
	parent := t.TempDir()
	firstName := filepath.Join(parent, "first")
	secondName := filepath.Join(parent, "second")
	if err := os.WriteFile(firstName, []byte("same\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secondName, []byte("same\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	first, err := os.Open(firstName)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	firstInfo, err := first.Stat()
	if err != nil {
		t.Fatal(err)
	}
	firstID, err := PersistentID(first, firstInfo)
	if err != nil {
		t.Fatal(err)
	}
	second, err := os.Open(secondName)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	secondInfo, err := second.Stat()
	if err != nil {
		t.Fatal(err)
	}
	secondID, err := PersistentID(second, secondInfo)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(firstID, secondID) {
		t.Fatal("distinct open files received the same persistent identity")
	}
	repeated, err := PersistentID(first, firstInfo)
	if err != nil || !bytes.Equal(firstID, repeated) {
		t.Fatalf("open descriptor identity changed between reads: %x, %v", repeated, err)
	}
	// os.Open does not request FILE_SHARE_DELETE on Windows, so the open
	// descriptor intentionally prevents the pathname rename tested below.
	// The descriptor-source and repeated-read guarantees above remain portable.
	if runtime.GOOS == "windows" {
		return
	}
	if err := os.Rename(firstName, filepath.Join(parent, "moved")); err != nil {
		t.Fatal(err)
	}
	repeated, err = PersistentID(first, firstInfo)
	if err != nil || !bytes.Equal(firstID, repeated) {
		t.Fatalf("open descriptor identity changed after rename: %x, %v", repeated, err)
	}
}
