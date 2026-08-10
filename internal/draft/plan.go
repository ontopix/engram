package draft

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/ontopix/engram/internal/snapshot"
)

type observationKind uint8

const (
	observationAbsent observationKind = iota
	observationRegular
	observationDirectory
	observationSymlink
	observationSpecial
)

type observedEntry struct {
	name string
	kind observationKind
}

type observation struct {
	path    string
	kind    observationKind
	mode    fs.FileMode
	info    fs.FileInfo
	data    []byte
	entries []observedEntry
}

type fileEdit struct {
	path    string
	before  observation
	after   []byte
	mode    fs.FileMode
	staging string
	delete  bool

	temporary string
	applied   bool
	durable   bool
}

type directoryEdit struct {
	path    string
	mode    fs.FileMode
	applied bool
	durable bool
}

// Phase identifies a deterministic per-plan fault boundary.
type Phase string

const (
	PhaseApplied  Phase = "applied"
	PhaseRollback Phase = "rollback"
	PhaseCleanup  Phase = "cleanup"
)

// Plan is a one-shot, immutable publication plan. Its exported methods return
// copies so callers cannot invalidate captured preimages or final bytes.
type Plan struct {
	root      string
	rootInfo  fs.FileInfo
	operation string
	captures  map[string]observation
	files     []fileEdit
	dirs      []directoryEdit

	mu        sync.Mutex
	published bool

	// beforeApply is a deterministic fault-injection point used by package
	// tests. It is intentionally not part of the public API.
	beforeApply func(index int, logicalPath string) error
	// fault is also per-plan: parallel tests and callers never share mutable
	// package-global fault state.
	fault func(Phase, int, string) error
	// syncDirectory is an instance-local durability seam. Production plans use
	// the package implementation; tests can fail one exact publication boundary.
	syncDirectory func(*os.Root, string) (bool, error)
}

// Root returns the absolute store root captured by the plan.
func (p *Plan) Root() string {
	if p == nil {
		return ""
	}
	return p.root
}

// Operation returns the canonical helper name: fmt, new, mv, or schema.copy.
func (p *Plan) Operation() string {
	if p == nil {
		return ""
	}
	return p.operation
}

// Changed reports whether publication would replace or create any file.
func (p *Plan) Changed() bool { return p != nil && len(p.files) != 0 }

// Changes returns planned file updates in publication order.
func (p *Plan) Changes() []FileChange {
	if p == nil {
		return nil
	}
	result := make([]FileChange, len(p.files))
	for index, edit := range p.files {
		result[index] = FileChange{
			Path: edit.path, Before: append([]byte(nil), edit.before.data...),
			After: append([]byte(nil), edit.after...), Create: edit.before.kind == observationAbsent,
			Delete: edit.delete,
		}
	}
	return result
}

func (p *Plan) checkpoint(phase Phase, index int, logicalPath string) error {
	if p == nil || p.fault == nil {
		return nil
	}
	return p.fault(phase, index, logicalPath)
}

func (p *Plan) sync(root *os.Root, directory string) (bool, error) {
	if p != nil && p.syncDirectory != nil {
		return p.syncDirectory(root, directory)
	}
	return syncDirectory(root, directory)
}

func (p *Plan) mutation(durable, checkoutChanged, recoveryRequired bool) Mutation {
	return Mutation{Durable: durable, CheckoutChanged: checkoutChanged, RecoveryRequired: recoveryRequired}
}

func errorKind(err error, fallback ErrorKind) ErrorKind {
	if kind := KindOf(err); kind != "" {
		return kind
	}
	return fallback
}

// Publish applies a plan without an external rendezvous. Managed callers
// should normally call PublishWith with their worktree Locker.
func (p *Plan) Publish(ctx context.Context) error { return p.PublishWith(ctx, nil) }

