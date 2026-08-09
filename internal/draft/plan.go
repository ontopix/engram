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
}

type directoryEdit struct {
	path    string
	mode    fs.FileMode
	applied bool
}

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
	if locker != nil {
		var err error
		unlock, err = locker.LockDraft(ctx, p.root)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return typed(ErrorCancelled, p.operation, "", err)
			}
			return typed(ErrorConcurrency, p.operation, "", fmt.Errorf("%w: acquire worktree rendezvous: %v", ErrConcurrent, err))
		}
		if unlock == nil {
			return typed(ErrorInternal, p.operation, "", errors.New("locker returned a nil unlock function"))
		}
		defer func() {
			if err := unlock(); err != nil {
				resultErr = typed(ErrorConflict, p.operation, "", fmt.Errorf("%w: release worktree rendezvous: %v; operation result: %v", ErrRecoveryRequired, err, resultErr))
			}
		}()
	}

	root, err := os.OpenRoot(p.root)
	if err != nil {
		return typed(ErrorIO, p.operation, ".", err)
	}
	defer root.Close()
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

	if err := p.prepare(root); err != nil {
		if cleanupErr := p.cleanupTemporaries(root); cleanupErr != nil {
			return typed(ErrorConflict, p.operation, "", fmt.Errorf("%w: preparation: %v; cleanup: %v", ErrRecoveryRequired, err, cleanupErr))
		}
		return err
	}
	defer func() { _ = p.cleanupTemporaries(root) }()

	appliedFiles := make([]int, 0, len(p.files))
	appliedDirs := make([]int, 0, len(p.dirs))
	fail := func(cause error) error {
		rollbackErr := p.rollback(root, appliedFiles, appliedDirs)
		cleanupErr := p.cleanupTemporaries(root)
		if rollbackErr != nil || cleanupErr != nil {
			return typed(ErrorConflict, p.operation, "", fmt.Errorf("%w: cause: %v; rollback: %v; cleanup: %v", ErrRecoveryRequired, cause, rollbackErr, cleanupErr))
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
		edit.applied = true
		appliedDirs = append(appliedDirs, index)
		if err := syncDirectory(root, parentLogical(edit.path)); err != nil {
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
			edit.applied = true
			appliedFiles = append(appliedFiles, index)
		} else if edit.before.kind == observationAbsent {
			if err := root.Link(native(edit.temporary), native(edit.path)); err != nil {
				if errors.Is(err, fs.ErrExist) {
					return fail(concurrencyError(p.operation, edit.path))
				}
				return fail(typed(ErrorIO, p.operation, edit.path, err))
			}
			edit.applied = true
			appliedFiles = append(appliedFiles, index)
			if err := root.Remove(native(edit.temporary)); err != nil {
				return fail(typed(ErrorIO, p.operation, edit.path, err))
			}
			edit.temporary = ""
		} else {
			if err := root.Rename(native(edit.temporary), native(edit.path)); err != nil {
				return fail(typed(ErrorIO, p.operation, edit.path, err))
			}
			edit.temporary = ""
			edit.applied = true
			appliedFiles = append(appliedFiles, index)
		}
		if err := syncDirectory(root, parentLogical(edit.path)); err != nil {
			return fail(typed(ErrorIO, p.operation, edit.path, err))
		}
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

func (p *Plan) prepare(root *os.Root) error {
	for index := range p.files {
		edit := &p.files[index]
		if edit.delete {
			continue
		}
		temporary, err := createTemporary(root, edit.staging, edit.mode, edit.after)
		if err != nil {
			return typed(ErrorIO, p.operation, edit.path, err)
		}
		edit.temporary = temporary
	}
	return nil
}

func (p *Plan) cleanupTemporaries(root *os.Root) error {
	var failures []error
	for index := range p.files {
		if p.files[index].temporary != "" {
			if err := root.Remove(native(p.files[index].temporary)); err != nil && !errors.Is(err, fs.ErrNotExist) {
				failures = append(failures, fmt.Errorf("remove temporary %s: %w", p.files[index].temporary, err))
				continue
			}
			p.files[index].temporary = ""
		}
	}
	return errors.Join(failures...)
}

func (p *Plan) rollback(root *os.Root, fileIndexes, directoryIndexes []int) error {
	var failures []error
	for position := len(fileIndexes) - 1; position >= 0; position-- {
		edit := &p.files[fileIndexes[position]]
		if !edit.applied {
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
			temporary, err := createTemporary(root, parentLogical(edit.path), edit.before.mode.Perm(), edit.before.data)
			if err != nil {
				failures = append(failures, fmt.Errorf("prepare rollback %s: %w", edit.path, err))
				continue
			}
			if err := root.Link(native(temporary), native(edit.path)); err != nil {
				_ = root.Remove(native(temporary))
				failures = append(failures, fmt.Errorf("restore %s: %w", edit.path, err))
				continue
			}
			if err := root.Remove(native(temporary)); err != nil {
				failures = append(failures, fmt.Errorf("remove rollback temporary for %s: %w", edit.path, err))
				continue
			}
		} else if edit.before.kind == observationAbsent {
			if current.kind == observationAbsent {
				edit.applied = false
				continue
			}
			if current.kind != observationRegular || !bytes.Equal(current.data, edit.after) || current.mode.Perm() != edit.mode.Perm() {
				failures = append(failures, fmt.Errorf("%s no longer equals its planned final image", edit.path))
				continue
			}
			if err := root.Remove(native(edit.path)); err != nil {
				failures = append(failures, fmt.Errorf("remove %s: %w", edit.path, err))
				continue
			}
		} else {
			if sameImage(edit.before, current) {
				edit.applied = false
				continue
			}
			if current.kind != observationRegular || !bytes.Equal(current.data, edit.after) || current.mode.Perm() != edit.mode.Perm() {
				failures = append(failures, fmt.Errorf("%s no longer equals its planned final image", edit.path))
				continue
			}
			temporary, err := createTemporary(root, parentLogical(edit.path), edit.before.mode.Perm(), edit.before.data)
			if err != nil {
				failures = append(failures, fmt.Errorf("prepare rollback %s: %w", edit.path, err))
				continue
			}
			if err := root.Rename(native(temporary), native(edit.path)); err != nil {
				_ = root.Remove(native(temporary))
				failures = append(failures, fmt.Errorf("restore %s: %w", edit.path, err))
				continue
			}
		}
		edit.applied = false
		if err := syncDirectory(root, parentLogical(edit.path)); err != nil {
			failures = append(failures, fmt.Errorf("sync rollback %s: %w", edit.path, err))
		}
	}
	for position := len(directoryIndexes) - 1; position >= 0; position-- {
		edit := &p.dirs[directoryIndexes[position]]
		if !edit.applied {
			continue
		}
		if err := root.Remove(native(edit.path)); err != nil {
			failures = append(failures, fmt.Errorf("remove created directory %s: %w", edit.path, err))
			continue
		}
		edit.applied = false
		if err := syncDirectory(root, parentLogical(edit.path)); err != nil {
			failures = append(failures, fmt.Errorf("sync rollback directory %s: %w", edit.path, err))
		}
	}
	return errors.Join(failures...)
}

func createTemporary(root *os.Root, directory string, mode fs.FileMode, content []byte) (string, error) {
	for attempt := 0; attempt < 32; attempt++ {
		var random [12]byte
		if _, err := rand.Read(random[:]); err != nil {
			return "", err
		}
		name := ".engram-draft-" + hex.EncodeToString(random[:])
		logicalPath := joinLogical(directory, name)
		file, err := root.OpenFile(native(logicalPath), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		failed := func(err error) (string, error) {
			_ = file.Close()
			_ = root.Remove(native(logicalPath))
			return "", err
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
			_ = root.Remove(native(logicalPath))
			return "", err
		}
		return logicalPath, nil
	}
	return "", errors.New("could not reserve a temporary filename")
}

func syncDirectory(root *os.Root, directory string) error {
	file, err := root.Open(native(directory))
	if err != nil {
		return err
	}
	syncErr := file.Sync()
	closeErr := file.Close()
	// Windows does not expose a portable directory-fsync operation. File
	// content itself was synced before publication; the exact preimage checks
	// and rollback guarantees remain available there.
	if runtime.GOOS == "windows" {
		syncErr = nil
	}
	return errors.Join(syncErr, closeErr)
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
