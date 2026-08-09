package schemaprofile

import (
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/ontopix/engram/internal/regexprofile"
)

var vendorKeywordPattern = regexp.MustCompile(`^x-[a-z0-9]+-[a-z0-9]+(?:-[a-z0-9]+)*$`)

var recognizedKeywords = map[string]struct{}{
	// Core vocabulary.
	"$schema": {}, "$id": {}, "$ref": {}, "$anchor": {}, "$dynamicRef": {},
	"$dynamicAnchor": {}, "$vocabulary": {}, "$comment": {}, "$defs": {},
	// Applicator vocabulary.
	"prefixItems": {}, "items": {}, "contains": {}, "additionalProperties": {},
	"properties": {}, "patternProperties": {}, "dependentSchemas": {},
	"propertyNames": {}, "if": {}, "then": {}, "else": {}, "allOf": {},
	"anyOf": {}, "oneOf": {}, "not": {},
	// Unevaluated vocabulary.
	"unevaluatedItems": {}, "unevaluatedProperties": {},
	// Validation vocabulary.
	"type": {}, "const": {}, "enum": {}, "multipleOf": {}, "maximum": {},
	"exclusiveMaximum": {}, "minimum": {}, "exclusiveMinimum": {},
	"maxLength": {}, "minLength": {}, "pattern": {}, "maxItems": {},
	"minItems": {}, "uniqueItems": {}, "maxContains": {}, "minContains": {},
	"maxProperties": {}, "minProperties": {}, "required": {},
	"dependentRequired": {},
	// Format and Content vocabularies.
	"format": {}, "contentEncoding": {}, "contentMediaType": {}, "contentSchema": {},
	// Meta-Data vocabulary.
	"title": {}, "description": {}, "default": {}, "deprecated": {},
	"readOnly": {}, "writeOnly": {}, "examples": {},
}

var forbiddenKeywords = map[string]string{
	"$id":               "$id is forbidden by the engram profile",
	"$anchor":           "$anchor is forbidden by the engram profile",
	"$dynamicRef":       "$dynamicRef is forbidden by the engram profile",
	"$dynamicAnchor":    "$dynamicAnchor is forbidden by the engram profile",
	"$vocabulary":       "$vocabulary is forbidden by the engram profile",
	"patternProperties": "patternProperties is forbidden by the engram profile",
}

type analyzer struct {
	root map[string]any

	problems           []Problem
	schemaValues       map[string]any
	schemaObjects      map[string]map[string]any
	translatedPatterns map[string]string
	refs               []refUse
	refTargets         map[string]string
	vendorLocations    []string
	linkFields         []LinkField
}

type refUse struct {
	owner    string
	location string
	value    string
}

type linkPath struct {
	allowed   bool
	selectors []linkSelector
}

func newAnalyzer(root map[string]any) *analyzer {
	return &analyzer{
		root:               root,
		schemaValues:       make(map[string]any),
		schemaObjects:      make(map[string]map[string]any),
		translatedPatterns: make(map[string]string),
		refTargets:         make(map[string]string),
	}
}

func (a *analyzer) run() {
	a.walkSchema(a.root, nil, linkPath{allowed: true}, true)
	a.resolveRefs()
	if len(a.problems) == 0 {
		a.checkReservedRootProperties()
	}
	sort.Strings(a.vendorLocations)
	sort.SliceStable(a.linkFields, func(i, j int) bool {
		return a.linkFields[i].SchemaLocation < a.linkFields[j].SchemaLocation
	})
}

