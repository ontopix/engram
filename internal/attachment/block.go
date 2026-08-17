// Package attachment owns the versioned project memory manifest used by the
// attach and detach workflows. It deliberately knows nothing about Git or
// managed-store validation; callers must validate a store before Attach.
package attachment

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/ontopix/engram/internal/fileidentity"
	"github.com/ontopix/engram/internal/lockidentity"
)

const (
	OpenMarker  = "<!-- engram:attachments:v1 -->"
	CloseMarker = "<!-- /engram:attachments:v1 -->"

	introduction = `# Project memory

This project uses Engram stores for durable agent memory.

Attachments only identify store locations. They do not authorize writes,
hook execution, network access, or synchronization. Before using a store,
read its root README and apply the independently installed ` + "`using-engram`" + ` skill.`
)

// ErrMalformedBlock identifies memory-manifest bytes which look owned by engram
// but cannot be replaced without guessing.
var ErrMalformedBlock = errors.New("malformed or duplicate engram attachment block")

// ErrBusy identifies another cooperating attachment update in progress.
var ErrBusy = errors.New("memory manifest is busy")

type attachedStore struct {
	Path   string `json:"path"`
	README string `json:"readme"`
}

type document struct {
	Version int             `json:"version"`
	Stores  []attachedStore `json:"stores"`
}

// Result is the published local attachment change.
type Result struct {
	Project    string `json:"project"`
	Store      string `json:"store"`
	MemoryFile string `json:"memory_file"`
	Changed    bool   `json:"changed"`
}

// ManagedResult describes reconciliation of the setup-owned attachment
// namespace. Stores outside ManagedRoot are preserved verbatim.
type ManagedResult struct {
	Project     string `json:"project"`
	MemoryFile  string `json:"memory_file"`
	ManagedRoot string `json:"managed_root"`
	Changed     bool   `json:"changed"`
}

// Effect is the closed protocol evidence attached to a failed update. The
// containing EffectError also records that publication may already be visible
// when Durable is false (rename completed but directory sync did not).
type Effect struct {
	Durable          bool
	RecoveryRequired bool
}

// EffectError reports a failed operation which nevertheless published the
// memory manifest or left its cooperating lock in place.
type EffectError struct {
	Effect Effect
	Err    error
}

func (e *EffectError) Error() string {
	if e == nil || e.Err == nil {
		return "attachment update failed after mutation"
	}
	return e.Err.Error()
}

func (e *EffectError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// EffectOf returns the persistent evidence carried by err.
func EffectOf(err error) (Effect, bool) {
	var typed *EffectError
	if !errors.As(err, &typed) || typed == nil {
		return Effect{}, false
	}
	return typed.Effect, true
}

// Updater owns one attachment operation and its instance-local fault seams.
// Package-level Attach and Detach create a fresh production updater.
type Updater struct {
	afterRename           func(string) error
	afterSync             func(string) error
	beforeRemove          func(string) error
	afterRelease          func(string) error
	establishLockIdentity func(*os.File) (lockidentity.Identity, error)
}

func NewUpdater() *Updater { return &Updater{} }

type parsedBlock struct {
	start  int
	end    int
	stores []attachedStore
}

type publication struct {
	visible bool
	durable bool
}

// ResolveProject returns an explicit real directory, or the containing Git
// worktree root when Git recognizes one, otherwise the current directory.
// Git is invoked with network- and hook-irrelevant ambient overlays removed.
func ResolveProject(ctx context.Context, explicit string) (string, error) {
	if explicit != "" {
		return realDirectory(explicit)
	}
	working, err := os.Getwd()
	if err != nil {
		return "", err
	}
	git, err := exec.LookPath("git")
	if err == nil {
		command := exec.CommandContext(ctx, git,
			"-c", "core.longpaths=true",
			"--no-pager", "--no-optional-locks", "--no-replace-objects",
			"-c", "core.hooksPath="+os.DevNull, "-c", "core.fsmonitor=false", "-c", "core.untrackedCache=false",
			"-c", "maintenance.auto=false", "-c", "gc.auto=0",
			"-C", working, "rev-parse", "--path-format=absolute", "--show-toplevel",
		)
		command.Env = isolatedEnvironment(os.Environ())
		output, commandErr := command.Output()
		if commandErr == nil {
			candidate := strings.TrimSuffix(string(output), "\n")
			if utf8.ValidString(candidate) && filepath.IsAbs(candidate) {
				return realDirectory(candidate)
			}
		}
	}
	return realDirectory(working)
}

// ResolveMemoryFile resolves explicit relative to project and proves the
// resulting lexical path remains below the project root.
func ResolveMemoryFile(project, explicit string) (string, error) {
	project, err := realDirectory(project)
	if err != nil {
		return "", err
	}
	if explicit == "" {
		explicit = "MEMORY.md"
	}
	candidate := explicit
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(project, candidate)
	}
	candidate, err = filepath.Abs(candidate)
	if err != nil {
		return "", err
	}
	candidate = filepath.Clean(candidate)
	if resolved, resolveErr := filepath.EvalSymlinks(candidate); resolveErr == nil {
		candidate = resolved
	} else if errors.Is(resolveErr, os.ErrNotExist) {
		resolvedParent, parentErr := filepath.EvalSymlinks(filepath.Dir(candidate))
		if parentErr != nil {
			return "", parentErr
		}
		candidate = filepath.Join(resolvedParent, filepath.Base(candidate))
	} else {
		return "", resolveErr
	}
	relative, err := filepath.Rel(project, candidate)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("memory file must stay below project root")
	}
	if info, statErr := os.Lstat(candidate); statErr == nil && (info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular()) {
		return "", fmt.Errorf("memory file is not a real regular file")
	} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return "", statErr
	}
	return candidate, nil
}

