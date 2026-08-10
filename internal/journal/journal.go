// Package journal defines the controller-private durable recovery record for
// one managed transaction. The format is intentionally versioned and closed;
// foreign or malformed bytes are never guessed through.
package journal

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/ontopix/engram/internal/fileidentity"
	"github.com/ontopix/engram/internal/gitraw"
)

type State string

const (
	Pending   State = "pending"
	Cancelled State = "cancelled"
	Complete  State = "complete"
)

var ErrExists = errors.New("managed recovery journal already exists")
var ErrChanged = errors.New("managed recovery journal changed")

// Effect records exact publication evidence when a journal operation fails
// after its canonical name or one owned temporary may already have changed.
// Visible does not imply crash durability; Durable is true only after the
// containing directory has been flushed successfully.
type Effect struct {
	Visible bool
	Durable bool
}

type EffectError struct {
	Effect Effect
	Err    error
}

func (e *EffectError) Error() string {
	if e == nil || e.Err == nil {
		return "journal operation failed after publication"
	}
	return e.Err.Error()
}

func (e *EffectError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// EffectOf returns mutation evidence carried by a failed journal operation.
func EffectOf(err error) (Effect, bool) {
	var typed *EffectError
	if !errors.As(err, &typed) || typed == nil {
		return Effect{}, false
	}
	return typed.Effect, true
}

func effectError(err error, visible, durable bool) error {
	if err == nil {
		return nil
	}
	return &EffectError{Effect: Effect{Visible: visible, Durable: durable}, Err: err}
}

type RefUpdate struct {
	Ref    string  `json:"ref"`
	Before *string `json:"before"`
	After  string  `json:"after"`
}

type RawFileImage struct {
	Present bool   `json:"present"`
	Data    []byte `json:"data"`
}

type OwnerIdentity struct {
	PID       int    `json:"pid"`
	Hostname  string `json:"hostname"`
	StartedAt string `json:"started_at"`
}

type Image struct {
	Kind string `json:"kind"`
	Mode uint32 `json:"mode"`
	Data []byte `json:"data"`
}

type PathUpdate struct {
	Path   string `json:"path"`
	Before *Image `json:"before"`
	After  *Image `json:"after"`
}

type Fingerprint struct {
	Name    string `json:"name"`
	Present bool   `json:"present"`
	Kind    string `json:"kind"`
	Data    []byte `json:"data"`
}

type Record struct {
	Version      int                 `json:"version"`
	State        State               `json:"state"`
	OwnerToken   string              `json:"owner_token"`
	Owner        OwnerIdentity       `json:"owner"`
	ObjectFormat gitraw.ObjectFormat `json:"object_format"`
	Ref          RefUpdate           `json:"ref"`
	IndexBefore  RawFileImage        `json:"index_before"`
	IndexAfter   RawFileImage        `json:"index_after"`
	Paths        []PathUpdate        `json:"paths"`
	Fingerprints []Fingerprint       `json:"fingerprints"`
}

func Path(worktreeGitDir string) string {
	return filepath.Join(worktreeGitDir, "engram", "recovery", "transaction-v1.json")
}

// WritePending creates the only active journal after validating its closed,
// deterministic shape and durably flushing its bytes.
func WritePending(name string, record Record) error {
	record.Version = 1
	record.State = Pending
	if err := validate(record); err != nil {
		return err
	}
	data, err := encode(record)
	if err != nil {
		return err
	}
	directory, base, err := openJournalDirectory(name, true)
	if err != nil {
		return err
	}
	defer directory.Close()
	// The canonical name is never opened for writing. A crash can leave the
	// owner-token temporary link, but can never expose truncated canonical
	// journal bytes. Link provides the portable no-replace publication step.
	temporaryName := base + ".pending-" + record.OwnerToken
	file, err := directory.OpenFile(temporaryName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return ErrExists
	}
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		_ = directory.Remove(temporaryName)
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		_ = directory.Remove(temporaryName)
		return err
	}
	if err := file.Close(); err != nil {
		_ = directory.Remove(temporaryName)
		return err
	}
	if err := directory.Link(temporaryName, base); err != nil {
		_ = directory.Remove(temporaryName)
		if errors.Is(err, os.ErrExist) {
			return ErrExists
		}
		return err
	}
	durable, err := syncRootState(directory)
	if err != nil {
		// The canonical journal may already be durable; never remove it after
		// publication merely because the durability proof failed.
		return effectError(err, true, durable)
	}
	// The canonical name is already durable. Cleanup is retryable from the
	// owner token and must not turn a published pending journal into a
	// pre-journal-looking error at the caller.
	if err := directory.Remove(temporaryName); err == nil {
		_ = syncRoot(directory)
	}
	return nil
}

