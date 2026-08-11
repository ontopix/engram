package documentprofile

import (
	"errors"
	"reflect"
	"testing"

	"github.com/ontopix/engram/internal/markdownprofile"
	"github.com/ontopix/engram/internal/yamlprofile"
)

func TestSplitFrontmatterExactRanges(t *testing.T) {
	t.Parallel()
	source := []byte("---\ntype: note\ndescription: Example\n---\n# Body\n")
	sections, err := SplitFrontmatter(source)
	if err != nil {
		t.Fatal(err)
	}
	want := Sections{
		OpeningDelimiter: markdownprofile.Span{Start: 0, End: 4},
		Frontmatter:      markdownprofile.Span{Start: 4, End: 36},
		ClosingDelimiter: markdownprofile.Span{Start: 36, End: 40},
		Body:             markdownprofile.Span{Start: 40, End: len(source)},
	}
	if !reflect.DeepEqual(sections, want) {
		t.Fatalf("sections = %#v, want %#v", sections, want)
	}
	if got := string(source[sections.Frontmatter.Start:sections.Frontmatter.End]); got != "type: note\ndescription: Example\n" {
		t.Fatalf("frontmatter = %q", got)
	}
	if got := string(source[sections.Body.Start:sections.Body.End]); got != "# Body\n" {
		t.Fatalf("body = %q", got)
	}
}

func TestSplitFrontmatterUsesNextExactWholeLine(t *testing.T) {
	t.Parallel()
	source := []byte("---\nvalue: x---\nother: --- # not a delimiter\n--- \n---\nbody\n")
	sections, err := SplitFrontmatter(source)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(source[sections.ClosingDelimiter.Start:sections.ClosingDelimiter.End]); got != "---\n" {
		t.Fatalf("closing delimiter = %q", got)
	}
	if got := string(source[sections.Body.Start:]); got != "body\n" {
		t.Fatalf("body = %q", got)
	}
}

func TestSplitFrontmatterRejectsDelimiterVariants(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		source  string
		problem FrontmatterProblem
	}{
		{"no opening", "type: note\n---\n", FrontmatterOpening},
		{"opening spaces", "--- \ntype: note\n---\n", FrontmatterOpening},
		{"opening comment", "--- # comment\ntype: note\n---\n", FrontmatterOpening},
		{"closing spaces", "---\ntype: note\n--- \n", FrontmatterClosing},
		{"closing comment", "---\ntype: note\n--- # comment\n", FrontmatterClosing},
		{"no closing", "---\ntype: note\n", FrontmatterClosing},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := SplitFrontmatter([]byte(test.source))
			var frontmatterErr *FrontmatterError
			if !errors.As(err, &frontmatterErr) || frontmatterErr.Problem != test.problem {
				t.Fatalf("error = %v, want %s", err, test.problem)
			}
		})
	}
}

func TestParseDocumentComposesYAMLAndMarkdownRanges(t *testing.T) {
	t.Parallel()
	source := []byte("---\ntype: note\ndescription: Example\n---\ntext [[target]] and `[[code]]`\n")
	document, err := Parse(source)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(document.FrontmatterBytes()); got != "type: note\ndescription: Example\n" {
		t.Fatalf("frontmatter = %q", got)
	}
	if got := string(document.BodyBytes()); got != "text [[target]] and `[[code]]`\n" {
		t.Fatalf("body = %q", got)
	}
	if len(document.Markdown.Wikilinks) != 1 || document.Markdown.Wikilinks[0].Raw != "[[target]]" {
		t.Fatalf("wikilinks = %#v", document.Markdown.Wikilinks)
	}
	bodySpan := document.Markdown.Wikilinks[0].Span
	sourceSpan, ok := document.SourceSpan(bodySpan)
	if !ok || string(source[sourceSpan.Start:sourceSpan.End]) != "[[target]]" {
		t.Fatalf("source span = %#v", sourceSpan)
	}
	typeNode, _ := document.YAML.Root.Lookup("type")
	if got := document.SourcePosition(typeNode.Position); got != (yamlprofile.Position{Line: 2, Column: 7}) {
		t.Fatalf("source position = %+v", got)
	}
}

func TestParseAllowsEmptyBody(t *testing.T) {
	t.Parallel()
	document, err := Parse([]byte("---\n{}\n---\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(document.BodyBytes()) != 0 || document.Body.Start != document.Body.End {
		t.Fatalf("body = %#v", document.Body)
	}
}

func TestParseHonorsTextBeforeStructureAndWrapsYAML(t *testing.T) {
	t.Parallel()
	_, err := Parse([]byte("not frontmatter"))
	var textErr *TextError
	if !errors.As(err, &textErr) || textErr.Problem != TextFinalLF {
		t.Fatalf("error = %v, want final-LF text error", err)
	}

	_, err = Parse([]byte("---\n[]\n---\n"))
	var yamlErr *yamlprofile.ParseError
	if !errors.As(err, &yamlErr) || yamlErr.Problem != yamlprofile.ProblemRootKind {
		t.Fatalf("error = %v, want wrapped YAML root-kind error", err)
	}
}

func TestSourceSpanRejectsOutOfBodyRange(t *testing.T) {
	t.Parallel()
	document, err := Parse([]byte("---\n{}\n---\nbody\n"))
	if err != nil {
		t.Fatal(err)
	}
	for _, span := range []markdownprofile.Span{{Start: -1, End: 0}, {Start: 1, End: 0}, {Start: 0, End: 6}} {
		if _, ok := document.SourceSpan(span); ok {
			t.Fatalf("SourceSpan(%#v) unexpectedly succeeded", span)
		}
	}
}
