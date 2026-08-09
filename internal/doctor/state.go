package doctor

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ontopix/engram/internal/gitraw"
	"github.com/ontopix/engram/internal/journal"
	"github.com/ontopix/engram/internal/managedread"
	"github.com/ontopix/engram/internal/rendezvous"
)

const maxStateBytes = 8 << 20

type lifecyclePhase string

const (
	lifecycleRunning         lifecyclePhase = "running"
	lifecycleCleanupRequired lifecyclePhase = "cleanup-required"
)

type lifecycleState struct {
	Version   int              `json:"version"`
	Operation string           `json:"operation"`
	Target    string           `json:"target"`
	Owner     rendezvous.Owner `json:"owner"`
	Phase     lifecyclePhase   `json:"phase"`
}

type lifecycleObservation struct {
	path  string
	state lifecycleState
	raw   []byte
	alive ownerCondition
}

type ownerCondition uint8

const (
	ownerUnknown ownerCondition = iota
	ownerAlive
	ownerDead
)

func lifecycleSidecar(target, operation string) string {
	return target + ".engram-" + operation + "-v1.json"
}

func lifecycleInternal(repository *gitraw.Repository, operation string) string {
	if repository == nil {
		return ""
	}
	return filepath.Join(repository.GitDir, "engram", "state", operation+"-v1.json")
}

func targetStateEvidence(target string) (bool, error) {
	for _, operation := range []string{"initialization", "acquisition"} {
		name := lifecycleSidecar(target, operation)
		_, err := os.Lstat(name)
		if err == nil {
			return true, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return false, err
		}
	}
	return false, nil
}

func inspectLifecycleStates(current *inspection) recoveryPlan {
	combined := recoveryPlan{safe: true}
	for _, operation := range []string{"initialization", "acquisition"} {
		name := operation + ".state"
		sidecar := lifecycleSidecar(current.target, operation)
		paths := []string{sidecar}
		if internal := lifecycleInternal(current.repository, operation); internal != "" {
			paths = append(paths, internal)
		}
		paths = sortedUnique(paths)
		observations := make([]lifecycleObservation, 0, 1)
		var problem string
		for _, statePath := range paths {
			base := filepath.Dir(sidecar)
			if current.repository != nil && statePath != sidecar {
				base = current.repository.GitDir
			}
			observation, present, err := readLifecycleState(base, statePath, current.target, operation)
			if err != nil {
				problem = err.Error()
				break
			}
			if present {
				observations = append(observations, observation)
			}
		}
		switch {
		case problem != "":
			setRequired(&current.result, name, Error, nil, "controller state is inconsistent: "+problem)
			combined.needed = true
			combined.safe = false
		case len(observations) > 1:
			setRequired(&current.result, name, Error, nil, "more than one controller state record names the exact target")
			combined.needed = true
			combined.safe = false
		case len(observations) == 1:
			observation := observations[0]
			switch {
			case observation.alive != ownerDead && observation.state.Phase == lifecycleRunning:
				message := "coherent live " + operation + " operation"
				if observation.alive == ownerUnknown {
					message = operation + " owner liveness cannot be disproved"
				}
				setRequired(&current.result, name, OK, pathPointer(observation.path), message)
				combined.blocked = true
			case observation.alive != ownerDead:
				setRequired(&current.result, name, Error, pathPointer(observation.path), "controller state requires recovery but owner death is not proven")
				combined.needed = true
				combined.safe = false
			default:
				setRequired(&current.result, name, Error, pathPointer(observation.path), "recognized stale "+operation+" state requires bounded recovery")
				combined.needed = true
				combined.lifecycle = append(combined.lifecycle, observation)
			}
		case operation == "initialization":
			status, statePath, message, present := inspectInitializationJournal(current.repository)
			if present {
				setRequired(&current.result, name, status, pathPointer(statePath), message)
			}
		}
	}
	if !combined.needed {
		combined.safe = false
	}
	return combined
}

