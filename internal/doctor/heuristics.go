package doctor

import (
	"bytes"
	"path"
	"sort"
	"strings"

	"github.com/ontopix/engram/internal/checker"
	"github.com/ontopix/engram/internal/documentprofile"
	"github.com/ontopix/engram/internal/markdownprofile"
	"github.com/ontopix/engram/internal/yamlprofile"
)

func appendHeuristics(current *inspection) {
	if current.accepted == nil || current.accepted.Tree == nil || current.accepted.Validation.Status != checker.StatusComplete || current.accepted.Validation.HasErrors() {
		return
	}
	for _, finding := range current.accepted.Validation.Findings {
		if finding.Code != "W903" {
			continue
		}
		current.result.Checks = append(current.result.Checks, Check{
			Name: "heuristic.duplicate", Class: Heuristic, Status: Warning,
			Path: pathPointer(finding.Path), Detail: detail("record description is duplicated verbatim"),
		})
	}

	inbound := make(map[string]struct{}, len(current.accepted.Records))
	for source, record := range current.accepted.Records {
		collectDocumentLinks(source, record.Markdown, inbound, current.accepted.Records)
		collectYAMLLinks(source, record.Frontmatter, inbound, current.accepted.Records)
	}
	for source, directoryMap := range current.accepted.Maps {
		collectDocumentLinks(source, directoryMap.Markdown, inbound, current.accepted.Records)
		collectYAMLLinks(source, directoryMap.Frontmatter, inbound, current.accepted.Records)
	}
	for source, schema := range current.accepted.Schemas {
		collectDocumentLinks(source, schema.Markdown, inbound, current.accepted.Records)
	}

	names := make([]string, 0, len(current.accepted.Records))
	for name := range current.accepted.Records {
		names = append(names, name)
	}
	sort.Slice(names, func(left, right int) bool { return bytes.Compare([]byte(names[left]), []byte(names[right])) < 0 })
	for _, name := range names {
		if _, linked := inbound[name]; linked {
			continue
		}
		current.result.Checks = append(current.result.Checks, Check{
			Name: "heuristic.orphan", Class: Heuristic, Status: Warning,
			Path: pathPointer(name), Detail: detail("no inbound local document link was observed"),
		})
	}
}

func collectDocumentLinks(source string, document markdownprofile.Document, inbound map[string]struct{}, records map[string]*checker.Record) {
	for _, occurrence := range document.Wikilinks {
		link, err := documentprofile.ParseWikilink(occurrence.Raw)
		if err == nil {
			addInbound(source, link.RecordPath(), inbound, records)
		}
	}
	for _, link := range document.Links {
		if target, ok := markdownRecordTarget(source, link.Destination); ok {
			addInbound(source, target, inbound, records)
		}
	}
}

func collectYAMLLinks(source string, root *yamlprofile.Node, inbound map[string]struct{}, records map[string]*checker.Record) {
	if root == nil {
		return
	}
	for _, occurrence := range documentprofile.YAMLWikilinks(root) {
		if occurrence.Err == nil {
			addInbound(source, occurrence.Link.RecordPath(), inbound, records)
		}
	}
}

func addInbound(source, target string, inbound map[string]struct{}, records map[string]*checker.Record) {
	if source == target {
		return
	}
	if _, exists := records[target]; exists {
		inbound[target] = struct{}{}
	}
}

// markdownRecordTarget intentionally recognizes only unambiguous local
// Markdown record destinations. Anything external, directory-shaped, empty,
// absolute, backslash-spelled, or escaping the root supplies no orphan
// evidence and is ignored.
func markdownRecordTarget(source, destination string) (string, bool) {
	if destination == "" || strings.Contains(destination, "\\") || strings.HasPrefix(destination, "/") || hasURIScheme(destination) {
		return "", false
	}
	if marker := strings.IndexAny(destination, "?#"); marker >= 0 {
		destination = destination[:marker]
	}
	if destination == "" || strings.HasSuffix(destination, "/") {
		return "", false
	}
	target := path.Clean(path.Join(path.Dir(source), destination))
	if target == "." || target == ".." || strings.HasPrefix(target, "../") || !strings.HasSuffix(target, ".md") {
		return "", false
	}
	return target, true
}

func hasURIScheme(value string) bool {
	if value == "" || !asciiLetter(value[0]) {
		return false
	}
	for index := 1; index < len(value); index++ {
		character := value[index]
		if character == ':' {
			return true
		}
		if !asciiLetter(character) && (character < '0' || character > '9') && character != '+' && character != '.' && character != '-' {
			return false
		}
	}
	return false
}

func asciiLetter(value byte) bool {
	return value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z'
}
