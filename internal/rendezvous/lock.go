// Package rendezvous implements the annex-defined cooperative accepted-ref
// and worktree lock paths. It never guesses that an existing owner is stale;
// bounded recovery owns that decision.
package rendezvous

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"time"

	"github.com/ontopix/engram/internal/fileidentity"
)

type Phase string

const (
	PreJournal      Phase = "pre-journal"
	JournalRequired Phase = "journal-required"
)

var ErrBusy = errors.New("engram rendezvous is busy")
var ErrOwnership = errors.New("engram rendezvous ownership changed")
var errRecoveryBusy = errors.New("engram recovery lease is busy")

type Owner struct {
	Version   int    `json:"version"`
	Token     string `json:"token"`
	PID       int    `json:"pid"`
	Hostname  string `json:"hostname"`
	StartedAt string `json:"started_at"`
	Phase     Phase  `json:"phase"`
}

type Handle struct {
	owner         Owner
	paths         []string // Acquisition order; release is reverse.
	owners        map[string]Owner
	removed       map[string]bool
	mutated       bool
	syncDirectory func(string) (bool, error)
}

// RecoveryLease serializes recognized recovery controllers. The persistent
// lease file is harmless metadata; the authority is the host advisory lock,
// which the kernel releases if the controller exits.
type RecoveryLease struct {
	file       *os.File
	adoptFault func(int, string) error
}

type mutationError struct {
	durable          bool
	recoveryRequired bool
	err              error
}

type replacementEffect struct {
	visible          bool
	durable          bool
	recoveryRequired bool
}

func (e *mutationError) Error() string { return e.err.Error() }
func (e *mutationError) Unwrap() error { return e.err }

// DurableMutationOf reports whether a failed rendezvous operation is known to
// have durably created, replaced, or removed at least one owned lock.
func DurableMutationOf(err error) bool {
	durable, _, _ := rendezvousMutationOf(err)
	return durable
}

// RecoveryRequiredOf reports whether a failed rendezvous operation retained
// at least one exact owned lock and therefore blocks another managed writer.
func RecoveryRequiredOf(err error) bool {
	_, recoveryRequired, _ := rendezvousMutationOf(err)
	return recoveryRequired
}

func rendezvousMutationOf(err error) (durable, recoveryRequired, present bool) {
	var visit func(error) (bool, bool, bool)
	visit = func(current error) (bool, bool, bool) {
		if current == nil {
			return false, false, false
		}
		if mutation, ok := current.(*mutationError); ok {
			nestedDurable, _, _ := visit(mutation.err)
			return mutation.durable || nestedDurable, mutation.recoveryRequired, true
		}
		switch unwrapped := current.(type) {
		case interface{ Unwrap() []error }:
			joinedDurable := false
			joinedRecoveryRequired := false
			joinedPresent := false
			for _, child := range unwrapped.Unwrap() {
				childDurable, childRecoveryRequired, childPresent := visit(child)
				if !childPresent {
					continue
				}
				joinedDurable = joinedDurable || childDurable
				joinedRecoveryRequired = childRecoveryRequired
				joinedPresent = true
			}
			return joinedDurable, joinedRecoveryRequired, joinedPresent
		case interface{ Unwrap() error }:
			return visit(unwrapped.Unwrap())
		default:
			return false, false, false
		}
	}
	return visit(err)
}

// RefPath returns the normative lock path for one exact full refname.
func RefPath(commonGitDir, refname string) string {
	digest := sha256.Sum256([]byte(refname))
	return filepath.Join(commonGitDir, "engram", "locks", "refs", hex.EncodeToString(digest[:])+".lock")
}

func WorktreePath(worktreeGitDir string) string {
	return filepath.Join(worktreeGitDir, "engram", "locks", "worktree.lock")
}

