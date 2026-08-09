package documentprofile

import (
	"errors"
	"reflect"
	"testing"
)

func TestParseWikilinkValidForms(t *testing.T) {
	t.Parallel()
	tests := []struct {
		raw        string
		target     string
		label      string
		hasLabel   bool
		recordPath string
	}{
		{"[[people/jane-doe]]", "people/jane-doe", "", false, "people/jane-doe.md"},
		{"[[projects/acme/decision.v1]]", "projects/acme/decision.v1", "", false, "projects/acme/decision.v1.md"},
		{"[[café/résumé|Résumé visible]]", "café/résumé", "Résumé visible", true, "café/résumé.md"},
		{"[[target|label|with|bars]]", "target", "label|with|bars", true, "target.md"},
		{"[[target| ]]", "target", " ", true, "target.md"},
		{"[[README]]", "README", "", false, "README.md"},
		{"[[record.md]]", "record.md", "", false, "record.md.md"},
	}
	for _, test := range tests {
		link, err := ParseWikilink(test.raw)
		if err != nil {
			t.Errorf("ParseWikilink(%q): %v", test.raw, err)
			continue
		}
		if link.Target != test.target || link.Label != test.label || link.HasLabel != test.hasLabel || link.RecordPath() != test.recordPath {
			t.Errorf("ParseWikilink(%q) = %#v", test.raw, link)
		}
	}
}

func TestParseWikilinkRejectsInvalidForms(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		raw     string
		problem WikilinkProblem
	}{
		{"not wrapped", "people/jane", WikilinkEnvelope},
		{"trailing candidate bytes", "[[one]]tail]]", WikilinkEnvelope},
		{"empty target", "[[]]", WikilinkTarget},
		{"leading slash", "[[/one]]", WikilinkTarget},
		{"trailing slash", "[[one/]]", WikilinkTarget},
		{"empty segment", "[[one//two]]", WikilinkTarget},
		{"dot segment", "[[one/./two]]", WikilinkTarget},
		{"dotdot segment", "[[one/../two]]", WikilinkTarget},
		{"backslash", `[[one\two]]`, WikilinkTarget},
		{"percent", "[[one%20two]]", WikilinkTarget},
		{"space", "[[one two]]", WikilinkTarget},
		{"nested opener", "[[one[[two]]", WikilinkTarget},
		{"NFD", "[[cafe\u0301]]", WikilinkTarget},
		{"empty label", "[[one|]]", WikilinkLabel},
		{"label open bracket", "[[one|bad[label]]", WikilinkLabel},
		{"label close bracket", "[[one|bad]label]]", WikilinkLabel},
		{"label control", "[[one|bad\tlabel]]", WikilinkLabel},
		{"label line separator", "[[one|bad\u2028label]]", WikilinkLabel},
		{"invalid UTF-8", string([]byte{'[', '[', 0xff, ']', ']'}), WikilinkEnvelope},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := ParseWikilink(test.raw)
			var linkErr *WikilinkError
			if !errors.As(err, &linkErr) || linkErr.Problem != test.problem {
				t.Fatalf("error = %v, want %s", err, test.problem)
			}
		})
	}
}

func TestParseScalarWikilinkASCIITrimAndWholeScalar(t *testing.T) {
	t.Parallel()
	tests := []struct {
		value      string
		recognized bool
		target     string
		invalid    bool
	}{
		{" \t[[people/jane]]\r\n", true, "people/jane", false},
		{"[[bad|]]", true, "", true},
		{"prefix [[people/jane]]", false, "", false},
		{"[[people/jane]] suffix", false, "", false},
		{"[[people/jane]] [[other]]", false, "", false},
		{"[[unclosed", false, "", false},
		{"\u00a0[[people/jane]]\u00a0", false, "", false},
	}
	for _, test := range tests {
		link, recognized, err := ParseScalarWikilink(test.value)
		if recognized != test.recognized {
			t.Errorf("ParseScalarWikilink(%q) recognized = %v, want %v", test.value, recognized, test.recognized)
			continue
		}
		if (err != nil) != test.invalid {
			t.Errorf("ParseScalarWikilink(%q) error = %v", test.value, err)
		}
		if err == nil && recognized && link.Target != test.target {
			t.Errorf("ParseScalarWikilink(%q) target = %q", test.value, link.Target)
		}
	}
}

func TestYAMLWikilinksScansOnlyRecursiveValues(t *testing.T) {
	t.Parallel()
	document := parseYAML(t, `"[[mapping-key]]": ignored
direct: " [[one]] "
nested:
  - "[[two|Two]]"
  - prose [[ignored]]
  - "[[bad|]]"
`)
	links := YAMLWikilinks(document.Root)
	if len(links) != 3 {
		t.Fatalf("links = %#v", links)
	}
	got := [][3]string{
		{links[0].Pointer, links[0].Link.Target, errorFlag(links[0].Err)},
		{links[1].Pointer, links[1].Link.Target, errorFlag(links[1].Err)},
		{links[2].Pointer, links[2].Link.Target, errorFlag(links[2].Err)},
	}
	want := [][3]string{
		{"/direct", "one", ""},
		{"/nested/0", "two", ""},
		{"/nested/2", "", "error"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("links = %#v, want %#v", got, want)
	}
}

func errorFlag(err error) string {
	if err != nil {
		return "error"
	}
	return ""
}