func Read(name string) (Record, []byte, error) {
	directory, base, err := openJournalDirectory(name, false)
	if err != nil {
		return Record{}, nil, err
	}
	defer directory.Close()
	data, err := stableRead(directory, base)
	if err != nil {
		return Record{}, nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var record Record
	if err := decoder.Decode(&record); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return Record{}, data, fmt.Errorf("malformed managed recovery journal")
	}
	if err := validate(record); err != nil {
		return Record{}, data, fmt.Errorf("malformed managed recovery journal: %w", err)
	}
	canonical, err := encode(record)
	if err != nil || !bytes.Equal(canonical, data) {
		return Record{}, data, fmt.Errorf("managed recovery journal is not canonically encoded")
	}
	return record, data, nil
}

// SetState atomically replaces exactly the observed canonical record.
func SetState(name string, expected []byte, state State) ([]byte, error) {
	if state != Cancelled && state != Complete {
		return nil, fmt.Errorf("invalid terminal journal state %q", state)
	}
	record, observed, err := Read(name)
	if err != nil {
		return nil, err
	}
	if record.State != Pending || !bytes.Equal(observed, expected) {
		return nil, ErrChanged
	}
	record.State = state
	updated, err := encode(record)
	if err != nil {
		return nil, err
	}
	if err := replace(name, observed, updated); err != nil {
		return nil, err
	}
	return updated, nil
}

// Remove deletes exactly the expected terminal journal.
func Remove(name string, expected []byte) error {
	record, observed, err := Read(name)
	if err != nil {
		return err
	}
	if record.State != Cancelled && record.State != Complete || !bytes.Equal(observed, expected) {
		return ErrChanged
	}
	if _, err := CleanupOwnedTemporaries(name, expected); err != nil {
		return err
	}
	directory, base, err := openJournalDirectory(name, false)
	if err != nil {
		return err
	}
	defer directory.Close()
	current, err := stableRead(directory, base)
	if err != nil || !bytes.Equal(current, expected) {
		return ErrChanged
	}
	if err := directory.Remove(base); err != nil {
		return err
	}
	durable, err := syncRootState(directory)
	if err != nil {
		return effectError(err, true, durable)
	}
	return nil
}

