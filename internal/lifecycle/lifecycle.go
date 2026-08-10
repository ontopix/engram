// Package lifecycle owns the exact pre-publication state used by init and
// acquisition. The small public sidecar is intentionally compatible with
// doctor's closed state reader; workflow-specific recovery data stays in the
// token-derived private staging directory.
package lifecycle

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/ontopix/engram/internal/fileidentity"
	"github.com/ontopix/engram/internal/rendezvous"
)

const (
	maxStateBytes        = 8 << 20
	maxRecoveryPlanBytes = 16 << 20
)

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
	path                 string
	state                State
	raw                  []byte
	info                 os.FileInfo
	operations           lifecycleOperations
	recoveryExpectation  *RecoveryExpectation
	recoveryPlanRaw      []byte
	recoveryPlanPresent  bool
	recoveryStageID      []byte
	recoveryStagePresent bool
}

// Mutation is the exact lifecycle-controller evidence carried by a failed
// operation. Visible means this call changed the authoritative sidecar name;
// Durable means one such change reached its parent-directory sync boundary.
type Mutation struct {
	Visible          bool
	Durable          bool
	RecoveryRequired bool
}

type mutationError struct {
	mutation Mutation
	err      error
}

func (e *mutationError) Error() string { return e.err.Error() }
func (e *mutationError) Unwrap() error { return e.err }

// MutationOf merges lifecycle evidence across wrapped and joined errors.
// Visible and Durable are monotonic. RecoveryRequired is a final-state fact:
// an outer mutation snapshot overrides its wrapped causes, and the last
// evidence-bearing member of an errors.Join overrides earlier siblings.
func MutationOf(err error) (Mutation, bool) {
	var visit func(error) (Mutation, bool)
	visit = func(current error) (Mutation, bool) {
		if current == nil {
			return Mutation{}, false
		}
		if failure, ok := current.(*mutationError); ok {
			result := failure.mutation
			if nested, present := visit(failure.err); present {
				result.Visible = result.Visible || nested.Visible
				result.Durable = result.Durable || nested.Durable
			}
			return result, true
		}
		switch unwrapped := current.(type) {
		case interface{ Unwrap() []error }:
			result := Mutation{}
			present := false
			for _, child := range unwrapped.Unwrap() {
				childMutation, childPresent := visit(child)
				if !childPresent {
					continue
				}
				result.Visible = result.Visible || childMutation.Visible
				result.Durable = result.Durable || childMutation.Durable
				result.RecoveryRequired = childMutation.RecoveryRequired
				present = true
			}
			return result, present
		case interface{ Unwrap() error }:
			return visit(unwrapped.Unwrap())
		default:
			return Mutation{}, false
		}
	}
	return visit(err)
}

func mutationFailure(err error, mutation Mutation) error {
	if err == nil {
		err = errors.New("unknown lifecycle mutation failure")
	}
	return &mutationError{mutation: mutation, err: err}
}

type lifecycleOperations struct {
	write         func(*os.File, []byte) (int, error)
	syncFile      func(*os.File) error
	closeFile     func(*os.File) error
	removePath    func(string) (bool, error)
	renamePath    func(string, string) (bool, error)
	syncDirectory func(string) (bool, error)
}

func (o lifecycleOperations) writeTo(file *os.File, data []byte) (int, error) {
	if o.write != nil {
		return o.write(file, data)
	}
	return file.Write(data)
}

func (o lifecycleOperations) sync(file *os.File) error {
	if o.syncFile != nil {
		return o.syncFile(file)
	}
	return file.Sync()
}

func (o lifecycleOperations) close(file *os.File) error {
	if o.closeFile != nil {
		return o.closeFile(file)
	}
	return file.Close()
}

func (o lifecycleOperations) remove(name string) (bool, error) {
	if o.removePath != nil {
		return o.removePath(name)
	}
	err := os.Remove(name)
	return err == nil, err
}

func (o lifecycleOperations) rename(oldPath, newPath string) (bool, error) {
	if o.renamePath != nil {
		return o.renamePath(oldPath, newPath)
	}
	err := os.Rename(oldPath, newPath)
	return err == nil, err
}

