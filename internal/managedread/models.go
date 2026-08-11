package managedread

import (
	"bytes"
	"context"
	"fmt"
	"sort"

	"github.com/ontopix/engram/internal/changeset"
	"github.com/ontopix/engram/internal/checker"
	"github.com/ontopix/engram/internal/gitraw"
	"github.com/ontopix/engram/internal/snapshot"
)

type StatusMode string

const (
	StatusNormal     StatusMode = "normal"
	StatusPullReplay StatusMode = "pull-replay"
)

// ReplayState is reserved for the M5 pull-replay reader. M2 Status always
// returns nil Replay and mode normal, while keeping the protocol model closed.
type ReplayState struct {
	Original  GitState `json:"original"`
	Private   GitState `json:"private"`
	Base      GitState `json:"base"`
	Reason    string   `json:"reason"`
	Conflicts []string `json:"conflicts"`
}

// StatusResult is the deterministic status protocol payload.
type StatusResult struct {
	Mode          StatusMode         `json:"mode"`
	Accepted      GitState           `json:"accepted"`
	CandidateBase GitState           `json:"candidate_base"`
	Staged        []changeset.Change `json:"staged"`
	Unstaged      []changeset.Change `json:"unstaged"`
	Replay        *ReplayState       `json:"replay"`
}

// Status compares accepted -> index and index -> working without consulting
// Git's porcelain diff or allowing one invalid boundary to yield partial data.
func (s *Store) Status(ctx context.Context) (result *StatusResult, err error) {
	repository, err := s.observeRepository(ctx)
	if err != nil {
		return nil, err
	}
	inputs := operationInputs{repository: repository}
	defer func() {
		if err != nil {
			return
		}
		if finalErr := s.finishOperation(ctx, operationStatus, inputs); finalErr != nil {
			result = nil
			err = finalErr
		}
	}()
	operationStore := s.atRepository(repository)
	entries, err := operationStore.readIndex(ctx, "")
	if err != nil {
		return nil, err
	}
	inputs.index = cloneIndexEntries(entries)
	inputs.hasIndex = true
	working, err := operationStore.Working(ctx)
	if err != nil {
		return nil, err
	}
	inputs.working = working
	accepted, err := operationStore.Accepted(ctx)
	if err != nil {
		return nil, err
	}
	staged, err := operationStore.projectStagedEntries(ctx, accepted, entries)
	if err != nil {
		return nil, err
	}
	if err := requirePreflight(accepted, normalizedAcceptedSelector(accepted)); err != nil {
		return nil, err
	}
	if err := requirePreflight(staged, IndexSelector()); err != nil {
		return nil, err
	}
	if err := requirePreflight(working, WorkingSelector()); err != nil {
		return nil, err
	}
	result = &StatusResult{
		Mode:          StatusNormal,
		Accepted:      cloneGitState(accepted.State),
		CandidateBase: cloneGitState(accepted.State),
		Staged:        changeset.Diff(snapshotTree(accepted), snapshotTree(staged)),
		Unstaged:      changeset.Diff(snapshotTree(staged), snapshotTree(working)),
	}
	return result, nil
}

// ChangeStat contains the complete logical change counts.
type ChangeStat struct {
	Added    int `json:"added"`
	Modified int `json:"modified"`
	Deleted  int `json:"deleted"`
}

// DiffResult is the deterministic diff protocol payload.
type DiffResult struct {
	From    StateSelector      `json:"from"`
	To      StateSelector      `json:"to"`
	Changes []changeset.Change `json:"changes"`
	Stat    ChangeStat         `json:"stat"`

	// textFiles retains the byte-exact changed inputs needed by the optional
	// human content renderer. It is not part of the protocol result.
	textFiles []diffTextFile
}

type diffTextFile struct {
	path          string
	before        []byte
	after         []byte
	beforePresent bool
	afterPresent  bool
}

