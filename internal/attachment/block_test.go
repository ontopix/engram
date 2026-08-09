package attachment

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestAttachDetachPreserveSurroundingBytes(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	project := filepath.Join(root, "project")
	first := filepath.Join(root, "first")
	second := filepath.Join(root, "second")
	for _, directory := range []string{project, first, second} {
		if err := os.Mkdir(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	entrypoint := filepath.Join(project, "AGENTS.md")
	if err := os.WriteFile(entrypoint, []byte("before\n\nafter\n"), 0o640); err != nil {
		t.Fatal(err)
	}

	attached, err := Attach(project, entrypoint, second)
	if err != nil || !attached.Changed {
		t.Fatalf("first attach = %#v, %v", attached, err)
	}
	attached, err = Attach(project, entrypoint, first)
	if err != nil || !attached.Changed {
		t.Fatalf("second attach = %#v, %v", attached, err)
	}
	unchanged, err := Attach(project, entrypoint, first)
	if err != nil || unchanged.Changed {
		t.Fatalf("idempotent attach = %#v, %v", unchanged, err)
	}
	data, err := os.ReadFile(entrypoint)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(data), "before\n\nafter\n\n") {
		t.Fatalf("surrounding prefix changed: %q", data)
	}
	firstIndex := strings.Index(string(data), filepath.Clean(first))
	secondIndex := strings.Index(string(data), filepath.Clean(second))
	if firstIndex < 0 || secondIndex <= firstIndex {
		t.Fatalf("stores not sorted in block: %q", data)
	}
	if strings.Count(string(data), OpenMarker) != 1 || strings.Count(string(data), CloseMarker) != 1 {
		t.Fatalf("owned markers = %q", data)
	}

	detached, err := Detach(project, entrypoint, first)
	if err != nil || !detached.Changed {
		t.Fatalf("first detach = %#v, %v", detached, err)
	}
	detached, err = Detach(project, entrypoint, second)
	if err != nil || !detached.Changed {
		t.Fatalf("last detach = %#v, %v", detached, err)
	}
	final, err := os.ReadFile(entrypoint)
	if err != nil {
		t.Fatal(err)
	}
	if string(final) != "before\n\nafter\n\n" {
		t.Fatalf("detach changed surrounding bytes: %q", final)
	}
	info, err := os.Stat(entrypoint)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o640 {
		t.Fatalf("mode = %o", info.Mode().Perm())
	}
}

func TestAttachCreatesDefaultEntrypoint(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	project := filepath.Join(root, "project")
	store := filepath.Join(root, "store")
	for _, directory := range []string{project, store} {
		if err := os.Mkdir(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	result, err := Attach(project, "", store)
	if err != nil {
		t.Fatal(err)
	}
	canonicalProject, err := CanonicalStore(project)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed || result.Entrypoint != filepath.Join(canonicalProject, "AGENTS.md") {
		t.Fatalf("result = %#v", result)
	}
	data, err := os.ReadFile(result.Entrypoint)
	if err != nil {
		t.Fatal(err)
	}
	canonicalStore, err := CanonicalStore(store)
	if err != nil {
		t.Fatal(err)
	}
	want, err := encode([]string{canonicalStore})
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(want) {
		t.Fatalf("entrypoint = %q, want %q", data, want)
	}
}

func TestMalformedOwnedBlockIsNeverRepaired(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	project := filepath.Join(root, "project")
	store := filepath.Join(root, "store")
	for _, directory := range []string{project, store} {
		if err := os.Mkdir(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	entrypoint := filepath.Join(project, "AGENTS.md")
	original := []byte(OpenMarker + "\nnot the owned body\n" + CloseMarker + "\n")
	if err := os.WriteFile(entrypoint, original, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Attach(project, entrypoint, store); !errors.Is(err, ErrMalformedBlock) {
		t.Fatalf("error = %v", err)
	}
	got, err := os.ReadFile(entrypoint)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(original) {
		t.Fatalf("malformed entrypoint changed: %q", got)
	}
}

func TestDuplicatePhysicalIdentityIsRejected(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symbolic-link setup requires platform privileges")
	}
	t.Parallel()
	root := t.TempDir()
	project := filepath.Join(root, "project")
	store := filepath.Join(root, "store")
	alias := filepath.Join(root, "alias")
	for _, directory := range []string{project, store} {
		if err := os.Mkdir(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(store, alias); err != nil {
		t.Fatal(err)
	}
	block, err := encode([]string{store, alias})
	if err != nil {
		t.Fatal(err)
	}
	entrypoint := filepath.Join(project, "AGENTS.md")
	if err := os.WriteFile(entrypoint, block, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Detach(project, entrypoint, store); !errors.Is(err, ErrMalformedBlock) {
		t.Fatalf("error = %v", err)
	}
}

func TestEntrypointMustStayBelowProject(t *testing.T) {
	t.Parallel()
	project := t.TempDir()
	if _, err := ResolveEntrypoint(project, filepath.Join("..", "AGENTS.md")); err == nil {
		t.Fatal("path escape accepted")
	}
}

func TestCooperatingLockRejectsConcurrentUpdate(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	project := filepath.Join(root, "project")
	store := filepath.Join(root, "store")
	for _, directory := range []string{project, store} {
		if err := os.Mkdir(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	entrypoint := filepath.Join(project, "AGENTS.md")
	if err := os.WriteFile(entrypoint+".engram.lock", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Attach(project, entrypoint, store); !errors.Is(err, ErrBusy) {
		t.Fatalf("error = %v", err)
	}
}
