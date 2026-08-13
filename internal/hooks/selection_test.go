package hooks

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/ontopix/engram/internal/changeset"
	"github.com/ontopix/engram/internal/snapshot"
)

func TestEmptySetHasStableIdentity(t *testing.T) {
	set := EmptySet()
	if set.Hooks == nil || len(set.Hooks) != 0 {
		t.Fatalf("empty hooks = %#v, want non-nil empty slice", set.Hooks)
	}
	canonical, err := set.CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(canonical), `{"version":1,"hooks":[]}`; got != want {
		t.Fatalf("canonical bytes = %q, want %q", got, want)
	}
	digest := sha256.Sum256(canonical)
	if got, want := set.SHA256, hex.EncodeToString(digest[:]); got != want {
		t.Fatalf("set digest = %q, want %q", got, want)
	}
}

func TestSelectTreeOrdersAndDescribesCompleteSet(t *testing.T) {
	programs := map[string][]byte{
		"30-z.sh":      []byte("#!/usr/bin/env sh\nprintf z\n"),
		"10-alpha.py":  []byte("#!/usr/bin/env python3\nprint('a')\n"),
		"10-alpha2.js": []byte("#!/usr/bin/env node\n"),
	}
	set := mustSelect(t, programs)
	wantPaths := []string{
		programDirectory + "/10-alpha.py",
		programDirectory + "/10-alpha2.js",
		programDirectory + "/30-z.sh",
	}
	wantInterpreters := []string{"python3", "node", "sh"}
	for index, hook := range set.Hooks {
		if hook.Path != wantPaths[index] || hook.Interpreter != wantInterpreters[index] {
			t.Fatalf("hook %d = %#v", index, hook)
		}
		name := hook.Path[len(programDirectory)+1:]
		digest := sha256.Sum256(programs[name])
		if hook.SHA256 != hex.EncodeToString(digest[:]) {
			t.Fatalf("hook %d digest = %q", index, hook.SHA256)
		}
	}
	if err := set.Valid(); err != nil {
		t.Fatalf("selected set is invalid: %v", err)
	}

	programs["10-alpha.py"][0] = 'X'
	if set.Hooks[0].Bytes[0] != '#' {
		t.Fatal("selected program bytes alias source bytes")
	}
	set.Hooks[0].Bytes[0] = 'Y'
	if err := set.Valid(); !errors.Is(err, ErrInvalidSelection) {
		t.Fatalf("mutated set error = %v, want ErrInvalidSelection", err)
	}
}

