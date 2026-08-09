package regexprofile

import "testing"

func TestValidPatterns(t *testing.T) {
	t.Parallel()
	tests := []struct {
		pattern string
		match   string
		miss    string
	}{
		{"^[a-z0-9]+(-[a-z0-9]+)*$", "project-17", "Project"},
		{"a|bc", "xxbcxx", "zzz"},
		{"(ab){2,3}", "abab", "ab"},
		{"[a-cx]", "b", "z"},
		{"[\\^\\-\\]]", "]", "a"},
		{"a\\!b", "a!b", "ab"},
		{"^a{0,}$", "aaa", "b"},
		{"\\^a\\$", "^a$", "a"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.pattern, func(t *testing.T) {
			t.Parallel()
			compiled, err := Compile(test.pattern)
			if err != nil {
				t.Fatal(err)
			}
			if !compiled.MatchString(test.match) {
				t.Errorf("%q did not match %q", test.pattern, test.match)
			}
			if compiled.MatchString(test.miss) {
				t.Errorf("%q unexpectedly matched %q", test.pattern, test.miss)
			}
		})
	}
}

func TestInvalidPatterns(t *testing.T) {
	t.Parallel()
	patterns := []string{
		"",
		".",
		"a||b",
		"|a",
		"a|",
		"()",
		"(a",
		"a)",
		"*a",
		"a**",
		"a{",
		"a{}",
		"a{01}",
		"a{2,1}",
		"a{65536}",
		"[^a]",
		"[]",
		"[a-]",
		"[-a]",
		"[z-a]",
		"[a-b-c]",
		"[a^]",
		"\\a",
		"a^",
		"a$b",
		"é",
	}
	for _, pattern := range patterns {
		pattern := pattern
		t.Run(pattern, func(t *testing.T) {
			t.Parallel()
			if _, err := Compile(pattern); err == nil {
				t.Fatalf("Compile(%q) succeeded", pattern)
			}
		})
	}
}
