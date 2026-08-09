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
)

type Phase string

const (
	PreJournal      Phase = "pre-journal"
	JournalRequired Phase = "journal-required"
)

var ErrBusy = errors.New("engram rendezvous is busy")
var ErrOwnership = errors.New("engram rendezvous ownership changed")

type Owner struct {
	Version   int    `json:"version"`
	Token     string `json:"token"`
	PID       int    `json:"pid"`
	Hostname  string `json:"hostname"`
	StartedAt string `json:"started_at"`
	Phase     Phase  `json:"phase"`
}

type Handle struct {
	owner Owner
	paths []string // Acquisition order; release is reverse.
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
			_ = handle.releaseCreated()
			return nil, err
		}
		handle.paths = append(handle.paths, name)
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
		if err != nil || owner.Token != h.owner.Token || owner.Phase != h.owner.Phase {
			return ErrOwnership
		}
	}
	updated := h.owner
	updated.Phase = phase
	for _, name := range h.paths {
		if err := replaceOwned(name, h.owner, updated); err != nil {
			return err
		}
	}
	h.owner = updated
	return nil
}

// Release removes owned locks in reverse order only after proving their exact
// token and phase. Recovery-required callers deliberately retain the handle.
func (h *Handle) Release() error {
	if h == nil {
		return nil
	}
	for index := len(h.paths) - 1; index >= 0; index-- {
		name := h.paths[index]
		owner, err := Read(name)
		if err != nil || owner.Token != h.owner.Token || owner.Phase != h.owner.Phase {
			return ErrOwnership
		}
		if err := os.Remove(name); err != nil {
			return err
		}
	}
	h.paths = nil
	return nil
}

func (h *Handle) Owner() Owner {
	if h == nil {
		return Owner{}
	}
	return h.owner
}

func Read(name string) (Owner, error) {
	data, err := os.ReadFile(name)
	if err != nil {
		return Owner{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var owner Owner
	if err := decoder.Decode(&owner); err != nil || decoder.Decode(&struct{}{}) != io.EOF || owner.Version != 1 || len(owner.Token) != 64 || owner.PID <= 0 || owner.Hostname == "" || owner.StartedAt == "" || owner.Phase != PreJournal && owner.Phase != JournalRequired {
		return Owner{}, fmt.Errorf("malformed rendezvous owner")
	}
	return owner, nil
}

func create(name string, owner Owner) error {
	if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(owner)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	file, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return ErrBusy
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
		_ = os.Remove(name)
		return err
	}
	return syncDirectory(filepath.Dir(name))
}

func replaceOwned(name string, before, after Owner) error {
	current, err := Read(name)
	if err != nil || current.Token != before.Token || current.Phase != before.Phase {
		return ErrOwnership
	}
	data, err := json.Marshal(after)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	temporary, err := os.CreateTemp(filepath.Dir(name), ".engram-lock-*")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
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
	current, err = Read(name)
	if err != nil || current.Token != before.Token || current.Phase != before.Phase {
		return ErrOwnership
	}
	if err := os.Rename(temporaryName, name); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(name))
}

func (h *Handle) releaseCreated() error {
	var result error
	for index := len(h.paths) - 1; index >= 0; index-- {
		owner, err := Read(h.paths[index])
		if err == nil && owner.Token == h.owner.Token {
			result = errors.Join(result, os.Remove(h.paths[index]))
		}
	}
	h.paths = nil
	return result
}

func syncDirectory(name string) error {
	if runtime.GOOS == "windows" {
		// Go does not expose a portable directory-flush primitive on Windows;
		// each owned file itself has already been synchronously flushed.
		return nil
	}
	directory, err := os.Open(name)
	if err != nil {
		return err
	}
	err = directory.Sync()
	return errors.Join(err, directory.Close())
}
