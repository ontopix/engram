// Package schemaprofile implements the engram JSON Schema profile.
//
// It performs the profile's finite syntactic checks before handing the
// assertion work to a draft 2020-12 JSON Schema compiler. Schema and instance
// numbers remain json.Number values throughout; binary floating-point values
// are deliberately rejected.
package schemaprofile

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/url"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

const (
	// Draft2020Schema is the only $schema value admitted by the profile.
	Draft2020Schema = "https://json-schema.org/draft/2020-12/schema"
	resourceURL     = "https://engram.invalid/schema"
)

var jsonNumberPattern = regexp.MustCompile(`^-?(?:0|[1-9][0-9]*)(?:\.[0-9]+)?(?:[eE][+-]?[0-9]+)?$`)

// Schema is one successfully compiled engram-profile schema. It is safe for
// concurrent validation after Compile returns.
type Schema struct {
	compiled *jsonschema.Schema

	missingUniversalLabels bool
	vendorLocations        []string
	linkFields             []LinkField
}

// Compile checks document against the engram profile and compiles it as JSON
// Schema draft 2020-12. The root must be a schema mapping, rather than a
// Boolean schema.
func Compile(document any) (*Schema, error) {
	cloned, problem := cloneExact(document, nil, make(map[containerKey]struct{}))
	if problem != nil {
		return nil, newProfileError(*problem)
	}
	root, ok := cloned.(map[string]any)
	if !ok {
		return nil, newProfileError(Problem{
			Location: "",
			Message:  "schema root must be a mapping",
		})
	}

	analysis := newAnalyzer(root)
	analysis.run()
	if len(analysis.problems) != 0 {
		return nil, &ProfileError{Problems: sortedProblems(analysis.problems)}
	}

	// The profile-specific annotations and non-asserted formats are removed
	// from the private compiler copy. Portable patterns are replaced by the
	// equivalent regexp produced by regexprofile so no host regexp extension
	// can alter their meaning.
	analysis.prepareCompilerDocument()

	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	compiler.AssertFormat()
	compiler.RegisterFormat(&jsonschema.Format{Name: "date", Validate: validateDateFormat})
	compiler.RegisterFormat(&jsonschema.Format{Name: "date-time", Validate: validateDateTimeFormat})
	compiler.UseLoader(blockedLoader{})
	if err := compiler.AddResource(resourceURL, root); err != nil {
		return nil, compileProfileError(err)
	}
	compiled, err := compiler.Compile(resourceURL)
	if err != nil {
		return nil, compileProfileError(err)
	}

	return &Schema{
		compiled:               compiled,
		missingUniversalLabels: analysis.missingUniversalLabels(),
		vendorLocations:        append([]string(nil), analysis.vendorLocations...),
		linkFields:             cloneLinkFields(analysis.linkFields),
	}, nil
}

// CompileJSON decodes a JSON document without converting numbers to float64,
// then calls Compile.
func CompileJSON(source []byte) (*Schema, error) {
	value, err := DecodeJSON(source)
	if err != nil {
		return nil, err
	}
	return Compile(value)
}

// DecodeJSON decodes exactly one JSON value and preserves every number as a
// json.Number. CompileJSON is normally the more convenient entry point.
func DecodeJSON(source []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(source))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("decode schema JSON: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("decode schema JSON: expected exactly one JSON value")
		}
		return nil, fmt.Errorf("decode schema JSON: %w", err)
	}
	return value, nil
}

// Validate checks an instance. The returned *ValidationError contains stable
// RFC 6901 instance and schema locations.
func (s *Schema) Validate(instance any) error {
	if s == nil || s.compiled == nil {
		return errors.New("schemaprofile: validate with nil schema")
	}
	cloned, problem := cloneExact(instance, nil, make(map[containerKey]struct{}))
	if problem != nil {
		return &ValidationError{Problems: []ValidationProblem{{
			InstanceLocation: problem.Location,
			Message:          problem.Message,
		}}}
	}
	if err := s.compiled.Validate(cloned); err != nil {
		return validationError(err)
	}
	return nil
}

// MissingUniversalLabels reports the E305 condition: the schema closes the
// root object with additionalProperties: false but does not directly declare
// both universal fields, type and description.
func (s *Schema) MissingUniversalLabels() bool {
	return s != nil && s.missingUniversalLabels
}

// HasE305 is a concise alias for MissingUniversalLabels.
func (s *Schema) HasE305() bool {
	return s.MissingUniversalLabels()
}

// HasVendorAnnotations reports whether check must emit W904 for the schema
// file. W904 is emitted once even if several locations are present.
func (s *Schema) HasVendorAnnotations() bool {
	return s != nil && len(s.vendorLocations) != 0
}

// VendorAnnotationLocations returns the schema keyword locations that caused
// HasVendorAnnotations to be true.
func (s *Schema) VendorAnnotationLocations() []string {
	if s == nil {
		return nil
	}
	return append([]string(nil), s.vendorLocations...)
}

