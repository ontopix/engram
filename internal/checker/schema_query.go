package checker

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/ontopix/engram/internal/snapshot"
	"github.com/ontopix/engram/internal/typeref"
)

type SchemaDescription struct {
	Reference   string      `json:"reference"`
	Scope       *string     `json:"scope"`
	Type        string      `json:"type"`
	Source      string      `json:"source"`
	Path        *string     `json:"path"`
	Version     json.Number `json:"version"`
	Description string      `json:"description"`
}

// VisibleSchemas returns the winning conforming local schemas at one content
// directory. Invalid selected schema state fails atomically.
func (s *Snapshot) VisibleSchemas(at string) ([]SchemaDescription, error) {
	if s == nil || s.Tree == nil {
		return nil, fmt.Errorf("snapshot is nil")
	}
	at, err := normalizeDirectory(at)
	if err != nil || !containsString(s.Tree.Directories, at) {
		return nil, fmt.Errorf("schema query path is not a content directory")
	}

	winners := make(map[string]string)
	for scope := at; ; scope = parentScope(scope) {
		config := joinScope(scope, ".engram")
		directory := config + "/schemas"
		for _, boundary := range []string{config, directory} {
			if kind, exists := s.Tree.Boundaries[boundary]; exists && kind != snapshot.KindDirectory {
				return nil, fmt.Errorf("unavailable schema boundary %q", boundary)
			}
		}
		for _, finding := range s.Validation.Findings {
			if finding.Path == directory && len(finding.Code) > 0 && finding.Code[0] == 'E' {
				return nil, fmt.Errorf("invalid schema directory %q", directory)
			}
		}
		for name := range s.families[directory] {
			if _, exists := winners[name]; !exists {
				winners[name] = directory
			}
		}
		if scope == "." {
			break
		}
	}
	result := make([]SchemaDescription, 0)
	for name := range s.Tree.Boundaries {
		ref, ok := typeref.Filename(path.Base(name))
		if !ok || winners[ref.Name] != path.Dir(name) {
			continue
		}
		definition := s.Schemas[name]
		if !s.conformingSchema(definition) {
			return nil, fmt.Errorf("selected schema %q is not conforming", name)
		}
		pathValue, scopeValue := definition.Path, definition.Scope
		result = append(result, SchemaDescription{Type: ref.Name, Reference: ref.String(), Scope: &scopeValue, Source: "local", Path: &pathValue, Version: json.Number(ref.Version), Description: definition.Description})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Type != result[j].Type {
			return bytes.Compare([]byte(result[i].Type), []byte(result[j].Type)) < 0
		}
		return typeref.CompareVersions(result[i].Version.String(), result[j].Version.String()) < 0
	})

	return result, nil
}

// ShowSchema resolves typeName at at and returns its exact UTF-8 source.
func (s *Snapshot) ShowSchema(at, typeName string) (SchemaDescription, string, error) {
	if !typeref.Valid(typeName) {
		return SchemaDescription{}, "", fmt.Errorf("invalid schema type %q", typeName)
	}
	visible, err := s.VisibleSchemas(at)
	if err != nil {
		return SchemaDescription{}, "", err
	}
	for _, description := range visible {
		if description.Reference != typeName {
			continue
		}
		file, ok := s.Tree.Files[*description.Path]
		if !ok {
			return SchemaDescription{}, "", fmt.Errorf("resolved schema bytes are unavailable")
		}
		return description, string(file.Data), nil
	}
	return SchemaDescription{}, "", fmt.Errorf("schema type %q is not visible", typeName)
}

func (s *Snapshot) conformingSchema(definition *Schema) bool {
	if definition == nil || !definition.Valid || !definition.SchemaValid || !definition.BodyValid || !definition.PolicyValid {
		return false
	}
	for _, finding := range s.Validation.Findings {
		if finding.Path == definition.Path && len(finding.Code) != 0 && finding.Code[0] == 'E' {
			return false
		}
	}
	return true
}

func normalizeDirectory(value string) (string, error) {
	if value == "" || value == "." {
		return ".", nil
	}
	if strings.HasPrefix(value, "/") || strings.Contains(value, "\\") || path.Clean(value) != value || value == ".." || strings.HasPrefix(value, "../") {
		return "", fmt.Errorf("invalid logical directory")
	}
	return value, nil
}