func (o lifecycleOperations) syncParent(name string) (bool, error) {
	if o.syncDirectory != nil {
		return o.syncDirectory(name)
	}
	err := syncDirectory(name)
	return err == nil, err
}

// RecoveryLease serializes lifecycle recovery controllers across processes.
// Its persistent sibling file is harmless advisory-lock storage: it contains
// no owner authority and is never a doctor-recognized lifecycle sidecar.
type RecoveryLease struct {
	lease *rendezvous.RecoveryLease
}

// RecoveryExpectation binds adoption to controller state previously approved
// by a read-only recovery inspector. StateSHA256 is a framed digest of the
// canonical sidecar and the exact stable stage/plan observation, rather than a
// digest of the sidecar alone. This keeps the public approval shape small while
// making plan appearance, disappearance, replacement, or byte changes visible.
type RecoveryExpectation struct {
	OwnerToken  string
	StateSHA256 string
}

// RecoveryObservation is the stable lifecycle proof used by doctor and by
// recovery adoption. PlanRaw is populated only when the exact token-derived
// stage contains one stable regular plan-v1.json.
type RecoveryObservation struct {
	State         State
	StateRaw      []byte
	Expectation   RecoveryExpectation
	TargetPath    string
	TargetPresent bool
	StagePath     string
	StagePresent  bool
	PlanPath      string
	PlanPresent   bool
	PlanRaw       []byte

	stateInfo  os.FileInfo
	targetInfo os.FileInfo
	stageInfo  os.FileInfo
	planInfo   os.FileInfo

	stateIdentity  []byte
	targetIdentity []byte
	stageIdentity  []byte
	planIdentity   []byte
}

// Sidecar returns the exact target-derived controller-state name.
func Sidecar(target string, operation Operation) string {
	return target + ".engram-" + string(operation) + "-v1.json"
}

// AcquireRecovery takes one exact, nonblocking advisory lease before a caller
// observes or adopts lifecycle state. The target itself may be absent.
func AcquireRecovery(target string, operation Operation) (*RecoveryLease, error) {
	if !filepath.IsAbs(target) || filepath.Clean(target) != target || operation != Initialization && operation != Acquisition {
		return nil, fmt.Errorf("%w: invalid recovery lease target or operation", ErrMalformed)
	}
	lease, err := rendezvous.AcquireRecoveryPath(Sidecar(target, operation) + ".lease")
	if err != nil {
		return nil, err
	}
	return &RecoveryLease{lease: lease}, nil
}

// Release relinquishes the advisory lock. The non-authoritative lease file is
// intentionally retained so future recovery does not need to unlink it.
func (l *RecoveryLease) Release() error {
	if l == nil || l.lease == nil {
		return nil
	}
	lease := l.lease
	l.lease = nil
	return lease.Release()
}

// Stage returns the private staging directory derived from the immutable
// owner token. Recovery never scans or guesses by prefix.
func Stage(state State) (string, error) {
	if err := validateState(state); err != nil {
		return "", err
	}
	// The authoritative sidecar retains the complete target, operation, and
	// 256-bit owner token. The sibling name needs only a deterministic,
	// collision-resistant derivative of that tuple: one target cannot have two
	// live owners because sidecar publication is exclusive. A fixed-size base
	// name also avoids repeating an arbitrarily long target name below Git for
	// Windows' legacy MAX_PATH boundary.
	const stageDigestHex = 32
	identity := sha256.Sum256([]byte(state.Target + "\x00" + string(state.Operation) + "\x00" + state.Owner.Token))
	name := ".engram-stage-v1-" + hex.EncodeToString(identity[:])[:stageDigestHex] + ".stage"
	return filepath.Join(filepath.Dir(state.Target), name), nil
}

// Begin durably publishes one running state without creating the target.
func Begin(target string, operation Operation) (*Handle, error) {
	return beginWithOperations(target, operation, lifecycleOperations{})
}

