package yamlprofile

import (
	"encoding/json"
	"errors"
	"math/big"
	"reflect"
	"strings"
	"testing"
)

func TestParseCoreSchemaAndExactNumbers(t *testing.T) {
	document := mustParse(t, `null-word: null
null-empty:
truth: TRUE
falsehood: False
decimal: +00120
octal: 0o17
hexadecimal: 0x3A
fraction: .5
exponent: -2E+05
integral-float: 1.00e2
negative-zero: -0.0
date-looking: 2026-08-09
legacy-bool: yes
quoted: "true"
nested: [null, false, 1.5]
block: |
  hello
`)

	tests := []struct {
		key  string
		kind Kind
	}{
		{"null-word", NullKind},
		{"null-empty", NullKind},
		{"truth", BooleanKind},
		{"falsehood", BooleanKind},
		{"decimal", NumberKind},
		{"octal", NumberKind},
		{"hexadecimal", NumberKind},
		{"fraction", NumberKind},
		{"exponent", NumberKind},
		{"integral-float", NumberKind},
		{"negative-zero", NumberKind},
		{"date-looking", StringKind},
		{"legacy-bool", StringKind},
		{"quoted", StringKind},
		{"nested", SequenceKind},
		{"block", StringKind},
	}
	for _, test := range tests {
		node, ok := document.Root.Lookup(test.key)
		if !ok {
			t.Fatalf("missing key %q", test.key)
		}
		if node.Kind != test.kind {
			t.Errorf("%s kind = %s, want %s", test.key, node.Kind, test.kind)
		}
	}

	assertNumberEqualsInt64(t, document, "decimal", 120)
	assertNumberEqualsInt64(t, document, "octal", 15)
	assertNumberEqualsInt64(t, document, "hexadecimal", 58)
	assertNumberEqualsInt64(t, document, "exponent", -200000)
	assertNumberEqualsInt64(t, document, "integral-float", 100)
	assertNumberEqualsInt64(t, document, "negative-zero", 0)
	octal, _ := document.Root.Lookup("octal")
	if got := octal.Number.Source(); got != "0o17" {
		t.Fatalf("octal source = %q, want original spelling", got)
	}

	fraction, _ := document.Root.Lookup("fraction")
	if fraction.Number.IsInteger() {
		t.Fatal(".5 must not satisfy an integer constraint")
	}
	integral, _ := document.Root.Lookup("integral-float")
	if !integral.Number.IsInteger() || integral.Number.Resolution() != FloatResolution {
		t.Fatal("1.00e2 must be a float-resolved value that satisfies integer constraints")
	}
	decimal, _ := document.Root.Lookup("decimal")
	if decimal.Number.Resolution() != IntegerResolution {
		t.Fatal("+00120 must resolve through the Core Schema integer rule")
	}
}

func TestParsePreservesNumbersBeyondNativeRanges(t *testing.T) {
	document := mustParse(t, "huge: 123456789012345678901234567890.000e999999999999999999999\nsmall: 1e-999999999999999999999\n")
	huge, _ := document.Root.Lookup("huge")
	if !huge.Number.IsPositiveInteger() {
		t.Fatal("huge exact value must be a positive integer")
	}
	if got, want := huge.Number.Coefficient().String(), "12345678901234567890123456789"; got != want {
		t.Fatalf("coefficient = %s, want %s", got, want)
	}
	if got, want := huge.Number.Exponent().String(), "1000000000000000000000"; got != want {
		t.Fatalf("exponent = %s, want %s", got, want)
	}
	small, _ := document.Root.Lookup("small")
	if small.Number.IsInteger() {
		t.Fatal("tiny non-zero exact value must not be an integer")
	}
	if huge.Number.Cmp(small.Number) <= 0 {
		t.Fatal("exact comparison produced wrong ordering")
	}
}

