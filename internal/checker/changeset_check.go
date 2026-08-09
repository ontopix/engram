package checker

import (
	"bytes"
	"encoding/json"
	"math/big"
	"reflect"

	"github.com/ontopix/engram/internal/changeset"
	"github.com/ontopix/engram/internal/documentprofile"
	"github.com/ontopix/engram/internal/snapshot"
)

// CheckTransition validates candidate as a snapshot and applies every E5xx
// rule against base. A nil base is the known empty initialization state only
// when initialization is true; otherwise it is unavailable and the status is
// indeterminate.
func CheckTransition(base, candidate *Snapshot, initialization bool) (Result, []changeset.Change) {
	// Protocol arrays are never null. Initialize the empty finding set before
	// any indeterminate early return so every result has one stable shape.
	result := Result{Target: TargetChangeset, Status: StatusComplete, Findings: []Finding{}}
	findings := make(findingSet)
	if candidate == nil {
		result.Status = StatusIndeterminate
		return result, nil
	}
	for _, finding := range candidate.Validation.Findings {
		findings.add(finding.Code, finding.Path, finding.Detail)
	}
	if base == nil && !initialization {
		result.Status = StatusIndeterminate
		result.Findings = findings.sorted()
		return result, nil
	}
	if !changeset.PreflightOK(candidate.Tree) || base != nil && !changeset.PreflightOK(base.Tree) {
		result.Status = StatusIndeterminate
		result.Findings = findings.sorted()
		return result, nil
	}

	var baseTree *snapshot.Tree
	if base != nil {
		baseTree = base.Tree
	}
	changes := changeset.Diff(baseTree, candidate.Tree)
	if base == nil {
		result.Findings = findings.sorted()
		return result, changes
	}
	byPath := make(map[string]changeset.Operation, len(changes))
	for _, change := range changes {
		byPath[change.Path] = change.Operation
	}

	for name, file := range base.Tree.Files {
		operation, changed := byPath[name]
		if !changed || file.Role != snapshot.RoleRecord {
			continue
		}
		record := base.Records[name]
		if record == nil || !record.Policy.Available {
			result.Status = StatusIndeterminate
			continue
		}
		if record.Policy.Immutable {
			findings.add("E501", name, "immutable base record changed")
		}
		if record.Policy.AppendOnly {
			if operation == changeset.Deleted {
				findings.add("E502", name, "append-only base record was deleted")
			} else {
				candidateFile := candidate.Tree.Files[name]
				if !bytes.HasPrefix(candidateFile.Data, file.Data) {
					findings.add("E502", name, "append-only edit is not a byte-exact append")
				}
			}
		}
	}

	requiredTargets := requiredWikilinkTargets(candidate)
	for name, file := range base.Tree.Files {
		if file.Role != snapshot.RoleRecord {
			continue
		}
		candidateFile, remains := candidate.Tree.Files[name]
		if remains && candidateFile.Role == snapshot.RoleRecord {
			continue
		}
		if _, retained := requiredTargets[name]; retained {
			findings.add("E503", name, "candidate retains a required inbound wikilink")
		}
	}

	for name, baseFile := range base.Tree.Files {
		candidateFile, exists := candidate.Tree.Files[name]
		if !exists || baseFile.Role != snapshot.RoleSchema || candidateFile.Role != snapshot.RoleSchema || bytes.Equal(baseFile.Data, candidateFile.Data) {
			continue
		}
		baseSchema, baseOK := base.Schemas[name]
		candidateSchema, candidateOK := candidate.Schemas[name]
		if !baseOK || !candidateOK || baseSchema.Version == nil || candidateSchema.Version == nil ||
			baseSchema.RawSchema == nil || candidateSchema.RawSchema == nil ||
			!baseSchema.BodyValid || !candidateSchema.BodyValid || !baseSchema.PolicyValid || !candidateSchema.PolicyValid {
			result.Status = StatusIndeterminate
			continue
		}
		versionOrder := candidateSchema.Version.Cmp(baseSchema.Version)
		componentsChanged := !jsonValuesEqual(baseSchema.RawSchema, candidateSchema.RawSchema) ||
			!jsonValuesEqual(baseSchema.RawBody, candidateSchema.RawBody) ||
			!jsonValuesEqual(baseSchema.RawPolicy, candidateSchema.RawPolicy)
		if versionOrder < 0 || componentsChanged && versionOrder <= 0 {
			findings.add("E504", name, "schema version does not advance its semantic components")
		}
	}

	result.Findings = findings.sorted()
	return result, changes
}

func requiredWikilinkTargets(candidate *Snapshot) map[string]struct{} {
	result := make(map[string]struct{})
	addBody := func(document []string) {
		for _, raw := range document {
			if link, err := documentprofile.ParseWikilink(raw); err == nil {
				result[link.RecordPath()] = struct{}{}
			}
		}
	}
	for _, record := range candidate.Records {
		raw := make([]string, len(record.Markdown.Wikilinks))
		for index := range record.Markdown.Wikilinks {
			raw[index] = record.Markdown.Wikilinks[index].Raw
		}
		addBody(raw)

		definition := candidate.Schemas[record.SchemaPath]
		typed := make(map[string]bool)
		if definition != nil && definition.SchemaValid {
			for _, occurrence := range definition.Validator.ExtractLinks(record.Frontmatter.JSONValue()) {
				typed[occurrence.InstanceLocation] = true
				if !occurrence.Field.MustExist {
					continue
				}
				if link, recognized, err := documentprofile.ParseScalarWikilink(occurrence.Value); recognized && err == nil {
					result[link.RecordPath()] = struct{}{}
				}
			}
		}
		for _, occurrence := range documentprofile.YAMLWikilinks(record.Frontmatter) {
			if typed[occurrence.Pointer] || occurrence.Err != nil {
				continue
			}
			result[occurrence.Link.RecordPath()] = struct{}{}
		}
	}
	for _, directoryMap := range candidate.Maps {
		raw := make([]string, len(directoryMap.Markdown.Wikilinks))
		for index := range directoryMap.Markdown.Wikilinks {
			raw[index] = directoryMap.Markdown.Wikilinks[index].Raw
		}
		addBody(raw)
	}
	for _, definition := range candidate.Schemas {
		raw := make([]string, len(definition.Markdown.Wikilinks))
		for index := range definition.Markdown.Wikilinks {
			raw[index] = definition.Markdown.Wikilinks[index].Raw
		}
		addBody(raw)
	}
	return result
}

func jsonValuesEqual(left, right any) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	switch left := left.(type) {
	case map[string]any:
		right, ok := right.(map[string]any)
		if !ok || len(left) != len(right) {
			return false
		}
		for key, value := range left {
			other, exists := right[key]
			if !exists || !jsonValuesEqual(value, other) {
				return false
			}
		}
		return true
	case []any:
		right, ok := right.([]any)
		if !ok || len(left) != len(right) {
			return false
		}
		for index := range left {
			if !jsonValuesEqual(left[index], right[index]) {
				return false
			}
		}
		return true
	case json.Number:
		right, ok := right.(json.Number)
		if !ok {
			return false
		}
		leftRat, leftOK := new(big.Rat).SetString(left.String())
		rightRat, rightOK := new(big.Rat).SetString(right.String())
		return leftOK && rightOK && leftRat.Cmp(rightRat) == 0
	default:
		return reflect.DeepEqual(left, right)
	}
}
