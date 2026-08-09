package checker

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/big"
	"path"
	"sort"
	"strings"

	"github.com/ontopix/engram/internal/documentprofile"
	"github.com/ontopix/engram/internal/schemaprofile"
	"github.com/ontopix/engram/internal/snapshot"
	"github.com/ontopix/engram/internal/yamlprofile"
)

// CheckFS validates one portable filesystem snapshot.
func CheckFS(root string) (*Snapshot, error) {
	source, err := snapshot.OpenFS(root)
	if err != nil {
		return nil, err
	}
	defer source.Close()
	return CheckSource(source)
}

// CheckSource validates one already bounded portable source.
func CheckSource(source snapshot.Source) (*Snapshot, error) {
	tree, err := snapshot.Load(source)
	if err != nil {
		return nil, err
	}
	analysis := &snapshotAnalysis{
		tree:      tree,
		findings:  make(findingSet),
		validText: make(map[string]bool),
		records:   make(map[string]*Record),
		maps:      make(map[string]*Map),
		schemas:   make(map[string]*Schema),
	}
	if err := analysis.run(); err != nil {
		return nil, err
	}
	result := Result{Target: TargetSnapshot, Status: StatusComplete, Findings: analysis.findings.sorted()}
	return &Snapshot{Tree: tree, Validation: result, Records: analysis.records, Maps: analysis.maps, Schemas: analysis.schemas}, nil
}

type snapshotAnalysis struct {
	tree      *snapshot.Tree
	findings  findingSet
	validText map[string]bool
	records   map[string]*Record
	maps      map[string]*Map
	schemas   map[string]*Schema
}

func (a *snapshotAnalysis) run() error {
	for _, issue := range a.tree.Issues {
		a.findings.add(issue.Code, issue.Path, "")
	}
	a.checkMapsPresent()
	a.checkAdvisoryNames()
	a.checkText()
	if err := a.checkManifest(); err != nil {
		return err
	}
	a.parseDocuments()
	a.compileSchemas()
	a.checkShadowing()
	a.resolveRecords()
	a.checkNoteBaseline()
	a.checkDuplicateDescriptions()
	a.checkBodiesAndLinks()
	a.checkCatalogs()
	return nil
}

func (a *snapshotAnalysis) checkMapsPresent() {
	for _, directory := range a.tree.Directories {
		readme := joinLogical(directory, "README.md")
		file, ok := a.tree.Files[readme]
		if !ok || file.Role != snapshot.RoleMap {
			a.findings.add("E101", readme, "directory has no regular README.md")
		}
	}
}

func (a *snapshotAnalysis) checkAdvisoryNames() {
	for _, directory := range a.tree.Directories {
		if directory == "." {
			continue
		}
		name := path.Base(directory)
		if !advisorySlug(name) {
			a.findings.add("W901", directory, "directory name is outside the advisory ASCII form")
		}
	}
	for name, file := range a.tree.Files {
		if file.Role != snapshot.RoleRecord && file.Role != snapshot.RoleAsset {
			continue
		}
		if !advisoryFilename(path.Base(name)) {
			a.findings.add("W901", name, "filename is outside the advisory ASCII form")
		}
	}
}

func (a *snapshotAnalysis) checkText() {
	for name, file := range a.tree.Files {
		if file.Role == snapshot.RoleAsset {
			continue
		}
		if err := documentprofile.ValidateText(file.Data); err != nil {
			a.findings.add("E108", name, err.Error())
			continue
		}
		a.validText[name] = true
		if file.Role == snapshot.RoleHook && !validInterpreterLine(file.Data) {
			a.findings.add("E308", name, "invalid preparation-hook interpreter line")
		}
	}
}

func (a *snapshotAnalysis) checkManifest() error {
	const manifestPath = ".engram/root.yaml"
	file, exists := a.tree.Files[manifestPath]
	if !exists || file.Role != snapshot.RoleRootManifest {
		a.findings.add("E105", manifestPath, "root manifest is missing or not a regular file")
		return nil
	}
	if !a.validText[manifestPath] {
		return nil
	}
	document, err := yamlprofile.Parse(file.Data)
	if err != nil {
		a.findings.add("E105", manifestPath, err.Error())
		return nil
	}
	version, ok := document.Root.Lookup("engram")
	if !ok || version.Kind != yamlprofile.NumberKind || !version.Number.IsPositiveInteger() {
		a.findings.add("E105", manifestPath, "engram must be a positive integer")
		return nil
	}
	if version.Number.CmpInt64(1) != 0 {
		return &CapabilityError{Message: fmt.Sprintf("unsupported engram major %s", version.Number)}
	}
	return nil
}