func (a *analyzer) walkSchema(value any, tokens []string, path linkPath, top bool) {
	location := pointer(tokens)
	a.schemaValues[location] = value
	object, ok := value.(map[string]any)
	if !ok {
		return
	}
	a.schemaObjects[location] = object

	for _, keyword := range sortedKeys(object) {
		keywordTokens := child(tokens, keyword)
		keywordLocation := pointer(keywordTokens)

		if message, forbidden := forbiddenKeywords[keyword]; forbidden {
			a.add(keywordLocation, message)
			continue
		}
		if keyword == "$schema" {
			if !top {
				a.add(keywordLocation, "$schema may appear only at the schema root")
			} else if schemaURI, ok := object[keyword].(string); !ok || schemaURI != Draft2020Schema {
				a.add(keywordLocation, "$schema must equal "+Draft2020Schema)
			}
			continue
		}
		if keyword == "$ref" {
			if reference, ok := object[keyword].(string); ok {
				a.refs = append(a.refs, refUse{owner: location, location: keywordLocation, value: reference})
			} else {
				a.add(keywordLocation, "$ref must be a string")
			}
			continue
		}
		if keyword == "pattern" {
			if source, ok := object[keyword].(string); ok {
				compiled, err := regexprofile.Compile(source)
				if err != nil {
					a.add(keywordLocation, "pattern is outside the portable subset: "+err.Error())
				} else {
					a.translatedPatterns[location] = compiled.String()
				}
			}
			continue
		}
		if keyword == "x-engram-link" {
			a.checkLink(object, tokens, path)
			continue
		}
		if strings.HasPrefix(keyword, "x-engram-") {
			a.add(keywordLocation, fmt.Sprintf("reserved engram keyword %q is not defined in v1", keyword))
			continue
		}
		if isVendorKeyword(keyword) {
			a.vendorLocations = append(a.vendorLocations, keywordLocation)
			continue
		}
		if _, recognized := recognizedKeywords[keyword]; !recognized {
			a.add(keywordLocation, fmt.Sprintf("unknown JSON Schema keyword %q", keyword))
		}
	}

	// Mapping-of-schema keywords.
	a.walkSchemaMap(object["$defs"], child(tokens, "$defs"), linkPath{allowed: false})
	if properties, ok := object["properties"].(map[string]any); ok {
		for _, name := range sortedKeys(properties) {
			childPath := linkPath{allowed: path.allowed}
			if path.allowed {
				childPath.selectors = appendSelectors(path.selectors, linkSelector{property: name})
			}
			a.walkSchema(properties[name], child(child(tokens, "properties"), name), childPath, false)
		}
	}
	a.walkSchemaMap(object["dependentSchemas"], child(tokens, "dependentSchemas"), linkPath{allowed: false})

	// Arrays of schemas.
	for _, keyword := range []string{"prefixItems", "allOf", "anyOf", "oneOf"} {
		values, ok := object[keyword].([]any)
		if !ok {
			continue
		}
		for index, item := range values {
			a.walkSchema(item, child(child(tokens, keyword), strconv.Itoa(index)), linkPath{allowed: false}, false)
		}
	}

	// Single-schema keywords other than items cannot occur on a link path.
	for _, keyword := range []string{
		"contains", "additionalProperties", "propertyNames", "if", "then", "else", "not",
		"unevaluatedItems", "unevaluatedProperties", "contentSchema",
	} {
		if item, exists := object[keyword]; exists && isSchemaValue(item) {
			a.walkSchema(item, child(tokens, keyword), linkPath{allowed: false}, false)
		}
	}

	if item, exists := object["items"]; exists && isSchemaValue(item) {
		childPath := linkPath{allowed: path.allowed}
		if path.allowed {
			childPath.selectors = appendSelectors(path.selectors, linkSelector{items: true})
		}
		a.walkSchema(item, child(tokens, "items"), childPath, false)
	}
}

func (a *analyzer) walkSchemaMap(value any, tokens []string, path linkPath) {
	mapping, ok := value.(map[string]any)
	if !ok {
		return
	}
	for _, name := range sortedKeys(mapping) {
		a.walkSchema(mapping[name], child(tokens, name), path, false)
	}
}

func isSchemaValue(value any) bool {
	switch value.(type) {
	case bool, map[string]any:
		return true
	default:
		return false
	}
}

func isVendorKeyword(keyword string) bool {
	return vendorKeywordPattern.MatchString(keyword) && !strings.HasPrefix(keyword, "x-engram-")
}

func (a *analyzer) resolveRefs() {
	for _, reference := range a.refs {
		target, err := profileRefTarget(reference.value)
		if err != nil {
			a.add(reference.location, err.Error())
			continue
		}
		if _, exists := a.schemaValues[target]; !exists {
			a.add(reference.location, fmt.Sprintf("$ref target %q is unresolved or is not a schema-valued position", target))
			continue
		}
		a.refTargets[reference.owner] = target
	}
}

func profileRefTarget(reference string) (string, error) {
	if !strings.HasPrefix(reference, "#") || strings.Contains(reference[1:], "#") {
		return "", fmt.Errorf("$ref must be a fragment-only URI reference")
	}
	if !validURIFragment(reference[1:]) {
		return "", fmt.Errorf("$ref fragment is not a valid RFC 3986 URI fragment")
	}
	parsed, err := url.Parse(reference)
	if err != nil {
		return "", fmt.Errorf("invalid $ref URI reference: %v", err)
	}
	if parsed.Scheme != "" || parsed.Host != "" || parsed.Path != "" || parsed.RawQuery != "" || parsed.ForceQuery {
		return "", fmt.Errorf("$ref must be a fragment-only URI reference")
	}
	tokens, err := decodePointer(parsed.Fragment)
	if err != nil {
		return "", fmt.Errorf("$ref fragment must be an RFC 6901 JSON Pointer: %v", err)
	}
	if len(tokens) < 2 || tokens[0] != "$defs" {
		return "", fmt.Errorf("$ref pointer must start with $defs and contain a target token")
	}
	return pointer(tokens), nil
}

