package pullflow

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/ontopix/engram/internal/checker"
	"github.com/ontopix/engram/internal/gitraw"
	"github.com/ontopix/engram/internal/journal"
	"github.com/ontopix/engram/internal/managedread"
	"github.com/ontopix/engram/internal/rendezvous"
	"github.com/ontopix/engram/internal/treeimage"
)

type transitionPhase string

var errTransitionChanged = errors.New("local transition journal changed concurrently")

const (
	transitionPrepared      transitionPhase = "prepared"
	transitionRefDispatched transitionPhase = "ref-dispatched"
	transitionRefsUpdated   transitionPhase = "refs-updated"
	transitionHeadUpdated   transitionPhase = "head-updated"
	transitionIndexUpdated  transitionPhase = "index-updated"
	transitionWorktreeDone  transitionPhase = "worktree-updated"
	transitionComplete      transitionPhase = "complete"
	transitionCancelled     transitionPhase = "cancelled"
)

type transitionRef struct {
	Ref    string  `json:"ref"`
	Before *string `json:"before"`
	After  *string `json:"after"`
}

type pathImage struct {
	Kind string `json:"kind"`
	Mode uint32 `json:"mode"`
	Data []byte `json:"data"`
}

type pathTransition struct {
	Path   string     `json:"path"`
	Before *pathImage `json:"before"`
	After  *pathImage `json:"after"`
}

type transitionRecord struct {
	Version      int                  `json:"version"`
	Phase        transitionPhase      `json:"phase"`
	OwnerToken   string               `json:"owner_token"`
	ObjectFormat gitraw.ObjectFormat  `json:"object_format"`
	Refs         []transitionRef      `json:"refs"`
	HeadBefore   managedread.GitState `json:"head_before"`
	HeadAfter    managedread.GitState `json:"head_after"`
	IndexBefore  journal.RawFileImage `json:"index_before"`
	IndexAfter   journal.RawFileImage `json:"index_after"`
	Paths        []pathTransition     `json:"paths"`
}

type transitionRequest struct {
	repository *gitraw.Repository
	refs       []transitionRef
	headAfter  managedread.GitState
	snapshot   *checker.Snapshot
	modes      map[string]gitraw.TreeMode
	index      []byte
	allowDraft bool
}

type refUpdateOutcome uint8

const (
	refUpdateSucceeded refUpdateOutcome = iota
	refUpdateRejected
	refUpdateUnknown
)

var activeTransitionTokens sync.Map

func transitionPath(repository *gitraw.Repository) string {
	return filepath.Join(repository.GitDir, "engram", "replay", "transition-v1.json")
}

