package pullflow

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

	"github.com/ontopix/engram/internal/checker"
	"github.com/ontopix/engram/internal/gitraw"
	"github.com/ontopix/engram/internal/managedread"
)

const maxControllerStateBytes = 32 << 20

var errReplayControllerBusy = errors.New("pull replay controller is busy")

type replayState struct {
	Version   int                  `json:"version"`
	Original  managedread.GitState `json:"original"`
	Private   managedread.GitState `json:"private"`
	Base      managedread.GitState `json:"base"`
	Reason    string               `json:"reason"`
	Conflicts []string             `json:"conflicts"`
}

type sourceCommit struct {
	ID      string `json:"id"`
	Base    string `json:"base"`
	Message string `json:"message"`
}

type replayPlan struct {
	Version    int                        `json:"version"`
	Remote     string                     `json:"remote"`
	RemoteRef  string                     `json:"remote_ref"`
	Original   managedread.GitState       `json:"original"`
	PrivateRef string                     `json:"private_ref"`
	RemoteTip  string                     `json:"remote_tip"`
	Sources    []sourceCommit             `json:"sources"`
	Next       int                        `json:"next"`
	DraftReady bool                       `json:"draft_ready"`
	Fetched    int                        `json:"fetched"`
	Replayed   int                        `json:"replayed"`
	Validation checker.Result             `json:"validation"`
	Audits     []managedread.HistoryAudit `json:"audits"`
}

func replayDirectory(repository *gitraw.Repository) string {
	return filepath.Join(repository.GitDir, "engram", "replay")
}

func replayStatePath(repository *gitraw.Repository) string {
	return filepath.Join(replayDirectory(repository), "state-v1.json")
}

func replayPlanPath(repository *gitraw.Repository) string {
	return filepath.Join(replayDirectory(repository), "plan-v1.json")
}

// Active returns the protocol-facing active replay state. It treats an
// orphaned state or plan as recognized recovery-required state, never as an
// absent replay.
func Active(repository *gitraw.Repository) (*managedread.ReplayState, error) {
	if _, _, present, err := readReplayTerminal(repository); err != nil || present {
		if err == nil {
			err = ErrRecovery
		}
		return nil, errors.Join(ErrRecovery, err)
	}
	state, plan, present, err := readReplay(repository)
	if err != nil || !present {
		return nil, err
	}
	if err := validateReplayPair(repository, state, plan); err != nil {
		return nil, err
	}
	conflicts := make([]string, len(state.Conflicts))
	copy(conflicts, state.Conflicts)
	return &managedread.ReplayState{
		Original: cloneGitState(state.Original), Private: cloneGitState(state.Private),
		Base: cloneGitState(state.Base), Reason: state.Reason,
		Conflicts: conflicts,
	}, nil
}

func readReplay(repository *gitraw.Repository) (replayState, replayPlan, bool, error) {
	if _, _, present, err := readReplayPairJournal(repository); err != nil || present {
		if err == nil {
			err = ErrRecovery
		}
		return replayState{}, replayPlan{}, true, errors.Join(ErrRecovery, err)
	}
	return readReplayFiles(repository)
}

func readReplayFiles(repository *gitraw.Repository) (replayState, replayPlan, bool, error) {
	if repository == nil {
		return replayState{}, replayPlan{}, false, errors.New("nil repository")
	}
	stateBytes, statePresent, err := readControllerFile(replayStatePath(repository))
	if err != nil {
		return replayState{}, replayPlan{}, true, err
	}
	planBytes, planPresent, err := readControllerFile(replayPlanPath(repository))
	if err != nil {
		return replayState{}, replayPlan{}, true, err
	}
	if statePresent != planPresent {
		return replayState{}, replayPlan{}, true, fmt.Errorf("incomplete pull replay controller state")
	}
	if !statePresent {
		return replayState{}, replayPlan{}, false, nil
	}
	var state replayState
	if err := decodeCanonical(stateBytes, &state); err != nil {
		return replayState{}, replayPlan{}, true, fmt.Errorf("replay state: %w", err)
	}
	var plan replayPlan
	if err := decodeCanonical(planBytes, &plan); err != nil {
		return replayState{}, replayPlan{}, true, fmt.Errorf("replay plan: %w", err)
	}
	return state, plan, true, nil
}

