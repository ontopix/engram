package managedread

import (
	"context"
	"errors"
	"sync"

	"github.com/ontopix/engram/internal/checker"
	"github.com/ontopix/engram/internal/gitraw"
	"github.com/ontopix/engram/internal/markdownprofile"
	"github.com/ontopix/engram/internal/snapshot"
	"github.com/ontopix/engram/internal/yamlprofile"
)

// The accepted-history checker implements these exact normative source bytes.
// Keeping their digests in the cache key prevents a long-lived process from
// reusing an audit after its rule-set identity changes. A repository test binds
// both values to the authoritative checked-in documents.
const (
	acceptedAuditCoreSHA256 = "19ae8dc527c9e7519b202d0eef3a23ab08fe99e1733a49f52304bcc380d587ad"
	acceptedAuditGitSHA256  = "7912f9f77ee5d87be06f1680c6924c8ad86453e809a5be0e7c05c21b41414193"

	acceptedAuditRuleSetIdentity = "core/v1@sha256:" + acceptedAuditCoreSHA256 +
		";annex-git/v1@sha256:" + acceptedAuditGitSHA256
)

type acceptedAuditLoader func(context.Context, *Store) (*AcceptedAudit, error)

type acceptedAuditCacheKey struct {
	format  gitraw.ObjectFormat
	tip     string
	ruleset string
}

type acceptedAuditFlight struct {
	done       chan struct{}
	generation uint64
}

// acceptedAuditCache is intentionally a one-result cache. It bounds memory in
// long-lived embedders while single-flighting concurrent requests for the same
// exact tip and rule set. A generation prevents an older, slower computation
// from replacing the result of a newer key.
type acceptedAuditCache struct {
	mu        sync.Mutex
	key       acceptedAuditCacheKey
	result    *AcceptedAudit
	flights   map[acceptedAuditCacheKey]*acceptedAuditFlight
	next      uint64
	published uint64
}

func newAcceptedAuditCache() *acceptedAuditCache {
	return &acceptedAuditCache{flights: make(map[acceptedAuditCacheKey]*acceptedAuditFlight)}
}

func (s *Store) cachedAcceptedAudit(ctx context.Context) (*AcceptedAudit, error) {
	if s == nil || s.repository == nil || s.repository.Head == nil || !s.repository.Head.Valid() {
		return s.loadAcceptedAudit(ctx)
	}
	ruleset := s.ruleSetID
	if ruleset == "" {
		ruleset = acceptedAuditRuleSetIdentity
	}
	key := acceptedAuditCacheKey{
		format:  s.repository.Format,
		tip:     s.repository.Head.String(),
		ruleset: ruleset,
	}
	if s.acceptedAudits == nil {
		return s.loadAcceptedAudit(ctx)
	}
	return s.acceptedAudits.get(ctx, key, func(loadContext context.Context) (*AcceptedAudit, error) {
		return s.loadAcceptedAudit(loadContext)
	})
}

func (s *Store) loadAcceptedAudit(ctx context.Context) (*AcceptedAudit, error) {
	if s != nil && s.auditLoader != nil {
		return s.auditLoader(ctx, s)
	}
	return s.auditAccepted(ctx)
}

func (c *acceptedAuditCache) get(ctx context.Context, key acceptedAuditCacheKey, load func(context.Context) (*AcceptedAudit, error)) (*AcceptedAudit, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		c.mu.Lock()
		if c.result != nil && c.key == key {
			result := cloneAcceptedAudit(c.result)
			c.mu.Unlock()
			return result, nil
		}
		if flight := c.flights[key]; flight != nil {
			done := flight.done
			c.mu.Unlock()
			select {
			case <-done:
				// A successful flight populated the cache. A failed flight did
				// not; loop so one surviving waiter can retry with its context.
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}

		c.next++
		flight := &acceptedAuditFlight{done: make(chan struct{}), generation: c.next}
		c.flights[key] = flight
		c.mu.Unlock()

		loaded, err := load(ctx)
		if err == nil && loaded == nil {
			err = errors.New("managedread: accepted audit loader returned no result")
		}
		var canonical *AcceptedAudit
		if err == nil {
			canonical = cloneAcceptedAudit(loaded)
		}

		c.mu.Lock()
		delete(c.flights, key)
		if canonical != nil && flight.generation >= c.published {
			c.key = key
			c.result = canonical
			c.published = flight.generation
		}
		close(flight.done)
		c.mu.Unlock()

		if err != nil {
			return nil, err
		}
		return cloneAcceptedAudit(canonical), nil
	}
}

