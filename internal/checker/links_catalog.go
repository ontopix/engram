package checker

import (
	"bytes"
	"path"
	"sort"
	"strings"

	"github.com/ontopix/engram/internal/documentprofile"
	"github.com/ontopix/engram/internal/markdownprofile"
	"github.com/ontopix/engram/internal/snapshot"
)

func (a *snapshotAnalysis) checkBodiesAndLinks() {
	for _, name := range sortedRecordNames(a.records) {
		record := a.records[name]
		definition := a.schemas[record.SchemaPath]
		if definition != nil && definition.BodyValid {
			available := make(map[string]struct{})
			for _, heading := range record.Markdown.Headings {
				if heading.Level == 2 {
					available[heading.Source] = struct{}{}
				}
			}
			for _, required := range definition.Body.RequiredSections {
				if _, exists := available[required]; !exists {
					a.findings.add("E302", name, "missing required level-2 heading")
					break
				}
			}
		}
		a.checkMarkdownLinks(name, record.Markdown)
		for _, occurrence := range record.Markdown.Wikilinks {
			a.checkWikilink(name, occurrence.Raw, true)
		}
		typedPointers := make(map[string]struct{})
		if definition != nil && definition.SchemaValid {
			for _, occurrence := range definition.Validator.ExtractLinks(record.Frontmatter.JSONValue()) {
				typedPointers[occurrence.InstanceLocation] = struct{}{}
				a.checkTypedLink(name, occurrence.Value, occurrence.Field.Types, occurrence.Field.MustExist)
			}
		}
		for _, occurrence := range documentprofile.YAMLWikilinks(record.Frontmatter) {
			if _, typed := typedPointers[occurrence.Pointer]; typed {
				continue
			}
			if occurrence.Err != nil {
				a.findings.add("E403", name, occurrence.Err.Error())
				continue
			}
			a.resolveWikilink(name, occurrence.Link, true)
		}
	}
	for name, directoryMap := range a.maps {
		a.checkMarkdownLinks(name, directoryMap.Markdown)
		for _, occurrence := range directoryMap.Markdown.Wikilinks {
			a.checkWikilink(name, occurrence.Raw, true)
		}
	}
	for name, definition := range a.schemas {
		a.checkMarkdownLinks(name, definition.Markdown)
		for _, occurrence := range definition.Markdown.Wikilinks {
			a.checkWikilink(name, occurrence.Raw, true)
		}
	}
}

func (a *snapshotAnalysis) checkTypedLink(sourcePath, value string, allowedTypes []string, mustExist bool) {
	link, recognized, err := documentprofile.ParseScalarWikilink(value)
	if !recognized || err != nil {
		detail := "typed link value is not one complete wikilink"
		if err != nil {
			detail = err.Error()
		}
		a.findings.add("E403", sourcePath, detail)
		return
	}
	target := link.RecordPath()
	file, exists := a.tree.Files[target]
	if !exists {
		if _, boundaryExists := a.tree.Boundaries[target]; boundaryExists || containsString(a.tree.Directories, target) {
			a.findings.add("E401", sourcePath, "typed-link target is not a record")
			return
		}
		if mustExist {
			a.findings.add("E401", sourcePath, "required typed-link target is absent")
		}
		return
	}
	if file.Role != snapshot.RoleRecord {
		a.findings.add("E401", sourcePath, "typed-link target is not a record")
		return
	}
	targetRecord := a.records[target]
	if targetRecord == nil || targetRecord.Type == "" {
		return
	}
	for _, allowed := range allowedTypes {
		if targetRecord.Type == allowed {
			return
		}
	}
	a.findings.add("E402", sourcePath, "typed-link target type is not admitted")
}

func (a *snapshotAnalysis) checkWikilink(sourcePath, raw string, mustExist bool) {
	link, err := documentprofile.ParseWikilink(raw)
	if err != nil {
		a.findings.add("E403", sourcePath, err.Error())
		return
	}
	a.resolveWikilink(sourcePath, link, mustExist)
}

func (a *snapshotAnalysis) resolveWikilink(sourcePath string, link documentprofile.Wikilink, mustExist bool) {
	target := link.RecordPath()
	file, exists := a.tree.Files[target]
	if !exists || file.Role != snapshot.RoleRecord {
		if mustExist {
			a.findings.add("E401", sourcePath, "wikilink target is not an existing record")
		}
		return
	}
}

