package hooks

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/ontopix/engram/internal/lockidentity"
)

func TestEmptySetIsTrustedWithoutGrant(t *testing.T) {
	registry, store := registryFixture(t)
	empty := EmptySet()

	listed, err := registry.List(store, empty)
	if err != nil {
		t.Fatal(err)
	}
	if !listed.Trusted || listed.Changed || listed.Hooks == nil || len(listed.Hooks) != 0 {
		t.Fatalf("empty list = %#v", listed)
	}
	trusted, err := registry.Trust(store, empty)
	if err != nil {
		t.Fatal(err)
	}
	if !trusted.Trusted || trusted.Changed {
		t.Fatalf("empty trust = %#v", trusted)
	}
	if _, err := os.Lstat(registry.Path()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("empty trust created registry: %v", err)
	}
}

func TestTrustBindsExactSetAndPublishesStablePrivateRegistry(t *testing.T) {
	registry, store := registryFixture(t)
	base := mustSelect(t, map[string][]byte{"10-a.sh": []byte("#!/usr/bin/env sh\n")})
	changed := mustSelect(t, map[string][]byte{"10-a.sh": []byte("#!/usr/bin/env sh\nprintf changed\n")})
	added := mustSelect(t, map[string][]byte{
		"10-a.sh": []byte("#!/usr/bin/env sh\n"),
		"20-b.py": []byte("#!/usr/bin/env python3\n"),
	})

	before, err := registry.List(store, base)
	if err != nil {
		t.Fatal(err)
	}
	if before.Trusted {
		t.Fatal("non-empty set unexpectedly trusted")
	}
	first, err := registry.Trust(store, base)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Changed || !first.Trusted || first.SHA256 != base.SHA256 || !sameDescriptions(first.Hooks, base.Hooks) {
		t.Fatalf("first trust = %#v", first)
	}

	content, err := os.ReadFile(registry.Path())
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(registry.Path())
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); runtime.GOOS != "windows" && got != 0o600 {
		t.Fatalf("registry mode = %o, want 600", got)
	}
	parentInfo, err := os.Stat(filepath.Dir(registry.Path()))
	if err != nil {
		t.Fatal(err)
	}
	if got := parentInfo.Mode().Perm(); !safeRegistryDirectoryMode(parentInfo.Mode()) {
		t.Fatalf("created registry directory mode = %o, want private", got)
	}
	document, err := decodeRegistry(content)
	if err != nil {
		t.Fatal(err)
	}
	reencoded, err := encodeRegistry(document)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(content, reencoded) {
		t.Fatalf("registry serialization is unstable:\n%s\n%s", content, reencoded)
	}

	second, err := registry.Trust(store, base)
	if err != nil {
		t.Fatal(err)
	}
	if second.Changed || !second.Trusted {
		t.Fatalf("second trust = %#v", second)
	}
	after, err := os.ReadFile(registry.Path())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(content, after) {
		t.Fatal("unchanged trust rewrote registry")
	}

	for name, set := range map[string]Set{"byte-change": changed, "addition": added} {
		listed, err := registry.List(store, set)
		if err != nil {
			t.Fatal(err)
		}
		if listed.Trusted {
			t.Fatalf("%s inherited trust from different set", name)
		}
	}
}

func TestPhysicalAliasSharesBindingButMoveAndCopyDoNot(t *testing.T) {
	registry, store := registryFixture(t)
	set := mustSelect(t, map[string][]byte{"10-a.sh": []byte("#!/usr/bin/env sh\n")})
	if _, err := registry.Trust(store, set); err != nil {
		t.Fatal(err)
	}

	alias := filepath.Join(filepath.Dir(store), "store-alias")
	if err := os.Symlink(store, alias); err != nil {
		t.Skipf("symbolic links unavailable: %v", err)
	}
	listed, err := registry.List(alias, set)
	if err != nil {
		t.Fatal(err)
	}
	if !listed.Trusted {
		t.Fatal("physical alias did not share trust binding")
	}

	copyPath := filepath.Join(filepath.Dir(store), "store-copy")
	if err := os.Mkdir(copyPath, 0o700); err != nil {
		t.Fatal(err)
	}
	copied, err := registry.List(copyPath, set)
	if err != nil {
		t.Fatal(err)
	}
	if copied.Trusted {
		t.Fatal("copied physical store inherited trust")
	}

	moved := filepath.Join(filepath.Dir(store), "store-moved")
	if err := os.Rename(store, moved); err != nil {
		t.Fatal(err)
	}
	movedResult, err := registry.List(moved, set)
	if err != nil {
		t.Fatal(err)
	}
	if movedResult.Trusted {
		t.Fatal("moved store inherited old-path trust")
	}
}

