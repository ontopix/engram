// Package staging implements the literal, logical `add` helper. It constructs
// and validates an alternate complete index before publishing one native
// index-lock replacement; accepted refs and working-draft bytes never move.
package staging

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/ontopix/engram/internal/changeset"
	"github.com/ontopix/engram/internal/checker"
	"github.com/ontopix/engram/internal/fileidentity"
	"github.com/ontopix/engram/internal/gitraw"
	"github.com/ontopix/engram/internal/managedread"
	"github.com/ontopix/engram/internal/rendezvous"
	"github.com/ontopix/engram/internal/snapshot"
)

var ErrSelection = errors.New("invalid add selection")
var ErrConcurrent = errors.New("add input changed concurrently")

// Mutation is the closed local effect set known after an add failure. Add
// never changes refs, HEAD, worktree bytes, or remotes; index replacement is
// reported through CheckoutChanged.
type Mutation struct {
	Durable          bool
	CheckoutChanged  bool
	RecoveryRequired bool
}

// Error preserves ordinary error identity while carrying mutation evidence.
type Error struct {
	Operation string
	Err       error
	Mutation  *Mutation
}

func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Err == nil {
		return e.Operation
	}
	if e.Operation == "" {
		return e.Err.Error()
	}
	return e.Operation + ": " + e.Err.Error()
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// MutationOf merges effect evidence across joined or wrapped staging errors.
// RecoveryRequired is the final snapshot: an outer mutation overrides its
// causes and the last evidence-bearing joined error overrides earlier ones.
func MutationOf(err error) (Mutation, bool) {
	var visit func(error) (Mutation, bool)
	visit = func(current error) (Mutation, bool) {
		if current == nil {
			return Mutation{}, false
		}
		if typedError, ok := current.(*Error); ok && typedError.Mutation != nil {
			result := *typedError.Mutation
			if nested, present := visit(typedError.Err); present {
				result.Durable = result.Durable || nested.Durable
				result.CheckoutChanged = result.CheckoutChanged || nested.CheckoutChanged
			}
			return result, true
		}
		switch unwrapped := current.(type) {
		case interface{ Unwrap() []error }:
			result := Mutation{}
			present := false
			for _, child := range unwrapped.Unwrap() {
				childMutation, childPresent := visit(child)
				if !childPresent {
					continue
				}
				result.Durable = result.Durable || childMutation.Durable
				result.CheckoutChanged = result.CheckoutChanged || childMutation.CheckoutChanged
				result.RecoveryRequired = childMutation.RecoveryRequired
				present = true
			}
			return result, present
		case interface{ Unwrap() error }:
			return visit(unwrapped.Unwrap())
		default:
			return Mutation{}, false
		}
	}
	return visit(err)
}

func mutationError(operation string, err error, mutation Mutation) error {
	if err == nil {
		err = errors.New("unknown staging mutation failure")
	}
	return &Error{Operation: operation, Err: err, Mutation: &mutation}
}

// Phase identifies an instance-local fault boundary reached immediately after
// the named publication step completes.
type Phase string

const (
	PhaseIndexRenamed     Phase = "index-renamed"
	PhaseIndexSynced      Phase = "index-synced"
	PhaseWorktreeReleased Phase = "worktree-released"
)

type worktreeHandle interface {
	Release() error
}

// Adder owns per-instance fault and rendezvous seams. Package-level Add uses a
// fresh default instance, so parallel tests never share mutable fault state.
type Adder struct {
	Fault              func(Phase) error
	acquire            func(string) (worktreeHandle, error)
	removePath         func(string) (bool, error)
	renamePath         func(string, string) (bool, error)
	syncIndexDirectory func(string) (bool, error)
}

func New() *Adder { return &Adder{} }

func (a *Adder) checkpoint(phase Phase) error {
	if a == nil || a.Fault == nil {
		return nil
	}
	return a.Fault(phase)
}

func (a *Adder) acquireWorktree(gitDirectory string) (worktreeHandle, error) {
	if a != nil && a.acquire != nil {
		return a.acquire(gitDirectory)
	}
	return rendezvous.AcquireWorktree(gitDirectory)
}

