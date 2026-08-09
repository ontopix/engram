package lifecycle

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestBeginTransitionAndRemoveExactState(t *testing.T) {
	target := canonicalTarget(t, "store")
	handle, err := Begin(target, Initialization)
	if err != nil {
		t.Fatal(err)
	}
	state, raw, err := Read(target, Initialization)
	if err != nil {
		t.Fatal(err)
	}
	if state.Phase != Running || state.Operation != Initialization || state.Target != target || len(state.Owner.Token) != 64 || raw[len(raw)-1] != '\n' {
		t.Fatalf("state = %#v, raw = %q", state, raw)
	}
	stage, err := Stage(state)
	if err != nil || filepath.Dir(stage) != filepath.Dir(target) || bytes.Contains([]byte(filepath.Base(stage)), []byte("..")) {
		t.Fatalf("stage = %q, %v", stage, err)
	}
	if err := handle.RequireCleanup(); err != nil {
		t.Fatal(err)
	}
	state, _, err = Read(target, Initialization)
	if err != nil || state.Phase != CleanupRequired {
		t.Fatalf("cleanup state = %#v, %v", state, err)
	}
	if err := handle.RequireCleanup(); !errors.Is(err, ErrChanged) {
		t.Fatalf("second transition = %v", err)
	}
	if err := handle.Remove(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(Sidecar(target, Initialization)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("state remains: %v", err)
	}
}

func TestBeginNeverReusesExistingState(t *testing.T) {
	target := canonicalTarget(t, "store")
	first, err := Begin(target, Acquisition)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Remove()
	if _, err := Begin(target, Acquisition); !errors.Is(err, ErrExists) {
		t.Fatalf("second begin = %v", err)
	}
}

func TestReadRejectsUnknownAndNonCanonicalState(t *testing.T) {
	target := canonicalTarget(t, "store")
	handle, err := Begin(target, Initialization)
	if err != nil {
		t.Fatal(err)
	}
	name := Sidecar(target, Initialization)
	state := handle.State()
	encoded, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	if err := os.WriteFile(name, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Read(target, Initialization); !errors.Is(err, ErrMalformed) {
		t.Fatalf("non-canonical read = %v", err)
	}
	if err := handle.Remove(); !errors.Is(err, ErrChanged) {
		t.Fatalf("changed owner removal = %v", err)
	}
}

func canonicalTarget(t *testing.T, base string) string {
	t.Helper()
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(parent, base)
}