// PublishWith acquires locker, rechecks every exact captured input, publishes
// through synced temporary regular files, and rolls back on any later error.
func (p *Plan) PublishWith(ctx context.Context, locker Locker) (resultErr error) {
	if p == nil {
		return typed(ErrorInternal, "publish", "", errors.New("plan is nil"))
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.published {
		return typed(ErrorConflict, p.operation, "", errors.New("plan was already published"))
	}
	if err := cancelled(ctx, p.operation); err != nil {
		return err
	}

	var unlock Unlock
	durableEffect := false
	checkoutEffect := false
	if locker != nil {
		var err error
		unlock, err = locker.LockDraft(ctx, p.root)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return typed(ErrorCancelled, p.operation, "", err)
			}
			return typed(ErrorConcurrency, p.operation, "", errors.Join(ErrConcurrent, fmt.Errorf("acquire worktree rendezvous: %w", err)))
		}
		if unlock == nil {
			return typed(ErrorInternal, p.operation, "", errors.New("locker returned a nil unlock function"))
		}
		defer func() {
			if err := unlock(); err != nil {
				p.published = true
				mutation := p.mutation(durableEffect, checkoutEffect, false)
				// A successful LockDraft call establishes that the rendezvous was
				// durably acquired. A failed unlock therefore carries one known
				// durable workflow mutation even when draft bytes were restored. A
				// managed adapter can additionally report whether its exact lock path
				// remains; an opaque locker failure is conservatively blocking.
				mutation.Durable = true
				if release, present := MutationOf(err); present {
					mutation.Durable = mutation.Durable || release.Durable
					mutation.CheckoutChanged = mutation.CheckoutChanged || release.CheckoutChanged
					mutation.RecoveryRequired = release.RecoveryRequired
				} else {
					mutation.RecoveryRequired = true
				}
				cause := errors.Join(fmt.Errorf("release worktree rendezvous: %w", err), resultErr)
				if mutation.RecoveryRequired {
					cause = errors.Join(ErrRecoveryRequired, cause)
				}
				resultErr = mutationError(ErrorConflict, p.operation, cause, mutation)
			}
		}()
	}

	root, err := os.OpenRoot(p.root)
	if err != nil {
		return typed(ErrorIO, p.operation, ".", err)
	}
	defer func() {
		if closeErr := root.Close(); closeErr != nil {
			closeFailure := typed(ErrorIO, p.operation, ".", fmt.Errorf("close store root: %w", closeErr))
			mutation := p.mutation(durableEffect, checkoutEffect, false)
			if mutation.Durable || mutation.CheckoutChanged {
				resultErr = errors.Join(resultErr, mutationError(ErrorIO, p.operation, closeFailure, mutation))
			} else {
				resultErr = errors.Join(resultErr, closeFailure)
			}
		}
	}()
	currentRoot, err := os.Lstat(p.root)
	if err != nil || currentRoot.Mode()&os.ModeSymlink != 0 || !currentRoot.IsDir() || !os.SameFile(p.rootInfo, currentRoot) {
		if err == nil {
			err = ErrConcurrent
		}
		return typed(ErrorConcurrency, p.operation, ".", fmt.Errorf("%w: store root identity changed: %v", ErrConcurrent, err))
	}
	if err := p.recheck(root); err != nil {
		return err
	}
	if len(p.files) == 0 && len(p.dirs) == 0 {
		p.published = true
		return nil
	}

	if err := p.prepare(root, &checkoutEffect); err != nil {
		if cleanupErr := p.cleanupTemporaries(root, &checkoutEffect, &durableEffect); cleanupErr != nil {
			p.published = true
			return mutationError(ErrorConflict, p.operation, errors.Join(
				ErrRecoveryRequired, fmt.Errorf("preparation: %w", err), fmt.Errorf("cleanup: %w", cleanupErr),
			), p.mutation(durableEffect, checkoutEffect, false))
		}
		if checkoutEffect {
			return mutationError(errorKind(err, ErrorIO), p.operation, err, p.mutation(durableEffect, true, false))
		}
		return err
	}

	appliedFiles := make([]int, 0, len(p.files))
	appliedDirs := make([]int, 0, len(p.dirs))
	fail := func(cause error) error {
		rollbackErr := p.rollback(root, appliedFiles, appliedDirs, &checkoutEffect, &durableEffect)
		cleanupErr := p.cleanupTemporaries(root, &checkoutEffect, &durableEffect)
		if rollbackErr != nil || cleanupErr != nil {
			p.published = true
			return mutationError(ErrorConflict, p.operation, errors.Join(ErrRecoveryRequired, cause, rollbackErr, cleanupErr),
				p.mutation(durableEffect, checkoutEffect, false))
		}
		if checkoutEffect {
			return mutationError(errorKind(cause, ErrorConflict), p.operation, cause, p.mutation(durableEffect, true, false))
		}
		return cause
	}

	for index := range p.dirs {
		if err := cancelled(ctx, p.operation); err != nil {
			return fail(err)
		}
		edit := &p.dirs[index]
		if p.beforeApply != nil {
			if err := p.beforeApply(index, edit.path); err != nil {
				return fail(typed(ErrorIO, p.operation, edit.path, err))
			}
		}
		current, err := observe(root, edit.path, false, false)
		if err != nil {
			return fail(typed(ErrorIO, p.operation, edit.path, err))
		}
		if current.kind != observationAbsent {
			return fail(concurrencyError(p.operation, edit.path))
		}
		if err := root.Mkdir(native(edit.path), edit.mode); err != nil {
			if errors.Is(err, fs.ErrExist) {
				return fail(concurrencyError(p.operation, edit.path))
			}
			return fail(typed(ErrorIO, p.operation, edit.path, err))
		}
		checkoutEffect = true
		edit.applied = true
		appliedDirs = append(appliedDirs, index)
		durable, syncErr := p.sync(root, parentLogical(edit.path))
		if durable {
			edit.durable = true
			durableEffect = true
		}
		if syncErr != nil {
			return fail(typed(ErrorIO, p.operation, edit.path, syncErr))
		}
		if !durable {
			return fail(typed(ErrorIO, p.operation, edit.path, errors.New("draft directory sync reported no durability")))
		}
		if err := p.checkpoint(PhaseApplied, index, edit.path); err != nil {
			return fail(typed(ErrorIO, p.operation, edit.path, err))
		}
	}

	for index := range p.files {
		if err := cancelled(ctx, p.operation); err != nil {
			return fail(err)
		}
		edit := &p.files[index]
		if p.beforeApply != nil {
			if err := p.beforeApply(len(p.dirs)+index, edit.path); err != nil {
				return fail(typed(ErrorIO, p.operation, edit.path, err))
			}
		}
		current, err := observe(root, edit.path, true, false)
		if err != nil {
			return fail(typed(ErrorIO, p.operation, edit.path, err))
		}
		if !sameObservation(edit.before, current) {
			return fail(concurrencyError(p.operation, edit.path))
		}
		if edit.delete {
			if err := root.Remove(native(edit.path)); err != nil {
				return fail(typed(ErrorIO, p.operation, edit.path, err))
			}
			checkoutEffect = true
			edit.applied = true
			appliedFiles = append(appliedFiles, index)
		} else if edit.before.kind == observationAbsent {
			if err := root.Link(native(edit.temporary), native(edit.path)); err != nil {
				if errors.Is(err, fs.ErrExist) {
					return fail(concurrencyError(p.operation, edit.path))
				}
				return fail(typed(ErrorIO, p.operation, edit.path, err))
			}
			checkoutEffect = true
			edit.applied = true
			appliedFiles = append(appliedFiles, index)
			if err := root.Remove(native(edit.temporary)); err != nil {
				return fail(typed(ErrorIO, p.operation, edit.path, err))
			}
			checkoutEffect = true
			edit.temporary = ""
		} else {
			if err := root.Rename(native(edit.temporary), native(edit.path)); err != nil {
				return fail(typed(ErrorIO, p.operation, edit.path, err))
			}
			checkoutEffect = true
			edit.temporary = ""
			edit.applied = true
			appliedFiles = append(appliedFiles, index)
		}
		durable, syncErr := p.sync(root, parentLogical(edit.path))
		if durable {
			edit.durable = true
			durableEffect = true
		}
		if syncErr != nil {
			return fail(typed(ErrorIO, p.operation, edit.path, syncErr))
		}
		if !durable {
			return fail(typed(ErrorIO, p.operation, edit.path, errors.New("draft directory sync reported no durability")))
		}
		if err := p.checkpoint(PhaseApplied, len(p.dirs)+index, edit.path); err != nil {
			return fail(typed(ErrorIO, p.operation, edit.path, err))
		}
	}

	if err := p.cleanupTemporaries(root, &checkoutEffect, &durableEffect); err != nil {
		return fail(typed(ErrorIO, p.operation, "", err))
	}
	p.published = true
	return nil
}