// CanonicalStore resolves a store to its canonical physical directory path.
func CanonicalStore(store string) (string, error) {
	return realDirectory(store)
}

// Attach adds store to the owned block and atomically publishes the result.
func Attach(project, memoryFile, store string) (Result, error) {
	return NewUpdater().Attach(project, memoryFile, store)
}

// Detach removes store from the owned block and atomically publishes the
// result. A missing block or entry is an unchanged success.
func Detach(project, memoryFile, store string) (Result, error) {
	return NewUpdater().Detach(project, memoryFile, store)
}

// PlanManaged reports whether replacing the attachments below managedRoot
// with stores would change the registry. Missing desired stores are accepted
// for planning; callers remain responsible for acquiring and validating them.
func PlanManaged(project, memoryFile, managedRoot string, stores []string) (ManagedResult, error) {
	return NewUpdater().reconcileManaged(project, memoryFile, managedRoot, stores, false)
}

// ReconcileManaged atomically replaces only attachments below managedRoot.
// It preserves attachments outside that namespace and never deletes stores.
// Callers must acquire and validate every desired store at their declared
// validation scope first.
func ReconcileManaged(project, memoryFile, managedRoot string, stores []string) (ManagedResult, error) {
	return NewUpdater().reconcileManaged(project, memoryFile, managedRoot, stores, true)
}

func (u *Updater) Attach(project, memoryFile, store string) (Result, error) {
	return u.update(project, memoryFile, store, true)
}

func (u *Updater) Detach(project, memoryFile, store string) (Result, error) {
	return u.update(project, memoryFile, store, false)
}

func (u *Updater) update(project, memoryFile, store string, attach bool) (result Result, resultErr error) {
	if u == nil {
		return Result{}, fmt.Errorf("attachment updater is nil")
	}
	var err error
	project, err = realDirectory(project)
	if err != nil {
		return Result{}, err
	}
	memoryFile, err = ResolveMemoryFile(project, memoryFile)
	if err != nil {
		return Result{}, err
	}
	if attach {
		store, err = CanonicalStore(store)
	} else {
		store, err = detachStorePath(store)
	}
	if err != nil {
		return Result{}, err
	}
	result = Result{Project: project, Store: store, MemoryFile: memoryFile}

	lock, err := acquireLockWith(memoryFile+".engram.lock", u.establishLockIdentity)
	if err != nil {
		return Result{}, err
	}
	published := publication{}
	defer func() {
		residual, releaseErr := lock.release(u.beforeRemove, u.afterRelease)
		if releaseErr != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("release attachment lock: %w", releaseErr))
		}
		if resultErr != nil {
			result = Result{}
			resultErr = withEffect(resultErr, published, residual)
		}
	}()

	original, originalInfo, err := readOptional(memoryFile)
	if err != nil {
		return Result{}, err
	}
	block, present, err := parse(original)
	if err != nil {
		return Result{}, err
	}
	stores := []attachedStore(nil)
	if present {
		stores = append(stores, block.stores...)
	}
	if err := validatePhysicalDuplicates(project, stores); err != nil {
		return Result{}, err
	}

	matching := matchingStores(project, stores, store)
	if attach {
		if len(matching) == 0 {
			stores = append(stores, describeStore(project, store))
		}
	} else if len(matching) != 0 {
		kept := stores[:0]
		for index, existing := range stores {
			if !matching[index] {
				kept = append(kept, existing)
			}
		}
		stores = kept
	}
	sort.Slice(stores, func(left, right int) bool { return stores[left].Path < stores[right].Path })

	updated, err := replace(original, block, present, stores, false)
	if err != nil {
		return Result{}, err
	}
	if bytes.Equal(original, updated) {
		return result, nil
	}
	published, err = u.publish(memoryFile, original, originalInfo, updated)
	if err != nil {
		return Result{}, err
	}
	result.Changed = true
	return result, nil
}

