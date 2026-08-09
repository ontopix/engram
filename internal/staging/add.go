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
	"github.com/ontopix/engram/internal/gitraw"
	"github.com/ontopix/engram/internal/managedread"
	"github.com/ontopix/engram/internal/rendezvous"
	"github.com/ontopix/engram/internal/snapshot"
)

var ErrSelection = errors.New("invalid add selection")
var ErrConcurrent = errors.New("add input changed concurrently")

type Result struct {
	Changed bool               `json:"changed"`
	Staged  []changeset.Change `json:"staged"`
}

// Add stages selected logical changes, or all logical changes when all is
// true. Paths are literals and are never passed to Git as pathspecs.
func Add(ctx context.Context, store *managedread.Store, selections []string, all bool) (result Result, resultErr error) {
	if store == nil || store.Repository() == nil {
		return Result{}, fmt.Errorf("staging: nil managed store")
	}
	if all && len(selections) != 0 || !all && len(selections) == 0 {
		return Result{}, fmt.Errorf("%w: choose paths or all", ErrSelection)
	}
	repository := store.Repository()
	lock, err := rendezvous.AcquireWorktree(repository.GitDir)
	if err != nil {
		return Result{}, err
	}
	defer func() {
		if err := lock.Release(); err != nil {
			result = Result{}
			resultErr = errors.Join(resultErr, fmt.Errorf("release worktree rendezvous: %w", err))
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
	if err := temporary.Close(); err != nil {
		os.Remove(temporaryPath)
		return Result{}, err
	}
	defer os.Remove(temporaryPath)
	if indexInfo != nil {
		if err := os.WriteFile(temporaryPath, indexBefore, indexInfo.Mode().Perm()); err != nil {
			return Result{}, err
		}
	} else if err := initializeEmptyIndex(ctx, repository.Root, temporaryPath); err != nil {
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
	if err := publishIndex(indexPath, indexBefore, indexInfo, indexAfter); err != nil {
		return Result{}, err
	}
	result.Changed = true
	return result, nil
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
		command := exec.CommandContext(ctx, git, "--no-pager", "--no-optional-locks", "--no-replace-objects", "-c", "core.hooksPath="+os.DevNull, "-C", repository.Root, "hash-object", "-w", "--stdin")
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
	command := exec.CommandContext(ctx, git, "--no-pager", "--no-optional-locks", "--no-replace-objects", "-c", "core.hooksPath="+os.DevNull, "-C", root, "update-index", "-z", "--index-info")
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
	command := exec.CommandContext(ctx, git, "--no-pager", "--no-optional-locks", "--no-replace-objects", "-c", "core.hooksPath="+os.DevNull, "-C", root, "read-tree", "--empty")
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

func publishIndex(name string, before []byte, beforeInfo os.FileInfo, after []byte) error {
	lockName := name + ".lock"
	lock, err := os.OpenFile(lockName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return rendezvous.ErrBusy
	}
	if err != nil {
		return err
	}
	removeLock := true
	defer func() {
		if removeLock {
			_ = os.Remove(lockName)
		}
	}()
	current, currentInfo, err := readIndex(name)
	if err != nil || !bytes.Equal(current, before) || beforeInfo == nil != (currentInfo == nil) || beforeInfo != nil && !os.SameFile(beforeInfo, currentInfo) {
		lock.Close()
		return ErrConcurrent
	}
	if _, err := lock.Write(after); err != nil {
		lock.Close()
		return err
	}
	if err := lock.Sync(); err != nil {
		lock.Close()
		return err
	}
	if err := lock.Close(); err != nil {
		return err
	}
	if err := os.Rename(lockName, name); err != nil {
		return err
	}
	removeLock = false
	if runtime.GOOS == "windows" {
		return nil
	}
	directory, err := os.Open(filepath.Dir(name))
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}

func readIndex(name string) ([]byte, os.FileInfo, error) {
	info, err := os.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
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