// AcquireWriter takes exact ref locks in byte order and then the worktree lock.
func AcquireWriter(commonGitDir, worktreeGitDir string, refnames ...string) (*Handle, error) {
	if commonGitDir == "" || worktreeGitDir == "" || len(refnames) == 0 {
		return nil, fmt.Errorf("writer rendezvous requires Git directories and at least one ref")
	}
	refs := append([]string(nil), refnames...)
	sort.Strings(refs)
	paths := make([]string, 0, len(refs)+1)
	last := ""
	for _, refname := range refs {
		if refname == "" {
			return nil, fmt.Errorf("empty accepted refname")
		}
		if refname == last {
			continue
		}
		last = refname
		paths = append(paths, RefPath(commonGitDir, refname))
	}
	paths = append(paths, WorktreePath(worktreeGitDir))
	return acquire(paths)
}

// AcquireWorktree coordinates one helper that cannot move accepted refs.
func AcquireWorktree(worktreeGitDir string) (*Handle, error) {
	if worktreeGitDir == "" {
		return nil, fmt.Errorf("worktree Git directory is empty")
	}
	return acquire([]string{WorktreePath(worktreeGitDir)})
}

func acquire(paths []string) (*Handle, error) {
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return nil, err
	}
	hostname, err := os.Hostname()
	if err != nil {
		return nil, err
	}
	handle := &Handle{owner: Owner{
		Version: 1, Token: hex.EncodeToString(tokenBytes), PID: os.Getpid(), Hostname: hostname,
		StartedAt: time.Now().UTC().Format(time.RFC3339Nano), Phase: PreJournal,
	}}
	for _, name := range paths {
		if err := create(name, handle.owner); err != nil {
			cleanupErr := handle.releaseCreated()
			durable := handle.Mutated() || DurableMutationOf(err) || DurableMutationOf(cleanupErr)
			recoveryRequired := RecoveryRequiredOf(err) || RecoveryRequiredOf(cleanupErr)
			joined := errors.Join(err, cleanupErr)
			if durable || recoveryRequired {
				joined = &mutationError{durable: durable, recoveryRequired: recoveryRequired, err: joined}
			}
			return nil, joined
		}
		handle.paths = append(handle.paths, name)
		if handle.owners == nil {
			handle.owners = make(map[string]Owner)
		}
		handle.owners[name] = handle.owner
		handle.mutated = true
	}
	return handle, nil
}

// SetPhase durably advances every owned lock. Phase may never move backward.
func (h *Handle) SetPhase(phase Phase) error {
	if h == nil || len(h.paths) == 0 {
		return ErrOwnership
	}
	if phase != JournalRequired || h.owner.Phase != PreJournal {
		return fmt.Errorf("invalid rendezvous phase transition %q -> %q", h.owner.Phase, phase)
	}
	for _, name := range h.paths {
		owner, err := Read(name)
		if err != nil || owner != h.ownerAt(name) {
			return ErrOwnership
		}
	}
	updated := h.owner
	updated.Phase = phase
	transitionDurable := false
	var transitionErr error
	for _, name := range h.paths {
		effect, err := replaceOwned(name, h.ownerAt(name), updated, h.syncLock)
		transitionDurable = transitionDurable || effect.durable
		h.mutated = h.mutated || effect.durable
		if effect.visible {
			h.setOwner(name, updated)
		}
		if err != nil {
			transitionErr = errors.Join(transitionErr, fmt.Errorf("advance %s: %w", name, err))
			// A visible exact replacement can be rolled forward: continue so
			// the remaining locks reach the same phase despite a sync tail.
			if !effect.visible || !effect.recoveryRequired {
				break
			}
		}
	}
	allUpdated := true
	for _, name := range h.paths {
		if !ownedLockPresent(name, updated) {
			allUpdated = false
			break
		}
	}
	if allUpdated {
		h.owner = updated
	}
	if transitionErr != nil {
		return rendezvousMutationError(transitionDurable, h.RecoveryRequired(), transitionErr)
	}
	if !allUpdated {
		return rendezvousMutationError(transitionDurable, h.RecoveryRequired(), ErrOwnership)
	}
	return nil
}