func beginWithOperations(target string, operation Operation, operations lifecycleOperations) (*Handle, error) {
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
	opened, statErr := file.Stat()
	named, namedErr := os.Lstat(name)
	if statErr != nil || namedErr != nil || !opened.Mode().IsRegular() || named.Mode()&os.ModeSymlink != 0 || !named.Mode().IsRegular() || !os.SameFile(opened, named) {
		closeErr := operations.close(file)
		return nil, cleanupCreatedSidecar(name, opened, operations, false, errors.Join(statErr, namedErr, ErrChanged), closeErr)
	}
	if _, err := operations.writeTo(file, raw); err != nil {
		closeErr := operations.close(file)
		return nil, cleanupCreatedSidecar(name, opened, operations, false, err, closeErr)
	}
	if err := operations.sync(file); err != nil {
		closeErr := operations.close(file)
		return nil, cleanupCreatedSidecar(name, opened, operations, false, err, closeErr)
	}
	if err := operations.close(file); err != nil {
		return nil, cleanupCreatedSidecar(name, opened, operations, false, err, nil)
	}
	if !exactRegularPath(name, opened) {
		return nil, mutationFailure(ErrChanged, Mutation{Visible: true})
	}
	durable, syncErr := operations.syncParent(parent)
	if syncErr != nil || !durable {
		if syncErr == nil {
			syncErr = errors.New("lifecycle directory sync reported no durability")
		}
		return nil, cleanupCreatedSidecar(name, opened, operations, durable, syncErr, nil)
	}
	if !exactRegularPath(name, opened) {
		return nil, mutationFailure(ErrChanged, Mutation{Visible: true, Durable: true})
	}
	return &Handle{path: name, state: state, raw: raw, info: opened, operations: operations}, nil
}

func cleanupCreatedSidecar(name string, identity os.FileInfo, operations lifecycleOperations, durableBefore bool, cause, closeErr error) error {
	removed, removeErr := removeExactRegular(name, identity, operations)
	cleanupDurable := false
	var syncErr error
	if removed {
		cleanupDurable, syncErr = operations.syncParent(filepath.Dir(name))
		if syncErr == nil && !cleanupDurable {
			syncErr = errors.New("lifecycle cleanup directory sync reported no durability")
		}
	}
	mutation := Mutation{
		Visible: true, Durable: durableBefore || removed && cleanupDurable,
		RecoveryRequired: exactRegularPath(name, identity),
	}
	return mutationFailure(errors.Join(cause, closeErr, removeErr, syncErr), mutation)
}

