package gitraw

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/ontopix/engram/internal/snapshot"
)

type memoryReader struct {
	objects map[OID]Object
	reads   []OID
}

func (m *memoryReader) ReadObject(_ context.Context, oid OID) (Object, error) {
	m.reads = append(m.reads, oid)
	object, ok := m.objects[oid]
	if !ok {
		return Object{}, &Error{Kind: FailureMissing, Op: "memory", OID: oid}
	}
	return object, nil
}

func TestTreeSourceDoesNotResolvePrunedOrTargetFreeEntries(t *testing.T) {
	t.Parallel()
	root := mustOID(t, SHA1, '1')
	missingDot := mustOID(t, SHA1, '2')
	missingLink := mustOID(t, SHA1, '3')
	missingGitlink := mustOID(t, SHA1, '4')
	raw := appendTreeEntry(nil, SHA1, ModeRegular, ".hidden", 2)
	raw = appendTreeEntry(raw, SHA1, ModeSymlink, "link", 3)
	raw = appendTreeEntry(raw, SHA1, ModeGitlink, "module", 4)
	reader := &memoryReader{objects: map[OID]Object{
		root: {OID: root, Type: TypeTree, Data: raw},
	}}
	source := NewTreeSource(context.Background(), reader, root)
	projected, err := snapshot.Load(source)
	if err != nil {
		t.Fatal(err)
	}
	if len(reader.reads) != 1 || reader.reads[0] != root {
		t.Fatalf("resolved targets = %v; missing IDs %s %s %s must remain unread", reader.reads, missingDot, missingLink, missingGitlink)
	}
	if got := source.PrunedWithoutCoreFinding(); len(got) != 1 || got[0] != ".hidden" {
		t.Fatalf("pruned = %v", got)
	}
	if len(projected.Issues) != 2 || projected.Issues[0].Code != "E103" || projected.Issues[1].Code != "E104" {
		t.Fatalf("issues = %#v", projected.Issues)
	}
}

func TestTreeSourceProjectsRoutineDeclarations(t *testing.T) {
	t.Parallel()
	root := mustOID(t, SHA1, '1')
	config := mustOID(t, SHA1, '2')
	routines := mustOID(t, SHA1, '3')
	declaration := mustOID(t, SHA1, '4')
	rootTree := appendTreeEntry(nil, SHA1, ModeDirectory, ".engram", 0x22)
	configTree := appendTreeEntry(nil, SHA1, ModeDirectory, "routines", 0x33)
	routineTree := appendTreeEntry(nil, SHA1, ModeRegular, "daily-journal.md", 0x44)
	reader := &memoryReader{objects: map[OID]Object{
		root:        {OID: root, Type: TypeTree, Data: rootTree},
		config:      {OID: config, Type: TypeTree, Data: configTree},
		routines:    {OID: routines, Type: TypeTree, Data: routineTree},
		declaration: {OID: declaration, Type: TypeBlob, Data: []byte("routine\n")},
	}}
	source := NewTreeSource(context.Background(), reader, root)
	tree, err := snapshot.Load(source)
	if err != nil {
		t.Fatal(err)
	}
	if pruned := source.PrunedWithoutCoreFinding(); len(pruned) != 0 {
		t.Fatalf("pruned = %v", pruned)
	}
	file, exists := tree.Files[".engram/routines/daily-journal.md"]
	if !exists || file.Role != snapshot.RoleRoutine {
		t.Fatalf("routine = %#v, exists = %t", file, exists)
	}
}

func TestAuditStopsAtMergeBeforeTreeAndParents(t *testing.T) {
	t.Parallel()
	commitID := mustOID(t, SHA1, '1')
	treeID := mustOID(t, SHA1, '2')
	parentOne := mustOID(t, SHA1, '3')
	parentTwo := mustOID(t, SHA1, '4')
	reader := &memoryReader{objects: map[OID]Object{
		commitID: {OID: commitID, Type: TypeCommit, Data: rawCommit(treeID, parentOne, parentTwo)},
	}}
	audit, err := AuditLineage(context.Background(), reader, commitID)
	if err != nil {
		t.Fatal(err)
	}
	if !audit.Complete || !hasFinding(audit, "E602") {
		t.Fatalf("audit = %#v", audit)
	}
	if len(reader.reads) != 1 || reader.reads[0] != commitID {
		t.Fatalf("merge resolved forbidden targets: %v", reader.reads)
	}
}