// Diff resolves both selectors, requires complete boundary-safe projections,
// and returns one byte-oriented logical changeset.
func (s *Store) Diff(ctx context.Context, from, to StateSelector) (result *DiffResult, err error) {
	repository, err := s.observeRepository(ctx)
	if err != nil {
		return nil, err
	}
	inputs := operationInputs{repository: repository}
	defer func() {
		if err != nil {
			return
		}
		if finalErr := s.finishOperation(ctx, operationDiff, inputs); finalErr != nil {
			result = nil
			err = finalErr
		}
	}()
	operationStore := s.atRepository(repository)
	resolver := projectionResolver{store: operationStore, ctx: ctx}
	if selectorUsesIndex(from) || selectorUsesIndex(to) {
		entries, readErr := operationStore.readIndex(ctx, "")
		if readErr != nil {
			return nil, readErr
		}
		inputs.index = cloneIndexEntries(entries)
		inputs.hasIndex = true
		accepted, readErr := operationStore.Accepted(ctx)
		if readErr != nil {
			return nil, readErr
		}
		staged, readErr := operationStore.projectStagedEntries(ctx, accepted, entries)
		if readErr != nil {
			return nil, readErr
		}
		resolver.accepted = accepted
		resolver.staged = staged
	}
	if selectorUsesWorking(from) || selectorUsesWorking(to) {
		working, readErr := operationStore.Working(ctx)
		if readErr != nil {
			return nil, readErr
		}
		inputs.working = working
		resolver.working = working
	}
	fromView, normalizedFrom, err := resolver.resolve(from)
	if err != nil {
		return nil, err
	}
	toView, normalizedTo, err := resolver.resolve(to)
	if err != nil {
		return nil, err
	}
	if err := requirePreflight(fromView, normalizedFrom); err != nil {
		return nil, err
	}
	if err := requirePreflight(toView, normalizedTo); err != nil {
		return nil, err
	}
	changes := changeset.Diff(snapshotTree(fromView), snapshotTree(toView))
	result = &DiffResult{
		From:      normalizedFrom,
		To:        normalizedTo,
		Changes:   changes,
		Stat:      changeStat(changes),
		textFiles: diffTextFiles(snapshotTree(fromView), snapshotTree(toView), changes),
	}
	return result, nil
}

func diffTextFiles(before, after *snapshot.Tree, changes []changeset.Change) []diffTextFile {
	result := make([]diffTextFile, 0, len(changes))
	for _, change := range changes {
		file := diffTextFile{path: change.Path}
		if before != nil {
			if value, present := before.Files[change.Path]; present {
				file.beforePresent = true
				file.before = append([]byte(nil), value.Data...)
			}
		}
		if after != nil {
			if value, present := after.Files[change.Path]; present {
				file.afterPresent = true
				file.after = append([]byte(nil), value.Data...)
			}
		}
		result = append(result, file)
	}
	return result
}

func selectorUsesIndex(selector StateSelector) bool {
	return selector.Kind == SelectorIndex
}

func selectorUsesWorking(selector StateSelector) bool {
	return selector.Kind == SelectorWorking
}

// DiffWorking is the no-revision CLI default: index to working draft.
func (s *Store) DiffWorking(ctx context.Context) (*DiffResult, error) {
	return s.Diff(ctx, IndexSelector(), WorkingSelector())
}

// DiffStaged is the --staged/--cached form: accepted to index.
func (s *Store) DiffStaged(ctx context.Context) (*DiffResult, error) {
	return s.Diff(ctx, AcceptedSelector(), IndexSelector())
}

// ResolveRevision normalizes HEAD or verifies one full lowercase object ID is
// a snapshot-bearing commit in the inspected current accepted lineage.
func (s *Store) ResolveRevision(ctx context.Context, value string) (StateSelector, error) {
	resolver := projectionResolver{store: s, ctx: ctx}
	_, selector, err := resolver.revision(value)
	return selector, err
}

type projectionResolver struct {
	store *Store
	ctx   context.Context

	accepted  *SnapshotView
	staged    *SnapshotView
	working   *SnapshotView
	rawAudit  *gitraw.Audit
	revisions map[string]*SnapshotView
}

func (r *projectionResolver) resolve(selector StateSelector) (*SnapshotView, StateSelector, error) {
	switch selector.Kind {
	case SelectorAccepted:
		if selector.Value != nil {
			return nil, StateSelector{}, &RevisionError{Value: *selector.Value, Detail: "accepted selector must not carry a value"}
		}
		if r.accepted == nil {
			value, err := r.store.Accepted(r.ctx)
			if err != nil {
				return nil, StateSelector{}, err
			}
			r.accepted = value
		}
		return r.accepted, normalizedAcceptedSelector(r.accepted), nil
	case SelectorIndex:
		if selector.Value != nil {
			return nil, StateSelector{}, &RevisionError{Value: *selector.Value, Detail: "index selector must not carry a value"}
		}
		if r.staged == nil {
			value, err := r.store.Staged(r.ctx)
			if err != nil {
				return nil, StateSelector{}, err
			}
			r.staged = value
		}
		return r.staged, IndexSelector(), nil
	case SelectorWorking:
		if selector.Value != nil {
			return nil, StateSelector{}, &RevisionError{Value: *selector.Value, Detail: "working selector must not carry a value"}
		}
		if r.working == nil {
			value, err := r.store.Working(r.ctx)
			if err != nil {
				return nil, StateSelector{}, err
			}
			r.working = value
		}
		return r.working, WorkingSelector(), nil
	case SelectorRevision:
		if selector.Value == nil {
			return nil, StateSelector{}, &RevisionError{Detail: "revision selector requires a value"}
		}
		return r.revision(*selector.Value)
	default:
		return nil, StateSelector{}, &RevisionError{Detail: fmt.Sprintf("unknown selector kind %q", selector.Kind)}
	}
}

