// Package lifecycle owns the exact pre-publication state used by init and
// acquisition. The small public sidecar is intentionally compatible with
// doctor's closed state reader; workflow-specific recovery data stays in the
// token-derived private staging directory.
package lifecycle

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/ontopix/engram/internal/rendezvous"
)

const maxStateBytes = 8 << 20

type Operation string

const (
	Initialization Operation = "initialization"
	Acquisition    Operation = "acquisition"
)

type Phase string

const (
	Running         Phase = "running"
	CleanupRequired Phase = "cleanup-required"
)

var (
	ErrExists    = errors.New("lifecycle state already exists")
	ErrChanged   = errors.New("lifecycle state changed")
	ErrMalformed = errors.New("malformed lifecycle state")
	ErrOwnerLive = errors.New("lifecycle owner may still be live")
)

// State is the exact closed shape recognized by doctor.
type State struct {
	Version   int              `json:"version"`
	Operation Operation        `json:"operation"`
	Target    string           `json:"target"`
	Owner     rendezvous.Owner `json:"owner"`
	Phase     Phase            `json:"phase"`
}

type Handle struct {
	path  string
	state State
	raw   []byte
}

// Sidecar returns the exact target-derived controller-state name.
func Sidecar(target string, operation Operation) string {
	return target + ".engram-" + string(operation) + "-v1.json"
}

// Stage returns the private staging directory derived from the immutable
// owner token. Recovery never scans or guesses by prefix.
func Stage(state State) (string, error) {
	if err := validateState(state); err != nil {
		return "", err
	}
	return state.Target + ".engram-" + string(state.Operation) + "-v1-" + state.Owner.Token + ".stage", nil
}

// Begin durably publishes one running state without creating the target.
func Begin(target string, operation Operation) (*Handle, error) {
	if !filepath.IsAbs(target) || filepath.Clean(target) != target || operation != Initialization && operation != Acquisition {
		return nil, fmt.Errorf("%w: invalid target or operation", ErrMalformed)
	}
	parent := filepath.Dir(target)
	info, err := os.Lstat(parent)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, fmt.Errorf("%w: lifecycle parent is not a real directory", ErrMalformed)
	}
	canonical, err := filepath.EvalSymlinks(parent)
	if err != nil || filepath.Clean(canonical) != parent {
		return nil, fmt.Errorf("%w: lifecycle parent is not canonical", ErrMalformed)
	}
	token := make([]byte, 32)
	if _, err := rand.Read(token); err != nil {
		return nil, err
	}
	hostname, err := os.Hostname()
	if err != nil {
		return nil, err
	}
	state := State{
		Version: 1, Operation: operation, Target: target, Phase: Running,
		Owner: rendezvous.Owner{
			Version: 1, Token: hex.EncodeToString(token), PID: os.Getpid(), Hostname: hostname,
			StartedAt: time.Now().UTC().Format(time.RFC3339Nano), Phase: rendezvous.PreJournal,
		},
	}
	raw, err := encode(state)
	if err != nil {
		return nil, err
	}
	name := Sidecar(target, operation)
	file, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return nil, ErrExists
	}
	if err != nil {
		return nil, err
	}
	if _, err := file.Write(raw); err != nil {
		_ = file.Close()
		_ = os.Remove(name)
		return nil, err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		_ = os.Remove(name)
		return nil, err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(name)
		return nil, err
	}
	if err := syncDirectory(parent); err != nil {
		return nil, err
	}
	return &Handle{path: name, state: state, raw: raw}, nil
}

