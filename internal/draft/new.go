package draft

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/ontopix/engram/internal/checker"
	"github.com/ontopix/engram/internal/documentprofile"
	"github.com/ontopix/engram/internal/markdownprofile"
	"github.com/ontopix/engram/internal/snapshot"
	"github.com/ontopix/engram/internal/unicode17"
	"github.com/ontopix/engram/internal/yamlprofile"
)

// PlanNew generates and validates a deterministic record plus its containing
// catalog update without publishing either file.
func PlanNew(ctx context.Context, root, typeName, logicalPath string, options NewOptions) (*Plan, NewResult, error) {
	const operation = "new"
	if !snapshot.ValidTypeSlug(typeName) {
		return nil, NewResult{}, typed(ErrorUsage, operation, logicalPath, fmt.Errorf("invalid record type %q", typeName))
	}
	if !validRecordPath(logicalPath) {
		return nil, NewResult{}, typed(ErrorUsage, operation, logicalPath, errors.New("record path must be a valid store-relative .md path other than README.md"))
	}
	if err := documentprofile.ValidateDescription(options.Description); err != nil {
		return nil, NewResult{}, typed(ErrorUsage, operation, logicalPath, err)
	}
	bodyProvided := options.BodyProvided || options.Body != nil
	titleProvided := options.TitleProvided || options.Title != ""
	if bodyProvided && titleProvided {
		return nil, NewResult{}, typed(ErrorUsage, operation, logicalPath, errors.New("title cannot accompany an explicit body"))
	}
	if bodyProvided {
		if err := documentprofile.ValidateText(options.Body); err != nil {
			return nil, NewResult{}, typed(ErrorUsage, operation, logicalPath, fmt.Errorf("body: %w", err))
		}
	} else if titleProvided {
		if err := validateTitle(options.Title); err != nil {
			return nil, NewResult{}, typed(ErrorUsage, operation, logicalPath, err)
		}
	}

	p, err := openPlanner(ctx, operation, root)
	if err != nil {
		return nil, NewResult{}, err
	}
	defer p.close()
	parent := parentLogical(logicalPath)
	if !containsDirectory(p.checked.Tree.Directories, parent) {
		return nil, NewResult{}, typed(ErrorRepository, operation, parent, errors.New("record parent is not an existing real content directory"))
	}
	if _, err := p.captureDirectory(parent); err != nil {
		return nil, NewResult{}, err
	}
	if err := p.captureSchemaRoute(parent); err != nil {
		return nil, NewResult{}, err
	}
	absent, err := p.captureAbsent(logicalPath)
	if err != nil {
		return nil, NewResult{}, err
	}
	for existing := range p.checked.Tree.Boundaries {
		if parentLogical(existing) == parent && existing != logicalPath &&
			unicode17.CaseFoldKey(path.Base(existing)) == unicode17.CaseFoldKey(path.Base(logicalPath)) {
			return nil, NewResult{}, typed(ErrorConflict, operation, logicalPath, fmt.Errorf("record name collides case-insensitively with %q", existing))
		}
	}

	description, _, err := p.checked.ShowSchema(parent, typeName)
	if err != nil || description.Path == nil {
		if err == nil {
			err = errors.New("resolved schema has no local path")
		}
		return nil, NewResult{}, typed(ErrorRepository, operation, logicalPath, fmt.Errorf("resolve visible schema: %w", err))
	}
	definition := p.checked.Schemas[*description.Path]
	if definition == nil || definition.Validator == nil || !definition.BodyValid {
		return nil, NewResult{}, typed(ErrorRepository, operation, *description.Path, errors.New("resolved schema is unavailable for authoring"))
	}
	schemaFile := p.checked.Tree.Files[*description.Path]
	if _, err := p.captureFile(*description.Path, schemaFile.Data); err != nil {
		return nil, NewResult{}, err
	}

	fields, err := parseAdditionalFields(options.Fields)
	if err != nil {
		return nil, NewResult{}, typed(ErrorUsage, operation, logicalPath, err)
	}
	frontmatter := make(map[string]any, len(fields)+2)
	frontmatter["type"] = typeName
	frontmatter["description"] = options.Description
	for name, value := range fields {
		frontmatter[name] = value
	}
	if err := definition.Validator.Validate(frontmatter); err != nil {
		return nil, NewResult{}, typed(ErrorUsage, operation, logicalPath, fmt.Errorf("frontmatter does not satisfy schema %s: %w", definition.Path, err))
	}
	pinned, _ := frontmatter["pinned"].(bool)

	body := append([]byte(nil), options.Body...)
	if !bodyProvided {
		title := options.Title
		if !titleProvided {
			title = strings.TrimSuffix(path.Base(logicalPath), ".md")
		}
		body = generatedBody(title, definition.Body.RequiredSections)
	}
	if err := validateRequiredSections(body, definition.Body.RequiredSections); err != nil {
		kind := ErrorUsage
		problemPath := logicalPath
		if !bodyProvided {
			kind = ErrorCapability
			problemPath = definition.Path
		}
		return nil, NewResult{}, typed(kind, operation, problemPath, err)
	}

	frontmatterBytes, err := encodeFrontmatter(typeName, options.Description, fields)
	if err != nil {
		return nil, NewResult{}, typed(ErrorInternal, operation, logicalPath, err)
	}
	record := make([]byte, 0, len(frontmatterBytes)+len(body)+8)
	record = append(record, []byte("---\n")...)
	record = append(record, frontmatterBytes...)
	record = append(record, []byte("---\n")...)
	record = append(record, body...)
	if _, err := documentprofile.Parse(record); err != nil {
		return nil, NewResult{}, typed(ErrorInternal, operation, logicalPath, fmt.Errorf("generated record is invalid: %w", err))
	}

	catalogPath, catalogBytes, catalogChanged, err := p.planCatalog(parent, &catalogAddition{
		path: logicalPath, description: options.Description, pinned: pinned,
	})
	if err != nil {
		return nil, NewResult{}, err
	}

	edits := map[string][]byte{logicalPath: record}
	if catalogChanged {
		edits[catalogPath] = catalogBytes
	}
	candidate, err := checker.CheckSource(newTreeOverlay(p.checked.Tree, edits, nil))
	if err != nil {
		return nil, NewResult{}, typed(ErrorInternal, operation, logicalPath, fmt.Errorf("validate generated candidate: %w", err))
	}
	if finding, found := firstNewError(p.checked.Validation, candidate.Validation); found {
		return nil, NewResult{}, typed(ErrorConflict, operation, finding.Path, fmt.Errorf("generated candidate has %s", finding.Code))
	}

	p.addFile(logicalPath, absent, record, parent)
	catalogs := []string{}
	if catalogChanged {
		p.addFile(catalogPath, p.plan.captures[catalogPath], catalogBytes, parent)
		catalogs = append(catalogs, catalogPath)
	}
	result := NewResult{
		DryRun: options.DryRun, Changed: true, Record: logicalPath, Catalogs: catalogs,
	}
	return p.plan, result, nil
}