func inspectInitializationJournal(repository *gitraw.Repository) (Status, string, string, bool) {
	if repository == nil {
		return OK, "", "", false
	}
	name := journal.Path(repository.GitDir)
	_, present, err := readStableControllerFile(repository.GitDir, name)
	if err != nil {
		return Error, name, "initialization controller state is inconsistent: " + err.Error(), true
	}
	if !present {
		return OK, "", "", false
	}
	record, _, err := journal.Read(name)
	if err != nil {
		return Error, name, "initialization controller state is inconsistent: " + err.Error(), true
	}
	if record.Ref.Before != nil {
		return OK, "", "", false
	}
	lockPath := rendezvous.WorktreePath(repository.GitDir)
	owner, err := rendezvous.Read(lockPath)
	if err != nil || !validOwner(owner) || owner.Token != record.OwnerToken {
		return Error, name, "initialization journal lacks matching owner state", true
	}
	if ownerLiveness(owner) == ownerDead {
		return Error, name, "recognized stale initialization transaction requires bounded recovery", true
	}
	return OK, name, "coherent live initialization transaction", true
}

func readLifecycleState(base, name, target, operation string) (lifecycleObservation, bool, error) {
	raw, present, err := readStableControllerFile(base, name)
	if err != nil || !present {
		return lifecycleObservation{}, present, err
	}
	var state lifecycleState
	if err := decodeClosedCanonical(raw, &state); err != nil {
		return lifecycleObservation{}, true, fmt.Errorf("%s: %w", name, err)
	}
	if state.Version != 1 || state.Operation != operation || state.Target != target ||
		state.Phase != lifecycleRunning && state.Phase != lifecycleCleanupRequired || !validOwner(state.Owner) {
		return lifecycleObservation{}, true, fmt.Errorf("%s: unsupported or inconsistent lifecycle record", name)
	}
	alive := ownerLiveness(state.Owner)
	return lifecycleObservation{path: name, state: state, raw: raw, alive: alive}, true, nil
}

type replayRecord struct {
	Version   int                  `json:"version"`
	Original  managedread.GitState `json:"original"`
	Private   managedread.GitState `json:"private"`
	Base      managedread.GitState `json:"base"`
	Reason    string               `json:"reason"`
	Conflicts []string             `json:"conflicts"`
}

func replayStatePath(repository *gitraw.Repository) string {
	if repository == nil {
		return ""
	}
	return filepath.Join(repository.GitDir, "engram", "replay", "state-v1.json")
}

func inspectReplayState(current *inspection) {
	if current.repository == nil {
		setRequired(&current.result, "replay.state", Error, nil, "managed repository is unavailable")
		return
	}
	name := replayStatePath(current.repository)
	raw, present, err := readStableControllerFile(current.repository.GitDir, name)
	if err != nil {
		setRequired(&current.result, "replay.state", Error, pathPointer(name), "replay state is inconsistent: "+err.Error())
		return
	}
	if !present {
		return
	}
	var record replayRecord
	if err := decodeClosedCanonical(raw, &record); err != nil || !validReplayRecord(record, current.repository) {
		message := "unsupported or inconsistent replay state"
		if err != nil {
			message += ": " + err.Error()
		}
		setRequired(&current.result, "replay.state", Error, pathPointer(name), message)
		return
	}
	setRequired(&current.result, "replay.state", OK, pathPointer(name), "active pull replay ("+record.Reason+")")
}

func validReplayRecord(record replayRecord, repository *gitraw.Repository) bool {
	if record.Version != 1 || record.Conflicts == nil || record.Reason != "conflict" && record.Reason != "rejected" {
		return false
	}
	if !validState(record.Original, repository.Format, true) || !validState(record.Private, repository.Format, true) ||
		!validState(record.Base, repository.Format, false) || record.Base.Ref != nil {
		return false
	}
	if repository.Head == nil || record.Private.Ref == nil || record.Private.Commit == nil ||
		repository.HeadRef != *record.Private.Ref || repository.Head.String() != *record.Private.Commit {
		return false
	}
	previous := ""
	for _, conflict := range record.Conflicts {
		if !validLogicalPath(conflict) || previous != "" && previous >= conflict {
			return false
		}
		previous = conflict
	}
	return record.Reason != "conflict" || len(record.Conflicts) != 0
}