// LinkFields returns the typed-link declarations found during compilation.
func (s *Schema) LinkFields() []LinkField {
	if s == nil {
		return nil
	}
	return cloneLinkFields(s.linkFields)
}

type blockedLoader struct{}

func (blockedLoader) Load(address string) (any, error) {
	return nil, fmt.Errorf("external schema loading is disabled: %s", address)
}

type containerKey struct {
	kind reflect.Kind
	ptr  uintptr
}

// cloneExact both makes caller mutation harmless and rejects values that do
// not have an exact JSON representation. In particular float32/float64 are
// rejected even when their current binary value happens to print cleanly.
func cloneExact(value any, tokens []string, active map[containerKey]struct{}) (any, *Problem) {
	switch value := value.(type) {
	case nil, bool:
		return value, nil
	case string:
		if !utf8.ValidString(value) {
			return nil, &Problem{Location: pointer(tokens), Message: "string is not valid UTF-8"}
		}
		return value, nil
	case json.Number:
		spelling := string(value)
		if !jsonNumberPattern.MatchString(spelling) {
			return nil, &Problem{Location: pointer(tokens), Message: fmt.Sprintf("invalid JSON number %q", spelling)}
		}
		if _, ok := new(big.Rat).SetString(spelling); !ok {
			return nil, &Problem{Location: pointer(tokens), Message: fmt.Sprintf("invalid exact number %q", spelling)}
		}
		return value, nil
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return value, nil
	case float32, float64:
		return nil, &Problem{
			Location: pointer(tokens),
			Message:  "binary floating-point values are not admitted; use json.Number",
		}
	case map[string]any:
		key := containerKey{kind: reflect.Map, ptr: reflect.ValueOf(value).Pointer()}
		if _, cycle := active[key]; cycle {
			return nil, &Problem{Location: pointer(tokens), Message: "cyclic mappings are not JSON values"}
		}
		active[key] = struct{}{}
		defer delete(active, key)

		result := make(map[string]any, len(value))
		keys := sortedKeys(value)
		for _, name := range keys {
			if !utf8.ValidString(name) {
				return nil, &Problem{Location: pointer(tokens), Message: "mapping key is not valid UTF-8"}
			}
			item, problem := cloneExact(value[name], child(tokens, name), active)
			if problem != nil {
				return nil, problem
			}
			result[name] = item
		}
		return result, nil
	case []any:
		key := containerKey{kind: reflect.Slice, ptr: reflect.ValueOf(value).Pointer()}
		if _, cycle := active[key]; cycle {
			return nil, &Problem{Location: pointer(tokens), Message: "cyclic sequences are not JSON values"}
		}
		active[key] = struct{}{}
		defer delete(active, key)

		result := make([]any, len(value))
		for i := range value {
			item, problem := cloneExact(value[i], child(tokens, strconv.Itoa(i)), active)
			if problem != nil {
				return nil, problem
			}
			result[i] = item
		}
		return result, nil
	default:
		return nil, &Problem{
			Location: pointer(tokens),
			Message:  fmt.Sprintf("%T is not a JSON data-model value", value),
		}
	}
}

func sortedKeys[V any](mapping map[string]V) []string {
	keys := make([]string, 0, len(mapping))
	for key := range mapping {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func child(tokens []string, token string) []string {
	result := make([]string, len(tokens)+1)
	copy(result, tokens)
	result[len(tokens)] = token
	return result
}

func pointer(tokens []string) string {
	if len(tokens) == 0 {
		return ""
	}
	var result strings.Builder
	for _, token := range tokens {
		result.WriteByte('/')
		result.WriteString(strings.ReplaceAll(strings.ReplaceAll(token, "~", "~0"), "/", "~1"))
	}
	return result.String()
}

func decodePointer(value string) ([]string, error) {
	if value == "" {
		return nil, nil
	}
	if !strings.HasPrefix(value, "/") {
		return nil, fmt.Errorf("JSON Pointer must start with /")
	}
	parts := strings.Split(value[1:], "/")
	for i, part := range parts {
		var decoded strings.Builder
		for offset := 0; offset < len(part); offset++ {
			if part[offset] != '~' {
				decoded.WriteByte(part[offset])
				continue
			}
			if offset+1 >= len(part) || part[offset+1] != '0' && part[offset+1] != '1' {
				return nil, fmt.Errorf("~ must be followed by 0 or 1")
			}
			offset++
			if part[offset] == '0' {
				decoded.WriteByte('~')
			} else {
				decoded.WriteByte('/')
			}
		}
		parts[i] = decoded.String()
	}
	return parts, nil
}

func pointerFromURL(address string) string {
	parsed, err := url.Parse(address)
	if err != nil || parsed.Fragment == "" {
		return ""
	}
	tokens, err := decodePointer(parsed.Fragment)
	if err != nil {
		return ""
	}
	return pointer(tokens)
}
