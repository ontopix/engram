package documentprofile

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/ontopix/engram/internal/snapshot"
	"github.com/ontopix/engram/internal/yamlprofile"
)

// DescriptionProblem identifies a universal-description violation.
type DescriptionProblem string

const (
	DescriptionInvalidUTF8 DescriptionProblem = "invalid-utf8"
	DescriptionLength      DescriptionProblem = "length"
	DescriptionEdgeSpace   DescriptionProblem = "edge-space"
	DescriptionCharacter   DescriptionProblem = "forbidden-character"
)

// DescriptionError describes a failed §4.2 description constraint.
type DescriptionError struct {
	Problem DescriptionProblem
	Index   int
	Rune    rune
}

func (e *DescriptionError) Error() string {
	if e == nil {
		return "<nil>"
	}
	switch e.Problem {
	case DescriptionLength:
		return "description must contain 1 through 200 Unicode code points"
	case DescriptionEdgeSpace:
		return "description must not begin or end with U+0020 SPACE"
	case DescriptionCharacter:
		return fmt.Sprintf("description contains forbidden U+%04X at code-point index %d", e.Rune, e.Index)
	default:
		return "description is not valid UTF-8"
	}
}

// ValidateDescription applies the exact universal label constraints. Length
// is counted in Unicode code points, not bytes or grapheme clusters.
func ValidateDescription(value string) error {
	if !utf8.ValidString(value) {
		return &DescriptionError{Problem: DescriptionInvalidUTF8}
	}
	count := utf8.RuneCountInString(value)
	if count < 1 || count > 200 {
		return &DescriptionError{Problem: DescriptionLength}
	}
	if strings.HasPrefix(value, " ") || strings.HasSuffix(value, " ") {
		return &DescriptionError{Problem: DescriptionEdgeSpace}
	}
	for index, character := range []rune(value) {
		if forbiddenSingleLineCharacter(character) {
			return &DescriptionError{Problem: DescriptionCharacter, Index: index, Rune: character}
		}
	}
	return nil
}

// ValidDescription reports whether value obeys §4.2.
func ValidDescription(value string) bool { return ValidateDescription(value) == nil }

func forbiddenSingleLineCharacter(character rune) bool {
	return character <= 0x1f || character >= 0x7f && character <= 0x9f || character == 0x2028 || character == 0x2029
}

// FieldProblem classifies a reusable frontmatter field failure.
type FieldProblem string

const (
	FieldMissing   FieldProblem = "missing"
	FieldWrongKind FieldProblem = "wrong-kind"
	FieldEmpty     FieldProblem = "empty"
	FieldInvalid   FieldProblem = "invalid"
)

// FieldError contains enough information for an artifact validator to map a
// field failure to its applicable normative finding.
type FieldError struct {
	Field    string
	Problem  FieldProblem
	Position yamlprofile.Position
	Cause    error
}

func (e *FieldError) Error() string {
	if e == nil {
		return "<nil>"
	}
	message := fmt.Sprintf("frontmatter field %q is %s", e.Field, e.Problem)
	if e.Cause != nil {
		message += ": " + e.Cause.Error()
	}
	return message
}

func (e *FieldError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// RequiredString reads a top-level required string and applies the common
// YAML profile's ASCII-trimmed non-empty rule. The returned value is never
// trimmed or normalized.
func RequiredString(mapping *yamlprofile.Node, field string) (string, error) {
	if mapping == nil || mapping.Kind != yamlprofile.MappingKind {
		return "", &FieldError{Field: field, Problem: FieldMissing}
	}
	node, exists := mapping.Lookup(field)
	if !exists {
		return "", &FieldError{Field: field, Problem: FieldMissing, Position: mapping.Position}
	}
	if node.Kind != yamlprofile.StringKind {
		return "", &FieldError{Field: field, Problem: FieldWrongKind, Position: node.Position}
	}
	if yamlprofile.TrimASCIIWhitespace(node.String) == "" {
		return "", &FieldError{Field: field, Problem: FieldEmpty, Position: node.Position}
	}
	return node.String, nil
}

// Description reads and validates a top-level universal description.
func Description(mapping *yamlprofile.Node) (string, error) {
	value, err := RequiredString(mapping, "description")
	if err != nil {
		return "", err
	}
	if err := ValidateDescription(value); err != nil {
		node, _ := mapping.Lookup("description")
		return "", &FieldError{Field: "description", Problem: FieldInvalid, Position: node.Position, Cause: err}
	}
	return value, nil
}

// ValidTypeSlug delegates to the single mandatory v1 type-name grammar used
// by snapshot/schema traversal.
func ValidTypeSlug(value string) bool { return snapshot.ValidTypeSlug(value) }

// Type reads a required top-level type and validates its slug syntax.
func Type(mapping *yamlprofile.Node) (string, error) {
	value, err := RequiredString(mapping, "type")
	if err != nil {
		return "", err
	}
	if !ValidTypeSlug(value) {
		node, _ := mapping.Lookup("type")
		return "", &FieldError{Field: "type", Problem: FieldInvalid, Position: node.Position}
	}
	return value, nil
}

// Pinned reads the optional top-level universal pin. present distinguishes an
// absent value from an explicit false value.
func Pinned(mapping *yamlprofile.Node) (value bool, present bool, err error) {
	if mapping == nil || mapping.Kind != yamlprofile.MappingKind {
		return false, false, nil
	}
	node, exists := mapping.Lookup("pinned")
	if !exists {
		return false, false, nil
	}
	if node.Kind != yamlprofile.BooleanKind {
		return false, true, &FieldError{Field: "pinned", Problem: FieldWrongKind, Position: node.Position}
	}
	return node.Boolean, true, nil
}

// CatalogMode is the closed README catalog policy.
type CatalogMode string

const (
	CatalogAll  CatalogMode = "all"
	CatalogDirs CatalogMode = "dirs"
	CatalogNone CatalogMode = "none"
)

// ValidCatalogMode reports whether mode is one of the three v1 values.
func ValidCatalogMode(mode CatalogMode) bool {
	return mode == CatalogAll || mode == CatalogDirs || mode == CatalogNone
}

// CatalogModeFrom reads a README's optional catalog field, defaulting an
// absent field to all.
func CatalogModeFrom(mapping *yamlprofile.Node) (CatalogMode, error) {
	if mapping == nil || mapping.Kind != yamlprofile.MappingKind {
		return CatalogAll, nil
	}
	node, exists := mapping.Lookup("catalog")
	if !exists {
		return CatalogAll, nil
	}
	if node.Kind != yamlprofile.StringKind {
		return "", &FieldError{Field: "catalog", Problem: FieldWrongKind, Position: node.Position}
	}
	mode := CatalogMode(node.String)
	if !ValidCatalogMode(mode) {
		return "", &FieldError{Field: "catalog", Problem: FieldInvalid, Position: node.Position}
	}
	return mode, nil
}
