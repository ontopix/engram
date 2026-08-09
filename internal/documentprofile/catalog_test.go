package documentprofile

import (
	"errors"
	"reflect"
	"testing"

	"github.com/ontopix/engram/internal/markdownprofile"
)

func TestEscapeCatalogTextSinglePass(t *testing.T) {
	t.Parallel()
	input := "&<>\\`*_[] café"
	want := "&amp;&lt;&gt;\\\\\\`\\*\\_\\[\\] café"
	if got := EscapeCatalogText(input); got != want {
		t.Fatalf("EscapeCatalogText = %q, want %q", got, want)
	}
	if got := EscapeCatalogText("&amp;"); got != "&amp;amp;" {
		t.Fatalf("escape replacement was rescanned or existing text mishandled: %q", got)
	}
}

func TestGenerateCatalogExactLayoutAndOrdering(t *testing.T) {
	t.Parallel()
	directories := []CatalogDirectory{
		{Name: "zeta", Description: "Z > A"},
		{Name: "alpha", Description: "A & B"},
	}
	records := []CatalogRecord{
		{Name: "a.md", Description: "Base"},
		{Name: "a-early.md", Description: "Hyphen sorts after its shorter raw stem", Pinned: true},
		{Name: "z.note.md", Description: "Use *care* and [links]"},
	}
	got, err := GenerateCatalog(CatalogAll, directories, records)
	if err != nil {
		t.Fatal(err)
	}
	want := "<!-- engram:catalog -->\n" +
		"- [alpha/](alpha/README.md) — A &amp; B\n" +
		"- [zeta/](zeta/README.md) — Z &gt; A\n" +
		"- [a](a.md) — Base\n" +
		"- [a-early](a-early.md) (pinned) — Hyphen sorts after its shorter raw stem\n" +
		"- [z.note](z.note.md) — Use \\*care\\* and \\[links\\]\n" +
		"<!-- /engram:catalog -->\n"
	if string(got) != want {
		t.Fatalf("catalog:\n%s\nwant:\n%s", got, want)
	}
}

func TestGenerateCatalogModes(t *testing.T) {
	t.Parallel()
	directories := []CatalogDirectory{{Name: "child", Description: "Child"}}
	records := []CatalogRecord{{Name: "record.md", Description: "Record"}}
	dirs, err := GenerateCatalog(CatalogDirs, directories, append(records, CatalogRecord{Name: "bad name.md", Description: ""}))
	if err != nil {
		t.Fatalf("dirs mode inspected excluded records: %v", err)
	}
	wantDirs := "<!-- engram:catalog -->\n- [child/](child/README.md) — Child\n<!-- /engram:catalog -->\n"
	if string(dirs) != wantDirs {
		t.Fatalf("dirs catalog = %q", dirs)
	}
	none, err := GenerateCatalog(CatalogNone, []CatalogDirectory{{Name: "bad name"}}, nil)
	if err != nil || none != nil {
		t.Fatalf("none catalog = %q, %v", none, err)
	}
	empty, err := GenerateCatalog(CatalogAll, nil, nil)
	if err != nil || string(empty) != "<!-- engram:catalog -->\n<!-- /engram:catalog -->\n" {
		t.Fatalf("empty catalog = %q, %v", empty, err)
	}
}

func TestGenerateCatalogRejectsUnavailableInputs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		mode        CatalogMode
		directories []CatalogDirectory
		records     []CatalogRecord
	}{
		{"unknown mode", "records", nil, nil},
		{"invalid directory", CatalogAll, []CatalogDirectory{{Name: "bad name", Description: "ok"}}, nil},
		{"duplicate directory", CatalogAll, []CatalogDirectory{{Name: "same", Description: "one"}, {Name: "same", Description: "two"}}, nil},
		{"bad directory description", CatalogAll, []CatalogDirectory{{Name: "child", Description: " bad"}}, nil},
		{"README is not a record", CatalogAll, nil, []CatalogRecord{{Name: "README.md", Description: "map"}}},
		{"missing record suffix", CatalogAll, nil, []CatalogRecord{{Name: "record", Description: "record"}}},
		{"duplicate record", CatalogAll, nil, []CatalogRecord{{Name: "same.md", Description: "one"}, {Name: "same.md", Description: "two"}}},
		{"bad record description", CatalogAll, nil, []CatalogRecord{{Name: "record.md", Description: "bad\nvalue"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := GenerateCatalog(test.mode, test.directories, test.records); err == nil {
				t.Fatal("GenerateCatalog unexpectedly succeeded")
			}
		})
	}
}

