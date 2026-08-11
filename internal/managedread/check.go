package managedread

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ontopix/engram/internal/checker"
	"github.com/ontopix/engram/internal/gitraw"
)

// CheckAccepted performs tolerant read-only discovery for the managed check.
// Unlike Open, it turns a stably invalid managed target, HEAD, or accepted ref
// into the normative E601 result. Writers continue to use strict Open and
// therefore never receive a Store from invalid topology.
func CheckAccepted(ctx context.Context, selectedPath string) (checker.Result, error) {
	validation := checker.Result{
		Target:   checker.TargetManagedStore,
		Status:   checker.StatusComplete,
		Findings: []checker.Finding{},
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if selectedPath == "" {
		return validation, fmt.Errorf("managed check target is empty")
	}

	topology, err := gitraw.DiscoverTopology(ctx, selectedPath)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return validation, err
		}
		if invalidManagedTopology(err) {
			validation.Findings = []checker.Finding{{Code: "E601", Path: ".", Detail: "managed target is not the exact root of a non-bare Git worktree"}}
			return validation, nil
		}
		return validation, err
	}

	selectedFinding, err := selectedRootFinding(selectedPath, topology.Root)
	if err != nil {
		return validation, err
	}
	repository, err := gitraw.Discover(ctx, selectedPath)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return validation, err
		}
		if invalidAcceptedIdentity(err) {
			validation.Findings = append(validation.Findings, selectedFinding...)
			mergeManagedFindings(&validation, []checker.Finding{{Code: "E601", Path: ".", Detail: "HEAD or its accepted local branch is structurally invalid"}})
			return validation, nil
		}
		return validation, err
	}
	if repository.Root != topology.Root || repository.GitDir != topology.GitDir ||
		repository.CommonGitDir != topology.CommonGitDir || repository.Format != topology.Format {
		return validation, &ConcurrencyError{Operation: "check accepted", Inputs: []string{"repository topology"}}
	}
	store, err := newStore(repository)
	if err != nil {
		return validation, err
	}
	if repository.Head == nil {
		// An unborn symbolic local branch is the annex's one admitted empty
		// accepted state. Presentation inputs are still checked.
		findings, _, err := store.auditPresentation(ctx, repository, nil)
		if err != nil {
			return validation, err
		}
		validation.Findings = append(validation.Findings, findings...)
	} else {
		audit, err := store.AuditAccepted(ctx)
		if err != nil {
			return validation, err
		}
		validation = audit.Validation
	}
	mergeManagedFindings(&validation, selectedFinding)
	return validation, nil
}

func invalidManagedTopology(err error) bool {
	var raw *gitraw.Error
	return errors.As(err, &raw) && raw.Kind == gitraw.FailureRepository
}

func invalidAcceptedIdentity(err error) bool {
	var raw *gitraw.Error
	if !errors.As(err, &raw) || raw.Op != "discover-head" {
		return false
	}
	if strings.Contains(raw.Detail, "changed while being resolved") {
		return false
	}
	switch raw.Kind {
	case gitraw.FailureRepository, gitraw.FailureGit, gitraw.FailureMalformed, gitraw.FailureWrongType:
		return true
	default:
		return false
	}
}

func selectedRootFinding(selectedPath, repositoryRoot string) ([]checker.Finding, error) {
	absolute, err := filepath.Abs(selectedPath)
	if err != nil {
		return nil, err
	}
	selected, err := os.Lstat(absolute)
	if err != nil {
		return nil, err
	}
	root, err := os.Stat(repositoryRoot)
	if err != nil {
		return nil, err
	}
	if selected.Mode()&os.ModeSymlink == 0 && selected.IsDir() && os.SameFile(selected, root) {
		return nil, nil
	}
	return []checker.Finding{{
		Code:   "E601",
		Path:   ".",
		Detail: "managed check target is not exactly the repository worktree root",
	}}, nil
}