func removeExactRegular(name string, identity os.FileInfo, operations lifecycleOperations) (bool, error) {
	current, err := os.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if identity == nil || current.Mode()&os.ModeSymlink != 0 || !current.Mode().IsRegular() || !os.SameFile(identity, current) {
		return false, ErrChanged
	}
	removed, err := operations.remove(name)
	if err != nil && !removed {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	if removed {
		return true, err
	}
	if err == nil {
		err = errors.New("lifecycle removal reported success without removing the owned path")
	}
	return false, errors.Join(ErrChanged, err)
}

func exactRegularPath(name string, identity os.FileInfo) bool {
	current, err := os.Lstat(name)
	return err == nil && identity != nil && current.Mode()&os.ModeSymlink == 0 && current.Mode().IsRegular() && os.SameFile(identity, current)
}

// Read performs a stable, closed, canonical state read.
func Read(target string, operation Operation) (State, []byte, error) {
	state, raw, _, _, err := readState(target, operation)
	return state, raw, err
}

func readState(target string, operation Operation) (State, []byte, os.FileInfo, []byte, error) {
	name := Sidecar(target, operation)
	raw, info, identity, err := readStableRegular(name, maxStateBytes)
	if err != nil {
		return State{}, nil, nil, nil, err
	}
	var state State
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil || decoder.Decode(&struct{}{}) != io.EOF || validateState(state) != nil || state.Target != target || state.Operation != operation {
		return State{}, nil, nil, nil, ErrMalformed
	}
	canonical, err := encode(state)
	if err != nil || !bytes.Equal(raw, canonical) {
		return State{}, nil, nil, nil, ErrMalformed
	}
	return state, append([]byte(nil), raw...), info, append([]byte(nil), identity...), nil
}

// ObserveRecovery returns one closed stable observation. It samples the whole
// sidecar/stage/plan tuple twice and requires identical file identities and
// bytes, so a lifecycle transition cannot be spliced together from different
// filesystem moments.
func ObserveRecovery(target string, operation Operation) (RecoveryObservation, error) {
	first, err := observeRecoveryOnce(target, operation)
	if err != nil {
		return RecoveryObservation{}, err
	}
	second, err := observeRecoveryOnce(target, operation)
	if err != nil {
		return RecoveryObservation{}, errors.Join(ErrChanged, err)
	}
	if !sameRecoveryObservation(first, second) {
		return RecoveryObservation{}, ErrChanged
	}
	second.Expectation = RecoveryExpectation{
		OwnerToken:  second.State.Owner.Token,
		StateSHA256: recoveryDigest(second),
	}
	second.StateRaw = append([]byte(nil), second.StateRaw...)
	second.PlanRaw = append([]byte(nil), second.PlanRaw...)
	return second, nil
}

func observeRecoveryOnce(target string, operation Operation) (RecoveryObservation, error) {
	state, raw, stateInfo, stateIdentity, err := readState(target, operation)
	if err != nil {
		return RecoveryObservation{}, err
	}
	stage, err := Stage(state)
	if err != nil {
		return RecoveryObservation{}, err
	}
	result := RecoveryObservation{
		State: state, StateRaw: raw, TargetPath: state.Target, StagePath: stage,
		PlanPath: filepath.Join(stage, "plan-v1.json"), stateInfo: stateInfo, stateIdentity: stateIdentity,
	}
	targetInfo, targetIdentity, targetErr := readStableRealDirectory(state.Target)
	if errors.Is(targetErr, os.ErrNotExist) {
		if _, afterErr := os.Lstat(state.Target); !errors.Is(afterErr, os.ErrNotExist) {
			return RecoveryObservation{}, errors.Join(ErrChanged, afterErr)
		}
	} else {
		if targetErr != nil {
			return RecoveryObservation{}, targetErr
		}
		result.TargetPresent = true
		result.targetInfo = targetInfo
		result.targetIdentity = targetIdentity
	}
	stageBefore, stageIdentity, err := readStableRealDirectory(stage)
	if errors.Is(err, os.ErrNotExist) {
		if _, afterErr := os.Lstat(stage); !errors.Is(afterErr, os.ErrNotExist) {
			return RecoveryObservation{}, errors.Join(ErrChanged, afterErr)
		}
		return result, nil
	}
	if err != nil {
		return RecoveryObservation{}, err
	}
	result.StagePresent = true
	result.stageInfo = stageBefore
	result.stageIdentity = stageIdentity
	planRaw, planInfo, planIdentity, planErr := readStableRegular(result.PlanPath, maxRecoveryPlanBytes)
	if planErr == nil {
		result.PlanPresent = true
		result.PlanRaw = planRaw
		result.planInfo = planInfo
		result.planIdentity = planIdentity
	} else if !errors.Is(planErr, os.ErrNotExist) {
		return RecoveryObservation{}, errors.Join(planErr, errors.New("lifecycle recovery plan is not stable"))
	} else if state.Phase == CleanupRequired {
		return RecoveryObservation{}, errors.Join(ErrMalformed, errors.New("cleanup-required lifecycle stage has no publication plan"))
	}
	stageAfter, afterIdentity, err := readStableRealDirectory(stage)
	if err != nil || !os.SameFile(stageBefore, stageAfter) || !bytes.Equal(stageIdentity, afterIdentity) {
		return RecoveryObservation{}, errors.Join(ErrChanged, err)
	}
	result.stageInfo = stageAfter
	return result, nil
}

func sameRecoveryObservation(left, right RecoveryObservation) bool {
	if left.State != right.State || !bytes.Equal(left.StateRaw, right.StateRaw) ||
		left.TargetPath != right.TargetPath || left.TargetPresent != right.TargetPresent ||
		left.StagePath != right.StagePath || left.StagePresent != right.StagePresent ||
		left.PlanPath != right.PlanPath || left.PlanPresent != right.PlanPresent ||
		!os.SameFile(left.stateInfo, right.stateInfo) || !bytes.Equal(left.stateIdentity, right.stateIdentity) {
		return false
	}
	if left.TargetPresent && (!os.SameFile(left.targetInfo, right.targetInfo) || !bytes.Equal(left.targetIdentity, right.targetIdentity)) {
		return false
	}
	if left.StagePresent && (!os.SameFile(left.stageInfo, right.stageInfo) || !bytes.Equal(left.stageIdentity, right.stageIdentity)) {
		return false
	}
	return !left.PlanPresent || os.SameFile(left.planInfo, right.planInfo) && bytes.Equal(left.planIdentity, right.planIdentity) && bytes.Equal(left.PlanRaw, right.PlanRaw)
}

func recoveryDigest(observation RecoveryObservation) string {
	digest := sha256.New()
	writeDigestField := func(label string, value []byte) {
		var length [8]byte
		binary.BigEndian.PutUint64(length[:], uint64(len(label)))
		_, _ = digest.Write(length[:])
		_, _ = digest.Write([]byte(label))
		binary.BigEndian.PutUint64(length[:], uint64(len(value)))
		_, _ = digest.Write(length[:])
		_, _ = digest.Write(value)
	}
	writeDigestField("format", []byte("engram-lifecycle-recovery-v1"))
	writeDigestField("state", observation.StateRaw)
	writeDigestField("state-identity", observation.stateIdentity)
	if observation.TargetPresent {
		writeDigestField("target", []byte("present"))
		writeDigestField("target-identity", observation.targetIdentity)
	} else {
		writeDigestField("target", []byte("absent"))
	}
	if observation.StagePresent {
		writeDigestField("stage", []byte("present"))
		writeDigestField("stage-identity", observation.stageIdentity)
	} else {
		writeDigestField("stage", []byte("absent"))
	}
	if observation.PlanPresent {
		writeDigestField("plan", []byte("present"))
		writeDigestField("plan-identity", observation.planIdentity)
		writeDigestField("plan-bytes", observation.PlanRaw)
	} else {
		writeDigestField("plan", []byte("absent"))
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func readStableRealDirectory(name string) (os.FileInfo, []byte, error) {
	before, err := os.Lstat(name)
	if err != nil {
		return nil, nil, err
	}
	if before.Mode()&os.ModeSymlink != 0 || !before.IsDir() {
		return nil, nil, errors.Join(ErrMalformed, errors.New("lifecycle path is not one real directory"))
	}
	if err := fileidentity.Pin(before); err != nil {
		return nil, nil, errors.Join(ErrChanged, err)
	}
	directory, err := os.Open(name)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: open stable lifecycle directory: %v", ErrChanged, err)
	}
	opened, statErr := directory.Stat()
	var identity []byte
	var identityErr error
	if statErr == nil {
		identity, identityErr = fileidentity.PersistentID(directory, opened)
	}
	after, lstatErr := os.Lstat(name)
	if lstatErr == nil {
		if pinErr := fileidentity.Pin(after); pinErr != nil {
			lstatErr = errors.Join(ErrChanged, pinErr)
		}
	}
	closeErr := directory.Close()
	if statErr != nil || identityErr != nil || lstatErr != nil || closeErr != nil {
		return nil, nil, errors.Join(statErr, identityErr, lstatErr, closeErr)
	}
	if after.Mode()&os.ModeSymlink != 0 || !after.IsDir() ||
		!os.SameFile(before, opened) || !os.SameFile(opened, after) {
		return nil, nil, ErrChanged
	}
	return after, append([]byte(nil), identity...), nil
}

func readStableRegular(name string, maximum int64) ([]byte, os.FileInfo, []byte, error) {
	before, err := os.Lstat(name)
	if err != nil {
		return nil, nil, nil, err
	}
	if before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() {
		return nil, nil, nil, ErrMalformed
	}
	if err := fileidentity.Pin(before); err != nil {
		return nil, nil, nil, errors.Join(ErrChanged, err)
	}
	file, err := os.Open(name)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("%w: open stable lifecycle file: %v", ErrChanged, err)
	}
	openedBefore, openedBeforeErr := file.Stat()
	var identity []byte
	var identityErr error
	if openedBeforeErr == nil {
		identity, identityErr = fileidentity.PersistentID(file, openedBefore)
	}
	raw, readErr := io.ReadAll(io.LimitReader(file, maximum+1))
	openedAfter, openedAfterErr := file.Stat()
	_, seekErr := file.Seek(0, io.SeekStart)
	secondRaw, secondReadErr := io.ReadAll(io.LimitReader(file, maximum+1))
	openedSecond, openedSecondErr := file.Stat()
	closeErr := file.Close()
	if openedBeforeErr != nil || identityErr != nil || readErr != nil || openedAfterErr != nil || seekErr != nil || secondReadErr != nil || openedSecondErr != nil || closeErr != nil {
		return nil, nil, nil, errors.Join(openedBeforeErr, identityErr, readErr, openedAfterErr, seekErr, secondReadErr, openedSecondErr, closeErr)
	}
	if int64(len(raw)) > maximum || int64(len(secondRaw)) > maximum {
		return nil, nil, nil, ErrMalformed
	}
	after, err := os.Lstat(name)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("%w: restat stable lifecycle file: %v", ErrChanged, err)
	}
	if err := fileidentity.Pin(after); err != nil {
		return nil, nil, nil, errors.Join(ErrChanged, err)
	}
	if after.Mode()&os.ModeSymlink != 0 || !after.Mode().IsRegular() ||
		!sameStableFile(before, openedBefore) || !sameStableFile(openedBefore, openedAfter) ||
		!sameStableFile(openedAfter, openedSecond) || !sameStableFile(openedSecond, after) ||
		!bytes.Equal(raw, secondRaw) {
		return nil, nil, nil, ErrChanged
	}
	return append([]byte(nil), raw...), after, append([]byte(nil), identity...), nil
}

