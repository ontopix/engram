// Package yamlprofile parses the restricted YAML 1.2.2 Core Schema profile
// shared by engram's manifest, frontmatter, and hook protocol formats.
package yamlprofile

import (
	"bytes"
	"fmt"
	"io"
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"
)

// Kind is one of the value kinds in the restricted YAML/JSON data model.
type Kind uint8

const (
	InvalidKind Kind = iota
	NullKind
	BooleanKind
	NumberKind
	StringKind
	SequenceKind
	MappingKind
)

func (k Kind) String() string {
	switch k {
	case NullKind:
		return "null"
	case BooleanKind:
		return "boolean"
	case NumberKind:
		return "number"
	case StringKind:
		return "string"
	case SequenceKind:
		return "sequence"
	case MappingKind:
		return "mapping"
	default:
		return "invalid"
	}
}

// Position is a one-based source location. A zero position means that the
// underlying YAML parser could not attribute an error more precisely.
type Position struct {
	Line   int
	Column int
}

// Member preserves a mapping member's source order and key location.
type Member struct {
	Key         string
	KeyPosition Position
	Value       *Node
}

// Node is a lossless-with-respect-to-values representation of the admitted
// YAML/JSON data model. Numbers remain exact and mapping order is preserved.
type Node struct {
	Kind     Kind
	Position Position

	String   string
	Boolean  bool
	Number   *Number
	Sequence []*Node
	Mapping  []Member
}

// Lookup returns the value associated with key in a mapping node.
func (n *Node) Lookup(key string) (*Node, bool) {
	if n == nil || n.Kind != MappingKind {
		return nil, false
	}
	for i := range n.Mapping {
		if n.Mapping[i].Key == key {
			return n.Mapping[i].Value, true
		}
	}
	return nil, false
}

// JSONValue converts a parsed node into the ordinary Go representation used
// by JSON consumers. Numeric values are json.Number spellings generated from
// their exact decimal representation, never float64.
func (n *Node) JSONValue() any {
	if n == nil {
		return nil
	}
	switch n.Kind {
	case NullKind:
		return nil
	case BooleanKind:
		return n.Boolean
	case NumberKind:
		return n.Number.JSONNumber()
	case StringKind:
		return n.String
	case SequenceKind:
		result := make([]any, len(n.Sequence))
		for i := range n.Sequence {
			result[i] = n.Sequence[i].JSONValue()
		}
		return result
	case MappingKind:
		result := make(map[string]any, len(n.Mapping))
		for i := range n.Mapping {
			result[n.Mapping[i].Key] = n.Mapping[i].Value.JSONValue()
		}
		return result
	default:
		return nil
	}
}

// Document is one successfully parsed profile document.
type Document struct {
	Root *Node
}

// JSONValue returns the document root in the ordinary Go JSON data model.
func (d *Document) JSONValue() map[string]any {
	if d == nil || d.Root == nil || d.Root.Kind != MappingKind {
		return nil
	}
	value, _ := d.Root.JSONValue().(map[string]any)
	return value
}

// Problem classifies a common-profile parse failure. Artifact validators map
// any of these problems to the finding code appropriate for that artifact
// (for example E105, E201, E209, or E303).
type Problem string

const (
	ProblemSyntax        Problem = "syntax"
	ProblemDocumentCount Problem = "document-count"
	ProblemRootKind      Problem = "root-kind"
	ProblemDirective     Problem = "directive"
	ProblemExplicitTag   Problem = "explicit-tag"
	ProblemAnchor        Problem = "anchor"
	ProblemAlias         Problem = "alias"
	ProblemMergeKey      Problem = "merge-key"
	ProblemKeyType       Problem = "key-type"
	ProblemDuplicateKey  Problem = "duplicate-key"
	ProblemNonFinite     Problem = "non-finite"
	ProblemDataModel     Problem = "data-model"
)

// ParseError is a strict profile violation with an optional source position.
type ParseError struct {
	Problem  Problem
	Position Position
	Detail   string
}

