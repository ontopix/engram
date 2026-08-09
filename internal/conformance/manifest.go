// Package conformance loads and materializes the repository's versioned
// conformance-fixture manifest.
package conformance

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

const ManifestVersion = 1

type CaseKind string

const (
	KindSnapshot  CaseKind = "snapshot"
	KindChangeset CaseKind = "changeset"
)

type OperationKind string

const (
	OperationWriteText   OperationKind = "write_text"
	OperationWriteBase64 OperationKind = "write_base64"
	OperationRemove      OperationKind = "remove"
)

type ExpectedStatus string

const (
	StatusComplete      ExpectedStatus = "complete"
	StatusIndeterminate ExpectedStatus = "indeterminate"
)

// Manifest is the closed, versioned shape of testdata/conformance/cases.json.
type Manifest struct {
	Version int         `json:"version"`
	Seed    string      `json:"seed"`
	Common  []Operation `json:"common"`
	Cases   []Case      `json:"cases"`
}

// Case describes one independently materialized snapshot or changeset.
type Case struct {
	ID          string     `json:"id"`
	Description string     `json:"description"`
	Kind        CaseKind   `json:"kind"`
	Snapshot    *State     `json:"snapshot,omitempty"`
	Base        *BaseState `json:"base,omitempty"`
	Candidate   *State     `json:"candidate,omitempty"`
	Expected    *Expected  `json:"expected"`
}

// State is a sequence of operations applied after the manifest's common
// operations to a fresh copy of Seed.
type State struct {
	Operations []Operation `json:"operations"`
}

// BaseState represents either an ordinary state or the exact JSON string
// "unavailable".
type BaseState struct {
	Unavailable bool
	Operations  []Operation
}

// Operation is a closed tagged union. Source is present only for write_text;
// Content is present only for write_base64.
type Operation struct {
	Kind    OperationKind `json:"operation"`
	Path    string        `json:"path"`
	Source  *string       `json:"source,omitempty"`
	Content *string       `json:"content,omitempty"`
}

type Expected struct {
	Status   *ExpectedStatus `json:"status,omitempty"`
	Findings []Finding       `json:"findings"`
}

type Finding struct {
	Code string `json:"code"`
	Path string `json:"path"`
}

var findingCodePattern = regexp.MustCompile(`^[EW][0-9]{3}$`)

// Load reads, strictly decodes, and validates a manifest file.
func Load(name string) (*Manifest, error) {
	data, err := os.ReadFile(name)
	if err != nil {
		return nil, fmt.Errorf("read conformance manifest: %w", err)
	}
	manifest, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("parse conformance manifest %q: %w", name, err)
	}
	return manifest, nil
}

// Parse strictly decodes and validates a manifest. Unknown and duplicate JSON
// object fields are rejected at every depth.
func Parse(data []byte) (*Manifest, error) {
	if !utf8.Valid(data) {
		return nil, fmt.Errorf("manifest is not valid UTF-8")
	}
	if err := rejectDuplicateFields(data); err != nil {
		return nil, err
	}
	var manifest Manifest
	if err := decodeClosed(data, &manifest); err != nil {
		return nil, err
	}
	if err := manifest.Validate(); err != nil {
		return nil, err
	}
	return &manifest, nil
}