func (a *snapshotAnalysis) parseDocuments() {
	for _, name := range sortedFileNames(a.tree.Files) {
		file := a.tree.Files[name]
		if !a.validText[name] {
			continue
		}
		switch file.Role {
		case snapshot.RoleMap:
			a.parseMap(file)
		case snapshot.RoleRecord:
			a.parseRecord(file)
		case snapshot.RoleSchema:
			a.parseSchema(file)
		}
	}
}

func (a *snapshotAnalysis) parseMap(file snapshot.File) {
	document, err := documentprofile.Parse(file.Data)
	if err != nil {
		a.findings.add("E209", file.Path, err.Error())
		return
	}
	result := &Map{Path: file.Path, Frontmatter: document.YAML.Root, Body: append([]byte(nil), document.BodyBytes()...), Markdown: document.Markdown, Catalog: string(documentprofile.CatalogAll)}
	if description, err := documentprofile.Description(document.YAML.Root); err != nil {
		a.findings.add("E206", file.Path, err.Error())
	} else {
		result.Description = stringPointer(description)
	}
	if mode, err := documentprofile.CatalogModeFrom(document.YAML.Root); err != nil {
		a.findings.add("E207", file.Path, err.Error())
		result.Catalog = ""
	} else {
		result.Catalog = string(mode)
	}
	a.maps[file.Path] = result
}

func (a *snapshotAnalysis) parseRecord(file snapshot.File) {
	document, err := documentprofile.Parse(file.Data)
	if err != nil {
		a.findings.add("E201", file.Path, err.Error())
		return
	}
	record := &Record{
		Path:        file.Path,
		Bytes:       append([]byte(nil), file.Data...),
		Frontmatter: document.YAML.Root,
		Body:        append([]byte(nil), document.BodyBytes()...),
		Markdown:    document.Markdown,
		PinnedValid: true,
	}
	if value, err := documentprofile.RequiredString(document.YAML.Root, "type"); err != nil {
		a.findings.add("E202", file.Path, err.Error())
	} else if !documentprofile.ValidTypeSlug(value) {
		a.findings.add("E203", file.Path, "type is outside the v1 slug grammar")
	} else {
		record.Type = value
	}
	if description, err := documentprofile.Description(document.YAML.Root); err != nil {
		a.findings.add("E204", file.Path, err.Error())
	} else {
		record.Description = stringPointer(description)
	}
	if pinned, _, err := documentprofile.Pinned(document.YAML.Root); err != nil {
		a.findings.add("E208", file.Path, err.Error())
		record.PinnedValid = false
	} else {
		record.Pinned = pinned
	}
	for _, member := range document.YAML.Root.Mapping {
		if strings.HasPrefix(member.Key, "engram-") {
			a.findings.add("E205", file.Path, "reserved record-frontmatter key")
			break
		}
	}
	a.records[file.Path] = record
}