func (u *Updater) reconcileManaged(project, memoryFile, managedRoot string, stores []string, publish bool) (result ManagedResult, resultErr error) {
	if u == nil {
		return ManagedResult{}, fmt.Errorf("attachment updater is nil")
	}
	var err error
	project, err = realDirectory(project)
	if err != nil {
		return ManagedResult{}, err
	}
	memoryFile, err = ResolveMemoryFile(project, memoryFile)
	if err != nil {
		return ManagedResult{}, err
	}
	managedRoot, err = resolveManagedRoot(project, managedRoot)
	if err != nil {
		return ManagedResult{}, err
	}
	desired, err := normalizeManagedStores(project, managedRoot, stores, !publish)
	if err != nil {
		return ManagedResult{}, err
	}
	result = ManagedResult{Project: project, MemoryFile: memoryFile, ManagedRoot: managedRoot}

	var lock *lockFile
	if publish {
		lock, err = acquireLockWith(memoryFile+".engram.lock", u.establishLockIdentity)
		if err != nil {
			return ManagedResult{}, err
		}
		published := publication{}
		defer func() {
			residual, releaseErr := lock.release(u.beforeRemove, u.afterRelease)
			if releaseErr != nil {
				resultErr = errors.Join(resultErr, fmt.Errorf("release attachment lock: %w", releaseErr))
			}
			if resultErr != nil {
				result = ManagedResult{}
				resultErr = withEffect(resultErr, published, residual)
			}
		}()

		original, originalInfo, readErr := readOptional(memoryFile)
		if readErr != nil {
			return ManagedResult{}, readErr
		}
		updated, updateErr := reconcileManagedBytes(project, managedRoot, original, desired)
		if updateErr != nil {
			return ManagedResult{}, updateErr
		}
		if bytes.Equal(original, updated) {
			return result, nil
		}
		published, err = u.publish(memoryFile, original, originalInfo, updated)
		if err != nil {
			return ManagedResult{}, err
		}
		result.Changed = true
		return result, nil
	}

	original, _, err := readOptional(memoryFile)
	if err != nil {
		return ManagedResult{}, err
	}
	updated, err := reconcileManagedBytes(project, managedRoot, original, desired)
	if err != nil {
		return ManagedResult{}, err
	}
	result.Changed = !bytes.Equal(original, updated)
	return result, nil
}

func reconcileManagedBytes(project, managedRoot string, original []byte, desired []string) ([]byte, error) {
	block, present, err := parse(original)
	if err != nil {
		return nil, err
	}
	stores := []attachedStore(nil)
	if present {
		stores = append(stores, block.stores...)
	}
	if err := validatePhysicalDuplicates(project, stores); err != nil {
		return nil, err
	}

	kept := make([]attachedStore, 0, len(stores)+len(desired))
	for _, existing := range stores {
		if !storedPathBelow(project, existing.Path, managedRoot) {
			kept = append(kept, existing)
		}
	}
	for _, store := range desired {
		kept = append(kept, describeStore(project, store))
	}
	sort.Slice(kept, func(left, right int) bool { return kept[left].Path < kept[right].Path })
	seen := make(map[string]struct{}, len(kept))
	for _, store := range kept {
		if _, duplicate := seen[store.Path]; duplicate {
			return nil, ErrMalformedBlock
		}
		seen[store.Path] = struct{}{}
	}
	if err := validatePhysicalDuplicates(project, kept); err != nil {
		return nil, err
	}
	return replace(original, block, present, kept, true)
}

