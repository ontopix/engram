package schemaprofile

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestExactNumbers(t *testing.T) {
	t.Run("const beyond binary precision", func(t *testing.T) {
		schema := mustCompile(t, object(
			"type", "number",
			"const", number("9007199254740993"),
		))
		assertValid(t, schema, number("9007199254740993"))
		assertInvalidAt(t, schema, number("9007199254740992"), "")
	})

	t.Run("exact multipleOf", func(t *testing.T) {
		schema := mustCompile(t, object(
			"type", "number",
			"multipleOf", number("0.1"),
		))
		assertValid(t, schema, number("0.3"))
		assertInvalidAt(t, schema, number("0.3000000000000000000000000000000000001"), "")
	})

	t.Run("integer is mathematical", func(t *testing.T) {
		schema := mustCompile(t, object("type", "integer"))
		assertValid(t, schema, number("1.0"))
		assertValid(t, schema, number("10e-1"))
		assertInvalidAt(t, schema, number("1.5"), "")
	})

	t.Run("float64 rejected in schema", func(t *testing.T) {
		_, err := Compile(object("const", 0.1))
		assertProfileProblem(t, err, "/const", "binary floating-point")
	})

	t.Run("float64 rejected in instance with location", func(t *testing.T) {
		schema := mustCompile(t, object(
			"type", "object",
			"properties", object("amount", object("type", "number")),
		))
		err := schema.Validate(object("amount", 0.1))
		var validation *ValidationError
		if !errors.As(err, &validation) {
			t.Fatalf("Validate error = %T %v, want *ValidationError", err, err)
		}
		if got := validation.Problems[0].InstanceLocation; got != "/amount" {
			t.Fatalf("instance location = %q, want /amount", got)
		}
	})

	t.Run("CompileJSON preserves number", func(t *testing.T) {
		schema, err := CompileJSON([]byte(`{"const":9007199254740993}`))
		if err != nil {
			t.Fatalf("CompileJSON: %v", err)
		}
		assertValid(t, schema, number("9007199254740993"))
		assertInvalidAt(t, schema, number("9007199254740992"), "")
	})
}

func TestAssertedTemporalFormats(t *testing.T) {
	tests := []struct {
		name    string
		format  string
		valid   []string
		invalid []string
	}{
		{
			name:   "date",
			format: "date",
			valid:  []string{"0001-01-01", "2000-02-29", "9999-12-31"},
			invalid: []string{
				"0000-01-01", "1900-02-29", "2024-02-30", "2024-1-01",
				"+2024-01-01", "２０２４-01-01",
			},
		},
		{
			name:   "date-time",
			format: "date-time",
			valid: []string{
				"2024-02-29T00:00:00Z",
				"2024-02-29T23:59:59.0+23:59",
				"0001-01-01T12:30:45.123456-00:00",
			},
			invalid: []string{
				"2024-01-01t00:00:00Z", "2024-01-01T00:00:00z",
				"2024-01-01T00:00:60Z", "2024-01-01T24:00:00Z",
				"2024-01-01T00:00:00", "2024-01-01T00:00:00.Z",
				"2024-01-01T00:00:00+24:00", "2024-01-01T00:00:00+00:60",
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			schema := mustCompile(t, object("type", "string", "format", test.format))
			for _, value := range test.valid {
				assertValid(t, schema, value)
			}
			for _, value := range test.invalid {
				assertInvalidAt(t, schema, value, "")
			}
		})
	}

	t.Run("other formats are annotations", func(t *testing.T) {
		schema := mustCompile(t, object("type", "string", "format", "email"))
		assertValid(t, schema, "not an email")
	})
}

func TestPortablePatterns(t *testing.T) {
	t.Run("translated punctuation escape", func(t *testing.T) {
		// \# belongs to the portable language but Go's regexp parser rejects
		// that source directly. Successful compilation proves translation went
		// through regexprofile rather than the host parser.
		schema := mustCompile(t, object("type", "string", "pattern", `^a\#b$`))
		assertValid(t, schema, "a#b")
		assertInvalidAt(t, schema, "xa#b", "")
	})

	for _, pattern := range []string{`\d+`, `.`, `[a-b-c]`, "café"} {
		t.Run(pattern, func(t *testing.T) {
			_, err := Compile(object(
				"properties", object("name", object("pattern", pattern)),
			))
			assertProfileProblem(t, err, "/properties/name/pattern", "portable subset")
		})
	}
}

