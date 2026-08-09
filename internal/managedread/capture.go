package managedread

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"

	"github.com/ontopix/engram/internal/gitraw"
)

const (
	operationStatus      = "status"
	operationDiff        = "diff"
	operationCheckStaged = "check-staged"
	operationAudit       = "audit-accepted"
)

type operationInputs struct {
	repository   *gitraw.Repository
	index        []IndexEntry
	indexPath    string
	hasIndex     bool
	working      *SnapshotView
	presentation *presentationObservation
}

func (s *Store) observeRepository(ctx context.Context) (*gitraw.Repository, error) {
	if s == nil || s.repository == nil {
		return nil, fmt.Errorf("managedread: nil store")
	}
	repository, err := gitraw.Discover(ctx, s.repository.Root)
	if err != nil {
		return nil, err
	}
	if !sameRepositoryTopology(s.repository, repository) {
		return nil, concurrencyFailure("observe", []string{"repository"}, nil)
	}
	return repository, nil
}

func (s *Store) atRepository(repository *gitraw.Repository) *Store {
	return &Store{
		repository:     repository,
		git:            s.git,
		acceptedAudits: s.acceptedAudits,
		ruleSetID:      s.ruleSetID,
		auditLoader:    s.auditLoader,
	}
}

func (s *Store) finishOperation(ctx context.Context, operation string, before operationInputs) error {
	// This is the second semantic collect for an optimistic read snapshot. It
	// proves a coherent point-in-time result when paired with the first collect;
	// it is deliberately not the raw-index capture, lock, or CAS required of a
	// managed writer, and like Git's own compare model it cannot expose ABA.
	if s.afterCapture != nil {
		s.afterCapture(operation)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	after, err := gitraw.Discover(ctx, before.repository.Root)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return concurrencyFailure(operation, []string{"HEAD/ref"}, err)
	}
	changed := make([]string, 0, 4)
	if !sameRepositoryTopology(before.repository, after) {
		changed = append(changed, "repository")
	} else if before.repository.HeadRef != after.HeadRef || !sameOID(before.repository.Head, after.Head) {
		changed = append(changed, "HEAD/ref")
	}

	probe := s.atRepository(after)
	if before.hasIndex {
		entries, readErr := probe.readIndex(ctx, before.indexPath)
		if readErr != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return concurrencyFailure(operation, []string{"index"}, readErr)
		}
		if !reflect.DeepEqual(before.index, entries) {
			changed = append(changed, "index")
		}
	}
	if before.working != nil {
		working, readErr := probe.Working(ctx)
		if readErr != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return concurrencyFailure(operation, []string{"working"}, readErr)
		}
		if !reflect.DeepEqual(snapshotTree(before.working), snapshotTree(working)) {
			changed = append(changed, "working")
		}
	}
	if before.presentation != nil {
		_, fingerprint, readErr := probe.auditPresentation(ctx, after, before.presentation.accepted)
		if readErr != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return concurrencyFailure(operation, []string{"presentation"}, readErr)
		}
		if !before.presentation.fingerprint.Equal(fingerprint) {
			changed = append(changed, "presentation")
		}
	}
	if len(changed) != 0 {
		return concurrencyFailure(operation, changed, nil)
	}
	return nil
}

func sameRepositoryTopology(left, right *gitraw.Repository) bool {
	return left != nil && right != nil &&
		left.Root == right.Root &&
		left.GitDir == right.GitDir &&
		left.CommonGitDir == right.CommonGitDir &&
		left.Format == right.Format
}

func sameOID(left, right *gitraw.OID) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Equal(*right)
}

func concurrencyFailure(operation string, inputs []string, err error) error {
	set := make(map[string]struct{}, len(inputs))
	for _, input := range inputs {
		if input != "" {
			set[input] = struct{}{}
		}
	}
	ordered := make([]string, 0, len(set))
	for input := range set {
		ordered = append(ordered, input)
	}
	sort.Slice(ordered, func(i, j int) bool {
		return bytes.Compare([]byte(ordered[i]), []byte(ordered[j])) < 0
	})
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return &ConcurrencyError{Operation: operation, Inputs: ordered, Err: err}
}