// Release removes owned locks in reverse order only after proving their exact
// token and phase. Recovery-required callers deliberately retain the handle.
func (h *Handle) Release() error {
	if h == nil {
		return nil
	}
	for len(h.paths) != 0 {
		index := len(h.paths) - 1
		name := h.paths[index]
		expected := h.ownerAt(name)
		if h.removed == nil || !h.removed[name] {
			removed, err := removeOwned(name, expected)
			if h.removed == nil {
				h.removed = make(map[string]bool)
			}
			if removed {
				h.removed[name] = true
			}
			if err != nil {
				return rendezvousMutationError(h.mutated, ownedLockPresent(name, expected), err)
			}
		}
		durable, syncErr := h.syncLock(name)
		if durable {
			h.mutated = true
		}
		if syncErr != nil {
			return rendezvousMutationError(h.mutated, ownedLockPresent(name, expected), syncErr)
		}
		if !durable {
			return rendezvousMutationError(h.mutated, ownedLockPresent(name, expected), errors.New("rendezvous directory sync reported no durability"))
		}
		if h.removed[name] {
			h.mutated = true
		}
		delete(h.removed, name)
		delete(h.owners, name)
		h.paths = h.paths[:index]
	}
	return nil
}

func rendezvousMutationError(durable, recoveryRequired bool, err error) error {
	if err == nil || !durable && !recoveryRequired {
		return err
	}
	return &mutationError{durable: durable, recoveryRequired: recoveryRequired, err: err}
}

func (h *Handle) syncLock(name string) (bool, error) {
	if h != nil && h.syncDirectory != nil {
		return h.syncDirectory(name)
	}
	err := syncLockDirectory(name)
	return err == nil, err
}

func (h *Handle) ownerAt(name string) Owner {
	if h != nil && h.owners != nil {
		if owner, ok := h.owners[name]; ok {
			return owner
		}
	}
	if h == nil {
		return Owner{}
	}
	return h.owner
}

func (h *Handle) setOwner(name string, owner Owner) {
	if h.owners == nil {
		h.owners = make(map[string]Owner)
	}
	h.owners[name] = owner
}

func (h *Handle) Owner() Owner {
	if h == nil {
		return Owner{}
	}
	return h.owner
}

// Mutated reports whether this handle is known to have durably created,
// adopted, advanced, or removed owned lock state.
func (h *Handle) Mutated() bool {
	return h != nil && h.mutated
}

// RecoveryRequired reports whether this handle still owns at least one named
// rendezvous lock. It is intentionally an exact owner check so callers do not
// attribute a replacement writer's lock to the failed operation.
func (h *Handle) RecoveryRequired() bool {
	if h == nil {
		return false
	}
	for _, name := range h.paths {
		if ownedLockPresent(name, h.ownerAt(name)) {
			return true
		}
	}
	return false
}

// AcquireRecovery obtains a process-lifetime exclusive lease for one
// worktree's recovery. The file remains after release; it is not a lifecycle
// signal and contains no owner authority.
func AcquireRecovery(worktreeGitDir string) (*RecoveryLease, error) {
	if worktreeGitDir == "" {
		return nil, fmt.Errorf("recovery rendezvous requires a worktree Git directory")
	}
	return AcquireRecoveryPath(filepath.Join(worktreeGitDir, "engram", "locks", "recovery.lease"))
}

