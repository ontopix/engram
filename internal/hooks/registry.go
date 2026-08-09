package hooks

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

const maxRegistryBytes = 8 << 20

var (
	// ErrCorruptRegistry identifies controller state which is malformed,
	// ambiguous, internally inconsistent, or from an unsupported version.
	ErrCorruptRegistry = errors.New("corrupt hook trust registry")
	// ErrConcurrent identifies a cooperating update or an observed change to
	// controller state while an operation was in progress.
	ErrConcurrent = errors.New("hook trust registry changed concurrently")
	// ErrUnsafePermissions identifies controller state writable by another
	// host account or readable outside its owner.
	ErrUnsafePermissions = errors.New("unsafe hook trust registry permissions")
)

// Registry owns complete-set grants in one controller-selected external file.
// Construction resolves existing path aliases but creates and writes nothing.
type Registry struct {
	path string

	// beforePublish is an internal fault/concurrency seam used by tests. It is
	// called after mutation and before the compare-and-replace publication.
	beforePublish func()
}

// Selection reports one complete selected set and its local trust state.
// Changed is true only for Trust when a new grant was durably published.
type Selection struct {
	Changed bool   `json:"changed"`
	SHA256  string `json:"sha256"`
	Trusted bool   `json:"trusted"`
	Hooks   []Hook `json:"hooks"`
}

// RevokeResult reports every historical complete-set digest removed, in
// ASCII order.
type RevokeResult struct {
	Changed     bool     `json:"changed"`
	RevokedSets []string `json:"revoked_sets"`
}

type registryDocument struct {
	Version int           `json:"version"`
	Stores  []storeGrants `json:"stores"`
}

type storeGrants struct {
	Identity StoreIdentity `json:"identity"`
	Grants   []grant       `json:"grants"`
}

type grant struct {
	SHA256 string `json:"sha256"`
	Hooks  []Hook `json:"hooks"`
}

type fileSnapshot struct {
	bytes []byte
	info  os.FileInfo
}

// NewRegistry returns a handle for a controller-owned trust file. The path is
// canonicalized through every existing ancestor so a repository symlink
// cannot disguise in-store configuration.
func NewRegistry(name string) (*Registry, error) {
	if name == "" {
		return nil, fmt.Errorf("hook trust registry path is empty")
	}
	canonical, err := canonicalFuturePath(name)
	if err != nil {
		return nil, err
	}
	if filepath.Base(canonical) == "." || filepath.Base(canonical) == string(filepath.Separator) {
		return nil, fmt.Errorf("hook trust registry path names a directory")
	}
	return &Registry{path: canonical}, nil
}

// Path returns the canonical external configuration path.
func (r *Registry) Path() string {
	if r == nil {
		return ""
	}
	return r.path
}

// List reports whether the exact complete selected set is trusted for the
// current physical store binding. It never writes configuration or executes a
// hook. The empty set is trusted intrinsically after identity validation.
func (r *Registry) List(store string, set Set) (Selection, error) {
	selection, identity, err := r.prepare(store, set)
	if err != nil {
		return Selection{}, err
	}
	document, _, err := r.read()
	if err != nil {
		return Selection{}, err
	}
	selection.Trusted = len(set.Hooks) == 0 || document.trusted(identity, set)
	return selection, nil
}

// Trust explicitly authorizes one complete non-empty selected set for the
// current physical store binding. Trusting an already trusted set, or the
// intrinsically trusted empty set, is an unchanged success.
func (r *Registry) Trust(store string, set Set) (Selection, error) {
	selection, identity, err := r.prepare(store, set)
	if err != nil {
		return Selection{}, err
	}
	selection.Trusted = true
	if len(set.Hooks) == 0 {
		return selection, nil
	}
	changed, err := r.update(func(document *registryDocument) (bool, error) {
		if document.trusted(identity, set) {
			return false, nil
		}
		storeIndex := document.storeIndex(identity)
		if storeIndex < 0 {
			document.Stores = append(document.Stores, storeGrants{Identity: identity, Grants: []grant{grantFrom(set)}})
		} else {
			for _, existing := range document.Stores[storeIndex].Grants {
				if existing.SHA256 == set.SHA256 {
					return false, fmt.Errorf("%w: set digest collision", ErrCorruptRegistry)
				}
			}
			document.Stores[storeIndex].Grants = append(document.Stores[storeIndex].Grants, grantFrom(set))
		}
		document.sort()
		return true, nil
	})
	if err != nil {
		return Selection{}, err
	}
	selection.Changed = changed
	return selection, nil
}