func TestDetectCatalogExactMarkersAndRegion(t *testing.T) {
	t.Parallel()
	body := []byte("prose\n <!-- engram:catalog -->\n<!-- engram:catalog --> trailing\n<!-- engram:catalog -->\nentry\n<!-- /engram:catalog -->\nafter\n")
	detection := DetectCatalog(body)
	if len(detection.Openings) != 1 || len(detection.Closings) != 1 {
		t.Fatalf("detection = %#v", detection)
	}
	region, ok := detection.Region()
	if !ok {
		t.Fatal("expected one region")
	}
	if got := string(body[region.Entries.Start:region.Entries.End]); got != "entry\n" {
		t.Fatalf("entries = %q", got)
	}
	if got := string(body[region.Span.Start:region.Span.End]); got != "<!-- engram:catalog -->\nentry\n<!-- /engram:catalog -->\n" {
		t.Fatalf("region = %q", got)
	}
	wantOpening := markdownprofile.Span{Start: 64, End: 88}
	if !reflect.DeepEqual(region.Opening, wantOpening) {
		t.Fatalf("opening = %#v, want %#v", region.Opening, wantOpening)
	}
}

func TestCatalogDetectionModeAndDuplicates(t *testing.T) {
	t.Parallel()
	tests := []struct {
		body      string
		allValid  bool
		noneValid bool
	}{
		{"prose\n", false, true},
		{"<!-- engram:catalog -->\n<!-- /engram:catalog -->\n", true, false},
		{"<!-- /engram:catalog -->\n<!-- engram:catalog -->\n", false, false},
		{"<!-- engram:catalog -->\n<!-- engram:catalog -->\n<!-- /engram:catalog -->\n", false, false},
		{"<!-- engram:catalog -->\n<!-- /engram:catalog -->\n<!-- /engram:catalog -->\n", false, false},
		{" <!-- engram:catalog -->\n", false, true},
	}
	for _, test := range tests {
		detection := DetectCatalog([]byte(test.body))
		if got := detection.ValidForMode(CatalogAll); got != test.allValid {
			t.Errorf("all ValidForMode(%q) = %v", test.body, got)
		}
		if got := detection.ValidForMode(CatalogNone); got != test.noneValid {
			t.Errorf("none ValidForMode(%q) = %v", test.body, got)
		}
	}
}

func TestRegionMatchesAndReplaceCatalogPreserveOtherBytes(t *testing.T) {
	t.Parallel()
	body := []byte("before\n<!-- engram:catalog -->\nstale\n<!-- /engram:catalog -->\nafter\n")
	generated := []byte("<!-- engram:catalog -->\nfresh\n<!-- /engram:catalog -->\n")
	detection := DetectCatalog(body)
	if detection.RegionMatches(body, generated) {
		t.Fatal("stale region unexpectedly matched")
	}
	replaced, changed, err := ReplaceCatalog(body, generated)
	if err != nil || !changed {
		t.Fatalf("ReplaceCatalog = changed %v, error %v", changed, err)
	}
	want := "before\n" + string(generated) + "after\n"
	if string(replaced) != want {
		t.Fatalf("replaced = %q, want %q", replaced, want)
	}
	if !DetectCatalog(replaced).RegionMatches(replaced, generated) {
		t.Fatal("replacement did not match generated region")
	}
	unchanged, changed, err := ReplaceCatalog(replaced, generated)
	if err != nil || changed || string(unchanged) != string(replaced) {
		t.Fatalf("idempotent replace = %q, %v, %v", unchanged, changed, err)
	}
	if &unchanged[0] == &replaced[0] {
		t.Fatal("unchanged result aliases the input")
	}
}

func TestReplaceCatalogDoesNotRepairMarkerGrammar(t *testing.T) {
	t.Parallel()
	generated := []byte("<!-- engram:catalog -->\n<!-- /engram:catalog -->\n")
	for _, body := range [][]byte{
		[]byte("no markers\n"),
		[]byte("<!-- engram:catalog -->\n"),
		[]byte("<!-- /engram:catalog -->\n<!-- engram:catalog -->\n"),
	} {
		if _, _, err := ReplaceCatalog(body, generated); !errors.Is(err, ErrCatalogMarkerShape) {
			t.Fatalf("ReplaceCatalog(%q) error = %v", body, err)
		}
	}
}
