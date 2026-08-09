package checker

import (
	"bytes"
	"strings"
	"unicode/utf8"

	"github.com/ontopix/engram/internal/yamlprofile"
)

func normedText(data []byte) bool {
	return len(data) != 0 && utf8.Valid(data) && !bytes.HasPrefix(data, []byte{0xef, 0xbb, 0xbf}) &&
		!bytes.ContainsRune(data, '\r') && data[len(data)-1] == '\n'
}

func frontmatter(data []byte) (*yamlprofile.Document, []byte, error) {
	if !bytes.HasPrefix(data, []byte("---\n")) {
		return nil, nil, errFrontmatter
	}
	position := 4
	for position < len(data) {
		end := bytes.IndexByte(data[position:], '\n')
		if end < 0 {
			break
		}
		end += position
		if bytes.Equal(data[position:end], []byte("---")) {
			document, err := yamlprofile.Parse(data[4:position])
			if err != nil {
				return nil, nil, err
			}
			return document, data[end+1:], nil
		}
		position = end + 1
	}
	return nil, nil, errFrontmatter
}

type frontmatterError string

func (e frontmatterError) Error() string { return string(e) }

const errFrontmatter = frontmatterError("missing exact frontmatter delimiters")

func validDescription(value string) bool {
	count := 0
	for _, character := range value {
		count++
		if character <= 0x1f || character >= 0x7f && character <= 0x9f || character == 0x2028 || character == 0x2029 {
			return false
		}
	}
	return count >= 1 && count <= 200 && !strings.HasPrefix(value, " ") && !strings.HasSuffix(value, " ")
}

func stringField(root *yamlprofile.Node, name string) (string, bool) {
	node, exists := root.Lookup(name)
	return nodeString(node, exists)
}

func nodeString(node *yamlprofile.Node, exists bool) (string, bool) {
	if !exists || node == nil || node.Kind != yamlprofile.StringKind {
		return "", false
	}
	return node.String, true
}

func boolField(root *yamlprofile.Node, name string) (bool, bool, bool) {
	node, exists := root.Lookup(name)
	if !exists {
		return false, false, true
	}
	if node.Kind != yamlprofile.BooleanKind {
		return false, true, false
	}
	return node.Boolean, true, true
}

func mappingField(root *yamlprofile.Node, name string) (*yamlprofile.Node, bool) {
	node, exists := root.Lookup(name)
	if !exists || node.Kind != yamlprofile.MappingKind {
		return nil, false
	}
	return node, true
}

func unknownKeys(root *yamlprofile.Node, allowed map[string]struct{}) bool {
	for _, member := range root.Mapping {
		if _, ok := allowed[member.Key]; !ok {
			return true
		}
	}
	return false
}