func TestSelectiveAndTotalRevokeHistoricalSets(t *testing.T) {
	registry, store := registryFixture(t)
	a := mustSelect(t, map[string][]byte{"10-a.sh": []byte("#!/usr/bin/env sh\n")})
	ab := mustSelect(t, map[string][]byte{
		"10-a.sh": []byte("#!/usr/bin/env sh\n"),
		"20-b.sh": []byte("#!/usr/bin/env sh\n"),
	})
	b := mustSelect(t, map[string][]byte{"20-b.sh": []byte("#!/usr/bin/env sh\n")})
	for _, set := range []Set{a, ab, b} {
		if _, err := registry.Trust(store, set); err != nil {
			t.Fatal(err)
		}
	}

	selective, err := registry.Revoke(store, "10-a.sh", "10-a.sh")
	if err != nil {
		t.Fatal(err)
	}
	wantSelective := []string{a.SHA256, ab.SHA256}
	sort.Strings(wantSelective)
	if !selective.Changed || !reflect.DeepEqual(selective.RevokedSets, wantSelective) {
		t.Fatalf("selective revoke = %#v, want %#v", selective, wantSelective)
	}
	for _, set := range []Set{a, ab} {
		listed, err := registry.List(store, set)
		if err != nil {
			t.Fatal(err)
		}
		if listed.Trusted {
			t.Fatalf("revoked set %s remains trusted", set.SHA256)
		}
	}
	retained, err := registry.List(store, b)
	if err != nil {
		t.Fatal(err)
	}
	if !retained.Trusted {
		t.Fatal("unmatched historical set was revoked")
	}

	total, err := registry.Revoke(store)
	if err != nil {
		t.Fatal(err)
	}
	if !total.Changed || !reflect.DeepEqual(total.RevokedSets, []string{b.SHA256}) {
		t.Fatalf("total revoke = %#v", total)
	}
	again, err := registry.Revoke(store)
	if err != nil {
		t.Fatal(err)
	}
	if again.Changed || again.RevokedSets == nil || len(again.RevokedSets) != 0 {
		t.Fatalf("repeated total revoke = %#v", again)
	}
}

func TestRevokeRejectsNonDirectHookNames(t *testing.T) {
	registry, store := registryFixture(t)
	for _, name := range []string{"", "../10-a.sh", "prepare-changeset/10-a.sh", "10-BAD.sh"} {
		if _, err := registry.Revoke(store, name); !errors.Is(err, ErrInvalidName) {
			t.Fatalf("Revoke(%q) error = %v, want ErrInvalidName", name, err)
		}
	}
}