func validState(state managedread.GitState, format gitraw.ObjectFormat, refRequired bool) bool {
	if state.Commit == nil {
		return false
	}
	if _, err := gitraw.ParseOID(format, *state.Commit); err != nil {
		return false
	}
	if refRequired {
		return state.Ref != nil && validFullBranchRef(*state.Ref)
	}
	return state.Ref == nil
}

func validFullBranchRef(value string) bool {
	const prefix = "refs/heads/"
	if !utf8.ValidString(value) || !strings.HasPrefix(value, prefix) || len(value) == len(prefix) ||
		strings.Contains(value, "..") || strings.Contains(value, "@{") || strings.Contains(value, "//") ||
		strings.HasSuffix(value, ".") || strings.HasSuffix(value, ".lock") || strings.HasSuffix(value, "/") {
		return false
	}
	for _, component := range strings.Split(strings.TrimPrefix(value, prefix), "/") {
		if component == "" || strings.HasPrefix(component, ".") || strings.HasSuffix(component, ".lock") {
			return false
		}
	}
	for _, character := range value {
		if character <= ' ' || character == 0x7f || strings.ContainsRune("~^:?*[\\", character) {
			return false
		}
	}
	return value != "@"
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
	for _, character := range value {
		if character <= 0x1f || character == 0x7f {
			return false
		}
	}
	return true
}

func readStableRealFile(name string) ([]byte, bool, error) {
	before, err := os.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() {
		return nil, true, errors.New("controller state is not a real regular file")
	}
	file, err := os.Open(name)
	if err != nil {
		return nil, true, err
	}
	content, readErr := io.ReadAll(io.LimitReader(file, maxStateBytes+1))
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
	if len(content) > maxStateBytes {
		return nil, true, errors.New("controller state exceeds size limit")
	}
	after, err := os.Lstat(name)
	if err != nil || after.Mode()&os.ModeSymlink != 0 || !after.Mode().IsRegular() || !os.SameFile(before, opened) || !os.SameFile(opened, after) {
		return nil, true, errors.New("controller state changed while being read")
	}
	return content, true, nil
}

func readStableControllerFile(base, name string) ([]byte, bool, error) {
	if err := realDirectoryChain(base, filepath.Dir(name)); err != nil {
		return nil, true, err
	}
	return readStableRealFile(name)
}

func realDirectoryChain(base, directory string) error {
	base = filepath.Clean(base)
	directory = filepath.Clean(directory)
	relative, err := filepath.Rel(base, directory)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errors.New("controller state path escapes its administration directory")
	}
	cursor := base
	components := []string{}
	if relative != "." {
		components = strings.Split(relative, string(filepath.Separator))
	}
	for index := -1; index < len(components); index++ {
		if index >= 0 {
			cursor = filepath.Join(cursor, components[index])
		}
		info, err := os.Lstat(cursor)
		if errors.Is(err, os.ErrNotExist) {
			// An absent remaining directory proves the target file absent; no
			// symlink can redirect through an absent component.
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return errors.New("controller state has a non-directory or symbolic ancestor")
		}
	}
	return nil
}

func decodeClosedCanonical(data []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return errors.New("controller state has trailing JSON")
	}
	var canonical bytes.Buffer
	encoder := json.NewEncoder(&canonical)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(destination); err != nil {
		return err
	}
	if !bytes.Equal(data, canonical.Bytes()) {
		return errors.New("controller state is not canonically encoded")
	}
	return nil
}

func validOwner(owner rendezvous.Owner) bool {
	if owner.Version != 1 || len(owner.Token) != 64 || owner.PID <= 0 || owner.PID > 1<<31-1 || owner.Hostname == "" || owner.StartedAt == "" ||
		owner.Phase != rendezvous.PreJournal && owner.Phase != rendezvous.JournalRequired {
		return false
	}
	for _, character := range owner.Token {
		if character < '0' || character > '9' && character < 'a' || character > 'f' {
			return false
		}
	}
	if !utf8.ValidString(owner.Hostname) || strings.ContainsAny(owner.Hostname, "\x00\r\n") || !utf8.ValidString(owner.StartedAt) {
		return false
	}
	_, err := time.Parse(time.RFC3339Nano, owner.StartedAt)
	return err == nil
}

func canonicalJSON(value any) []byte {
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(value)
	return output.Bytes()
}