func (e *ParseError) Error() string {
	if e == nil {
		return "<nil>"
	}
	location := ""
	if e.Position.Line > 0 {
		location = fmt.Sprintf(" at line %d", e.Position.Line)
		if e.Position.Column > 0 {
			location += fmt.Sprintf(", column %d", e.Position.Column)
		}
	}
	if e.Detail == "" {
		return string(e.Problem) + location
	}
	return string(e.Problem) + location + ": " + e.Detail
}

// Parse decodes exactly one YAML 1.2.2 document and enforces engram's common
// YAML profile. The root must be a mapping.
func Parse(source []byte) (*Document, error) {
	index := newSourceIndex(source)
	if position, ok := index.directivePosition(); ok {
		return nil, parseError(ProblemDirective, position, "YAML directives are forbidden")
	}

	decoder := yaml.NewDecoder(bytes.NewReader(source))
	var raw yaml.Node
	if err := decoder.Decode(&raw); err != nil {
		if err == io.EOF {
			return nil, parseError(ProblemDocumentCount, Position{}, "expected exactly one document, found none")
		}
		return nil, syntaxError(err)
	}

	var extra yaml.Node
	if err := decoder.Decode(&extra); err == nil {
		return nil, parseError(ProblemDocumentCount, rawPosition(&extra), "expected exactly one document, found more than one")
	} else if err != io.EOF {
		return nil, syntaxError(err)
	}

	if raw.Kind != yaml.DocumentNode || len(raw.Content) != 1 {
		return nil, parseError(ProblemDocumentCount, rawPosition(&raw), "invalid YAML document representation")
	}
	if raw.Content[0].Kind != yaml.MappingNode {
		return nil, parseError(ProblemRootKind, rawPosition(raw.Content[0]), "document root must be a mapping")
	}

	root, err := convertNode(raw.Content[0], index)
	if err != nil {
		return nil, err
	}
	return &Document{Root: root}, nil
}

// TrimASCIIWhitespace removes exactly U+0009 through U+000D and U+0020 from
// the two ends of a string, as required for mandatory string fields.
func TrimASCIIWhitespace(value string) string {
	return strings.TrimFunc(value, func(r rune) bool {
		return r >= '\t' && r <= '\r' || r == ' '
	})
}

// IsNonEmptyRequiredString applies the common profile's mandatory-string
// rule to a node.
func IsNonEmptyRequiredString(node *Node) bool {
	return node != nil && node.Kind == StringKind && TrimASCIIWhitespace(node.String) != ""
}