func TestLocalReferences(t *testing.T) {
	t.Run("resolved schema positions below defs", func(t *testing.T) {
		schema := mustCompile(t, object(
			"$defs", object(
				"container", object(
					"properties", object("value", object("type", "integer")),
				),
				"never", false,
			),
			"$ref", "#/%24defs/container/properties/value",
		))
		assertValid(t, schema, number("1.0"))
		assertInvalidAt(t, schema, "1", "")

		never := mustCompile(t, object(
			"$defs", object("never", false),
			"$ref", "#/$defs/never",
		))
		assertInvalidAt(t, never, nil, "")
	})

	tests := []struct {
		name      string
		reference string
		extra     map[string]any
	}{
		{name: "remote", reference: "https://example.com/schema"},
		{name: "cross file", reference: "other.json#/$defs/x"},
		{name: "wrong root", reference: "#/properties/x"},
		{name: "empty target", reference: "#/$defs"},
		{name: "invalid pointer", reference: "#/$defs/a~2b"},
		{name: "raw URI space", reference: "#/$defs/a b"},
		{name: "unresolved", reference: "#/$defs/missing", extra: object("$defs", object())},
		{
			name:      "instance data target",
			reference: "#/$defs/x/const/not-a-schema",
			extra: object("$defs", object(
				"x", object("const", object("not-a-schema", object("type", "string"))),
			)),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			document := object("$ref", test.reference)
			for key, value := range test.extra {
				document[key] = value
			}
			_, err := Compile(document)
			assertProfileProblem(t, err, "/$ref", "")
		})
	}
}

func TestExternalLoaderIsBlocked(t *testing.T) {
	_, err := (blockedLoader{}).Load("https://example.com/schema.json")
	if err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("blocked loader error = %v", err)
	}
	_, err = Compile(object("$ref", "https://example.com/schema.json"))
	assertProfileProblem(t, err, "/$ref", "fragment-only")
}

func TestKeywordRecognitionAndVendorAnnotations(t *testing.T) {
	t.Run("instance values are opaque", func(t *testing.T) {
		mustCompile(t, object(
			"const", object(
				"unknownKeyword", true,
				"$id", "not inspected",
				"x-engram-future", object("anything", true),
			),
			"default", object("anotherUnknown", true),
			"examples", array(object("alsoUnknown", true)),
		))
	})

	t.Run("vendor annotations are ignored and observable", func(t *testing.T) {
		schema := mustCompile(t, object(
			"x-acme-owner", object("anyKeywordLikeKey", array(number("1.25"))),
			"properties", object(
				"name", object("type", "string", "x-zed-note-id", nil),
			),
		))
		if !schema.HasVendorAnnotations() {
			t.Fatal("HasVendorAnnotations = false, want true")
		}
		want := []string{"/properties/name/x-zed-note-id", "/x-acme-owner"}
		if got := schema.VendorAnnotationLocations(); !reflect.DeepEqual(got, want) {
			t.Fatalf("vendor locations = %#v, want %#v", got, want)
		}
		assertValid(t, schema, object("name", "Ada"))
	})

	tests := []struct {
		name     string
		document map[string]any
		location string
	}{
		{"unknown", object("definitions", object()), "/definitions"},
		{"bad vendor spelling", object("x-Acme-owner", true), "/x-Acme-owner"},
		{"reserved extension", object("x-engram-future", true), "/x-engram-future"},
		{"forbidden id", object("$id", "https://example.com"), "/$id"},
		{"forbidden anchor", object("$anchor", "x"), "/$anchor"},
		{"forbidden patternProperties", object("patternProperties", object()), "/patternProperties"},
		{
			"nested schema declaration",
			object("properties", object("x", object("$schema", Draft2020Schema))),
			"/properties/x/$schema",
		},
		{"wrong dialect", object("$schema", "https://json-schema.org/draft/2019-09/schema"), "/$schema"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Compile(test.document)
			assertProfileProblem(t, err, test.location, "")
		})
	}

	t.Run("vocabulary syntax error has source location", func(t *testing.T) {
		tests := []struct {
			document map[string]any
			location string
		}{
			{object("required", "name"), "/required"},
			{
				object("properties", object("a/b", object("type", "not-a-type"))),
				"/properties/a~1b/type",
			},
			{object("minLength", number("-1")), "/minLength"},
		}
		for _, test := range tests {
			_, err := Compile(test.document)
			assertProfileProblem(t, err, test.location, "")
		}
	})
}