func (a *Adder) remove(name string) (bool, error) {
	if a != nil && a.removePath != nil {
		return a.removePath(name)
	}
	err := os.Remove(name)
	return err == nil, err
}

func (a *Adder) rename(oldPath, newPath string) (bool, error) {
	if a != nil && a.renamePath != nil {
		return a.renamePath(oldPath, newPath)
	}
	err := os.Rename(oldPath, newPath)
	return err == nil, err
}

func (a *Adder) syncIndexParent(name string) (bool, error) {
	if a != nil && a.syncIndexDirectory != nil {
		return a.syncIndexDirectory(name)
	}
	return syncIndexParent(name)
}

type Result struct {
	Changed bool               `json:"changed"`
	Staged  []changeset.Change `json:"staged"`
}

// Add stages selected logical changes, or all logical changes when all is
// true. Paths are literals and are never passed to Git as pathspecs.
func Add(ctx context.Context, store *managedread.Store, selections []string, all bool) (result Result, resultErr error) {
	return New().Add(ctx, store, selections, all)
}

// Add stages selected logical changes using this instance's fault seams.
func (a *Adder) Add(ctx context.Context, store *managedread.Store, selections []string, all bool) (result Result, resultErr error) {
	if store == nil || store.Repository() == nil {
		return Result{}, fmt.Errorf("staging: nil managed store")
	}
	if all && len(selections) != 0 || !all && len(selections) == 0 {
		return Result{}, fmt.Errorf("%w: choose paths or all", ErrSelection)
	}
	repository := store.Repository()
	lock, err := a.acquireWorktree(repository.GitDir)
	if err != nil {
		mutation := Mutation{Durable: rendezvous.DurableMutationOf(err), RecoveryRequired: rendezvous.RecoveryRequiredOf(err)}
		if mutation.Durable || mutation.RecoveryRequired {
			return Result{}, mutationError("acquire worktree rendezvous", err, mutation)
		}
		return Result{}, err
	}
	published := Mutation{}
	defer func() {
		releaseErr := lock.Release()
		recoveryRequired := rendezvous.RecoveryRequiredOf(releaseErr)
		if owned, ok := lock.(interface{ RecoveryRequired() bool }); ok {
			recoveryRequired = recoveryRequired || owned.RecoveryRequired()
		} else if !recoveryRequired {
			recoveryRequired = pathMayRemain(rendezvous.WorktreePath(repository.GitDir))
		}
		if releaseErr != nil || recoveryRequired {
			result = Result{}
			published.Durable = true // Lock acquisition itself completed durably.
			if releaseErr == nil {
				releaseErr = errors.New("worktree rendezvous release retained an owned lock")
			}
			published.Durable = published.Durable || rendezvous.DurableMutationOf(releaseErr)
			published.RecoveryRequired = published.RecoveryRequired || recoveryRequired
			resultErr = mutationError("release worktree rendezvous", errors.Join(resultErr, releaseErr), published)
			return
		}
		if err := a.checkpoint(PhaseWorktreeReleased); err != nil {
			result = Result{}
			if published.CheckoutChanged {
				resultErr = mutationError("fault after worktree rendezvous release", errors.Join(resultErr, err), published)
			} else {
				resultErr = errors.Join(resultErr, &Error{Operation: "fault after worktree rendezvous release", Err: err})
			}
		}
	}()

	audit, err := store.AuditAccepted(ctx)
	if err != nil {
		return Result{}, err
	}
	if audit.Validation.Status != checker.StatusComplete || audit.Validation.HasErrors() {
		return Result{}, fmt.Errorf("managed store is not conforming: %v", audit.Validation.Findings)
	}

	accepted, err := store.Accepted(ctx)
	if err != nil {
		return Result{}, err
	}
	staged, err := store.Staged(ctx)
	if err != nil {
		return Result{}, err
	}
	working, err := store.Working(ctx)
	if err != nil {
		return Result{}, err
	}
	if staged.Snapshot == nil || working.Snapshot == nil || !changeset.PreflightOK(staged.Snapshot.Tree) || !changeset.PreflightOK(working.Snapshot.Tree) || accepted.Snapshot != nil && !changeset.PreflightOK(accepted.Snapshot.Tree) {
		return Result{}, fmt.Errorf("%w: selected states fail changeset boundary preflight", ErrSelection)
	}
	unstaged := changeset.Diff(tree(staged), tree(working))
	selected, err := selectChanges(selections, all, unstaged, staged, working)
	if err != nil {
		return Result{}, err
	}

	indexPath := filepath.Join(repository.GitDir, "index")
	indexBefore, indexInfo, err := readIndex(indexPath)
	if err != nil {
		return Result{}, err
	}
	temporary, err := os.CreateTemp(repository.GitDir, ".engram-index-candidate-*")
	if err != nil {
		return Result{}, err
	}
	temporaryPath := temporary.Name()
	temporaryInfo, statErr := temporary.Stat()
	defer func() {
		_, removeErr := a.removeExact(temporaryPath, temporaryInfo)
		residual := exactRegularPath(temporaryPath, temporaryInfo)
		if removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) || residual {
			result = Result{}
			if removeErr == nil {
				removeErr = errors.New("prospective Git index cleanup retained its exact temporary")
			}
			resultErr = mutationError("clean prospective Git index", errors.Join(resultErr, removeErr), published)
		}
	}()
	if statErr != nil {
		return Result{}, errors.Join(statErr, temporary.Close())
	}
	if err := temporary.Close(); err != nil {
		return Result{}, err
	}
	if indexInfo != nil {
		if err := os.WriteFile(temporaryPath, indexBefore, indexInfo.Mode().Perm()); err != nil {
			return Result{}, err
		}
	} else if err := initializeEmptyIndex(ctx, repository.Root, temporaryPath); err != nil {
		return Result{}, err
	}
	// Git publishes an alternate index through its own adjacent lock+rename,
	// so a successful command may legitimately replace the CreateTemp inode.
	// Advance cleanup ownership only after that command has completed.
	temporaryInfo, err = regularPathInfo(temporaryPath)
	if err != nil {
		return Result{}, err
	}

	if len(selected) != 0 {
		updates, err := indexUpdates(ctx, repository, accepted, working, selected)
		if err != nil {
			return Result{}, err
		}
		if err := updateAlternateIndex(ctx, repository.Root, temporaryPath, repository.Format, updates); err != nil {
			return Result{}, err
		}
		temporaryInfo, err = regularPathInfo(temporaryPath)
		if err != nil {
			return Result{}, err
		}
	}
	prospective, err := store.StagedFromIndex(ctx, filepath.Clean(temporaryPath))
	if err != nil {
		return Result{}, err
	}
	result = Result{Staged: changeset.Diff(tree(accepted), tree(prospective))}
	if result.Staged == nil {
		result.Staged = []changeset.Change{}
	}
	indexAfter, err := os.ReadFile(temporaryPath)
	if err != nil {
		return Result{}, err
	}
	if bytes.Equal(indexBefore, indexAfter) && (indexInfo != nil || len(selected) == 0) {
		return result, nil
	}
	if err := verifyInputs(ctx, store, selected, working, indexPath, indexBefore, indexInfo); err != nil {
		return Result{}, err
	}
	published, err = a.publishIndex(indexPath, indexBefore, indexInfo, indexAfter)
	if err != nil {
		return Result{}, err
	}
	result.Changed = true
	return result, nil
}

