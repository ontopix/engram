// Package skills embeds the byte-identical canonical Agent Skills and their
// closed release manifest for trusted, skills-only runtime adapters.
package skills

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/ontopix/engram/internal/yamlprofile"
)

//go:embed */SKILL.md manifest-v1.json
var files embed.FS

type Entry struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

type Manifest struct {
	Version int     `json:"version"`
	Format  string  `json:"format"`
	Skills  []Entry `json:"skills"`
}

// FS returns the immutable canonical bundle. Callers install only the paths
// listed by Manifest; no executable, attachment, writer, or network adapter is
// included.
func FS() fs.FS { return files }

// VerifiedManifest parses the closed canonical manifest and proves every
// listed artifact digest and name/path binding before returning it.
func VerifiedManifest() (Manifest, error) {
	raw, err := files.ReadFile("manifest-v1.json")
	if err != nil {
		return Manifest{}, err
	}
	if err := rejectDuplicateJSONFields(raw); err != nil {
		return Manifest{}, err
	}
	var manifest Manifest
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return Manifest{}, errors.Join(err, errors.New("canonical skill manifest is malformed"))
	}
	if manifest.Version != 1 || manifest.Format != "agentskills.io" || len(manifest.Skills) != 5 {
		return Manifest{}, errors.New("canonical skill manifest header differs")
	}
	previous := ""
	for _, entry := range manifest.Skills {
		if entry.Name == "" || previous >= entry.Name || entry.Path != entry.Name+"/SKILL.md" || len(entry.SHA256) != 64 || strings.Contains(entry.Path, "\\") {
			return Manifest{}, errors.New("canonical skill manifest entry differs")
		}
		data, err := files.ReadFile(entry.Path)
		if err != nil {
			return Manifest{}, err
		}
		digest := sha256.Sum256(data)
		if hex.EncodeToString(digest[:]) != entry.SHA256 {
			return Manifest{}, errors.New("canonical skill artifact digest differs")
		}
		if err := validateSkillArtifact(data, entry.Name); err != nil {
			return Manifest{}, fmt.Errorf("canonical skill %s: %w", entry.Name, err)
		}
		previous = entry.Name
	}
	entries, err := fs.Glob(files, "*/SKILL.md")
	if err != nil {
		return Manifest{}, err
	}
	sort.Strings(entries)
	if len(entries) != len(manifest.Skills) {
		return Manifest{}, errors.New("unlisted canonical skill artifact")
	}
	for index := range entries {
		if entries[index] != manifest.Skills[index].Path {
			return Manifest{}, errors.New("canonical skill manifest is not complete")
		}
	}
	return cloneManifest(manifest), nil
}

func validateSkillArtifact(data []byte, expectedName string) error {
	if !utf8.Valid(data) || bytes.ContainsRune(data, '\r') || !bytes.HasPrefix(data, []byte("---\n")) {
		return errors.New("artifact must be UTF-8 with LF-delimited frontmatter")
	}
	remainder := data[len("---\n"):]
	boundary := bytes.Index(remainder, []byte("\n---\n"))
	if boundary < 0 || boundary+len("\n---\n") == len(remainder) {
		return errors.New("artifact must contain closed frontmatter and a body")
	}
	document, err := yamlprofile.Parse(append(bytes.Clone(remainder[:boundary]), '\n'))
	if err != nil {
		return fmt.Errorf("invalid frontmatter: %w", err)
	}
	if len(document.Root.Mapping) != 2 {
		return errors.New("frontmatter must contain exactly name and description")
	}
	name, nameOK := document.Root.Lookup("name")
	description, descriptionOK := document.Root.Lookup("description")
	if !nameOK || name.Kind != yamlprofile.StringKind || name.String != expectedName || !validSkillName(name.String) {
		return errors.New("frontmatter name differs from its manifest and directory binding")
	}
	if !descriptionOK || description.Kind != yamlprofile.StringKind || yamlprofile.TrimASCIIWhitespace(description.String) == "" || utf8.RuneCountInString(description.String) > 1024 {
		return errors.New("frontmatter description must contain 1 through 1024 characters")
	}
	return nil
}

func validSkillName(value string) bool {
	if len(value) == 0 || len(value) > 64 || value[0] == '-' || value[len(value)-1] == '-' || strings.Contains(value, "--") {
		return false
	}
	for _, character := range []byte(value) {
		if character != '-' && (character < 'a' || character > 'z') && (character < '0' || character > '9') {
			return false
		}
	}
	return true
}

func rejectDuplicateJSONFields(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := walkJSONValue(decoder, "$"); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("canonical skill manifest contains more than one JSON value")
		}
		return fmt.Errorf("canonical skill manifest has invalid trailing JSON: %w", err)
	}
	return nil
}

func walkJSONValue(decoder *json.Decoder, location string) error {
	token, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("invalid JSON at %s: %w", location, err)
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			fieldToken, fieldErr := decoder.Token()
			field, fieldOK := fieldToken.(string)
			if fieldErr != nil || !fieldOK {
				return errors.New("canonical skill manifest has an invalid JSON object")
			}
			if _, duplicate := seen[field]; duplicate {
				return fmt.Errorf("duplicate JSON field %q at %s", field, location)
			}
			seen[field] = struct{}{}
			if err := walkJSONValue(decoder, location+"."+field); err != nil {
				return err
			}
		}
		end, endErr := decoder.Token()
		if endErr != nil || end != json.Delim('}') {
			return errors.New("canonical skill manifest has an invalid JSON object")
		}
	case '[':
		for index := 0; decoder.More(); index++ {
			if err := walkJSONValue(decoder, fmt.Sprintf("%s[%d]", location, index)); err != nil {
				return err
			}
		}
		end, endErr := decoder.Token()
		if endErr != nil || end != json.Delim(']') {
			return errors.New("canonical skill manifest has an invalid JSON array")
		}
	default:
		return fmt.Errorf("canonical skill manifest has invalid JSON delimiter %q", delimiter)
	}
	return nil
}

func cloneManifest(value Manifest) Manifest {
	value.Skills = append([]Entry(nil), value.Skills...)
	return value
}
