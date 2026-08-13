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
		".engram/routines":                         {kind: KindDirectory},
		".engram/routines/daily-journal.md":        {kind: KindRegular, data: []byte("routine\n")},
		".secret":                                  {kind: KindSymlink},
		"bad name":                                 {kind: KindSymlink},
		"nested":                                   {kind: KindDirectory},
		"nested/README.md":                         {kind: KindRegular, data: []byte("map\n")},
		"nested/.git":                              {kind: KindDirectory},
		"nested/.engram":                           {kind: KindDirectory},
		"nested/.engram/hooks":                     {kind: KindSymlink},
		"local":                                    {kind: KindDirectory},
		"local/README.md":                          {kind: KindRegular, data: []byte("map\n")},
		"local/.engram":                            {kind: KindDirectory},
		"local/.engram/hooks":                      {kind: KindDirectory},
		"local/.engram/hooks/prepare-changeset":    {kind: KindDirectory},
		"local/.engram/hooks/prepare-changeset/10-ok.sh": {kind: KindRegular, data: []byte("#!/usr/bin/env sh\n")},
		"local/.engram/routines":                         {kind: KindDirectory},
		"local/.engram/routines/weekly-review.md":        {kind: KindRegular, data: []byte("routine\n")},
		"record.md": {kind: KindRegular, data: []byte("record\n")},
	}
	tree, err := Load(source)
	if err != nil {
		t.Fatal(err)
	}
	wantIssues := []Issue{
		{Code: "E107", Path: "."},
		{Code: "E303", Path: ".engram/schemas"},
		{Code: "E103", Path: "nested/.engram/hooks"},
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
	if tree.Files["local/.engram/hooks/prepare-changeset/10-ok.sh"].Role != RoleHook {
		t.Fatalf("local hook was not projected: %#v", tree.Files)
	}
	if tree.Files["local/.engram/routines/weekly-review.md"].Role != RoleRoutine {
		t.Fatalf("local routine was not projected: %#v", tree.Files)
	}
}

func TestRoutineTreeIsClosed(t *testing.T) {
	t.Parallel()
	source := memorySource{
		".":                              {kind: KindDirectory},
		".engram":                        {kind: KindDirectory},
		".engram/routines":               {kind: KindDirectory},
		".engram/routines/daily.md":      {kind: KindRegular, data: []byte("routine\n")},
		".engram/routines/invalid.txt":   {kind: KindRegular},
		".engram/routines/nested":        {kind: KindDirectory},
		".engram/routines/wrong-kind.md": {kind: KindDirectory},
		".engram/routines/symlinked.md":  {kind: KindSymlink},
	}
	tree, err := Load(source)
	if err != nil {
		t.Fatal(err)
	}
	wantIssues := []Issue{
		{Code: "E309", Path: ".engram/routines"},
		{Code: "E103", Path: ".engram/routines/symlinked.md"},
		{Code: "E309", Path: ".engram/routines/wrong-kind.md"},
	}
	if !reflect.DeepEqual(tree.Issues, wantIssues) {
		t.Fatalf("issues = %#v, want %#v", tree.Issues, wantIssues)
	}
	if file, exists := tree.Files[".engram/routines/daily.md"]; !exists || file.Role != RoleRoutine {
		t.Fatalf("routine = %#v, exists = %t", file, exists)
	}
	for _, logicalPath := range []string{".engram/routines/invalid.txt", ".engram/routines/nested", ".engram/routines/wrong-kind.md", ".engram/routines/symlinked.md"} {
		if _, exists := tree.Files[logicalPath]; exists {
			t.Fatalf("closed routine tree projected %q", logicalPath)
		}
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
