package draft

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/ontopix/engram/internal/checker"
	"github.com/ontopix/engram/internal/documentprofile"
	"github.com/ontopix/engram/schemas"
)

func TestFmtSelectionDryRunCheckAndPublication(t *testing.T) {
	store := minimalStore(t)
	readmeName := filepath.Join(store, "topics", "README.md")
	original := readFile(t, readmeName)
	stale := bytes.Replace(original,
		[]byte("- [why-files](why-files.md) — Why this store is plain markdown files instead of a database.\n"),
		[]byte("- stale bytes that are machine owned\n"), 1)
	writeFile(t, readmeName, stale, 0o640)

	dryResult, err := Fmt(context.Background(), store, FmtOptions{
		Paths: []string{"topics", "topics/README.md", "topics"}, DryRun: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !dryResult.DryRun || dryResult.Check || !dryResult.Changed || !reflect.DeepEqual(dryResult.Paths, []string{"topics/README.md"}) {
		t.Fatalf("dry result = %#v", dryResult)
	}
	if got := readFile(t, readmeName); !bytes.Equal(got, stale) {
		t.Fatal("dry-run changed README bytes")
	}

	checkResult, err := Fmt(context.Background(), store, FmtOptions{Paths: []string{"topics/README.md"}, Check: true})
	if err != nil {
		t.Fatal(err)
	}
	if !checkResult.Check || checkResult.DryRun || !checkResult.Changed {
		t.Fatalf("check result = %#v", checkResult)
	}
	if got := readFile(t, readmeName); !bytes.Equal(got, stale) {
		t.Fatal("check changed README bytes")
	}

	result, err := Fmt(context.Background(), store, FmtOptions{Paths: []string{"topics"}})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed || !reflect.DeepEqual(result.Paths, []string{"topics/README.md"}) {
		t.Fatalf("result = %#v", result)
	}
	got := readFile(t, readmeName)
	if !bytes.Equal(got, original) {
		t.Fatal("fmt did not restore the exact generated catalog")
	}
	info, err := os.Stat(readmeName)
	if err != nil {
		t.Fatal(err)
	}
	if !equivalentPermissions(info.Mode(), 0o640) {
		t.Fatalf("mode = %o, want 640", info.Mode().Perm())
	}

	unchanged, err := Fmt(context.Background(), store, FmtOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.Changed || len(unchanged.Paths) != 0 {
		t.Fatalf("unchanged result = %#v", unchanged)
	}
}

func TestFmtPreservesEveryByteOutsideCatalogRegion(t *testing.T) {
	store := minimalStore(t)
	name := filepath.Join(store, "topics", "README.md")
	source := readFile(t, name)
	document, err := documentprofile.Parse(source)
	if err != nil {
		t.Fatal(err)
	}
	region, ok := documentprofile.DetectCatalog(document.BodyBytes()).Region()
	if !ok {
		t.Fatal("fixture has no catalog region")
	}
	absoluteStart := document.Body.Start + region.Span.Start
	absoluteEnd := document.Body.Start + region.Span.End
	prefix := append([]byte(nil), source[:absoluteStart]...)
	suffix := append([]byte(nil), source[absoluteEnd:]...)
	stale := append(append(append([]byte(nil), prefix...), []byte("<!-- engram:catalog -->\n- wrong\n<!-- /engram:catalog -->\n")...), suffix...)
	writeFile(t, name, stale, 0o644)

	if _, err := Fmt(context.Background(), store, FmtOptions{Paths: []string{"topics"}}); err != nil {
		t.Fatal(err)
	}
	updated := readFile(t, name)
	updatedDocument, err := documentprofile.Parse(updated)
	if err != nil {
		t.Fatal(err)
	}
	updatedRegion, ok := documentprofile.DetectCatalog(updatedDocument.BodyBytes()).Region()
	if !ok {
		t.Fatal("updated README has no catalog")
	}
	updatedStart := updatedDocument.Body.Start + updatedRegion.Span.Start
	updatedEnd := updatedDocument.Body.Start + updatedRegion.Span.End
	if !bytes.Equal(updated[:updatedStart], prefix) || !bytes.Equal(updated[updatedEnd:], suffix) {
		t.Fatal("fmt changed bytes outside the machine-owned region")
	}
}

func TestFmtCatalogNoneIsUnchangedAndMalformedMarkersAreRejected(t *testing.T) {
	store := minimalStore(t)
	name := filepath.Join(store, "topics", "README.md")
	source := readFile(t, name)
	document, err := documentprofile.Parse(source)
	if err != nil {
		t.Fatal(err)
	}
	region, _ := documentprofile.DetectCatalog(document.BodyBytes()).Region()
	start := document.Body.Start + region.Span.Start
	end := document.Body.Start + region.Span.End
	none := bytes.Replace(source, []byte("description: \"Standalone notes, one self-contained topic per record.\"\n"),
		[]byte("description: \"Standalone notes, one self-contained topic per record.\"\ncatalog: none\n"), 1)
	// Reparse because adding frontmatter bytes shifts the body span.
	noneDocument, err := documentprofile.Parse(none)
	if err != nil {
		t.Fatal(err)
	}
	noneRegion, _ := documentprofile.DetectCatalog(noneDocument.BodyBytes()).Region()
	start = noneDocument.Body.Start + noneRegion.Span.Start
	end = noneDocument.Body.Start + noneRegion.Span.End
	none = append(append([]byte(nil), none[:start]...), none[end:]...)
	writeFile(t, name, none, 0o644)
	result, err := Fmt(context.Background(), store, FmtOptions{Paths: []string{"topics"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Changed || !bytes.Equal(readFile(t, name), none) {
		t.Fatalf("catalog:none result = %#v", result)
	}

	malformedStore := minimalStore(t)
	malformedName := filepath.Join(malformedStore, "topics", "README.md")
	malformed := bytes.Replace(readFile(t, malformedName), []byte("<!-- /engram:catalog -->\n"), nil, 1)
	writeFile(t, malformedName, malformed, 0o644)
	_, err = Fmt(context.Background(), malformedStore, FmtOptions{Paths: []string{"topics"}})
	if KindOf(err) != ErrorConflict {
		t.Fatalf("malformed marker error = %v, kind %q", err, KindOf(err))
	}
}

func TestFmtRejectsNonLiteralSelections(t *testing.T) {
	store := minimalStore(t)
	for _, selection := range []string{"topics/*.md", "topics/why-files.md", "../topics", "/topics", "topics/"} {
		_, _, err := PlanFmt(context.Background(), store, FmtOptions{Paths: []string{selection}})
		if KindOf(err) != ErrorUsage {
			t.Errorf("selection %q error = %v, kind %q", selection, err, KindOf(err))
		}
	}
}

func TestNewDeterministicRecordAndCatalog(t *testing.T) {
	store := minimalStore(t)
	options := NewOptions{
		Description: "A newly captured topic.",
		Fields:      []byte("tags: [alpha, beta]\npinned: true\n"),
		Title:       "Fresh Topic",
	}
	result, err := New(context.Background(), store, "note", "topics/fresh-topic.md", options)
	if err != nil {
		t.Fatal(err)
	}
	if result.DryRun || !result.Changed || result.Record != "topics/fresh-topic.md" || !reflect.DeepEqual(result.Catalogs, []string{"topics/README.md"}) {
		t.Fatalf("result = %#v", result)
	}
	want := "---\n" +
		"type: note\n" +
		"description: \"A newly captured topic.\"\n" +
		"pinned: true\n" +
		"tags: [\"alpha\",\"beta\"]\n" +
		"---\n# Fresh Topic\n"
	if got := string(readFile(t, filepath.Join(store, "topics", "fresh-topic.md"))); got != want {
		t.Fatalf("record bytes:\n%s\nwant:\n%s", got, want)
	}
	catalog := string(readFile(t, filepath.Join(store, "topics", "README.md")))
	if !strings.Contains(catalog, "- [fresh-topic](fresh-topic.md) (pinned) — A newly captured topic.\n") {
		t.Fatalf("catalog does not contain generated entry:\n%s", catalog)
	}
	checked, err := checker.CheckFS(store)
	if err != nil {
		t.Fatal(err)
	}
	if checked.Validation.HasErrors() {
		t.Fatalf("published store findings = %#v", checked.Validation.Findings)
	}
}

func TestNewDryRunAndCollisionDoNotWrite(t *testing.T) {
	store := minimalStore(t)
	options := NewOptions{Description: "Dry-run topic.", DryRun: true}
	result, err := New(context.Background(), store, "note", "topics/dry-run.md", options)
	if err != nil {
		t.Fatal(err)
	}
	if !result.DryRun || !result.Changed {
		t.Fatalf("result = %#v", result)
	}
	if _, err := os.Lstat(filepath.Join(store, "topics", "dry-run.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dry-run destination stat = %v", err)
	}

	existing := filepath.Join(store, "topics", "why-files.md")
	before := readFile(t, existing)
	_, err = New(context.Background(), store, "note", "topics/why-files.md", NewOptions{Description: "Collision."})
	if KindOf(err) != ErrorConflict {
		t.Fatalf("collision error = %v, kind %q", err, KindOf(err))
	}
	if got := readFile(t, existing); !bytes.Equal(got, before) {
		t.Fatal("collision overwrote existing record")
	}
	_, err = New(context.Background(), store, "note", "topics/WHY-FILES.md", NewOptions{Description: "Case collision."})
	if KindOf(err) != ErrorConflict {
		t.Fatalf("case collision error = %v, kind %q", err, KindOf(err))
	}
}

func TestNewCopiesExplicitBodyBytesExactly(t *testing.T) {
	store := minimalStore(t)
	body := []byte("# Supplied body\n\nEvery supplied body byte remains exact.\n")
	_, err := New(context.Background(), store, "note", "topics/supplied.md", NewOptions{
		Description: "A record with a supplied body.", Body: body,
	})
	if err != nil {
		t.Fatal(err)
	}
	content := readFile(t, filepath.Join(store, "topics", "supplied.md"))
	document, err := documentprofile.Parse(content)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(document.BodyBytes(), body) {
		t.Fatalf("body = %q, want %q", document.BodyBytes(), body)
	}
}

func TestNewInDirsCatalogModeDoesNotRewriteCatalog(t *testing.T) {
	store := minimalStore(t)
	name := filepath.Join(store, "topics", "README.md")
	source := bytes.Replace(readFile(t, name),
		[]byte("description: \"Standalone notes, one self-contained topic per record.\"\n"),
		[]byte("description: \"Standalone notes, one self-contained topic per record.\"\ncatalog: dirs\n"), 1)
	writeFile(t, name, source, 0o644)
	if _, err := Fmt(context.Background(), store, FmtOptions{Paths: []string{"topics"}}); err != nil {
		t.Fatal(err)
	}
	before := readFile(t, name)
	result, err := New(context.Background(), store, "note", "topics/not-listed.md", NewOptions{Description: "Not listed in dirs mode."})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Catalogs) != 0 {
		t.Fatalf("catalogs = %#v, want []", result.Catalogs)
	}
	if got := readFile(t, name); !bytes.Equal(got, before) {
		t.Fatal("new rewrote a dirs-mode catalog for a direct record")
	}
}

func TestNewValidatesFieldsBodiesAndPaths(t *testing.T) {
	store := minimalStore(t)
	tests := []struct {
		name    string
		kind    ErrorKind
		path    string
		options NewOptions
	}{
		{"invalid path", ErrorUsage, "topics/README.md", NewOptions{Description: "Description."}},
		{"override type", ErrorUsage, "topics/a.md", NewOptions{Description: "Description.", Fields: []byte("type: note\n")}},
		{"reserved field", ErrorUsage, "topics/a.md", NewOptions{Description: "Description.", Fields: []byte("engram-owner: x\n")}},
		{"schema violation", ErrorUsage, "topics/a.md", NewOptions{Description: "Description.", Fields: []byte("pinned: nope\n")}},
		{"body and title", ErrorUsage, "topics/a.md", NewOptions{Description: "Description.", Body: []byte("# A\n"), Title: "A"}},
		{"empty explicit title", ErrorUsage, "topics/a.md", NewOptions{Description: "Description.", TitleProvided: true}},
		{"invalid body", ErrorUsage, "topics/a.md", NewOptions{Description: "Description.", Body: []byte("# A\r\n")}},
		{"missing parent", ErrorRepository, "missing/a.md", NewOptions{Description: "Description."}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := PlanNew(context.Background(), store, "note", test.path, test.options)
			if KindOf(err) != test.kind {
				t.Fatalf("error = %v, kind %q, want %q", err, KindOf(err), test.kind)
			}
		})
	}
}

func TestNewEmitsRequiredHeadingsAndRejectsInvalidExplicitBody(t *testing.T) {
	store := minimalStore(t)
	if _, err := SchemaCopy(context.Background(), store, "project", SchemaCopyOptions{}); err != nil {
		t.Fatal(err)
	}
	fields := []byte("name: launch\nstatus: active\npeople: []\n")
	result, err := New(context.Background(), store, "project", "topics/launch.md", NewOptions{
		Description: "Launch work and its current state.", Fields: fields, Title: "Launch",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Record != "topics/launch.md" {
		t.Fatalf("result = %#v", result)
	}
	content := string(readFile(t, filepath.Join(store, "topics", "launch.md")))
	if !strings.HasSuffix(content, "# Launch\n\n## Status\n\n## Decisions\n") {
		t.Fatalf("generated body does not contain exact required headings:\n%s", content)
	}

	_, _, err = PlanNew(context.Background(), store, "project", "topics/bad-body.md", NewOptions{
		Description: "Project with incomplete supplied body.", Fields: fields,
		Body: []byte("# Bad body\n\n## Status\n"),
	})
	if KindOf(err) != ErrorUsage || !strings.Contains(err.Error(), "Decisions") {
		t.Fatalf("missing heading error = %v", err)
	}
}

func TestNewChecksTypedLinksAgainstVisibleSchema(t *testing.T) {
	store := minimalStore(t)
	if _, err := SchemaCopy(context.Background(), store, "project", SchemaCopyOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := SchemaCopy(context.Background(), store, "person", SchemaCopyOptions{}); err != nil {
		t.Fatal(err)
	}
	_, _, err := PlanNew(context.Background(), store, "person", "topics/ada.md", NewOptions{
		Description: "Ada, linked to one missing project.",
		Fields:      []byte("name: Ada\nprojects: [\"[[topics/missing]]\"]\n"),
		Body:        []byte("# Ada\n\n## Facts\n"),
	})
	if KindOf(err) != ErrorConflict || !strings.Contains(err.Error(), "E401") {
		t.Fatalf("typed-link error = %v, kind %q", err, KindOf(err))
	}
}

func TestSchemaCopyRootAndNestedAreByteExact(t *testing.T) {
	store := minimalStore(t)
	dry, err := SchemaCopy(context.Background(), store, "fact", SchemaCopyOptions{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if !dry.DryRun || !dry.Changed || dry.Path != ".engram/schemas/fact.md" || dry.Schema.Source != "inventory" || dry.Schema.Path != nil {
		t.Fatalf("dry result = %#v", dry)
	}
	if _, err := os.Lstat(filepath.Join(store, ".engram", "schemas", "fact.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dry-run destination stat = %v", err)
	}

	result, err := SchemaCopy(context.Background(), store, "fact", SchemaCopyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.DryRun || result.Path != ".engram/schemas/fact.md" {
		t.Fatalf("result = %#v", result)
	}
	assertInventoryBytes(t, "fact", readFile(t, filepath.Join(store, ".engram", "schemas", "fact.md")))

	nested, err := SchemaCopy(context.Background(), store, "person", SchemaCopyOptions{Scope: "topics"})
	if err != nil {
		t.Fatal(err)
	}
	if nested.Path != "topics/.engram/schemas/person.md" {
		t.Fatalf("nested result = %#v", nested)
	}
	assertInventoryBytes(t, "person", readFile(t, filepath.Join(store, "topics", ".engram", "schemas", "person.md")))
	checked, err := checker.CheckFS(store)
	if err != nil {
		t.Fatal(err)
	}
	if checked.Validation.HasErrors() {
		t.Fatalf("copied schemas made store invalid: %#v", checked.Validation.Findings)
	}
}

func TestSchemaCopyRejectsOverwriteAndBothShadowingDirections(t *testing.T) {
	store := minimalStore(t)
	_, err := SchemaCopy(context.Background(), store, "note", SchemaCopyOptions{})
	if KindOf(err) != ErrorConflict {
		t.Fatalf("overwrite error = %v, kind %q", err, KindOf(err))
	}
	_, err = SchemaCopy(context.Background(), store, "note", SchemaCopyOptions{Scope: "topics"})
	if KindOf(err) != ErrorConflict {
		t.Fatalf("ancestor-shadow error = %v, kind %q", err, KindOf(err))
	}
	if _, err := os.Lstat(filepath.Join(store, "topics", ".engram")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rejected copy created nested config: %v", err)
	}

	descendantStore := minimalStore(t)
	if _, err := SchemaCopy(context.Background(), descendantStore, "fact", SchemaCopyOptions{Scope: "topics"}); err != nil {
		t.Fatal(err)
	}
	_, err = SchemaCopy(context.Background(), descendantStore, "fact", SchemaCopyOptions{})
	if KindOf(err) != ErrorConflict {
		t.Fatalf("descendant-shadow error = %v, kind %q", err, KindOf(err))
	}
	if _, err := os.Lstat(filepath.Join(descendantStore, ".engram", "schemas", "fact.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rejected root copy created destination: %v", err)
	}
}

func TestSchemaCopyRejectsUnknownTypeAndInvalidScope(t *testing.T) {
	store := minimalStore(t)
	for _, test := range []struct {
		typeName string
		scope    string
		kind     ErrorKind
	}{
		{"unknown", "", ErrorUsage},
		{"fact", "missing", ErrorRepository},
		{"fact", "../topics", ErrorUsage},
	} {
		_, _, err := PlanSchemaCopy(context.Background(), store, test.typeName, SchemaCopyOptions{Scope: test.scope})
		if KindOf(err) != test.kind {
			t.Errorf("type %q scope %q error = %v, kind %q", test.typeName, test.scope, err, KindOf(err))
		}
	}
	_, _, err := PlanSchemaCopy(context.Background(), store, "fact", SchemaCopyOptions{ScopeProvided: true})
	if KindOf(err) != ErrorUsage {
		t.Fatalf("explicit empty scope error = %v, kind %q", err, KindOf(err))
	}
}

func TestSchemaCopyMayResolveAnExistingInvalidDraftRecord(t *testing.T) {
	store := minimalStore(t)
	recordName := filepath.Join(store, "topics", "unresolved-fact.md")
	writeFile(t, recordName, []byte("---\ntype: fact\ndescription: \"A draft fact that still lacks required domain fields.\"\n---\n# Draft fact\n"), 0o644)
	if _, err := Fmt(context.Background(), store, FmtOptions{Paths: []string{"topics"}}); err != nil {
		t.Fatal(err)
	}
	before, err := checker.CheckFS(store)
	if err != nil {
		t.Fatal(err)
	}
	if !hasFinding(before, "E203", "topics/unresolved-fact.md") {
		t.Fatalf("fixture does not begin unresolved: %#v", before.Validation.Findings)
	}
	if _, err := SchemaCopy(context.Background(), store, "fact", SchemaCopyOptions{}); err != nil {
		t.Fatal(err)
	}
	after, err := checker.CheckFS(store)
	if err != nil {
		t.Fatal(err)
	}
	if !hasFinding(after, "E301", "topics/unresolved-fact.md") {
		t.Fatalf("copy should publish the schema without pretending to repair the draft record: %#v", after.Validation.Findings)
	}
}

func TestPublishDetectsConcurrentCapturedChanges(t *testing.T) {
	store := minimalStore(t)
	readmeName := filepath.Join(store, "topics", "README.md")
	stale := bytes.Replace(readFile(t, readmeName), []byte("- [why-files]"), []byte("- [stale]"), 1)
	writeFile(t, readmeName, stale, 0o644)
	plan, _, err := PlanFmt(context.Background(), store, FmtOptions{Paths: []string{"topics"}})
	if err != nil {
		t.Fatal(err)
	}
	concurrent := append(append([]byte(nil), stale...), []byte("Concurrent prose.\n")...)
	writeFile(t, readmeName, concurrent, 0o644)
	err = plan.Publish(context.Background())
	if KindOf(err) != ErrorConcurrency || !errors.Is(err, ErrConcurrent) {
		t.Fatalf("publish error = %v, kind %q", err, KindOf(err))
	}
	if got := readFile(t, readmeName); !bytes.Equal(got, concurrent) {
		t.Fatal("concurrency failure overwrote concurrent bytes")
	}
}

func TestNewConcurrentCatalogChangePublishesNothing(t *testing.T) {
	store := minimalStore(t)
	readmeName := filepath.Join(store, "topics", "README.md")
	plan, _, err := PlanNew(context.Background(), store, "note", "topics/concurrent.md", NewOptions{Description: "Concurrent plan."})
	if err != nil {
		t.Fatal(err)
	}
	concurrent := append(readFile(t, readmeName), []byte("Concurrent prose.\n")...)
	writeFile(t, readmeName, concurrent, 0o644)
	err = plan.Publish(context.Background())
	if KindOf(err) != ErrorConcurrency {
		t.Fatalf("publish error = %v, kind %q", err, KindOf(err))
	}
	if _, err := os.Lstat(filepath.Join(store, "topics", "concurrent.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("concurrent publication created record: %v", err)
	}
	if got := readFile(t, readmeName); !bytes.Equal(got, concurrent) {
		t.Fatal("concurrent catalog bytes were overwritten")
	}
}

func TestNewPreservesUnrelatedConcurrentBytes(t *testing.T) {
	store := minimalStore(t)
	plan, _, err := PlanNew(context.Background(), store, "note", "topics/preserved.md", NewOptions{Description: "Unrelated bytes stay untouched."})
	if err != nil {
		t.Fatal(err)
	}
	rootReadme := filepath.Join(store, "README.md")
	concurrent := append(readFile(t, rootReadme), []byte("Concurrent unrelated prose.\n")...)
	writeFile(t, rootReadme, concurrent, 0o640)
	if err := plan.Publish(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, rootReadme); !bytes.Equal(got, concurrent) {
		t.Fatal("publication changed unrelated concurrent bytes")
	}
	if _, err := os.Stat(filepath.Join(store, "topics", "preserved.md")); err != nil {
		t.Fatal(err)
	}
}

func TestNewDetectsConcurrentVisibleSchemaChange(t *testing.T) {
	store := minimalStore(t)
	plan, _, err := PlanNew(context.Background(), store, "note", "topics/schema-race.md", NewOptions{Description: "Schema race."})
	if err != nil {
		t.Fatal(err)
	}
	schemaName := filepath.Join(store, ".engram", "schemas", "note.md")
	concurrent := append(readFile(t, schemaName), []byte("\nConcurrent schema documentation.\n")...)
	writeFile(t, schemaName, concurrent, 0o644)
	err = plan.Publish(context.Background())
	if KindOf(err) != ErrorConcurrency {
		t.Fatalf("publish error = %v, kind %q", err, KindOf(err))
	}
	if _, err := os.Lstat(filepath.Join(store, "topics", "schema-race.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("schema race created record: %v", err)
	}
}

func TestNewRollsBackRecordWhenCatalogPublicationFails(t *testing.T) {
	store := minimalStore(t)
	readmeName := filepath.Join(store, "topics", "README.md")
	original := readFile(t, readmeName)
	plan, _, err := PlanNew(context.Background(), store, "note", "topics/rollback.md", NewOptions{Description: "Rollback test."})
	if err != nil {
		t.Fatal(err)
	}
	plan.beforeApply = func(index int, logicalPath string) error {
		if index == 1 && logicalPath == "topics/README.md" {
			return errors.New("injected catalog failure")
		}
		return nil
	}
	err = plan.Publish(context.Background())
	mutation, present := MutationOf(err)
	if KindOf(err) != ErrorIO || errors.Is(err, ErrRecoveryRequired) || !present || !mutation.Durable || !mutation.CheckoutChanged || mutation.RecoveryRequired {
		t.Fatalf("publish error = %v, kind %q, mutation = %#v, present = %t", err, KindOf(err), mutation, present)
	}
	if _, err := os.Lstat(filepath.Join(store, "topics", "rollback.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rolled-back record stat = %v", err)
	}
	if got := readFile(t, readmeName); !bytes.Equal(got, original) {
		t.Fatal("catalog changed despite rollback")
	}
}

func TestFmtRollsBackEarlierCatalogWhenLaterPublicationFails(t *testing.T) {
	store := minimalStore(t)
	rootReadme := filepath.Join(store, "README.md")
	topicsReadme := filepath.Join(store, "topics", "README.md")
	rootStale := bytes.Replace(readFile(t, rootReadme), []byte("- [topics/]"), []byte("- [wrong/]"), 1)
	topicsStale := bytes.Replace(readFile(t, topicsReadme), []byte("- [why-files]"), []byte("- [wrong]"), 1)
	writeFile(t, rootReadme, rootStale, 0o644)
	writeFile(t, topicsReadme, topicsStale, 0o644)
	plan, result, err := PlanFmt(context.Background(), store, FmtOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.Paths, []string{"README.md", "topics/README.md"}) {
		t.Fatalf("paths = %#v", result.Paths)
	}
	plan.beforeApply = func(index int, _ string) error {
		if index == 1 {
			return errors.New("injected second-file failure")
		}
		return nil
	}
	if err := plan.Publish(context.Background()); KindOf(err) != ErrorIO {
		t.Fatalf("publish error = %v, kind %q", err, KindOf(err))
	} else if mutation, present := MutationOf(err); !present || !mutation.Durable || !mutation.CheckoutChanged || mutation.RecoveryRequired {
		t.Fatalf("publish mutation = %#v, present = %t", mutation, present)
	}
	if !bytes.Equal(readFile(t, rootReadme), rootStale) || !bytes.Equal(readFile(t, topicsReadme), topicsStale) {
		t.Fatal("multi-catalog failure did not restore every preimage")
	}
}

func TestSchemaCopyRollsBackCreatedDirectoriesOnFailure(t *testing.T) {
	store := minimalStore(t)
	plan, _, err := PlanSchemaCopy(context.Background(), store, "fact", SchemaCopyOptions{Scope: "topics"})
	if err != nil {
		t.Fatal(err)
	}
	plan.beforeApply = func(index int, logicalPath string) error {
		if logicalPath == "topics/.engram/schemas/fact.md" {
			return errors.New("injected schema publication failure")
		}
		return nil
	}
	if err := plan.Publish(context.Background()); KindOf(err) != ErrorIO {
		t.Fatalf("publish error = %v, kind %q", err, KindOf(err))
	} else if mutation, present := MutationOf(err); !present || !mutation.Durable || !mutation.CheckoutChanged || mutation.RecoveryRequired {
		t.Fatalf("publish mutation = %#v, present = %t", mutation, present)
	}
	if _, err := os.Lstat(filepath.Join(store, "topics", ".engram")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rollback left configuration directory: %v", err)
	}
}

func TestCancellationDuringPublicationRollsBack(t *testing.T) {
	store := minimalStore(t)
	readmeName := filepath.Join(store, "topics", "README.md")
	original := readFile(t, readmeName)
	plan, _, err := PlanNew(context.Background(), store, "note", "topics/cancelled.md", NewOptions{Description: "Cancelled publication."})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	plan.beforeApply = func(index int, _ string) error {
		if index == 0 {
			cancel()
		}
		return nil
	}
	err = plan.Publish(ctx)
	mutation, present := MutationOf(err)
	if KindOf(err) != ErrorCancelled || !present || !mutation.Durable || !mutation.CheckoutChanged || mutation.RecoveryRequired {
		t.Fatalf("publish error = %v, kind %q, mutation = %#v, present = %t", err, KindOf(err), mutation, present)
	}
	if _, err := os.Lstat(filepath.Join(store, "topics", "cancelled.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cancelled publication left record: %v", err)
	}
	if !bytes.Equal(readFile(t, readmeName), original) {
		t.Fatal("cancelled publication changed catalog")
	}
}

func TestRendezvousAndCancellation(t *testing.T) {
	store := minimalStore(t)
	name := filepath.Join(store, "topics", "README.md")
	stale := bytes.Replace(readFile(t, name), []byte("- [why-files]"), []byte("- [stale]"), 1)
	writeFile(t, name, stale, 0o644)
	locker := &recordingLocker{}
	result, err := Fmt(context.Background(), store, FmtOptions{Paths: []string{"topics"}, Rendezvous: locker})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed || !locker.locked || !locker.unlocked {
		t.Fatalf("result = %#v, locker = %#v", result, locker)
	}

	cancelledContext, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err = PlanFmt(cancelledContext, store, FmtOptions{})
	if KindOf(err) != ErrorCancelled {
		t.Fatalf("cancelled plan error = %v, kind %q", err, KindOf(err))
	}
}

func TestRendezvousFailuresAreTypedAndDoNotWrite(t *testing.T) {
	store := minimalStore(t)
	name := filepath.Join(store, "topics", "README.md")
	stale := bytes.Replace(readFile(t, name), []byte("- [why-files]"), []byte("- [stale]"), 1)
	writeFile(t, name, stale, 0o644)
	plan, _, err := PlanFmt(context.Background(), store, FmtOptions{Paths: []string{"topics"}})
	if err != nil {
		t.Fatal(err)
	}
	err = plan.PublishWith(context.Background(), failingLocker{lockErr: errors.New("busy")})
	if KindOf(err) != ErrorConcurrency || !bytes.Equal(readFile(t, name), stale) {
		t.Fatalf("busy rendezvous error = %v, kind %q", err, KindOf(err))
	}

	plan, _, err = PlanFmt(context.Background(), store, FmtOptions{Paths: []string{"topics"}})
	if err != nil {
		t.Fatal(err)
	}
	err = plan.PublishWith(context.Background(), failingLocker{unlockErr: errors.New("stuck lock")})
	mutation, present := MutationOf(err)
	if KindOf(err) != ErrorConflict || !errors.Is(err, ErrRecoveryRequired) || !present || !mutation.Durable || !mutation.CheckoutChanged || !mutation.RecoveryRequired {
		t.Fatalf("unlock error = %v, kind %q", err, KindOf(err))
	}
}

func TestDraftRollbackAndCleanupFailuresCarryExactEffects(t *testing.T) {
	t.Run("durable published image survives failed rollback", func(t *testing.T) {
		store := minimalStore(t)
		name := filepath.Join(store, "topics", "README.md")
		stale := bytes.Replace(readFile(t, name), []byte("- [why-files]"), []byte("- [stale]"), 1)
		writeFile(t, name, stale, 0o644)
		plan, _, err := PlanFmt(context.Background(), store, FmtOptions{Paths: []string{"topics"}})
		if err != nil {
			t.Fatal(err)
		}
		appliedErr := errors.New("fault after durable apply")
		rollbackErr := errors.New("fault before rollback")
		plan.fault = func(phase Phase, _ int, _ string) error {
			switch phase {
			case PhaseApplied:
				return appliedErr
			case PhaseRollback:
				return rollbackErr
			default:
				return nil
			}
		}
		err = plan.Publish(context.Background())
		mutation, present := MutationOf(err)
		if !errors.Is(err, appliedErr) || !errors.Is(err, rollbackErr) || !present || !mutation.Durable || !mutation.CheckoutChanged || mutation.RecoveryRequired {
			t.Fatalf("error = %v, mutation = %#v, present = %t", err, mutation, present)
		}
		if bytes.Equal(readFile(t, name), stale) {
			t.Fatal("failed rollback unexpectedly restored the preimage")
		}
	})

	t.Run("temporary cleanup fails before publication", func(t *testing.T) {
		store := minimalStore(t)
		plan, _, err := PlanNew(context.Background(), store, "note", "topics/cleanup-fault.md", NewOptions{Description: "Cleanup fault."})
		if err != nil {
			t.Fatal(err)
		}
		applyErr := errors.New("fault before first apply")
		cleanupErr := errors.New("fault cleaning temporary")
		plan.beforeApply = func(int, string) error { return applyErr }
		plan.fault = func(phase Phase, _ int, _ string) error {
			if phase == PhaseCleanup {
				return cleanupErr
			}
			return nil
		}
		err = plan.Publish(context.Background())
		mutation, present := MutationOf(err)
		if !errors.Is(err, applyErr) || !errors.Is(err, cleanupErr) || !present || mutation.Durable || !mutation.CheckoutChanged || mutation.RecoveryRequired {
			t.Fatalf("error = %v, mutation = %#v, present = %t", err, mutation, present)
		}
		matches, globErr := filepath.Glob(filepath.Join(store, "topics", ".engram-draft-*"))
		if globErr != nil || len(matches) == 0 {
			t.Fatalf("residual temporaries = %v, %v", matches, globErr)
		}
	})
}

func TestDraftFsyncFailureReportsOnlyResidualCheckoutEffects(t *testing.T) {
	store := minimalStore(t)
	name := filepath.Join(store, "topics", "README.md")
	stale := bytes.Replace(readFile(t, name), []byte("- [why-files]"), []byte("- [stale]"), 1)
	writeFile(t, name, stale, 0o644)
	plan, _, err := PlanFmt(context.Background(), store, FmtOptions{Paths: []string{"topics"}})
	if err != nil {
		t.Fatal(err)
	}
	syncErr := errors.New("injected publication fsync failure")
	rollbackErr := errors.New("injected rollback failure")
	calls := 0
	plan.syncDirectory = func(root *os.Root, directory string) (bool, error) {
		calls++
		if calls == 1 {
			return false, syncErr
		}
		return syncDirectory(root, directory)
	}
	plan.fault = func(phase Phase, _ int, _ string) error {
		if phase == PhaseRollback {
			return rollbackErr
		}
		return nil
	}
	err = plan.Publish(context.Background())
	mutation, present := MutationOf(err)
	if !errors.Is(err, syncErr) || !errors.Is(err, rollbackErr) || !present || mutation.Durable || !mutation.CheckoutChanged || mutation.RecoveryRequired {
		t.Fatalf("error = %v, mutation = %#v, present = %t", err, mutation, present)
	}
	if bytes.Equal(readFile(t, name), stale) {
		t.Fatal("failed rollback unexpectedly restored the preimage")
	}
}

func TestDraftDirectorySyncTailPreservesDurableEvidence(t *testing.T) {
	store := minimalStore(t)
	name := filepath.Join(store, "topics", "README.md")
	stale := bytes.Replace(readFile(t, name), []byte("- [why-files]"), []byte("- [stale]"), 1)
	writeFile(t, name, stale, 0o644)
	plan, _, err := PlanFmt(context.Background(), store, FmtOptions{Paths: []string{"topics"}})
	if err != nil {
		t.Fatal(err)
	}
	injected := errors.New("injected close tail after directory sync")
	calls := 0
	plan.syncDirectory = func(root *os.Root, directory string) (bool, error) {
		calls++
		durable, syncErr := syncDirectory(root, directory)
		if calls == 1 {
			return durable, errors.Join(syncErr, injected)
		}
		return durable, syncErr
	}
	err = plan.Publish(context.Background())
	mutation, present := MutationOf(err)
	if !errors.Is(err, injected) || !present || !mutation.Durable || !mutation.CheckoutChanged || mutation.RecoveryRequired {
		t.Fatalf("error = %v, mutation = %#v, present = %t", err, mutation, present)
	}
	if !bytes.Equal(readFile(t, name), stale) {
		t.Fatal("sync-tail rollback did not restore the preimage")
	}
}

func TestDraftUnlockUsesReportedResidualState(t *testing.T) {
	store := minimalStore(t)
	name := filepath.Join(store, "topics", "README.md")
	stale := bytes.Replace(readFile(t, name), []byte("- [why-files]"), []byte("- [stale]"), 1)
	writeFile(t, name, stale, 0o644)
	plan, _, err := PlanFmt(context.Background(), store, FmtOptions{Paths: []string{"topics"}})
	if err != nil {
		t.Fatal(err)
	}
	injected := errors.New("release failed after removing lock")
	err = plan.PublishWith(context.Background(), reportedUnlockLocker{err: &Error{
		Kind: ErrorConflict, Operation: "release", Err: injected,
		Mutation: &Mutation{Durable: true, RecoveryRequired: false},
	}})
	mutation, present := MutationOf(err)
	if !errors.Is(err, injected) || !present || !mutation.Durable || !mutation.CheckoutChanged || mutation.RecoveryRequired || errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("error = %v, mutation = %#v, present = %t", err, mutation, present)
	}
}

func TestDraftMutationOfUsesFinalRecoverySnapshot(t *testing.T) {
	first := mutationError(ErrorConflict, "first", errors.New("first"), Mutation{CheckoutChanged: true, RecoveryRequired: true})
	last := mutationError(ErrorIO, "last", errors.New("last"), Mutation{Durable: true})
	mutation, present := MutationOf(errors.Join(first, last))
	if !present || !mutation.Durable || !mutation.CheckoutChanged || mutation.RecoveryRequired {
		t.Fatalf("joined mutation = %#v, present = %t", mutation, present)
	}
	outer := mutationError(ErrorConflict, "outer", first, Mutation{})
	mutation, present = MutationOf(outer)
	if !present || mutation.Durable || !mutation.CheckoutChanged || mutation.RecoveryRequired {
		t.Fatalf("outer mutation = %#v, present = %t", mutation, present)
	}
}

func TestEveryDraftHelperFailsClosedWhenUnlockFails(t *testing.T) {
	injected := errors.New("residual rendezvous")
	tests := []struct {
		name string
		run  func(*testing.T) error
	}{
		{name: "fmt", run: func(t *testing.T) error {
			store := minimalStore(t)
			name := filepath.Join(store, "topics", "README.md")
			writeFile(t, name, bytes.Replace(readFile(t, name), []byte("- [why-files]"), []byte("- [stale]"), 1), 0o644)
			_, err := Fmt(context.Background(), store, FmtOptions{Paths: []string{"topics"}, Rendezvous: failingLocker{unlockErr: injected}})
			return err
		}},
		{name: "new", run: func(t *testing.T) error {
			_, err := New(context.Background(), minimalStore(t), "note", "topics/unlock.md", NewOptions{Description: "Unlock failure.", Rendezvous: failingLocker{unlockErr: injected}})
			return err
		}},
		{name: "mv", run: func(t *testing.T) error {
			_, err := Move(context.Background(), minimalStore(t), "topics/why-files.md", "topics/unlock-moved.md", MoveOptions{Rendezvous: failingLocker{unlockErr: injected}})
			return err
		}},
		{name: "schema.copy", run: func(t *testing.T) error {
			_, err := SchemaCopy(context.Background(), minimalStore(t), "person", SchemaCopyOptions{Rendezvous: failingLocker{unlockErr: injected}})
			return err
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.run(t)
			mutation, present := MutationOf(err)
			if !errors.Is(err, injected) || !present || !mutation.Durable || !mutation.CheckoutChanged || !mutation.RecoveryRequired {
				t.Fatalf("error = %v, mutation = %#v, present = %t", err, mutation, present)
			}
		})
	}
}

type recordingLocker struct {
	locked   bool
	unlocked bool
}

type failingLocker struct {
	lockErr   error
	unlockErr error
}

type reportedUnlockLocker struct{ err error }

func (l reportedUnlockLocker) LockDraft(context.Context, string) (Unlock, error) {
	return func() error { return l.err }, nil
}

func (l failingLocker) LockDraft(_ context.Context, _ string) (Unlock, error) {
	if l.lockErr != nil {
		return nil, l.lockErr
	}
	return func() error { return l.unlockErr }, nil
}

func (l *recordingLocker) LockDraft(_ context.Context, root string) (Unlock, error) {
	if root == "" || l.locked {
		return nil, errors.New("bad lock request")
	}
	l.locked = true
	return func() error {
		l.unlocked = true
		return nil
	}, nil
}

func minimalStore(t *testing.T) string {
	t.Helper()
	root := repositoryRoot(t)
	store := filepath.Join(t.TempDir(), "store")
	if err := os.CopyFS(store, os.DirFS(filepath.Join(root, "examples", "minimal"))); err != nil {
		t.Fatal(err)
	}
	return store
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(filename), "..", ".."))
}

func readFile(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func writeFile(t *testing.T, name string, data []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(name, data, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(name, mode); err != nil {
		t.Fatal(err)
	}
}

func assertInventoryBytes(t *testing.T, typeName string, got []byte) {
	t.Helper()
	entries, err := schemas.Inventory()
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Type == typeName {
			if !bytes.Equal(got, []byte(entry.Content)) {
				t.Fatalf("copied %s bytes differ from inventory", typeName)
			}
			return
		}
	}
	t.Fatalf("inventory has no %s", typeName)
}

func hasFinding(checked *checker.Snapshot, code, path string) bool {
	for _, finding := range checked.Validation.Findings {
		if finding.Code == code && finding.Path == path {
			return true
		}
	}
	return false
}
