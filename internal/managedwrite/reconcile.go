package managedwrite

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/ontopix/engram/internal/changeset"
	"github.com/ontopix/engram/internal/checker"
	"github.com/ontopix/engram/internal/journal"
	"github.com/ontopix/engram/internal/managedread"
	"github.com/ontopix/engram/internal/rendezvous"
	"github.com/ontopix/engram/internal/treeimage"
)

func verifyFinalInputs(ctx context.Context, git *gitClient, expected *repositoryObservation, proof *preservationProof) error {
	current, err := observeRepository(ctx, expected.repository.Root)
	if err != nil {
		return err
	}
	changed := make([]string, 0)
	if !sameRepository(expected.repository, current.repository) || !bytes.Equal(expected.headBytes, current.headBytes) {
		changed = append(changed, "HEAD/ref")
	}
	if expected.indexExists != current.indexExists || expected.indexMode != current.indexMode || !bytes.Equal(expected.indexBytes, current.indexBytes) {
		changed = append(changed, "index")
	}
	for _, update := range proof.paths {
		value, readErr := observePath(expected.repository.Root, update.Path)
		if readErr != nil {
			return readErr
		}
		if !sameJournalImage(value, update.Before) {
			changed = append(changed, update.Path)
		}
	}
	for _, expectedFingerprint := range proof.fingerprints {
		currentFingerprint, readErr := recaptureFingerprint(ctx, git, expected.repository.Root, expectedFingerprint, proof.fingerprints)
		if readErr != nil {
			return readErr
		}
		if !sameFingerprint(expectedFingerprint, currentFingerprint) {
			changed = append(changed, expectedFingerprint.Name)
		}
	}
	if proof.cleanBase != nil {
		store, openErr := managedread.Open(ctx, expected.repository.Root)
		if openErr != nil {
			return openErr
		}
		working, workingErr := store.Working(ctx)
		if workingErr != nil {
			return workingErr
		}
		if working.Snapshot == nil || working.Snapshot.Validation.Status != checker.StatusComplete || working.Snapshot.Validation.HasErrors() || !changeset.PreflightOK(working.Snapshot.Tree) {
			changed = append(changed, "working")
		} else {
			workingImage, imageErr := treeimage.FromSnapshot(working.Snapshot.Tree, nil)
			if imageErr != nil || !treeimage.Equal(proof.cleanBase, workingImage) {
				changed = append(changed, "working")
			}
		}
	}
	if len(changed) != 0 {
		return typedPaths(FailureConcurrency, PhaseFinalRecheck, compactSorted(changed), ErrConcurrent)
	}
	return nil
}

func verifyRecoverableInputs(ctx context.Context, git *gitClient, root string, record journal.Record) error {
	_ = git // Recovery is intentionally presentation-independent after CAS.
	changed := make([]string, 0)
	// Presentation evidence is required up to CAS. Recovery deliberately uses
	// only raw journal bytes and no configuration/attribute presentation input;
	// partial reconciliation may itself have changed those source identities.
	for _, update := range record.Paths {
		value, err := observePath(root, update.Path)
		if err != nil {
			return err
		}
		if !sameJournalImage(value, update.Before) && !sameJournalImage(value, update.After) {
			changed = append(changed, update.Path)
		}
	}
	indexPath, err := liveIndexPath(ctx, root)
	if err != nil {
		return err
	}
	current, exists, _, err := readOptionalRealFile(indexPath)
	if err != nil {
		return err
	}
	beforeExists := recordedIndexPresent(record)
	if !(exists == record.IndexAfter.Present && bytes.Equal(current, record.IndexAfter.Data)) && !(exists == beforeExists && bytes.Equal(current, record.IndexBefore.Data)) {
		changed = append(changed, "index")
	}
	if len(changed) != 0 {
		return typedPaths(FailureRecovery, PhaseWorktreeReconciled, compactSorted(changed), ErrRecovery)
	}
	return nil
}