// Validate checks all version, shape, tagged-union, ordering, and safe-path
// invariants that do not require filesystem access.
func (m *Manifest) Validate() error {
	if m == nil {
		return fmt.Errorf("manifest is nil")
	}
	if m.Version != ManifestVersion {
		return fmt.Errorf("unsupported manifest version %d (want %d)", m.Version, ManifestVersion)
	}
	if err := validateRepositoryPath("seed", m.Seed); err != nil {
		return err
	}
	if m.Common == nil {
		return fmt.Errorf("common must be an array")
	}
	for i := range m.Common {
		if err := m.Common[i].validate(fmt.Sprintf("common[%d]", i)); err != nil {
			return err
		}
	}
	if len(m.Cases) == 0 {
		return fmt.Errorf("cases must be a non-empty array")
	}

	seenIDs := make(map[string]struct{}, len(m.Cases))
	for i := range m.Cases {
		c := &m.Cases[i]
		where := fmt.Sprintf("cases[%d]", i)
		if c.ID == "" {
			return fmt.Errorf("%s.id must be a non-empty string", where)
		}
		if _, exists := seenIDs[c.ID]; exists {
			return fmt.Errorf("%s.id %q is duplicated", where, c.ID)
		}
		seenIDs[c.ID] = struct{}{}
		if c.Description == "" {
			return fmt.Errorf("%s.description must be a non-empty string", where)
		}
		if c.Expected == nil {
			return fmt.Errorf("%s.expected is required", where)
		}
		if err := c.Expected.validate(where + ".expected"); err != nil {
			return err
		}

		switch c.Kind {
		case KindSnapshot:
			if c.Snapshot == nil {
				return fmt.Errorf("%s.snapshot is required for snapshot cases", where)
			}
			if c.Base != nil || c.Candidate != nil {
				return fmt.Errorf("%s snapshot case must not contain base or candidate", where)
			}
			if c.Expected.Status != nil {
				return fmt.Errorf("%s.expected.status is not allowed for snapshot cases", where)
			}
			if err := c.Snapshot.validate(where + ".snapshot"); err != nil {
				return err
			}
		case KindChangeset:
			if c.Snapshot != nil {
				return fmt.Errorf("%s changeset case must not contain snapshot", where)
			}
			if c.Base == nil {
				return fmt.Errorf("%s.base is required for changeset cases", where)
			}
			if c.Candidate == nil {
				return fmt.Errorf("%s.candidate is required for changeset cases", where)
			}
			if c.Expected.Status == nil {
				return fmt.Errorf("%s.expected.status is required for changeset cases", where)
			}
			if *c.Expected.Status != StatusComplete && *c.Expected.Status != StatusIndeterminate {
				return fmt.Errorf("%s.expected.status %q is invalid", where, *c.Expected.Status)
			}
			if err := c.Base.validate(where + ".base"); err != nil {
				return err
			}
			if c.Base.Unavailable && *c.Expected.Status != StatusIndeterminate {
				return fmt.Errorf("%s.expected.status must be %q when base is unavailable", where, StatusIndeterminate)
			}
			if !c.Base.Unavailable && *c.Expected.Status != StatusComplete {
				return fmt.Errorf("%s.expected.status must be %q when base is available", where, StatusComplete)
			}
			if err := c.Candidate.validate(where + ".candidate"); err != nil {
				return err
			}
		default:
			return fmt.Errorf("%s.kind %q is invalid", where, c.Kind)
		}
	}
	return nil
}

// CaseByID returns the manifest case with id.
func (m *Manifest) CaseByID(id string) (*Case, bool) {
	if m == nil {
		return nil, false
	}
	for i := range m.Cases {
		if m.Cases[i].ID == id {
			return &m.Cases[i], true
		}
	}
	return nil, false
}

func (s *State) validate(where string) error {
	if s.Operations == nil {
		return fmt.Errorf("%s.operations must be an array", where)
	}
	for i := range s.Operations {
		if err := s.Operations[i].validate(fmt.Sprintf("%s.operations[%d]", where, i)); err != nil {
			return err
		}
	}
	return nil
}

func (b *BaseState) validate(where string) error {
	if b.Unavailable {
		if b.Operations != nil {
			return fmt.Errorf("%s unavailable base must not contain operations", where)
		}
		return nil
	}
	return (&State{Operations: b.Operations}).validate(where)
}

func (o *Operation) validate(where string) error {
	if err := validateStorePath(where+".path", o.Path); err != nil {
		return err
	}
	switch o.Kind {
	case OperationWriteText:
		if o.Source == nil {
			return fmt.Errorf("%s.source is required for write_text", where)
		}
		if err := validateRepositoryPath(where+".source", *o.Source); err != nil {
			return err
		}
		if o.Content != nil {
			return fmt.Errorf("%s.content is not allowed for write_text", where)
		}
	case OperationWriteBase64:
		if o.Content == nil {
			return fmt.Errorf("%s.content is required for write_base64", where)
		}
		if o.Source != nil {
			return fmt.Errorf("%s.source is not allowed for write_base64", where)
		}
		if _, err := base64.StdEncoding.Strict().DecodeString(*o.Content); err != nil {
			return fmt.Errorf("%s.content is not strict RFC 4648 base64: %w", where, err)
		}
	case OperationRemove:
		if o.Source != nil || o.Content != nil {
			return fmt.Errorf("%s remove must contain only operation and path", where)
		}
	default:
		return fmt.Errorf("%s.operation %q is invalid", where, o.Kind)
	}
	return nil
}