func pathMayRemain(name string) bool {
	_, err := os.Lstat(name)
	return !errors.Is(err, os.ErrNotExist)
}

func exactRegularPath(name string, expected os.FileInfo) bool {
	current, err := os.Lstat(name)
	return err == nil && expected != nil && current.Mode()&os.ModeSymlink == 0 && current.Mode().IsRegular() && os.SameFile(expected, current)
}

func regularPathInfo(name string) (os.FileInfo, error) {
	info, err := os.Lstat(name)
	if err != nil {
		return nil, err
	}
	if err := fileidentity.Pin(info); err != nil {
		return nil, errors.Join(ErrConcurrent, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, ErrConcurrent
	}
	return info, nil
}

func (a *Adder) removeExact(name string, expected os.FileInfo) (bool, error) {
	current, err := os.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if expected == nil || current.Mode()&os.ModeSymlink != 0 || !current.Mode().IsRegular() || !os.SameFile(expected, current) {
		return false, ErrConcurrent
	}
	removed, removeErr := a.remove(name)
	if !removed && removeErr == nil && exactRegularPath(name, expected) {
		removeErr = errors.New("staging cleanup reported success without removing the exact path")
	}
	return removed, removeErr
}

type update struct {
	path string
	mode gitraw.TreeMode
	oid  string
}

func indexUpdates(ctx context.Context, repository *gitraw.Repository, accepted, working *managedread.SnapshotView, selected []changeset.Change) ([]update, error) {
	git, err := exec.LookPath("git")
	if err != nil {
		return nil, err
	}
	result := make([]update, 0, len(selected))
	for _, change := range selected {
		if change.Operation == changeset.Deleted {
			result = append(result, update{path: change.Path})
			continue
		}
		file, exists := working.Snapshot.Tree.Files[change.Path]
		if !exists {
			return nil, fmt.Errorf("%w: selected working file %q became unavailable", ErrConcurrent, change.Path)
		}
		command := exec.CommandContext(ctx, git,
			"-c", "core.longpaths=true",
			"--no-pager", "--no-optional-locks", "--no-replace-objects",
			"-c", "core.hooksPath="+os.DevNull, "-c", "core.fsmonitor=false", "-c", "core.untrackedCache=false",
			"-c", "maintenance.auto=false", "-c", "gc.auto=0",
			"-C", repository.Root, "hash-object", "-w", "--stdin",
		)
		command.Env = isolatedEnvironment(os.Environ(), "")
		command.Stdin = bytes.NewReader(file.Data)
		output, err := command.Output()
		if err != nil {
			return nil, fmt.Errorf("write selected blob: %w", err)
		}
		oid := strings.TrimSuffix(string(output), "\n")
		if _, err := gitraw.ParseOID(repository.Format, oid); err != nil {
			return nil, fmt.Errorf("hash-object returned invalid object ID: %w", err)
		}
		mode := gitraw.ModeRegular
		if accepted != nil && accepted.Modes[change.Path] == gitraw.ModeExecutable {
			mode = gitraw.ModeExecutable
		}
		result = append(result, update{path: change.Path, mode: mode, oid: oid})
	}
	return result, nil
}

func updateAlternateIndex(ctx context.Context, root, indexPath string, format gitraw.ObjectFormat, updates []update) error {
	git, err := exec.LookPath("git")
	if err != nil {
		return err
	}
	var input bytes.Buffer
	for _, update := range updates {
		if update.mode == "" {
			fmt.Fprintf(&input, "0 %s\t%s%c", strings.Repeat("0", format.HexWidth()), update.path, byte(0))
		} else {
			fmt.Fprintf(&input, "%s %s\t%s%c", update.mode, update.oid, update.path, byte(0))
		}
	}
	command := exec.CommandContext(ctx, git,
		"-c", "core.longpaths=true",
		"--no-pager", "--no-optional-locks", "--no-replace-objects",
		"-c", "core.hooksPath="+os.DevNull, "-c", "core.fsmonitor=false", "-c", "core.untrackedCache=false",
		"-c", "maintenance.auto=false", "-c", "gc.auto=0",
		"-C", root, "update-index", "-z", "--index-info",
	)
	command.Env = isolatedEnvironment(os.Environ(), indexPath)
	command.Stdin = &input
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("construct prospective index: %w: %s", err, bytes.TrimSpace(output))
	}
	return nil
}

