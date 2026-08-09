package hooks

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
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
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("registry mode = %o, want 600", got)
	}
	parentInfo, err := os.Stat(filepath.Dir(registry.Path()))
	if err != nil {
		t.Fatal(err)
	}
	if got := parentInfo.Mode().Perm(); got&0o077 != 0 {
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
		if _, err := registry.Revoke(store, name); err == nil {
			t.Fatalf("Revoke(%q) succeeded", name)
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
		if _, err := registry.List(store, EmptySet()); !errors.Is(err, ErrUnsafePermissions) {
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