func (p *Plan) recheck(root *os.Root) error {
	names := make([]string, 0, len(p.captures))
	for name := range p.captures {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool { return bytes.Compare([]byte(names[i]), []byte(names[j])) < 0 })
	for _, name := range names {
		expected := p.captures[name]
		current, err := observe(root, name, expected.kind == observationRegular, expected.entries != nil)
		if err != nil {
			return typed(ErrorIO, p.operation, name, err)
		}
		if !sameObservation(expected, current) {
			return concurrencyError(p.operation, name)
		}
	}
	return nil
}

func (p *Plan) prepare(root *os.Root, checkoutChanged *bool) error {
	for index := range p.files {
		edit := &p.files[index]
		if edit.delete {
			continue
		}
		temporary, created, err := createTemporary(root, edit.staging, edit.mode, edit.after)
		if created {
			*checkoutChanged = true
		}
		if temporary != "" {
			edit.temporary = temporary
		}
		if err != nil {
			return typed(ErrorIO, p.operation, edit.path, err)
		}
	}
	return nil
}

func (p *Plan) cleanupTemporaries(root *os.Root, checkoutChanged, durableEffect *bool) error {
	var failures []error
	dirtyDirectories := make(map[string]struct{})
	for index := range p.files {
		if p.files[index].temporary != "" {
			if err := p.checkpoint(PhaseCleanup, index, p.files[index].path); err != nil {
				failures = append(failures, fmt.Errorf("clean temporary for %s: %w", p.files[index].path, err))
				continue
			}
			removeErr := root.Remove(native(p.files[index].temporary))
			if removeErr != nil && !errors.Is(removeErr, fs.ErrNotExist) {
				failures = append(failures, fmt.Errorf("remove temporary %s: %w", p.files[index].temporary, removeErr))
				continue
			}
			if removeErr == nil {
				*checkoutChanged = true
				dirtyDirectories[parentLogical(p.files[index].temporary)] = struct{}{}
			}
			p.files[index].temporary = ""
		}
	}
	directories := make([]string, 0, len(dirtyDirectories))
	for directory := range dirtyDirectories {
		directories = append(directories, directory)
	}
	sort.Slice(directories, func(left, right int) bool {
		leftDepth := strings.Count(directories[left], "/")
		rightDepth := strings.Count(directories[right], "/")
		if leftDepth != rightDepth {
			return leftDepth > rightDepth
		}
		return directories[left] < directories[right]
	})
	for _, directory := range directories {
		durable, err := p.sync(root, directory)
		if durable {
			*durableEffect = true
		}
		if err != nil {
			failures = append(failures, fmt.Errorf("sync temporary cleanup in %s: %w", directory, err))
		} else if !durable {
			failures = append(failures, fmt.Errorf("sync temporary cleanup in %s: no durability reported", directory))
		}
	}
	return errors.Join(failures...)
}