func (a *snapshotAnalysis) parseSchema(file snapshot.File) {
	document, err := documentprofile.Parse(file.Data)
	if err != nil {
		a.findings.add("E303", file.Path, err.Error())
		return
	}
	definition := &Schema{
		Path:          file.Path,
		Scope:         schemaScope(file.Path),
		BodyValid:     true,
		PolicyValid:   true,
		Documentation: append([]byte(nil), document.BodyBytes()...),
		Markdown:      document.Markdown,
	}
	root := document.YAML.Root
	valid := true
	allowed := map[string]struct{}{"type": {}, "version": {}, "description": {}, "schema": {}, "body": {}, "policy": {}}
	if unknownKeys(root, allowed) {
		a.findings.add("E303", file.Path, "unknown schema-file frontmatter key")
		valid = false
	}
	typeName, err := documentprofile.Type(root)
	if err != nil || typeName != strings.TrimSuffix(path.Base(file.Path), ".md") {
		a.findings.add("E303", file.Path, "invalid or mismatched schema type")
		valid = false
	} else {
		definition.Type = typeName
	}
	version, exists := root.Lookup("version")
	if !exists || version.Kind != yamlprofile.NumberKind || !version.Number.IsPositiveInteger() {
		a.findings.add("E303", file.Path, "schema version must be a positive integer")
		valid = false
	} else {
		definition.Version = version.Number
	}
	if description, err := documentprofile.Description(root); err != nil {
		a.findings.add("E303", file.Path, err.Error())
		valid = false
	} else {
		definition.Description = description
	}
	schemaNode, exists := root.Lookup("schema")
	if !exists || schemaNode.Kind != yamlprofile.MappingKind {
		a.findings.add("E303", file.Path, "schema field must be a mapping")
		valid = false
	} else {
		definition.RawSchema, _ = schemaNode.JSONValue().(map[string]any)
	}
	if bodyNode, exists := root.Lookup("body"); exists {
		definition.RawBody = bodyNode.JSONValue()
		body, ok := parseBodyRequirements(bodyNode)
		if !ok {
			a.findings.add("E303", file.Path, "invalid body requirements")
			definition.BodyValid = false
			valid = false
		} else {
			definition.Body = body
		}
	}
	if policyNode, exists := root.Lookup("policy"); exists {
		definition.RawPolicy = policyNode.JSONValue()
		policy, ok := parsePolicy(policyNode)
		if !ok {
			a.findings.add("E303", file.Path, "invalid policy mapping")
			definition.PolicyValid = false
			valid = false
		} else {
			definition.Policy = policy
			if policy.Immutable && policy.AppendOnly {
				a.findings.add("E306", file.Path, "immutable and append-only are mutually exclusive")
			}
		}
	} else {
		definition.Policy = Policy{Available: true}
	}
	definition.Valid = valid
	a.schemas[file.Path] = definition
}

func (a *snapshotAnalysis) compileSchemas() {
	for _, name := range sortedSchemaNames(a.schemas) {
		definition := a.schemas[name]
		if definition.RawSchema == nil {
			continue
		}
		compiled, err := schemaprofile.Compile(definition.RawSchema)
		if err != nil {
			a.findings.add("E303", name, err.Error())
			continue
		}
		definition.Validator = compiled
		definition.SchemaValid = true
		if compiled.HasE305() {
			a.findings.add("E305", name, "closed top-level schema omits a universal property")
		}
		if compiled.HasVendorAnnotations() {
			definition.Vendor = true
			a.findings.add("W904", name, "schema contains a vendor annotation")
		}
	}
}

func (a *snapshotAnalysis) checkShadowing() {
	for name := range a.schemas {
		scope := schemaScope(name)
		if scope == "." {
			continue
		}
		filename := path.Base(name)
		for ancestor := parentScope(scope); ; ancestor = parentScope(ancestor) {
			candidate := joinScope(ancestor, ".engram/schemas/"+filename)
			if file, ok := a.tree.Files[candidate]; ok && file.Role == snapshot.RoleSchema {
				a.findings.add("E304", name, "schema type shadows an ancestor definition")
				break
			}
			if ancestor == "." {
				break
			}
		}
	}
}

func (a *snapshotAnalysis) resolveRecords() {
	for _, name := range sortedRecordNames(a.records) {
		record := a.records[name]
		if record.Type == "" {
			continue
		}
		definition, found, available := a.resolveSchema(path.Dir(name), record.Type)
		if !found && available {
			a.findings.add("E203", name, "type resolves to no visible schema")
			continue
		}
		if !available || definition == nil {
			continue
		}
		record.SchemaPath = definition.Path
		if definition.PolicyValid {
			record.Policy = definition.Policy
		}
		if definition.SchemaValid {
			if err := definition.Validator.Validate(record.Frontmatter.JSONValue()); err != nil {
				a.findings.add("E301", name, err.Error())
			}
		}
	}
}