func (p *Puller) transition(ctx context.Context, request transitionRequest) (mutation *Mutation, resultErr error) {
	if request.repository == nil || request.repository.Head == nil || request.snapshot == nil || request.snapshot.Tree == nil || request.headAfter.Ref == nil || request.headAfter.Commit == nil || len(request.refs) == 0 {
		return nil, typed(ErrorOperational, "prepare local synchronization", errors.New("incomplete transition request"))
	}
	refnames := make([]string, len(request.refs))
	for index, update := range request.refs {
		refnames[index] = update.Ref
	}
	lock, err := rendezvous.AcquireWriter(request.repository.CommonGitDir, request.repository.GitDir, refnames...)
	if err != nil {
		return nil, classifyLocal(ctx, "lock local synchronization", err)
	}
	activeTransitionTokens.Store(lock.Owner().Token, struct{}{})
	release := true
	defer func() {
		if !release {
			activeTransitionTokens.Delete(lock.Owner().Token)
			return
		}
		activeTransitionTokens.Delete(lock.Owner().Token)
		if err := lock.Release(); err != nil {
			operation := "release local synchronization locks"
			priorMutation := MutationOf(resultErr)
			releaseErr := classifyLocal(ctx, operation, err)
			combined := mergePullMutations(priorMutation, rendezvousMutation(lock, err))
			resultErr = errors.Join(resultErr, releaseErr)
			if combined != nil {
				resultErr = replayErrorWithMutation(resultErr, operation, combined)
			}
		}
	}()

	current, err := gitraw.Discover(ctx, request.repository.Root)
	if err != nil {
		return nil, classifyReadError(ctx, "recapture repository", err)
	}
	if !sameTopology(request.repository, current) || current.Head == nil || current.HeadRef != request.repository.HeadRef || !current.Head.Equal(*request.repository.Head) {
		return nil, typed(ErrorConcurrency, "recapture repository", errors.New("HEAD or accepted ref changed before synchronization"))
	}
	if _, present, err := readControllerFile(transitionPath(current)); err != nil || present {
		if err == nil {
			err = ErrRecovery
		}
		return nil, typed(ErrorConflict, "inspect pull recovery", err)
	}
	opened, err := managedread.Open(ctx, current.Root)
	if err != nil {
		return nil, classifyReadError(ctx, "open locked repository", err)
	}
	status, err := opened.Status(ctx)
	if err != nil {
		return nil, classifyReadError(ctx, "verify clean synchronization input", err)
	}
	if len(status.Unstaged) != 0 || !request.allowDraft && len(status.Staged) != 0 {
		return nil, typed(ErrorConflict, "verify clean synchronization input", ErrUnrelated)
	}
	accepted, err := opened.Accepted(ctx)
	if err != nil {
		return nil, classifyReadError(ctx, "capture accepted synchronization input", err)
	}
	beforeView := accepted
	if request.allowDraft && len(status.Staged) != 0 {
		beforeView, err = opened.Staged(ctx)
		if err != nil {
			return nil, classifyReadError(ctx, "capture staged resolution input", err)
		}
	}
	beforeImage, err := treeimage.FromSnapshot(beforeView.Snapshot.Tree, beforeView.Modes)
	if err != nil {
		return nil, typed(ErrorRepository, "capture accepted synchronization input", err)
	}
	afterImage, err := treeimage.FromSnapshot(request.snapshot.Tree, request.modes)
	if err != nil {
		return nil, typed(ErrorRepository, "capture target synchronization image", err)
	}
	paths, err := capturePathTransitions(current.Root, beforeImage, afterImage)
	if err != nil {
		return nil, typed(ErrorConcurrency, "capture worktree reconciliation", err)
	}
	indexBefore, indexPresent, err := readOptionalFile(filepath.Join(current.GitDir, "index"))
	if err != nil {
		return nil, typed(ErrorIO, "capture index reconciliation", err)
	}
	git, err := p.gitPath()
	if err != nil {
		return nil, typed(ErrorCapability, "locate git", err)
	}
	indexAfter := append([]byte(nil), request.index...)
	if len(indexAfter) == 0 {
		indexAfter, err = p.indexForCommit(ctx, git, current, *request.headAfter.Commit)
		if err != nil {
			return nil, err
		}
	}
	record := transitionRecord{
		Version: 1, Phase: transitionPrepared, OwnerToken: lock.Owner().Token, ObjectFormat: current.Format,
		Refs:        append([]transitionRef(nil), request.refs...),
		HeadBefore:  managedread.GitState{Ref: stringPointer(current.HeadRef), Commit: stringPointer(current.Head.String())},
		HeadAfter:   cloneGitState(request.headAfter),
		IndexBefore: journal.RawFileImage{Present: indexPresent, Data: append([]byte(nil), indexBefore...)},
		IndexAfter:  journal.RawFileImage{Present: true, Data: indexAfter}, Paths: paths,
	}
	if err := validateTransition(record); err != nil {
		return nil, typed(ErrorOperational, "validate local transition journal", err)
	}
	journalBytes, _ := encodeCanonical(record)
	if err := createControllerFileAfter(transitionPath(current), journalBytes, func() error {
		return p.checkpoint(PhaseTransitionPublished)
	}); err != nil {
		if controllerFilePublished(err) {
			// The exact prepared journal may now be durable. Retain its
			// pre-journal rendezvous owner so bounded recovery can either
			// cancel it or prove a later transition.
			release = false
			return nil, recoveryError("publish local transition journal", record, false, false, err)
		}
		return nil, typed(ErrorIO, "publish local transition journal", err)
	}
	if err := lock.SetPhase(rendezvous.JournalRequired); err != nil {
		release = false
		return nil, recoveryError("advance local transition owner", record, false, false, err)
	}
	if err := p.checkpoint(phaseForTransition(request, PhaseFastForwarding)); err != nil {
		cancelErr := p.cancelTransition(current, record, journalBytes, lock, err)
		activeTransitionTokens.Delete(lock.Owner().Token)
		release = false
		return nil, cancelErr
	}
	if err := verifyTransitionInputs(ctx, current, record); err != nil {
		cancelErr := p.cancelTransition(current, record, journalBytes, lock, err)
		activeTransitionTokens.Delete(lock.Owner().Token)
		release = false
		return nil, cancelErr
	}
	record, journalBytes, err = setTransitionPhase(current, record, journalBytes, transitionRefDispatched)
	if err != nil {
		release = false
		return nil, recoveryError("record ref dispatch", record, false, false, err)
	}
	refOutcome, refErr := p.updateRefs(ctx, git, current, record.Refs)
	if refOutcome == refUpdateRejected {
		cancelErr := p.cancelTransition(current, record, journalBytes, lock, typed(ErrorConcurrency, "compare-and-swap local refs", refErr))
		activeTransitionTokens.Delete(lock.Owner().Token)
		release = false
		return nil, cancelErr
	}
	if refOutcome == refUpdateUnknown {
		release = false
		return nil, recoveryError("update local synchronization refs", record, false, false, refErr)
	}
	record, journalBytes, err = setTransitionPhase(current, record, journalBytes, transitionRefsUpdated)
	if err != nil {
		release = false
		return nil, recoveryError("record local ref updates", record, true, false, err)
	}
	if err := p.checkpoint(PhaseRefUpdated); err != nil {
		release = false
		return nil, recoveryError("local ref update", record, true, false, err)
	}
	if record.HeadBefore.Ref == nil || record.HeadAfter.Ref == nil {
		release = false
		return nil, recoveryError("switch symbolic HEAD", record, true, false, errors.New("symbolic HEAD state is unavailable"))
	}
	if *record.HeadBefore.Ref != *record.HeadAfter.Ref {
		if err := p.symbolicHead(ctx, git, current, *record.HeadBefore.Ref, *record.HeadAfter.Ref); err != nil {
			release = false
			return nil, recoveryError("switch symbolic HEAD", record, true, false, err)
		}
	}
	record, journalBytes, err = setTransitionPhase(current, record, journalBytes, transitionHeadUpdated)
	if err != nil {
		release = false
		return nil, recoveryError("record symbolic HEAD update", record, true, false, err)
	}
	if err := p.checkpoint(PhaseHeadUpdated); err != nil {
		release = false
		return nil, recoveryError("symbolic HEAD update", record, true, false, err)
	}
	checkoutChanged := false
	indexChanged, err := installIndex(current, record)
	checkoutChanged = checkoutChanged || indexChanged
	if err != nil {
		release = false
		return nil, recoveryError("reconcile synchronization index", record, true, checkoutChanged, err)
	}
	record, journalBytes, err = setTransitionPhase(current, record, journalBytes, transitionIndexUpdated)
	if err != nil {
		release = false
		return nil, recoveryError("record index reconciliation", record, true, checkoutChanged, err)
	}
	if err := p.checkpoint(PhaseIndexUpdated); err != nil {
		release = false
		return nil, recoveryError("index reconciliation", record, true, checkoutChanged, err)
	}
	pathsChanged, err := reconcilePaths(current.Root, record.Paths)
	checkoutChanged = checkoutChanged || pathsChanged
	if err != nil {
		release = false
		return nil, recoveryError("reconcile synchronization worktree", record, true, checkoutChanged, err)
	}
	record, journalBytes, err = setTransitionPhase(current, record, journalBytes, transitionWorktreeDone)
	if err != nil {
		release = false
		return nil, recoveryError("record worktree reconciliation", record, true, checkoutChanged, err)
	}
	if err := p.checkpoint(PhaseWorktreeUpdated); err != nil {
		release = false
		return nil, recoveryError("worktree reconciliation", record, true, checkoutChanged, err)
	}
	if err := verifyTransitionResult(ctx, current.Root, record); err != nil {
		release = false
		return nil, recoveryError("verify local synchronization", record, true, checkoutChanged, err)
	}
	record, journalBytes, err = setTransitionPhase(current, record, journalBytes, transitionComplete)
	if err != nil {
		release = false
		return nil, recoveryError("complete local transition journal", record, true, checkoutChanged, err)
	}
	if err := lock.Release(); err != nil {
		release = false
		return nil, recoveryRendezvousError("release local transition locks", record, true, checkoutChanged, lock, err)
	}
	activeTransitionTokens.Delete(lock.Owner().Token)
	release = false
	if err := removeTransition(current, record, journalBytes); err != nil {
		return nil, recoveryError("remove local transition journal", record, true, checkoutChanged, err)
	}
	return mutationFromRecord(record, true, checkoutChanged, false), nil
}

