package attachment

import (
	"bytes"
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

func TestDetachCanRemoveAStaleMissingPath(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	project := filepath.Join(root, "project")
	store := filepath.Join(root, "store")
	for _, directory := range []string{project, store} {
		if err := os.Mkdir(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	attached, err := Attach(project, "", store)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(store); err != nil {
		t.Fatal(err)
	}
	detached, err := Detach(project, attached.Entrypoint, store)
	if err != nil || !detached.Changed {
		t.Fatalf("detach = %#v, %v", detached, err)
	}
}

func TestUpdaterReportsPostMutationEffects(t *testing.T) {
	fault := errors.New("injected attachment fault")
	tests := []struct {
		name             string
		configure        func(*Updater)
		wantDurable      bool
		wantRecovery     bool
		wantLockResidual bool
	}{
		{
			name: "after rename",
			configure: func(updater *Updater) {
				updater.afterRename = func(string) error { return fault }
			},
		},
		{
			name: "after directory sync",
			configure: func(updater *Updater) {
				updater.afterSync = func(string) error { return fault }
			},
			wantDurable: true,
		},
		{
			name: "after release without residual lock",
			configure: func(updater *Updater) {
				updater.afterRelease = func(string) error { return fault }
			},
			wantDurable: true,
		},
		{
			name: "lock removal failure",
			configure: func(updater *Updater) {
				updater.beforeRemove = func(string) error { return fault }
			},
			wantDurable:      true,
			wantRecovery:     true,
			wantLockResidual: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
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
			updater := NewUpdater()
			test.configure(updater)
			result, err := updater.Attach(project, entrypoint, store)
			if err == nil || result != (Result{}) {
				t.Fatalf("result=%#v error=%v", result, err)
			}
			effect, ok := EffectOf(err)
			if !ok || effect.Durable != test.wantDurable || effect.RecoveryRequired != test.wantRecovery {
				t.Fatalf("effect=%#v present=%v error=%v", effect, ok, err)
			}
			content, readErr := os.ReadFile(entrypoint)
			if readErr != nil || !bytes.Contains(content, []byte(OpenMarker)) {
				t.Fatalf("published entrypoint=%q error=%v", content, readErr)
			}
			_, lockErr := os.Lstat(entrypoint + ".engram.lock")
			if test.wantLockResidual && lockErr != nil {
				t.Fatalf("residual lock missing: %v", lockErr)
			}
			if !test.wantLockResidual && !errors.Is(lockErr, os.ErrNotExist) {
				t.Fatalf("unexpected residual lock: %v", lockErr)
			}
		})
	}
}

func TestUpdaterDoesNotInventEffectAfterUnchangedRelease(t *testing.T) {
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
	if _, err := Attach(project, entrypoint, store); err != nil {
		t.Fatal(err)
	}
	fault := errors.New("injected release fault")
	updater := NewUpdater()
	updater.afterRelease = func(string) error { return fault }
	result, err := updater.Attach(project, entrypoint, store)
	if !errors.Is(err, fault) || result != (Result{}) {
		t.Fatalf("result=%#v error=%v", result, err)
	}
	if effect, ok := EffectOf(err); ok {
		t.Fatalf("invented effect=%#v error=%v", effect, err)
	}
	if _, statErr := os.Lstat(entrypoint + ".engram.lock"); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("lock remains: %v", statErr)
	}
}

func TestUpdaterResidualLockWithoutPublicationIsNotDurable(t *testing.T) {
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
	if _, err := Attach(project, entrypoint, store); err != nil {
		t.Fatal(err)
	}
	updater := NewUpdater()
	updater.beforeRemove = func(string) error { return errors.New("injected lock removal failure") }
	result, err := updater.Attach(project, entrypoint, store)
	if err == nil || result != (Result{}) {
		t.Fatalf("result=%#v error=%v", result, err)
	}
	effect, ok := EffectOf(err)
	if !ok || effect.Durable || !effect.RecoveryRequired {
		t.Fatalf("effect=%#v present=%v error=%v", effect, ok, err)
	}
}

func TestLockReleaseDoesNotClaimANextOwnerAsResidual(t *testing.T) {
	t.Parallel()
	name := filepath.Join(t.TempDir(), "entrypoint.engram.lock")
	first, err := acquireLock(name)
	if err != nil {
		t.Fatal(err)
	}
	var next *lockFile
	residual, err := first.release(nil, func(string) error {
		var acquireErr error
		next, acquireErr = acquireLock(name)
		return acquireErr
	})
	if err != nil || residual || next == nil {
		t.Fatalf("residual=%v next=%#v error=%v", residual, next, err)
	}
	if _, err := next.release(nil, nil); err != nil {
		t.Fatal(err)
	}
}

func TestLockReleaseNeverUnlinksAReplacementOwner(t *testing.T) {
	t.Parallel()
	name := filepath.Join(t.TempDir(), "entrypoint.engram.lock")
	first, err := acquireLock(name)
	if err != nil {
		t.Fatal(err)
	}
	var next *lockFile
	residual, err := first.release(func(string) error {
		if removeErr := os.Remove(name); removeErr != nil {
			return removeErr
		}
		var acquireErr error
		next, acquireErr = acquireLock(name)
		return acquireErr
	}, nil)
	if err == nil || residual || next == nil || !errors.Is(err, ErrBusy) {
		t.Fatalf("residual=%v next=%#v error=%v", residual, next, err)
	}
	owned, statErr := next.file.Stat()
	current, pathErr := os.Lstat(name)
	if statErr != nil || pathErr != nil || !os.SameFile(owned, current) {
		t.Fatalf("replacement owner was unlinked: stat=%v path=%v", statErr, pathErr)
	}
	if _, err := next.release(nil, nil); err != nil {
		t.Fatal(err)
	}
}

func TestUpdaterDetachCannotSucceedWithResidualLock(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	project := filepath.Join(root, "project")
	store := filepath.Join(root, "store")
	for _, directory := range []string{project, store} {
		if err := os.Mkdir(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	attached, err := Attach(project, "", store)
	if err != nil {
		t.Fatal(err)
	}
	updater := NewUpdater()
	updater.beforeRemove = func(string) error { return errors.New("injected lock removal failure") }
	result, err := updater.Detach(project, attached.Entrypoint, store)
	if err == nil || result != (Result{}) {
		t.Fatalf("result=%#v error=%v", result, err)
	}
	effect, ok := EffectOf(err)
	if !ok || !effect.Durable || !effect.RecoveryRequired {
		t.Fatalf("effect=%#v present=%v error=%v", effect, ok, err)
	}
	if _, err := os.Lstat(attached.Entrypoint + ".engram.lock"); err != nil {
		t.Fatalf("residual lock missing: %v", err)
	}
	if content, readErr := os.ReadFile(attached.Entrypoint); readErr != nil || bytes.Contains(content, []byte(OpenMarker)) {
		t.Fatalf("detachment was not published: %q %v", content, readErr)
	}
}