// Read performs a stable, closed, canonical state read.
func Read(target string, operation Operation) (State, []byte, error) {
	name := Sidecar(target, operation)
	before, err := os.Lstat(name)
	if err != nil {
		return State{}, nil, err
	}
	if before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() {
		return State{}, nil, ErrMalformed
	}
	file, err := os.Open(name)
	if err != nil {
		return State{}, nil, err
	}
	raw, readErr := io.ReadAll(io.LimitReader(file, maxStateBytes+1))
	opened, statErr := file.Stat()
	closeErr := file.Close()
	if readErr != nil || statErr != nil || closeErr != nil {
		return State{}, nil, errors.Join(readErr, statErr, closeErr)
	}
	if len(raw) > maxStateBytes {
		return State{}, nil, ErrMalformed
	}
	after, err := os.Lstat(name)
	if err != nil || after.Mode()&os.ModeSymlink != 0 || !after.Mode().IsRegular() || !os.SameFile(before, opened) || !os.SameFile(opened, after) {
		return State{}, nil, ErrChanged
	}
	var state State
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil || decoder.Decode(&struct{}{}) != io.EOF || validateState(state) != nil || state.Target != target || state.Operation != operation {
		return State{}, nil, ErrMalformed
	}
	canonical, err := encode(state)
	if err != nil || !bytes.Equal(raw, canonical) {
		return State{}, nil, ErrMalformed
	}
	return state, append([]byte(nil), raw...), nil
}

func (h *Handle) State() State {
	if h == nil {
		return State{}
	}
	return h.state
}

// RequireCleanup durably advances the state immediately before any target
// publication. It never moves backward.
func (h *Handle) RequireCleanup() error {
	if h == nil || h.state.Phase != Running {
		return ErrChanged
	}
	current, raw, err := Read(h.state.Target, h.state.Operation)
	if err != nil || current.Owner.Token != h.state.Owner.Token || !bytes.Equal(raw, h.raw) {
		return errors.Join(ErrChanged, err)
	}
	next := h.state
	next.Phase = CleanupRequired
	nextRaw, err := encode(next)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(h.path), ".engram-lifecycle-")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(nextRaw); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	current, raw, err = Read(h.state.Target, h.state.Operation)
	if err != nil || current.Owner.Token != h.state.Owner.Token || !bytes.Equal(raw, h.raw) {
		return errors.Join(ErrChanged, err)
	}
	if err := os.Rename(temporaryName, h.path); err != nil {
		return err
	}
	if err := syncDirectory(filepath.Dir(h.path)); err != nil {
		return err
	}
	h.state = next
	h.raw = nextRaw
	return nil
}

// Remove deletes only the exact state still owned by the handle.
func (h *Handle) Remove() error {
	if h == nil {
		return nil
	}
	current, raw, err := Read(h.state.Target, h.state.Operation)
	if err != nil || current.Owner.Token != h.state.Owner.Token || !bytes.Equal(raw, h.raw) {
		return errors.Join(ErrChanged, err)
	}
	if err := os.Remove(h.path); err != nil {
		return err
	}
	if err := syncDirectory(filepath.Dir(h.path)); err != nil {
		return err
	}
	h.path = ""
	return nil
}

// Adopt reconstructs a handle only after the caller has independently proved
// owner death. It rechecks that proof at adoption time.
func Adopt(target string, operation Operation) (*Handle, error) {
	state, raw, err := Read(target, operation)
	if err != nil {
		return nil, err
	}
	dead, err := ownerDead(state.Owner)
	if err != nil || !dead {
		return nil, errors.Join(ErrOwnerLive, err)
	}
	return &Handle{path: Sidecar(target, operation), state: state, raw: raw}, nil
}

func validateState(state State) error {
	if state.Version != 1 || state.Operation != Initialization && state.Operation != Acquisition ||
		!filepath.IsAbs(state.Target) || filepath.Clean(state.Target) != state.Target ||
		state.Phase != Running && state.Phase != CleanupRequired ||
		state.Owner.Version != 1 || len(state.Owner.Token) != 64 || state.Owner.PID <= 0 || state.Owner.Hostname == "" || state.Owner.StartedAt == "" || state.Owner.Phase != rendezvous.PreJournal {
		return ErrMalformed
	}
	for _, character := range state.Owner.Token {
		if character < '0' || character > '9' && character < 'a' || character > 'f' {
			return ErrMalformed
		}
	}
	return nil
}

func encode(state State) ([]byte, error) {
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(state); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func syncDirectory(name string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	directory, err := os.Open(name)
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}