func phaseForTransition(request transitionRequest, fallback Phase) Phase {
	if len(request.refs) == 1 && request.refs[0].Ref == request.repository.HeadRef && request.headAfter.Ref != nil && *request.headAfter.Ref == request.repository.HeadRef {
		return PhaseFastForwarding
	}
	return fallback
}

func (p *Puller) cancelTransition(repository *gitraw.Repository, record transitionRecord, expected []byte, lock *rendezvous.Handle, cause error) error {
	updated, bytes, err := setTransitionPhase(repository, record, expected, transitionCancelled)
	if err != nil {
		return recoveryError("cancel local transition", record, false, false, errors.Join(cause, err))
	}
	if lock == nil {
		return recoveryError("release cancelled local transition", updated, false, false, errors.Join(cause, ErrRecovery))
	}
	if err := lock.Release(); err != nil {
		return recoveryRendezvousError("release cancelled local transition", updated, false, false, lock, errors.Join(cause, err))
	}
	if err := removeTransition(repository, updated, bytes); err != nil {
		return recoveryError("clean cancelled local transition", updated, false, false, errors.Join(cause, err))
	}
	return classifyLocal(context.Background(), "local transition rejected", cause)
}

func transitionMutation(record transitionRecord, durable, checkout, recovery bool) *Mutation {
	result := &Mutation{Durable: durable, LocalRefs: []RefMutation{}, CheckoutChanged: checkout, RecoveryRequired: recovery}
	if !durable {
		return result
	}
	for _, update := range record.Refs {
		if equalString(update.Before, update.After) {
			continue
		}
		result.LocalRefs = append(result.LocalRefs, RefMutation{Ref: update.Ref, Before: cloneString(update.Before), After: cloneString(update.After)})
	}
	if !sameGitState(record.HeadBefore, record.HeadAfter) {
		result.Head = &HeadMutation{Before: cloneGitState(record.HeadBefore), After: cloneGitState(record.HeadAfter)}
	}
	return result
}

func mutationFromRecord(record transitionRecord, durable, checkout, recovery bool) *Mutation {
	return transitionMutation(record, durable, checkout, recovery)
}

func recoveryError(operation string, record transitionRecord, durable, checkout bool, err error) error {
	mutation := mergePullMutations(transitionMutation(record, durable, checkout, true), rendezvousMutation(nil, err))
	// A controller publication can be durable before any ref effect is known.
	// Preserve that fact without turning the journal's planned refs/HEAD into
	// effects of this invocation.
	mutation.Durable = mutation.Durable || controllerFileDurable(err)
	mutation.RecoveryRequired = true
	return &Error{Kind: ErrorOperational, Operation: operation, Mutation: mutation, Err: errors.Join(ErrRecovery, err)}
}