func sameStableFile(left, right os.FileInfo) bool {
	return left != nil && right != nil && os.SameFile(left, right) && left.Mode() == right.Mode() && left.Size() == right.Size() && left.ModTime().Equal(right.ModTime())
}

func (h *Handle) State() State {
	if h == nil {
		return State{}
	}
	return h.state
}

// RecoveryRequired reports whether the authoritative sidecar still has the
// exact inode, canonical bytes, owner, and phase represented by this handle.
// A replacement lifecycle record is never attributed to this handle.
func (h *Handle) RecoveryRequired() bool {
	if h == nil || h.path == "" || h.info == nil {
		return false
	}
	current, raw, currentInfo, _, err := readState(h.state.Target, h.state.Operation)
	return err == nil && current == h.state && bytes.Equal(raw, h.raw) && os.SameFile(h.info, currentInfo)
}

// RequireCleanup durably advances the state immediately before any target
// publication. It never moves backward.
func (h *Handle) RequireCleanup() (resultErr error) {
	if h == nil || h.state.Phase != Running {
		return ErrChanged
	}
	current, raw, currentInfo, _, err := readState(h.state.Target, h.state.Operation)
	if err != nil || current.Owner.Token != h.state.Owner.Token || !bytes.Equal(raw, h.raw) || h.info != nil && !os.SameFile(h.info, currentInfo) {
		return errors.Join(ErrChanged, err)
	}
	mutation := Mutation{}
	defer func() {
		if resultErr == nil {
			return
		}
		mutation.RecoveryRequired = h.RecoveryRequired()
		resultErr = mutationFailure(resultErr, mutation)
	}()
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
	temporaryInfo, statErr := temporary.Stat()
	cleanupTemporary := true
	defer func() {
		if !cleanupTemporary {
			return
		}
		removed, removeErr := removeExactRegular(temporaryName, temporaryInfo, h.operations)
		var syncErr error
		if removed {
			durable, err := h.operations.syncParent(filepath.Dir(temporaryName))
			syncErr = err
			if syncErr == nil && !durable {
				syncErr = errors.New("lifecycle temporary cleanup directory sync reported no durability")
			}
		}
		resultErr = errors.Join(resultErr, removeErr, syncErr)
	}()
	namedInfo, namedErr := os.Lstat(temporaryName)
	if statErr != nil || namedErr != nil || !temporaryInfo.Mode().IsRegular() || namedInfo.Mode()&os.ModeSymlink != 0 || !namedInfo.Mode().IsRegular() || !os.SameFile(temporaryInfo, namedInfo) {
		return errors.Join(statErr, namedErr, h.operations.close(temporary))
	}
	if err := temporary.Chmod(0o600); err != nil {
		return errors.Join(err, h.operations.close(temporary))
	}
	if _, err := h.operations.writeTo(temporary, nextRaw); err != nil {
		return errors.Join(err, h.operations.close(temporary))
	}
	if err := h.operations.sync(temporary); err != nil {
		return errors.Join(err, h.operations.close(temporary))
	}
	if err := h.operations.close(temporary); err != nil {
		return err
	}
	current, raw, currentInfo, _, err = readState(h.state.Target, h.state.Operation)
	if err != nil || current.Owner.Token != h.state.Owner.Token || !bytes.Equal(raw, h.raw) || h.info != nil && !os.SameFile(h.info, currentInfo) {
		return errors.Join(ErrChanged, err)
	}
	renamed, renameErr := h.operations.rename(temporaryName, h.path)
	published := exactRegularPath(h.path, temporaryInfo)
	if !published {
		if renamed {
			mutation.Visible = true
			h.state = next
			h.raw = nextRaw
			h.info = temporaryInfo
		}
		if renameErr == nil {
			renameErr = errors.New("lifecycle transition reported success without publishing the expected inode")
		}
		return errors.Join(ErrChanged, renameErr)
	}
	cleanupTemporary = false
	h.state = next
	h.raw = nextRaw
	h.info = temporaryInfo
	mutation.Visible = true
	if renameErr != nil {
		return renameErr
	}
	durable, syncErr := h.operations.syncParent(filepath.Dir(h.path))
	mutation.Durable = durable
	if syncErr != nil {
		return syncErr
	}
	if !durable {
		return errors.New("lifecycle transition directory sync reported no durability")
	}
	if !h.RecoveryRequired() {
		return ErrChanged
	}
	return nil
}

