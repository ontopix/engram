// Package hookprotocol implements the byte-level preparation-hook process
// contract independently from hook selection and process execution.
package hookprotocol

import (
	"bytes"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/ontopix/engram/internal/changeset"
)

const InterpreterPrefix = "#!/usr/bin/env "

// Interpreter returns the exact admitted interpreter token from a hook's
// first line.
func Interpreter(program []byte) (string, error) {
	lineEnd := bytes.IndexByte(program, '\n')
	if lineEnd < 0 {
		return "", fmt.Errorf("hook has no LF-terminated interpreter line")
	}
	line := string(program[:lineEnd])
	if !strings.HasPrefix(line, InterpreterPrefix) {
		return "", fmt.Errorf("hook interpreter line has the wrong prefix")
	}
	name := strings.TrimPrefix(line, InterpreterPrefix)
	if name == "" {
		return "", fmt.Errorf("hook interpreter token is empty")
	}
	for _, character := range name {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' || strings.ContainsRune("._+-", character) {
			continue
		}
		return "", fmt.Errorf("hook interpreter token contains a forbidden character")
	}
	return name, nil
}

// MarshalInput serializes the current ordered changeset using the exact JSON
// spelling required by core Appendix C.3.
func MarshalInput(changes []changeset.Change) ([]byte, error) {
	var result bytes.Buffer
	result.WriteString(`{"version":1,"event":"prepare-changeset","changes":[`)
	for index, change := range changes {
		if index != 0 {
			result.WriteByte(',')
		}
		switch change.Operation {
		case changeset.Added, changeset.Modified, changeset.Deleted:
		default:
			return nil, fmt.Errorf("invalid changeset operation %q", change.Operation)
		}
		if !validLogicalPath(change.Path) {
			return nil, fmt.Errorf("invalid changeset path %q", change.Path)
		}
		result.WriteString(`{"operation":`)
		writeJSONString(&result, string(change.Operation))
		result.WriteString(`,"path":`)
		writeJSONString(&result, change.Path)
		result.WriteByte('}')
	}
	result.WriteString("]}\n")
	return result.Bytes(), nil
}

// Environment removes every inherited Git and engram protocol variable
// under ASCII case-insensitive comparison and installs exactly the three
// protocol variables. Duplicate inherited variable names under that same
// comparison are rejected on case-insensitive hosts by the caller-selected
// flag.
func Environment(inherited []string, base, candidate string, caseInsensitiveHost bool) ([]string, error) {
	seen := make(map[string]struct{})
	result := make([]string, 0, len(inherited)+3)
	for _, item := range inherited {
		name, _, found := strings.Cut(item, "=")
		if !found || name == "" || strings.ContainsRune(name, 0) {
			return nil, fmt.Errorf("invalid inherited environment entry")
		}
		folded := asciiUpper(name)
		if strings.HasPrefix(folded, "ENGRAM_") || strings.HasPrefix(folded, "GIT_") {
			continue
		}
		key := name
		if caseInsensitiveHost {
			key = folded
		}
		if _, duplicate := seen[key]; duplicate {
			return nil, fmt.Errorf("inherited environment contains an aliasing name collision")
		}
		seen[key] = struct{}{}
		result = append(result, item)
	}
	if strings.ContainsAny(base, "\x00\r\n") || strings.ContainsAny(candidate, "\x00\r\n") {
		return nil, fmt.Errorf("protocol path contains a forbidden character")
	}
	return append(result,
		"ENGRAM_HOOK_PROTOCOL=1",
		"ENGRAM_BASE="+base,
		"ENGRAM_CANDIDATE="+candidate,
	), nil
}

func writeJSONString(destination *bytes.Buffer, value string) {
	destination.WriteByte('"')
	for _, character := range value {
		switch character {
		case '"':
			destination.WriteString(`\"`)
		case '\\':
			destination.WriteString(`\\`)
		default:
			if character >= 0 && character <= 0x1f {
				fmt.Fprintf(destination, `\u%04X`, character)
			} else {
				destination.WriteRune(character)
			}
		}
	}
	destination.WriteByte('"')
}

func validLogicalPath(value string) bool {
	if value == "" || !utf8.ValidString(value) || strings.HasPrefix(value, "/") || strings.HasSuffix(value, "/") || strings.Contains(value, "\\") {
		return false
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
	}
	return true
}

func asciiUpper(value string) string {
	result := []byte(value)
	for index, character := range result {
		if character >= 'a' && character <= 'z' {
			result[index] = character - ('a' - 'A')
		}
	}
	return string(result)
}