func (e *Expected) validate(where string) error {
	if e.Findings == nil {
		return fmt.Errorf("%s.findings must be an array", where)
	}
	seen := make(map[string]struct{}, len(e.Findings))
	for i := range e.Findings {
		f := e.Findings[i]
		if !findingCodePattern.MatchString(f.Code) {
			return fmt.Errorf("%s.findings[%d].code %q is invalid", where, i, f.Code)
		}
		if f.Path == "" {
			return fmt.Errorf("%s.findings[%d].path must be a non-empty string", where, i)
		}
		key := f.Code + "\x00" + f.Path
		if _, exists := seen[key]; exists {
			return fmt.Errorf("%s.findings[%d] duplicates (%s, %s)", where, i, f.Code, f.Path)
		}
		seen[key] = struct{}{}
		if i > 0 && compareFinding(e.Findings[i-1], f) > 0 {
			return fmt.Errorf("%s.findings must be ordered by UTF-8 path bytes, then ASCII code", where)
		}
	}
	return nil
}

func compareFinding(a, b Finding) int {
	if n := bytes.Compare([]byte(a.Path), []byte(b.Path)); n != 0 {
		return n
	}
	return strings.Compare(a.Code, b.Code)
}

func validateRepositoryPath(field, value string) error {
	if value == "" {
		return fmt.Errorf("%s must be a non-empty repository-root-relative path", field)
	}
	return validateRelativePath(field, value)
}

func validateStorePath(field, value string) error {
	if value == "" {
		return fmt.Errorf("%s must be a non-empty store-root-relative path", field)
	}
	return validateRelativePath(field, value)
}

func validateRelativePath(field, value string) error {
	if !utf8.ValidString(value) || strings.ContainsRune(value, '\x00') {
		return fmt.Errorf("%s is not a valid UTF-8 path", field)
	}
	if strings.Contains(value, "\\") {
		return fmt.Errorf("%s must use '/' separators", field)
	}
	if strings.HasPrefix(value, "/") || path.IsAbs(value) || path.Clean(value) != value || value == "." {
		return fmt.Errorf("%s %q is not a normalized relative path", field, value)
	}
	for _, part := range strings.Split(value, "/") {
		if part == "" || part == "." || part == ".." {
			return fmt.Errorf("%s %q contains an unsafe path segment", field, value)
		}
	}
	return nil
}

func (b *BaseState) UnmarshalJSON(data []byte) error {
	var literal string
	if err := json.Unmarshal(data, &literal); err == nil {
		if literal != "unavailable" {
			return fmt.Errorf("base literal must be %q", "unavailable")
		}
		b.Unavailable = true
		b.Operations = nil
		return nil
	}
	var state State
	if err := decodeClosed(data, &state); err != nil {
		return fmt.Errorf("base must be a state object or %q: %w", "unavailable", err)
	}
	b.Unavailable = false
	b.Operations = state.Operations
	return nil
}

func (c *Case) UnmarshalJSON(data []byte) error {
	var wire struct {
		ID          string          `json:"id"`
		Description string          `json:"description"`
		Kind        CaseKind        `json:"kind"`
		Snapshot    json.RawMessage `json:"snapshot"`
		Base        json.RawMessage `json:"base"`
		Candidate   json.RawMessage `json:"candidate"`
		Expected    json.RawMessage `json:"expected"`
	}
	if err := decodeClosed(data, &wire); err != nil {
		return err
	}
	c.ID = wire.ID
	c.Description = wire.Description
	c.Kind = wire.Kind
	c.Snapshot = nil
	c.Base = nil
	c.Candidate = nil
	c.Expected = nil
	if len(wire.Snapshot) != 0 {
		if isJSONNull(wire.Snapshot) {
			return fmt.Errorf("snapshot must not be null")
		}
		var state State
		if err := decodeClosed(wire.Snapshot, &state); err != nil {
			return fmt.Errorf("snapshot: %w", err)
		}
		c.Snapshot = &state
	}
	if len(wire.Base) != 0 {
		if isJSONNull(wire.Base) {
			return fmt.Errorf("base must not be null")
		}
		var base BaseState
		if err := base.UnmarshalJSON(wire.Base); err != nil {
			return err
		}
		c.Base = &base
	}
	if len(wire.Candidate) != 0 {
		if isJSONNull(wire.Candidate) {
			return fmt.Errorf("candidate must not be null")
		}
		var state State
		if err := decodeClosed(wire.Candidate, &state); err != nil {
			return fmt.Errorf("candidate: %w", err)
		}
		c.Candidate = &state
	}
	if len(wire.Expected) != 0 {
		if isJSONNull(wire.Expected) {
			return fmt.Errorf("expected must not be null")
		}
		var expected Expected
		if err := expected.UnmarshalJSON(wire.Expected); err != nil {
			return err
		}
		c.Expected = &expected
	}
	return nil
}

