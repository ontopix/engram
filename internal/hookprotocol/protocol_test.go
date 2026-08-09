package hookprotocol

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	"github.com/ontopix/engram/internal/changeset"
)

func TestMarshalInputExactBytes(t *testing.T) {
	t.Parallel()
	input, err := MarshalInput([]changeset.Change{
		{Operation: changeset.Added, Path: "people/ada.md"},
		{Operation: changeset.Modified, Path: "mémoire.md"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "{\"version\":1,\"event\":\"prepare-changeset\",\"changes\":[{\"operation\":\"added\",\"path\":\"people/ada.md\"},{\"operation\":\"modified\",\"path\":\"mémoire.md\"}]}\n"
	if string(input) != want {
		t.Fatalf("input = %q, want %q", input, want)
	}
}

func TestJSONStringEscapingUsesUppercaseControlEscapes(t *testing.T) {
	t.Parallel()
	var buffer bytes.Buffer
	writeJSONString(&buffer, "quote-\"-\\-\n-\u001f-é")
	want := "\"quote-\\\"-\\\\-\\u000A-\\u001F-é\""
	if buffer.String() != want {
		t.Fatalf("JSON string = %q, want %q", buffer.String(), want)
	}
}

func TestMarshalInputRejectsInvalidData(t *testing.T) {
	t.Parallel()
	for _, change := range []changeset.Change{
		{Operation: "renamed", Path: "a.md"},
		{Operation: changeset.Added, Path: "../a.md"},
		{Operation: changeset.Added, Path: "a\\b.md"},
	} {
		if _, err := MarshalInput([]changeset.Change{change}); err == nil {
			t.Errorf("accepted %#v", change)
		}
	}
}

func TestInterpreterGrammar(t *testing.T) {
	t.Parallel()
	valid := map[string]string{
		"#!/usr/bin/env python3\n":       "python3",
		"#!/usr/bin/env node.js-20+\n":   "node.js-20+",
		"#!/usr/bin/env tool_name\nbody": "tool_name",
	}
	for source, want := range valid {
		got, err := Interpreter([]byte(source))
		if err != nil || got != want {
			t.Errorf("Interpreter(%q) = %q, %v", source, got, err)
		}
	}
	for _, source := range []string{"", "#!/bin/sh\n", "#!/usr/bin/env \n", "#!/usr/bin/env python 3\n", "#!/usr/bin/env /bin/sh\n"} {
		if _, err := Interpreter([]byte(source)); err == nil {
			t.Errorf("Interpreter(%q) succeeded", source)
		}
	}
}

func TestEnvironmentIsClosed(t *testing.T) {
	t.Parallel()
	inherited := []string{"PATH=/bin", "HOME=/home/test", "git_dir=bad", "EnGrAm_Base=bad"}
	got, err := Environment(inherited, "/base", "/candidate", true)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"PATH=/bin", "HOME=/home/test", "ENGRAM_HOOK_PROTOCOL=1", "ENGRAM_BASE=/base", "ENGRAM_CANDIDATE=/candidate"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("environment = %#v, want %#v", got, want)
	}
	for _, entry := range got[:2] {
		if strings.HasPrefix(strings.ToUpper(entry), "GIT_") || strings.HasPrefix(strings.ToUpper(entry), "ENGRAM_") {
			t.Fatalf("reserved inherited entry survived: %q", entry)
		}
	}
}

func TestEnvironmentRejectsAliasingNames(t *testing.T) {
	t.Parallel()
	if _, err := Environment([]string{"Path=/one", "PATH=/two"}, "/base", "/candidate", true); err == nil {
		t.Fatal("case-insensitive alias accepted")
	}
	if _, err := Environment([]string{"Path=/one", "PATH=/two"}, "/base", "/candidate", false); err != nil {
		t.Fatalf("case-sensitive host rejected distinct names: %v", err)
	}
}