func recoveryRendezvousError(operation string, record transitionRecord, refsKnown, checkout bool, handle *rendezvous.Handle, err error) error {
	mutation := mergePullMutations(transitionMutation(record, refsKnown, checkout, true), rendezvousMutation(handle, err))
	mutation.RecoveryRequired = true
	return &Error{Kind: ErrorOperational, Operation: operation, Mutation: mutation, Err: errors.Join(ErrRecovery, err)}
}

func (p *Puller) indexForCommit(ctx context.Context, git string, repository *gitraw.Repository, commit string) ([]byte, error) {
	parent := p.TempRoot
	if parent == "" {
		parent = repository.GitDir
	}
	directory, err := os.MkdirTemp(parent, "engram-pull-index-")
	if err != nil {
		return nil, typed(ErrorIO, "create private index", err)
	}
	defer os.RemoveAll(directory)
	indexPath := filepath.Join(directory, "index")
	environment := isolatedEnvironment(p.Environment)
	environment = append(environment, "GIT_INDEX_FILE="+indexPath)
	command := execCommand(ctx, git, repository.Root, environment, nil, "read-tree", commit)
	if !command.started {
		return nil, typed(ErrorCapability, "create private index", command.err)
	}
	if command.err != nil || command.status != 0 {
		return nil, typed(ErrorRepository, "create private index", errors.New(commandDetail(command)))
	}
	data, present, err := readOptionalFile(indexPath)
	if err != nil || !present || len(data) == 0 {
		return nil, typed(ErrorIO, "read private index", errors.Join(err, errors.New("Git did not create a private index")))
	}
	store, err := managedread.Open(ctx, repository.Root)
	if err != nil {
		return nil, classifyReadError(ctx, "verify private index", err)
	}
	if _, err := store.StagedFromIndex(ctx, indexPath); err != nil {
		return nil, classifyReadError(ctx, "verify private index", err)
	}
	return append([]byte(nil), data...), nil
}

func (p *Puller) indexForSnapshot(ctx context.Context, git string, repository *gitraw.Repository, snapshot *checker.Snapshot, modes map[string]gitraw.TreeMode) ([]byte, error) {
	if snapshot == nil || snapshot.Tree == nil || len(modes) != len(snapshot.Tree.Files) {
		return nil, typed(ErrorOperational, "construct resolution index", errors.New("candidate or mode map is incomplete"))
	}
	parent := p.TempRoot
	if parent == "" {
		parent = repository.GitDir
	}
	directory, err := os.MkdirTemp(parent, "engram-pull-draft-index-")
	if err != nil {
		return nil, typed(ErrorIO, "create resolution index", err)
	}
	defer os.RemoveAll(directory)
	indexPath := filepath.Join(directory, "index")
	environment := append(isolatedEnvironment(p.Environment), "GIT_INDEX_FILE="+indexPath)
	initialized := execCommand(ctx, git, repository.Root, environment, nil, "read-tree", "--empty")
	if initialized.err != nil || initialized.status != 0 {
		return nil, typed(ErrorRepository, "initialize resolution index", errors.New(commandDetail(initialized)))
	}
	paths := make([]string, 0, len(snapshot.Tree.Files))
	for name := range snapshot.Tree.Files {
		paths = append(paths, name)
	}
	sort.Strings(paths)
	var entries strings.Builder
	for _, name := range paths {
		file := snapshot.Tree.Files[name]
		hashed := p.command(ctx, git, repository.Root, file.Data, "hash-object", "-w", "--stdin")
		if hashed.err != nil || hashed.status != 0 {
			return nil, typed(ErrorRepository, "write resolution blob", errors.New(commandDetail(hashed)))
		}
		oid := strings.TrimSuffix(string(hashed.stdout), "\n")
		if _, err := gitraw.ParseOID(repository.Format, oid); err != nil {
			return nil, typed(ErrorRepository, "write resolution blob", err)
		}
		mode := modes[name]
		if !mode.IsRegular() {
			return nil, typed(ErrorRepository, "construct resolution index", fmt.Errorf("%q has invalid mode %q", name, mode))
		}
		fmt.Fprintf(&entries, "%s %s\t%s\x00", mode, oid, name)
	}
	updated := execCommand(ctx, git, repository.Root, environment, []byte(entries.String()), "update-index", "-z", "--index-info")
	if updated.err != nil || updated.status != 0 {
		return nil, typed(ErrorRepository, "populate resolution index", errors.New(commandDetail(updated)))
	}
	data, present, err := readOptionalFile(indexPath)
	if err != nil || !present || len(data) == 0 {
		return nil, typed(ErrorIO, "read resolution index", errors.Join(err, errors.New("resolution index is absent")))
	}
	store, err := managedread.Open(ctx, repository.Root)
	if err != nil {
		return nil, classifyReadError(ctx, "verify resolution index", err)
	}
	projected, err := store.StagedFromIndex(ctx, indexPath)
	if err != nil {
		return nil, classifyReadError(ctx, "verify resolution index", err)
	}
	if !sameSnapshot(projected.Snapshot, snapshot) {
		return nil, typed(ErrorOperational, "verify resolution index", errors.New("private index does not project to the requested candidate"))
	}
	return data, nil
}