func validate(record Record) error {
	if record.Version != 1 || record.State != Pending && record.State != Cancelled && record.State != Complete {
		return fmt.Errorf("unsupported version or state")
	}
	if len(record.OwnerToken) != 64 || !lowerHex(record.OwnerToken) || !strings.HasPrefix(record.Ref.Ref, "refs/heads/") || len(record.Ref.Ref) == len("refs/heads/") || !utf8.ValidString(record.Ref.Ref) {
		return fmt.Errorf("invalid owner or ref update")
	}
	if record.Owner.PID <= 0 || record.Owner.Hostname == "" || record.Owner.StartedAt == "" || !utf8.ValidString(record.Owner.Hostname) || !utf8.ValidString(record.Owner.StartedAt) {
		return fmt.Errorf("invalid owner identity")
	}
	if record.ObjectFormat != gitraw.SHA1 && record.ObjectFormat != gitraw.SHA256 {
		return fmt.Errorf("invalid object format")
	}
	if _, err := gitraw.ParseOID(record.ObjectFormat, record.Ref.After); err != nil {
		return fmt.Errorf("invalid new ref object ID")
	}
	if record.Ref.Before != nil {
		if _, err := gitraw.ParseOID(record.ObjectFormat, *record.Ref.Before); err != nil {
			return fmt.Errorf("invalid old ref object ID")
		}
	}
	if !record.IndexBefore.Present && len(record.IndexBefore.Data) != 0 || !record.IndexAfter.Present || len(record.IndexAfter.Data) == 0 {
		return fmt.Errorf("invalid raw index images")
	}
	previous := ""
	for _, update := range record.Paths {
		if !validPath(update.Path) || previous != "" && previous >= update.Path || update.Before == nil && update.After == nil {
			return fmt.Errorf("invalid or unordered path update")
		}
		previous = update.Path
		if err := validateImage(update.Before); err != nil {
			return err
		}
		if err := validateImage(update.After); err != nil {
			return err
		}
	}
	previous = ""
	for _, fingerprint := range record.Fingerprints {
		if fingerprint.Name == "" || !utf8.ValidString(fingerprint.Name) || previous != "" && previous >= fingerprint.Name {
			return fmt.Errorf("invalid or unordered safety fingerprint")
		}
		previous = fingerprint.Name
		if !fingerprint.Present && (fingerprint.Kind != "" || len(fingerprint.Data) != 0) {
			return fmt.Errorf("absent fingerprint carries an image")
		}
		if fingerprint.Present && fingerprint.Kind == "" {
			return fmt.Errorf("present fingerprint has no kind")
		}
	}
	return nil
}

func validateImage(image *Image) error {
	if image == nil {
		return nil
	}
	switch image.Kind {
	case "regular", "directory", "symlink":
	default:
		return fmt.Errorf("invalid journal path image kind %q", image.Kind)
	}
	if image.Mode&^0o777 != 0 {
		return fmt.Errorf("journal path image has non-permission mode bits")
	}
	if image.Kind == "directory" && len(image.Data) != 0 {
		return fmt.Errorf("directory image carries bytes")
	}
	return nil
}

func lowerHex(value string) bool {
	for _, character := range []byte(value) {
		if character >= '0' && character <= '9' || character >= 'a' && character <= 'f' {
			continue
		}
		return false
	}
	return value != ""
}

func validPath(value string) bool {
	if value == "" || !utf8.ValidString(value) || strings.HasPrefix(value, "/") || strings.Contains(value, "\\") {
		return false
	}
	for _, component := range strings.Split(value, "/") {
		if component == "" || component == "." || component == ".." {
			return false
		}
	}
	return true
}

func encode(record Record) ([]byte, error) {
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(record); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func replace(name string, expected, updated []byte) error {
	directory, base, err := openJournalDirectory(name, false)
	if err != nil {
		return err
	}
	defer directory.Close()
	current, err := stableRead(directory, base)
	if err != nil || !bytes.Equal(current, expected) {
		return ErrChanged
	}
	temporaryName := filepath.Base(replacementTemporaryPath(name, expected))
	temporary, err := directory.OpenFile(temporaryName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return ErrChanged
		}
		return err
	}
	defer directory.Remove(temporaryName)
	if err := temporary.Chmod(0o600); err != nil {
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
	current, err = stableRead(directory, base)
	if err != nil || !bytes.Equal(current, expected) {
		return ErrChanged
	}
	if err := directory.Rename(temporaryName, base); err != nil {
		return err
	}
	durable, err := syncRootState(directory)
	if err != nil {
		return effectError(err, true, durable)
	}
	return nil
}

// CleanupOwnedTemporaries removes only deterministic journal temporaries that
// are cryptographically and structurally bound to the exact observed
// canonical journal. Unknown or changed bytes are left untouched.
func CleanupOwnedTemporaries(name string, expected []byte) (bool, error) {
	record, observed, err := Read(name)
	if err != nil || !bytes.Equal(observed, expected) {
		return false, ErrChanged
	}
	directory, base, err := openJournalDirectory(name, false)
	if err != nil {
		return false, err
	}
	defer directory.Close()
	names := []string{
		base + ".pending-" + record.OwnerToken,
		filepath.Base(replacementTemporaryPath(name, expected)),
	}
	removed := false
	for _, temporaryName := range names {
		data, err := stableRead(directory, temporaryName)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return false, err
		}
		owned := bytes.Equal(data, expected)
		if !owned {
			candidate, decodeErr := decodeCanonical(data)
			if decodeErr == nil && candidate.OwnerToken == record.OwnerToken {
				candidate.State = record.State
				canonical, encodeErr := encode(candidate)
				owned = encodeErr == nil && bytes.Equal(canonical, expected)
			}
		}
		if !owned {
			return false, fmt.Errorf("foreign journal temporary %s", temporaryName)
		}
		current, err := stableRead(directory, temporaryName)
		if err != nil || !bytes.Equal(current, data) {
			return false, ErrChanged
		}
		if err := directory.Remove(temporaryName); err != nil {
			return false, err
		}
		removed = true
	}
	if removed {
		durable, err := syncRootState(directory)
		if err != nil {
			return durable, effectError(err, true, durable)
		}
		return true, nil
	}
	return false, nil
}

