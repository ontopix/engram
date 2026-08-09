package snapshot

import (
	"errors"
	"reflect"
	"sort"
	"testing"
)

type memoryNode struct {
	kind Kind
	data []byte
}

type memorySource map[string]memoryNode

func (m memorySource) ReadDir(directory string) ([]Entry, error) {
	node, exists := m[directory]
	if !exists || node.kind != KindDirectory {
		return nil, errors.New("not a directory")
	}
	prefix := ""
	if directory != "." {
		prefix = directory + "/"
	}
	seen := map[string]Kind{}
	for name, child := range m {
		if name == directory || len(name) <= len(prefix) || name[:len(prefix)] != prefix {
			continue
		}
		remainder := name[len(prefix):]
		for index, character := range remainder {
			if character == '/' {
				remainder = remainder[:index]
				break
			}
		}
		if remainder != "" {
			if direct, ok := m[join(directory, remainder)]; ok {
				seen[remainder] = direct.kind
			} else {
				seen[remainder] = child.kind
			}
		}
	}
	result := make([]Entry, 0, len(seen))
	for name, kind := range seen {
		result = append(result, Entry{Name: name, Kind: kind})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}

func (m memorySource) ReadFile(name string) ([]byte, error) {
	node, exists := m[name]
	if !exists || node.kind != KindRegular {
		return nil, errors.New("not a file")
	}
	return append([]byte(nil), node.data...), nil
}

func TestTraversalPrecedenceAndPruning(t *testing.T) {
	t.Parallel()
	source := memorySource{
		".":                               {kind: KindDirectory},
		"README.md":                       {kind: KindRegular, data: []byte("map\n")},
		".engram":                         {kind: KindDirectory},
		".engram/root.yaml":               {kind: KindRegular, data: []byte("engram: 1\n")},
		".engram/schemas":                 {kind: KindDirectory},
		".engram/schemas/note.md":         {kind: KindRegular, data: []byte("schema\n")},
		".engram/schemas/BAD.md":          {kind: KindSymlink},
		".engram/hooks":                   {kind: KindDirectory},
		".engram/hooks/prepare-changeset": {kind: KindDirectory},
		".engram/hooks/prepare-changeset/20-ok.sh": {kind: KindRegular, data: []byte("#!/usr/bin/env sh\n")},
		".secret":              {kind: KindSymlink},
		"bad name":             {kind: KindSymlink},
		"nested":               {kind: KindDirectory},
		"nested/README.md":     {kind: KindRegular, data: []byte("map\n")},
		"nested/.git":          {kind: KindDirectory},
		"nested/.engram":       {kind: KindDirectory},
		"nested/.engram/hooks": {kind: KindSymlink},
		"record.md":            {kind: KindRegular, data: []byte("record\n")},
	}
	tree, err := Load(source)
	if err != nil {
		t.Fatal(err)
	}
	wantIssues := []Issue{
		{Code: "E107", Path: "."},
		{Code: "E303", Path: ".engram/schemas"},
		{Code: "E109", Path: "nested/.engram/hooks"},
		{Code: "E110", Path: "nested/.git"},
	}
	if !reflect.DeepEqual(tree.Issues, wantIssues) {
		t.Fatalf("issues = %#v, want %#v", tree.Issues, wantIssues)
	}
	if _, exists := tree.Files["bad name"]; exists {
		t.Fatal("invalid-name boundary was not pruned")
	}
	if tree.Files["record.md"].Role != RoleRecord || tree.Files["README.md"].Role != RoleMap {
		t.Fatalf("unexpected file roles: %#v", tree.Files)
	}
}

func TestUnicodeCollisionAndAdvisoryHelpers(t *testing.T) {
	t.Parallel()
	source := memorySource{
		".":          {kind: KindDirectory},
		"STRASSE.md": {kind: KindRegular},
		"straße.md":  {kind: KindRegular},
	}
	tree, err := Load(source)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(tree.Issues, []Issue{{Code: "E106", Path: "."}}) {
		t.Fatalf("issues = %#v", tree.Issues)
	}
	if ValidContentName("cafe\u0301.md") || !ValidContentName("café.md") {
		t.Fatal("NFC validation is incorrect")
	}
	if ValidTypeSlug("bad--type") || !ValidTypeSlug("good-type2") {
		t.Fatal("type slug validation is incorrect")
	}
}