func TestReservedRootInstanceProperties(t *testing.T) {
	tests := []struct {
		name     string
		document map[string]any
		location string
	}{
		{
			"properties",
			object("properties", object("engram-secret", object())),
			"/properties/engram-secret",
		},
		{
			"required through applicator",
			object("allOf", array(object("required", array("engram-secret")))),
			"/allOf/0/required/0",
		},
		{
			"dependent required value",
			object("dependentRequired", object("name", array("engram-secret"))),
			"/dependentRequired/name/0",
		},
		{
			"through ref",
			object(
				"$defs", object("root", object("properties", object("engram-secret", true))),
				"$ref", "#/$defs/root",
			),
			"/$defs/root/properties/engram-secret",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Compile(test.document)
			assertProfileProblem(t, err, test.location, "reserved root")
		})
	}

	t.Run("does not descend into property instance", func(t *testing.T) {
		mustCompile(t, object(
			"properties", object(
				"metadata", object("properties", object("engram-local", object())),
			),
		))
	})
}

func TestE305Indicator(t *testing.T) {
	tests := []struct {
		name     string
		document map[string]any
		want     bool
	}{
		{"open schema", object(), false},
		{"closed without properties", object("additionalProperties", false), true},
		{
			"closed direct declarations",
			object(
				"additionalProperties", false,
				"properties", object("type", object(), "description", object()),
			),
			false,
		},
		{
			"applicator declarations do not count",
			object(
				"additionalProperties", false,
				"allOf", array(object("properties", object("type", object(), "description", object()))),
			),
			true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			schema := mustCompile(t, test.document)
			if got := schema.MissingUniversalLabels(); got != test.want {
				t.Fatalf("MissingUniversalLabels = %v, want %v", got, test.want)
			}
			if got := schema.HasE305(); got != test.want {
				t.Fatalf("HasE305 = %v, want %v", got, test.want)
			}
		})
	}
}

func TestLinkFieldsAndInstancePointers(t *testing.T) {
	link := object(
		"types", array("person", "organization"),
		"must-exist", false,
	)
	document := object(
		"type", "object",
		"properties", object(
			"people", object(
				"type", "array",
				"items", object(
					"type", "object",
					"properties", object(
						"manager/ref", object(
							"type", "string",
							"x-engram-link", link,
						),
					),
				),
			),
		),
	)
	schema := mustCompile(t, document)
	fields := schema.LinkFields()
	if len(fields) != 1 {
		t.Fatalf("len(LinkFields) = %d, want 1", len(fields))
	}
	field := fields[0]
	if field.SchemaLocation != "/properties/people/items/properties/manager~1ref/x-engram-link" {
		t.Fatalf("SchemaLocation = %q", field.SchemaLocation)
	}
	if field.InstancePattern != "/people/*/manager~1ref" {
		t.Fatalf("InstancePattern = %q", field.InstancePattern)
	}
	if field.MustExist {
		t.Fatal("MustExist = true, want false")
	}
	if want := []string{"person", "organization"}; !reflect.DeepEqual(field.Types, want) {
		t.Fatalf("Types = %#v, want %#v", field.Types, want)
	}

	instance := object(
		"people", array(
			object("manager/ref", "[[Ada Lovelace]]"),
			object(),
			object("manager/ref", "[[Grace Hopper]]"),
		),
	)
	assertValid(t, schema, instance)
	occurrences := schema.ExtractLinks(instance)
	if len(occurrences) != 2 {
		t.Fatalf("ExtractLinks = %#v, want 2 occurrences", occurrences)
	}
	wantPointers := []string{"/people/0/manager~1ref", "/people/2/manager~1ref"}
	if got := schema.LinkPointers(instance); !reflect.DeepEqual(got, wantPointers) {
		t.Fatalf("LinkPointers = %#v, want %#v", got, wantPointers)
	}
	if occurrences[1].Value != "[[Grace Hopper]]" {
		t.Fatalf("second link value = %q", occurrences[1].Value)
	}
	// Wikilink syntax belongs to the later link checker (E403), not schema
	// assertion (E301).
	assertValid(t, schema, object("people", array(object("manager/ref", "not a wikilink"))))

	// Returned metadata is defensive: mutating it must not alter extraction.
	fields[0].Types[0] = "corrupted"
	if got := schema.LinkFields()[0].Types[0]; got != "person" {
		t.Fatalf("LinkFields leaked mutable state: %q", got)
	}
}