func convertNode(raw *yaml.Node, index sourceIndex) (*Node, error) {
	position := rawPosition(raw)
	if raw.Anchor != "" {
		return nil, parseError(ProblemAnchor, position, "anchors are forbidden")
	}
	if raw.Style&yaml.TaggedStyle != 0 || index.runeAt(position) == '!' {
		return nil, parseError(ProblemExplicitTag, position, "explicit tags are forbidden")
	}
	if raw.Kind == yaml.AliasNode {
		return nil, parseError(ProblemAlias, position, "aliases are forbidden")
	}

	switch raw.Kind {
	case yaml.MappingNode:
		if len(raw.Content)%2 != 0 {
			return nil, parseError(ProblemSyntax, position, "mapping has an unmatched key")
		}
		node := &Node{Kind: MappingKind, Position: position, Mapping: make([]Member, 0, len(raw.Content)/2)}
		seen := make(map[string]struct{}, len(raw.Content)/2)
		for i := 0; i < len(raw.Content); i += 2 {
			rawKey := raw.Content[i]
			keyPosition := rawPosition(rawKey)
			if rawKey.Anchor != "" {
				return nil, parseError(ProblemAnchor, keyPosition, "anchors are forbidden")
			}
			if rawKey.Style&yaml.TaggedStyle != 0 || index.runeAt(keyPosition) == '!' {
				return nil, parseError(ProblemExplicitTag, keyPosition, "explicit tags are forbidden")
			}
			if rawKey.Kind == yaml.AliasNode {
				return nil, parseError(ProblemAlias, keyPosition, "aliases are forbidden")
			}
			if rawKey.ShortTag() == "!!merge" {
				return nil, parseError(ProblemMergeKey, keyPosition, "the YAML merge key is forbidden")
			}
			key, err := convertScalar(rawKey)
			if err != nil {
				return nil, err
			}
			if key.Kind != StringKind {
				return nil, parseError(ProblemKeyType, keyPosition, "mapping keys must be strings")
			}
			if _, duplicate := seen[key.String]; duplicate {
				return nil, parseError(ProblemDuplicateKey, keyPosition, fmt.Sprintf("duplicate key %q", key.String))
			}
			seen[key.String] = struct{}{}

			value, err := convertNode(raw.Content[i+1], index)
			if err != nil {
				return nil, err
			}
			node.Mapping = append(node.Mapping, Member{Key: key.String, KeyPosition: keyPosition, Value: value})
		}
		return node, nil

	case yaml.SequenceNode:
		node := &Node{Kind: SequenceKind, Position: position, Sequence: make([]*Node, len(raw.Content))}
		for i := range raw.Content {
			child, err := convertNode(raw.Content[i], index)
			if err != nil {
				return nil, err
			}
			node.Sequence[i] = child
		}
		return node, nil

	case yaml.ScalarNode:
		return convertScalar(raw)

	default:
		return nil, parseError(ProblemDataModel, position, "node kind is outside the JSON data model")
	}
}

func convertScalar(raw *yaml.Node) (*Node, error) {
	position := rawPosition(raw)
	if raw.Kind != yaml.ScalarNode {
		return nil, parseError(ProblemKeyType, position, "mapping keys must be scalar strings")
	}

	if raw.Style&(yaml.DoubleQuotedStyle|yaml.SingleQuotedStyle|yaml.LiteralStyle|yaml.FoldedStyle) != 0 {
		return &Node{Kind: StringKind, Position: position, String: raw.Value}, nil
	}

	value := raw.Value
	switch value {
	case "", "~", "null", "Null", "NULL":
		return &Node{Kind: NullKind, Position: position}, nil
	case "true", "True", "TRUE":
		return &Node{Kind: BooleanKind, Position: position, Boolean: true}, nil
	case "false", "False", "FALSE":
		return &Node{Kind: BooleanKind, Position: position, Boolean: false}, nil
	}

	if isNonFinite(value) {
		return nil, parseError(ProblemNonFinite, position, "non-finite YAML numbers are forbidden")
	}
	if decimalIntegerPattern.MatchString(value) {
		return numberNode(value, value, 10, IntegerResolution, position)
	}
	if octalIntegerPattern.MatchString(value) {
		return numberNode(value, value[2:], 8, IntegerResolution, position)
	}
	if hexadecimalIntegerPattern.MatchString(value) {
		return numberNode(value, value[2:], 16, IntegerResolution, position)
	}
	if finiteNumberPattern.MatchString(value) {
		number, ok := parseFiniteDecimal(value)
		if !ok {
			return nil, parseError(ProblemDataModel, position, "could not represent exact finite number")
		}
		return &Node{Kind: NumberKind, Position: position, Number: number}, nil
	}
	return &Node{Kind: StringKind, Position: position, String: value}, nil
}

var (
	decimalIntegerPattern     = regexp.MustCompile(`^[+-]?[0-9]+$`)
	octalIntegerPattern       = regexp.MustCompile(`^0o[0-7]+$`)
	hexadecimalIntegerPattern = regexp.MustCompile(`^0x[0-9a-fA-F]+$`)
	finiteNumberPattern       = regexp.MustCompile(`^[+-]?(?:\.[0-9]+|[0-9]+(?:\.[0-9]*)?)(?:[eE][+-]?[0-9]+)?$`)
	yamlLinePattern           = regexp.MustCompile(`(?:^| )line ([0-9]+)(?:: column ([0-9]+))?`)
)

