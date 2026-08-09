package checker

import (
	"path"

	"github.com/ontopix/engram/internal/markdownprofile"
	"github.com/ontopix/engram/internal/schemaprofile"
	"github.com/ontopix/engram/internal/snapshot"
	"github.com/ontopix/engram/internal/yamlprofile"
)

// Snapshot is the reusable result of portable snapshot analysis. Managed
// history, changeset policy checks, schema queries, and authoring helpers all
// consume this same immutable view.
type Snapshot struct {
	Tree       *snapshot.Tree
	Validation Result
	Records    map[string]*Record
	Schemas    map[string]*Schema
	Maps       map[string]*Map
}

type Record struct {
	Path        string
	Bytes       []byte
	Frontmatter *yamlprofile.Node
	Body        []byte
	Markdown    markdownprofile.Document
	Type        string
	Description *string
	Pinned      bool
	PinnedValid bool
	SchemaPath  string
	Policy      Policy
}

type Map struct {
	Path        string
	Frontmatter *yamlprofile.Node
	Body        []byte
	Markdown    markdownprofile.Document
	Description *string
	Catalog     string
}

type Policy struct {
	Immutable  bool
	AppendOnly bool
	Available  bool
}

type Schema struct {
	Path          string
	Scope         string
	Type          string
	Description   string
	Version       *yamlprofile.Number
	RawSchema     map[string]any
	RawBody       any
	RawPolicy     any
	Documentation []byte
	Markdown      markdownprofile.Document
	Body          BodyRequirements
	Policy        Policy
	Validator     *schemaprofile.Schema
	Valid         bool
	SchemaValid   bool
	BodyValid     bool
	PolicyValid   bool
	Vendor        bool
}

type BodyRequirements struct {
	RequiredSections []string
}

func schemaScope(schemaPath string) string {
	return path.Dir(path.Dir(path.Dir(schemaPath)))
}

func inScope(scope, logicalPath string) bool {
	if scope == "." {
		return true
	}
	return logicalPath == scope || len(logicalPath) > len(scope) && logicalPath[:len(scope)] == scope && logicalPath[len(scope)] == '/'
}

func joinScope(scope, suffix string) string {
	if scope == "." {
		return suffix
	}
	return path.Join(scope, suffix)
}