func execCommand(ctx context.Context, executable, root string, environment []string, input []byte, arguments ...string) commandResult {
	global := []string{
		"--no-pager", "--no-optional-locks", "--no-replace-objects",
		"-c", "core.hooksPath=" + os.DevNull, "-c", "core.fsmonitor=false", "-c", "core.untrackedCache=false",
		"-c", "maintenance.auto=false", "-c", "gc.auto=0", "-C", root,
	}
	command := exec.CommandContext(ctx, executable, append(global, arguments...)...)
	command.Env = environment
	command.Stdin = bytes.NewReader(input)
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Start(); err != nil {
		return commandResult{status: -1, err: err}
	}
	result := commandResult{started: true}
	err := command.Wait()
	result.stdout, result.stderr = stdout.Bytes(), stderr.Bytes()
	if err == nil {
		return result
	}
	if ctx.Err() != nil {
		result.status, result.err = -1, ctx.Err()
		return result
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		result.status = exit.ExitCode()
		return result
	}
	result.status, result.err = -1, err
	return result
}

func (p *Puller) updateRefs(ctx context.Context, git string, repository *gitraw.Repository, updates []transitionRef) (refUpdateOutcome, error) {
	var input strings.Builder
	input.WriteString("start\n")
	zero := strings.Repeat("0", repository.Format.HexWidth())
	for _, update := range updates {
		before, after := zero, zero
		if update.Before != nil {
			before = *update.Before
		}
		if update.After != nil {
			after = *update.After
		}
		if equalString(update.Before, update.After) {
			fmt.Fprintf(&input, "verify %s %s\n", update.Ref, before)
		} else {
			fmt.Fprintf(&input, "update %s %s %s\n", update.Ref, after, before)
		}
	}
	input.WriteString("prepare\ncommit\n")
	result := p.command(ctx, git, repository.Root, []byte(input.String()), "update-ref", "--stdin")
	if !result.started {
		return refUpdateRejected, result.err
	}
	if result.err == nil && result.status == 0 && bytes.Contains(result.stdout, []byte("commit: ok")) {
		return refUpdateSucceeded, nil
	}
	detail := errors.New(commandDetail(result))
	if ctx.Err() != nil || result.err != nil || bytes.Contains(result.stdout, []byte("prepare: ok")) {
		return refUpdateUnknown, detail
	}
	return refUpdateRejected, detail
}

func (p *Puller) symbolicHead(ctx context.Context, git string, repository *gitraw.Repository, before, after string) error {
	current := p.command(ctx, git, repository.Root, nil, "symbolic-ref", "--quiet", "HEAD")
	if current.err != nil || current.status != 0 || strings.TrimSuffix(string(current.stdout), "\n") != before {
		return errors.New("symbolic HEAD changed before branch switch")
	}
	updated := p.command(ctx, git, repository.Root, nil, "symbolic-ref", "HEAD", after)
	if updated.err != nil || updated.status != 0 {
		return errors.New(commandDetail(updated))
	}
	return nil
}

func capturePathTransitions(root string, before, after treeimage.Image) ([]pathTransition, error) {
	set := make(map[string]struct{}, len(before)+len(after))
	for name := range before {
		set[name] = struct{}{}
	}
	for name := range after {
		set[name] = struct{}{}
	}
	paths := make([]string, 0, len(set))
	for name := range set {
		paths = append(paths, name)
	}
	sort.Strings(paths)
	result := make([]pathTransition, 0, len(paths))
	for _, name := range paths {
		observed, err := observePath(root, name)
		if err != nil {
			return nil, err
		}
		beforeEntry, beforePresent := before[name]
		if !matchesTreeImage(observed, beforeEntry, beforePresent) {
			return nil, fmt.Errorf("worktree path %q does not match the clean accepted preimage", name)
		}
		var final *pathImage
		if entry, present := after[name]; present {
			final = imageFromTree(entry)
			if observed != nil && observed.Kind == final.Kind && beforePresent {
				final.Mode = observed.Mode
			}
		}
		// Preserve an otherwise-pruned physical child by retaining its parent
		// directory. Reconciliation owns only logical entries in this record.
		if final == nil && observed != nil && observed.Kind == "directory" {
			entries, err := os.ReadDir(filepath.Join(root, filepath.FromSlash(name)))
			if err != nil {
				return nil, err
			}
			for _, entry := range entries {
				if _, owned := set[name+"/"+entry.Name()]; !owned {
					copy := *observed
					final = &copy
					break
				}
			}
		}
		result = append(result, pathTransition{Path: name, Before: observed, After: final})
	}
	return result, nil
}

func observePath(root, logical string) (*pathImage, error) {
	name := filepath.Join(root, filepath.FromSlash(logical))
	before, err := os.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	result := &pathImage{Mode: uint32(before.Mode().Perm())}
	switch {
	case before.Mode()&os.ModeSymlink != 0:
		result.Kind = "symlink"
		target, err := os.Readlink(name)
		if err != nil {
			return nil, err
		}
		result.Data = []byte(target)
	case before.IsDir():
		result.Kind = "directory"
	case before.Mode().IsRegular():
		result.Kind = "regular"
		result.Data, err = os.ReadFile(name)
		if err != nil {
			return nil, err
		}
		after, err := os.Lstat(name)
		if err != nil || !after.Mode().IsRegular() || !os.SameFile(before, after) || before.Mode().Perm() != after.Mode().Perm() {
			return nil, fmt.Errorf("worktree path %q changed while being captured", logical)
		}
	default:
		return nil, fmt.Errorf("worktree path %q has an unsupported kind", logical)
	}
	return result, nil
}

