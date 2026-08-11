package markdownprofile

import (
	"reflect"
	"testing"
)

func TestCommonMarkDestinations(t *testing.T) {
	t.Parallel()
	source := []byte("[escaped](asset\\(one\\).pdf) ![image][id]\n\n[id]: images/pic&amp;.png\n")
	document := Parse(source)
	want := []Link{
		{Destination: "asset(one).pdf"},
		{Destination: "images/pic&.png", Image: true},
	}
	if !reflect.DeepEqual(document.Links, want) {
		t.Fatalf("links = %#v, want %#v", document.Links, want)
	}
}

func TestATXHeadingPreservesInlineSource(t *testing.T) {
	t.Parallel()
	source := []byte("## Decisions ##\n## **Decisions**\nDecisions\n---------\n")
	document := Parse(source)
	want := []Heading{
		{Level: 2, Source: "Decisions"},
		{Level: 2, Source: "**Decisions**"},
	}
	if !reflect.DeepEqual(document.Headings, want) {
		t.Fatalf("headings = %#v, want %#v", document.Headings, want)
	}
}

func TestWikilinksUseSourceRangesAndIgnoreCode(t *testing.T) {
	t.Parallel()
	source := []byte("before [[one]] and `[[inline]]` then \\[[escaped]]\n\n    [[indented]]\n\n```md [[info]]\n[[fenced]]\n```\n\na [[nested [[bad]] tail [[two|label]]\n")
	document := Parse(source)
	wantRaw := []string{"[[one]]", "[[escaped]]", "[[nested [[bad]]", "[[two|label]]"}
	if len(document.Wikilinks) != len(wantRaw) {
		t.Fatalf("wikilinks = %#v", document.Wikilinks)
	}
	for index, want := range wantRaw {
		got := document.Wikilinks[index]
		if got.Raw != want {
			t.Errorf("wikilink %d raw = %q, want %q", index, got.Raw, want)
		}
		if string(source[got.Span.Start:got.Span.End]) != want {
			t.Errorf("wikilink %d span does not reproduce source", index)
		}
	}
}

func TestUnclosedWikilinkIsOrdinaryText(t *testing.T) {
	t.Parallel()
	if got := Parse([]byte("unclosed [[target\n")).Wikilinks; len(got) != 0 {
		t.Fatalf("wikilinks = %#v, want none", got)
	}
}