// Remove deletes only the exact state still owned by the handle.
func (h *Handle) Remove() error {
	if h == nil {
		return nil
	}
	current, raw, currentInfo, _, err := readState(h.state.Target, h.state.Operation)
	if err != nil || current.Owner.Token != h.state.Owner.Token || !bytes.Equal(raw, h.raw) || h.info != nil && !os.SameFile(h.info, currentInfo) {
		return errors.Join(ErrChanged, err)
	}
	removed, removeErr := removeExactRegular(h.path, currentInfo, h.operations)
	mutation := Mutation{Visible: removed, RecoveryRequired: h.RecoveryRequired()}
	if !removed {
		if removeErr == nil {
			removeErr = ErrChanged
		}
		if mutation.RecoveryRequired {
			return mutationFailure(removeErr, mutation)
		}
		return removeErr
	}
	parent := filepath.Dir(h.path)
	durable, syncErr := h.operations.syncParent(parent)
	mutation.Durable = durable
	mutation.RecoveryRequired = h.RecoveryRequired()
	if !mutation.RecoveryRequired {
		h.path = ""
	}
	if syncErr == nil && !durable {
		syncErr = errors.New("lifecycle removal directory sync reported no durability")
	}
	if err := errors.Join(removeErr, syncErr); err != nil {
		return mutationFailure(err, mutation)
	}
	if mutation.RecoveryRequired {
		return mutationFailure(ErrChanged, mutation)
	}
	return nil
}