func TestNumberComparisonAndRangeUseMathematicalValue(t *testing.T) {
	document := mustParse(t, "a: 1000\nb: 1e3\nc: 1000.000\nd: 1000.001\nversion: 1.0\n")
	a, _ := document.Root.Lookup("a")
	b, _ := document.Root.Lookup("b")
	c, _ := document.Root.Lookup("c")
	d, _ := document.Root.Lookup("d")
	version, _ := document.Root.Lookup("version")
	if a.Number.Cmp(b.Number) != 0 || b.Number.Cmp(c.Number) != 0 {
		t.Fatal("equivalent exact spellings must compare equal")
	}
	if d.Number.Cmp(c.Number) <= 0 {
		t.Fatal("fractional comparison produced wrong ordering")
	}
	if !version.Number.IsIntegerInRange(1, 1) {
		t.Fatal("1.0 must satisfy an integer range by mathematical value")
	}
}

func TestNumberComparisonSignsAndDecimalAlignment(t *testing.T) {
	tests := []struct {
		left  string
		right string
		want  int
	}{
		{"9e3", "1e3", 1},
		{"999", "1e3", -1},
		{"1.000001", "1", 1},
		{"-1.000001", "-1", -1},
		{"-999", "-1e3", 1},
		{"12e3", "1.3e4", -1},
	}
	for _, test := range tests {
		document := mustParse(t, "left: "+test.left+"\nright: "+test.right+"\n")
		left, _ := document.Root.Lookup("left")
		right, _ := document.Root.Lookup("right")
		if got := left.Number.Cmp(right.Number); got != test.want {
			t.Errorf("Cmp(%s, %s) = %d, want %d", test.left, test.right, got, test.want)
		}
	}
}

func TestJSONValueUsesJSONNumberWithoutPrecisionLoss(t *testing.T) {
	document := mustParse(t, "value: 123456789012345678901234567890.125\n")
	value := document.JSONValue()["value"]
	number, ok := value.(json.Number)
	if !ok {
		t.Fatalf("value type = %T, want json.Number", value)
	}
	want, _ := new(big.Rat).SetString("123456789012345678901234567890.125")
	got, ok := new(big.Rat).SetString(number.String())
	if !ok || got.Cmp(want) != 0 {
		t.Fatalf("JSON number %q lost precision", number)
	}
}

func TestParseRejectsProfileViolations(t *testing.T) {
	tests := []struct {
		name    string
		source  string
		problem Problem
		line    int
		column  int
	}{
		{"empty stream", "# only a comment\n", ProblemDocumentCount, 0, 0},
		{"multiple documents", "a: 1\n---\nb: 2\n", ProblemDocumentCount, 2, 1},
		{"root sequence", "[one, two]\n", ProblemRootKind, 1, 1},
		{"directive", "%YAML 1.2\n---\na: b\n", ProblemDirective, 1, 1},
		{"explicit standard tag", "a: !!str value\n", ProblemExplicitTag, 1, 4},
		{"explicit nonspecific tag", "a: ! value\n", ProblemExplicitTag, 1, 4},
		{"explicit collection tag", "a: ! {b: c}\n", ProblemExplicitTag, 1, 4},
		{"anchor", "a: &saved value\n", ProblemAnchor, 1, 4},
		{"merge key", "target: {<<: {a: b}}\n", ProblemMergeKey, 1, 10},
		{"boolean key", "true: value\n", ProblemKeyType, 1, 1},
		{"sequence key", "? [a, b]\n: value\n", ProblemKeyType, 1, 3},
		{"duplicate key", "outer:\n  key: one\n  \"key\": two\n", ProblemDuplicateKey, 3, 3},
		{"infinity", "value: -.iNf\n", ProblemNonFinite, 1, 8},
		{"nan", "value: +.NaN\n", ProblemNonFinite, 1, 8},
		{"syntax", "a: [\n", ProblemSyntax, 1, 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Parse([]byte(test.source))
			if err == nil {
				t.Fatal("Parse unexpectedly succeeded")
			}
			var parseErr *ParseError
			if !errors.As(err, &parseErr) {
				t.Fatalf("error type = %T, want *ParseError", err)
			}
			if parseErr.Problem != test.problem {
				t.Fatalf("problem = %s, want %s (%v)", parseErr.Problem, test.problem, err)
			}
			if test.line != 0 && parseErr.Position.Line != test.line {
				t.Errorf("line = %d, want %d", parseErr.Position.Line, test.line)
			}
			if test.column != 0 && parseErr.Position.Column != test.column {
				t.Errorf("column = %d, want %d", parseErr.Position.Column, test.column)
			}
		})
	}
}

