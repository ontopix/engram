package managedread

import (
	"context"
	"errors"
	"fmt"

	"github.com/ontopix/engram/internal/checker"
	"github.com/ontopix/engram/internal/gitraw"
)

// CheckAcceptedState validates the accepted state named directly by the
// current local branch. It checks the tip commit and its snapshot together
// with the repository's current presentation, but deliberately does not
// resolve or inspect any parent commit.
func (s *Store) CheckAcceptedState(ctx context.Context) (validation checker.Result, err error) {
	validation = checker.Result{
		Target:   checker.TargetManagedState,
		Status:   checker.StatusComplete,
		Findings: []checker.Finding{},
	}
	repository, err := s.observeRepository(ctx)
	if err != nil {
		return validation, err
	}
	inputs := operationInputs{repository: repository}
	defer func() {
		if err != nil {
			return
		}
		if finalErr := s.finishOperation(ctx, operationCheckAcceptedState, inputs); finalErr != nil {
			validation = checker.Result{}
			err = finalErr
		}
	}()

	operationStore := s.atRepository(repository)
	var accepted *checker.Snapshot
	if repository.Head == nil {
		mergeManagedFindings(&validation, []checker.Finding{{
			Code:   "E601",
			Path:   ".",
			Detail: "accepted ref does not contain a commit",
		}})
	} else {
		source, commit, readErr := repository.SnapshotSource(ctx, *repository.Head)
		if readErr != nil {
			if !acceptedShapeFinding(readErr) {
				return validation, readErr
			}
			mergeManagedFindings(&validation, []checker.Finding{{
				Code:   "E601",
				Path:   ".",
				Detail: readErr.Error(),
			}})
		} else if len(commit.Parents) > 1 {
			// The parent list is part of the tip commit itself. A merge is the
			// causal boundary: report it without resolving the tip tree or either
			// parent target.
			mergeManagedFindings(&validation, []checker.Finding{{
				Code:   "E602",
				Path:   ".",
				Detail: "accepted tip commit has more than one parent",
			}})
		} else {
			accepted, readErr = checker.CheckSource(source)
			if readErr != nil {
				if !acceptedShapeFinding(readErr) {
					return validation, readErr
				}
				mergeManagedFindings(&validation, []checker.Finding{{
					Code:   "E601",
					Path:   ".",
					Detail: readErr.Error(),
				}})
				accepted = nil
			} else {
				mergeManagedFindings(&validation, accepted.Validation.Findings)
				if tracked, ok := source.(interface{ PrunedWithoutCoreFinding() []string }); ok {
					if unprojected := tracked.PrunedWithoutCoreFinding(); len(unprojected) != 0 {
						mergeManagedFindings(&validation, []checker.Finding{{
							Code:   "E603",
							Path:   ".",
							Detail: fmt.Sprintf("pruned raw paths: %v", unprojected),
						}})
					}
				}
			}
		}
	}

	presentationFindings, fingerprint, err := operationStore.auditPresentation(ctx, repository, accepted)
	if err != nil {
		return validation, err
	}
	mergeManagedFindings(&validation, presentationFindings)
	inputs.presentation = &presentationObservation{accepted: accepted, fingerprint: fingerprint}
	return validation, nil
}

func acceptedShapeFinding(err error) bool {
	return errors.Is(err, gitraw.ErrMalformedObject) || errors.Is(err, gitraw.ErrWrongObjectType)
}