// Revoke removes every historical grant for the current physical store which
// contains any named direct hook. With no names it removes all grants for the
// store. Duplicate names collapse. It never executes a hook.
func (r *Registry) Revoke(store string, names ...string) (RevokeResult, error) {
	if r == nil {
		return RevokeResult{}, fmt.Errorf("hook trust registry is nil")
	}
	identity, err := ResolveStoreIdentity(store)
	if err != nil {
		return RevokeResult{}, err
	}
	if err := ensureExternalConfig(identity, r.path); err != nil {
		return RevokeResult{}, err
	}
	wanted := make(map[string]struct{}, len(names))
	for _, name := range names {
		if strings.Contains(name, "/") || strings.Contains(name, "\\") || !validFilename(name) {
			return RevokeResult{}, fmt.Errorf("invalid direct hook filename %q", name)
		}
		wanted[name] = struct{}{}
	}
	result := RevokeResult{RevokedSets: []string{}}
	changed, err := r.update(func(document *registryDocument) (bool, error) {
		storeIndex := document.storeIndex(identity)
		if storeIndex < 0 {
			return false, nil
		}
		current := document.Stores[storeIndex]
		kept := make([]grant, 0, len(current.Grants))
		for _, existing := range current.Grants {
			revoke := len(wanted) == 0 || grantContains(existing, wanted)
			if revoke {
				result.RevokedSets = append(result.RevokedSets, existing.SHA256)
			} else {
				kept = append(kept, existing)
			}
		}
		if len(result.RevokedSets) == 0 {
			return false, nil
		}
		if len(kept) == 0 {
			document.Stores = append(document.Stores[:storeIndex], document.Stores[storeIndex+1:]...)
		} else {
			document.Stores[storeIndex].Grants = kept
		}
		document.sort()
		sort.Strings(result.RevokedSets)
		return true, nil
	})
	if err != nil {
		return RevokeResult{}, err
	}
	result.Changed = changed
	return result, nil
}

func (r *Registry) prepare(store string, set Set) (Selection, StoreIdentity, error) {
	if r == nil {
		return Selection{}, StoreIdentity{}, fmt.Errorf("hook trust registry is nil")
	}
	if err := set.Valid(); err != nil {
		return Selection{}, StoreIdentity{}, err
	}
	identity, err := ResolveStoreIdentity(store)
	if err != nil {
		return Selection{}, StoreIdentity{}, err
	}
	if err := ensureExternalConfig(identity, r.path); err != nil {
		return Selection{}, StoreIdentity{}, err
	}
	return Selection{SHA256: set.SHA256, Hooks: cloneHooks(set.Hooks)}, identity, nil
}

func (r *Registry) update(mutate func(*registryDocument) (bool, error)) (bool, error) {
	if err := ensurePrivateDirectory(filepath.Dir(r.path)); err != nil {
		return false, err
	}
	lock, err := acquireRegistryLock(r.path + ".lock")
	if err != nil {
		return false, err
	}
	defer lock.release()

	document, original, err := r.read()
	if err != nil {
		return false, err
	}
	changed, err := mutate(&document)
	if err != nil || !changed {
		return changed, err
	}
	serialized, err := encodeRegistry(document)
	if err != nil {
		return false, err
	}
	if r.beforePublish != nil {
		r.beforePublish()
	}
	if err := r.publish(original, serialized); err != nil {
		return false, err
	}
	return true, nil
}

func (r *Registry) read() (registryDocument, fileSnapshot, error) {
	original, err := readRegistryFile(r.path)
	if err != nil {
		return registryDocument{}, fileSnapshot{}, err
	}
	if original.info == nil {
		return registryDocument{Version: 1, Stores: []storeGrants{}}, original, nil
	}
	document, err := decodeRegistry(original.bytes)
	return document, original, err
}

