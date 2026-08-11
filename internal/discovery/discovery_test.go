package discovery

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestFromSelectsNearestMarker(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	outer := filepath.Join(root, "outer")
	inner := filepath.Join(outer, "inner")
	for _, directory := range []string{filepath.Join(outer, ".engram"), filepath.Join(inner, ".engram"), filepath.Join(inner, "child")} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, marker := range []string{filepath.Join(outer, ".engram", "root.yaml"), filepath.Join(inner, ".engram", "root.yaml")} {
		if err := os.WriteFile(marker, []byte("engram: 1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := From(filepath.Join(inner, "child"))
	if err != nil {
		t.Fatal(err)
	}
	if got != inner {
		t.Fatalf("root = %q, want %q", got, inner)
	}
}

func TestFromDoesNotUseGitRepositoryAsStore(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := From(root); !errors.Is(err, ErrNotFound) {
		t.Fatalf("error = %v, want ErrNotFound", err)
	}
}
