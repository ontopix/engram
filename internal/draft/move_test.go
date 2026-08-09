package draft

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ontopix/engram/internal/checker"
	"github.com/ontopix/engram/internal/documentprofile"
)

func TestMoveAcrossDirectoriesRewritesEverySupportedLinkAndCatalog(t *testing.T) {
	store := moveStore(t)
	sourceName := filepath.Join(store, "topics", "why-files.md")
	source := readFile(t, sourceName)
	source = bytes.Replace(source, []byte("---\n# Why files\n"), []byte("---\n# Why files\n\n[Derived](derived-state.md \"Keep title\") and ![Picture](picture.png?q=1#frag).\n"), 1)
	writeFile(t, sourceName, source, 0o640)
	writeFile(t, filepath.Join(store, "topics", "picture.png"), []byte("asset\n"), 0o644)

	inboundName := filepath.Join(store, "topics", "derived-state.md")
	inbound := readFile(t, inboundName)
	inbound = bytes.Replace(inbound, []byte("description: \"Indexes and caches are rebuildable projections; the files stay the truth.\"\n"),
		[]byte("description: \"Indexes and caches are rebuildable projections; the files stay the truth.\"\nrelated: '  [[topics/why-files|Why label]]  '\nlinks:\n  - \"[[topics/why-files|Nested label]]\"\n"), 1)
	inbound = append(inbound,
		[]byte("\n[[topics/why-files|Why files]] and [inline](why-files.md?q=1#frag \"Keep title\") and [encoded](why-files&#x2e;md?raw=&amp;#frag) and [reference][why].\n\n[why]: <why-files.md?q=2#two> 'Reference title'\n")...)
	writeFile(t, inboundName, inbound, 0o644)
	schemaName := filepath.Join(store, ".engram", "schemas", "note.md")
	writeFile(t, schemaName, append(readFile(t, schemaName), []byte("\nSee [[topics/why-files|Why]].\n")...), 0o644)
	rootMapName := filepath.Join(store, "README.md")
	writeFile(t, rootMapName, append(readFile(t, rootMapName), []byte("\nRead [why files](topics/why-files.md \"Root title\").\n")...), 0o644)

	result, err := Move(context.Background(), store, "topics/why-files.md", "archive/renamed.md", MoveOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.DryRun || !result.Changed || result.From != "topics/why-files.md" || result.To != "archive/renamed.md" {
		t.Fatalf("result = %#v", result)
	}
	if !reflect.DeepEqual(result.Paths, []string{".engram/schemas/note.md", "README.md", "archive/renamed.md", "topics/derived-state.md"}) {
		t.Fatalf("paths = %#v", result.Paths)
	}
	if !reflect.DeepEqual(result.Catalogs, []string{"archive/README.md", "topics/README.md"}) {
		t.Fatalf("catalogs = %#v", result.Catalogs)
	}
	if _, err := os.Lstat(sourceName); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("source still exists: %v", err)
	}
	destinationName := filepath.Join(store, "archive", "renamed.md")
	destination := string(readFile(t, destinationName))
	if !strings.Contains(destination, `[Derived](../topics/derived-state.md "Keep title")`) ||
		!strings.Contains(destination, `![Picture](../topics/picture.png?q=1#frag)`) {
		t.Fatalf("moved record links were not rewritten:\n%s", destination)
	}
	info, err := os.Stat(destinationName)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("destination mode = %o, want 640", info.Mode().Perm())
	}
	updatedInbound := string(readFile(t, inboundName))
	for _, wanted := range []string{
		`related: '  [[archive/renamed|Why label]]  '`,
		`  - "[[archive/renamed|Nested label]]"`,
		`[[archive/renamed|Why files]]`,
		`[inline](../archive/renamed.md?q=1#frag "Keep title")`,
		`[encoded](../archive/renamed.md?raw=&amp;#frag)`,
		`[why]: <../archive/renamed.md?q=2#two> 'Reference title'`,
	} {
		if !strings.Contains(updatedInbound, wanted) {
			t.Errorf("inbound record lacks %q:\n%s", wanted, updatedInbound)
		}
	}
	if strings.Contains(string(readFile(t, filepath.Join(store, "topics", "README.md"))), "why-files") {
		t.Fatal("source catalog retained moved record")
	}
	if !strings.Contains(string(readFile(t, filepath.Join(store, "archive", "README.md"))), "- [renamed](renamed.md)") {
		t.Fatal("destination catalog lacks moved record")
	}
	if !strings.Contains(string(readFile(t, schemaName)), "[[archive/renamed|Why]]") {
		t.Fatal("schema-body wikilink was not rewritten")
	}
	if !strings.Contains(string(readFile(t, rootMapName)), `[why files](archive/renamed.md "Root title")`) {
		t.Fatal("map-body Markdown link was not rewritten")
	}
	checked, err := checker.CheckFS(store)
	if err != nil {
		t.Fatal(err)
	}
	if checked.Validation.HasErrors() {
		t.Fatalf("move result is not conforming: %#v", checked.Validation.Findings)
	}
}