func validURIFragment(fragment string) bool {
	for index := 0; index < len(fragment); index++ {
		current := fragment[index]
		if current == '%' {
			if index+2 >= len(fragment) || !asciiHex(fragment[index+1]) || !asciiHex(fragment[index+2]) {
				return false
			}
			index += 2
			continue
		}
		if current >= 'a' && current <= 'z' || current >= 'A' && current <= 'Z' ||
			current >= '0' && current <= '9' || strings.ContainsRune("-._~!$&'()*+,;=:@/?", rune(current)) {
			continue
		}
		return false
	}
	return true
}

func asciiHex(value byte) bool {
	return value >= '0' && value <= '9' || value >= 'a' && value <= 'f' || value >= 'A' && value <= 'F'
}

func (a *analyzer) checkReservedRootProperties() {
	queue := []string{""}
	visited := make(map[string]struct{})
	for len(queue) != 0 {
		location := queue[0]
		queue = queue[1:]
		if _, seen := visited[location]; seen {
			continue
		}
		visited[location] = struct{}{}
		object, ok := a.schemaObjects[location]
		if !ok {
			continue
		}
		base, _ := decodePointer(location)

		a.checkReservedMapKeys(object["properties"], child(base, "properties"))
		a.checkReservedStringList(object["required"], child(base, "required"))
		a.checkReservedMapKeys(object["dependentSchemas"], child(base, "dependentSchemas"))
		if dependencies, ok := object["dependentRequired"].(map[string]any); ok {
			for _, name := range sortedKeys(dependencies) {
				if reservedInstanceName(name) {
					a.add(pointer(child(child(base, "dependentRequired"), name)),
						fmt.Sprintf("reserved root instance property %q", name))
				}
				a.checkReservedStringList(dependencies[name], child(child(base, "dependentRequired"), name))
			}
		}

		for _, keyword := range []string{"allOf", "anyOf", "oneOf"} {
			if values, ok := object[keyword].([]any); ok {
				for index := range values {
					childLocation := pointer(child(child(base, keyword), strconv.Itoa(index)))
					if _, exists := a.schemaValues[childLocation]; exists {
						queue = append(queue, childLocation)
					}
				}
			}
		}
		for _, keyword := range []string{"not", "if", "then", "else"} {
			childLocation := pointer(child(base, keyword))
			if _, exists := a.schemaValues[childLocation]; exists {
				queue = append(queue, childLocation)
			}
		}
		if dependencies, ok := object["dependentSchemas"].(map[string]any); ok {
			for _, name := range sortedKeys(dependencies) {
				childLocation := pointer(child(child(base, "dependentSchemas"), name))
				if _, exists := a.schemaValues[childLocation]; exists {
					queue = append(queue, childLocation)
				}
			}
		}
		if target, exists := a.refTargets[location]; exists {
			queue = append(queue, target)
		}
	}
}

func (a *analyzer) checkReservedMapKeys(value any, tokens []string) {
	mapping, ok := value.(map[string]any)
	if !ok {
		return
	}
	for _, name := range sortedKeys(mapping) {
		if reservedInstanceName(name) {
			a.add(pointer(child(tokens, name)), fmt.Sprintf("reserved root instance property %q", name))
		}
	}
}

func (a *analyzer) checkReservedStringList(value any, tokens []string) {
	items, ok := value.([]any)
	if !ok {
		return
	}
	for index, item := range items {
		name, ok := item.(string)
		if ok && reservedInstanceName(name) {
			a.add(pointer(child(tokens, strconv.Itoa(index))), fmt.Sprintf("reserved root instance property %q", name))
		}
	}
}

func reservedInstanceName(name string) bool {
	return strings.HasPrefix(name, "engram-")
}

func (a *analyzer) missingUniversalLabels() bool {
	closed, ok := a.root["additionalProperties"].(bool)
	if !ok || closed {
		return false
	}
	properties, ok := a.root["properties"].(map[string]any)
	if !ok {
		return true
	}
	_, hasType := properties["type"]
	_, hasDescription := properties["description"]
	return !hasType || !hasDescription
}

func (a *analyzer) prepareCompilerDocument() {
	for location, object := range a.schemaObjects {
		for keyword := range object {
			if keyword == "x-engram-link" || isVendorKeyword(keyword) {
				delete(object, keyword)
			}
		}
		if format, ok := object["format"].(string); ok && format != "date" && format != "date-time" {
			delete(object, "format")
		}
		if translated, ok := a.translatedPatterns[location]; ok {
			object["pattern"] = translated
		}
	}
}

func (a *analyzer) add(location, message string) {
	a.problems = append(a.problems, Problem{Location: location, Message: message})
}