// New plans and, unless DryRun is set, publishes one record and its catalog as
// one rollback-safe helper operation.
func New(ctx context.Context, root, typeName, logicalPath string, options NewOptions) (NewResult, error) {
	plan, result, err := PlanNew(ctx, root, typeName, logicalPath, options)
	if err != nil {
		return NewResult{}, err
	}
	if options.DryRun {
		return result, nil
	}
	if err := plan.PublishWith(ctx, options.Rendezvous); err != nil {
		return NewResult{}, err
	}
	return result, nil
}

func parseAdditionalFields(source []byte) (map[string]any, error) {
	if source == nil {
		return map[string]any{}, nil
	}
	document, err := yamlprofile.Parse(source)
	if err != nil {
		return nil, fmt.Errorf("parse fields YAML: %w", err)
	}
	result := make(map[string]any, len(document.Root.Mapping))
	for _, member := range document.Root.Mapping {
		if member.Key == "type" || member.Key == "description" {
			return nil, fmt.Errorf("fields cannot override %q", member.Key)
		}
		if strings.HasPrefix(member.Key, "engram-") {
			return nil, fmt.Errorf("fields cannot use reserved key %q", member.Key)
		}
		result[member.Key] = member.Value.JSONValue()
	}
	return result, nil
}

func encodeFrontmatter(typeName, description string, fields map[string]any) ([]byte, error) {
	var result bytes.Buffer
	result.WriteString("type: ")
	result.WriteString(typeName)
	result.WriteByte('\n')
	if err := writeYAMLMember(&result, "description", description); err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(fields))
	for name := range fields {
		keys = append(keys, name)
	}
	sort.Slice(keys, func(i, j int) bool { return bytes.Compare([]byte(keys[i]), []byte(keys[j])) < 0 })
	for _, name := range keys {
		if err := writeYAMLMember(&result, name, fields[name]); err != nil {
			return nil, err
		}
	}
	return result.Bytes(), nil
}

func writeYAMLMember(destination *bytes.Buffer, name string, value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if plainYAMLKey(name) {
		destination.WriteString(name)
	} else {
		key, err := json.Marshal(name)
		if err != nil {
			return err
		}
		destination.Write(key)
	}
	destination.WriteString(": ")
	destination.Write(encoded)
	destination.WriteByte('\n')
	return nil
}

func plainYAMLKey(value string) bool {
	if value == "" {
		return false
	}
	for index := range len(value) {
		character := value[index]
		letter := character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z'
		if index == 0 && (letter || character == '_') || index > 0 && (letter || character == '_' || character == '-' || character >= '0' && character <= '9') {
			continue
		}
		return false
	}
	switch strings.ToLower(value) {
	case "null", "true", "false":
		return false
	default:
		return true
	}
}

func generatedBody(title string, required []string) []byte {
	var result strings.Builder
	result.WriteString("# ")
	result.WriteString(title)
	result.WriteByte('\n')
	for _, section := range required {
		result.WriteByte('\n')
		result.WriteString("## ")
		result.WriteString(section)
		result.WriteByte('\n')
	}
	return []byte(result.String())
}

