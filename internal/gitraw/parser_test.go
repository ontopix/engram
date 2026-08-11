package gitraw

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestParseCommitBothObjectFormats(t *testing.T) {
	t.Parallel()
	for _, format := range []ObjectFormat{SHA1, SHA256} {
		format := format
		t.Run(string(format), func(t *testing.T) {
			t.Parallel()
			tree := strings.Repeat("1", format.HexWidth())
			parent := strings.Repeat("2", format.HexWidth())
			raw := []byte("tree " + tree + "\nparent " + parent + "\nauthor A <a@example.test> 0 +0000\ngpgsig opaque\n continuation\n\nmessage\x00\xff")
			commit, err := ParseCommit(format, raw)
			if err != nil {
				t.Fatal(err)
			}
			if commit.Tree.String() != tree || len(commit.Parents) != 1 || commit.Parents[0].String() != parent {
				t.Fatalf("commit = %#v", commit)
			}
			if !bytes.Equal(commit.Message, []byte("message\x00\xff")) {
				t.Fatalf("message = %x", commit.Message)
			}
			if len(commit.Headers[3].Continuations) != 1 || string(commit.Headers[3].Continuations[0]) != "continuation" {
				t.Fatalf("headers = %#v", commit.Headers)
			}
		})
	}
}

func TestParseCommitRejectsMalformedFraming(t *testing.T) {
	t.Parallel()
	oid := strings.Repeat("1", SHA1.HexWidth())
	parent := strings.Repeat("2", SHA1.HexWidth())
	tests := []struct {
		name string
		raw  string
	}{
		{"empty header", "\n\n"},
		{"missing separator", "tree " + oid + "\n"},
		{"orphan continuation", " continuation\n\n"},
		{"first not tree", "author x\n\n"},
		{"tree continuation", "tree " + oid + "\n continuation\n\n"},
		{"parent continuation", "tree " + oid + "\nparent " + parent + "\n continuation\n\n"},
		{"late parent", "tree " + oid + "\nauthor x\nparent " + parent + "\n\n"},
		{"second tree", "tree " + oid + "\ntree " + oid + "\n\n"},
		{"uppercase oid", "tree " + strings.Repeat("A", SHA1.HexWidth()) + "\n\n"},
		{"short oid", "tree 1234\n\n"},
		{"CR value", "tree " + oid + "\ra\n\n"},
		{"NUL value", "tree " + oid + "\x00\n\n"},
		{"bad name", "tree " + oid + "\nna\x7fme value\n\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := ParseCommit(SHA1, []byte(test.raw))
			if !errors.Is(err, ErrMalformedObject) {
				t.Fatalf("error = %v, want malformed", err)
			}
		})
	}
}

func TestParseTreeBothObjectFormatsAndCanonicalOrder(t *testing.T) {
	t.Parallel()
	for _, format := range []ObjectFormat{SHA1, SHA256} {
		format := format
		t.Run(string(format), func(t *testing.T) {
			t.Parallel()
			raw := appendTreeEntry(nil, format, ModeRegular, "foo.c", 1)
			raw = appendTreeEntry(raw, format, ModeDirectory, "foo", 2)
			raw = appendTreeEntry(raw, format, ModeExecutable, "run", 3)
			entries, err := ParseTree(format, raw)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 3 || string(entries[0].Name) != "foo.c" || entries[1].Mode != ModeDirectory || len(entries[2].OID.Raw()) != format.RawWidth() {
				t.Fatalf("entries = %#v", entries)
			}
		})
	}
}

func TestParseTreeRejectsMalformedInputWithoutRepair(t *testing.T) {
	t.Parallel()
	valid := appendTreeEntry(nil, SHA1, ModeRegular, "a", 1)
	tests := []struct {
		name string
		raw  []byte
	}{
		{"pretty directory mode", appendTreeEntryMode(nil, SHA1, "040000", "a", 1)},
		{"unknown mode", appendTreeEntryMode(nil, SHA1, "100600", "a", 1)},
		{"empty name", appendTreeEntry(nil, SHA1, ModeRegular, "", 1)},
		{"slash name", appendTreeEntry(nil, SHA1, ModeRegular, "a/b", 1)},
		{"truncated oid", valid[:len(valid)-1]},
		{"missing space", []byte("100644a\x00")},
		{"missing nul", []byte("100644 a")},
		{"out of order", append(appendTreeEntry(nil, SHA1, ModeRegular, "b", 1), appendTreeEntry(nil, SHA1, ModeRegular, "a", 2)...)},
		{"duplicate", append(appendTreeEntry(nil, SHA1, ModeRegular, "a", 1), appendTreeEntry(nil, SHA1, ModeExecutable, "a", 2)...)},
		{"directory virtual slash order", append(appendTreeEntry(nil, SHA1, ModeDirectory, "foo", 1), appendTreeEntry(nil, SHA1, ModeRegular, "foo.c", 2)...)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := ParseTree(SHA1, test.raw)
			if !errors.Is(err, ErrMalformedObject) {
				t.Fatalf("error = %v, want malformed", err)
			}
		})
	}
}

func appendTreeEntry(raw []byte, format ObjectFormat, mode TreeMode, name string, fill byte) []byte {
	return appendTreeEntryMode(raw, format, string(mode), name, fill)
}

func appendTreeEntryMode(raw []byte, format ObjectFormat, mode, name string, fill byte) []byte {
	raw = append(raw, mode...)
	raw = append(raw, ' ')
	raw = append(raw, name...)
	raw = append(raw, 0)
	raw = append(raw, bytes.Repeat([]byte{fill}, format.RawWidth())...)
	return raw
}