func (a *snapshotAnalysis) checkMarkdownLinks(sourcePath string, document markdownprofile.Document) {
	for _, link := range document.Links {
		target, directoryOnly, external, valid := resolveMarkdownDestination(sourcePath, link.Destination)
		if external {
			continue
		}
		if !valid {
			a.findings.add("E403", sourcePath, "invalid local Markdown destination")
			continue
		}
		if target == sourcePath {
			continue
		}
		if directoryOnly {
			if !containsString(a.tree.Directories, target) {
				a.findings.add("E404", sourcePath, "local directory destination does not exist")
			}
			continue
		}
		if _, exists := a.tree.Boundaries[target]; !exists && !containsString(a.tree.Directories, target) {
			a.findings.add("E404", sourcePath, "local Markdown destination does not exist")
		}
	}
}

func resolveMarkdownDestination(sourcePath, destination string) (target string, directoryOnly, external, valid bool) {
	if hasScheme(destination) {
		return "", false, true, true
	}
	pathPart := destination
	if index := strings.IndexAny(pathPart, "?#"); index >= 0 {
		pathPart = pathPart[:index]
	}
	if pathPart == "" {
		return sourcePath, false, false, true
	}
	if strings.HasPrefix(pathPart, "/") || strings.Contains(pathPart, "\\") {
		return "", false, false, false
	}
	directoryOnly = strings.HasSuffix(pathPart, "/")
	segments := strings.Split(pathPart, "/")
	base := path.Dir(sourcePath)
	stack := []string{}
	if base != "." {
		stack = strings.Split(base, "/")
	}
	for index, segment := range segments {
		if segment == "" {
			if index == len(segments)-1 && directoryOnly {
				continue
			}
			return "", false, false, false
		}
		switch segment {
		case ".":
			continue
		case "..":
			if len(stack) == 0 {
				return "", false, false, false
			}
			stack = stack[:len(stack)-1]
		default:
			if segment != "README.md" && !validDestinationName(segment) {
				return "", false, false, false
			}
			stack = append(stack, segment)
		}
	}
	if len(stack) == 0 {
		return ".", directoryOnly, false, true
	}
	return strings.Join(stack, "/"), directoryOnly, false, true
}

func validDestinationName(name string) bool {
	return snapshot.ValidContentName(name)
}

func hasScheme(value string) bool {
	if value == "" || (value[0] < 'A' || value[0] > 'Z') && (value[0] < 'a' || value[0] > 'z') {
		return false
	}
	for index := 1; index < len(value); index++ {
		character := value[index]
		if character == ':' {
			return true
		}
		if (character < 'A' || character > 'Z') && (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '+' && character != '.' && character != '-' {
			return false
		}
	}
	return false
}

func (a *snapshotAnalysis) checkCatalogs() {
	for _, name := range sortedMapNames(a.maps) {
		directoryMap := a.maps[name]
		mode := documentprofile.CatalogMode(directoryMap.Catalog)
		if !documentprofile.ValidCatalogMode(mode) {
			continue
		}
		detection := documentprofile.DetectCatalog(directoryMap.Body)
		if !detection.ValidForMode(mode) {
			a.findings.add("E405", name, "catalog marker shape does not match catalog mode")
			continue
		}
		if mode == documentprofile.CatalogNone {
			continue
		}
		directory := path.Dir(name)
		if name == "README.md" {
			directory = "."
		}
		if a.hasIssue("E107", directory) {
			continue
		}
		var directories []documentprofile.CatalogDirectory
		available := true
		for _, child := range a.tree.Directories {
			if child == "." || path.Dir(child) != directory {
				continue
			}
			childMap := a.maps[joinLogical(child, "README.md")]
			if childMap == nil || childMap.Description == nil {
				available = false
				break
			}
			directories = append(directories, documentprofile.CatalogDirectory{Name: path.Base(child), Description: *childMap.Description})
		}
		var records []documentprofile.CatalogRecord
		if available && mode == documentprofile.CatalogAll {
			for _, record := range a.records {
				if path.Dir(record.Path) != directory {
					continue
				}
				if record.Description == nil || !record.PinnedValid {
					available = false
					break
				}
				records = append(records, documentprofile.CatalogRecord{Name: path.Base(record.Path), Description: *record.Description, Pinned: record.Pinned})
			}
		}
		if !available {
			continue
		}
		generated, err := documentprofile.GenerateCatalog(mode, directories, records)
		if err != nil {
			continue
		}
		if !detection.RegionMatches(directoryMap.Body, generated) {
			a.findings.add("E405", name, "catalog region differs from exact regeneration")
		}
	}
}

func (a *snapshotAnalysis) hasIssue(code, logicalPath string) bool {
	_, exists := a.findings[[2]string{code, logicalPath}]
	return exists
}

func sortedMapNames(values map[string]*Map) []string {
	result := make([]string, 0, len(values))
	for name := range values {
		result = append(result, name)
	}
	sort.Slice(result, func(i, j int) bool { return bytes.Compare([]byte(result[i]), []byte(result[j])) < 0 })
	return result
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
