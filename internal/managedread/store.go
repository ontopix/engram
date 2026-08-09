package managedread

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/ontopix/engram/internal/checker"
	"github.com/ontopix/engram/internal/gitraw"
)

// Store is a read-only handle to the exact root of one managed repository
// worktree. It owns no locks and performs no fetches or mutations.
type Store struct {
	repository *gitraw.Repository
	git        string

	// acceptedAudits is shared by the short-lived repository observations made
	// from this handle. Only the immutable lineage/portable audit is reusable;
	// presentation inputs are deliberately observed on every public operation.
	acceptedAudits *acceptedAuditCache
	ruleSetID      string
	auditLoader    acceptedAuditLoader

	// afterCapture is an internal deterministic fault seam. Production stores
	// leave it nil; tests can mutate an observed input before final recheck.
	afterCapture func(operation string)
}

// Open discovers the repository through gitraw and requires selectedPath to
// be the worktree root itself, not merely a path somewhere below it.
func Open(ctx context.Context, selectedPath string) (*Store, error) {
	repository, err := gitraw.Discover(ctx, selectedPath)
	if err != nil {
		return nil, err
	}
	absolute, err := filepath.Abs(selectedPath)
	if err != nil {
		return nil, &gitraw.Error{Kind: gitraw.FailureRepository, Op: "open-managed-store", Err: err}
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		return nil, &gitraw.Error{Kind: gitraw.FailureRepository, Op: "open-managed-store", Err: err}
	}
	rootInfo, rootErr := os.Stat(repository.Root)
	if rootErr != nil {
		return nil, &gitraw.Error{Kind: gitraw.FailureRepository, Op: "open-managed-store", Err: rootErr}
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || !os.SameFile(info, rootInfo) {
		return nil, &gitraw.Error{
			Kind:   gitraw.FailureRepository,
			Op:     "open-managed-store",
			Detail: "selected path is not exactly the repository worktree root",
		}
	}
	git, err := exec.LookPath("git")
	if err != nil {
		return nil, &gitraw.Error{Kind: gitraw.FailureCapability, Op: "locate-git", Err: err}
	}
	return &Store{
		repository:     repository,
		git:            git,
		acceptedAudits: newAcceptedAuditCache(),
		ruleSetID:      acceptedAuditRuleSetIdentity,
	}, nil
}

// Repository exposes the immutable discovery result used by this handle.
func (s *Store) Repository() *gitraw.Repository {
	if s == nil {
		return nil
	}
	return s.repository
}

// Accepted projects the exact tree of the commit directly named by the
// accepted ref. An unborn branch returns a view with a nil Snapshot.
func (s *Store) Accepted(ctx context.Context) (*SnapshotView, error) {
	if s == nil || s.repository == nil {
		return nil, fmt.Errorf("managedread: nil store")
	}
	state := GitState{Ref: stringPointer(s.repository.HeadRef)}
	if s.repository.Head == nil {
		return &SnapshotView{State: state, Modes: make(map[string]gitraw.TreeMode)}, nil
	}
	id := *s.repository.Head
	state.Commit = stringPointer(id.String())
	source, commit, err := s.repository.SnapshotSource(ctx, id)
	if err != nil {
		return nil, err
	}
	analysis, err := checker.CheckSource(source)
	if err != nil {
		return nil, err
	}
	var unprojected []string
	if tracked, ok := source.(interface{ PrunedWithoutCoreFinding() []string }); ok {
		unprojected = tracked.PrunedWithoutCoreFinding()
	}
	modes, err := logicalRegularModes(ctx, s.repository, commit.Tree, analysis)
	if err != nil {
		return nil, err
	}
	return &SnapshotView{
		State:       state,
		Snapshot:    analysis,
		Modes:       modes,
		oid:         &id,
		commit:      commit,
		unprojected: append([]string(nil), unprojected...),
	}, nil
}

// Staged projects the complete logical initial candidate declared by the
// resolved real index. It rejects conflicts, intent-to-add, path collisions,
// and candidate mode violations without choosing or repairing an entry.
func (s *Store) Staged(ctx context.Context) (*SnapshotView, error) {
	return s.projectStaged(ctx, "")
}

// StagedFromIndex projects and validates one explicit alternate Git index.
// The path must identify an existing real regular file by absolute clean path;
// the ambient environment and the repository's live index remain excluded.
func (s *Store) StagedFromIndex(ctx context.Context, absoluteIndexPath string) (*SnapshotView, error) {
	if !filepath.IsAbs(absoluteIndexPath) || filepath.Clean(absoluteIndexPath) != absoluteIndexPath {
		return nil, &IndexError{Kind: IndexMalformed, Detail: "alternate index path must be absolute and clean"}
	}
	info, err := os.Lstat(absoluteIndexPath)
	if err != nil {
		return nil, &IndexError{Kind: IndexMalformed, Detail: "cannot inspect alternate index path", Err: err}
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, &IndexError{Kind: IndexMalformed, Detail: "alternate index path is not a real regular file"}
	}
	return s.projectStaged(ctx, absoluteIndexPath)
}

func (s *Store) projectStaged(ctx context.Context, absoluteIndexPath string) (*SnapshotView, error) {
	base, err := s.Accepted(ctx)
	if err != nil {
		return nil, err
	}
	entries, err := s.readIndex(ctx, absoluteIndexPath)
	if err != nil {
		return nil, err
	}
	return s.projectStagedEntries(ctx, base, entries)
}

func (s *Store) projectStagedEntries(ctx context.Context, base *SnapshotView, entries []IndexEntry) (*SnapshotView, error) {
	resolved, err := resolveIndex(entries)
	if err != nil {
		return nil, err
	}
	if pruned := prunedIndexPaths(resolved); len(pruned) != 0 {
		return nil, &IndexError{
			Kind:   IndexConflict,
			Paths:  pruned,
			Detail: "index entries would be pruned without a core boundary finding",
		}
	}
	source, err := newIndexSource(ctx, s.repository, resolved)
	if err != nil {
		return nil, err
	}
	analysis, err := checker.CheckSource(source)
	if err != nil {
		return nil, err
	}
	if err := validateCandidateModes(base.Modes, resolved, analysis); err != nil {
		return nil, err
	}
	modes := make(map[string]gitraw.TreeMode)
	for path := range analysis.Tree.Files {
		modes[path] = resolved[path].Mode
	}
	return &SnapshotView{
		Snapshot: analysis,
		Index:    cloneIndexEntries(entries),
		Modes:    modes,
	}, nil
}

// Working projects the current filesystem working draft. Git administration
// and other pruned state are excluded by the portable snapshot boundary.
func (s *Store) Working(ctx context.Context) (*SnapshotView, error) {
	if s == nil || s.repository == nil {
		return nil, fmt.Errorf("managedread: nil store")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	analysis, err := checker.CheckFS(s.repository.Root)
	if err != nil {
		return nil, err
	}
	return &SnapshotView{Snapshot: analysis, Modes: make(map[string]gitraw.TreeMode)}, nil
}
