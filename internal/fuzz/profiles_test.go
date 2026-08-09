package fuzz_test

import (
	"testing"

	"github.com/ontopix/engram/internal/documentprofile"
	"github.com/ontopix/engram/internal/gitraw"
	"github.com/ontopix/engram/internal/markdownprofile"
	"github.com/ontopix/engram/internal/regexprofile"
	"github.com/ontopix/engram/internal/yamlprofile"
)

func FuzzYAMLProfile(f *testing.F) {
	for _, seed := range []string{"engram: 1\n", "value: 1e400\n", "a: &x 1\nb: *x\n", "!!str value\n"} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, input []byte) {
		_, _ = yamlprofile.Parse(input)
	})
}

func FuzzDocumentAndMarkdownProfiles(f *testing.F) {
	for _, seed := range []string{
		"---\ntype: note\ndescription: ok\n---\n# Title\n",
		"---\na: b\n---\n[[target|label]] [link](asset.png)\n",
		"```\n[[ignored]]\n```\n",
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, input []byte) {
		_, _ = documentprofile.Parse(input)
		_ = markdownprofile.Parse(input)
	})
}

func FuzzWikilinkProfile(f *testing.F) {
	for _, seed := range []string{"[[person/ada]]", "[[path|label]]", "[[../escape]]", "[[nested[[x]]"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, input string) {
		_, _ = documentprofile.ParseWikilink(input)
		_, _, _ = documentprofile.ParseScalarWikilink(input)
	})
}

func FuzzPortableRegex(f *testing.F) {
	for _, seed := range []string{"^[a-z]+$", "(?:a)", "(?=x)", "[\\p{L}]"} {
		f.Add(seed, "value")
	}
	f.Fuzz(func(t *testing.T, pattern, value string) {
		compiled, err := regexprofile.Compile(pattern)
		if err == nil {
			_ = compiled.MatchString(value)
		}
	})
}

func FuzzRawCommit(f *testing.F) {
	for _, seed := range [][]byte{
		[]byte("tree 0123456789012345678901234567890123456789\n\nmessage\n"),
		[]byte("tree bad\nparent bad\n\n"),
	} {
		f.Add(seed, false)
	}
	f.Fuzz(func(t *testing.T, input []byte, useSHA256 bool) {
		format := gitraw.SHA1
		if useSHA256 {
			format = gitraw.SHA256
		}
		_, _ = gitraw.ParseCommit(format, input)
	})
}

func FuzzRawTree(f *testing.F) {
	f.Add([]byte{}, false)
	f.Add([]byte("100644 name\x0001234567890123456789"), false)
	f.Fuzz(func(t *testing.T, input []byte, useSHA256 bool) {
		format := gitraw.SHA1
		if useSHA256 {
			format = gitraw.SHA256
		}
		_, _ = gitraw.ParseTree(format, input)
	})
}