func (p *Plan) rollback(root *os.Root, fileIndexes, directoryIndexes []int, checkoutChanged, durableEffect *bool) error {
	var failures []error
	for position := len(fileIndexes) - 1; position >= 0; position-- {
		edit := &p.files[fileIndexes[position]]
		if !edit.applied {
			continue
		}
		if err := p.checkpoint(PhaseRollback, fileIndexes[position], edit.path); err != nil {
			failures = append(failures, fmt.Errorf("roll back %s: %w", edit.path, err))
			continue
		}
		current, err := observe(root, edit.path, true, false)
		if err != nil {
			failures = append(failures, fmt.Errorf("observe %s: %w", edit.path, err))
			continue
		}
		if edit.delete {
			if current.kind != observationAbsent {
				failures = append(failures, fmt.Errorf("%s was recreated before rollback", edit.path))
				continue
			}
			temporary, created, err := createTemporary(root, parentLogical(edit.path), edit.before.mode.Perm(), edit.before.data)
			if created {
				*checkoutChanged = true
			}
			if temporary != "" {
				edit.temporary = temporary
			}
			if err != nil {
				failures = append(failures, fmt.Errorf("prepare rollback %s: %w", edit.path, err))
				continue
			}
			if err := root.Link(native(temporary), native(edit.path)); err != nil {
				failures = append(failures, fmt.Errorf("restore %s: %w", edit.path, err))
				continue
			}
			*checkoutChanged = true
			removeErr := root.Remove(native(temporary))
			if removeErr != nil && !errors.Is(removeErr, fs.ErrNotExist) {
				failures = append(failures, fmt.Errorf("remove rollback temporary for %s: %w", edit.path, removeErr))
			} else {
				if removeErr == nil {
					*checkoutChanged = true
				}
				edit.temporary = ""
			}
		} else if edit.before.kind == observationAbsent {
			if current.kind == observationAbsent {
				edit.applied = false
				edit.durable = false
				continue
			}
			if current.kind != observationRegular || !bytes.Equal(current.data, edit.after) || !equivalentPermissions(current.mode, edit.mode) {
				failures = append(failures, fmt.Errorf("%s no longer equals its planned final image", edit.path))
				continue
			}
			if err := root.Remove(native(edit.path)); err != nil {
				failures = append(failures, fmt.Errorf("remove %s: %w", edit.path, err))
				continue
			}
			*checkoutChanged = true
		} else {
			if sameImage(edit.before, current) {
				edit.applied = false
				edit.durable = false
				continue
			}
			if current.kind != observationRegular || !bytes.Equal(current.data, edit.after) || !equivalentPermissions(current.mode, edit.mode) {
				failures = append(failures, fmt.Errorf("%s no longer equals its planned final image", edit.path))
				continue
			}
			temporary, created, err := createTemporary(root, parentLogical(edit.path), edit.before.mode.Perm(), edit.before.data)
			if created {
				*checkoutChanged = true
			}
			if temporary != "" {
				edit.temporary = temporary
			}
			if err != nil {
				failures = append(failures, fmt.Errorf("prepare rollback %s: %w", edit.path, err))
				continue
			}
			if err := root.Rename(native(temporary), native(edit.path)); err != nil {
				failures = append(failures, fmt.Errorf("restore %s: %w", edit.path, err))
				continue
			}
			*checkoutChanged = true
			edit.temporary = ""
		}
		edit.applied = false
		edit.durable = false
		durable, err := p.sync(root, parentLogical(edit.path))
		if durable {
			*durableEffect = true
		}
		if err != nil {
			failures = append(failures, fmt.Errorf("sync rollback %s: %w", edit.path, err))
		} else if !durable {
			failures = append(failures, fmt.Errorf("sync rollback %s: no durability reported", edit.path))
		}
	}
	for position := len(directoryIndexes) - 1; position >= 0; position-- {
		edit := &p.dirs[directoryIndexes[position]]
		if !edit.applied {
			continue
		}
		if err := p.checkpoint(PhaseRollback, directoryIndexes[position], edit.path); err != nil {
			failures = append(failures, fmt.Errorf("roll back directory %s: %w", edit.path, err))
			continue
		}
		if err := root.Remove(native(edit.path)); err != nil {
			failures = append(failures, fmt.Errorf("remove created directory %s: %w", edit.path, err))
			continue
		}
		*checkoutChanged = true
		edit.applied = false
		edit.durable = false
		durable, err := p.sync(root, parentLogical(edit.path))
		if durable {
			*durableEffect = true
		}
		if err != nil {
			failures = append(failures, fmt.Errorf("sync rollback directory %s: %w", edit.path, err))
		} else if !durable {
			failures = append(failures, fmt.Errorf("sync rollback directory %s: no durability reported", edit.path))
		}
	}
	return errors.Join(failures...)
}

