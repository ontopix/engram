// Package markdownprofile contains the CommonMark-facing adapter used by the
// portable engram checker. It keeps Goldmark behind a small source-oriented
// API so library AST details never become part of the conformance model.
package markdownprofile

import (
	"bytes"
	"sort"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// Span is a half-open byte range in the supplied Markdown source.
type Span struct {
	Start int
	End   int
}

// Link is a CommonMark link or image destination after CommonMark escape and
// character-reference processing.
type Link struct {
	Destination string
	Image       bool
}

// Heading is an ATX heading with its source content after removal of opening
// and optional closing markers and their structural whitespace. Inline markup
// remains uninterpreted in Source.
type Heading struct {
	Level  int
	Source string
}

// Wikilink is one complete bytewise [[...]] candidate outside code. Raw and
// Span preserve the exact occurrence for diagnostics and lossless rewrites.
// Grammar validation and resolution are deliberately handled by the portable
// link layer rather than the CommonMark adapter.
type Wikilink struct {
	Span Span
	Raw  string
}

// Document is the source-derived information needed by engram validation.
type Document struct {
	Links     []Link
	Headings  []Heading
	Wikilinks []Wikilink
}

// Parse parses source as CommonMark and extracts deterministic, source-backed
// observations. CommonMark itself is total for arbitrary byte strings; callers
// perform engram UTF-8 and line-ending validation before invoking Parse.
func Parse(source []byte) Document {
	markdown := goldmark.New(
		goldmark.WithParserOptions(parser.WithAutoHeadingID()),
	)
	root := markdown.Parser().Parse(text.NewReader(source))

	var result Document
	var excluded []Span
	_ = ast.Walk(root, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch typed := node.(type) {
		case *ast.Link:
			result.Links = append(result.Links, Link{Destination: commonMarkDestination(typed.Destination)})
		case *ast.Image:
			result.Links = append(result.Links, Link{Destination: commonMarkDestination(typed.Destination), Image: true})
		case *ast.AutoLink:
			result.Links = append(result.Links, Link{Destination: string(typed.URL(source))})
		case *ast.Heading:
			if heading, ok := atxHeading(source, typed); ok {
				result.Headings = append(result.Headings, heading)
			}
		case *ast.CodeSpan:
			if span, ok := inlineCodeSpan(source, typed); ok {
				excluded = append(excluded, span)
			}
		case *ast.CodeBlock:
			excluded = append(excluded, blockLineSpans(source, typed.Lines())...)
		case *ast.FencedCodeBlock:
			spans := blockLineSpans(source, typed.Lines())
			if typed.Info != nil {
				spans = append(spans, physicalLine(source, typed.Info.Segment.Start))
			}
			excluded = append(excluded, spans...)
		}
		return ast.WalkContinue, nil
	})

	for _, allowed := range complement(len(source), mergeSpans(excluded)) {
		result.Wikilinks = append(result.Wikilinks, scanWikilinks(source, allowed)...)
	}
	return result
}

func commonMarkDestination(source []byte) string {
	value := util.UnescapePunctuations(source)
	value = util.ResolveNumericReferences(value)
	value = util.ResolveEntityNames(value)
	return string(value)
}

func atxHeading(source []byte, heading *ast.Heading) (Heading, bool) {
	if heading.Lines().Len() == 0 {
		return Heading{}, false
	}
	segment := heading.Lines().At(0)
	lineStart := bytes.LastIndexByte(source[:segment.Start], '\n') + 1
	prefix := source[lineStart:segment.Start]
	i := 0
	for i < len(prefix) && i < 3 && prefix[i] == ' ' {
		i++
	}
	markerStart := i
	for i < len(prefix) && prefix[i] == '#' {
		i++
	}
	if i-markerStart != heading.Level || heading.Level < 1 || heading.Level > 6 {
		return Heading{}, false
	}
	if i >= len(prefix) || prefix[i] != ' ' && prefix[i] != '\t' {
		return Heading{}, false
	}
	return Heading{Level: heading.Level, Source: string(segment.Value(source))}, true
}

func inlineCodeSpan(source []byte, span *ast.CodeSpan) (Span, bool) {
	if span.FirstChild() == nil || span.LastChild() == nil {
		return Span{}, false
	}
	first, firstOK := span.FirstChild().(*ast.Text)
	last, lastOK := span.LastChild().(*ast.Text)
	if !firstOK || !lastOK {
		return Span{}, false
	}
	start := first.Segment.Start
	for start > 0 && source[start-1] == '`' {
		start--
	}
	end := last.Segment.Stop
	for end < len(source) && source[end] == '`' {
		end++
	}
	if start == first.Segment.Start || end == last.Segment.Stop {
		return Span{}, false
	}
	return Span{Start: start, End: end}, true
}

func blockLineSpans(source []byte, segments *text.Segments) []Span {
	spans := make([]Span, 0, segments.Len())
	for index := 0; index < segments.Len(); index++ {
		spans = append(spans, physicalLine(source, segments.At(index).Start))
	}
	return spans
}

func physicalLine(source []byte, position int) Span {
	if position < 0 {
		position = 0
	}
	if position > len(source) {
		position = len(source)
	}
	start := bytes.LastIndexByte(source[:position], '\n') + 1
	relativeEnd := bytes.IndexByte(source[position:], '\n')
	if relativeEnd < 0 {
		return Span{Start: start, End: len(source)}
	}
	return Span{Start: start, End: position + relativeEnd + 1}
}

func mergeSpans(spans []Span) []Span {
	filtered := spans[:0]
	for _, span := range spans {
		if span.Start < 0 {
			span.Start = 0
		}
		if span.End > span.Start {
			filtered = append(filtered, span)
		}
	}
	sort.Slice(filtered, func(i, j int) bool {
		if filtered[i].Start != filtered[j].Start {
			return filtered[i].Start < filtered[j].Start
		}
		return filtered[i].End < filtered[j].End
	})
	merged := filtered[:0]
	for _, span := range filtered {
		if len(merged) == 0 || span.Start > merged[len(merged)-1].End {
			merged = append(merged, span)
			continue
		}
		if span.End > merged[len(merged)-1].End {
			merged[len(merged)-1].End = span.End
		}
	}
	return merged
}

func complement(length int, excluded []Span) []Span {
	allowed := make([]Span, 0, len(excluded)+1)
	position := 0
	for _, span := range excluded {
		start := min(max(span.Start, 0), length)
		end := min(max(span.End, start), length)
		if position < start {
			allowed = append(allowed, Span{Start: position, End: start})
		}
		if end > position {
			position = end
		}
	}
	if position < length {
		allowed = append(allowed, Span{Start: position, End: length})
	}
	return allowed
}

func scanWikilinks(source []byte, allowed Span) []Wikilink {
	var result []Wikilink
	position := allowed.Start
	for position < allowed.End {
		relativeOpen := bytes.Index(source[position:allowed.End], []byte("[["))
		if relativeOpen < 0 {
			break
		}
		start := position + relativeOpen
		relativeClose := bytes.Index(source[start+2:allowed.End], []byte("]]"))
		if relativeClose < 0 {
			break
		}
		end := start + 2 + relativeClose + 2
		result = append(result, Wikilink{
			Span: Span{Start: start, End: end},
			Raw:  string(source[start:end]),
		})
		position = end
	}
	return result
}
