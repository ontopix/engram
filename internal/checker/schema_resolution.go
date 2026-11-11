package checker

import (
	"path"

	"github.com/ontopix/engram/internal/snapshot"
	"github.com/ontopix/engram/internal/typeref"
)

func schemaFamilies(tree *snapshot.Tree) map[string]map[string]struct{} {
	families := make(map[string]map[string]struct{})
	for name := range tree.Boundaries {
		directory := path.Dir(name)
		if path.Base(directory) != "schemas" || path.Base(path.Dir(directory)) != ".engram" {
			continue
		}
		ref, ok := typeref.Filename(path.Base(name))
		if !ok {
			continue
		}
		if families[directory] == nil {
			families[directory] = make(map[string]struct{})
		}
		families[directory][ref.Name] = struct{}{}
	}
	return families
}

// The first owning family stops resolution even if the requested revision is absent.
func resolveSchemaPath(tree *snapshot.Tree, families map[string]map[string]struct{}, directory, reference string) (selected string, family, available bool) {
	ref, ok := typeref.Parse(reference)
	if !ok {
		return "", false, false
	}
	for scope := directory; ; scope = parentScope(scope) {
		config := joinScope(scope, ".engram")
		if kind, exists := tree.Boundaries[config]; exists {
			if kind != snapshot.KindDirectory {
				return "", false, false
			}
			schemas := joinScope(scope, ".engram/schemas")
			if kind, exists := tree.Boundaries[schemas]; exists {
				if kind != snapshot.KindDirectory {
					return "", false, false
				}
				if _, owns := families[schemas][ref.Name]; owns {
					return schemas + "/" + reference + ".md", true, true
				}
			}
		}
		if scope == "." {
			break
		}
	}
	return "", false, true
}