func TestInvalidLinkFields(t *testing.T) {
	validLink := object("types", array("person"))
	tests := []struct {
		name     string
		document map[string]any
		location string
	}{
		{
			"root placement",
			object("type", "string", "x-engram-link", validLink),
			"/x-engram-link",
		},
		{
			"defs path",
			object("$defs", object("link", object("type", "string", "x-engram-link", validLink))),
			"/$defs/link/x-engram-link",
		},
		{
			"applicator path",
			object("allOf", array(object(
				"properties", object("owner", object("type", "string", "x-engram-link", validLink)),
			))),
			"/allOf/0/properties/owner/x-engram-link",
		},
		{
			"direct string type required",
			object("properties", object(
				"owner", object("type", array("string"), "x-engram-link", validLink),
			)),
			"/properties/owner/x-engram-link",
		},
		{
			"closed mapping",
			object("properties", object(
				"owner", object("type", "string", "x-engram-link", object("types", array("person"), "extra", true)),
			)),
			"/properties/owner/x-engram-link/extra",
		},
		{
			"duplicate types",
			object("properties", object(
				"owner", object("type", "string", "x-engram-link", object("types", array("person", "person"))),
			)),
			"/properties/owner/x-engram-link/types/1",
		},
		{
			"invalid slug",
			object("properties", object(
				"owner", object("type", "string", "x-engram-link", object("types", array("Bad--slug"))),
			)),
			"/properties/owner/x-engram-link/types/0",
		},
		{
			"must exist boolean",
			object("properties", object(
				"owner", object("type", "string", "x-engram-link", object("types", array("person"), "must-exist", "false")),
			)),
			"/properties/owner/x-engram-link/must-exist",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Compile(test.document)
			assertProfileProblem(t, err, test.location, "")
		})
	}
}

func TestErrorLocationsEscapeJSONPointerTokens(t *testing.T) {
	_, err := Compile(object(
		"properties", object(
			"a/b~c", object("unknown", true),
		),
	))
	assertProfileProblem(t, err, "/properties/a~1b~0c/unknown", "unknown")

	schema := mustCompile(t, object(
		"type", "object",
		"properties", object("a/b~c", object("type", "string")),
	))
	err = schema.Validate(object("a/b~c", true))
	var validation *ValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("Validate error = %T %v", err, err)
	}
	if len(validation.Problems) == 0 || validation.Problems[0].InstanceLocation != "/a~1b~0c" {
		t.Fatalf("validation problems = %#v", validation.Problems)
	}
	if !strings.HasSuffix(validation.Problems[0].SchemaLocation, "/type") {
		t.Fatalf("schema location = %q, want suffix /type", validation.Problems[0].SchemaLocation)
	}
}

func object(values ...any) map[string]any {
	if len(values)%2 != 0 {
		panic("object requires key/value pairs")
	}
	result := make(map[string]any, len(values)/2)
	for index := 0; index < len(values); index += 2 {
		result[values[index].(string)] = values[index+1]
	}
	return result
}

func array(values ...any) []any {
	return values
}

func number(value string) json.Number {
	return json.Number(value)
}

func mustCompile(t *testing.T, document map[string]any) *Schema {
	t.Helper()
	schema, err := Compile(document)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	return schema
}

func assertValid(t *testing.T, schema *Schema, value any) {
	t.Helper()
	if err := schema.Validate(value); err != nil {
		t.Fatalf("Validate(%#v): %v", value, err)
	}
}

func assertInvalidAt(t *testing.T, schema *Schema, value any, location string) {
	t.Helper()
	err := schema.Validate(value)
	var validation *ValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("Validate(%#v) error = %T %v, want *ValidationError", value, err, err)
	}
	for _, problem := range validation.Problems {
		if problem.InstanceLocation == location {
			return
		}
	}
	t.Fatalf("validation problems = %#v, want instance location %q", validation.Problems, location)
}

func assertProfileProblem(t *testing.T, err error, location, messagePart string) {
	t.Helper()
	var profile *ProfileError
	if !errors.As(err, &profile) {
		t.Fatalf("error = %T %v, want *ProfileError", err, err)
	}
	for _, problem := range profile.Problems {
		if problem.Location == location && (messagePart == "" || strings.Contains(problem.Message, messagePart)) {
			return
		}
	}
	t.Fatalf("profile problems = %#v, want location %q containing %q", profile.Problems, location, messagePart)
}
