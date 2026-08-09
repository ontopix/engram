package gitraw

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"

	"github.com/ontopix/engram/internal/snapshot"
)

type Finding struct {
	Code    string
	Path    string
	Commits []OID
	Detail  []string
}

type AuditedCommit struct {
	ID       OID
	Commit   *Commit
	Snapshot *snapshot.Tree
}

type Audit struct {
	Tip      OID
	Commits  []AuditedCommit // Root to tip for every commit actually inspected.
	Findings []Finding
	Complete bool
}

// SnapshotSource returns a lazy snapshot.Source for a well-formed commit.
func (r *Repository) SnapshotSource(ctx context.Context, id OID) (snapshot.Source, *Commit, error) {
	object, err := r.ReadObject(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	if object.Type != TypeCommit {
		return nil, nil, wrongType("read-commit", id, object.Type, TypeCommit)
	}
	commit, err := ParseCommit(r.Format, object.Data)
	if err != nil {
		return nil, nil, err
	}
	return NewTreeSource(ctx, r, commit.Tree), commit, nil
}

func (r *Repository) Audit(ctx context.Context) (*Audit, error) {
	if r == nil || r.Head == nil {
		return nil, &Error{Kind: FailureRepository, Op: "audit", Detail: "accepted ref does not contain a commit"}
	}
	return AuditLineage(ctx, r, *r.Head)
}

// AuditLineage walks raw parent IDs itself. A merge stops before its tree or
// either parent is resolved; missing required targets remain capability errors.
func AuditLineage(ctx context.Context, reader Reader, tip OID) (*Audit, error) {
	audit := &Audit{Tip: tip}
	seen := make(map[OID]struct{})
	findings := make(map[string]*Finding)
	var availability error

	current := tip
	for {
		if _, duplicate := seen[current]; duplicate {
			addFinding(findings, "E601", current, "raw parent cycle")
			break
		}
		seen[current] = struct{}{}
		object, err := reader.ReadObject(ctx, current)
		if err != nil {
			if errors.Is(err, ErrMissingObject) {
				availability = err
				break
			}
			if errors.Is(err, ErrMalformedObject) || errors.Is(err, ErrWrongObjectType) {
				addFinding(findings, "E601", current, err.Error())
				break
			}
			return audit, err
		}
		if object.Type != TypeCommit {
			addFinding(findings, "E601", current, fmt.Sprintf("accepted parent target has type %s", object.Type))
			break
		}
		commit, parseErr := ParseCommit(current.Format(), object.Data)
		if parseErr != nil {
			addFinding(findings, "E601", current, parseErr.Error())
			break
		}
		audited := AuditedCommit{ID: current, Commit: commit}
		audit.Commits = append(audit.Commits, audited)

		if len(commit.Parents) > 1 {
			addFinding(findings, "E602", current, "commit has more than one parent")
			break
		}

		source := NewTreeSource(ctx, reader, commit.Tree)
		projected, projectionErr := snapshot.Load(source)
		if projectionErr != nil {
			switch {
			case errors.Is(projectionErr, ErrMissingObject):
				if availability == nil {
					availability = projectionErr
				}
			case errors.Is(projectionErr, ErrMalformedObject), errors.Is(projectionErr, ErrWrongObjectType):
				addFinding(findings, "E601", current, projectionErr.Error())
			default:
				return audit, projectionErr
			}
		} else {
			audit.Commits[len(audit.Commits)-1].Snapshot = projected
			pruned := source.PrunedWithoutCoreFinding()
			if len(pruned) != 0 {
				addFinding(findings, "E603", current, fmt.Sprintf("pruned raw paths: %v", pruned))
			}
		}

		if len(commit.Parents) == 0 {
			break
		}
		current = commit.Parents[0]
	}

	slices.Reverse(audit.Commits)
	audit.Findings = make([]Finding, 0, len(findings))
	for _, finding := range findings {
		audit.Findings = append(audit.Findings, *finding)
	}
	sort.Slice(audit.Findings, func(i, j int) bool {
		if audit.Findings[i].Path != audit.Findings[j].Path {
			return audit.Findings[i].Path < audit.Findings[j].Path
		}
		return audit.Findings[i].Code < audit.Findings[j].Code
	})
	audit.Complete = availability == nil
	if availability != nil {
		return audit, availability
	}
	return audit, nil
}

func addFinding(findings map[string]*Finding, code string, commit OID, detail string) {
	key := code + "\x00."
	finding, ok := findings[key]
	if !ok {
		finding = &Finding{Code: code, Path: "."}
		findings[key] = finding
	}
	finding.Commits = append(finding.Commits, commit)
	if detail != "" {
		finding.Detail = append(finding.Detail, detail)
	}
}
