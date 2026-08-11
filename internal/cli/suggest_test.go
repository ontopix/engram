package cli

import (
	"slices"
	"testing"
)

func TestCommandSuggestionsAreScopedAndDeterministic(t *testing.T) {
	t.Parallel()
	model := DefaultModel()
	tests := []struct {
		name    string
		group   string
		unknown string
		want    []string
	}{
		{name: "top-level transposition", unknown: "statsu", want: []string{"status"}},
		{name: "case mismatch", unknown: "CLONE", want: []string{"clone"}},
		{name: "equally close commands", unknown: "pusl", want: []string{"pull", "push"}},
		{name: "group subcommand", group: "schema", unknown: "shwo", want: []string{"show"}},
		{name: "unrelated short token", unknown: "wat"},
		{name: "bounded input", unknown: "this-command-name-is-deliberately-longer-than-sixty-four-bytes-to-bound-suggestion-work"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got := commandSuggestions(model, test.group, test.unknown)
			if !slices.Equal(got, test.want) {
				t.Fatalf("commandSuggestions(%q, %q) = %#v, want %#v", test.group, test.unknown, got, test.want)
			}
		})
	}
}

func TestEditDistance(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		left, right string
		want        int
	}{
		{"status", "statsu", 1},
		{"clone", "clone", 0},
		{"schema", "schem", 1},
		{"", "push", 4},
	} {
		if got := editDistance(test.left, test.right); got != test.want {
			t.Errorf("editDistance(%q, %q) = %d, want %d", test.left, test.right, got, test.want)
		}
	}
}