func reconcileIndex(ctx context.Context, root string, record journal.Record) (bool, error) {
	indexPath, err := liveIndexPath(ctx, root)
	if err != nil {
		return false, err
	}
	current, exists, _, err := readOptionalRealFile(indexPath)
	if err != nil {
		return false, err
	}
	if exists == record.IndexAfter.Present && bytes.Equal(current, record.IndexAfter.Data) {
		return false, cleanupOwnedIndexState(indexPath, record)
	}
	beforeExists := recordedIndexPresent(record)
	if exists != beforeExists || !bytes.Equal(current, record.IndexBefore.Data) {
		return false, typedPaths(FailureRecovery, PhaseIndexReconciled, []string{"index"}, ErrRecovery)
	}
	lockName := indexPath + ".lock"
	temporaryName := ownedIndexTemporary(indexPath, record.OwnerToken)
	temporaryData, temporaryExists, _, err := readOptionalRealFile(temporaryName)
	if err != nil {
		return false, err
	}
	if temporaryExists && !bytes.Equal(temporaryData, record.IndexAfter.Data) {
		if err := os.Remove(temporaryName); err != nil {
			return false, err
		}
		if err := syncDirectory(filepath.Dir(indexPath)); err != nil {
			return false, err
		}
		temporaryExists = false
	}
	if !temporaryExists {
		file, err := os.OpenFile(temporaryName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return false, err
		}
		if _, err := file.Write(record.IndexAfter.Data); err != nil {
			_ = file.Close()
			return false, err
		}
		if err := file.Sync(); err != nil {
			_ = file.Close()
			return false, err
		}
		if err := file.Close(); err != nil {
			return false, err
		}
	}
	if err := os.Link(temporaryName, lockName); err != nil {
		if !errors.Is(err, os.ErrExist) || !sameRealFile(lockName, temporaryName, record.IndexAfter.Data) {
			return false, rendezvous.ErrBusy
		}
	} else if err := syncDirectory(filepath.Dir(indexPath)); err != nil {
		return false, err
	}
	// Git's native index lock is now held by a fully written, durable image.
	// Recheck the live index one last time before the atomic replacement.
	current, exists, _, err = readOptionalRealFile(indexPath)
	if err != nil || exists != beforeExists || !bytes.Equal(current, record.IndexBefore.Data) {
		return false, typedPaths(FailureRecovery, PhaseIndexReconciled, []string{"index"}, ErrRecovery)
	}
	if err := os.Rename(lockName, indexPath); err != nil {
		return false, err
	}
	changed := true
	if err := syncDirectory(filepath.Dir(indexPath)); err != nil {
		return changed, err
	}
	if err := os.Remove(temporaryName); err != nil && !errors.Is(err, os.ErrNotExist) {
		return changed, err
	}
	return changed, syncDirectory(filepath.Dir(indexPath))
}

func ownedIndexTemporary(indexPath, token string) string {
	return filepath.Join(filepath.Dir(indexPath), ".engram-index-"+token+".tmp")
}

func sameRealFile(left, right string, data []byte) bool {
	leftInfo, leftErr := os.Lstat(left)
	rightInfo, rightErr := os.Lstat(right)
	if leftErr != nil || rightErr != nil || leftInfo.Mode()&os.ModeSymlink != 0 || rightInfo.Mode()&os.ModeSymlink != 0 || !leftInfo.Mode().IsRegular() || !rightInfo.Mode().IsRegular() || !os.SameFile(leftInfo, rightInfo) {
		return false
	}
	observed, exists, _, err := readOptionalRealFile(left)
	return err == nil && exists && bytes.Equal(observed, data)
}

