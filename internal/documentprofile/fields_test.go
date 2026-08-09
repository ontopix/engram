package documentprofile

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/ontopix/engram/internal/yamlprofile"
)

func TestValidateDescriptionExactConstraints(t *testing.T) {
	t.Parallel()
	valid := []string{
		"A concise catalog description",
		"\u00a0non-ASCII edge whitespace is permitted\u00a0",
		strings.Repeat("界", 200),
	}
	for _, value := range valid {
		if err := ValidateDescription(value); err != nil {
			t.Errorf("ValidateDescription(%q): %v", value, err)
		}
		if !ValidDescription(value) {
			t.Errorf("ValidDescription(%q) = false", value)
		}
	}

	invalid := []struct {
		name    string
		value   string
		problem DescriptionProblem
	}{
		{"empty", "", DescriptionLength},
		{"too long", strings.Repeat("a", 201), DescriptionLength},
		{"leading space", " leading", DescriptionEdgeSpace},
		{"trailing space", "trailing ", DescriptionEdgeSpace},
		{"tab", "a\tb", DescriptionCharacter},
		{"newline", "a\nb", DescriptionCharacter},
		{"delete", "a\x7fb", DescriptionCharacter},
		{"C1", "a\u0085b", DescriptionCharacter},
		{"line separator", "a\u2028b", DescriptionCharacter},
		{"paragraph separator", "a\u2029b", DescriptionCharacter},
		{"invalid UTF-8", string([]byte{0xff}), DescriptionInvalidUTF8},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateDescription(test.value)
			var descriptionErr *DescriptionError
			if !errors.As(err, &descriptionErr) || descriptionErr.Problem != test.problem {
				t.Fatalf("error = %v, want %s", err, test.problem)
			}
		})
	}
	if utf8.RuneCountInString(strings.Repeat("界", 200)) != 200 {
		t.Fatal("test setup did not exercise multibyte code-point count")
	}
}

func TestUniversalFieldReaders(t *testing.T) {
	t.Parallel()
	document := parseYAML(t, `type: decision
description: Decide how to persist memory
pinned: true
catalog: dirs
nested:
  pinned: nope
`)

	if got, err := Type(document.Root); err != nil || got != "decision" {
		t.Fatalf("Type = %q, %v", got, err)
	}
	if got, err := Description(document.Root); err != nil || got != "Decide how to persist memory" {
		t.Fatalf("Description = %q, %v", got, err)
	}
	if value, present, err := Pinned(document.Root); err != nil || !present || !value {
		t.Fatalf("Pinned = %v, %v, %v", value, present, err)
	}
	if mode, err := CatalogModeFrom(document.Root); err != nil || mode != CatalogDirs {
		t.Fatalf("CatalogModeFrom = %q, %v", mode, err)
	}
}

func TestRequiredAndTypedFieldFailuresAreClassified(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		source  string
		read    func(*yamlprofile.Node) error
		field   string
		problem FieldProblem
	}{
		{"missing type", "other: value\n", func(root *yamlprofile.Node) error { _, err := Type(root); return err }, "type", FieldMissing},
		{"numeric type", "type: 1\n", func(root *yamlprofile.Node) error { _, err := Type(root); return err }, "type", FieldWrongKind},
		{"ASCII-empty type", "type: \" \\t\"\n", func(root *yamlprofile.Node) error { _, err := Type(root); return err }, "type", FieldEmpty},
		{"bad type slug", "type: Bad-Type\n", func(root *yamlprofile.Node) error { _, err := Type(root); return err }, "type", FieldInvalid},
		{"missing description", "type: note\n", func(root *yamlprofile.Node) error { _, err := Description(root); return err }, "description", FieldMissing},
		{"bad description", "description: \" trailing \"\n", func(root *yamlprofile.Node) error { _, err := Description(root); return err }, "description", FieldInvalid},
		{"nonboolean pinned", "pinned: yes\n", func(root *yamlprofile.Node) error { _, _, err := Pinned(root); return err }, "pinned", FieldWrongKind},
		{"nonstring catalog", "catalog: false\n", func(root *yamlprofile.Node) error { _, err := CatalogModeFrom(root); return err }, "catalog", FieldWrongKind},
		{"unknown catalog", "catalog: records\n", func(root *yamlprofile.Node) error { _, err := CatalogModeFrom(root); return err }, "catalog", FieldInvalid},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			document := parseYAML(t, test.source)
			err := test.read(document.Root)
			var fieldErr *FieldError
			if !errors.As(err, &fieldErr) || fieldErr.Field != test.field || fieldErr.Problem != test.problem {
				t.Fatalf("error = %#v, want %s/%s", err, test.field, test.problem)
			}
		})
	}
}

func TestPinnedAbsentAndFalseAreDistinct(t *testing.T) {
	t.Parallel()
	absent := parseYAML(t, "nested: {pinned: true}\n")
	if value, present, err := Pinned(absent.Root); err != nil || present || value {
		t.Fatalf("absent Pinned = %v, %v, %v", value, present, err)
	}
	explicit := parseYAML(t, "pinned: false\n")
	if value, present, err := Pinned(explicit.Root); err != nil || !present || value {
		t.Fatalf("false Pinned = %v, %v, %v", value, present, err)
	}
}

func TestTypeSlugGrammar(t *testing.T) {
	t.Parallel()
	valid := []string{"a", "note", "decision-log2", strings.Repeat("a", 64)}
	invalid := []string{"", "-a", "a-", "a--b", "A", "a_b", "café", strings.Repeat("a", 65)}
	for _, value := range valid {
		if !ValidTypeSlug(value) {
			t.Errorf("ValidTypeSlug(%q) = false", value)
		}
	}
	for _, value := range invalid {
		if ValidTypeSlug(value) {
			t.Errorf("ValidTypeSlug(%q) = true", value)
		}
	}
}

func TestCatalogModeDefault(t *testing.T) {
	t.Parallel()
	if mode, err := CatalogModeFrom(parseYAML(t, "description: map\n").Root); err != nil || mode != CatalogAll {
		t.Fatalf("default mode = %q, %v", mode, err)
	}
}

func parseYAML(t *testing.T, source string) *yamlprofile.Document {
	t.Helper()
	document, err := yamlprofile.Parse([]byte(source))
	if err != nil {
		t.Fatalf("yamlprofile.Parse: %v", err)
	}
	return document
}