func resolveManagedRoot(project, root string) (string, error) {
	if root == "" || !utf8.ValidString(root) {
		return "", fmt.Errorf("managed attachment root is empty or not UTF-8")
	}
	if !filepath.IsAbs(root) {
		root = filepath.Join(project, root)
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	absolute = filepath.Clean(absolute)
	info, statErr := os.Lstat(absolute)
	switch {
	case statErr == nil:
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return "", fmt.Errorf("managed attachment root is not a real directory")
		}
		absolute, err = filepath.EvalSymlinks(absolute)
		if err != nil {
			return "", err
		}
	case errors.Is(statErr, os.ErrNotExist):
		parent, parentErr := filepath.EvalSymlinks(filepath.Dir(absolute))
		if parentErr != nil {
			return "", parentErr
		}
		absolute = filepath.Join(parent, filepath.Base(absolute))
	default:
		return "", statErr
	}
	absolute = filepath.Clean(absolute)
	if !pathBelow(absolute, project) {
		return "", fmt.Errorf("managed attachment root must stay below project root")
	}
	return absolute, nil
}

func normalizeManagedStores(project, managedRoot string, stores []string, allowMissing bool) ([]string, error) {
	result := make([]string, 0, len(stores))
	seen := make(map[string]struct{}, len(stores))
	for _, store := range stores {
		if store == "" || !utf8.ValidString(store) {
			return nil, fmt.Errorf("managed store path is empty or not UTF-8")
		}
		if !filepath.IsAbs(store) {
			store = filepath.Join(project, store)
		}
		absolute, err := filepath.Abs(store)
		if err != nil {
			return nil, err
		}
		absolute = filepath.Clean(absolute)
		info, statErr := os.Lstat(absolute)
		switch {
		case statErr == nil:
			if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
				return nil, fmt.Errorf("managed store is not a real directory: %s", store)
			}
			absolute, err = filepath.EvalSymlinks(absolute)
			if err != nil {
				return nil, err
			}
		case errors.Is(statErr, os.ErrNotExist) && allowMissing:
			// Planning uses the exact lexical child which setup will acquire.
		case errors.Is(statErr, os.ErrNotExist):
			return nil, fmt.Errorf("managed store does not exist: %s", store)
		default:
			return nil, statErr
		}
		absolute = filepath.Clean(absolute)
		if !pathBelow(absolute, managedRoot) {
			return nil, fmt.Errorf("managed store must stay below managed attachment root")
		}
		key := filepath.ToSlash(absolute)
		if _, duplicate := seen[key]; duplicate {
			return nil, fmt.Errorf("duplicate managed store path %q", key)
		}
		seen[key] = struct{}{}
		result = append(result, absolute)
	}
	return result, nil
}

func pathBelow(name, directory string) bool {
	relative, err := filepath.Rel(directory, name)
	return err == nil && relative != "." && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func storedPathBelow(project, stored, directory string) bool {
	candidate := filepath.FromSlash(stored)
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(project, candidate)
	}
	absolute, err := filepath.Abs(candidate)
	if err != nil {
		return false
	}
	return pathBelow(filepath.Clean(absolute), directory)
}

func withEffect(err error, published publication, residual bool) error {
	if err == nil || !published.visible && !residual {
		return err
	}
	return &EffectError{
		Effect: Effect{Durable: published.durable, RecoveryRequired: residual},
		Err:    err,
	}
}