func TestMoveSameDirectoryDryRunAndCatalog(t *testing.T) {
	store := minimalStore(t)
	beforeSource := readFile(t, filepath.Join(store, "topics", "why-files.md"))
	beforeCatalog := readFile(t, filepath.Join(store, "topics", "README.md"))
	result, err := Move(context.Background(), store, "topics/why-files.md", "topics/renamed.md", MoveOptions{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if !result.DryRun || !reflect.DeepEqual(result.Catalogs, []string{"topics/README.md"}) || !reflect.DeepEqual(result.Paths, []string{}) {
		t.Fatalf("dry-run result = %#v", result)
	}
	if !bytes.Equal(readFile(t, filepath.Join(store, "topics", "why-files.md")), beforeSource) ||
		!bytes.Equal(readFile(t, filepath.Join(store, "topics", "README.md")), beforeCatalog) {
		t.Fatal("dry-run changed the store")
	}
	if _, err := Move(context.Background(), store, "topics/why-files.md", "topics/renamed.md", MoveOptions{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(readFile(t, filepath.Join(store, "topics", "README.md"))), "- [renamed](renamed.md)") {
		t.Fatal("same-directory catalog was not regenerated")
	}
}

func TestMoveDoesNotReportUnchangedDirsOrNoneCatalogs(t *testing.T) {
	for _, mode := range []string{"dirs", "none"} {
		t.Run(mode, func(t *testing.T) {
			store := moveStore(t)
			for _, logical := range []string{"topics/README.md", "archive/README.md"} {
				name := filepath.Join(store, filepath.FromSlash(logical))
				source := readFile(t, name)
				closing := bytes.Index(source[4:], []byte("---\n"))
				if closing < 0 {
					t.Fatal("README fixture has no closing delimiter")
				}
				closing += 4
				source = append(append(append([]byte(nil), source[:closing]...), []byte("catalog: "+mode+"\n")...), source[closing:]...)
				if mode == "none" {
					document, err := documentprofile.Parse(source)
					if err != nil {
						t.Fatal(err)
					}
					region, ok := documentprofile.DetectCatalog(document.BodyBytes()).Region()
					if !ok {
						t.Fatal("README fixture has no catalog region")
					}
					start := document.Body.Start + region.Span.Start
					end := document.Body.Start + region.Span.End
					source = append(append([]byte(nil), source[:start]...), source[end:]...)
				}
				writeFile(t, name, source, 0o644)
			}
			if mode == "dirs" {
				if _, err := Fmt(context.Background(), store, FmtOptions{Paths: []string{"topics", "archive"}}); err != nil {
					t.Fatal(err)
				}
			}
			beforeTopics := readFile(t, filepath.Join(store, "topics", "README.md"))
			beforeArchive := readFile(t, filepath.Join(store, "archive", "README.md"))
			result, err := Move(context.Background(), store, "topics/why-files.md", "archive/moved.md", MoveOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Catalogs) != 0 {
				t.Fatalf("catalogs = %#v, want empty", result.Catalogs)
			}
			if !bytes.Equal(readFile(t, filepath.Join(store, "topics", "README.md")), beforeTopics) ||
				!bytes.Equal(readFile(t, filepath.Join(store, "archive", "README.md")), beforeArchive) {
				t.Fatal("move changed a dirs/none catalog")
			}
		})
	}
}

func TestMoveRejectsCollisionsAndAmbiguousDestinationWithoutWrites(t *testing.T) {
	store := minimalStore(t)
	before := snapshotDraftFiles(t, store)
	for _, test := range []struct {
		from string
		to   string
		kind ErrorKind
	}{
		{"topics/missing.md", "topics/new.md", ErrorRepository},
		{"topics/why-files.md", "topics/derived-state.md", ErrorConflict},
		{"topics/why-files.md", "missing/new.md", ErrorRepository},
		{"topics/why-files.md", "topics/why-files.md", ErrorUsage},
		{"topics/why-files.md", "topics/*.md", ErrorUsage},
	} {
		_, err := Move(context.Background(), store, test.from, test.to, MoveOptions{})
		if KindOf(err) != test.kind {
			t.Errorf("Move(%q, %q) error = %v, kind %q", test.from, test.to, err, KindOf(err))
		}
	}
	assertDraftFiles(t, store, before)

	name := filepath.Join(store, "topics", "derived-state.md")
	content := append(readFile(t, name), []byte("\n[encoded](why-files.md&#x3f;q=1)\n")...)
	writeFile(t, name, content, 0o644)
	ambiguousBefore := snapshotDraftFiles(t, store)
	_, err := Move(context.Background(), store, "topics/why-files.md", "topics/moved.md", MoveOptions{})
	if KindOf(err) != ErrorConflict {
		t.Fatalf("encoded destination error = %v, kind %q", err, KindOf(err))
	}
	assertDraftFiles(t, store, ambiguousBefore)
}

func TestMoveDetectsConcurrencyAndRollsBackPublishedPrefix(t *testing.T) {
	store := minimalStore(t)
	plan, _, err := PlanMove(context.Background(), store, "topics/why-files.md", "topics/moved.md", MoveOptions{})
	if err != nil {
		t.Fatal(err)
	}
	unrelated := filepath.Join(store, "topics", "derived-state.md")
	writeFile(t, unrelated, append(readFile(t, unrelated), []byte("\nconcurrent\n")...), 0o644)
	if err := plan.Publish(context.Background()); KindOf(err) != ErrorConcurrency {
		t.Fatalf("concurrent publish error = %v, kind %q", err, KindOf(err))
	}
	if _, err := os.Lstat(filepath.Join(store, "topics", "moved.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("concurrent plan created destination: %v", err)
	}

	rollbackStore := moveStore(t)
	backlink := filepath.Join(rollbackStore, "topics", "derived-state.md")
	writeFile(t, backlink, append(readFile(t, backlink), []byte("\n[[topics/why-files]]\n")...), 0o644)
	before := snapshotDraftFiles(t, rollbackStore)
	rollbackPlan, _, err := PlanMove(context.Background(), rollbackStore, "topics/why-files.md", "archive/moved.md", MoveOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// Append a package-private sentinel after the production move edits. Its
	// injected failure occurs after the source deletion and therefore proves
	// that rollback restores the deleted preimage as well as prior writes.
	rollbackPlan.captures["sentinel.md"] = observation{path: "sentinel.md", kind: observationAbsent}
	rollbackPlan.files = append(rollbackPlan.files, fileEdit{
		path: "sentinel.md", before: observation{path: "sentinel.md", kind: observationAbsent},
		after: []byte("never published\n"), mode: 0o644, staging: ".",
	})
	rollbackPlan.beforeApply = func(_ int, logicalPath string) error {
		if logicalPath == "sentinel.md" {
			return errors.New("injected publication failure")
		}
		return nil
	}
	if err := rollbackPlan.Publish(context.Background()); KindOf(err) != ErrorIO {
		t.Fatalf("rollback publish error = %v, kind %q", err, KindOf(err))
	}
	assertDraftFiles(t, rollbackStore, before)
}

func moveStore(t *testing.T) string {
	t.Helper()
	store := minimalStore(t)
	if err := os.Mkdir(filepath.Join(store, "archive"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(store, "archive", "README.md"), []byte("---\ndescription: \"Archived records.\"\n---\n# archive\n\n<!-- engram:catalog -->\n<!-- /engram:catalog -->\n"), 0o644)
	if _, err := Fmt(context.Background(), store, FmtOptions{}); err != nil {
		t.Fatal(err)
	}
	return store
}

func snapshotDraftFiles(t *testing.T, root string) map[string][]byte {
	t.Helper()
	result := make(map[string][]byte)
	err := filepath.WalkDir(root, func(name string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type().IsRegular() {
			relative, err := filepath.Rel(root, name)
			if err != nil {
				return err
			}
			result[filepath.ToSlash(relative)] = readFile(t, name)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func assertDraftFiles(t *testing.T, root string, expected map[string][]byte) {
	t.Helper()
	actual := snapshotDraftFiles(t, root)
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("store files differ after failed move\nactual: %#v\nexpected: %#v", actual, expected)
	}
}
