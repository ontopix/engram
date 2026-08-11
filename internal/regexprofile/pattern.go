// Package regexprofile validates and compiles engram's portable JSON Schema
// pattern subset.
package regexprofile

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Compile validates source against the engram portable subset and returns an
// equivalent Go regular expression. The validated language uses ASCII syntax
// but matches over Unicode scalar values.
func Compile(source string) (*regexp.Regexp, error) {
	if source == "" {
		return nil, fmt.Errorf("empty pattern")
	}
	for _, r := range source {
		if r < 0x20 || r > 0x7e {
			return nil, fmt.Errorf("pattern syntax must be printable ASCII")
		}
	}

	p := parser{source: source}
	var out strings.Builder
	if strings.HasPrefix(source, "^") {
		out.WriteByte('^')
		p.position++
	}
	p.end = len(source)
	if p.end > p.position && source[p.end-1] == '$' && !escapedAt(source, p.end-1) {
		p.end--
		p.trailingAnchor = true
	}

	if err := p.expression(&out, 0); err != nil {
		return nil, err
	}
	if p.position != p.end {
		return nil, fmt.Errorf("unexpected %q at byte %d", source[p.position], p.position)
	}
	if p.trailingAnchor {
		out.WriteByte('$')
	}

	compiled, err := regexp.Compile(out.String())
	if err != nil {
		return nil, fmt.Errorf("compile validated pattern: %w", err)
	}
	return compiled, nil
}

// Valid reports whether source belongs to the portable subset.
func Valid(source string) bool {
	_, err := Compile(source)
	return err == nil
}

type parser struct {
	source         string
	position       int
	end            int
	trailingAnchor bool
}

func (p *parser) expression(out *strings.Builder, terminator byte) error {
	if !p.hasAtomStart(terminator) {
		return fmt.Errorf("empty alternative at byte %d", p.position)
	}
	for {
		if err := p.concatenation(out, terminator); err != nil {
			return err
		}
		if p.position >= p.end || p.source[p.position] != '|' {
			return nil
		}
		out.WriteByte('|')
		p.position++
		if !p.hasAtomStart(terminator) {
			return fmt.Errorf("empty alternative at byte %d", p.position)
		}
	}
}

func (p *parser) concatenation(out *strings.Builder, terminator byte) error {
	for p.position < p.end {
		current := p.source[p.position]
		if current == '|' || current == terminator && terminator != 0 {
			return nil
		}
		if err := p.repetition(out); err != nil {
			return err
		}
	}
	return nil
}

func (p *parser) repetition(out *strings.Builder) error {
	if err := p.atom(out); err != nil {
		return err
	}
	if p.position >= p.end {
		return nil
	}
	switch p.source[p.position] {
	case '?', '*', '+':
		out.WriteByte(p.source[p.position])
		p.position++
	case '{':
		quantifier, err := p.quantifier()
		if err != nil {
			return err
		}
		out.WriteString(quantifier)
	}
	return nil
}

func (p *parser) atom(out *strings.Builder) error {
	if p.position >= p.end {
		return fmt.Errorf("missing atom at byte %d", p.position)
	}
	current := p.source[p.position]
	switch current {
	case '(':
		p.position++
		out.WriteByte('(')
		if err := p.expression(out, ')'); err != nil {
			return err
		}
		if p.position >= p.end || p.source[p.position] != ')' {
			return fmt.Errorf("unclosed group at byte %d", p.position)
		}
		p.position++
		out.WriteByte(')')
		return nil
	case '[':
		return p.characterClass(out)
	case '\\':
		literal, err := p.escapedLiteral()
		if err != nil {
			return err
		}
		out.WriteString(regexp.QuoteMeta(string(literal)))
		return nil
	case '.', '^', '$', ')', ']', '{', '}', '?', '*', '+', '|':
		return fmt.Errorf("unsupported or misplaced %q at byte %d", current, p.position)
	default:
		p.position++
		out.WriteByte(current)
		return nil
	}
}

