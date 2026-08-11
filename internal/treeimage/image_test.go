package treeimage

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/ontopix/engram/internal/gitraw"
	"github.com/ontopix/engram/internal/snapshot"
)

func TestCaptureMaterializeRoundTrip(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symbolic-link fixture requires platform privileges")
	}
	root := filepath.Join(t.TempDir(), "source")
	if err := os.MkdirAll(filepath.Join(root, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "dir", "file"), []byte("bytes\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("dir/file", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	image, err := Capture(root, true)
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "copy")
	if err := Materialize(destination, image, false); err != nil {
		t.Fatal(err)
	}
	copyImage, err := Capture(destination, true)
	if err != nil {
		t.Fatal(err)
	}
	if !Equal(image, copyImage) {
		t.Fatalf("round trip differs: %#v %#v", image, copyImage)
	}
}

func TestFromSnapshotAppliesManagedModes(t *testing.T) {
	t.Parallel()
	tree := &snapshot.Tree{
		Directories: []string{".", "dir"},
		Files: map[string]snapshot.File{
			"old.md":     {Path: "old.md", Data: []byte("old")},
			"new.md":     {Path: "new.md", Data: []byte("new")},
			"dir/map.md": {Path: "dir/map.md", Data: []byte("map")},
		},
	}
	image, err := FromSnapshot(tree, map[string]gitraw.TreeMode{"old.md": gitraw.ModeExecutable})
	if err != nil {
		t.Fatal(err)
	}
	if image["old.md"].Mode != 0o755 || image["new.md"].Mode != 0o644 || image["dir"].Kind != Directory {
		t.Fatalf("image = %#v", image)
	}
}

func TestLogicalOnlyRejectsEveryPrunedPath(t *testing.T) {
	t.Parallel()
	projected := &snapshot.Tree{Directories: []string{"."}, Files: map[string]snapshot.File{"README.md": {Path: "README.md"}}}
	for _, name := range []string{".hidden", ".engram/cache", ".git", ".engram/other"} {
		image := Image{"README.md": {Kind: Regular}, name: {Kind: Directory}}
		if err := LogicalOnly(image, projected); err == nil {
			t.Errorf("pruned path %q accepted", name)
		}
	}
}

func TestCaptureDoesNotFollowSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symbolic-link fixture requires platform privileges")
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "root")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	image, err := Capture(root, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(image) != 1 || image["escape"].Kind != Symlink {
		t.Fatalf("image followed symlink: %#v", image)
	}
}