// AcquireRecoveryPath obtains the same process-lifetime, nonblocking advisory
// lease at one exact absolute path. The persistent regular file contains no
// owner authority and is not itself recovery-required state.
func AcquireRecoveryPath(name string) (*RecoveryLease, error) {
	if !filepath.IsAbs(name) || filepath.Clean(name) != name || filepath.Base(name) == "." || filepath.Base(name) == string(filepath.Separator) {
		return nil, fmt.Errorf("recovery lease path must be absolute and clean")
	}
	directory, base, err := openLockDirectory(name, true)
	if err != nil {
		return nil, err
	}
	defer directory.Close()
	before, statErr := pinnedFileInfo(directory.Lstat(base))
	if statErr == nil && (before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular()) {
		return nil, fmt.Errorf("unsafe recovery lease file")
	}
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return nil, statErr
	}
	var file *os.File
	if errors.Is(statErr, os.ErrNotExist) {
		// Publish a previously absent lease with O_EXCL. A racing creator may
		// win between Lstat and OpenFile; in that case open only its already
		// named inode. Never retry creation after observing EEXIST, because a
		// subsequent disappearance could mean another process still holds an
		// advisory lock on an unlinked inode.
		file, err = directory.OpenFile(base, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, os.ErrExist) {
			before, statErr = pinnedFileInfo(directory.Lstat(base))
			if statErr != nil {
				return nil, errors.Join(ErrOwnership, statErr)
			}
			if before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() {
				return nil, fmt.Errorf("unsafe recovery lease file")
			}
			file, err = directory.OpenFile(base, os.O_RDWR, 0)
		}
	} else {
		file, err = directory.OpenFile(base, os.O_RDWR, 0)
	}
	if err != nil {
		return nil, err
	}
	opened, err := pinnedFileInfo(file.Stat())
	if err != nil || !opened.Mode().IsRegular() || before != nil && !os.SameFile(before, opened) {
		_ = file.Close()
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("recovery lease changed while opening")
	}
	named, err := pinnedFileInfo(directory.Lstat(base))
	if err != nil || named.Mode()&os.ModeSymlink != 0 || !os.SameFile(opened, named) {
		_ = file.Close()
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("recovery lease changed while opening")
	}
	// Every contender performs the creation durability barrier. A process
	// that opened the inode created by a peer can otherwise win the advisory
	// lock and return before that peer has synced the new directory entry.
	if err := file.Sync(); err != nil {
		return nil, errors.Join(err, file.Close())
	}
	if err := syncRoot(directory); err != nil {
		return nil, errors.Join(err, file.Close())
	}
	if err := lockRecoveryFile(file); err != nil {
		_ = file.Close()
		if errors.Is(err, errRecoveryBusy) {
			return nil, ErrBusy
		}
		return nil, err
	}
	// The advisory lock protects the opened inode, so prove the pathname still
	// names that inode after acquisition. Otherwise a replacement path could
	// admit a second controller holding a different lock concurrently.
	lockedName, err := pinnedFileInfo(directory.Lstat(base))
	if err != nil || lockedName.Mode()&os.ModeSymlink != 0 || !lockedName.Mode().IsRegular() || !os.SameFile(opened, lockedName) {
		unlockErr := unlockRecoveryFile(file)
		closeErr := file.Close()
		if err == nil {
			err = ErrOwnership
		}
		return nil, errors.Join(err, unlockErr, closeErr, fmt.Errorf("recovery lease changed after locking"))
	}
	return &RecoveryLease{file: file}, nil
}

// AdoptWriter replaces recognized dead-owner metadata while the recovery
// lease excludes another adopter. It preserves the transaction token and
// phase so the immutable journal remains bound to the locks.
func (l *RecoveryLease) AdoptWriter(commonGitDir, worktreeGitDir, token string, phase Phase, refnames ...string) (*Handle, error) {
	if l == nil || l.file == nil || len(token) != 64 || phase != PreJournal && phase != JournalRequired {
		return nil, ErrOwnership
	}
	paths, err := writerPaths(commonGitDir, worktreeGitDir, refnames)
	if err != nil {
		return nil, err
	}
	handle, err := l.AdoptPaths(token, phase, paths...)
	if err == nil {
		handle.paths = paths
	}
	return handle, err
}