func cleanupOwnedIndexState(indexPath string, record journal.Record) error {
	temporaryName := ownedIndexTemporary(indexPath, record.OwnerToken)
	lockName := indexPath + ".lock"
	if _, err := os.Lstat(lockName); err == nil {
		if !sameRealFile(lockName, temporaryName, record.IndexAfter.Data) {
			return rendezvous.ErrBusy
		}
		if err := os.Remove(lockName); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	removed := false
	if _, err := os.Lstat(temporaryName); err == nil {
		if err := os.Remove(temporaryName); err != nil {
			return err
		}
		removed = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if removed {
		return syncDirectory(filepath.Dir(indexPath))
	}
	return nil
}

func liveIndexPath(ctx context.Context, root string) (string, error) {
	repository, err := gitrawDiscoverForFingerprint(ctx, root)
	if err != nil {
		return "", err
	}
	return filepath.Join(repository.GitDir, "index"), nil
}

func recordedIndexPresent(record journal.Record) bool {
	return record.IndexBefore.Present
}

func reconcileWorktree(root string, record journal.Record) (changed bool, resultErr error) {
	if len(record.Paths) == 0 {
		return false, nil
	}
	if err := validateReconciliationPlan(record.Paths); err != nil {
		return false, err
	}
	current := make(map[string]*journal.Image, len(record.Paths))
	byPath := make(map[string]journal.PathUpdate, len(record.Paths))
	conflicts := make([]string, 0)
	for _, update := range record.Paths {
		byPath[update.Path] = update
		value, err := observePath(root, update.Path)
		if err != nil {
			return false, err
		}
		current[update.Path] = value
		if !sameJournalImage(value, update.Before) && !sameJournalImage(value, update.After) {
			conflicts = append(conflicts, update.Path)
		}
	}
	if len(conflicts) != 0 {
		return false, typedPaths(FailureRecovery, PhaseWorktreeReconciled, conflicts, ErrRecovery)
	}
	rootHandle, err := os.OpenRoot(root)
	if err != nil {
		return false, err
	}
	defer rootHandle.Close()

	updates := append([]journal.PathUpdate(nil), record.Paths...)
	// Deletions run child-first so every directory preimage is empty at its
	// native remove boundary. File/directory replacement is rejected by the
	// proof because its intermediate absent image is not journalized.
	sort.Slice(updates, func(i, j int) bool {
		leftDepth, rightDepth := strings.Count(updates[i].Path, "/"), strings.Count(updates[j].Path, "/")
		if leftDepth != rightDepth {
			return leftDepth > rightDepth
		}
		return updates[i].Path > updates[j].Path
	})
	for _, update := range updates {
		value := current[update.Path]
		if update.After != nil || sameJournalImage(value, update.After) {
			continue
		}
		if !sameJournalImage(value, update.Before) {
			return changed, typedPaths(FailureRecovery, PhaseWorktreeReconciled, []string{update.Path}, ErrRecovery)
		}
		if err := recheckPathAndDependencies(rootHandle, update, value, byPath, false); err != nil {
			return changed, err
		}
		parent, base, err := openLogicalParent(rootHandle, update.Path)
		if err != nil {
			return changed, err
		}
		if err := parent.Remove(base); err != nil {
			_ = parent.Close()
			return changed, fmt.Errorf("remove worktree preimage %q: %w", update.Path, err)
		}
		changed = true
		if err := syncOpenedRoot(parent); err != nil {
			_ = parent.Close()
			return changed, err
		}
		if err := parent.Close(); err != nil {
			return changed, err
		}
		current[update.Path] = nil
	}

	// Final directories establish dependencies before leaf publication.
	sort.Slice(updates, func(i, j int) bool {
		leftDepth, rightDepth := strings.Count(updates[i].Path, "/"), strings.Count(updates[j].Path, "/")
		if leftDepth != rightDepth {
			return leftDepth < rightDepth
		}
		return updates[i].Path < updates[j].Path
	})
	for _, update := range updates {
		if update.After == nil || update.After.Kind != "directory" || sameJournalImage(current[update.Path], update.After) {
			continue
		}
		if current[update.Path] != nil || update.Before != nil {
			return changed, typedPaths(FailureRecovery, PhaseWorktreeReconciled, []string{update.Path}, ErrRecovery)
		}
		if err := recheckPathAndDependencies(rootHandle, update, nil, byPath, true); err != nil {
			return changed, err
		}
		parent, base, err := openLogicalParent(rootHandle, update.Path)
		if err != nil {
			return changed, err
		}
		if err := parent.Mkdir(base, os.FileMode(update.After.Mode)); err != nil {
			_ = parent.Close()
			return changed, fmt.Errorf("create worktree directory %q: %w", update.Path, err)
		}
		changed = true
		err = errors.Join(syncOpenedRoot(parent), parent.Close())
		if err != nil {
			return changed, err
		}
		current[update.Path] = cloneJournalImage(update.After)
	}

	for _, update := range updates {
		if update.After == nil || update.After.Kind == "directory" {
			continue
		}
		if sameJournalImage(current[update.Path], update.After) {
			if err := cleanupRootTemporary(rootHandle, update.Path, record.OwnerToken); err != nil {
				return changed, err
			}
			continue
		}
		if !sameJournalImage(current[update.Path], update.Before) {
			return changed, typedPaths(FailureRecovery, PhaseWorktreeReconciled, []string{update.Path}, ErrRecovery)
		}
		if err := recheckPathAndDependencies(rootHandle, update, current[update.Path], byPath, true); err != nil {
			return changed, err
		}
		replaced, err := replaceRootPath(rootHandle, update.Path, update.Before, update.After, record.OwnerToken)
		changed = changed || replaced
		if err != nil {
			return changed, err
		}
		current[update.Path] = cloneJournalImage(update.After)
	}
	for _, update := range record.Paths {
		value, err := observePath(root, update.Path)
		if err != nil {
			return changed, err
		}
		if !sameJournalImage(value, update.After) {
			conflicts = append(conflicts, update.Path)
		}
	}
	if len(conflicts) != 0 {
		return changed, typedPaths(FailureRecovery, PhaseWorktreeReconciled, compactSorted(conflicts), ErrRecovery)
	}
	return changed, nil
}

func validateReconciliationPlan(updates []journal.PathUpdate) error {
	byPath := make(map[string]journal.PathUpdate, len(updates))
	conflicts := make([]string, 0)
	for _, update := range updates {
		byPath[update.Path] = update
		if update.Before != nil && update.After != nil && update.Before.Kind != update.After.Kind && (update.Before.Kind == "directory" || update.After.Kind == "directory") {
			conflicts = append(conflicts, update.Path)
		}
	}
	for _, update := range updates {
		for ancestor := filepath.ToSlash(filepath.Dir(filepath.FromSlash(update.Path))); ancestor != "."; ancestor = filepath.ToSlash(filepath.Dir(filepath.FromSlash(ancestor))) {
			boundary, exists := byPath[ancestor]
			if !exists || boundary.Before == nil && boundary.After == nil {
				conflicts = append(conflicts, update.Path)
				break
			}
		}
	}
	if len(conflicts) != 0 {
		return typedPaths(FailureRecovery, PhaseWorktreeReconciled, compactSorted(conflicts), ErrRecovery)
	}
	return nil
}

func recheckPathAndDependencies(root *os.Root, update journal.PathUpdate, expected *journal.Image, byPath map[string]journal.PathUpdate, finalDependencies bool) error {
	observed, err := observePathRoot(root, update.Path)
	if err != nil {
		return err
	}
	if !sameJournalImage(observed, expected) {
		return typedPaths(FailureRecovery, PhaseWorktreeReconciled, []string{update.Path}, ErrRecovery)
	}
	for ancestor := filepath.ToSlash(filepath.Dir(filepath.FromSlash(update.Path))); ancestor != "."; ancestor = filepath.ToSlash(filepath.Dir(filepath.FromSlash(ancestor))) {
		boundary, exists := byPath[ancestor]
		if !exists {
			return typedPaths(FailureRecovery, PhaseWorktreeReconciled, []string{ancestor}, ErrRecovery)
		}
		want := boundary.Before
		if finalDependencies {
			want = boundary.After
		}
		observed, err := observePathRoot(root, ancestor)
		if err != nil {
			return err
		}
		if want == nil || want.Kind != "directory" || !sameJournalImage(observed, want) {
			return typedPaths(FailureRecovery, PhaseWorktreeReconciled, []string{ancestor}, ErrRecovery)
		}
	}
	return nil
}

func reconciliationTemporary(logical, token string) string {
	digest := sha256.Sum256([]byte(token + "\x00" + logical))
	return ".engram-reconcile-" + hex.EncodeToString(digest[:16])
}

func cleanupRootTemporary(root *os.Root, logical, token string) error {
	parent, _, err := openLogicalParent(root, logical)
	if err != nil {
		return err
	}
	defer parent.Close()
	temporary := reconciliationTemporary(logical, token)
	if _, err := parent.Lstat(temporary); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	if err := parent.Remove(temporary); err != nil {
		return err
	}
	return syncOpenedRoot(parent)
}

func replaceRootPath(root *os.Root, logical string, before, image *journal.Image, token string) (bool, error) {
	parent, base, err := openLogicalParent(root, logical)
	if err != nil {
		return false, err
	}
	defer parent.Close()
	temporary := reconciliationTemporary(logical, token)
	if _, err := parent.Lstat(temporary); err == nil {
		if err := parent.Remove(temporary); err != nil {
			return false, err
		}
		if err := syncOpenedRoot(parent); err != nil {
			return false, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	switch image.Kind {
	case "regular":
		file, err := parent.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, os.FileMode(image.Mode))
		if err != nil {
			return false, err
		}
		if _, err := file.Write(image.Data); err != nil {
			_ = file.Close()
			return false, err
		}
		if err := file.Chmod(os.FileMode(image.Mode)); err != nil {
			_ = file.Close()
			return false, err
		}
		if err := file.Sync(); err != nil {
			_ = file.Close()
			return false, err
		}
		if err := file.Close(); err != nil {
			return false, err
		}
	case "symlink":
		if err := parent.Symlink(string(image.Data), temporary); err != nil {
			return false, err
		}
	default:
		return false, fmt.Errorf("cannot publish journal image kind %q", image.Kind)
	}
	observed, err := observePathRoot(root, logical)
	if err != nil || !sameJournalImage(observed, before) {
		return false, typedPaths(FailureRecovery, PhaseWorktreeReconciled, []string{logical}, errors.Join(ErrRecovery, err))
	}
	if before == nil {
		if err := parent.Link(temporary, base); err != nil {
			return false, err
		}
		changed := true
		if err := syncOpenedRoot(parent); err != nil {
			return changed, err
		}
		if err := parent.Remove(temporary); err != nil {
			return changed, err
		}
		return changed, syncOpenedRoot(parent)
	}
	if err := parent.Rename(temporary, base); err != nil {
		return false, err
	}
	return true, syncOpenedRoot(parent)
}

func syncOpenedRoot(root *os.Root) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}