func TestRegistryRejectsCorruptionAndUnsafePermissions(t *testing.T) {
	t.Run("malformed", func(t *testing.T) {
		registry, store := registryFixture(t)
		writeRegistryFixture(t, registry.Path(), []byte("{\n"), 0o600)
		if _, err := registry.List(store, EmptySet()); !errors.Is(err, ErrCorruptRegistry) {
			t.Fatalf("error = %v, want ErrCorruptRegistry", err)
		}
	})
	t.Run("unknown member", func(t *testing.T) {
		registry, store := registryFixture(t)
		writeRegistryFixture(t, registry.Path(), []byte("{\"version\":1,\"stores\":[],\"unknown\":true}\n"), 0o600)
		if _, err := registry.List(store, EmptySet()); !errors.Is(err, ErrCorruptRegistry) {
			t.Fatalf("error = %v, want ErrCorruptRegistry", err)
		}
	})
	t.Run("duplicate member", func(t *testing.T) {
		registry, store := registryFixture(t)
		writeRegistryFixture(t, registry.Path(), []byte("{\"version\":1,\"version\":1,\"stores\":[]}\n"), 0o600)
		if _, err := registry.List(store, EmptySet()); !errors.Is(err, ErrCorruptRegistry) {
			t.Fatalf("error = %v, want ErrCorruptRegistry", err)
		}
	})
	t.Run("digest mismatch", func(t *testing.T) {
		registry, store := registryFixture(t)
		set := mustSelect(t, map[string][]byte{"10-a.sh": []byte("#!/usr/bin/env sh\n")})
		if _, err := registry.Trust(store, set); err != nil {
			t.Fatal(err)
		}
		content, err := os.ReadFile(registry.Path())
		if err != nil {
			t.Fatal(err)
		}
		content = []byte(strings.Replace(string(content), set.SHA256, strings.Repeat("0", 64), 1))
		if err := os.WriteFile(registry.Path(), content, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := registry.List(store, set); !errors.Is(err, ErrCorruptRegistry) {
			t.Fatalf("error = %v, want ErrCorruptRegistry", err)
		}
	})
	t.Run("permissions", func(t *testing.T) {
		registry, store := registryFixture(t)
		writeRegistryFixture(t, registry.Path(), []byte("{\"version\":1,\"stores\":[]}\n"), 0o644)
		info, err := os.Stat(registry.Path())
		if err != nil {
			t.Fatal(err)
		}
		_, err = registry.List(store, EmptySet())
		if privateRegistryFileMode(info.Mode()) {
			if err != nil {
				t.Fatalf("platform considers permissions safe: %v", err)
			}
		} else if !errors.Is(err, ErrUnsafePermissions) {
			t.Fatalf("error = %v, want ErrUnsafePermissions", err)
		}
	})
}

func TestRegistryRejectsInStoreConfiguration(t *testing.T) {
	root := t.TempDir()
	store := filepath.Join(root, "store")
	if err := os.Mkdir(store, 0o700); err != nil {
		t.Fatal(err)
	}
	registry, err := NewRegistry(filepath.Join(store, ".engram", "controller-trust.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.List(store, EmptySet()); !errors.Is(err, ErrConfigInsideStore) {
		t.Fatalf("error = %v, want ErrConfigInsideStore", err)
	}
}

func TestRegistryDetectsCooperatingAndObservedConcurrentChanges(t *testing.T) {
	t.Run("existing lock", func(t *testing.T) {
		registry, store := registryFixture(t)
		if err := os.MkdirAll(filepath.Dir(registry.Path()), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(registry.Path()+".lock", nil, 0o600); err != nil {
			t.Fatal(err)
		}
		set := mustSelect(t, map[string][]byte{"10-a.sh": []byte("#!/usr/bin/env sh\n")})
		if _, err := registry.Trust(store, set); !errors.Is(err, ErrConcurrent) {
			t.Fatalf("error = %v, want ErrConcurrent", err)
		}
	})

	t.Run("cooperating writer", func(t *testing.T) {
		registry, store := registryFixture(t)
		other, err := NewRegistry(registry.Path())
		if err != nil {
			t.Fatal(err)
		}
		outerSet := mustSelect(t, map[string][]byte{"10-a.sh": []byte("#!/usr/bin/env sh\n")})
		innerSet := mustSelect(t, map[string][]byte{"20-b.sh": []byte("#!/usr/bin/env sh\n")})
		var innerErr error
		registry.beforePublish = func() {
			_, innerErr = other.Trust(store, innerSet)
		}
		outer, err := registry.Trust(store, outerSet)
		if err != nil || !outer.Changed {
			t.Fatalf("outer trust = %#v, %v", outer, err)
		}
		if !errors.Is(innerErr, ErrConcurrent) {
			t.Fatalf("inner error = %v, want ErrConcurrent", innerErr)
		}
	})

	t.Run("noncooperating replacement", func(t *testing.T) {
		registry, store := registryFixture(t)
		first := mustSelect(t, map[string][]byte{"10-a.sh": []byte("#!/usr/bin/env sh\n")})
		second := mustSelect(t, map[string][]byte{"20-b.sh": []byte("#!/usr/bin/env sh\n")})
		if _, err := registry.Trust(store, first); err != nil {
			t.Fatal(err)
		}
		replacement := []byte("{\"version\":1,\"stores\":[]}\n")
		registry.beforePublish = func() {
			if err := os.WriteFile(registry.Path(), replacement, 0o600); err != nil {
				t.Errorf("replace registry: %v", err)
			}
		}
		if _, err := registry.Trust(store, second); !errors.Is(err, ErrConcurrent) {
			t.Fatalf("error = %v, want ErrConcurrent", err)
		}
		content, err := os.ReadFile(registry.Path())
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(content, replacement) {
			t.Fatalf("concurrent replacement was overwritten: %s", content)
		}
	})
}

func TestRegistryReportsPostMutationEffects(t *testing.T) {
	fault := errors.New("injected registry fault")
	tests := []struct {
		name             string
		configure        func(*Registry)
		wantDurable      bool
		wantRecovery     bool
		wantLockResidual bool
	}{
		{
			name: "after rename",
			configure: func(registry *Registry) {
				registry.afterRename = func(string) error { return fault }
			},
		},
		{
			name: "after directory sync",
			configure: func(registry *Registry) {
				registry.afterSync = func(string) error { return fault }
			},
			wantDurable: true,
		},
		{
			name: "after release without residual lock",
			configure: func(registry *Registry) {
				registry.afterRelease = func(string) error { return fault }
			},
			wantDurable: true,
		},
		{
			name: "lock removal failure",
			configure: func(registry *Registry) {
				registry.beforeRemove = func(string) error { return fault }
			},
			wantDurable:      true,
			wantRecovery:     true,
			wantLockResidual: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			registry, store := registryFixture(t)
			set := mustSelect(t, map[string][]byte{"10-a.sh": []byte("#!/usr/bin/env sh\n")})
			test.configure(registry)
			selection, err := registry.Trust(store, set)
			if err == nil || selection.Changed || selection.SHA256 != "" || selection.Trusted || selection.Hooks != nil {
				t.Fatalf("selection=%#v error=%v", selection, err)
			}
			effect, ok := EffectOf(err)
			if !ok || effect.Durable != test.wantDurable || effect.RecoveryRequired != test.wantRecovery {
				t.Fatalf("effect=%#v present=%v error=%v", effect, ok, err)
			}
			_, lockErr := os.Lstat(registry.Path() + ".lock")
			if test.wantLockResidual && lockErr != nil {
				t.Fatalf("residual lock missing: %v", lockErr)
			}
			if !test.wantLockResidual && !errors.Is(lockErr, os.ErrNotExist) {
				t.Fatalf("unexpected residual lock: %v", lockErr)
			}
			if test.wantLockResidual {
				if err := os.Remove(registry.Path() + ".lock"); err != nil {
					t.Fatal(err)
				}
			}
			observed, err := registry.List(store, set)
			if err != nil || !observed.Trusted {
				t.Fatalf("published registry not observable: %#v %v", observed, err)
			}
		})
	}
}

func TestRegistryDoesNotInventEffectAfterUnchangedRelease(t *testing.T) {
	t.Parallel()
	registry, store := registryFixture(t)
	set := mustSelect(t, map[string][]byte{"10-a.sh": []byte("#!/usr/bin/env sh\n")})
	if _, err := registry.Trust(store, set); err != nil {
		t.Fatal(err)
	}
	fault := errors.New("injected release fault")
	registry.afterRelease = func(string) error { return fault }
	selection, err := registry.Trust(store, set)
	if !errors.Is(err, fault) || selection.Changed || selection.SHA256 != "" || selection.Trusted || selection.Hooks != nil {
		t.Fatalf("selection=%#v error=%v", selection, err)
	}
	if effect, ok := EffectOf(err); ok {
		t.Fatalf("invented effect=%#v error=%v", effect, err)
	}
	if _, statErr := os.Lstat(registry.Path() + ".lock"); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("lock remains: %v", statErr)
	}
}

func TestRegistryResidualLockWithoutPublicationIsNotDurable(t *testing.T) {
	t.Parallel()
	registry, store := registryFixture(t)
	set := mustSelect(t, map[string][]byte{"10-a.sh": []byte("#!/usr/bin/env sh\n")})
	if _, err := registry.Trust(store, set); err != nil {
		t.Fatal(err)
	}
	registry.beforeRemove = func(string) error { return errors.New("injected lock removal failure") }
	selection, err := registry.Trust(store, set)
	if err == nil || selection.Changed || selection.SHA256 != "" || selection.Trusted || selection.Hooks != nil {
		t.Fatalf("selection=%#v error=%v", selection, err)
	}
	effect, ok := EffectOf(err)
	if !ok || effect.Durable || !effect.RecoveryRequired {
		t.Fatalf("effect=%#v present=%v error=%v", effect, ok, err)
	}
}

func TestRegistryIdentityEstablishFailureLeavesRecoveryRequiredLock(t *testing.T) {
	t.Parallel()
	registry, store := registryFixture(t)
	set := mustSelect(t, map[string][]byte{"10-a.sh": []byte("#!/usr/bin/env sh\n")})
	fault := errors.New("injected identity establishment failure")
	registry.establishLockIdentity = func(*os.File) (lockidentity.Identity, error) {
		return lockidentity.Identity{}, fault
	}

	selection, err := registry.Trust(store, set)
	if !errors.Is(err, fault) || selection.Changed || selection.SHA256 != "" || selection.Trusted || selection.Hooks != nil {
		t.Fatalf("selection=%#v error=%v", selection, err)
	}
	effect, ok := EffectOf(err)
	if !ok || effect.Durable || !effect.RecoveryRequired {
		t.Fatalf("effect=%#v present=%v error=%v", effect, ok, err)
	}
	lockInfo, statErr := os.Lstat(registry.Path() + ".lock")
	if statErr != nil || !lockInfo.Mode().IsRegular() {
		t.Fatalf("recovery lock info=%#v error=%v", lockInfo, statErr)
	}
	if _, retryErr := acquireRegistryLock(registry.Path() + ".lock"); !errors.Is(retryErr, ErrConcurrent) {
		t.Fatalf("retry error=%v, want ErrConcurrent", retryErr)
	}
	if _, statErr := os.Lstat(registry.Path()); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("registry was published: %v", statErr)
	}
}