func initializeEmptyIndex(ctx context.Context, root, indexPath string) error {
	if err := os.Remove(indexPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	git, err := exec.LookPath("git")
	if err != nil {
		return err
	}
	command := exec.CommandContext(ctx, git,
		"-c", "core.longpaths=true",
		"--no-pager", "--no-optional-locks", "--no-replace-objects",
		"-c", "core.hooksPath="+os.DevNull, "-c", "core.fsmonitor=false", "-c", "core.untrackedCache=false",
		"-c", "maintenance.auto=false", "-c", "gc.auto=0",
		"-C", root, "read-tree", "--empty",
	)
	command.Env = isolatedEnvironment(os.Environ(), indexPath)
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("create empty prospective index: %w: %s", err, bytes.TrimSpace(output))
	}
	return nil
}

func selectChanges(selections []string, all bool, changes []changeset.Change, staged, working *managedread.SnapshotView) ([]changeset.Change, error) {
	if all {
		return append([]changeset.Change(nil), changes...), nil
	}
	selected := make(map[string]changeset.Change)
	for _, selection := range selections {
		if !validSelection(selection) {
			return nil, fmt.Errorf("%w: unsafe literal path %q", ErrSelection, selection)
		}
		matches := false
		for _, change := range changes {
			if selection == "." || change.Path == selection || strings.HasPrefix(change.Path, selection+"/") {
				selected[change.Path] = change
				matches = true
			}
		}
		if !matches && !existsLogical(selection, staged) && !existsLogical(selection, working) {
			return nil, fmt.Errorf("%w: path %q does not select an existing or changed logical entry", ErrSelection, selection)
		}
	}
	result := make([]changeset.Change, 0, len(selected))
	for _, change := range selected {
		result = append(result, change)
	}
	sort.Slice(result, func(i, j int) bool { return bytes.Compare([]byte(result[i].Path), []byte(result[j].Path)) < 0 })
	return result, nil
}