func detachStorePath(store string) (string, error) {
	absolute, err := filepath.Abs(store)
	if err != nil {
		return "", err
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err == nil {
		info, statErr := os.Stat(canonical)
		if statErr != nil {
			return "", statErr
		}
		if !info.IsDir() {
			return "", fmt.Errorf("%s is not a directory", store)
		}
		return filepath.Clean(canonical), nil
	}
	if errors.Is(err, os.ErrNotExist) {
		resolvedParent, parentErr := filepath.EvalSymlinks(filepath.Dir(absolute))
		if parentErr == nil {
			return filepath.Join(resolvedParent, filepath.Base(absolute)), nil
		}
		if errors.Is(parentErr, os.ErrNotExist) {
			return filepath.Clean(absolute), nil
		}
		return "", parentErr
	}
	return "", err
}

func parse(source []byte) (parsedBlock, bool, error) {
	openLine := []byte(OpenMarker + "\n")
	closeLine := []byte(CloseMarker + "\n")
	opens := wholeLineOffsets(source, openLine)
	closes := wholeLineOffsets(source, closeLine)
	if len(opens) == 0 && len(closes) == 0 {
		return parsedBlock{}, false, nil
	}
	if len(opens) != 1 || len(closes) != 1 || opens[0] >= closes[0] {
		return parsedBlock{}, false, ErrMalformedBlock
	}
	end := closes[0] + len(closeLine)
	blockBytes := source[opens[0]:end]
	prefix := OpenMarker + "\n" + introduction + "\n\n```json\n"
	suffix := "\n```\n" + CloseMarker + "\n"
	if !bytes.HasPrefix(blockBytes, []byte(prefix)) || !bytes.HasSuffix(blockBytes, []byte(suffix)) {
		return parsedBlock{}, false, ErrMalformedBlock
	}
	payload := blockBytes[len(prefix) : len(blockBytes)-len(suffix)]
	if err := rejectDuplicateJSONFields(payload); err != nil {
		return parsedBlock{}, false, ErrMalformedBlock
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var decoded document
	if err := decoder.Decode(&decoded); err != nil || decoder.Decode(&struct{}{}) != io.EOF || decoded.Version != 1 || decoded.Stores == nil {
		return parsedBlock{}, false, ErrMalformedBlock
	}
	seen := make(map[string]struct{}, len(decoded.Stores))
	for _, store := range decoded.Stores {
		if !validStoredPath(store.Path) || store.README != storeREADME(store.Path) {
			return parsedBlock{}, false, ErrMalformedBlock
		}
		if _, exists := seen[store.Path]; exists {
			return parsedBlock{}, false, ErrMalformedBlock
		}
		seen[store.Path] = struct{}{}
	}
	return parsedBlock{start: opens[0], end: end, stores: decoded.Stores}, true, nil
}

func rejectDuplicateJSONFields(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	var walk func() error
	walk = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delimiter {
		case '{':
			seen := make(map[string]struct{})
			for decoder.More() {
				fieldToken, fieldErr := decoder.Token()
				field, fieldOK := fieldToken.(string)
				if fieldErr != nil || !fieldOK {
					return ErrMalformedBlock
				}
				if _, duplicate := seen[field]; duplicate {
					return ErrMalformedBlock
				}
				seen[field] = struct{}{}
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		case '[':
			for decoder.More() {
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		default:
			return ErrMalformedBlock
		}
	}
	return walk()
}

func replace(original []byte, block parsedBlock, present bool, stores []attachedStore, ensure bool) ([]byte, error) {
	if len(stores) == 0 && !present && !ensure {
		return append([]byte(nil), original...), nil
	}
	encoded, err := encode(stores)
	if err != nil {
		return nil, err
	}
	if present {
		result := make([]byte, 0, len(original)-(block.end-block.start)+len(encoded))
		result = append(result, original[:block.start]...)
		result = append(result, encoded...)
		result = append(result, original[block.end:]...)
		return result, nil
	}
	result := append([]byte(nil), original...)
	if len(result) != 0 && result[len(result)-1] != '\n' {
		result = append(result, '\n')
	}
	if len(result) != 0 && !bytes.HasSuffix(result, []byte("\n\n")) {
		result = append(result, '\n')
	}
	return append(result, encoded...), nil
}

func encode(stores []attachedStore) ([]byte, error) {
	var payload bytes.Buffer
	encoder := json.NewEncoder(&payload)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(document{Version: 1, Stores: stores}); err != nil {
		return nil, err
	}
	payloadBytes := bytes.TrimSuffix(payload.Bytes(), []byte("\n"))
	var result bytes.Buffer
	fmt.Fprintf(&result, "%s\n%s\n\n```json\n", OpenMarker, introduction)
	result.Write(payloadBytes)
	fmt.Fprintf(&result, "\n```\n%s\n", CloseMarker)
	return result.Bytes(), nil
}

func wholeLineOffsets(source, line []byte) []int {
	var result []int
	for start := 0; start < len(source); {
		endRelative := bytes.IndexByte(source[start:], '\n')
		if endRelative < 0 {
			break
		}
		end := start + endRelative + 1
		if bytes.Equal(source[start:end], line) {
			result = append(result, start)
		}
		start = end
	}
	return result
}

func matchingStores(project string, stores []attachedStore, wanted string) map[int]bool {
	result := make(map[int]bool)
	wantedInfo, wantedErr := os.Stat(wanted)
	for index, existing := range stores {
		existingPath := resolveStoredPath(project, existing.Path)
		if existingPath == wanted {
			result[index] = true
			continue
		}
		existingInfo, err := os.Stat(existingPath)
		if wantedErr == nil && err == nil && os.SameFile(wantedInfo, existingInfo) {
			result[index] = true
		}
	}
	return result
}

func validatePhysicalDuplicates(project string, stores []attachedStore) error {
	for left := range stores {
		leftInfo, leftErr := os.Stat(resolveStoredPath(project, stores[left].Path))
		if leftErr != nil {
			continue
		}
		for right := left + 1; right < len(stores); right++ {
			rightInfo, rightErr := os.Stat(resolveStoredPath(project, stores[right].Path))
			if rightErr == nil && os.SameFile(leftInfo, rightInfo) {
				return ErrMalformedBlock
			}
		}
	}
	return nil
}

func describeStore(project, store string) attachedStore {
	stored := filepath.ToSlash(store)
	if relative, err := filepath.Rel(project, store); err == nil {
		stored = filepath.ToSlash(relative)
	}
	return attachedStore{Path: stored, README: storeREADME(stored)}
}

func resolveStoredPath(project, stored string) string {
	candidate := filepath.FromSlash(stored)
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(project, candidate)
	}
	absolute, err := filepath.Abs(candidate)
	if err != nil {
		return filepath.Clean(candidate)
	}
	if canonical, resolveErr := filepath.EvalSymlinks(absolute); resolveErr == nil {
		return filepath.Clean(canonical)
	}
	return filepath.Clean(absolute)
}

func validStoredPath(value string) bool {
	if value == "" || !utf8.ValidString(value) || strings.ContainsAny(value, "\\\x00\r\n") {
		return false
	}
	return filepath.ToSlash(filepath.Clean(filepath.FromSlash(value))) == value
}

func storeREADME(store string) string {
	if store == "." {
		return "README.md"
	}
	return strings.TrimSuffix(store, "/") + "/README.md"
}

func realDirectory(name string) (string, error) {
	absolute, err := filepath.Abs(name)
	if err != nil {
		return "", err
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory", name)
	}
	return filepath.Clean(canonical), nil
}

func readOptional(name string) ([]byte, os.FileInfo, error) {
	info, err := os.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	if err := fileidentity.Pin(info); err != nil {
		return nil, nil, fmt.Errorf("capture memory manifest identity: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, nil, fmt.Errorf("memory manifest is not a real regular file")
	}
	data, err := os.ReadFile(name)
	return data, info, err
}

func (u *Updater) publish(name string, original []byte, originalInfo os.FileInfo, updated []byte) (publication, error) {
	current, currentInfo, err := readOptional(name)
	if err != nil {
		return publication{}, err
	}
	if !bytes.Equal(current, original) || originalInfo == nil != (currentInfo == nil) || originalInfo != nil && !os.SameFile(originalInfo, currentInfo) {
		return publication{}, fmt.Errorf("%w: memory manifest changed concurrently", ErrBusy)
	}
	directory := filepath.Dir(name)
	temporary, err := os.CreateTemp(directory, ".engram-memory-*")
	if err != nil {
		return publication{}, err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	mode := os.FileMode(0o644)
	if originalInfo != nil {
		mode = originalInfo.Mode().Perm()
	}
	if err := temporary.Chmod(mode); err != nil {
		temporary.Close()
		return publication{}, err
	}
	if _, err := temporary.Write(updated); err != nil {
		temporary.Close()
		return publication{}, err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return publication{}, err
	}
	if err := temporary.Close(); err != nil {
		return publication{}, err
	}
	if err := os.Rename(temporaryName, name); err != nil {
		return publication{}, err
	}
	result := publication{visible: true}
	if u.afterRename != nil {
		if err := u.afterRename(name); err != nil {
			return result, err
		}
	}
	result.durable, err = syncAttachmentDirectory(directory)
	if err != nil {
		return result, err
	}
	if u.afterSync != nil {
		if err := u.afterSync(name); err != nil {
			return result, err
		}
	}
	return result, nil
}

func syncAttachmentDirectory(directory string) (bool, error) {
	if runtime.GOOS == "windows" {
		return true, nil
	}
	handle, err := os.Open(directory)
	if err != nil {
		return false, err
	}
	syncErr := handle.Sync()
	closeErr := handle.Close()
	if syncErr != nil {
		return false, errors.Join(syncErr, closeErr)
	}
	return true, closeErr
}

type lockFile struct {
	name     string
	file     *os.File
	identity lockidentity.Identity
}

func acquireLock(name string) (*lockFile, error) {
	return acquireLockWith(name, nil)
}

func acquireLockWith(name string, establish func(*os.File) (lockidentity.Identity, error)) (*lockFile, error) {
	file, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return nil, ErrBusy
	}
	if err != nil {
		return nil, err
	}
	if establish == nil {
		establish = lockidentity.Establish
	}
	identity, identityErr := establish(file)
	if identityErr != nil {
		closeErr := file.Close()
		return nil, &EffectError{
			Effect: Effect{RecoveryRequired: true},
			Err:    errors.Join(fmt.Errorf("establish attachment lock identity: %w", identityErr), closeErr),
		}
	}
	return &lockFile{name: name, file: file, identity: identity}, nil
}

func (l *lockFile) release(beforeRemove, afterRelease func(string) error) (bool, error) {
	if l == nil {
		return false, nil
	}
	var ownershipErr error
	var closeErr error
	if l.file != nil {
		_, ownershipErr = l.file.Stat()
		closeErr = l.file.Close()
	}
	var beforeRemoveErr error
	if beforeRemove != nil {
		beforeRemoveErr = beforeRemove(l.name)
	}
	var removeErr error
	ownedAtName := false
	var identityErr error
	if beforeRemoveErr == nil && ownershipErr == nil {
		state, inspectErr := l.identity.Inspect(l.name)
		switch {
		case inspectErr != nil:
			identityErr = inspectErr
		case state == lockidentity.Owned:
			ownedAtName = true
		case state == lockidentity.Other:
			identityErr = fmt.Errorf("%w: attachment lock ownership changed before release", ErrBusy)
		}
	}
	if ownedAtName {
		removeErr = os.Remove(l.name)
		if errors.Is(removeErr, os.ErrNotExist) {
			removeErr = nil
		}
	}
	var syncErr error
	if ownedAtName && removeErr == nil {
		_, syncErr = syncAttachmentDirectory(filepath.Dir(l.name))
	}
	var faultErr error
	if beforeRemoveErr == nil && ownershipErr == nil && identityErr == nil && removeErr == nil && afterRelease != nil {
		faultErr = afterRelease(l.name)
	}
	residual := false
	var inspectErr error
	state, inspectErr := l.identity.Inspect(l.name)
	switch state {
	case lockidentity.Owned:
		residual = true
	case lockidentity.Other:
		if inspectErr != nil {
			residual = true
		}
	}
	var residualErr error
	if residual {
		residualErr = fmt.Errorf("%w: attachment lock remains after release", ErrBusy)
	}
	return residual, errors.Join(ownershipErr, closeErr, beforeRemoveErr, identityErr, removeErr, syncErr, faultErr, inspectErr, residualErr)
}

func isolatedEnvironment(environment []string) []string {
	result := make([]string, 0, len(environment)+7)
	for _, item := range environment {
		name, _, _ := strings.Cut(item, "=")
		upper := strings.ToUpper(name)
		if strings.HasPrefix(upper, "GIT_") || strings.HasPrefix(upper, "ENGRAM_") {
			continue
		}
		result = append(result, item)
	}
	return append(result,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_CONFIG_COUNT=0",
		"GIT_NO_LAZY_FETCH=1",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_OPTIONAL_LOCKS=0",
		"GIT_PAGER=cat",
	)
}