// Adopt reconstructs a handle only after the caller has independently proved
// owner death. It rechecks that proof at adoption time.
func Adopt(target string, operation Operation) (*Handle, error) {
	return adopt(target, operation, nil)
}

// AdoptExpected is Adopt with an exact owner and canonical-state digest. A
// changed or replaced lifecycle record is rejected before any cleanup begins.
func AdoptExpected(target string, operation Operation, expected RecoveryExpectation) (*Handle, error) {
	return adopt(target, operation, &expected)
}

func adopt(target string, operation Operation, expected *RecoveryExpectation) (*Handle, error) {
	observation, err := ObserveRecovery(target, operation)
	if err != nil {
		if expected != nil {
			return nil, errors.Join(ErrChanged, err)
		}
		return nil, err
	}
	if expected != nil {
		if !validRecoveryExpectation(*expected) || observation.Expectation != *expected {
			return nil, ErrChanged
		}
	}
	dead, err := ownerDead(observation.State.Owner)
	if err != nil || !dead {
		return nil, errors.Join(ErrOwnerLive, err)
	}
	approved := observation.Expectation
	return &Handle{
		path: Sidecar(target, operation), state: observation.State, raw: observation.StateRaw, info: observation.stateInfo,
		recoveryExpectation: &approved, recoveryPlanRaw: append([]byte(nil), observation.PlanRaw...), recoveryPlanPresent: observation.PlanPresent,
		recoveryStageID: append([]byte(nil), observation.stageIdentity...), recoveryStagePresent: observation.StagePresent,
	}, nil
}