// cloneAcceptedAudit protects every exported mutable container reachable from
// a cached result. The only shared pointers are to types whose state is
// private and immutable after construction (exact YAML numbers and compiled
// schema validators).
func cloneAcceptedAudit(source *AcceptedAudit) *AcceptedAudit {
	if source == nil {
		return nil
	}
	result := *source
	result.Validation = cloneCheckerResult(source.Validation)
	result.Audits = make([]HistoryAudit, len(source.Audits))
	for index, audit := range source.Audits {
		result.Audits[index] = HistoryAudit{
			Base:       cloneAuditString(audit.Base),
			Candidate:  audit.Candidate,
			Validation: cloneCheckerResult(audit.Validation),
		}
	}
	result.Raw = cloneRawAudit(source.Raw)
	result.Snapshots = make(map[string]*checker.Snapshot, len(source.Snapshots))
	for id, current := range source.Snapshots {
		result.Snapshots[id] = cloneCheckerSnapshot(current)
	}
	return &result
}

func cloneCheckerResult(source checker.Result) checker.Result {
	result := source
	result.Findings = append([]checker.Finding(nil), source.Findings...)
	return result
}

func cloneCheckerSnapshot(source *checker.Snapshot) *checker.Snapshot {
	if source == nil {
		return nil
	}
	result := *source
	result.Tree = cloneSnapshotTree(source.Tree)
	result.Validation = cloneCheckerResult(source.Validation)
	result.Records = make(map[string]*checker.Record, len(source.Records))
	for name, record := range source.Records {
		result.Records[name] = cloneCheckerRecord(record)
	}
	result.Schemas = make(map[string]*checker.Schema, len(source.Schemas))
	for name, schema := range source.Schemas {
		result.Schemas[name] = cloneCheckerSchema(schema)
	}
	result.Maps = make(map[string]*checker.Map, len(source.Maps))
	for name, directoryMap := range source.Maps {
		result.Maps[name] = cloneCheckerMap(directoryMap)
	}
	return &result
}

func cloneCheckerRecord(source *checker.Record) *checker.Record {
	if source == nil {
		return nil
	}
	result := *source
	result.Bytes = append([]byte(nil), source.Bytes...)
	result.Frontmatter = cloneYAMLNode(source.Frontmatter)
	result.Body = append([]byte(nil), source.Body...)
	result.Markdown = cloneMarkdownDocument(source.Markdown)
	result.Description = cloneAuditString(source.Description)
	return &result
}

func cloneCheckerMap(source *checker.Map) *checker.Map {
	if source == nil {
		return nil
	}
	result := *source
	result.Frontmatter = cloneYAMLNode(source.Frontmatter)
	result.Body = append([]byte(nil), source.Body...)
	result.Markdown = cloneMarkdownDocument(source.Markdown)
	result.Description = cloneAuditString(source.Description)
	return &result
}

func cloneCheckerSchema(source *checker.Schema) *checker.Schema {
	if source == nil {
		return nil
	}
	result := *source
	result.RawSchema = cloneJSONObject(source.RawSchema)
	result.RawBody = cloneJSONValue(source.RawBody)
	result.RawPolicy = cloneJSONValue(source.RawPolicy)
	result.Documentation = append([]byte(nil), source.Documentation...)
	result.Markdown = cloneMarkdownDocument(source.Markdown)
	result.Body.RequiredSections = append([]string(nil), source.Body.RequiredSections...)
	// Number exposes no mutation and schemaprofile.Schema documents compiled
	// validators as concurrency-safe after construction, so those two opaque
	// pointers are the immutable leaves of the copy graph.
	return &result
}