func TestAuditReportsMissingRequiredObjectAsTypedCapabilityFailure(t *testing.T) {
	t.Parallel()
	commitID := mustOID(t, SHA1, '1')
	treeID := mustOID(t, SHA1, '2')
	reader := &memoryReader{objects: map[OID]Object{
		commitID: {OID: commitID, Type: TypeCommit, Data: rawCommit(treeID)},
	}}
	audit, err := AuditLineage(context.Background(), reader, commitID)
	if !errors.Is(err, ErrMissingObject) {
		t.Fatalf("error = %v, want missing object", err)
	}
	if audit.Complete || hasFinding(audit, "E601") {
		t.Fatalf("missing object must not become E601: %#v", audit)
	}
}

func TestAuditMalformedTreeIsE601AndParentTraversalContinues(t *testing.T) {
	t.Parallel()
	rootCommit := mustOID(t, SHA1, '1')
	tipCommit := mustOID(t, SHA1, '2')
	rootTree := mustOID(t, SHA1, '3')
	badTree := mustOID(t, SHA1, '4')
	reader := &memoryReader{objects: map[OID]Object{
		rootCommit: {OID: rootCommit, Type: TypeCommit, Data: rawCommit(rootTree)},
		tipCommit:  {OID: tipCommit, Type: TypeCommit, Data: rawCommit(badTree, rootCommit)},
		rootTree:   {OID: rootTree, Type: TypeTree, Data: nil},
		badTree:    {OID: badTree, Type: TypeTree, Data: []byte("bad")},
	}}
	audit, err := AuditLineage(context.Background(), reader, tipCommit)
	if err != nil {
		t.Fatal(err)
	}
	if !audit.Complete || !hasFinding(audit, "E601") || len(audit.Commits) != 2 || audit.Commits[0].ID != rootCommit || audit.Commits[1].ID != tipCommit {
		t.Fatalf("audit = %#v", audit)
	}
}

func TestAuditE603DoesNotRequirePrunedTarget(t *testing.T) {
	t.Parallel()
	commitID := mustOID(t, SHA1, '1')
	treeID := mustOID(t, SHA1, '2')
	rawTree := appendTreeEntry(nil, SHA1, ModeRegular, ".private", 9)
	reader := &memoryReader{objects: map[OID]Object{
		commitID: {OID: commitID, Type: TypeCommit, Data: rawCommit(treeID)},
		treeID:   {OID: treeID, Type: TypeTree, Data: rawTree},
	}}
	audit, err := AuditLineage(context.Background(), reader, commitID)
	if err != nil {
		t.Fatal(err)
	}
	if !hasFinding(audit, "E603") || len(reader.reads) != 2 {
		t.Fatalf("audit = %#v, reads = %v", audit, reader.reads)
	}
}

func TestAuditLinearHistoryIsRootToTip(t *testing.T) {
	t.Parallel()
	rootCommit := mustOID(t, SHA256, '1')
	tipCommit := mustOID(t, SHA256, '2')
	treeID := mustOID(t, SHA256, '3')
	reader := &memoryReader{objects: map[OID]Object{
		rootCommit: {OID: rootCommit, Type: TypeCommit, Data: rawCommit(treeID)},
		tipCommit:  {OID: tipCommit, Type: TypeCommit, Data: rawCommit(treeID, rootCommit)},
		treeID:     {OID: treeID, Type: TypeTree, Data: nil},
	}}
	audit, err := AuditLineage(context.Background(), reader, tipCommit)
	if err != nil {
		t.Fatal(err)
	}
	if !audit.Complete || len(audit.Findings) != 0 || len(audit.Commits) != 2 || audit.Commits[0].ID != rootCommit || audit.Commits[1].ID != tipCommit {
		t.Fatalf("audit = %#v", audit)
	}
}

func mustOID(t *testing.T, format ObjectFormat, fill byte) OID {
	t.Helper()
	oid, err := ParseOID(format, strings.Repeat(string(fill), format.HexWidth()))
	if err != nil {
		t.Fatal(err)
	}
	return oid
}

func rawCommit(tree OID, parents ...OID) []byte {
	var builder strings.Builder
	fmt.Fprintf(&builder, "tree %s\n", tree)
	for _, parent := range parents {
		fmt.Fprintf(&builder, "parent %s\n", parent)
	}
	builder.WriteString("\n")
	return []byte(builder.String())
}

func hasFinding(audit *Audit, code string) bool {
	for _, finding := range audit.Findings {
		if finding.Code == code {
			return true
		}
	}
	return false
}