func readRegistryFile(name string) (fileSnapshot, error) {
	before, err := os.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return fileSnapshot{}, nil
	}
	if err != nil {
		return fileSnapshot{}, err
	}
	if before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() {
		return fileSnapshot{}, fmt.Errorf("%w: registry is not a real regular file", ErrCorruptRegistry)
	}
	if !privateRegistryFileMode(before.Mode()) {
		return fileSnapshot{}, ErrUnsafePermissions
	}
	file, err := os.Open(name)
	if err != nil {
		return fileSnapshot{}, err
	}
	opened, err := file.Stat()
	if err != nil {
		file.Close()
		return fileSnapshot{}, err
	}
	if !os.SameFile(before, opened) {
		file.Close()
		return fileSnapshot{}, ErrConcurrent
	}
	reader := io.LimitReader(file, maxRegistryBytes+1)
	content, readErr := io.ReadAll(reader)
	closeErr := file.Close()
	if readErr != nil {
		return fileSnapshot{}, readErr
	}
	if closeErr != nil {
		return fileSnapshot{}, closeErr
	}
	if len(content) > maxRegistryBytes {
		return fileSnapshot{}, fmt.Errorf("%w: registry exceeds size limit", ErrCorruptRegistry)
	}
	after, err := os.Lstat(name)
	if err != nil || after.Mode()&os.ModeSymlink != 0 || !os.SameFile(opened, after) || before.Mode().Perm() != after.Mode().Perm() {
		return fileSnapshot{}, ErrConcurrent
	}
	return fileSnapshot{bytes: content, info: opened}, nil
}

func decodeRegistry(content []byte) (registryDocument, error) {
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	var document registryDocument
	if err := decoder.Decode(&document); err != nil {
		return registryDocument{}, fmt.Errorf("%w: %v", ErrCorruptRegistry, err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return registryDocument{}, err
	}
	if err := document.validate(); err != nil {
		return registryDocument{}, err
	}
	canonical, err := encodeRegistry(document)
	if err != nil {
		return registryDocument{}, err
	}
	if !bytes.Equal(content, canonical) {
		return registryDocument{}, fmt.Errorf("%w: registry is not in canonical serialization", ErrCorruptRegistry)
	}
	return document, nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("%w: trailing JSON value", ErrCorruptRegistry)
		}
		return fmt.Errorf("%w: %v", ErrCorruptRegistry, err)
	}
	return nil
}

func encodeRegistry(document registryDocument) ([]byte, error) {
	if err := document.validate(); err != nil {
		return nil, err
	}
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(document); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func (document registryDocument) validate() error {
	if document.Version != 1 || document.Stores == nil {
		return fmt.Errorf("%w: unsupported or incomplete registry document", ErrCorruptRegistry)
	}
	for storeIndex, stored := range document.Stores {
		if !validateIdentity(stored.Identity) || stored.Grants == nil || len(stored.Grants) == 0 {
			return fmt.Errorf("%w: invalid store binding", ErrCorruptRegistry)
		}
		if storeIndex != 0 && document.Stores[storeIndex-1].Identity.key() >= stored.Identity.key() {
			return fmt.Errorf("%w: duplicate or unordered store binding", ErrCorruptRegistry)
		}
		for grantIndex, existing := range stored.Grants {
			if err := existing.validate(); err != nil {
				return err
			}
			if grantIndex != 0 && stored.Grants[grantIndex-1].SHA256 >= existing.SHA256 {
				return fmt.Errorf("%w: duplicate or unordered grant", ErrCorruptRegistry)
			}
		}
	}
	return nil
}

func (existing grant) validate() error {
	if !validDigest(existing.SHA256) || len(existing.Hooks) == 0 {
		return fmt.Errorf("%w: invalid grant", ErrCorruptRegistry)
	}
	for index, hook := range existing.Hooks {
		if path.Dir(hook.Path) != programDirectory || !validFilename(path.Base(hook.Path)) || !validInterpreterToken(hook.Interpreter) || !validDigest(hook.SHA256) || hook.Bytes != nil {
			return fmt.Errorf("%w: invalid stored hook description", ErrCorruptRegistry)
		}
		if index != 0 && bytes.Compare([]byte(existing.Hooks[index-1].Path), []byte(hook.Path)) >= 0 {
			return fmt.Errorf("%w: duplicate or unordered stored hooks", ErrCorruptRegistry)
		}
	}
	canonical, err := canonicalDescriptions(existing.Hooks)
	if err != nil {
		return fmt.Errorf("%w: invalid set serialization", ErrCorruptRegistry)
	}
	digest := sha256.Sum256(canonical)
	if existing.SHA256 != hex.EncodeToString(digest[:]) {
		return fmt.Errorf("%w: inconsistent set digest", ErrCorruptRegistry)
	}
	return nil
}

func validInterpreterToken(token string) bool {
	if token == "" {
		return false
	}
	for index := 0; index < len(token); index++ {
		character := token[index]
		if character != '.' && character != '_' && character != '+' && character != '-' &&
			(character < 'a' || character > 'z') && (character < 'A' || character > 'Z') &&
			(character < '0' || character > '9') {
			return false
		}
	}
	return true
}

func (document registryDocument) storeIndex(identity StoreIdentity) int {
	for index, stored := range document.Stores {
		if stored.Identity == identity {
			return index
		}
	}
	return -1
}

func (document registryDocument) trusted(identity StoreIdentity, set Set) bool {
	storeIndex := document.storeIndex(identity)
	if storeIndex < 0 {
		return false
	}
	for _, existing := range document.Stores[storeIndex].Grants {
		if existing.SHA256 == set.SHA256 && sameDescriptions(existing.Hooks, set.Hooks) {
			return true
		}
	}
	return false
}

func (document *registryDocument) sort() {
	for index := range document.Stores {
		sort.Slice(document.Stores[index].Grants, func(left, right int) bool {
			return document.Stores[index].Grants[left].SHA256 < document.Stores[index].Grants[right].SHA256
		})
	}
	sort.Slice(document.Stores, func(left, right int) bool {
		return document.Stores[left].Identity.key() < document.Stores[right].Identity.key()
	})
}

func grantFrom(set Set) grant {
	hooks := cloneHooks(set.Hooks)
	for index := range hooks {
		hooks[index].Bytes = nil
	}
	return grant{SHA256: set.SHA256, Hooks: hooks}
}

func sameDescriptions(left, right []Hook) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].Path != right[index].Path || left[index].Interpreter != right[index].Interpreter || left[index].SHA256 != right[index].SHA256 {
			return false
		}
	}
	return true
}

