package checker

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"
)

type SchemaDescription struct {
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
	winners := make(map[string]*Schema)
	for scope := at; ; scope = parentScope(scope) {
		prefix := joinScope(scope, ".engram/schemas") + "/"
		for name, definition := range s.Schemas {
			if path.Dir(name)+"/" != prefix {
				continue
			}
			if _, exists := winners[definition.Type]; !exists && definition.Type != "" {
				winners[definition.Type] = definition
			}
		}
		if scope == "." {
			break
		}
	}
	result := make([]SchemaDescription, 0, len(winners))
	for typeName, definition := range winners {
		if !s.conformingSchema(definition) {
			return nil, fmt.Errorf("selected schema %q is not conforming", typeName)
		}
		pathValue := definition.Path
		result = append(result, SchemaDescription{
			Type: typeName, Source: "local", Path: &pathValue,
			Version: json.Number(definition.Version.CanonicalJSON()), Description: definition.Description,
		})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Type != result[j].Type {
			return bytes.Compare([]byte(result[i].Type), []byte(result[j].Type)) < 0
		}
		return *result[i].Path < *result[j].Path
	})
	return result, nil
}

// ShowSchema resolves typeName at at and returns its exact UTF-8 source.
func (s *Snapshot) ShowSchema(at, typeName string) (SchemaDescription, string, error) {
	if !documentTypeSlug(typeName) {
		return SchemaDescription{}, "", fmt.Errorf("invalid schema type %q", typeName)
	}
	visible, err := s.VisibleSchemas(at)
	if err != nil {
		return SchemaDescription{}, "", err
	}
	for _, description := range visible {
		if description.Type != typeName {
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

func documentTypeSlug(value string) bool {
	if value == "" || value[0] == '-' || value[len(value)-1] == '-' || len(value) > 64 || strings.Contains(value, "--") {
		return false
	}
	for index := range len(value) {
		character := value[index]
		if character != '-' && (character < 'a' || character > 'z') && (character < '0' || character > '9') {
			return false
		}
	}
	return true
}