func (a *snapshotAnalysis) resolveSchema(directory, typeName string) (*Schema, bool, bool) {
	for scope := directory; ; scope = parentScope(scope) {
		config := joinScope(scope, ".engram")
		if kind, exists := a.tree.Boundaries[config]; exists {
			if kind != snapshot.KindDirectory {
				return nil, false, false
			}
			schemaDirectory := joinScope(scope, ".engram/schemas")
			if schemaKind, exists := a.tree.Boundaries[schemaDirectory]; exists {
				if schemaKind != snapshot.KindDirectory {
					return nil, false, false
				}
				candidate := joinScope(scope, ".engram/schemas/"+typeName+".md")
				if _, exists := a.tree.Boundaries[candidate]; exists {
					definition, parsed := a.schemas[candidate]
					return definition, true, parsed
				}
			}
		}
		if scope == "." {
			break
		}
	}
	return nil, false, true
}

func (a *snapshotAnalysis) checkNoteBaseline() {
	const notePath = ".engram/schemas/note.md"
	definition, exists := a.schemas[notePath]
	if !exists || !definition.SchemaValid || !noteBaseline(definition) {
		a.findings.add("E307", notePath, "root note schema does not satisfy the baseline normal form")
	}
}

func (a *snapshotAnalysis) checkDuplicateDescriptions() {
	groups := make(map[string][]string)
	for name, record := range a.records {
		if value, ok := stringField(record.Frontmatter, "description"); ok {
			groups[value] = append(groups[value], name)
		}
	}
	for _, names := range groups {
		if len(names) < 2 {
			continue
		}
		for _, name := range names {
			a.findings.add("W903", name, "description is duplicated verbatim")
		}
	}
}

func parseBodyRequirements(node *yamlprofile.Node) (BodyRequirements, bool) {
	if node.Kind != yamlprofile.MappingKind || unknownKeys(node, map[string]struct{}{"required-sections": {}}) {
		return BodyRequirements{}, false
	}
	value, exists := node.Lookup("required-sections")
	if !exists {
		return BodyRequirements{}, true
	}
	if value.Kind != yamlprofile.SequenceKind {
		return BodyRequirements{}, false
	}
	result := BodyRequirements{RequiredSections: make([]string, 0, len(value.Sequence))}
	for _, section := range value.Sequence {
		if section.Kind != yamlprofile.StringKind || section.String == "" || strings.HasPrefix(section.String, " ") || strings.HasSuffix(section.String, " ") || strings.HasPrefix(section.String, "\t") || strings.HasSuffix(section.String, "\t") || strings.ContainsAny(section.String, "\r\n") {
			return BodyRequirements{}, false
		}
		result.RequiredSections = append(result.RequiredSections, section.String)
	}
	return result, true
}

func parsePolicy(node *yamlprofile.Node) (Policy, bool) {
	if node.Kind != yamlprofile.MappingKind || unknownKeys(node, map[string]struct{}{"immutable": {}, "append-only": {}}) {
		return Policy{}, false
	}
	result := Policy{Available: true}
	for _, member := range node.Mapping {
		if member.Value.Kind != yamlprofile.BooleanKind {
			return Policy{}, false
		}
		switch member.Key {
		case "immutable":
			result.Immutable = member.Value.Boolean
		case "append-only":
			result.AppendOnly = member.Value.Boolean
		}
	}
	return result, true
}

func validInterpreterLine(data []byte) bool {
	lineEnd := bytes.IndexByte(data, '\n')
	if lineEnd < 0 {
		return false
	}
	const prefix = "#!/usr/bin/env "
	line := string(data[:lineEnd])
	if !strings.HasPrefix(line, prefix) || len(line) == len(prefix) {
		return false
	}
	for _, character := range line[len(prefix):] {
		if character > 0x7f || character != '.' && character != '_' && character != '+' && character != '-' && (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') && (character < '0' || character > '9') {
			return false
		}
	}
	return true
}

func advisorySlug(value string) bool {
	if value == "" || value[0] == '-' || value[len(value)-1] == '-' || strings.Contains(value, "--") {
		return false
	}
	for _, character := range []byte(value) {
		if character != '-' && (character < 'a' || character > 'z') && (character < '0' || character > '9') {
			return false
		}
	}
	return true
}

