package documentprofile

import (
	"bytes"
	"fmt"

	"github.com/ontopix/engram/internal/markdownprofile"
	"github.com/ontopix/engram/internal/yamlprofile"
)

const frontmatterDelimiter = "---\n"

// Sections are half-open byte ranges in one complete source document.
// Frontmatter excludes both delimiter lines and Body begins immediately after
// the closing delimiter's LF.
type Sections struct {
	OpeningDelimiter markdownprofile.Span
	Frontmatter      markdownprofile.Span
	ClosingDelimiter markdownprofile.Span
	Body             markdownprofile.Span
}

// FrontmatterProblem identifies exact delimiter-grammar failures.
type FrontmatterProblem string

const (
	FrontmatterOpening FrontmatterProblem = "invalid-opening-delimiter"
	FrontmatterClosing FrontmatterProblem = "missing-closing-delimiter"
)

// FrontmatterError is a delimiter failure at a zero-based source byte offset.
type FrontmatterError struct {
	Problem FrontmatterProblem
	Offset  int
}

func (e *FrontmatterError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("frontmatter %s at byte %d", e.Problem, e.Offset)
}

// SplitFrontmatter finds the exact opening line and the next exact closing
// line. This function is purely structural; Parse performs normed-text and
// YAML validation around it.
func SplitFrontmatter(source []byte) (Sections, error) {
	if !bytes.HasPrefix(source, []byte(frontmatterDelimiter)) {
		return Sections{}, &FrontmatterError{Problem: FrontmatterOpening, Offset: 0}
	}

	for lineStart := len(frontmatterDelimiter); lineStart < len(source); {
		relativeLF := bytes.IndexByte(source[lineStart:], '\n')
		if relativeLF < 0 {
			break
		}
		lineEnd := lineStart + relativeLF + 1
		if bytes.Equal(source[lineStart:lineEnd], []byte(frontmatterDelimiter)) {
			return Sections{
				OpeningDelimiter: markdownprofile.Span{Start: 0, End: len(frontmatterDelimiter)},
				Frontmatter:      markdownprofile.Span{Start: len(frontmatterDelimiter), End: lineStart},
				ClosingDelimiter: markdownprofile.Span{Start: lineStart, End: lineEnd},
				Body:             markdownprofile.Span{Start: lineEnd, End: len(source)},
			}, nil
		}
		lineStart = lineEnd
	}
	return Sections{}, &FrontmatterError{Problem: FrontmatterClosing, Offset: len(source)}
}

// Document is a parsed frontmatter-bearing Markdown document. Markdown source
// spans are relative to BodyBytes; SourceSpan maps them back to the complete
// source.
type Document struct {
	Sections
	YAML     *yamlprofile.Document
	Markdown markdownprofile.Document

	source []byte
}

// Parse validates normed text, splits exact frontmatter, parses it under the
// common YAML profile, and parses the remaining body as CommonMark.
func Parse(source []byte) (*Document, error) {
	if err := ValidateText(source); err != nil {
		return nil, err
	}
	sections, err := SplitFrontmatter(source)
	if err != nil {
		return nil, err
	}
	yamlDocument, err := yamlprofile.Parse(source[sections.Frontmatter.Start:sections.Frontmatter.End])
	if err != nil {
		return nil, fmt.Errorf("parse frontmatter YAML: %w", err)
	}
	body := source[sections.Body.Start:sections.Body.End]
	return &Document{
		Sections: sections,
		YAML:     yamlDocument,
		Markdown: markdownprofile.Parse(body),
		source:   source,
	}, nil
}

// FrontmatterBytes returns the exact bytes between delimiter lines.
func (d *Document) FrontmatterBytes() []byte {
	if d == nil || !validSpan(d.Frontmatter, len(d.source)) {
		return nil
	}
	return d.source[d.Frontmatter.Start:d.Frontmatter.End]
}

// BodyBytes returns the exact bytes following the closing delimiter.
func (d *Document) BodyBytes() []byte {
	if d == nil || !validSpan(d.Body, len(d.source)) {
		return nil
	}
	return d.source[d.Body.Start:d.Body.End]
}

// SourceSpan translates a body-relative CommonMark span to the complete
// source. The boolean is false for a span outside the body.
func (d *Document) SourceSpan(bodySpan markdownprofile.Span) (markdownprofile.Span, bool) {
	if d == nil || bodySpan.Start < 0 || bodySpan.End < bodySpan.Start || bodySpan.End > d.Body.End-d.Body.Start {
		return markdownprofile.Span{}, false
	}
	return markdownprofile.Span{Start: d.Body.Start + bodySpan.Start, End: d.Body.Start + bodySpan.End}, true
}

// SourcePosition translates a one-based YAML position to a one-based position
// in the complete Markdown file.
func (d *Document) SourcePosition(position yamlprofile.Position) yamlprofile.Position {
	if d == nil || position.Line <= 0 {
		return yamlprofile.Position{}
	}
	return yamlprofile.Position{Line: position.Line + 1, Column: position.Column}
}

func validSpan(span markdownprofile.Span, length int) bool {
	return span.Start >= 0 && span.End >= span.Start && span.End <= length
}
