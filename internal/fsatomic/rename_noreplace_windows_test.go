//go:build windows

package fsatomic

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenameNoReplaceSupportsLongDirectoryPathsAndPreservesCollision(t *testing.T) {
	parent := t.TempDir()
	for range 3 {
		parent = filepath.Join(parent, strings.Repeat("long-path-", 8))
	}
	if err := os.MkdirAll(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(parent, "source")
	destination := filepath.Join(parent, "destination")
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if published, err := RenameNoReplace(source, destination); err != nil || !published {
		t.Fatalf("long publication = %v, %v", published, err)
	}

	competing := filepath.Join(parent, "competing")
	if err := os.Mkdir(competing, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(destination, "owned")
	if err := os.WriteFile(marker, []byte("destination"), 0o600); err != nil {
		t.Fatal(err)
	}
	if published, err := RenameNoReplace(competing, destination); published || !errors.Is(err, os.ErrExist) {
		t.Fatalf("collision = %v, %v", published, err)
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "destination" {
		t.Fatalf("destination marker = %q, %v", data, err)
	}
}
