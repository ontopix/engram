package documentprofile

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/ontopix/engram/internal/snapshot"
	"github.com/ontopix/engram/internal/yamlprofile"
)

// Wikilink is one syntactically valid §7.1 occurrence.
type Wikilink struct {
	Raw      string
	Target   string
	Label    string
	HasLabel bool
}

// RecordPath returns the exact store-root-relative target after appending the
// required literal .md suffix to the final segment.
func (w Wikilink) RecordPath() string { return w.Target + ".md" }

// WikilinkProblem classifies a syntactic failure independently of resolution.
type WikilinkProblem string

const (
	WikilinkEnvelope WikilinkProblem = "envelope"
	WikilinkTarget   WikilinkProblem = "target"
	WikilinkLabel    WikilinkProblem = "label"
)

// WikilinkError is an invalid wikilink form.
type WikilinkError struct {
	Problem WikilinkProblem
	Detail  string
}

func (e *WikilinkError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Detail == "" {
		return "invalid wikilink " + string(e.Problem)
	}
	return fmt.Sprintf("invalid wikilink %s: %s", e.Problem, e.Detail)
}

// ParseWikilink validates one complete, untrimmed [[...]] candidate. The
// first vertical bar separates the target from the optional label.
func ParseWikilink(raw string) (Wikilink, error) {
	if !utf8.ValidString(raw) || !strings.HasPrefix(raw, "[[") || !strings.HasSuffix(raw, "]]") || len(raw) < 4 {
		return Wikilink{}, &WikilinkError{Problem: WikilinkEnvelope, Detail: "expected one complete [[...]] candidate"}
	}
	if closer := strings.Index(raw[2:], "]]"); closer < 0 || closer+4 != len(raw) {
		return Wikilink{}, &WikilinkError{Problem: WikilinkEnvelope, Detail: "candidate closes before its final bytes"}
	}

	inside := raw[2 : len(raw)-2]
	target, label, hasLabel := inside, "", false
	if separator := strings.IndexByte(inside, '|'); separator >= 0 {
		target, label, hasLabel = inside[:separator], inside[separator+1:], true
	}
	if err := validateWikilinkTarget(target); err != nil {
		return Wikilink{}, err
	}
	if hasLabel {
		if label == "" {
			return Wikilink{}, &WikilinkError{Problem: WikilinkLabel, Detail: "label is empty"}
		}
		if !utf8.ValidString(label) {
			return Wikilink{}, &WikilinkError{Problem: WikilinkLabel, Detail: "label is not valid UTF-8"}
		}
		for _, character := range label {
			if character == '[' || character == ']' || forbiddenSingleLineCharacter(character) {
				return Wikilink{}, &WikilinkError{Problem: WikilinkLabel, Detail: fmt.Sprintf("forbidden U+%04X", character)}
			}
		}
	}
	return Wikilink{Raw: raw, Target: target, Label: label, HasLabel: hasLabel}, nil
}

func validateWikilinkTarget(target string) error {
	if target == "" {
		return &WikilinkError{Problem: WikilinkTarget, Detail: "target is empty"}
	}
	segments := strings.Split(target, "/")
	for index, segment := range segments {
		if segment == "" || segment == "." || segment == ".." {
			return &WikilinkError{Problem: WikilinkTarget, Detail: "target has an empty or dot segment"}
		}
		name := segment
		if index == len(segments)-1 {
			name += ".md"
		}
		if !snapshot.ValidContentName(name) {
			return &WikilinkError{Problem: WikilinkTarget, Detail: fmt.Sprintf("segment %q violates mandatory name syntax", segment)}
		}
	}
	return nil
}

// ParseScalarWikilink applies §7.2's ASCII-only edge trimming and whole-scalar
// rule. recognized is false for embedded prose, an opener without a closer,
// or any candidate followed by additional scalar text. A recognized but
// malformed complete candidate returns a WikilinkError.
func ParseScalarWikilink(value string) (link Wikilink, recognized bool, err error) {
	trimmed := yamlprofile.TrimASCIIWhitespace(value)
	if !strings.HasPrefix(trimmed, "[[") {
		return Wikilink{}, false, nil
	}
	closer := strings.Index(trimmed[2:], "]]")
	if closer < 0 || closer+4 != len(trimmed) {
		return Wikilink{}, false, nil
	}
	link, err = ParseWikilink(trimmed)
	return link, true, err
}

// YAMLWikilink ties one recognized frontmatter scalar occurrence to its YAML
// position and JSON Pointer. Err is non-nil when the complete candidate has
// invalid §7.1 form.
type YAMLWikilink struct {
	StringValue
	Link Wikilink
	Err  error
}

// YAMLWikilinks recursively scans YAML string values, excluding mapping keys,
// and returns only whole-scalar wikilink occurrences.
func YAMLWikilinks(root *yamlprofile.Node) []YAMLWikilink {
	var links []YAMLWikilink
	WalkStringValues(root, func(value StringValue) bool {
		link, recognized, err := ParseScalarWikilink(value.Value)
		if recognized {
			links = append(links, YAMLWikilink{StringValue: value, Link: link, Err: err})
		}
		return true
	})
	return links
}