func grantContains(existing grant, wanted map[string]struct{}) bool {
	for _, hook := range existing.Hooks {
		if _, found := wanted[path.Base(hook.Path)]; found {
			return true
		}
	}
	return false
}

func ensurePrivateDirectory(directory string) error {
	info, err := os.Lstat(directory)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return err
		}
		info, err = os.Lstat(directory)
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("hook trust registry parent is not a real directory")
	}
	if !safeRegistryDirectoryMode(info.Mode()) {
		return ErrUnsafePermissions
	}
	return nil
}

func (r *Registry) publish(original fileSnapshot, updated []byte) error {
	current, err := readRegistryFile(r.path)
	if err != nil {
		return err
	}
	if !sameFileSnapshot(original, current) {
		return ErrConcurrent
	}
	directory := filepath.Dir(r.path)
	temporary, err := os.CreateTemp(directory, ".engram-hook-trust-*")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	mode := os.FileMode(0o600)
	if original.info != nil {
		mode = original.info.Mode().Perm()
	}
	if err := temporary.Chmod(mode); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(updated); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	current, err = readRegistryFile(r.path)
	if err != nil {
		return err
	}
	if !sameFileSnapshot(original, current) {
		return ErrConcurrent
	}
	if err := os.Rename(temporaryName, r.path); err != nil {
		return err
	}
	return syncRegistryDirectory(directory)
}

func sameFileSnapshot(left, right fileSnapshot) bool {
	if !bytes.Equal(left.bytes, right.bytes) || (left.info == nil) != (right.info == nil) {
		return false
	}
	if left.info == nil {
		return true
	}
	return os.SameFile(left.info, right.info) && left.info.Mode().Perm() == right.info.Mode().Perm()
}

type registryLock struct {
	name string
	file *os.File
}

func acquireRegistryLock(name string) (*registryLock, error) {
	file, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return nil, ErrConcurrent
	}
	if err != nil {
		return nil, err
	}
	return &registryLock{name: name, file: file}, nil
}

func (lock *registryLock) release() {
	if lock == nil {
		return
	}
	if lock.file != nil {
		_ = lock.file.Close()
	}
	_ = os.Remove(lock.name)
}
