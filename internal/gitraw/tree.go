package gitraw

import (
	"bytes"
)

type TreeMode string

const (
	ModeDirectory  TreeMode = "40000"
	ModeRegular    TreeMode = "100644"
	ModeExecutable TreeMode = "100755"
	ModeSymlink    TreeMode = "120000"
	ModeGitlink    TreeMode = "160000"
)

func (m TreeMode) IsDirectory() bool { return m == ModeDirectory }
func (m TreeMode) IsRegular() bool   { return m == ModeRegular || m == ModeExecutable }

type TreeEntry struct {
	Mode TreeMode
	Name []byte
	OID  OID
}

// ParseTree validates raw tree grammar and canonical input order. It never
// sorts or repairs entries.
func ParseTree(format ObjectFormat, raw []byte) ([]TreeEntry, error) {
	width := format.RawWidth()
	if width == 0 {
		return nil, &Error{Kind: FailureCapability, Op: "parse-tree", Detail: "unsupported object format"}
	}
	entries := make([]TreeEntry, 0)
	seen := make(map[string]struct{})
	for offset := 0; offset < len(raw); {
		entryOffset := offset
		spaceRelative := bytes.IndexByte(raw[offset:], ' ')
		if spaceRelative < 0 {
			return nil, malformed("parse-tree", offset, "missing mode separator")
		}
		space := offset + spaceRelative
		mode := TreeMode(raw[offset:space])
		if !admittedMode(mode) {
			return nil, malformed("parse-tree", offset, "unadmitted raw mode %q", mode)
		}
		nameStart := space + 1
		nulRelative := bytes.IndexByte(raw[nameStart:], 0)
		if nulRelative < 0 {
			return nil, malformed("parse-tree", nameStart, "missing name terminator")
		}
		nul := nameStart + nulRelative
		name := raw[nameStart:nul]
		if len(name) == 0 || bytes.IndexByte(name, '/') >= 0 {
			return nil, malformed("parse-tree", nameStart, "name is not one non-empty component")
		}
		oidStart := nul + 1
		if oidStart+width > len(raw) {
			return nil, malformed("parse-tree", oidStart, "truncated object ID")
		}
		entry := TreeEntry{Mode: mode, Name: append([]byte(nil), name...), OID: oidFromRaw(format, raw[oidStart:oidStart+width])}
		nameKey := string(name)
		if _, duplicate := seen[nameKey]; duplicate {
			return nil, malformed("parse-tree", entryOffset, "duplicate name")
		}
		seen[nameKey] = struct{}{}
		if len(entries) != 0 && compareTreeEntries(entries[len(entries)-1], entry) >= 0 {
			return nil, malformed("parse-tree", entryOffset, "entries are not in canonical raw order")
		}
		entries = append(entries, entry)
		offset = oidStart + width
	}
	return entries, nil
}

func admittedMode(mode TreeMode) bool {
	switch mode {
	case ModeDirectory, ModeRegular, ModeExecutable, ModeSymlink, ModeGitlink:
		return true
	default:
		return false
	}
}

func compareTreeEntries(left, right TreeEntry) int {
	leftKey := append(append([]byte(nil), left.Name...), treeTerminator(left.Mode))
	rightKey := append(append([]byte(nil), right.Name...), treeTerminator(right.Mode))
	return bytes.Compare(leftKey, rightKey)
}

func treeTerminator(mode TreeMode) byte {
	if mode == ModeDirectory {
		return '/'
	}
	return 0
}