// AdoptPaths is the recovery-only form used for a recognized partial cleanup
// or stale pre-journal multi-ref owner. Callers must derive every path from
// annex-defined locations; this method refuses non-absolute or duplicate
// paths and never creates a missing lock.
func (l *RecoveryLease) AdoptPaths(token string, phase Phase, paths ...string) (*Handle, error) {
	if l == nil || l.file == nil || len(token) != 64 || phase != PreJournal && phase != JournalRequired || len(paths) == 0 {
		return nil, ErrOwnership
	}
	ordered := append([]string(nil), paths...)
	sort.Strings(ordered)
	for index, name := range ordered {
		if !filepath.IsAbs(name) || index != 0 && name == ordered[index-1] {
			return nil, ErrOwnership
		}
	}
	// Worktree is always the final acquisition and therefore the first
	// release, even when common/worktree administration paths sort otherwise.
	for index, name := range ordered {
		if name == WorktreePath(filepath.Dir(filepath.Dir(filepath.Dir(name)))) || filepath.Base(name) == "worktree.lock" {
			ordered = append(append(ordered[:index], ordered[index+1:]...), name)
			break
		}
	}
	before := Owner{Token: token, Phase: phase}
	for _, name := range ordered {
		owner, err := Read(name)
		if err != nil || owner.Token != token || owner.Phase != phase {
			return nil, ErrOwnership
		}
		if before.Version == 0 {
			before = owner
		}
	}
	hostname, err := os.Hostname()
	if err != nil {
		return nil, err
	}
	after := Owner{Version: 1, Token: token, PID: os.Getpid(), Hostname: hostname, StartedAt: time.Now().UTC().Format(time.RFC3339Nano), Phase: phase}
	mutated := false
	for index, name := range ordered {
		owner, err := Read(name)
		if err != nil || owner.Token != token || owner.Phase != phase {
			return nil, &mutationError{durable: mutated, recoveryRequired: true, err: ErrOwnership}
		}
		effect, err := replaceOwned(name, owner, after, func(name string) (bool, error) {
			syncErr := syncLockDirectory(name)
			return syncErr == nil, syncErr
		})
		mutated = mutated || effect.durable
		if err != nil {
			return nil, &mutationError{durable: mutated, recoveryRequired: true, err: err}
		}
		if l.adoptFault != nil {
			if err := l.adoptFault(index, name); err != nil {
				return nil, &mutationError{durable: mutated, recoveryRequired: true, err: err}
			}
		}
	}
	// Preserve the caller's acquisition order when it is known. Generic path
	// adoption uses byte order; Release still runs it in reverse.
	owners := make(map[string]Owner, len(ordered))
	for _, name := range ordered {
		owners[name] = after
	}
	handle := &Handle{owner: after, paths: append([]string(nil), ordered...), owners: owners, mutated: mutated}
	return handle, nil
}

func (l *RecoveryLease) Release() error {
	if l == nil || l.file == nil {
		return nil
	}
	file := l.file
	l.file = nil
	return errors.Join(unlockRecoveryFile(file), file.Close())
}

func Read(name string) (Owner, error) {
	directory, base, err := openLockDirectory(name, false)
	if err != nil {
		return Owner{}, err
	}
	defer directory.Close()
	data, err := stableLockRead(directory, base)
	if err != nil {
		return Owner{}, err
	}
	return decodeOwner(data)
}

func decodeOwner(data []byte) (Owner, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var owner Owner
	if err := decoder.Decode(&owner); err != nil || decoder.Decode(&struct{}{}) != io.EOF || owner.Version != 1 || len(owner.Token) != 64 || owner.PID <= 0 || owner.Hostname == "" || owner.StartedAt == "" || owner.Phase != PreJournal && owner.Phase != JournalRequired {
		return Owner{}, fmt.Errorf("malformed rendezvous owner")
	}
	return owner, nil
}

func create(name string, owner Owner) error {
	directory, base, err := openLockDirectory(name, true)
	if err != nil {
		return err
	}
	defer directory.Close()
	data, err := json.Marshal(owner)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	file, err := directory.OpenFile(base, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return ErrBusy
	}
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		_ = directory.Remove(base)
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		_ = directory.Remove(base)
		return err
	}
	if err := file.Close(); err != nil {
		_ = directory.Remove(base)
		return err
	}
	if err := syncRoot(directory); err != nil {
		// Publication is visible but not known durable. Remove only the exact
		// owner we just created; a failed cleanup remains an explicit
		// recovery-bearing rendezvous error rather than an ordinary retry.
		removed, removeErr := removeOwned(name, owner)
		var cleanupSyncErr error
		if removed {
			cleanupSyncErr = syncLockDirectory(name)
		}
		residual := ownedLockPresent(name, owner)
		durable := removed && cleanupSyncErr == nil
		return &mutationError{
			durable: durable, recoveryRequired: residual,
			err: errors.Join(err, removeErr, cleanupSyncErr),
		}
	}
	return nil
}

