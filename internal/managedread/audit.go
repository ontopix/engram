package managedread

import (
	"bytes"
	"context"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/ontopix/engram/internal/changeset"
	"github.com/ontopix/engram/internal/checker"
	"github.com/ontopix/engram/internal/gitraw"
	"github.com/ontopix/engram/internal/snapshot"
)

// HistoryAudit is one actually evaluated accepted transition, root to tip.
// Base is nil only for the parentless initialization transition.
type HistoryAudit struct {
	Base       *string        `json:"base"`
	Candidate  string         `json:"candidate"`
	Validation checker.Result `json:"validation"`
}

// AcceptedAudit combines the raw Git audit with complete portable snapshot
// and transition checks. Audits never contains an inferred pair whose input
// snapshot was unavailable.
type AcceptedAudit struct {
	Tip        string                       `json:"tip"`
	Validation checker.Result               `json:"validation"`
	Audits     []HistoryAudit               `json:"audits"`
	Raw        *gitraw.Audit                `json:"-"`
	Snapshots  map[string]*checker.Snapshot `json:"-"`
}

// AuditAccepted inspects the complete locally available accepted lineage. It
// never fetches. Missing required objects are returned as capability errors by
// gitraw; E601/E602 causal boundaries remain complete managed results. A
// handle may reuse the immutable lineage analysis only for the exact accepted
// tip and normative rule-set identity; mutable presentation inputs are always
// observed and rechecked for the current call.
func (s *Store) AuditAccepted(ctx context.Context) (result *AcceptedAudit, err error) {
	repository, err := s.observeRepository(ctx)
	if err != nil {
		return nil, err
	}
	inputs := operationInputs{repository: repository}
	defer func() {
		if err != nil {
			return
		}
		if finalErr := s.finishOperation(ctx, operationAudit, inputs); finalErr != nil {
			result = nil
			err = finalErr
		}
	}()
	operationStore := s.atRepository(repository)
	result, err = operationStore.cachedAcceptedAudit(ctx)
	if err != nil {
		return nil, err
	}
	accepted := result.Snapshots[result.Tip]
	presentationFindings, fingerprint, err := operationStore.auditPresentation(ctx, repository, accepted)
	if err != nil {
		return nil, err
	}
	mergeManagedFindings(&result.Validation, presentationFindings)
	inputs.presentation = &presentationObservation{accepted: accepted, fingerprint: fingerprint}
	return result, nil
}

func (s *Store) auditAccepted(ctx context.Context) (*AcceptedAudit, error) {
	if s == nil || s.repository == nil {
		return nil, fmt.Errorf("managedread: nil store")
	}
	raw, err := s.repository.Audit(ctx)
	if err != nil {
		return nil, err
	}
	result := &AcceptedAudit{
		Tip: raw.Tip.String(),
		Validation: checker.Result{
			Target: checker.TargetManagedStore,
			Status: checker.StatusComplete,
		},
		Raw:       raw,
		Snapshots: make(map[string]*checker.Snapshot),
	}
	findings := make(map[[2]string]checker.Finding)
	for _, finding := range raw.Findings {
		detail := strings.Join(finding.Detail, "; ")
		addManagedFinding(findings, checker.Finding{Code: finding.Code, Path: finding.Path, Detail: detail})
	}

	commits := make(map[string]gitraw.AuditedCommit, len(raw.Commits))
	for _, audited := range raw.Commits {
		id := audited.ID.String()
		commits[id] = audited
		if audited.Snapshot == nil {
			continue
		}
		portable, err := checker.CheckSource(newMemorySource(audited.Snapshot))
		if err != nil {
			return nil, err
		}
		result.Snapshots[id] = portable
		for _, finding := range portable.Validation.Findings {
			addManagedFinding(findings, finding)
		}
	}

	for _, audited := range raw.Commits {
		candidateID := audited.ID.String()
		candidate := result.Snapshots[candidateID]
		if candidate == nil || audited.Commit == nil {
			continue
		}
		var base *checker.Snapshot
		var baseID *string
		initialization := len(audited.Commit.Parents) == 0
		if !initialization {
			if len(audited.Commit.Parents) != 1 {
				continue
			}
			parent := audited.Commit.Parents[0].String()
			if _, visited := commits[parent]; !visited {
				continue
			}
			base = result.Snapshots[parent]
			if base == nil {
				continue
			}
			baseID = stringPointer(parent)
		}
		validation, _ := checker.CheckTransition(base, candidate, initialization)
		if validation.Status == checker.StatusIndeterminate {
			result.Validation.Status = checker.StatusIndeterminate
		}
		for _, finding := range validation.Findings {
			addManagedFinding(findings, finding)
		}
		result.Audits = append(result.Audits, HistoryAudit{
			Base:       baseID,
			Candidate:  candidateID,
			Validation: validation,
		})
	}

	result.Validation.Findings = sortedManagedFindings(findings)
	return result, nil
}