func matchesTreeImage(observed *pathImage, expected treeimage.Entry, present bool) bool {
	if !present {
		return observed == nil
	}
	if observed == nil || observed.Kind != string(expected.Kind) {
		return false
	}
	return expected.Kind == treeimage.Directory || bytes.Equal(observed.Data, expected.Data)
}

func imageFromTree(entry treeimage.Entry) *pathImage {
	return &pathImage{Kind: string(entry.Kind), Mode: uint32(entry.Mode.Perm()), Data: append([]byte(nil), entry.Data...)}
}

func samePathImage(left, right *pathImage) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	if left.Kind != right.Kind || !bytes.Equal(left.Data, right.Data) {
		return false
	}
	// Directory permission bits are presentation evidence and are subject to
	// the host umask. Windows also cannot represent POSIX group/other or
	// executable bits for regular files, so compare only host-observable bits.
	return left.Kind == "directory" || equivalentPathPermissions(left.Mode, right.Mode)
}

func verifyTransitionInputs(ctx context.Context, repository *gitraw.Repository, record transitionRecord) error {
	current, err := gitraw.Discover(ctx, repository.Root)
	if err != nil {
		return err
	}
	if !sameTopology(repository, current) || current.Head == nil || record.HeadBefore.Ref == nil || record.HeadBefore.Commit == nil || current.HeadRef != *record.HeadBefore.Ref || current.Head.String() != *record.HeadBefore.Commit {
		return errors.New("HEAD changed before local transition")
	}
	for _, update := range record.Refs {
		value, err := resolveRef(ctx, repository.Root, update.Ref, repository.Format)
		if err != nil || !equalString(value, update.Before) {
			return errors.New("local ref changed before transition")
		}
	}
	index, present, err := readOptionalFile(filepath.Join(repository.GitDir, "index"))
	if err != nil || present != record.IndexBefore.Present || !bytes.Equal(index, record.IndexBefore.Data) {
		return errors.New("index changed before local transition")
	}
	for _, update := range record.Paths {
		observed, err := observePath(repository.Root, update.Path)
		if err != nil || !samePathImage(observed, update.Before) {
			return fmt.Errorf("worktree path %q changed before local transition", update.Path)
		}
	}
	return nil
}

func resolveRef(ctx context.Context, root, ref string, format gitraw.ObjectFormat) (*string, error) {
	git, err := exec.LookPath("git")
	if err != nil {
		return nil, err
	}
	probe := execCommand(ctx, git, root, isolatedEnvironment(nil), nil, "show-ref", "--verify", "--quiet", ref)
	if probe.status == 1 && probe.err == nil {
		return nil, nil
	}
	if probe.err != nil || probe.status != 0 {
		return nil, errors.New(commandDetail(probe))
	}
	result := execCommand(ctx, git, root, isolatedEnvironment(nil), nil, "show-ref", "--verify", "--hash", ref)
	if result.err != nil || result.status != 0 {
		return nil, errors.New(commandDetail(result))
	}
	value := strings.TrimSuffix(string(result.stdout), "\n")
	if _, err := gitraw.ParseOID(format, value); err != nil {
		return nil, err
	}
	return stringPointer(value), nil
}

func installIndex(repository *gitraw.Repository, record transitionRecord) (bool, error) {
	name := filepath.Join(repository.GitDir, "index")
	current, present, err := readOptionalFile(name)
	if err != nil {
		return false, err
	}
	if present && bytes.Equal(current, record.IndexAfter.Data) {
		return false, nil
	}
	if present != record.IndexBefore.Present || !bytes.Equal(current, record.IndexBefore.Data) {
		return false, errors.New("index has neither its recorded preimage nor final image")
	}
	lockName := name + ".lock"
	file, err := os.OpenFile(lockName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return false, err
	}
	remove := true
	defer func() {
		if remove {
			_ = os.Remove(lockName)
		}
	}()
	current, present, err = readOptionalFile(name)
	if err != nil || present != record.IndexBefore.Present || !bytes.Equal(current, record.IndexBefore.Data) {
		file.Close()
		return false, errors.New("index changed before native publication")
	}
	if _, err := file.Write(record.IndexAfter.Data); err != nil {
		file.Close()
		return false, err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return false, err
	}
	if err := file.Close(); err != nil {
		return false, err
	}
	if err := os.Rename(lockName, name); err != nil {
		return false, err
	}
	remove = false
	return true, syncDirectory(filepath.Dir(name))
}