func cloneYAMLNode(source *yamlprofile.Node) *yamlprofile.Node {
	if source == nil {
		return nil
	}
	result := *source
	result.Sequence = make([]*yamlprofile.Node, len(source.Sequence))
	for index, child := range source.Sequence {
		result.Sequence[index] = cloneYAMLNode(child)
	}
	result.Mapping = make([]yamlprofile.Member, len(source.Mapping))
	for index, member := range source.Mapping {
		result.Mapping[index] = member
		result.Mapping[index].Value = cloneYAMLNode(member.Value)
	}
	return &result
}

func cloneMarkdownDocument(source markdownprofile.Document) markdownprofile.Document {
	return markdownprofile.Document{
		Links:     append([]markdownprofile.Link(nil), source.Links...),
		Headings:  append([]markdownprofile.Heading(nil), source.Headings...),
		Wikilinks: append([]markdownprofile.Wikilink(nil), source.Wikilinks...),
	}
}

func cloneJSONObject(source map[string]any) map[string]any {
	if source == nil {
		return nil
	}
	result, _ := cloneJSONValue(source).(map[string]any)
	return result
}

func cloneJSONValue(source any) any {
	switch source := source.(type) {
	case map[string]any:
		result := make(map[string]any, len(source))
		for name, value := range source {
			result[name] = cloneJSONValue(value)
		}
		return result
	case []any:
		result := make([]any, len(source))
		for index, value := range source {
			result[index] = cloneJSONValue(value)
		}
		return result
	default:
		// The admitted JSON data model has immutable scalar leaves.
		return source
	}
}

func cloneRawAudit(source *gitraw.Audit) *gitraw.Audit {
	if source == nil {
		return nil
	}
	result := *source
	result.Commits = make([]gitraw.AuditedCommit, len(source.Commits))
	for index, audited := range source.Commits {
		result.Commits[index] = gitraw.AuditedCommit{
			ID:       audited.ID,
			Commit:   cloneRawCommit(audited.Commit),
			Snapshot: cloneSnapshotTree(audited.Snapshot),
		}
	}
	result.Findings = make([]gitraw.Finding, len(source.Findings))
	for index, finding := range source.Findings {
		result.Findings[index] = gitraw.Finding{
			Code:    finding.Code,
			Path:    finding.Path,
			Commits: append([]gitraw.OID(nil), finding.Commits...),
			Detail:  append([]string(nil), finding.Detail...),
		}
	}
	return &result
}

func cloneRawCommit(source *gitraw.Commit) *gitraw.Commit {
	if source == nil {
		return nil
	}
	result := *source
	result.Parents = append([]gitraw.OID(nil), source.Parents...)
	result.Headers = make([]gitraw.Header, len(source.Headers))
	for index, header := range source.Headers {
		result.Headers[index] = gitraw.Header{
			Name:          header.Name,
			Value:         append([]byte(nil), header.Value...),
			Continuations: make([][]byte, len(header.Continuations)),
		}
		for continuation := range header.Continuations {
			result.Headers[index].Continuations[continuation] = append([]byte(nil), header.Continuations[continuation]...)
		}
	}
	result.Message = append([]byte(nil), source.Message...)
	return &result
}

func cloneSnapshotTree(source *snapshot.Tree) *snapshot.Tree {
	if source == nil {
		return nil
	}
	result := &snapshot.Tree{
		Directories: append([]string(nil), source.Directories...),
		Files:       make(map[string]snapshot.File, len(source.Files)),
		Boundaries:  make(map[string]snapshot.Kind, len(source.Boundaries)),
		Issues:      append([]snapshot.Issue(nil), source.Issues...),
	}
	for name, file := range source.Files {
		file.Data = append([]byte(nil), file.Data...)
		result.Files[name] = file
	}
	for name, kind := range source.Boundaries {
		result.Boundaries[name] = kind
	}
	return result
}

func cloneAuditString(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