func validateTitle(title string) error {
	if !utf8.ValidString(title) || title == "" || strings.HasPrefix(title, " ") || strings.HasSuffix(title, " ") || strings.HasPrefix(title, "\t") || strings.HasSuffix(title, "\t") {
		return errors.New("title must be a non-empty UTF-8 single line with no edge space or tab")
	}
	for _, character := range title {
		if character == '\n' || character == '\r' || character == 0 || character == 0x2028 || character == 0x2029 {
			return errors.New("title must be a non-empty UTF-8 single line with no edge space or tab")
		}
	}
	document := markdownprofile.Parse([]byte("# " + title + "\n"))
	if len(document.Headings) != 1 || document.Headings[0].Level != 1 || document.Headings[0].Source != title {
		return errors.New("title cannot be represented as one exact ATX H1")
	}
	return nil
}

func validateRequiredSections(body []byte, required []string) error {
	available := make(map[string]struct{})
	for _, heading := range markdownprofile.Parse(body).Headings {
		if heading.Level == 2 {
			available[heading.Source] = struct{}{}
		}
	}
	for _, section := range required {
		if _, exists := available[section]; !exists {
			return fmt.Errorf("body is missing required level-2 heading %q", section)
		}
	}
	return nil
}

type treeOverlay struct {
	base    *snapshot.Tree
	files   map[string][]byte
	dirs    map[string]struct{}
	deleted map[string]struct{}
}

func newTreeOverlay(base *snapshot.Tree, files map[string][]byte, directories []string) *treeOverlay {
	return newTreeOverlayWithDeletes(base, files, directories, nil)
}

func newTreeOverlayWithDeletes(base *snapshot.Tree, files map[string][]byte, directories, deletedPaths []string) *treeOverlay {
	cloned := make(map[string][]byte, len(files))
	for name, data := range files {
		cloned[name] = append([]byte(nil), data...)
	}
	dirs := make(map[string]struct{}, len(directories))
	for _, directory := range directories {
		dirs[directory] = struct{}{}
	}
	deleted := make(map[string]struct{}, len(deletedPaths))
	for _, name := range deletedPaths {
		deleted[name] = struct{}{}
	}
	return &treeOverlay{base: base, files: cloned, dirs: dirs, deleted: deleted}
}

func (s *treeOverlay) ReadDir(directory string) ([]snapshot.Entry, error) {
	if s == nil || s.base == nil {
		return nil, errors.New("overlay has no base tree")
	}
	if !containsDirectory(s.base.Directories, directory) {
		if _, exists := s.dirs[directory]; !exists && !baseConfigDirectory(s.base, directory) {
			return nil, errors.New("overlay directory does not exist")
		}
	}
	entries := make(map[string]snapshot.Kind)
	for name, kind := range s.base.Boundaries {
		if parentLogical(name) == directory {
			if _, deleted := s.deleted[name]; deleted {
				continue
			}
			entries[path.Base(name)] = kind
		}
	}
	for name := range s.dirs {
		if parentLogical(name) == directory {
			entries[path.Base(name)] = snapshot.KindDirectory
		}
	}
	for name := range s.files {
		if parentLogical(name) == directory {
			entries[path.Base(name)] = snapshot.KindRegular
		}
	}
	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool { return bytes.Compare([]byte(names[i]), []byte(names[j])) < 0 })
	result := make([]snapshot.Entry, len(names))
	for index, name := range names {
		result[index] = snapshot.Entry{Name: name, Kind: entries[name]}
	}
	return result, nil
}

func (s *treeOverlay) ReadFile(logicalPath string) ([]byte, error) {
	if data, exists := s.files[logicalPath]; exists {
		return append([]byte(nil), data...), nil
	}
	if _, deleted := s.deleted[logicalPath]; deleted {
		return nil, errors.New("overlay file does not exist")
	}
	file, exists := s.base.Files[logicalPath]
	if !exists {
		return nil, errors.New("overlay file does not exist")
	}
	return append([]byte(nil), file.Data...), nil
}

func baseConfigDirectory(tree *snapshot.Tree, directory string) bool {
	if directory == ".engram" {
		return tree.Boundaries[directory] == snapshot.KindDirectory
	}
	return tree.Boundaries[directory] == snapshot.KindDirectory
}

func firstNewError(base, candidate checker.Result) (checker.Finding, bool) {
	existing := make(map[[2]string]struct{})
	for _, finding := range base.Findings {
		if strings.HasPrefix(finding.Code, "E") {
			existing[[2]string{finding.Code, finding.Path}] = struct{}{}
		}
	}
	for _, finding := range candidate.Findings {
		if !strings.HasPrefix(finding.Code, "E") {
			continue
		}
		if _, already := existing[[2]string{finding.Code, finding.Path}]; !already {
			return finding, true
		}
	}
	return checker.Finding{}, false
}
