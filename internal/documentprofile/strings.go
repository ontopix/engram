package documentprofile

import (
	"strconv"
	"strings"

	"github.com/ontopix/engram/internal/yamlprofile"
)

// StringValue is one recursively discovered YAML string value. Pointer is an
// RFC 6901 JSON Pointer; mapping keys themselves are never emitted.
type StringValue struct {
	Pointer  string
	Position yamlprofile.Position
	Value    string
	Node     *yamlprofile.Node
}

// StringValues returns string values in deterministic depth-first YAML source
// order. It scans mapping values and sequence elements recursively, never
// mapping keys.
func StringValues(root *yamlprofile.Node) []StringValue {
	var values []StringValue
	WalkStringValues(root, func(value StringValue) bool {
		values = append(values, value)
		return true
	})
	return values
}

// WalkStringValues visits every recursively nested YAML string value. A false
// callback result stops the walk.
func WalkStringValues(root *yamlprofile.Node, visit func(StringValue) bool) {
	if visit == nil {
		return
	}
	walkStringValues(root, "", visit)
}

func walkStringValues(node *yamlprofile.Node, pointer string, visit func(StringValue) bool) bool {
	if node == nil {
		return true
	}
	switch node.Kind {
	case yamlprofile.StringKind:
		return visit(StringValue{Pointer: pointer, Position: node.Position, Value: node.String, Node: node})
	case yamlprofile.SequenceKind:
		for index := range node.Sequence {
			if !walkStringValues(node.Sequence[index], pointer+"/"+strconv.Itoa(index), visit) {
				return false
			}
		}
	case yamlprofile.MappingKind:
		for index := range node.Mapping {
			member := node.Mapping[index]
			if !walkStringValues(member.Value, pointer+"/"+escapePointerToken(member.Key), visit) {
				return false
			}
		}
	}
	return true
}

func escapePointerToken(token string) string {
	token = strings.ReplaceAll(token, "~", "~0")
	return strings.ReplaceAll(token, "/", "~1")
}