func existsLogical(name string, view *managedread.SnapshotView) bool {
	if name == "." {
		return true
	}
	if view == nil || view.Snapshot == nil || view.Snapshot.Tree == nil {
		return false
	}
	if _, exists := view.Snapshot.Tree.Files[name]; exists {
		return true
	}
	for _, directory := range view.Snapshot.Tree.Directories {
		if directory == name {
			return true
		}
	}
	return false
}

func validSelection(value string) bool {
	if value == "." {
		return true
	}
	if value == "" || strings.HasPrefix(value, "/") || strings.Contains(value, "\\") || path.Clean(value) != value || value == ".." || strings.HasPrefix(value, "../") {
		return false
	}
	return true
}

func verifyInputs(ctx context.Context, store *managedread.Store, selected []changeset.Change, captured *managedread.SnapshotView, indexPath string, indexBytes []byte, indexInfo os.FileInfo) error {
	repository := store.Repository()
	currentRepository, err := gitraw.Discover(ctx, repository.Root)
	if err != nil || currentRepository.HeadRef != repository.HeadRef || !sameOID(currentRepository.Head, repository.Head) {
		return fmt.Errorf("%w: accepted ref changed", ErrConcurrent)
	}
	currentIndex, currentInfo, err := readIndex(indexPath)
	if err != nil || !bytes.Equal(currentIndex, indexBytes) || indexInfo == nil != (currentInfo == nil) || indexInfo != nil && !os.SameFile(indexInfo, currentInfo) {
		return fmt.Errorf("%w: index changed", ErrConcurrent)
	}
	currentWorking, err := store.Working(ctx)
	if err != nil {
		return err
	}
	for _, change := range selected {
		before, beforeOK := captured.Snapshot.Tree.Files[change.Path]
		after, afterOK := currentWorking.Snapshot.Tree.Files[change.Path]
		if beforeOK != afterOK || beforeOK && !bytes.Equal(before.Data, after.Data) {
			return fmt.Errorf("%w: working path %q changed", ErrConcurrent, change.Path)
		}
	}
	return nil
}