func replacementTemporaryPath(name string, expected []byte) string {
	digest := sha256.Sum256(expected)
	return fmt.Sprintf("%s.replace-%x", name, digest[:])
}

func decodeCanonical(data []byte) (Record, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var record Record
	if err := decoder.Decode(&record); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return Record{}, fmt.Errorf("malformed managed recovery journal")
	}
	if err := validate(record); err != nil {
		return Record{}, err
	}
	canonical, err := encode(record)
	if err != nil || !bytes.Equal(canonical, data) {
		return Record{}, fmt.Errorf("managed recovery journal is not canonically encoded")
	}
	return record, nil
}

func openJournalDirectory(name string, create bool) (*os.Root, string, error) {
	name = filepath.Clean(name)
	parent := filepath.Dir(name)
	if gitDir, ok := journalGitDirectory(parent); ok {
		root, err := openStableDirectory(gitDir)
		if err != nil {
			return nil, "", err
		}
		for _, component := range []string{"engram", "recovery"} {
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

func journalGitDirectory(parent string) (string, bool) {
	if filepath.Base(parent) != "recovery" {
		return "", false
	}
	engram := filepath.Dir(parent)
	if filepath.Base(engram) != "engram" {
		return "", false
	}
	return filepath.Dir(engram), true
}

func openStableDirectory(name string) (*os.Root, error) {
	info, err := pinnedFileInfo(os.Lstat(name))
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, fmt.Errorf("unsafe journal administration directory")
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
		return nil, fmt.Errorf("journal administration directory changed while opening")
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
		return nil, fmt.Errorf("unsafe journal administration path %q", name)
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
		return nil, fmt.Errorf("journal administration path %q changed while opening", name)
	}
	return root, nil
}

func stableRead(root *os.Root, name string) ([]byte, error) {
	info, err := pinnedFileInfo(root.Lstat(name))
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("unsafe journal file")
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
		return nil, fmt.Errorf("journal file changed while opening")
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
		return nil, fmt.Errorf("journal file changed while reading")
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

func syncRoot(root *os.Root) error {
	_, err := syncRootState(root)
	return err
}

func syncRootState(root *os.Root) (bool, error) {
	if runtime.GOOS == "windows" {
		return true, nil
	}
	directory, err := root.Open(".")
	if err != nil {
		return false, err
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	if syncErr != nil {
		return false, errors.Join(syncErr, closeErr)
	}
	return true, closeErr
}

// Sort normalizes caller-owned slices before WritePending. It does not hide
// duplicates, which validation still rejects.
func Sort(record *Record) {
	if record == nil {
		return
	}
	sort.Slice(record.Paths, func(i, j int) bool { return record.Paths[i].Path < record.Paths[j].Path })
	sort.Slice(record.Fingerprints, func(i, j int) bool { return record.Fingerprints[i].Name < record.Fingerprints[j].Name })
}