func TestQuotedMergeKeyAndScalarLookalikesAreStrings(t *testing.T) {
	document := mustParse(t, `"<<": allowed
tag-like: "!!str"
infinity: ".inf"
mixed-bool: TrUe
signed-hex: -0xA
`)
	want := map[string]string{
		"<<":         "allowed",
		"tag-like":   "!!str",
		"infinity":   ".inf",
		"mixed-bool": "TrUe",
		"signed-hex": "-0xA",
	}
	for key, value := range want {
		node, ok := document.Root.Lookup(key)
		if !ok || node.Kind != StringKind || node.String != value {
			t.Errorf("%q = %#v, want string %q", key, node, value)
		}
	}
}

func TestMappingOrderAndLocationsArePreserved(t *testing.T) {
	document := mustParse(t, "first: one\nsecond:\n  nested: two\n")
	gotKeys := make([]string, len(document.Root.Mapping))
	for i := range document.Root.Mapping {
		gotKeys[i] = document.Root.Mapping[i].Key
	}
	if want := []string{"first", "second"}; !reflect.DeepEqual(gotKeys, want) {
		t.Fatalf("mapping order = %v, want %v", gotKeys, want)
	}
	second, _ := document.Root.Lookup("second")
	nested, _ := second.Lookup("nested")
	if nested.Position != (Position{Line: 3, Column: 11}) {
		t.Fatalf("nested value position = %+v", nested.Position)
	}
}

func TestRequiredStringASCIIWhitespace(t *testing.T) {
	for _, source := range []string{"value: \"\"\n", "value: \" \\t\\n\"\n"} {
		document := mustParse(t, source)
		value, _ := document.Root.Lookup("value")
		if IsNonEmptyRequiredString(value) {
			t.Fatalf("%q must be empty after ASCII trimming", value.String)
		}
	}
	document := mustParse(t, "value: \"\u00a0\"\n")
	value, _ := document.Root.Lookup("value")
	if !IsNonEmptyRequiredString(value) {
		t.Fatal("non-ASCII whitespace must not be trimmed")
	}
}

func TestCommentsAfterDocumentDoNotCreateAnotherDocument(t *testing.T) {
	document := mustParse(t, "---\na: b\n...\n# trailing comment\n")
	if value, ok := document.Root.Lookup("a"); !ok || value.String != "b" {
		t.Fatal("single explicit document did not parse")
	}
}

func TestPercentOutsideDirectivePositionIsNotPreemptivelyRejected(t *testing.T) {
	_, err := Parse([]byte("{value: \"first\n%second\"}\n"))
	if err == nil {
		return
	}
	var parseErr *ParseError
	if errors.As(err, &parseErr) && parseErr.Problem == ProblemDirective {
		t.Fatalf("column-one percent outside the stream prelude was treated as a directive: %v", err)
	}
}

func TestExplicitTagDetectionUsesRuneColumns(t *testing.T) {
	_, err := Parse([]byte("ключ: ! value\n"))
	var parseErr *ParseError
	if !errors.As(err, &parseErr) || parseErr.Problem != ProblemExplicitTag {
		t.Fatalf("error = %v, want explicit-tag", err)
	}
}

func mustParse(t *testing.T, source string) *Document {
	t.Helper()
	document, err := Parse([]byte(strings.TrimPrefix(source, "\n")))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return document
}

func assertNumberEqualsInt64(t *testing.T, document *Document, key string, want int64) {
	t.Helper()
	node, ok := document.Root.Lookup(key)
	if !ok || node.Kind != NumberKind {
		t.Fatalf("%s is not a number", key)
	}
	if got := node.Number.CmpInt64(want); got != 0 {
		t.Fatalf("%s = %s, want %d", key, node.Number, want)
	}
}