func (p *parser) characterClass(out *strings.Builder) error {
	start := p.position
	p.position++
	if p.position >= p.end || p.source[p.position] == ']' {
		return fmt.Errorf("empty or unclosed character class at byte %d", start)
	}
	if p.source[p.position] == '^' {
		return fmt.Errorf("negated character class at byte %d", start)
	}

	out.WriteByte('[')
	items := 0
	for {
		if p.position >= p.end {
			return fmt.Errorf("unclosed character class at byte %d", start)
		}
		if p.source[p.position] == ']' {
			if items == 0 {
				return fmt.Errorf("empty character class at byte %d", start)
			}
			p.position++
			out.WriteByte(']')
			return nil
		}

		first, err := p.classAtom()
		if err != nil {
			return err
		}
		writeClassLiteral(out, first)
		items++
		if p.position < p.end && p.source[p.position] == '-' {
			p.position++
			if p.position >= p.end || p.source[p.position] == ']' {
				return fmt.Errorf("unpaired range marker at byte %d", p.position-1)
			}
			second, err := p.classAtom()
			if err != nil {
				return err
			}
			if first > second {
				return fmt.Errorf("descending range %q-%q", first, second)
			}
			out.WriteByte('-')
			writeClassLiteral(out, second)
			if p.position < p.end && p.source[p.position] == '-' {
				return fmt.Errorf("overlapping range at byte %d", p.position)
			}
		}
	}
}

func (p *parser) classAtom() (byte, error) {
	if p.position >= p.end {
		return 0, fmt.Errorf("missing character-class atom")
	}
	current := p.source[p.position]
	if current == '\\' {
		return p.escapedLiteral()
	}
	if strings.ContainsRune("[]-^", rune(current)) {
		return 0, fmt.Errorf("unescaped %q in character class at byte %d", current, p.position)
	}
	p.position++
	return current, nil
}

func (p *parser) escapedLiteral() (byte, error) {
	start := p.position
	p.position++
	if p.position >= p.end {
		return 0, fmt.Errorf("trailing escape at byte %d", start)
	}
	literal := p.source[p.position]
	if !asciiPunctuation(literal) {
		return 0, fmt.Errorf("escape target %q is not ASCII punctuation", literal)
	}
	p.position++
	return literal, nil
}

func (p *parser) quantifier() (string, error) {
	start := p.position
	p.position++
	minimum, err := p.bound()
	if err != nil {
		return "", err
	}
	if p.position >= p.end {
		return "", fmt.Errorf("unclosed quantifier at byte %d", start)
	}
	if p.source[p.position] == '}' {
		p.position++
		return p.source[start:p.position], nil
	}
	if p.source[p.position] != ',' {
		return "", fmt.Errorf("invalid quantifier at byte %d", start)
	}
	p.position++
	if p.position < p.end && p.source[p.position] == '}' {
		p.position++
		return p.source[start:p.position], nil
	}
	maximum, err := p.bound()
	if err != nil {
		return "", err
	}
	if p.position >= p.end || p.source[p.position] != '}' {
		return "", fmt.Errorf("unclosed quantifier at byte %d", start)
	}
	if minimum > maximum {
		return "", fmt.Errorf("descending quantifier bounds at byte %d", start)
	}
	p.position++
	return p.source[start:p.position], nil
}

func (p *parser) bound() (uint64, error) {
	start := p.position
	for p.position < p.end && p.source[p.position] >= '0' && p.source[p.position] <= '9' {
		p.position++
	}
	if start == p.position {
		return 0, fmt.Errorf("missing quantifier bound at byte %d", start)
	}
	text := p.source[start:p.position]
	if len(text) > 1 && text[0] == '0' {
		return 0, fmt.Errorf("non-canonical quantifier bound %q", text)
	}
	value, err := strconv.ParseUint(text, 10, 16)
	if err != nil || value > 65535 {
		return 0, fmt.Errorf("quantifier bound %q exceeds 65535", text)
	}
	return value, nil
}

func (p *parser) hasAtomStart(terminator byte) bool {
	if p.position >= p.end {
		return false
	}
	current := p.source[p.position]
	return current != '|' && !(terminator != 0 && current == terminator)
}

func escapedAt(source string, position int) bool {
	backslashes := 0
	for i := position - 1; i >= 0 && source[i] == '\\'; i-- {
		backslashes++
	}
	return backslashes%2 == 1
}

func asciiPunctuation(value byte) bool {
	return value >= 0x21 && value <= 0x2f ||
		value >= 0x3a && value <= 0x40 ||
		value >= 0x5b && value <= 0x60 ||
		value >= 0x7b && value <= 0x7e
}

func writeClassLiteral(out *strings.Builder, value byte) {
	if strings.ContainsRune("\\[]-^", rune(value)) {
		out.WriteByte('\\')
	}
	out.WriteByte(value)
}