func TestRegistryLockReleaseDoesNotClaimANextOwnerAsResidual(t *testing.T) {
	t.Parallel()
	name := filepath.Join(t.TempDir(), "hook-trust-v1.json.lock")
	first, err := acquireRegistryLock(name)
	if err != nil {
		t.Fatal(err)
	}
	var next *registryLock
	residual, err := first.release(nil, func(string) error {
		var acquireErr error
		next, acquireErr = acquireRegistryLock(name)
		return acquireErr
	})
	if err != nil || residual || next == nil {
		t.Fatalf("residual=%v next=%#v error=%v", residual, next, err)
	}
	if _, err := next.release(nil, nil); err != nil {
		t.Fatal(err)
	}
}

func TestRegistryLockReleaseNeverUnlinksAReplacementOwner(t *testing.T) {
	t.Parallel()
	name := filepath.Join(t.TempDir(), "hook-trust-v1.json.lock")
	first, err := acquireRegistryLock(name)
	if err != nil {
		t.Fatal(err)
	}
	var next *registryLock
	residual, err := first.release(func(string) error {
		if removeErr := os.Remove(name); removeErr != nil {
			return removeErr
		}
		var acquireErr error
		next, acquireErr = acquireRegistryLock(name)
		return acquireErr
	}, nil)
	if err == nil || residual || next == nil || !errors.Is(err, ErrConcurrent) {
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

func TestRegistryRevokeCannotSucceedWithResidualLock(t *testing.T) {
	t.Parallel()
	registry, store := registryFixture(t)
	set := mustSelect(t, map[string][]byte{"10-a.sh": []byte("#!/usr/bin/env sh\n")})
	if _, err := registry.Trust(store, set); err != nil {
		t.Fatal(err)
	}
	registry.beforeRemove = func(string) error { return errors.New("injected lock removal failure") }
	result, err := registry.Revoke(store)
	if err == nil || result.Changed || result.RevokedSets != nil {
		t.Fatalf("result=%#v error=%v", result, err)
	}
	effect, ok := EffectOf(err)
	if !ok || !effect.Durable || !effect.RecoveryRequired {
		t.Fatalf("effect=%#v present=%v error=%v", effect, ok, err)
	}
	if _, err := os.Lstat(registry.Path() + ".lock"); err != nil {
		t.Fatalf("residual lock missing: %v", err)
	}
	if err := os.Remove(registry.Path() + ".lock"); err != nil {
		t.Fatal(err)
	}
	listed, err := registry.List(store, set)
	if err != nil || listed.Trusted {
		t.Fatalf("revocation was not published: %#v %v", listed, err)
	}
}

func registryFixture(t *testing.T) (*Registry, string) {
	t.Helper()
	root := t.TempDir()
	store := filepath.Join(root, "store")
	if err := os.Mkdir(store, 0o700); err != nil {
		t.Fatal(err)
	}
	registry, err := NewRegistry(filepath.Join(root, "controller", "hook-trust-v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	return registry, store
}

func writeRegistryFixture(t *testing.T, name string, content []byte, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, content, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(name, mode); err != nil {
		t.Fatal(err)
	}
}