func replaceOwned(name string, before, after Owner, syncDirectory func(string) (bool, error)) (replacementEffect, error) {
	effect := replacementEffect{}
	directory, base, err := openLockDirectory(name, false)
	if err != nil {
		return effect, err
	}
	defer directory.Close()
	current, err := readOwnerAt(directory, base)
	if err != nil || current != before {
		return effect, ErrOwnership
	}
	data, err := json.Marshal(after)
	if err != nil {
		return effect, err
	}
	data = append(data, '\n')
	temporaryName, temporary, err := createLockTemporary(directory)
	if err != nil {
		return effect, err
	}
	defer directory.Remove(temporaryName)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return effect, err
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return effect, err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return effect, err
	}
	if err := temporary.Close(); err != nil {
		return effect, err
	}
	current, err = readOwnerAt(directory, base)
	if err != nil || current != before {
		return effect, ErrOwnership
	}
	if err := directory.Rename(temporaryName, base); err != nil {
		return effect, err
	}
	effect.visible = true
	effect.recoveryRequired = ownedLockPresent(name, after)
	if syncDirectory == nil {
		syncDirectory = func(string) (bool, error) {
			syncErr := syncRoot(directory)
			return syncErr == nil, syncErr
		}
	}
	effect.durable, err = syncDirectory(name)
	effect.recoveryRequired = ownedLockPresent(name, after)
	if err != nil {
		return effect, err
	}
	if !effect.durable {
		return effect, errors.New("rendezvous replacement directory sync reported no durability")
	}
	if !effect.recoveryRequired {
		return effect, ErrOwnership
	}
	return effect, nil
}

func (h *Handle) releaseCreated() error {
	var result error
	paths := append([]string(nil), h.paths...)
	for index := len(h.paths) - 1; index >= 0; index-- {
		name := h.paths[index]
		owner, err := Read(name)
		if err == nil && owner == h.owner {
			removed, removeErr := removeOwned(name, h.owner)
			result = errors.Join(result, removeErr)
			if removed && removeErr == nil {
				syncErr := syncLockDirectory(name)
				result = errors.Join(result, syncErr)
				if syncErr == nil {
					h.mutated = true
				}
			}
		}
	}
	h.paths = nil
	residual := false
	for _, name := range paths {
		residual = residual || ownedLockPresent(name, h.owner)
	}
	if result != nil && (h.mutated || residual) {
		return &mutationError{durable: h.mutated, recoveryRequired: residual, err: result}
	}
	return result
}

func ownedLockPresent(name string, expected Owner) bool {
	owner, err := Read(name)
	return err == nil && owner == expected
}

func writerPaths(commonGitDir, worktreeGitDir string, refnames []string) ([]string, error) {
	if commonGitDir == "" || worktreeGitDir == "" || len(refnames) == 0 {
		return nil, fmt.Errorf("writer rendezvous requires Git directories and at least one ref")
	}
	refs := append([]string(nil), refnames...)
	sort.Strings(refs)
	paths := make([]string, 0, len(refs)+1)
	last := ""
	for _, refname := range refs {
		if refname == "" {
			return nil, fmt.Errorf("empty accepted refname")
		}
		if refname == last {
			continue
		}
		last = refname
		paths = append(paths, RefPath(commonGitDir, refname))
	}
	paths = append(paths, WorktreePath(worktreeGitDir))
	return paths, nil
}

func openLockDirectory(name string, create bool) (*os.Root, string, error) {
	name = filepath.Clean(name)
	parent := filepath.Dir(name)
	if gitDir, components, ok := lockGitDirectory(parent); ok {
		root, err := openStableDirectory(gitDir)
		if err != nil {
			return nil, "", err
		}
		for _, component := range components {
			child, err := openStableChild(root, component, create)
			if err != nil {
				root.Close()
				return nil, "", err
			}
			root.Close()
			root = child
		}
		return root, filepath.Base(name), nil
	}
	if create {
		if err := os.MkdirAll(parent, 0o700); err != nil {
			return nil, "", err
		}
	}
	root, err := openStableDirectory(parent)
	return root, filepath.Base(name), err
}

func lockGitDirectory(parent string) (string, []string, bool) {
	components := []string{"engram", "locks"}
	locks := parent
	if filepath.Base(parent) == "refs" {
		locks = filepath.Dir(parent)
		components = append(components, "refs")
	}
	if filepath.Base(locks) != "locks" {
		return "", nil, false
	}
	engram := filepath.Dir(locks)
	if filepath.Base(engram) != "engram" {
		return "", nil, false
	}
	return filepath.Dir(engram), components, true
}

