package schemaprofile

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// LinkField describes one valid x-engram-link declaration.
type LinkField struct {
	// SchemaLocation is the RFC 6901 location of x-engram-link.
	SchemaLocation string
	// InstancePattern is the deterministic instance path, using * for each
	// array item selected by an items edge.
	InstancePattern string
	Types           []string
	MustExist       bool

	selectors []linkSelector
}

// LinkOccurrence is one string at a declared link position in an instance.
type LinkOccurrence struct {
	InstanceLocation string
	Value            string
	Field            LinkField
}

type linkSelector struct {
	property string
	items    bool
}

func (a *analyzer) checkLink(object map[string]any, schemaTokens []string, path linkPath) {
	keywordTokens := child(schemaTokens, "x-engram-link")
	keywordLocation := pointer(keywordTokens)
	valid := true
	if !path.allowed || len(path.selectors) == 0 {
		a.add(keywordLocation, "x-engram-link is not on a properties/items-only instance path")
		valid = false
	}
	if kind, ok := object["type"].(string); !ok || kind != "string" {
		a.add(keywordLocation, "x-engram-link requires the direct sibling type: string")
		valid = false
	}

	declaration, ok := object["x-engram-link"].(map[string]any)
	if !ok {
		a.add(keywordLocation, "x-engram-link must be a mapping")
		return
	}
	for _, key := range sortedKeys(declaration) {
		if key != "types" && key != "must-exist" {
			a.add(pointer(child(keywordTokens, key)), fmt.Sprintf("unknown x-engram-link member %q", key))
			valid = false
		}
	}

	typesValue, exists := declaration["types"]
	types, ok := typesValue.([]any)
	if !exists || !ok || len(types) == 0 {
		a.add(pointer(child(keywordTokens, "types")), "x-engram-link types must be a non-empty sequence")
		valid = false
	}
	parsedTypes := make([]string, 0, len(types))
	seen := make(map[string]struct{}, len(types))
	for index, item := range types {
		location := pointer(child(child(keywordTokens, "types"), strconv.Itoa(index)))
		typeName, ok := item.(string)
		if !ok || !validTypeSlug(typeName) {
			a.add(location, "link type must be a type slug of 1 through 64 ASCII characters")
			valid = false
			continue
		}
		if _, duplicate := seen[typeName]; duplicate {
			a.add(location, fmt.Sprintf("duplicate link type %q", typeName))
			valid = false
			continue
		}
		seen[typeName] = struct{}{}
		parsedTypes = append(parsedTypes, typeName)
	}

	mustExist := true
	if value, exists := declaration["must-exist"]; exists {
		var boolValue bool
		boolValue, ok = value.(bool)
		if !ok {
			a.add(pointer(child(keywordTokens, "must-exist")), "x-engram-link must-exist must be boolean")
			valid = false
		} else {
			mustExist = boolValue
		}
	}
	if !valid {
		return
	}

	a.linkFields = append(a.linkFields, LinkField{
		SchemaLocation:  keywordLocation,
		InstancePattern: selectorPattern(path.selectors),
		Types:           append([]string(nil), parsedTypes...),
		MustExist:       mustExist,
		selectors:       appendSelectors(nil, path.selectors...),
	})
}

func validTypeSlug(value string) bool {
	if len(value) == 0 || len(value) > 64 || value[0] == '-' || value[len(value)-1] == '-' {
		return false
	}
	previousHyphen := false
	for index := 0; index < len(value); index++ {
		current := value[index]
		if current == '-' {
			if previousHyphen {
				return false
			}
			previousHyphen = true
			continue
		}
		if current < 'a' || current > 'z' {
			if current < '0' || current > '9' {
				return false
			}
		}
		previousHyphen = false
	}
	return true
}

func appendSelectors(base []linkSelector, additions ...linkSelector) []linkSelector {
	result := make([]linkSelector, len(base), len(base)+len(additions))
	copy(result, base)
	return append(result, additions...)
}

func selectorPattern(selectors []linkSelector) string {
	tokens := make([]string, 0, len(selectors))
	for _, selector := range selectors {
		if selector.items {
			tokens = append(tokens, "*")
		} else {
			tokens = append(tokens, selector.property)
		}
	}
	return pointer(tokens)
}

func cloneLinkFields(fields []LinkField) []LinkField {
	result := make([]LinkField, len(fields))
	for index, field := range fields {
		field.Types = append([]string(nil), field.Types...)
		field.selectors = appendSelectors(nil, field.selectors...)
		result[index] = field
	}
	return result
}

// ExtractLinks walks the deterministic paths described by x-engram-link and
// returns every string occurrence with its actual RFC 6901 instance pointer.
// Non-string values are omitted; ordinary schema validation reports their
// type failure before link checking runs.
func (s *Schema) ExtractLinks(instance any) []LinkOccurrence {
	if s == nil {
		return nil
	}
	result := make([]LinkOccurrence, 0)
	for _, field := range s.linkFields {
		extractLinkField(instance, nil, field.selectors, field, &result)
	}
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].InstanceLocation != result[j].InstanceLocation {
			return result[i].InstanceLocation < result[j].InstanceLocation
		}
		return result[i].Field.SchemaLocation < result[j].Field.SchemaLocation
	})
	return result
}

func extractLinkField(value any, tokens []string, selectors []linkSelector, field LinkField, result *[]LinkOccurrence) {
	if len(selectors) == 0 {
		if text, ok := value.(string); ok {
			*result = append(*result, LinkOccurrence{
				InstanceLocation: pointer(tokens),
				Value:            text,
				Field:            cloneLinkFields([]LinkField{field})[0],
			})
		}
		return
	}
	selector := selectors[0]
	if selector.items {
		items, ok := value.([]any)
		if !ok {
			return
		}
		for index, item := range items {
			extractLinkField(item, child(tokens, strconv.Itoa(index)), selectors[1:], field, result)
		}
		return
	}
	object, ok := value.(map[string]any)
	if !ok {
		return
	}
	item, exists := object[selector.property]
	if !exists {
		return
	}
	extractLinkField(item, child(tokens, selector.property), selectors[1:], field, result)
}

// LinkPointers is a convenience projection of ExtractLinks for callers that
// only need actual instance locations.
func (s *Schema) LinkPointers(instance any) []string {
	occurrences := s.ExtractLinks(instance)
	result := make([]string, len(occurrences))
	for index := range occurrences {
		result[index] = occurrences[index].InstanceLocation
	}
	return result
}

// String provides a compact diagnostic representation for logs.
func (field LinkField) String() string {
	return fmt.Sprintf("%s -> %s [%s]", field.SchemaLocation, field.InstancePattern, strings.Join(field.Types, ","))
}