func (o *Operation) UnmarshalJSON(data []byte) error {
	var wire struct {
		Kind    OperationKind   `json:"operation"`
		Path    string          `json:"path"`
		Source  json.RawMessage `json:"source"`
		Content json.RawMessage `json:"content"`
	}
	if err := decodeClosed(data, &wire); err != nil {
		return err
	}
	o.Kind = wire.Kind
	o.Path = wire.Path
	o.Source = nil
	o.Content = nil
	if len(wire.Source) != 0 {
		if isJSONNull(wire.Source) {
			return fmt.Errorf("source must not be null")
		}
		var source string
		if err := json.Unmarshal(wire.Source, &source); err != nil {
			return fmt.Errorf("source must be a string: %w", err)
		}
		o.Source = &source
	}
	if len(wire.Content) != 0 {
		if isJSONNull(wire.Content) {
			return fmt.Errorf("content must not be null")
		}
		var content string
		if err := json.Unmarshal(wire.Content, &content); err != nil {
			return fmt.Errorf("content must be a string: %w", err)
		}
		o.Content = &content
	}
	return nil
}

func (e *Expected) UnmarshalJSON(data []byte) error {
	var wire struct {
		Status   json.RawMessage `json:"status"`
		Findings json.RawMessage `json:"findings"`
	}
	if err := decodeClosed(data, &wire); err != nil {
		return err
	}
	e.Status = nil
	e.Findings = nil
	if len(wire.Status) != 0 {
		if isJSONNull(wire.Status) {
			return fmt.Errorf("status must not be null")
		}
		var status ExpectedStatus
		if err := json.Unmarshal(wire.Status, &status); err != nil {
			return fmt.Errorf("status must be a string: %w", err)
		}
		e.Status = &status
	}
	if len(wire.Findings) != 0 {
		if isJSONNull(wire.Findings) {
			return fmt.Errorf("findings must not be null")
		}
		if err := decodeClosed(wire.Findings, &e.Findings); err != nil {
			return fmt.Errorf("findings: %w", err)
		}
	}
	return nil
}

func isJSONNull(data []byte) bool {
	return bytes.Equal(bytes.TrimSpace(data), []byte("null"))
}

func decodeClosed(data []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return fmt.Errorf("invalid closed JSON object: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return err
	}
	return nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("manifest contains more than one JSON value")
		}
		return fmt.Errorf("invalid trailing JSON: %w", err)
	}
	return nil
}

func rejectDuplicateFields(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := walkJSONValue(decoder, "$"); err != nil {
		return err
	}
	return ensureJSONEOF(decoder)
}

func walkJSONValue(decoder *json.Decoder, location string) error {
	token, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("invalid JSON at %s: %w", location, err)
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			fieldToken, fieldErr := decoder.Token()
			if fieldErr != nil {
				return fmt.Errorf("invalid JSON object at %s: %w", location, fieldErr)
			}
			field, fieldOK := fieldToken.(string)
			if !fieldOK {
				return fmt.Errorf("invalid JSON object key at %s", location)
			}
			if _, duplicate := seen[field]; duplicate {
				return fmt.Errorf("duplicate JSON field %q at %s", field, location)
			}
			seen[field] = struct{}{}
			if err := walkJSONValue(decoder, location+"."+field); err != nil {
				return err
			}
		}
		end, endErr := decoder.Token()
		if endErr != nil || end != json.Delim('}') {
			return fmt.Errorf("invalid JSON object at %s", location)
		}
	case '[':
		for i := 0; decoder.More(); i++ {
			if err := walkJSONValue(decoder, fmt.Sprintf("%s[%d]", location, i)); err != nil {
				return err
			}
		}
		end, endErr := decoder.Token()
		if endErr != nil || end != json.Delim(']') {
			return fmt.Errorf("invalid JSON array at %s", location)
		}
	default:
		return fmt.Errorf("invalid JSON delimiter %q at %s", delim, location)
	}
	return nil
}

// SortedCaseIDs returns a deterministic list useful to harness callers.
func (m *Manifest) SortedCaseIDs() []string {
	if m == nil {
		return nil
	}
	ids := make([]string, len(m.Cases))
	for i := range m.Cases {
		ids[i] = m.Cases[i].ID
	}
	sort.Strings(ids)
	return ids
}