func openStableDirectory(name string) (*os.Root, error) {
	info, err := pinnedFileInfo(os.Lstat(name))
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, fmt.Errorf("unsafe rendezvous administration directory")
	}
	root, err := os.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	opened, err := pinnedFileInfo(root.Stat("."))
	if err != nil || !opened.IsDir() || !os.SameFile(info, opened) {
		root.Close()
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("rendezvous administration directory changed while opening")
	}
	return root, nil
}

func openStableChild(parent *os.Root, name string, create bool) (*os.Root, error) {
	info, err := pinnedFileInfo(parent.Lstat(name))
	if errors.Is(err, os.ErrNotExist) && create {
		mkdirErr := parent.Mkdir(name, 0o700)
		if mkdirErr != nil && !errors.Is(mkdirErr, os.ErrExist) {
			return nil, mkdirErr
		}
		if mkdirErr == nil {
			if err := syncRoot(parent); err != nil {
				return nil, err
			}
		}
		info, err = pinnedFileInfo(parent.Lstat(name))
	}
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, fmt.Errorf("unsafe rendezvous administration path %q", name)
	}
	root, err := parent.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	opened, err := pinnedFileInfo(root.Stat("."))
	if err != nil || !opened.IsDir() || !os.SameFile(info, opened) {
		root.Close()
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("rendezvous administration path %q changed while opening", name)
	}
	return root, nil
}

func stableLockRead(root *os.Root, name string) ([]byte, error) {
	info, err := pinnedFileInfo(root.Lstat(name))
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("unsafe rendezvous file")
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	opened, err := pinnedFileInfo(file.Stat())
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		file.Close()
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("rendezvous file changed while opening")
	}
	data, readErr := io.ReadAll(file)
	after, statErr := pinnedFileInfo(file.Stat())
	closeErr := file.Close()
	if readErr != nil || statErr != nil || closeErr != nil {
		return nil, errors.Join(readErr, statErr, closeErr)
	}
	named, err := pinnedFileInfo(root.Lstat(name))
	if err != nil || named.Mode()&os.ModeSymlink != 0 || !sameFileState(opened, after) || !sameFileState(after, named) {
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("rendezvous file changed while reading")
	}
	return data, nil
}

func sameFileState(left, right os.FileInfo) bool {
	return os.SameFile(left, right) && left.Mode() == right.Mode() && left.Size() == right.Size() && left.ModTime() == right.ModTime()
}

func pinnedFileInfo(info os.FileInfo, err error) (os.FileInfo, error) {
	if err != nil {
		return nil, err
	}
	if err := fileidentity.Pin(info); err != nil {
		return nil, err
	}
	return info, nil
}

func readOwnerAt(root *os.Root, name string) (Owner, error) {
	data, err := stableLockRead(root, name)
	if err != nil {
		return Owner{}, err
	}
	return decodeOwner(data)
}

func createLockTemporary(root *os.Root) (string, *os.File, error) {
	for range 16 {
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			return "", nil, err
		}
		name := ".engram-lock-" + hex.EncodeToString(random[:])
		file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		return name, file, err
	}
	return "", nil, fmt.Errorf("cannot allocate a private rendezvous temporary")
}

func removeOwned(name string, expected Owner) (bool, error) {
	directory, base, err := openLockDirectory(name, false)
	if err != nil {
		return false, err
	}
	defer directory.Close()
	owner, err := readOwnerAt(directory, base)
	if err != nil || owner != expected {
		return false, ErrOwnership
	}
	if err := directory.Remove(base); err != nil {
		return false, err
	}
	return true, nil
}

func syncLockDirectory(name string) error {
	directory, _, err := openLockDirectory(name, false)
	if err != nil {
		return err
	}
	defer directory.Close()
	return syncRoot(directory)
}

func syncRoot(root *os.Root) error {
	if runtime.GOOS == "windows" {
		// Go does not expose a portable directory-flush primitive on Windows;
		// each owned file itself has already been synchronously flushed.
		return nil
	}
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	err = directory.Sync()
	return errors.Join(err, directory.Close())
}