func reconcilePaths(root string, transitions []pathTransition) (bool, error) {
	changed := false
	current := make(map[string]*pathImage, len(transitions))
	for _, update := range transitions {
		observed, err := observePath(root, update.Path)
		if err != nil {
			return changed, err
		}
		if !samePathImage(observed, update.Before) && !samePathImage(observed, update.After) {
			return changed, fmt.Errorf("worktree path %q has neither recorded image", update.Path)
		}
		current[update.Path] = observed
	}
	ordered := append([]pathTransition(nil), transitions...)
	sort.Slice(ordered, func(i, j int) bool {
		left, right := strings.Count(ordered[i].Path, "/"), strings.Count(ordered[j].Path, "/")
		if left != right {
			return left > right
		}
		return ordered[i].Path > ordered[j].Path
	})
	for _, update := range ordered {
		observed := current[update.Path]
		if samePathImage(observed, update.After) || observed == nil {
			continue
		}
		if update.After == nil || observed.Kind != update.After.Kind && (observed.Kind == "directory" || update.After.Kind == "directory") {
			name := filepath.Join(root, filepath.FromSlash(update.Path))
			var err error
			if observed.Kind == "directory" {
				err = os.Remove(name)
			} else {
				err = os.Remove(name)
			}
			if err != nil {
				return changed, err
			}
			changed = true
			current[update.Path] = nil
		}
	}
	sort.Slice(ordered, func(i, j int) bool {
		left, right := strings.Count(ordered[i].Path, "/"), strings.Count(ordered[j].Path, "/")
		if left != right {
			return left < right
		}
		return ordered[i].Path < ordered[j].Path
	})
	for _, update := range ordered {
		if samePathImage(current[update.Path], update.After) || update.After == nil {
			continue
		}
		name := filepath.Join(root, filepath.FromSlash(update.Path))
		switch update.After.Kind {
		case "directory":
			if current[update.Path] != nil {
				return changed, fmt.Errorf("cannot create directory at %q", update.Path)
			}
			if err := os.Mkdir(name, fs.FileMode(update.After.Mode)); err != nil {
				return changed, err
			}
		case "regular":
			if err := replaceRegular(name, update.After); err != nil {
				return changed, err
			}
		default:
			return changed, fmt.Errorf("unsupported final path kind %q", update.After.Kind)
		}
		changed = true
		current[update.Path] = update.After
	}
	for _, update := range transitions {
		observed, err := observePath(root, update.Path)
		if err != nil || !samePathImage(observed, update.After) {
			return changed, fmt.Errorf("worktree path %q did not reach its final image", update.Path)
		}
	}
	return changed, nil
}

func replaceRegular(name string, image *pathImage) error {
	directory := filepath.Dir(name)
	temporary, err := os.CreateTemp(directory, ".engram-pull-*")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(fs.FileMode(image.Mode)); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(image.Data); err != nil {
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
	return syncDirectory(directory)
}

func verifyTransitionResult(ctx context.Context, root string, record transitionRecord) error {
	current, err := gitraw.Discover(ctx, root)
	if err != nil {
		return err
	}
	if current.Head == nil || record.HeadAfter.Ref == nil || record.HeadAfter.Commit == nil || current.HeadRef != *record.HeadAfter.Ref || current.Head.String() != *record.HeadAfter.Commit {
		return errors.New("HEAD does not name the completed transition")
	}
	for _, update := range record.Refs {
		value, err := resolveRef(ctx, root, update.Ref, record.ObjectFormat)
		if err != nil || !equalString(value, update.After) {
			return errors.New("local ref does not match completed transition")
		}
	}
	index, present, err := readOptionalFile(filepath.Join(current.GitDir, "index"))
	if err != nil || !present || !bytes.Equal(index, record.IndexAfter.Data) {
		return errors.New("index does not match completed transition")
	}
	for _, update := range record.Paths {
		value, err := observePath(root, update.Path)
		if err != nil || !samePathImage(value, update.After) {
			return errors.New("worktree does not match completed transition")
		}
	}
	return nil
}

func sameTopology(left, right *gitraw.Repository) bool {
	return left != nil && right != nil && left.Root == right.Root && left.GitDir == right.GitDir && left.CommonGitDir == right.CommonGitDir && left.Format == right.Format
}

func sameSnapshot(left, right *checker.Snapshot) bool {
	if left == nil || right == nil || left.Tree == nil || right.Tree == nil || len(left.Tree.Files) != len(right.Tree.Files) || len(left.Tree.Directories) != len(right.Tree.Directories) {
		return false
	}
	for name, file := range left.Tree.Files {
		other, ok := right.Tree.Files[name]
		if !ok || !bytes.Equal(file.Data, other.Data) {
			return false
		}
	}
	return true
}

func readOptionalFile(name string) ([]byte, bool, error) {
	data, err := os.ReadFile(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, true, err
	}
	info, err := os.Lstat(name)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, true, errors.New("path is not a real regular file")
	}
	return data, true, nil
}

func validateTransition(record transitionRecord) error {
	if record.Version != 1 || !validTransitionPhase(record.Phase) || len(record.OwnerToken) != 64 || record.Refs == nil || len(record.Refs) == 0 || record.Paths == nil || !record.IndexAfter.Present || len(record.IndexAfter.Data) == 0 {
		return errors.New("invalid transition record")
	}
	previous := ""
	for _, update := range record.Refs {
		if !strings.HasPrefix(update.Ref, "refs/heads/") || previous != "" && previous >= update.Ref || update.Before == nil && update.After == nil {
			return errors.New("invalid or unordered transition refs")
		}
		previous = update.Ref
		for _, value := range []*string{update.Before, update.After} {
			if value != nil {
				if _, err := gitraw.ParseOID(record.ObjectFormat, *value); err != nil {
					return err
				}
			}
		}
	}
	previous = ""
	for _, update := range record.Paths {
		if !validLogicalPath(update.Path) || previous != "" && previous >= update.Path || update.Before == nil && update.After == nil {
			return errors.New("invalid or unordered transition paths")
		}
		if err := validatePathImage(update.Before); err != nil {
			return err
		}
		if err := validatePathImage(update.After); err != nil {
			return err
		}
		previous = update.Path
	}
	return nil
}