func (r *projectionResolver) revision(value string) (*SnapshotView, StateSelector, error) {
	if r.revisions == nil {
		r.revisions = make(map[string]*SnapshotView)
	}
	if value == "HEAD" {
		if r.accepted == nil {
			accepted, err := r.store.Accepted(r.ctx)
			if err != nil {
				return nil, StateSelector{}, err
			}
			r.accepted = accepted
		}
		if r.accepted.State.Commit == nil {
			return nil, StateSelector{}, &RevisionError{Value: value, Detail: "accepted branch has no commit"}
		}
		value = *r.accepted.State.Commit
	}
	if cached := r.revisions[value]; cached != nil {
		return cached, StateSelector{Kind: SelectorRevision, Value: stringPointer(value)}, nil
	}
	oid, err := gitraw.ParseOID(r.store.repository.Format, value)
	if err != nil {
		return nil, StateSelector{}, &RevisionError{Value: value, Detail: "expected one full lowercase object ID at repository width"}
	}
	if r.rawAudit == nil {
		audit, err := r.store.repository.Audit(r.ctx)
		if err != nil {
			return nil, StateSelector{}, err
		}
		r.rawAudit = audit
	}
	var audited *gitraw.AuditedCommit
	for index := range r.rawAudit.Commits {
		if r.rawAudit.Commits[index].ID.Equal(oid) {
			audited = &r.rawAudit.Commits[index]
			break
		}
	}
	if audited == nil || audited.Snapshot == nil {
		return nil, StateSelector{}, &RevisionError{Value: value, Detail: "object is not a snapshot-bearing commit in the audited current accepted lineage"}
	}
	portable, err := checker.CheckSource(newMemorySource(audited.Snapshot))
	if err != nil {
		return nil, StateSelector{}, err
	}
	copyOID := oid
	view := &SnapshotView{
		State:    GitState{Commit: stringPointer(value)},
		Snapshot: portable,
		Modes:    make(map[string]gitraw.TreeMode),
		oid:      &copyOID,
		commit:   audited.Commit,
	}
	r.revisions[value] = view
	return view, StateSelector{Kind: SelectorRevision, Value: stringPointer(value)}, nil
}

func requirePreflight(view *SnapshotView, selector StateSelector) error {
	if view == nil || view.Snapshot == nil {
		// The absent accepted state is the only valid empty projection.
		if selector.Kind == SelectorAccepted {
			return nil
		}
		return &BoundaryError{Selector: selector, Issues: []string{"projection is unavailable"}}
	}
	if changeset.PreflightOK(view.Snapshot.Tree) && len(view.unprojected) == 0 {
		return nil
	}
	issues := make([]string, 0, len(view.Snapshot.Tree.Issues)+len(view.unprojected))
	for _, issue := range view.Snapshot.Tree.Issues {
		issues = append(issues, issue.Code+":"+issue.Path)
	}
	for _, name := range view.unprojected {
		issues = append(issues, "unprojected:"+name)
	}
	sort.Slice(issues, func(i, j int) bool { return bytes.Compare([]byte(issues[i]), []byte(issues[j])) < 0 })
	return &BoundaryError{Selector: selector, Issues: issues}
}

func normalizedAcceptedSelector(view *SnapshotView) StateSelector {
	selector := AcceptedSelector()
	if view != nil && view.State.Commit != nil {
		selector.Value = stringPointer(*view.State.Commit)
	}
	return selector
}

func snapshotTree(view *SnapshotView) *snapshot.Tree {
	if view == nil || view.Snapshot == nil {
		return nil
	}
	return view.Snapshot.Tree
}

func cloneGitState(value GitState) GitState {
	result := GitState{}
	if value.Ref != nil {
		result.Ref = stringPointer(*value.Ref)
	}
	if value.Commit != nil {
		result.Commit = stringPointer(*value.Commit)
	}
	return result
}

func changeStat(changes []changeset.Change) ChangeStat {
	result := ChangeStat{}
	for _, change := range changes {
		switch change.Operation {
		case changeset.Added:
			result.Added++
		case changeset.Modified:
			result.Modified++
		case changeset.Deleted:
			result.Deleted++
		}
	}
	return result
}
