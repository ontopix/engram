// Package schemas embeds the curated, non-normative schema inventory shipped
// with the reference CLI.
package schemas

import (
	"embed"
	"fmt"
	"sort"
	"strconv"

	"github.com/ontopix/engram/internal/documentprofile"
)

//go:embed note.md fact.md person.md project.md journal-entry.md
var inventoryFS embed.FS

var inventoryNames = []string{"note.md", "fact.md", "person.md", "project.md", "journal-entry.md"}

type Entry struct {
	Type        string
	Version     int64
	Description string
	Content     string
}

// Inventory parses and returns independent copies of all embedded entries in
// type order. A packaging drift is returned as an error rather than silently
// publishing malformed inventory metadata.
func Inventory() ([]Entry, error) {
	result := make([]Entry, 0, len(inventoryNames))
	for _, filename := range inventoryNames {
		data, err := inventoryFS.ReadFile(filename)
		if err != nil {
			return nil, fmt.Errorf("read embedded schema %s: %w", filename, err)
		}
		document, err := documentprofile.Parse(data)
		if err != nil {
			return nil, fmt.Errorf("parse embedded schema %s: %w", filename, err)
		}
		typeName, err := documentprofile.Type(document.YAML.Root)
		if err != nil {
			return nil, fmt.Errorf("embedded schema %s type: %w", filename, err)
		}
		description, err := documentprofile.Description(document.YAML.Root)
		if err != nil {
			return nil, fmt.Errorf("embedded schema %s description: %w", filename, err)
		}
		version, exists := document.YAML.Root.Lookup("version")
		if !exists || version.Number == nil || !version.Number.IsIntegerInRange(1, 1<<63-1) {
			return nil, fmt.Errorf("embedded schema %s has unsupported version", filename)
		}
		versionValue, err := strconv.ParseInt(version.Number.CanonicalJSON(), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("embedded schema %s version: %w", filename, err)
		}
		result = append(result, Entry{Type: typeName, Version: versionValue, Description: description, Content: string(data)})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Type < result[j].Type })
	return result, nil
}
