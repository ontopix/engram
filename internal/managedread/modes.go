package managedread

import (
	"context"
	"fmt"
	"path"

	"github.com/ontopix/engram/internal/checker"
	"github.com/ontopix/engram/internal/gitraw"
)

func logicalRegularModes(ctx context.Context, reader gitraw.Reader, root gitraw.OID, projected *checker.Snapshot) (map[string]gitraw.TreeMode, error) {
	result := make(map[string]gitraw.TreeMode)
	if projected == nil || projected.Tree == nil {
		return result, nil
	}
	// Snapshot.Tree.Directories enumerates content directories, while regular
	// logical files can also live below traversed configuration directories.
	// Derive the exact raw-tree prefixes from the files whose modes matter so
	// .engram schemas and hooks preserve their accepted modes too.
	directories := map[string]struct{}{".": {}}
	for name := range projected.Tree.Files {
		for directory := path.Dir(name); ; directory = path.Dir(directory) {
			directories[directory] = struct{}{}
			if directory == "." {
				break
			}
		}
	}
	var walk func(gitraw.OID, string) error
	walk = func(treeID gitraw.OID, directory string) error {
		object, err := reader.ReadObject(ctx, treeID)
		if err != nil {
			return err
		}
		if object.Type != gitraw.TypeTree {
			return &gitraw.Error{
				Kind:   gitraw.FailureWrongType,
				Op:     "read-mode-tree",
				OID:    treeID,
				Detail: fmt.Sprintf("got %s, want %s", object.Type, gitraw.TypeTree),
			}
		}
		entries, err := gitraw.ParseTree(treeID.Format(), object.Data)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			name := string(entry.Name)
			child := name
			if directory != "." {
				child = path.Join(directory, name)
			}
			if _, exists := projected.Tree.Files[child]; exists && entry.Mode.IsRegular() {
				result[child] = entry.Mode
				continue
			}
			if _, traversed := directories[child]; traversed && entry.Mode == gitraw.ModeDirectory {
				if err := walk(entry.OID, child); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := walk(root, "."); err != nil {
		return nil, err
	}
	return result, nil
}