func numberNode(source, digits string, base int, resolution NumberResolution, position Position) (*Node, error) {
	number := &Number{spelling: source, resolution: resolution}
	if _, ok := number.coefficient.SetString(digits, base); !ok {
		return nil, parseError(ProblemDataModel, position, "could not represent exact integer")
	}
	number.normalize()
	return &Node{Kind: NumberKind, Position: position, Number: number}, nil
}

func parseFiniteDecimal(source string) (*Number, bool) {
	number := &Number{spelling: source, resolution: FloatResolution}
	mantissa := source
	exponentText := "0"
	if at := strings.IndexAny(mantissa, "eE"); at >= 0 {
		exponentText = mantissa[at+1:]
		mantissa = mantissa[:at]
	}
	if _, ok := number.exponent.SetString(exponentText, 10); !ok {
		return nil, false
	}

	negative := strings.HasPrefix(mantissa, "-")
	if strings.HasPrefix(mantissa, "+") || negative {
		mantissa = mantissa[1:]
	}
	fractionDigits := 0
	if dot := strings.IndexByte(mantissa, '.'); dot >= 0 {
		fractionDigits = len(mantissa) - dot - 1
		mantissa = mantissa[:dot] + mantissa[dot+1:]
	}
	if mantissa == "" {
		return nil, false
	}
	if _, ok := number.coefficient.SetString(mantissa, 10); !ok {
		return nil, false
	}
	if negative {
		number.coefficient.Neg(&number.coefficient)
	}
	number.exponent.Sub(&number.exponent, big.NewInt(int64(fractionDigits)))
	number.normalize()
	return number, true
}

func isNonFinite(value string) bool {
	if strings.HasPrefix(value, "+") || strings.HasPrefix(value, "-") {
		value = value[1:]
	}
	return strings.EqualFold(value, ".inf") || strings.EqualFold(value, ".nan")
}

func rawPosition(node *yaml.Node) Position {
	if node == nil {
		return Position{}
	}
	if node.Line > 0 {
		return Position{Line: node.Line, Column: node.Column}
	}
	if len(node.Content) > 0 {
		return rawPosition(node.Content[0])
	}
	return Position{}
}

func parseError(problem Problem, position Position, detail string) *ParseError {
	return &ParseError{Problem: problem, Position: position, Detail: detail}
}

func syntaxError(err error) *ParseError {
	position := Position{}
	match := yamlLinePattern.FindStringSubmatch(err.Error())
	if len(match) > 1 {
		position.Line, _ = strconv.Atoi(match[1])
		if len(match) > 2 && match[2] != "" {
			position.Column, _ = strconv.Atoi(match[2])
		}
	}
	return parseError(ProblemSyntax, position, err.Error())
}

type sourceIndex [][]rune

func newSourceIndex(source []byte) sourceIndex {
	lines := bytes.Split(source, []byte{'\n'})
	index := make(sourceIndex, len(lines))
	for i := range lines {
		index[i] = []rune(string(lines[i]))
	}
	return index
}

func (s sourceIndex) directivePosition() (Position, bool) {
	// Directives are possible only in the stream prelude, before the first
	// document-start marker or content node. Restricting the scan to that
	// position avoids mistaking a column-one '%' inside a multi-line flow
	// scalar for a directive.
	for lineNumber, original := range s {
		line := original
		if lineNumber == 0 && len(line) > 0 && line[0] == '\ufeff' {
			line = line[1:]
		}
		trimmed := strings.TrimLeft(string(line), " \t\r")
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if len(line) > 0 && line[0] == '%' {
			return Position{Line: lineNumber + 1, Column: 1}, true
		}
		return Position{}, false
	}
	return Position{}, false
}

func (s sourceIndex) runeAt(position Position) rune {
	if position.Line <= 0 || position.Line > len(s) || position.Column <= 0 {
		return utf8.RuneError
	}
	line := s[position.Line-1]
	if position.Column > len(line) {
		return utf8.RuneError
	}
	return line[position.Column-1]
}