// CheckStaged evaluates the index-declared initial candidate against the
// accepted state. A missing accepted commit is the known empty initialization
// base; no worktree bytes participate.
func (s *Store) CheckStaged(ctx context.Context) (validation checker.Result, changes []changeset.Change, err error) {
	repository, err := s.observeRepository(ctx)
	if err != nil {
		return validation, nil, err
	}
	inputs := operationInputs{repository: repository}
	defer func() {
		if err != nil {
			return
		}
		if finalErr := s.finishOperation(ctx, operationCheckStaged, inputs); finalErr != nil {
			validation = checker.Result{}
			changes = nil
			err = finalErr
		}
	}()
	operationStore := s.atRepository(repository)
	entries, err := operationStore.readIndex(ctx, "")
	if err != nil {
		return validation, nil, err
	}
	inputs.index = cloneIndexEntries(entries)
	inputs.hasIndex = true
	base, err := operationStore.Accepted(ctx)
	if err != nil {
		return validation, nil, err
	}
	candidate, err := operationStore.projectStagedEntries(ctx, base, entries)
	if err != nil {
		return validation, nil, err
	}
	initialization := base.Snapshot == nil
	validation, changes = checker.CheckTransition(base.Snapshot, candidate.Snapshot, initialization)
	return validation, changes, nil
}

func addManagedFinding(set map[[2]string]checker.Finding, finding checker.Finding) {
	key := [2]string{finding.Code, finding.Path}
	if existing, found := set[key]; found {
		if existing.Detail == "" && finding.Detail != "" {
			existing.Detail = finding.Detail
			set[key] = existing
		}
		return
	}
	set[key] = finding
}

func sortedManagedFindings(set map[[2]string]checker.Finding) []checker.Finding {
	result := make([]checker.Finding, 0, len(set))
	for _, finding := range set {
		result = append(result, finding)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Path != result[j].Path {
			return bytes.Compare([]byte(result[i].Path), []byte(result[j].Path)) < 0
		}
		return result[i].Code < result[j].Code
	})
	return result
}

func mergeManagedFindings(validation *checker.Result, additional []checker.Finding) {
	if validation == nil || len(additional) == 0 {
		return
	}
	findings := make(map[[2]string]checker.Finding, len(validation.Findings)+len(additional))
	for _, finding := range validation.Findings {
		addManagedFinding(findings, finding)
	}
	for _, finding := range additional {
		addManagedFinding(findings, finding)
	}
	validation.Findings = sortedManagedFindings(findings)
}

type memorySource struct {
	children map[string][]snapshot.Entry
	files    map[string][]byte
}

func newMemorySource(tree *snapshot.Tree) *memorySource {
	source := &memorySource{
		children: map[string][]snapshot.Entry{".": nil},
		files:    make(map[string][]byte, len(tree.Files)),
	}
	for _, directory := range tree.Directories {
		if directory != "." {
			source.children[directory] = nil
		}
	}
	for name, boundaryKind := range tree.Boundaries {
		directory := path.Dir(name)
		if directory == "" {
			directory = "."
		}
		source.children[directory] = append(source.children[directory], snapshot.Entry{
			Name: path.Base(name),
			Kind: boundaryKind,
		})
	}
	for name, file := range tree.Files {
		source.files[name] = append([]byte(nil), file.Data...)
	}
	for directory := range source.children {
		sort.Slice(source.children[directory], func(i, j int) bool {
			return bytes.Compare([]byte(source.children[directory][i].Name), []byte(source.children[directory][j].Name)) < 0
		})
	}
	return source
}

func (s *memorySource) ReadDir(logicalPath string) ([]snapshot.Entry, error) {
	entries, ok := s.children[logicalPath]
	if !ok {
		return nil, fmt.Errorf("unknown projected directory %q", logicalPath)
	}
	return append([]snapshot.Entry(nil), entries...), nil
}

func (s *memorySource) ReadFile(logicalPath string) ([]byte, error) {
	data, ok := s.files[logicalPath]
	if !ok {
		return nil, fmt.Errorf("unknown projected file %q", logicalPath)
	}
	return append([]byte(nil), data...), nil
}