func validateReplayPair(repository *gitraw.Repository, state replayState, plan replayPlan) error {
	if state.Version != 1 || plan.Version != 1 || state.Reason != "conflict" && state.Reason != "rejected" || state.Conflicts == nil {
		return errors.New("unsupported pull replay state")
	}
	if state.Reason == "conflict" && len(state.Conflicts) == 0 || state.Reason == "rejected" && len(state.Conflicts) != 0 {
		return errors.New("pull replay reason and conflicts disagree")
	}
	if plan.Remote == "" || plan.RemoteRef == "" || !strings.HasPrefix(plan.RemoteRef, "refs/heads/") || plan.PrivateRef == "" || !strings.HasPrefix(plan.PrivateRef, "refs/heads/") || len(plan.Sources) == 0 || plan.Audits == nil || plan.Next < 0 || plan.Next > len(plan.Sources) || plan.Replayed < 0 || plan.Replayed != plan.Next || plan.Fetched < 0 {
		return errors.New("invalid pull replay plan")
	}
	if plan.Next == len(plan.Sources) && plan.DraftReady || plan.Next < len(plan.Sources) && state.Reason == "conflict" && !plan.DraftReady {
		return errors.New("pull replay draft state does not match its progress")
	}
	expectedBase := plan.Sources[len(plan.Sources)-1].Base
	if plan.Next < len(plan.Sources) {
		expectedBase = plan.Sources[plan.Next].Base
	}
	if !sameGitState(state.Original, plan.Original) || state.Private.Ref == nil || *state.Private.Ref != plan.PrivateRef || state.Private.Commit == nil || state.Base.Ref != nil || state.Base.Commit == nil || *state.Base.Commit != expectedBase {
		return errors.New("pull replay state does not match its plan")
	}
	if repository != nil {
		if repository.Head == nil || repository.HeadRef != plan.PrivateRef || repository.Head.String() != *state.Private.Commit {
			return errors.New("repository HEAD does not match active private replay state")
		}
		for _, oid := range []string{*state.Private.Commit, *state.Base.Commit, plan.RemoteTip} {
			if _, err := gitraw.ParseOID(repository.Format, oid); err != nil {
				return errors.New("pull replay contains an object ID at the wrong width")
			}
		}
	}
	previous := ""
	for _, name := range state.Conflicts {
		if !validLogicalPath(name) || previous != "" && previous >= name {
			return errors.New("pull replay conflicts are invalid or unordered")
		}
		previous = name
	}
	for index, source := range plan.Sources {
		if source.ID == "" || source.Base == "" || source.Message == "" {
			return fmt.Errorf("invalid source commit at index %d", index)
		}
		if repository != nil {
			if _, err := gitraw.ParseOID(repository.Format, source.ID); err != nil {
				return fmt.Errorf("invalid source commit at index %d", index)
			}
			if _, err := gitraw.ParseOID(repository.Format, source.Base); err != nil {
				return fmt.Errorf("invalid source base at index %d", index)
			}
		}
	}
	return nil
}

func withReplayLock(repository *gitraw.Repository, action func() error) (result error) {
	if repository == nil {
		return errors.New("nil replay repository")
	}
	directory := replayDirectory(repository)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	name := filepath.Join(directory, "controller.lock")
	before, beforeErr := os.Lstat(name)
	if beforeErr == nil && (before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() || before.Mode().Perm()&0o077 != 0) {
		return errors.New("unsafe replay controller lock")
	}
	if beforeErr != nil && !errors.Is(beforeErr, os.ErrNotExist) {
		return beforeErr
	}
	lock, err := os.OpenFile(name, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	locked := false
	defer func() {
		var unlockErr error
		if locked {
			unlockErr = unlockReplayControllerFile(lock)
		}
		result = errors.Join(result, unlockErr, lock.Close())
	}()
	opened, err := lock.Stat()
	if err != nil || !opened.Mode().IsRegular() || opened.Mode().Perm()&0o077 != 0 || beforeErr == nil && !os.SameFile(before, opened) {
		if err != nil {
			return err
		}
		return errors.New("replay controller lock changed while opening")
	}
	named, err := os.Lstat(name)
	if err != nil || named.Mode()&os.ModeSymlink != 0 || !os.SameFile(opened, named) {
		if err != nil {
			return err
		}
		return errors.New("replay controller lock changed while opening")
	}
	if err := lockReplayControllerFile(lock); err != nil {
		return err
	}
	locked = true
	after, err := os.Lstat(name)
	if err != nil || after.Mode()&os.ModeSymlink != 0 || !os.SameFile(opened, after) {
		if err != nil {
			return err
		}
		return errors.New("replay controller lock changed while held")
	}
	result = action()
	return result
}

func encodeCanonical(value any) ([]byte, error) {
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func decodeCanonical(data []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return errors.New("controller state has trailing JSON")
	}
	canonical, err := encodeCanonical(value)
	if err != nil || !bytes.Equal(data, canonical) {
		return errors.New("controller state is not canonically encoded")
	}
	return nil
}

func readControllerFile(name string) ([]byte, bool, error) {
	before, err := os.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, true, err
	}
	if before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() || before.Mode().Perm()&0o077 != 0 {
		return nil, true, errors.New("controller state is not a private real regular file")
	}
	file, err := os.Open(name)
	if err != nil {
		return nil, true, err
	}
	data, readErr := io.ReadAll(io.LimitReader(file, maxControllerStateBytes+1))
	opened, statErr := file.Stat()
	closeErr := file.Close()
	if readErr != nil {
		return nil, true, readErr
	}
	if statErr != nil {
		return nil, true, statErr
	}
	if closeErr != nil {
		return nil, true, closeErr
	}
	if len(data) > maxControllerStateBytes {
		return nil, true, errors.New("controller state exceeds size limit")
	}
	after, err := os.Lstat(name)
	if err != nil || after.Mode()&os.ModeSymlink != 0 || !after.Mode().IsRegular() || !os.SameFile(before, opened) || !os.SameFile(opened, after) {
		return nil, true, errors.New("controller state changed while being read")
	}
	return data, true, nil
}

func replaceControllerFile(name string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(name), ".engram-replay-*")
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
	return errors.Join(directory.Sync(), directory.Close())
}

func cloneGitState(value managedread.GitState) managedread.GitState {
	return managedread.GitState{Ref: cloneString(value.Ref), Commit: cloneString(value.Commit)}
}

func sameGitState(left, right managedread.GitState) bool {
	return equalString(left.Ref, right.Ref) && equalString(left.Commit, right.Commit)
}

func equalString(left, right *string) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func validLogicalPath(value string) bool {
	if value == "" || value == "." || !utf8.ValidString(value) || strings.HasPrefix(value, "/") || strings.Contains(value, "\\") {
		return false
	}
	for _, component := range strings.Split(value, "/") {
		if component == "" || component == "." || component == ".." {
			return false
		}
	}
	return true
}

func sortStrings(values []string) []string {
	result := append([]string(nil), values...)
	sort.Slice(result, func(i, j int) bool { return bytes.Compare([]byte(result[i]), []byte(result[j])) < 0 })
	return result
}
