package fuzz_test

import (
	"strconv"
	"strings"
	"testing"

	"github.com/ontopix/engram/internal/documentprofile"
	"github.com/ontopix/engram/internal/gitraw"
	"github.com/ontopix/engram/internal/markdownprofile"
	"github.com/ontopix/engram/internal/regexprofile"
	"github.com/ontopix/engram/internal/snapshot"
	"github.com/ontopix/engram/internal/yamlprofile"
)

type pathSource struct{ name string }

func (s pathSource) ReadDir(logical string) ([]snapshot.Entry, error) {
	if logical == "." {
		return []snapshot.Entry{{Name: s.name, Kind: snapshot.KindRegular}}, nil
	}
	return []snapshot.Entry{}, nil
}

func (pathSource) ReadFile(string) ([]byte, error) { return []byte("opaque\n"), nil }

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

func FuzzLogicalPaths(f *testing.F) {
	for _, seed := range []string{"record.md", "README.md", "../escape", "a/b", ".hidden", "Straße.md", "bad\\name.md", ""} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, name string) {
		_, _ = snapshot.Load(pathSource{name: name})
	})
}

func FuzzJSONPointerTraversal(f *testing.F) {
	for _, seed := range []string{"plain", "a/b", "a~b", "~/", "", "é"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, key string) {
		parsed, err := yamlprofile.Parse([]byte(strconv.Quote(key) + ": value\n"))
		if err != nil {
			return
		}
		values := documentprofile.StringValues(parsed.Root)
		if len(values) != 1 {
			t.Fatalf("string value count = %d", len(values))
		}
		if len(parsed.Root.Mapping) != 1 {
			t.Fatalf("mapping member count = %d", len(parsed.Root.Mapping))
		}
		parsedKey := parsed.Root.Mapping[0].Key
		escaped := strings.ReplaceAll(strings.ReplaceAll(parsedKey, "~", "~0"), "/", "~1")
		if values[0].Pointer != "/"+escaped {
			t.Fatalf("pointer = %q, want %q", values[0].Pointer, "/"+escaped)
		}
	})
}

func FuzzCatalogProfile(f *testing.F) {
	f.Add([]byte("before\n<!-- engram:catalog -->\n<!-- /engram:catalog -->\nafter\n"), "topic", "description")
	f.Add([]byte("<!-- engram:catalog -->\n"), "bad/name", "bad\ndescription")
	f.Fuzz(func(t *testing.T, body []byte, name, description string) {
		detection := documentprofile.DetectCatalog(body)
		generated, err := documentprofile.GenerateCatalog(documentprofile.CatalogAll, nil, []documentprofile.CatalogRecord{{Name: name + ".md", Description: description}})
		if err == nil {
			generatedDetection := documentprofile.DetectCatalog(generated)
			if !generatedDetection.ValidForMode(documentprofile.CatalogAll) || !generatedDetection.RegionMatches(generated, generated) {
				t.Fatal("generated catalog is not self-consistent")
			}
		}
		if _, ok := detection.Region(); ok {
			_, _, _ = documentprofile.ReplaceCatalog(body, generated)
		}
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
