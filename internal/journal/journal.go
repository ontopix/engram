// Package journal defines the controller-private durable recovery record for
// one managed transaction. The format is intentionally versioned and closed;
// foreign or malformed bytes are never guessed through.
package journal

import (
	"bytes"
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
)

type State string

const (
	Pending   State = "pending"
	Cancelled State = "cancelled"
	Complete  State = "complete"
)

var ErrExists = errors.New("managed recovery journal already exists")
var ErrChanged = errors.New("managed recovery journal changed")

type RefUpdate struct {
	Ref    string  `json:"ref"`
	Before *string `json:"before"`
	After  string  `json:"after"`
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
	Version      int           `json:"version"`
	State        State         `json:"state"`
	OwnerToken   string        `json:"owner_token"`
	Ref          RefUpdate     `json:"ref"`
	IndexBefore  []byte        `json:"index_before"`
	IndexAfter   []byte        `json:"index_after"`
	Paths        []PathUpdate  `json:"paths"`
	Fingerprints []Fingerprint `json:"fingerprints"`
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
	if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return ErrExists
	}
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		_ = os.Remove(name)
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		_ = os.Remove(name)
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(name))
}

func Read(name string) (Record, []byte, error) {
	data, err := os.ReadFile(name)
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
	if err := os.Remove(name); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(name))
}

func validate(record Record) error {
	if record.Version != 1 || record.State != Pending && record.State != Cancelled && record.State != Complete {
		return fmt.Errorf("unsupported version or state")
	}
	if len(record.OwnerToken) != 64 || record.Ref.Ref == "" || record.Ref.After == "" || !utf8.ValidString(record.Ref.Ref) {
		return fmt.Errorf("invalid owner or ref update")
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
	if image.Kind == "directory" && len(image.Data) != 0 {
		return fmt.Errorf("directory image carries bytes")
	}
	return nil
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
	current, err := os.ReadFile(name)
	if err != nil || !bytes.Equal(current, expected) {
		return ErrChanged
	}
	temporary, err := os.CreateTemp(filepath.Dir(name), ".engram-journal-*")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
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
	current, err = os.ReadFile(name)
	if err != nil || !bytes.Equal(current, expected) {
		return ErrChanged
	}
	if err := os.Rename(temporaryName, name); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(name))
}

func syncDirectory(name string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	directory, err := os.Open(name)
	if err != nil {
		return err
	}
	err = directory.Sync()
	return errors.Join(err, directory.Close())
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
