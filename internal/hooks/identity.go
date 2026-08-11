package hooks

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

var (
	// ErrPhysicalIdentity means the selected store could not be bound to a
	// stable host filesystem object.
	ErrPhysicalIdentity = errors.New("physical store identity unavailable")
	// ErrConfigInsideStore prevents repository-controlled bytes from granting
	// authority to repository hook programs.
	ErrConfigInsideStore = errors.New("hook trust configuration must be outside the store")
)

// StoreIdentity binds trust to both the canonical physical location and the
// directory object at that location. Path changes (moves/copies) and object
// replacement therefore require a fresh grant, while symlink aliases resolve
// to the same binding.
type StoreIdentity struct {
	Path   string `json:"path"`
	FileID string `json:"file_id"`
}

// ResolveStoreIdentity resolves aliases, proves the target is a real
// directory, and captures its persistent filesystem identity.
func ResolveStoreIdentity(name string) (StoreIdentity, error) {
	if name == "" {
		return StoreIdentity{}, fmt.Errorf("%w: empty store path", ErrPhysicalIdentity)
	}
	absolute, err := filepath.Abs(name)
	if err != nil {
		return StoreIdentity{}, err
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return StoreIdentity{}, err
	}
	canonical = filepath.Clean(canonical)
	if !utf8.ValidString(canonical) {
		return StoreIdentity{}, fmt.Errorf("%w: store path is not UTF-8", ErrPhysicalIdentity)
	}
	info, err := os.Lstat(canonical)
	if err != nil {
		return StoreIdentity{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return StoreIdentity{}, fmt.Errorf("%w: selected store is not a real directory", ErrPhysicalIdentity)
	}
	handle, err := os.Open(canonical)
	if err != nil {
		return StoreIdentity{}, err
	}
	defer handle.Close()
	opened, err := handle.Stat()
	if err != nil {
		return StoreIdentity{}, err
	}
	current, err := os.Stat(canonical)
	if err != nil {
		return StoreIdentity{}, err
	}
	if !opened.IsDir() || !os.SameFile(opened, current) {
		return StoreIdentity{}, fmt.Errorf("%w: store changed while resolving identity", ErrConcurrent)
	}
	physical, ok := persistentFileID(handle, opened)
	if !ok {
		return StoreIdentity{}, ErrPhysicalIdentity
	}
	digest := sha256.Sum256([]byte(physical))
	return StoreIdentity{Path: canonical, FileID: hex.EncodeToString(digest[:])}, nil
}

func (identity StoreIdentity) key() string {
	return identity.Path + "\x00" + identity.FileID
}

func validateIdentity(identity StoreIdentity) bool {
	return identity.Path != "" && utf8.ValidString(identity.Path) && filepath.IsAbs(identity.Path) && filepath.Clean(identity.Path) == identity.Path && validDigest(identity.FileID)
}

func canonicalFuturePath(name string) (string, error) {
	absolute, err := filepath.Abs(name)
	if err != nil {
		return "", err
	}
	absolute = filepath.Clean(absolute)
	cursor := absolute
	var suffix []string
	for {
		_, statErr := os.Lstat(cursor)
		if statErr == nil {
			resolved, resolveErr := filepath.EvalSymlinks(cursor)
			if resolveErr != nil {
				return "", resolveErr
			}
			for index := len(suffix) - 1; index >= 0; index-- {
				resolved = filepath.Join(resolved, suffix[index])
			}
			return filepath.Clean(resolved), nil
		}
		if !errors.Is(statErr, os.ErrNotExist) {
			return "", statErr
		}
		parent := filepath.Dir(cursor)
		if parent == cursor {
			return "", statErr
		}
		suffix = append(suffix, filepath.Base(cursor))
		cursor = parent
	}
}

func ensureExternalConfig(identity StoreIdentity, configPath string) error {
	canonical, err := canonicalFuturePath(configPath)
	if err != nil {
		return err
	}
	if canonical != filepath.Clean(configPath) {
		return fmt.Errorf("%w: trust configuration path changed identity", ErrConcurrent)
	}
	relative, err := filepath.Rel(identity.Path, canonical)
	if err != nil {
		return err
	}
	if relative == "." || relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return ErrConfigInsideStore
	}
	return nil
}