func TestSelectSourceTraversesCompleteHookDirectory(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, ".engram", "hooks", "prepare-changeset")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{
		"20-second.sh": []byte("#!/usr/bin/env sh\n"),
		"10-first.py":  []byte("#!/usr/bin/env python3\n"),
	} {
		if err := os.WriteFile(filepath.Join(directory, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	source, err := snapshot.OpenFS(root)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	set, err := SelectSource(source)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := []string{set.Hooks[0].Path, set.Hooks[1].Path}, []string{programDirectory + "/10-first.py", programDirectory + "/20-second.sh"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("paths = %#v, want %#v", got, want)
	}
}

func TestSelectTreeRejectsInvalidBytesInterpreterAndLayoutWithoutPartialSet(t *testing.T) {
	tests := []struct {
		name string
		tree *snapshot.Tree
		code string
	}{
		{
			name: "normed text",
			tree: hookTree(map[string][]byte{"10-bad.sh": []byte("#!/usr/bin/env sh\r\n")}),
			code: "E108",
		},
		{
			name: "interpreter",
			tree: hookTree(map[string][]byte{"10-bad.sh": []byte("#!/usr/bin/env sh -e\n")}),
			code: "E308",
		},
		{
			name: "layout issue",
			tree: &snapshot.Tree{
				Files: map[string]snapshot.File{
					programDirectory + "/10-good.sh": {
						Path: programDirectory + "/10-good.sh", Role: snapshot.RoleHook, Data: []byte("#!/usr/bin/env sh\n"),
					},
				},
				Issues: []snapshot.Issue{{Code: "E308", Path: programDirectory}},
			},
			code: "E308",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			set, err := SelectTree(test.tree)
			if !errors.Is(err, ErrInvalidSelection) {
				t.Fatalf("error = %v, want ErrInvalidSelection", err)
			}
			var selectionError *SelectionError
			if !errors.As(err, &selectionError) || selectionError.Code != test.code {
				t.Fatalf("selection error = %#v, want code %s", selectionError, test.code)
			}
			if !reflect.DeepEqual(set, Set{}) {
				t.Fatalf("partial set returned: %#v", set)
			}
		})
	}
}

func TestAddDeleteRenameAndByteChangeProduceDistinctSets(t *testing.T) {
	base := mustSelect(t, map[string][]byte{
		"10-a.sh": []byte("#!/usr/bin/env sh\n"),
		"20-b.sh": []byte("#!/usr/bin/env sh\n"),
	})
	variants := map[string]Set{
		"add": mustSelect(t, map[string][]byte{
			"10-a.sh": []byte("#!/usr/bin/env sh\n"),
			"20-b.sh": []byte("#!/usr/bin/env sh\n"),
			"30-c.sh": []byte("#!/usr/bin/env sh\n"),
		}),
		"delete": mustSelect(t, map[string][]byte{
			"10-a.sh": []byte("#!/usr/bin/env sh\n"),
		}),
		"rename": mustSelect(t, map[string][]byte{
			"10-renamed.sh": []byte("#!/usr/bin/env sh\n"),
			"20-b.sh":       []byte("#!/usr/bin/env sh\n"),
		}),
		"bytes": mustSelect(t, map[string][]byte{
			"10-a.sh": []byte("#!/usr/bin/env sh\nprintf changed\n"),
			"20-b.sh": []byte("#!/usr/bin/env sh\n"),
		}),
	}
	seen := map[string]string{base.SHA256: "base"}
	for name, variant := range variants {
		if previous, exists := seen[variant.SHA256]; exists {
			t.Fatalf("%s and %s have the same set digest %s", name, previous, variant.SHA256)
		}
		seen[variant.SHA256] = name
	}
}

func TestSelectTreeForChangesUsesHierarchicalScopesAndStableOrder(t *testing.T) {
	tree := hookTreePaths(map[string][]byte{
		".engram/hooks/prepare-changeset/90-global.sh":              []byte("#!/usr/bin/env sh\n"),
		"journal/.engram/hooks/prepare-changeset/10-journal.sh":     []byte("#!/usr/bin/env sh\n"),
		"journal/daily/.engram/hooks/prepare-changeset/20-daily.sh": []byte("#!/usr/bin/env sh\n"),
		"people/.engram/hooks/prepare-changeset/10-people.sh":       []byte("#!/usr/bin/env sh\n"),
	})
	tests := []struct {
		name    string
		changes []changeset.Change
		want    []string
	}{
		{
			name:    "daily selects root parent and child",
			changes: []changeset.Change{{Operation: changeset.Modified, Path: "journal/daily/2026-08-13.md"}},
			want: []string{
				"journal/.engram/hooks/prepare-changeset/10-journal.sh",
				"journal/daily/.engram/hooks/prepare-changeset/20-daily.sh",
				".engram/hooks/prepare-changeset/90-global.sh",
			},
		},
		{
			name:    "parent excludes child and sibling",
			changes: []changeset.Change{{Operation: changeset.Modified, Path: "journal/README.md"}},
			want: []string{
				"journal/.engram/hooks/prepare-changeset/10-journal.sh",
				".engram/hooks/prepare-changeset/90-global.sh",
			},
		},
		{
			name: "movement activates source and destination",
			changes: []changeset.Change{
				{Operation: changeset.Deleted, Path: "journal/daily/moved.md"},
				{Operation: changeset.Added, Path: "people/moved.md"},
			},
			want: []string{
				"journal/.engram/hooks/prepare-changeset/10-journal.sh",
				"people/.engram/hooks/prepare-changeset/10-people.sh",
				"journal/daily/.engram/hooks/prepare-changeset/20-daily.sh",
				".engram/hooks/prepare-changeset/90-global.sh",
			},
		},
		{
			name:    "hook change affects containing scope",
			changes: []changeset.Change{{Operation: changeset.Modified, Path: "people/.engram/hooks/prepare-changeset/10-people.sh"}},
			want: []string{
				"people/.engram/hooks/prepare-changeset/10-people.sh",
				".engram/hooks/prepare-changeset/90-global.sh",
			},
		},
		{
			name:    "routine declaration affects containing scope",
			changes: []changeset.Change{{Operation: changeset.Modified, Path: "journal/.engram/routines/daily-journal.md"}},
			want: []string{
				"journal/.engram/hooks/prepare-changeset/10-journal.sh",
				".engram/hooks/prepare-changeset/90-global.sh",
			},
		},
		{name: "empty selects none", changes: []changeset.Change{}, want: []string{}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			set, err := SelectTreeForChanges(tree, test.changes)
			if err != nil {
				t.Fatal(err)
			}
			got := make([]string, len(set.Hooks))
			for index, hook := range set.Hooks {
				got[index] = hook.Path
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("paths = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestSelectTreeForChangesValidatesUnselectedHookTree(t *testing.T) {
	tree := hookTreePaths(map[string][]byte{
		".engram/hooks/prepare-changeset/90-global.sh":        []byte("#!/usr/bin/env sh\n"),
		"people/.engram/hooks/prepare-changeset/10-people.sh": []byte("#!/usr/bin/env sh -e\n"),
	})
	set, err := SelectTreeForChanges(tree, []changeset.Change{{Operation: changeset.Modified, Path: "journal/entry.md"}})
	if !errors.Is(err, ErrInvalidSelection) || !reflect.DeepEqual(set, Set{}) {
		t.Fatalf("set, error = %#v, %v", set, err)
	}
}

func mustSelect(t *testing.T, programs map[string][]byte) Set {
	t.Helper()
	set, err := SelectTree(hookTree(programs))
	if err != nil {
		t.Fatal(err)
	}
	return set
}

func hookTree(programs map[string][]byte) *snapshot.Tree {
	paths := make(map[string][]byte, len(programs))
	for name, data := range programs {
		paths[programDirectory+"/"+name] = data
	}
	return hookTreePaths(paths)
}

func hookTreePaths(programs map[string][]byte) *snapshot.Tree {
	files := make(map[string]snapshot.File, len(programs))
	for logicalPath, data := range programs {
		files[logicalPath] = snapshot.File{Path: logicalPath, Role: snapshot.RoleHook, Data: data}
	}
	return &snapshot.Tree{Files: files}
}