func (a *Adder) publishIndex(name string, before []byte, beforeInfo os.FileInfo, after []byte) (mutation Mutation, resultErr error) {
	lockName := name + ".lock"
	lock, err := os.OpenFile(lockName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return mutation, rendezvous.ErrBusy
	}
	if err != nil {
		return mutation, err
	}
	lockInfo, statErr := lock.Stat()
	removeLock := true
	defer func() {
		if removeLock {
			_, removeErr := a.removeExact(lockName, lockInfo)
			mutation.RecoveryRequired = exactRegularPath(lockName, lockInfo)
			if removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) || mutation.RecoveryRequired {
				if removeErr == nil {
					removeErr = errors.New("Git index lock cleanup retained its exact inode")
				}
				resultErr = mutationError("clean Git index lock", errors.Join(resultErr, removeErr), mutation)
			}
		}
	}()
	if statErr != nil || !lockInfo.Mode().IsRegular() {
		return mutation, errors.Join(statErr, lock.Close())
	}
	current, currentInfo, err := readIndex(name)
	if err != nil || !bytes.Equal(current, before) || beforeInfo == nil != (currentInfo == nil) || beforeInfo != nil && !os.SameFile(beforeInfo, currentInfo) {
		closeErr := lock.Close()
		return mutation, errors.Join(ErrConcurrent, err, closeErr)
	}
	if _, err := lock.Write(after); err != nil {
		return mutation, errors.Join(err, lock.Close())
	}
	if err := lock.Sync(); err != nil {
		return mutation, errors.Join(err, lock.Close())
	}
	if err := lock.Close(); err != nil {
		return mutation, err
	}
	renamed, renameErr := a.rename(lockName, name)
	if !renamed && exactRegularPath(name, lockInfo) {
		renamed = true
	}
	if !renamed {
		if renameErr == nil {
			renameErr = errors.New("Git index rename reported success without publishing the exact inode")
		}
		return mutation, renameErr
	}
	removeLock = false
	mutation.CheckoutChanged = true
	if renameErr != nil {
		durable, syncErr := a.syncIndexParent(filepath.Dir(name))
		mutation.Durable = durable
		return mutation, mutationError("rename Git index", errors.Join(renameErr, syncErr), mutation)
	}
	if err := a.checkpoint(PhaseIndexRenamed); err != nil {
		return mutation, mutationError("fault after Git index rename", err, mutation)
	}
	durable, err := a.syncIndexParent(filepath.Dir(name))
	mutation.Durable = durable
	if err != nil {
		return mutation, mutationError("sync Git index directory", err, mutation)
	}
	if err := a.checkpoint(PhaseIndexSynced); err != nil {
		return mutation, mutationError("fault after Git index sync", err, mutation)
	}
	return mutation, nil
}

func syncIndexParent(name string) (bool, error) {
	directory, err := os.Open(name)
	if err != nil {
		return false, err
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	// Windows does not expose a portable directory-fsync operation. The index
	// lock contents were synced before the atomic replacement.
	if runtime.GOOS == "windows" {
		syncErr = nil
	}
	if syncErr != nil {
		return false, errors.Join(syncErr, closeErr)
	}
	return true, closeErr
}

func readIndex(name string) ([]byte, os.FileInfo, error) {
	info, err := os.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	if err := fileidentity.Pin(info); err != nil {
		return nil, nil, errors.Join(ErrConcurrent, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, nil, fmt.Errorf("Git index is not a real regular file")
	}
	data, err := os.ReadFile(name)
	return data, info, err
}

func tree(view *managedread.SnapshotView) *snapshot.Tree {
	if view == nil || view.Snapshot == nil {
		return nil
	}
	return view.Snapshot.Tree
}

func sameOID(left, right *gitraw.OID) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Equal(*right)
}

func isolatedEnvironment(environment []string, indexPath string) []string {
	result := make([]string, 0, len(environment)+9)
	for _, item := range environment {
		name, _, _ := strings.Cut(item, "=")
		upper := strings.ToUpper(name)
		if strings.HasPrefix(upper, "GIT_") || strings.HasPrefix(upper, "ENGRAM_") {
			continue
		}
		result = append(result, item)
	}
	result = append(result,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_CONFIG_COUNT=0",
		"GIT_NO_LAZY_FETCH=1",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_OPTIONAL_LOCKS=0",
		"GIT_PAGER=cat",
	)
	if indexPath != "" {
		result = append(result, "GIT_INDEX_FILE="+indexPath)
	}
	return result
}
