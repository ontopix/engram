package gitraw

import (
	"bytes"
	"fmt"
)

type Header struct {
	Name          string
	Value         []byte
	Continuations [][]byte
}

type Commit struct {
	Tree    OID
	Parents []OID
	Headers []Header
	Message []byte
}

// ParseCommit implements annex A.1 over raw commit content (without the loose
// object header). Unknown headers and message bytes remain opaque.
func ParseCommit(format ObjectFormat, raw []byte) (*Commit, error) {
	if format.RawWidth() == 0 {
		return nil, &Error{Kind: FailureCapability, Op: "parse-commit", Detail: "unsupported object format"}
	}
	separator := bytes.Index(raw, []byte("\n\n"))
	if separator <= 0 {
		return nil, malformed("parse-commit", 0, "missing non-empty LF-terminated header block")
	}
	headerBytes := raw[:separator]
	physical := bytes.Split(headerBytes, []byte{'\n'})
	headers := make([]Header, 0, len(physical))
	headerOffsets := make([]int, 0, len(physical))
	offset := 0
	for _, line := range physical {
		if len(line) == 0 {
			return nil, malformed("parse-commit", offset, "empty physical header line")
		}
		if line[0] == ' ' {
			if len(headers) == 0 {
				return nil, malformed("parse-commit", offset, "orphan continuation")
			}
			if invalidHeaderValue(line[1:]) {
				return nil, malformed("parse-commit", offset, "invalid continuation value")
			}
			headers[len(headers)-1].Continuations = append(headers[len(headers)-1].Continuations, append([]byte(nil), line[1:]...))
			offset += len(line) + 1
			continue
		}
		space := bytes.IndexByte(line, ' ')
		if space <= 0 {
			return nil, malformed("parse-commit", offset, "header lacks name/value separator")
		}
		nameBytes := line[:space]
		for _, character := range nameBytes {
			if character < 0x21 || character > 0x7e {
				return nil, malformed("parse-commit", offset, "invalid header name")
			}
		}
		value := line[space+1:]
		if invalidHeaderValue(value) {
			return nil, malformed("parse-commit", offset+space+1, "invalid header value")
		}
		headerOffsets = append(headerOffsets, offset)
		headers = append(headers, Header{Name: string(nameBytes), Value: append([]byte(nil), value...)})
		offset += len(line) + 1
	}

	if len(headers) == 0 || headers[0].Name != "tree" || len(headers[0].Continuations) != 0 {
		return nil, malformed("parse-commit", 0, "first header must be one simple tree header")
	}
	tree, err := ParseOID(format, string(headers[0].Value))
	if err != nil {
		return nil, malformed("parse-commit", headerOffsets[0], "tree value is not a canonical object ID")
	}

	commit := &Commit{Tree: tree, Headers: headers, Message: append([]byte(nil), raw[separator+2:]...)}
	parentPhase := true
	for index, header := range headers[1:] {
		physicalOffset := headerOffsets[index+1]
		switch header.Name {
		case "tree":
			return nil, malformed("parse-commit", physicalOffset, "tree header appears outside first position")
		case "parent":
			if !parentPhase || len(header.Continuations) != 0 {
				return nil, malformed("parse-commit", physicalOffset, "parent header is not simple and contiguous")
			}
			parent, parseErr := ParseOID(format, string(header.Value))
			if parseErr != nil {
				return nil, malformed("parse-commit", physicalOffset, "parent value is not a canonical object ID")
			}
			commit.Parents = append(commit.Parents, parent)
		default:
			parentPhase = false
		}
	}
	return commit, nil
}

func invalidHeaderValue(value []byte) bool {
	return bytes.IndexByte(value, 0) >= 0 || bytes.IndexByte(value, '\r') >= 0 || bytes.IndexByte(value, '\n') >= 0
}

func (c Commit) String() string {
	return fmt.Sprintf("commit(tree=%s, parents=%d)", c.Tree, len(c.Parents))
}