func advisoryFilename(value string) bool {
	parts := strings.Split(value, ".")
	if !advisorySlug(parts[0]) {
		return false
	}
	for _, extension := range parts[1:] {
		if extension == "" {
			return false
		}
		for _, character := range []byte(extension) {
			if (character < 'a' || character > 'z') && (character < '0' || character > '9') {
				return false
			}
		}
	}
	return true
}

func sortedFileNames(files map[string]snapshot.File) []string {
	result := make([]string, 0, len(files))
	for name := range files {
		result = append(result, name)
	}
	sort.Slice(result, func(i, j int) bool { return bytes.Compare([]byte(result[i]), []byte(result[j])) < 0 })
	return result
}

func sortedSchemaNames(values map[string]*Schema) []string {
	result := make([]string, 0, len(values))
	for name := range values {
		result = append(result, name)
	}
	sort.Strings(result)
	return result
}

func sortedRecordNames(values map[string]*Record) []string {
	result := make([]string, 0, len(values))
	for name := range values {
		result = append(result, name)
	}
	sort.Slice(result, func(i, j int) bool { return bytes.Compare([]byte(result[i]), []byte(result[j])) < 0 })
	return result
}

func parentScope(scope string) string {
	if scope == "." {
		return "."
	}
	parent := path.Dir(scope)
	if parent == "" {
		return "."
	}
	return parent
}

func joinLogical(directory, name string) string {
	if directory == "." {
		return name
	}
	return path.Join(directory, name)
}

func stringPointer(value string) *string { return &value }

func noteBaseline(definition *Schema) bool {
	root := definition.RawSchema
	if root == nil || root["type"] != "object" {
		return false
	}
	properties, ok := root["properties"].(map[string]any)
	if !ok {
		return false
	}
	if !mapExactly(properties["type"], map[string]any{"const": "note"}) ||
		!mapExactly(properties["description"], map[string]any{"type": "string", "minLength": integerJSON(1), "maxLength": integerJSON(200)}) ||
		!mapExactly(properties["pinned"], map[string]any{"type": "boolean"}) {
		return false
	}
	if required, exists := root["required"]; exists {
		values, ok := required.([]any)
		if !ok {
			return false
		}
		for _, value := range values {
			if value != "type" && value != "description" {
				return false
			}
		}
	}
	if additional, exists := root["additionalProperties"]; exists && additional != true {
		return false
	}
	allowed := map[string]struct{}{
		"type": {}, "properties": {}, "required": {}, "additionalProperties": {}, "$schema": {}, "$defs": {}, "$comment": {},
		"title": {}, "description": {}, "default": {}, "deprecated": {}, "readOnly": {}, "writeOnly": {}, "examples": {},
	}
	for keyword := range root {
		if _, ok := allowed[keyword]; ok || strings.HasPrefix(keyword, "x-") && !strings.HasPrefix(keyword, "x-engram-") {
			continue
		}
		return false
	}
	if len(definition.Body.RequiredSections) != 0 {
		return false
	}
	return !definition.Policy.Immutable && !definition.Policy.AppendOnly
}

func mapExactly(value any, expected map[string]any) bool {
	actual, ok := value.(map[string]any)
	if !ok || len(actual) != len(expected) {
		return false
	}
	for key, want := range expected {
		got, exists := actual[key]
		if !exists || !jsonScalarEqual(got, want) {
			return false
		}
	}
	return true
}

func integerJSON(value int64) any { return json.Number(fmt.Sprintf("%d", value)) }

func jsonScalarEqual(left, right any) bool {
	if left == right {
		return true
	}
	leftNumber, leftOK := exactJSONNumber(left)
	rightNumber, rightOK := exactJSONNumber(right)
	return leftOK && rightOK && leftNumber.Cmp(rightNumber) == 0
}

func exactJSONNumber(value any) (*big.Rat, bool) {
	var spelling string
	switch value := value.(type) {
	case json.Number:
		spelling = value.String()
	case int:
		spelling = fmt.Sprintf("%d", value)
	case int64:
		spelling = fmt.Sprintf("%d", value)
	default:
		return nil, false
	}
	number, ok := new(big.Rat).SetString(spelling)
	return number, ok
}
