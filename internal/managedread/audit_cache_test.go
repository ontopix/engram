package managedread

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/ontopix/engram/internal/checker"
	"github.com/ontopix/engram/internal/gitraw"
	"github.com/ontopix/engram/internal/markdownprofile"
	"github.com/ontopix/engram/internal/snapshot"
	"github.com/ontopix/engram/internal/yamlprofile"
)

func TestAuditAcceptedReusesOnlyExactTipAndRuleSet(t *testing.T) {
	root := newGitRepository(t, gitraw.SHA1, true)
	store := openStore(t, root)
	var loads atomic.Int32
	store.auditLoader = func(ctx context.Context, observed *Store) (*AcceptedAudit, error) {
		loads.Add(1)
		return observed.auditAccepted(ctx)
	}

	first, err := store.AuditAccepted(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.AuditAccepted(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if loads.Load() != 1 {
		t.Fatalf("lineage audit loads = %d, want 1", loads.Load())
	}
	if first == second || first.Raw == second.Raw || first.Snapshots[first.Tip] == second.Snapshots[second.Tip] {
		t.Fatal("cache returned mutable outer audit containers by identity")
	}

	// Presentation is intentionally outside the lineage cache. A same-tip Git
	// presentation change must therefore be visible without another raw audit.
	runGit(t, root, nil, "config", "core.autocrlf", "true")
	presented, err := store.AuditAccepted(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if loads.Load() != 1 || !hasFinding(presented.Validation.Findings, "E601", ".") {
		t.Fatalf("same-tip presentation audit loads/findings = %d / %#v", loads.Load(), presented.Validation.Findings)
	}
	runGit(t, root, nil, "config", "core.autocrlf", "false")

	modifyFile(t, filepath.Join(root, "topics", "why-files.md"), "\nA new accepted cache generation.\n")
	commitAll(t, root, "advance accepted tip")
	advanced, err := store.AuditAccepted(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if loads.Load() != 2 || advanced.Tip == first.Tip {
		t.Fatalf("tip invalidation loads/tips = %d / %q -> %q", loads.Load(), first.Tip, advanced.Tip)
	}

	store.ruleSetID = acceptedAuditRuleSetIdentity + ";test-generation"
	changedRules, err := store.AuditAccepted(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if loads.Load() != 3 || changedRules.Tip != advanced.Tip {
		t.Fatalf("rule-set invalidation loads/tips = %d / %q and %q", loads.Load(), advanced.Tip, changedRules.Tip)
	}
}

func TestAuditAcceptedCacheResultsDoNotAliasProtocolContainers(t *testing.T) {
	root := newGitRepository(t, gitraw.SHA1, true)
	store := openStore(t, root)
	var loads atomic.Int32
	store.auditLoader = func(ctx context.Context, observed *Store) (*AcceptedAudit, error) {
		loads.Add(1)
		return observed.auditAccepted(ctx)
	}

	first, err := store.AuditAccepted(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	wantTip := first.Tip
	wantAudits := len(first.Audits)
	wantRaw := len(first.Raw.Commits)
	wantSnapshots := len(first.Snapshots)
	snapshot := first.Snapshots[wantTip]
	const recordPath = "topics/why-files.md"
	record := snapshot.Records[recordPath]
	directoryMap := snapshot.Maps["README.md"]
	schema := snapshot.Schemas[".engram/schemas/note.md"]
	if record == nil || directoryMap == nil || schema == nil || record.Description == nil || directoryMap.Description == nil || len(record.Bytes) == 0 || len(record.Body) == 0 || len(record.Markdown.Headings) == 0 || len(directoryMap.Body) == 0 || len(schema.Documentation) == 0 || len(schema.Markdown.Headings) == 0 || schema.Validator == nil || schema.Version == nil {
		t.Fatalf("minimal cached snapshot lacks expected parsed data: %#v", snapshot)
	}
	recordDescription, _ := record.Frontmatter.Lookup("description")
	mapDescription, _ := directoryMap.Frontmatter.Lookup("description")
	properties, _ := schema.RawSchema["properties"].(map[string]any)
	if recordDescription == nil || mapDescription == nil || properties == nil {
		t.Fatal("minimal cached snapshot lacks expected nested metadata")
	}
	wantRecordBytes := append([]byte(nil), record.Bytes...)
	wantRecordBody := append([]byte(nil), record.Body...)
	wantRecordDescription := recordDescription.String
	wantDerivedRecordDescription := *record.Description
	wantRecordHeading := record.Markdown.Headings[0].Source
	wantMapBody := append([]byte(nil), directoryMap.Body...)
	wantMapDescription := mapDescription.String
	wantDerivedMapDescription := *directoryMap.Description
	wantMapHeading := directoryMap.Markdown.Headings[0].Source
	wantSchemaDocumentation := append([]byte(nil), schema.Documentation...)
	wantSchemaHeading := schema.Markdown.Headings[0].Source
	wantSchemaType := schema.RawSchema["type"]
	wantDescriptionSchema := cloneJSONValue(properties["description"])
	wantRequiredSections := append([]string(nil), schema.Body.RequiredSections...)
	wantTreeFile := append([]byte(nil), snapshot.Tree.Files[recordPath].Data...)

	first.Tip = "caller mutation"
	first.Validation.Findings = append(first.Validation.Findings, checker.Finding{Code: "E999", Path: "."})
	first.Audits = nil
	first.Raw.Commits = nil
	delete(first.Snapshots, wantTip)
	record.Bytes[0] ^= 0xff
	record.Body[0] ^= 0xff
	recordDescription.String = "caller-mutated record metadata"
	record.Markdown.Headings[0].Source = "caller-mutated record heading"
	*record.Description = "caller-mutated derived description"
	directoryMap.Body[0] ^= 0xff
	mapDescription.String = "caller-mutated map metadata"
	directoryMap.Markdown.Headings[0].Source = "caller-mutated map heading"
	*directoryMap.Description = "caller-mutated map description"
	schema.Documentation[0] ^= 0xff
	schema.Markdown.Headings[0].Source = "caller-mutated schema heading"
	schema.RawSchema["type"] = "array"
	delete(properties, "description")
	schema.Body.RequiredSections = append(schema.Body.RequiredSections, "Caller mutation")
	schema.Validator = nil
	schema.Version = nil
	treeFile := snapshot.Tree.Files[recordPath]
	treeFile.Data[0] ^= 0xff
	snapshot.Tree.Files[recordPath] = treeFile
	delete(snapshot.Records, recordPath)
	delete(snapshot.Maps, "README.md")
	delete(snapshot.Schemas, ".engram/schemas/note.md")

	second, err := store.AuditAccepted(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if loads.Load() != 1 || second.Tip != wantTip || len(second.Audits) != wantAudits || len(second.Raw.Commits) != wantRaw || len(second.Snapshots) != wantSnapshots {
		t.Fatalf("cached result was aliased: loads=%d result=%#v raw=%d snapshots=%d", loads.Load(), second, len(second.Raw.Commits), len(second.Snapshots))
	}
	if hasFinding(second.Validation.Findings, "E999", ".") {
		t.Fatal("caller finding mutation contaminated the cache")
	}
	secondSnapshot := second.Snapshots[wantTip]
	if secondSnapshot == nil {
		t.Fatal("caller snapshot-map mutation contaminated the cache")
	}
	secondRecord := secondSnapshot.Records[recordPath]
	secondMap := secondSnapshot.Maps["README.md"]
	secondSchema := secondSnapshot.Schemas[".engram/schemas/note.md"]
	if secondRecord == nil || secondMap == nil || secondSchema == nil || secondRecord.Description == nil || secondMap.Description == nil {
		t.Fatal("caller map-entry mutation contaminated the cache")
	}
	secondRecordDescription, _ := secondRecord.Frontmatter.Lookup("description")
	secondMapDescription, _ := secondMap.Frontmatter.Lookup("description")
	secondProperties, _ := secondSchema.RawSchema["properties"].(map[string]any)
	if string(secondRecord.Bytes) != string(wantRecordBytes) || string(secondRecord.Body) != string(wantRecordBody) || secondRecordDescription == nil || secondRecordDescription.String != wantRecordDescription || secondRecord.Markdown.Headings[0].Source != wantRecordHeading || *secondRecord.Description != wantDerivedRecordDescription {
		t.Fatal("caller record mutation contaminated the cache")
	}
	if string(secondMap.Body) != string(wantMapBody) || secondMapDescription == nil || secondMapDescription.String != wantMapDescription || secondMap.Markdown.Headings[0].Source != wantMapHeading || *secondMap.Description != wantDerivedMapDescription {
		t.Fatal("caller map mutation contaminated the cache")
	}
	if string(secondSchema.Documentation) != string(wantSchemaDocumentation) || secondSchema.Markdown.Headings[0].Source != wantSchemaHeading || !reflect.DeepEqual(secondSchema.RawSchema["type"], wantSchemaType) || secondSchema.Validator == nil || secondSchema.Version == nil || !reflect.DeepEqual(secondSchema.Body.RequiredSections, wantRequiredSections) {
		t.Fatal("caller schema mutation contaminated the cache")
	}
	if secondProperties == nil || !reflect.DeepEqual(secondProperties["description"], wantDescriptionSchema) {
		t.Fatal("caller nested JSON-schema mutation contaminated the cache")
	}
	if got := secondSnapshot.Tree.Files[recordPath].Data; string(got) != string(wantTreeFile) {
		t.Fatal("caller snapshot-tree mutation contaminated the cache")
	}
}

func TestAcceptedAuditCacheSingleFlightsConcurrentExactKey(t *testing.T) {
	cache := newAcceptedAuditCache()
	key := acceptedAuditCacheKey{
		format:  gitraw.SHA1,
		tip:     "0123456789012345678901234567890123456789",
		ruleset: acceptedAuditRuleSetIdentity,
	}
	var loads atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	loader := func(ctx context.Context) (*AcceptedAudit, error) {
		loads.Add(1)
		once.Do(func() { close(started) })
		select {
		case <-release:
			return acceptedAuditCacheFixture(key.tip), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	const callers = 8
	begin := make(chan struct{})
	errors := make(chan error, callers)
	var group sync.WaitGroup
	group.Add(callers)
	for index := range callers {
		go func() {
			defer group.Done()
			<-begin
			result, err := cache.get(context.Background(), key, loader)
			if err == nil && (result == nil || result.Tip == "") {
				err = fmt.Errorf("empty accepted audit")
			}
			if err == nil {
				// Mutating each returned graph concurrently makes accidental
				// sharing observable to the race detector.
				current := result.Snapshots[key.tip]
				current.Records["record.md"].Bytes[0] = byte(index)
				current.Records["record.md"].Frontmatter.Mapping[0].Value.String = fmt.Sprintf("caller-%d", index)
				current.Maps["README.md"].Body[0] = byte(index)
				current.Schemas["schema.md"].RawSchema["type"] = fmt.Sprintf("caller-%d", index)
			}
			errors <- err
		}()
	}
	close(begin)
	<-started
	close(release)
	group.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	if loads.Load() != 1 {
		t.Fatalf("concurrent lineage audit loads = %d, want 1", loads.Load())
	}
}

func acceptedAuditCacheFixture(tip string) *AcceptedAudit {
	recordDescription := "record description"
	mapDescription := "map description"
	return &AcceptedAudit{
		Tip: tip,
		Validation: checker.Result{
			Target: checker.TargetManagedStore, Status: checker.StatusComplete,
			Findings: []checker.Finding{},
		},
		Audits: []HistoryAudit{},
		Raw:    &gitraw.Audit{Complete: true},
		Snapshots: map[string]*checker.Snapshot{
			tip: {
				Tree: &snapshot.Tree{
					Directories: []string{"."},
					Files: map[string]snapshot.File{
						"record.md": {Path: "record.md", Role: snapshot.RoleRecord, Data: []byte("record bytes")},
					},
					Boundaries: map[string]snapshot.Kind{"record.md": snapshot.KindRegular},
					Issues:     []snapshot.Issue{},
				},
				Validation: checker.Result{Target: checker.TargetSnapshot, Status: checker.StatusComplete, Findings: []checker.Finding{}},
				Records: map[string]*checker.Record{
					"record.md": {
						Path: "record.md", Bytes: []byte("record bytes"), Body: []byte("record body"),
						Frontmatter: &yamlprofile.Node{Kind: yamlprofile.MappingKind, Mapping: []yamlprofile.Member{{
							Key: "description", Value: &yamlprofile.Node{Kind: yamlprofile.StringKind, String: recordDescription},
						}}},
						Markdown:    markdownprofile.Document{Headings: []markdownprofile.Heading{{Level: 1, Source: "Record"}}},
						Description: &recordDescription,
					},
				},
				Schemas: map[string]*checker.Schema{
					"schema.md": {
						Path: "schema.md",
						RawSchema: map[string]any{
							"type": "object", "properties": map[string]any{"description": map[string]any{"type": "string"}},
						},
						Documentation: []byte("schema documentation"),
						Markdown:      markdownprofile.Document{Headings: []markdownprofile.Heading{{Level: 1, Source: "Schema"}}},
						Body:          checker.BodyRequirements{RequiredSections: []string{"Details"}},
					},
				},
				Maps: map[string]*checker.Map{
					"README.md": {
						Path: "README.md", Body: []byte("map body"),
						Frontmatter: &yamlprofile.Node{Kind: yamlprofile.MappingKind, Mapping: []yamlprofile.Member{{
							Key: "description", Value: &yamlprofile.Node{Kind: yamlprofile.StringKind, String: mapDescription},
						}}},
						Markdown:    markdownprofile.Document{Headings: []markdownprofile.Heading{{Level: 1, Source: "Map"}}},
						Description: &mapDescription,
					},
				},
			},
		},
	}
}

func TestAcceptedAuditRuleSetIdentityMatchesNormativeSources(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{filepath.Join("..", "..", "docs", "spec", "README.md"), acceptedAuditCoreSHA256},
		{filepath.Join("..", "..", "docs", "spec", "annex-git.md"), acceptedAuditGitSHA256},
	}
	for _, test := range tests {
		data, err := os.ReadFile(test.path)
		if err != nil {
			t.Fatal(err)
		}
		if got := fmt.Sprintf("%x", sha256.Sum256(data)); got != test.want {
			t.Errorf("digest of %s = %s, want %s", test.path, got, test.want)
		}
	}
	wantIdentity := "core/v1@sha256:" + acceptedAuditCoreSHA256 + ";annex-git/v1@sha256:" + acceptedAuditGitSHA256
	if acceptedAuditRuleSetIdentity != wantIdentity {
		t.Fatalf("accepted audit rule-set identity = %q, want %q", acceptedAuditRuleSetIdentity, wantIdentity)
	}
}