func validatePathImage(image *pathImage) error {
	if image == nil {
		return nil
	}
	switch image.Kind {
	case "regular", "directory", "symlink":
	default:
		return fmt.Errorf("invalid transition path image kind %q", image.Kind)
	}
	if image.Mode&^0o777 != 0 {
		return errors.New("transition path image has non-permission mode bits")
	}
	if image.Kind == "directory" && len(image.Data) != 0 {
		return errors.New("transition directory image carries bytes")
	}
	return nil
}

func validTransitionPhase(phase transitionPhase) bool {
	switch phase {
	case transitionPrepared, transitionRefDispatched, transitionRefsUpdated, transitionHeadUpdated,
		transitionIndexUpdated, transitionWorktreeDone, transitionComplete, transitionCancelled:
		return true
	default:
		return false
	}
}

type controllerPublicationError struct {
	path    string
	durable bool
	err     error
}

func (e *controllerPublicationError) Error() string { return e.err.Error() }
func (e *controllerPublicationError) Unwrap() error { return e.err }

func controllerFilePublished(err error) bool {
	var published *controllerPublicationError
	return errors.As(err, &published)
}

func controllerFileDurable(err error) bool {
	var published *controllerPublicationError
	return errors.As(err, &published) && published.durable
}

func createControllerFile(name string, data []byte) error {
	return createControllerFileAfter(name, data, nil)
}

// createControllerFileAfter exposes the first post-link boundary to callers
// that publish recovery authority. Every error after link(2) is typed so the
// caller cannot accidentally report an ordinary I/O failure after durable
// controller state may already exist.
func createControllerFileAfter(name string, data []byte, afterLink func() error) error {
	if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(name), ".engram-transition-pending-*")
	if err != nil {
		return err
	}
	temporary := file.Name()
	defer os.Remove(temporary)
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		return err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Link(temporary, name); err != nil {
		return err
	}
	publishedError := func(err error, durable bool) error {
		if err == nil {
			return nil
		}
		return &controllerPublicationError{path: name, durable: durable, err: err}
	}
	if afterLink != nil {
		if err := afterLink(); err != nil {
			return publishedError(err, false)
		}
	}
	if err := syncDirectory(filepath.Dir(name)); err != nil {
		return publishedError(err, false)
	}
	if err := os.Remove(temporary); err != nil {
		return publishedError(err, true)
	}
	return publishedError(syncDirectory(filepath.Dir(name)), true)
}

func setTransitionPhase(repository *gitraw.Repository, record transitionRecord, expected []byte, phase transitionPhase) (transitionRecord, []byte, error) {
	current, present, err := readControllerFile(transitionPath(repository))
	if err != nil || !present || !bytes.Equal(current, expected) {
		return record, expected, errTransitionChanged
	}
	record.Phase = phase
	updated, err := encodeCanonical(record)
	if err != nil {
		return record, expected, err
	}
	if err := replaceControllerFile(transitionPath(repository), updated); err != nil {
		return record, expected, err
	}
	return record, updated, nil
}

func removeTransition(repository *gitraw.Repository, record transitionRecord, expected []byte) error {
	if record.Phase != transitionComplete && record.Phase != transitionCancelled {
		return errors.New("cannot remove a nonterminal transition journal")
	}
	current, present, err := readControllerFile(transitionPath(repository))
	if err != nil || !present || !bytes.Equal(current, expected) {
		return errTransitionChanged
	}
	if err := os.Remove(transitionPath(repository)); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(transitionPath(repository)))
}

func randomPrivateRef() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return "refs/heads/engram-pull/" + hex.EncodeToString(value), nil
}

func classifyLocal(ctx context.Context, operation string, err error) error {
	var existing *Error
	if errors.As(err, &existing) {
		return err
	}
	kind := ErrorIO
	if ctx != nil && ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		kind = ErrorCancelled
	} else if errors.Is(err, rendezvous.ErrBusy) {
		kind = ErrorConcurrency
	}
	return &Error{Kind: kind, Operation: operation, Mutation: rendezvousMutation(nil, err), Err: err}
}

// rendezvousMutation reports only effects attributable to this invocation.
// Error metadata covers partial acquisition/adoption; a live handle adds the
// exact locks it still owns when an adapter obscures the underlying error.
func rendezvousMutation(handle *rendezvous.Handle, err error) *Mutation {
	durable := rendezvous.DurableMutationOf(err)
	recoveryRequired := rendezvous.RecoveryRequiredOf(err)
	if handle != nil {
		durable = durable || handle.Mutated()
		recoveryRequired = recoveryRequired || handle.RecoveryRequired()
	}
	if !durable && !recoveryRequired {
		return nil
	}
	return &Mutation{Durable: durable, LocalRefs: []RefMutation{}, RecoveryRequired: recoveryRequired}
}