func createTemporary(root *os.Root, directory string, mode fs.FileMode, content []byte) (string, bool, error) {
	for attempt := 0; attempt < 32; attempt++ {
		var random [12]byte
		if _, err := rand.Read(random[:]); err != nil {
			return "", false, err
		}
		name := ".engram-draft-" + hex.EncodeToString(random[:])
		logicalPath := joinLogical(directory, name)
		file, err := root.OpenFile(native(logicalPath), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return "", false, err
		}
		failed := func(cause error) (string, bool, error) {
			closeErr := file.Close()
			removeErr := root.Remove(native(logicalPath))
			if removeErr != nil && !errors.Is(removeErr, fs.ErrNotExist) {
				return logicalPath, true, errors.Join(cause, closeErr, removeErr)
			}
			return "", true, errors.Join(cause, closeErr)
		}
		if err := file.Chmod(mode.Perm()); err != nil {
			return failed(err)
		}
		if _, err := file.Write(content); err != nil {
			return failed(err)
		}
		if err := file.Sync(); err != nil {
			return failed(err)
		}
		if err := file.Close(); err != nil {
			removeErr := root.Remove(native(logicalPath))
			if removeErr != nil && !errors.Is(removeErr, fs.ErrNotExist) {
				return logicalPath, true, errors.Join(err, removeErr)
			}
			return "", true, err
		}
		return logicalPath, true, nil
	}
	return "", false, errors.New("could not reserve a temporary filename")
}