// RecoveryPlanRaw returns the exact plan bytes approved during recovery
// observation. Recovery controllers decode this sealed copy instead of
// reopening plan-v1.json through a mutable pathname.
func (h *Handle) RecoveryPlanRaw() ([]byte, bool) {
	if h == nil || h.recoveryExpectation == nil || !h.recoveryPlanPresent {
		return nil, false
	}
	return append([]byte(nil), h.recoveryPlanRaw...), true
}

// RecoveryStageIdentity returns the descriptor-derived physical identity of
// the exact stage approved during recovery observation. It remains available
// after the lifecycle sidecar has been removed so a controller can bind final
// best-effort stage cleanup to the object that the sidecar authorized.
func (h *Handle) RecoveryStageIdentity() ([]byte, bool) {
	if h == nil || h.recoveryExpectation == nil || !h.recoveryStagePresent || len(h.recoveryStageID) == 0 {
		return nil, false
	}
	return append([]byte(nil), h.recoveryStageID...), true
}

// RevalidateRecoveryStage requires the current exact stage name to still
// identify the descriptor-derived physical directory approved at adoption.
// Unlike RevalidateRecovery, it intentionally remains usable after successful
// sidecar removal, immediately before best-effort cleanup of that stage.
func (h *Handle) RevalidateRecoveryStage() error {
	expected, present := h.RecoveryStageIdentity()
	if !present {
		return ErrChanged
	}
	stage, err := Stage(h.state)
	if err != nil {
		return errors.Join(ErrChanged, err)
	}
	_, current, err := readStableRealDirectory(stage)
	if err != nil || !bytes.Equal(expected, current) {
		return errors.Join(ErrChanged, err)
	}
	return nil
}

// RevalidateRecovery requires the adopted sidecar/stage/plan tuple to remain
// byte-for-byte and identity-for-identity equal to the recovery approval. It
// must run immediately before cleanup or rollback that relies on that tuple.
func (h *Handle) RevalidateRecovery() error {
	if h == nil || h.recoveryExpectation == nil || h.path == "" {
		return ErrChanged
	}
	observation, err := ObserveRecovery(h.state.Target, h.state.Operation)
	if err != nil {
		return errors.Join(ErrChanged, err)
	}
	if observation.Expectation != *h.recoveryExpectation || observation.State != h.state || !bytes.Equal(observation.StateRaw, h.raw) {
		return ErrChanged
	}
	return nil
}

func validRecoveryExpectation(expected RecoveryExpectation) bool {
	if len(expected.OwnerToken) != 64 || len(expected.StateSHA256) != 64 {
		return false
	}
	for _, value := range []string{expected.OwnerToken, expected.StateSHA256} {
		for _, character := range value {
			if character < '0' || character > '9' && character < 'a' || character > 'f' {
				return false
			}
		}
	}
	return true
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
