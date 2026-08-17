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

// CheckAcceptedState performs tolerant read-only discovery and validates only
// the accepted state named by the current branch. Unlike Open, it turns a
// stably invalid managed target, HEAD, or accepted ref into the normative E601
// result. It never follows the accepted tip's parent IDs.
func CheckAcceptedState(ctx context.Context, selectedPath string) (checker.Result, error) {
	return checkAccepted(ctx, selectedPath, checker.TargetManagedState,
		func(ctx context.Context, store *Store, _ *gitraw.Repository) (checker.Result, error) {
			return store.CheckAcceptedState(ctx)
		})
}

// CheckAcceptedHistory performs the complete root-to-tip managed-store audit
// while retaining tolerant attribution for an invalid selected target.
func CheckAcceptedHistory(ctx context.Context, selectedPath string) (checker.Result, error) {
	return checkAccepted(ctx, selectedPath, checker.TargetManagedStore,
		func(ctx context.Context, store *Store, repository *gitraw.Repository) (checker.Result, error) {
			if repository.Head == nil {
				validation, err := store.CheckAcceptedState(ctx)
				if err != nil {
					return validation, err
				}
				validation.Target = checker.TargetManagedStore
				return validation, nil
			}
			audit, err := store.AuditAccepted(ctx)
			if err != nil {
				return checker.Result{}, err
			}
			return audit.Validation, nil
		})
}

// CheckAccepted retains the pre-split API for callers that explicitly depend
// on a complete managed-store audit. New state-only callers should use
// CheckAcceptedState.
func CheckAccepted(ctx context.Context, selectedPath string) (checker.Result, error) {
	return CheckAcceptedHistory(ctx, selectedPath)
}

type acceptedCheck func(context.Context, *Store, *gitraw.Repository) (checker.Result, error)

func checkAccepted(ctx context.Context, selectedPath string, target checker.Target, check acceptedCheck) (checker.Result, error) {
	validation := checker.Result{
		Target:   target,
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
	validation, err = check(ctx, store, repository)
	if err != nil {
		return validation, err
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