func syncDirectory(root *os.Root, directory string) (bool, error) {
	file, err := root.Open(native(directory))
	if err != nil {
		return false, err
	}
	syncErr := file.Sync()
	closeErr := file.Close()
	// Windows does not expose a portable directory-fsync operation. File
	// content itself was synced before publication; the exact preimage checks
	// and rollback guarantees remain available there.
	if runtime.GOOS == "windows" {
		syncErr = nil
	}
	return syncErr == nil, errors.Join(syncErr, closeErr)
}

func observe(root *os.Root, logicalPath string, readData, readEntries bool) (observation, error) {
	name := native(logicalPath)
	info, err := root.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return observation{path: logicalPath, kind: observationAbsent}, nil
	}
	if err != nil {
		return observation{}, err
	}
	result := observation{path: logicalPath, kind: modeKind(info.Mode()), mode: info.Mode(), info: info}
	if readData {
		if result.kind != observationRegular {
			return result, nil
		}
		data, err := root.ReadFile(name)
		if err != nil {
			return observation{}, err
		}
		after, err := root.Lstat(name)
		if err != nil {
			return observation{}, err
		}
		if after.Mode()&os.ModeSymlink != 0 || !after.Mode().IsRegular() || !os.SameFile(info, after) {
			return observation{}, fmt.Errorf("file changed while being observed")
		}
		result.data = data
		result.info = after
		result.mode = after.Mode()
	}
	if readEntries {
		if result.kind != observationDirectory {
			return result, nil
		}
		file, err := root.Open(name)
		if err != nil {
			return observation{}, err
		}
		entries, readErr := file.ReadDir(-1)
		closeErr := file.Close()
		if readErr != nil || closeErr != nil {
			return observation{}, errors.Join(readErr, closeErr)
		}
		result.entries = make([]observedEntry, 0, len(entries))
		for _, entry := range entries {
			childName := joinLogical(logicalPath, entry.Name())
			childInfo, err := root.Lstat(native(childName))
			if err != nil {
				return observation{}, err
			}
			result.entries = append(result.entries, observedEntry{name: entry.Name(), kind: modeKind(childInfo.Mode())})
		}
		sort.Slice(result.entries, func(i, j int) bool {
			return bytes.Compare([]byte(result.entries[i].name), []byte(result.entries[j].name)) < 0
		})
		after, err := root.Lstat(name)
		if err != nil || !after.IsDir() || !os.SameFile(info, after) {
			if err == nil {
				err = errors.New("directory changed while being observed")
			}
			return observation{}, err
		}
		result.info = after
		result.mode = after.Mode()
	}
	return result, nil
}

func sameObservation(expected, current observation) bool {
	if expected.kind != current.kind || expected.mode != current.mode {
		return false
	}
	if expected.kind == observationAbsent {
		return true
	}
	if expected.info == nil || current.info == nil || !os.SameFile(expected.info, current.info) {
		return false
	}
	if expected.kind == observationRegular && !bytes.Equal(expected.data, current.data) {
		return false
	}
	if expected.entries != nil {
		if len(expected.entries) != len(current.entries) {
			return false
		}
		for index := range expected.entries {
			if expected.entries[index] != current.entries[index] {
				return false
			}
		}
	}
	return true
}

func sameImage(expected, current observation) bool {
	if expected.kind != current.kind || expected.mode != current.mode {
		return false
	}
	if expected.kind == observationRegular {
		return bytes.Equal(expected.data, current.data)
	}
	return expected.kind == observationAbsent
}

func modeKind(mode fs.FileMode) observationKind {
	switch {
	case mode&os.ModeSymlink != 0:
		return observationSymlink
	case mode.IsRegular():
		return observationRegular
	case mode.IsDir():
		return observationDirectory
	default:
		return observationSpecial
	}
}

func concurrencyError(operation, logicalPath string) error {
	return typed(ErrorConcurrency, operation, logicalPath, ErrConcurrent)
}

func native(logicalPath string) string {
	if logicalPath == "." {
		return "."
	}
	return filepath.FromSlash(logicalPath)
}

func joinLogical(directory, name string) string {
	if directory == "." {
		return name
	}
	return path.Join(directory, name)
}

func parentLogical(name string) string {
	parent := path.Dir(name)
	if parent == "" {
		return "."
	}
	return parent
}

func snapshotKind(kind snapshot.Kind) observationKind {
	switch kind {
	case snapshot.KindRegular:
		return observationRegular
	case snapshot.KindDirectory:
		return observationDirectory
	case snapshot.KindSymlink:
		return observationSymlink
	default:
		return observationSpecial
	}
}
