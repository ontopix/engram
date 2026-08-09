// Package initialize creates and recovers one managed store without exposing
// a partially accepted repository at the requested target.
package initialize

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/ontopix/engram/internal/bootstrap"
	"github.com/ontopix/engram/internal/changeset"
	"github.com/ontopix/engram/internal/checker"
	"github.com/ontopix/engram/internal/gitpresent"
	"github.com/ontopix/engram/internal/gitraw"
	"github.com/ontopix/engram/internal/guard"
	"github.com/ontopix/engram/internal/lifecycle"
	"github.com/ontopix/engram/internal/managedread"
	"github.com/ontopix/engram/internal/managedwrite"
	"github.com/ontopix/engram/internal/treeimage"
)

const initialMessage = "Initialize engram store"

type ErrorKind string

const (
	ErrorUsage       ErrorKind = "usage"
	ErrorCancelled   ErrorKind = "cancelled"
	ErrorCapability  ErrorKind = "capability"
	ErrorConflict    ErrorKind = "conflict"
	ErrorConcurrency ErrorKind = "concurrency"
	ErrorIntegration ErrorKind = "integration"
	ErrorRepository  ErrorKind = "repository"
	ErrorIO          ErrorKind = "io"
	ErrorRecovery    ErrorKind = "recovery"
)

type Error struct {
	Kind             ErrorKind
	Operation        string
	Durable          bool
	Commit           *string
	CheckoutChanged  bool
	RecoveryRequired bool
	Underlying       error
}

func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	message := string(e.Kind)
	if e.Operation != "" {
		message += ": " + e.Operation
	}
	if e.Underlying != nil {
		message += ": " + e.Underlying.Error()
	}
	return message
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Underlying
}

func KindOf(err error) ErrorKind {
	var typed *Error
	if errors.As(err, &typed) {
		return typed.Kind
	}
	return ""
}

type Identity struct {
	Name  string
	Email string
}

type Options struct {
	Schemas     []string
	DryRun      bool
	Identity    *Identity
	Environment []string
}

type Result struct {
	DryRun     bool                 `json:"dry_run"`
	Root       string               `json:"root"`
	Accepted   managedread.GitState `json:"accepted"`
	Files      []changeset.Change   `json:"files"`
	Launcher   guard.State          `json:"launcher"`
	Validation checker.Result       `json:"validation"`
}

// RecoveryResult reports only effects that the bounded initialization
// recovery can establish. Accepted is populated when publication had already
// completed and the exact accepted state was re-audited.
type RecoveryResult struct {
	Needed    bool                  `json:"needed"`
	Performed bool                  `json:"performed"`
	Durable   bool                  `json:"durable"`
	Accepted  *managedread.GitState `json:"accepted"`
}

type Writer interface {
	CommitImage(context.Context, managedwrite.ImageRequest) (*managedwrite.Result, error)
}

type Phase string

const (
	PhaseStaged              Phase = "staged"
	PhaseAccepted            Phase = "accepted"
	PhaseCleanupRequired     Phase = "cleanup-required"
	PhaseFilesPublished      Phase = "files-published"
	PhaseRepositoryPublished Phase = "repository-published"
	PhaseCleaned             Phase = "cleaned"
)

// Initializer owns fault seams and the managed writer used only inside the
// unpublished staging repository.
type Initializer struct {
	Writer Writer
	Fault  func(Phase) error
}

func New(writer Writer) *Initializer { return &Initializer{Writer: writer} }

func (i *Initializer) checkpoint(phase Phase) error {
	if i == nil || i.Fault == nil {
		return nil
	}
	return i.Fault(phase)
}

// Recover adopts one exact dead-owner lifecycle and either discards its
// unpublished private store, rolls back its known additions, or verifies an
// already-published repository. It never scans siblings, runs hooks, uses the
// network, or moves an accepted ref.
func Recover(ctx context.Context, target string) (RecoveryResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return RecoveryResult{Needed: true}, typed(ErrorCancelled, "recover initialization", err)
	}
	canonical, err := canonicalTarget(target)
	if err != nil {
		return RecoveryResult{Needed: true}, typed(ErrorUsage, "resolve initialization recovery target", err)
	}
	handle, err := lifecycle.Adopt(canonical, lifecycle.Initialization)
	if err != nil {
		kind := ErrorRecovery
		if errors.Is(err, lifecycle.ErrOwnerLive) || errors.Is(err, lifecycle.ErrChanged) {
			kind = ErrorConcurrency
		}
		return RecoveryResult{Needed: true}, typed(kind, "adopt initialization lifecycle", err)
	}
	stage, err := lifecycle.Stage(handle.State())
	if err != nil {
		return RecoveryResult{Needed: true}, typed(ErrorRecovery, "derive initialization recovery staging", err)
	}
	if handle.State().Phase == lifecycle.Running {
		return finishRecovery(stage, handle, nil, false)
	}
	if handle.State().Phase != lifecycle.CleanupRequired {
		return RecoveryResult{Needed: true}, typed(ErrorRecovery, "recognize initialization lifecycle", errors.New("unsupported initialization recovery phase"))
	}
	record, err := readPublicationPlan(stage, handle.State())
	if err != nil {
		return RecoveryResult{Needed: true}, typed(ErrorRecovery, "read initialization recovery plan", err)
	}
	if err := ctx.Err(); err != nil {
		return RecoveryResult{Needed: true}, typed(ErrorCancelled, "recover initialization", err)
	}

	gitPath := filepath.Join(canonical, ".git")
	if _, err := os.Lstat(gitPath); err == nil {
		accepted, verifyErr := acceptedPublishedState(ctx, canonical, record.Commit)
		if verifyErr != nil {
			return RecoveryResult{Needed: true}, typed(ErrorRecovery, "verify recovered initialization", verifyErr)
		}
		return finishRecovery(stage, handle, accepted, true)
	} else if !errors.Is(err, os.ErrNotExist) {
		return RecoveryResult{Needed: true}, typed(ErrorRecovery, "inspect recovered initialization", err)
	}

	if !record.RootExists {
		if _, err := os.Lstat(canonical); err == nil {
			return RecoveryResult{Needed: true}, typed(ErrorConflict, "recover initialization publication", errors.New("target exists without the recorded managed repository"))
		} else if !errors.Is(err, os.ErrNotExist) {
			return RecoveryResult{Needed: true}, typed(ErrorRecovery, "inspect initialization target", err)
		}
		return finishRecovery(stage, handle, nil, false)
	}

	info, err := os.Lstat(canonical)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return RecoveryResult{Needed: true}, typed(ErrorConflict, "recover initialization target", errors.Join(err, errors.New("existing initialization target is no longer one real directory")))
	}
	// Before removing a single published addition, prove that the private
	// accepted store and the recorded commit are still exact.
	if _, err := acceptedPublishedState(ctx, filepath.Join(stage, "store"), record.Commit); err != nil {
		return RecoveryResult{Needed: true}, typed(ErrorRecovery, "verify private initialization store", err)
	}
	changed, err := rollbackPublishedAdditions(ctx, canonical, record)
	if err != nil {
		return RecoveryResult{Needed: true, Durable: changed}, &Error{Kind: ErrorRecovery, Operation: "roll back initialization publication", Durable: changed, CheckoutChanged: changed, RecoveryRequired: true, Underlying: err}
	}
	return finishRecovery(stage, handle, nil, changed)
}

// Run plans, accepts, and publishes one initialization. Dry-run stops after
// the portable empty-base transition and performs no write or Git operation.
func (i *Initializer) Run(ctx context.Context, target string, options Options) (result Result, resultErr error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return Result{}, typed(ErrorCancelled, "initialize", err)
	}
	plan, err := bootstrap.Build(ctx, target, options.Schemas)
	if err != nil {
		return Result{}, classify("plan initialization", err)
	}
	result = resultFromPlan(plan, options.DryRun)
	if options.DryRun || plan.Validation.Status != checker.StatusComplete || plan.Validation.HasErrors() {
		return result, nil
	}
	var original treeimage.Image
	if plan.RootExists {
		original, err = treeimage.Capture(plan.Root, true)
		if err != nil {
			return result, classify("capture initialization target", err)
		}
		verifiedPlan, rebuildErr := bootstrap.Build(ctx, plan.Root, options.Schemas)
		current, captureErr := treeimage.Capture(plan.Root, true)
		if rebuildErr != nil || captureErr != nil || !treeimage.Equal(original, current) || !samePlan(plan, verifiedPlan) {
			return result, typed(ErrorConcurrency, "stabilize initialization plan", errors.Join(rebuildErr, captureErr, errors.New("target changed while initialization was planned")))
		}
	}
	if i == nil || i.Writer == nil {
		return Result{}, typed(ErrorCapability, "select managed writer", errors.New("managed writer is unavailable"))
	}
	identity := Identity{}
	if options.Identity != nil {
		identity = *options.Identity
	} else {
		identity, err = configuredIdentity(ctx, options.Environment)
		if err != nil {
			return result, classify("read Git author identity", err)
		}
	}
	if !validIdentity(identity) {
		return result, typed(ErrorUsage, "validate Git author identity", errors.New("Git author name or email is not representable"))
	}

	handle, err := lifecycle.Begin(plan.Root, lifecycle.Initialization)
	if err != nil {
		return result, classify("begin initialization lifecycle", err)
	}
	stage, err := lifecycle.Stage(handle.State())
	if err != nil {
		_ = handle.Remove()
		return result, typed(ErrorRecovery, "derive initialization staging", err)
	}
	cleanupRunning := true
	defer func() {
		if !cleanupRunning {
			return
		}
		cleanupErr := cleanupPrivateStage(stage)
		stateErr := handle.Remove()
		if cleanupErr != nil || stateErr != nil {
			resultErr = errors.Join(resultErr, &Error{Kind: ErrorRecovery, Operation: "clean pre-publication initialization", RecoveryRequired: true, Underlying: errors.Join(cleanupErr, stateErr)})
		}
	}()

	if err := os.Mkdir(stage, 0o700); err != nil {
		return result, classify("create initialization staging", err)
	}
	stageStore := filepath.Join(stage, "store")
	image, err := treeimage.FromSnapshot(plan.Candidate.Tree, plan.Modes)
	if err != nil {
		return result, typed(ErrorRepository, "construct initialization image", err)
	}
	if err := treeimage.Materialize(stageStore, image, false); err != nil {
		return result, classify("materialize initialization staging", err)
	}
	if err := initializeGit(ctx, stageStore, identity, options.Environment); err != nil {
		return result, err
	}
	if err := gitpresent.Configure(ctx, stageStore); err != nil {
		return result, typed(ErrorIntegration, "configure initialization presentation", err)
	}
	repository, err := gitraw.Discover(ctx, stageStore)
	if err != nil {
		return result, typed(ErrorRepository, "discover initialization staging", err)
	}
	launcher, err := guard.Install(ctx, repository)
	if err != nil {
		return result, typed(ErrorIntegration, "install initialization guard", err)
	}
	if err := stageCandidate(ctx, stageStore, options.Environment); err != nil {
		return result, err
	}
	if err := i.checkpoint(PhaseStaged); err != nil {
		return result, typed(ErrorIO, "fault after initialization staging", err)
	}
	written, err := i.Writer.CommitImage(ctx, managedwrite.ImageRequest{
		Store: stageStore, Message: initialMessage, Candidate: plan.Candidate, Modes: plan.Modes,
		RequireBase: true, ExpectedBase: nil,
	})
	if err != nil {
		if written != nil && written.Validation != nil {
			result.Validation = cloneValidation(*written.Validation)
			result.Files = cloneChanges(written.Changes)
		}
		return result, classifyManagedWrite("accept initialization candidate", err)
	}
	if written == nil || !written.Created || written.Commit == nil {
		return result, typed(ErrorRepository, "accept initialization candidate", errors.New("managed writer did not create an initialization commit"))
	}
	if written.Validation == nil {
		return result, typed(ErrorRepository, "accept initialization candidate", errors.New("managed writer omitted definitive validation"))
	}
	result.Validation = cloneValidation(*written.Validation)
	result.Files = cloneChanges(written.Changes)
	result.Accepted.Commit = cloneString(written.Commit)
	result.Launcher = launcher
	if err := i.checkpoint(PhaseAccepted); err != nil {
		return result, typed(ErrorIO, "fault after initialization acceptance", err)
	}

	record, err := makePublicationPlan(plan, *written.Commit)
	if err != nil {
		return result, typed(ErrorRepository, "construct initialization publication plan", err)
	}
	if err := writePublicationPlan(stage, record); err != nil {
		return result, typed(ErrorIO, "record initialization publication plan", err)
	}
	if plan.RootExists {
		current, captureErr := treeimage.Capture(plan.Root, true)
		if captureErr != nil || !treeimage.Equal(original, current) {
			return result, typed(ErrorConcurrency, "recheck initialization target", errors.Join(captureErr, errors.New("target changed after planning")))
		}
	}
	if err := handle.RequireCleanup(); err != nil {
		return result, &Error{Kind: ErrorRecovery, Operation: "advance initialization lifecycle", Commit: cloneString(written.Commit), RecoveryRequired: true, Underlying: err}
	}
	cleanupRunning = false
	if err := i.checkpoint(PhaseCleanupRequired); err != nil {
		return result, recoveryError("fault before initialization publication", *written.Commit, false, err)
	}

	checkoutChanged := false
	if plan.RootExists {
		if err := publishIntoExisting(ctx, plan, record); err != nil {
			return result, recoveryError("publish initialization files", *written.Commit, checkoutChanged || publicationMayHaveStarted(err), err)
		}
		checkoutChanged = len(record.Files) != 0
		if err := i.checkpoint(PhaseFilesPublished); err != nil {
			return result, recoveryError("fault after initialization files", *written.Commit, checkoutChanged, err)
		}
		if err := publishGitDirectory(stageStore, plan.Root); err != nil {
			return result, recoveryError("publish initialization repository", *written.Commit, checkoutChanged, err)
		}
		checkoutChanged = true
	} else {
		if _, err := os.Lstat(plan.Root); err == nil {
			return result, recoveryError("publish initialization target", *written.Commit, false, errors.New("target appeared concurrently"))
		} else if !errors.Is(err, os.ErrNotExist) {
			return result, recoveryError("inspect initialization target", *written.Commit, false, err)
		}
		if err := os.Rename(stageStore, plan.Root); err != nil {
			return result, recoveryError("publish initialization target", *written.Commit, false, err)
		}
		checkoutChanged = true
		if err := syncDirectory(filepath.Dir(plan.Root)); err != nil {
			return result, recoveryError("durably publish initialization target", *written.Commit, true, err)
		}
	}
	if err := i.checkpoint(PhaseRepositoryPublished); err != nil {
		return result, recoveryError("fault after initialization publication", *written.Commit, checkoutChanged, err)
	}
	if err := verifyPublished(ctx, plan.Root, *written.Commit); err != nil {
		return result, recoveryError("verify published initialization", *written.Commit, checkoutChanged, err)
	}
	if err := cleanupPrivateStage(stage); err != nil {
		return result, recoveryError("clean initialization staging", *written.Commit, checkoutChanged, err)
	}
	if err := handle.Remove(); err != nil {
		return result, recoveryError("clean initialization lifecycle", *written.Commit, checkoutChanged, err)
	}
	if err := i.checkpoint(PhaseCleaned); err != nil {
		return result, &Error{Kind: ErrorIO, Operation: "fault after initialization cleanup", Durable: true, Commit: cloneString(written.Commit), CheckoutChanged: checkoutChanged, Underlying: err}
	}
	return result, nil
}

func canonicalTarget(target string) (string, error) {
	if target == "" || !utf8.ValidString(target) {
		return "", errors.New("initialization recovery target is empty or not UTF-8")
	}
	absolute, err := filepath.Abs(target)
	if err != nil {
		return "", err
	}
	absolute = filepath.Clean(absolute)
	parent, err := filepath.EvalSymlinks(filepath.Dir(absolute))
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Clean(parent), filepath.Base(absolute)), nil
}

func acceptedPublishedState(ctx context.Context, root, commit string) (*managedread.GitState, error) {
	if err := verifyPublished(ctx, root, commit); err != nil {
		return nil, err
	}
	ref := "refs/heads/main"
	return &managedread.GitState{Ref: &ref, Commit: cloneString(&commit)}, nil
}

func finishRecovery(stage string, handle *lifecycle.Handle, accepted *managedread.GitState, alreadyDurable bool) (RecoveryResult, error) {
	if err := cleanupPrivateStage(stage); err != nil {
		return RecoveryResult{Needed: true, Durable: alreadyDurable, Accepted: accepted}, &Error{
			Kind: ErrorRecovery, Operation: "clean initialization recovery staging", Durable: alreadyDurable,
			RecoveryRequired: true, Underlying: err,
		}
	}
	if err := handle.Remove(); err != nil {
		// A concurrent successful recovery is idempotent. Only accept that case
		// after the exact sidecar is observed absent.
		if _, statErr := os.Lstat(lifecycle.Sidecar(handle.State().Target, lifecycle.Initialization)); errors.Is(statErr, os.ErrNotExist) {
			return RecoveryResult{Needed: true, Performed: true, Durable: true, Accepted: accepted}, nil
		}
		return RecoveryResult{Needed: true, Durable: true, Accepted: accepted}, &Error{
			Kind: ErrorRecovery, Operation: "clean initialization recovery lifecycle", Durable: true,
			RecoveryRequired: true, Underlying: err,
		}
	}
	return RecoveryResult{Needed: true, Performed: true, Durable: true, Accepted: accepted}, nil
}

func rollbackPublishedAdditions(ctx context.Context, rootPath string, record publicationPlan) (bool, error) {
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return false, err
	}
	defer root.Close()
	present := make([]string, 0, len(record.Files))
	for _, planned := range record.Files {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		logical := filepath.FromSlash(planned.Path)
		info, err := root.Lstat(logical)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return false, errors.Join(err, fmt.Errorf("published path %s no longer has its owned shape", planned.Path))
		}
		file, err := root.Open(logical)
		if err != nil {
			return false, err
		}
		data, readErr := io.ReadAll(io.LimitReader(file, int64(len(planned.Data))+1))
		opened, statErr := file.Stat()
		closeErr := file.Close()
		after, afterErr := root.Lstat(logical)
		if readErr != nil || statErr != nil || closeErr != nil || afterErr != nil || !os.SameFile(info, opened) || !os.SameFile(opened, after) || !bytes.Equal(data, planned.Data) {
			return false, errors.Join(readErr, statErr, closeErr, afterErr, fmt.Errorf("published path %s differs from its recovery record", planned.Path))
		}
		present = append(present, planned.Path)
	}
	changed := false
	for index := len(present) - 1; index >= 0; index-- {
		if err := ctx.Err(); err != nil {
			return changed, err
		}
		if err := root.Remove(filepath.FromSlash(present[index])); err != nil {
			return changed, err
		}
		changed = true
	}
	for index := len(record.Directories) - 1; index >= 0; index-- {
		logical := filepath.FromSlash(record.Directories[index])
		info, err := root.Lstat(logical)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return changed, errors.Join(err, fmt.Errorf("publication directory %s changed shape", record.Directories[index]))
		}
		directory, err := root.Open(logical)
		if err != nil {
			return changed, err
		}
		entries, readErr := directory.ReadDir(1)
		closeErr := directory.Close()
		if readErr != nil && !errors.Is(readErr, io.EOF) || closeErr != nil {
			return changed, errors.Join(readErr, closeErr)
		}
		if len(entries) != 0 {
			continue // Preserve a directory now containing unrelated bytes.
		}
		if err := root.Remove(logical); err != nil && !errors.Is(err, os.ErrNotExist) {
			return changed, err
		}
		changed = true
	}
	if changed {
		if err := syncDirectory(rootPath); err != nil {
			return true, err
		}
	}
	return changed, nil
}

func resultFromPlan(plan *bootstrap.Plan, dryRun bool) Result {
	ref := "refs/heads/main"
	return Result{
		DryRun: dryRun, Root: plan.Root,
		Accepted: managedread.GitState{Ref: &ref}, Files: cloneChanges(plan.Changes),
		Launcher: guard.Planned, Validation: cloneValidation(plan.Validation),
	}
}

func samePlan(left, right *bootstrap.Plan) bool {
	return left != nil && right != nil && left.Root == right.Root && left.RootExists == right.RootExists &&
		reflect.DeepEqual(left.Files, right.Files) && reflect.DeepEqual(left.Candidate, right.Candidate) &&
		reflect.DeepEqual(left.Modes, right.Modes) && reflect.DeepEqual(left.Changes, right.Changes) &&
		reflect.DeepEqual(left.Validation, right.Validation)
}

type publicationFile struct {
	Path string `json:"path"`
	Data []byte `json:"data"`
}

type publicationPlan struct {
	Version     int               `json:"version"`
	Target      string            `json:"target"`
	RootExists  bool              `json:"root_exists"`
	Commit      string            `json:"commit"`
	Files       []publicationFile `json:"files"`
	Directories []string          `json:"directories"`
}

func makePublicationPlan(plan *bootstrap.Plan, commit string) (publicationPlan, error) {
	record := publicationPlan{Version: 1, Target: plan.Root, RootExists: plan.RootExists, Commit: commit, Files: []publicationFile{}, Directories: []string{}}
	paths := make([]string, 0, len(plan.Files))
	for name := range plan.Files {
		paths = append(paths, name)
	}
	sort.Strings(paths)
	directories := make(map[string]struct{})
	for _, name := range paths {
		record.Files = append(record.Files, publicationFile{Path: name, Data: append([]byte(nil), plan.Files[name]...)})
		if !plan.RootExists {
			continue
		}
		for directory := path.Dir(name); directory != "."; directory = path.Dir(directory) {
			host := filepath.Join(plan.Root, filepath.FromSlash(directory))
			info, err := os.Lstat(host)
			if errors.Is(err, os.ErrNotExist) {
				directories[directory] = struct{}{}
				continue
			}
			if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
				return publicationPlan{}, errors.Join(err, fmt.Errorf("publication parent %s is not a real directory", directory))
			}
		}
	}
	for directory := range directories {
		record.Directories = append(record.Directories, directory)
	}
	sort.Slice(record.Directories, func(left, right int) bool {
		leftDepth, rightDepth := strings.Count(record.Directories[left], "/"), strings.Count(record.Directories[right], "/")
		if leftDepth != rightDepth {
			return leftDepth < rightDepth
		}
		return record.Directories[left] < record.Directories[right]
	})
	return record, nil
}

func writePublicationPlan(stage string, record publicationPlan) error {
	data, err := encodePlan(record)
	if err != nil {
		return err
	}
	name := filepath.Join(stage, "plan-v1.json")
	file, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return syncDirectory(stage)
}

func encodePlan(record publicationPlan) ([]byte, error) {
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(record); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func readPublicationPlan(stage string, state lifecycle.State) (publicationPlan, error) {
	name := filepath.Join(stage, "plan-v1.json")
	info, err := os.Lstat(name)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return publicationPlan{}, errors.Join(err, errors.New("initialization publication plan is unavailable"))
	}
	file, err := os.Open(name)
	if err != nil {
		return publicationPlan{}, err
	}
	data, readErr := io.ReadAll(io.LimitReader(file, (16<<20)+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil {
		return publicationPlan{}, errors.Join(readErr, closeErr)
	}
	if len(data) > 16<<20 {
		return publicationPlan{}, errors.New("initialization publication plan is too large")
	}
	var record publicationPlan
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return publicationPlan{}, errors.Join(err, errors.New("malformed initialization publication plan"))
	}
	canonical, err := encodePlan(record)
	if err != nil || !bytes.Equal(data, canonical) || validatePlan(record, state) != nil {
		return publicationPlan{}, errors.Join(err, errors.New("inconsistent initialization publication plan"))
	}
	return record, nil
}

func validatePlan(record publicationPlan, state lifecycle.State) error {
	if record.Version != 1 || record.Target != state.Target || record.Files == nil || record.Directories == nil {
		return errors.New("invalid plan header")
	}
	if _, err := gitraw.ParseOID(gitraw.SHA1, record.Commit); err != nil {
		if _, sha256Err := gitraw.ParseOID(gitraw.SHA256, record.Commit); sha256Err != nil {
			return errors.New("invalid plan commit")
		}
	}
	previous := ""
	files := make(map[string]struct{}, len(record.Files))
	for _, file := range record.Files {
		if !validLogicalPath(file.Path) || previous != "" && previous >= file.Path {
			return errors.New("invalid or unordered plan files")
		}
		files[file.Path] = struct{}{}
		previous = file.Path
	}
	previous = ""
	previousDepth := -1
	directories := make(map[string]struct{}, len(record.Directories))
	for _, directory := range record.Directories {
		depth := strings.Count(directory, "/")
		if !validLogicalPath(directory) || depth < previousDepth || depth == previousDepth && previous >= directory {
			return errors.New("invalid or unordered plan directories")
		}
		if _, collision := files[directory]; collision {
			return errors.New("plan file and directory collide")
		}
		directories[directory] = struct{}{}
		previous, previousDepth = directory, depth
	}
	for name := range files {
		for ancestor := path.Dir(name); ancestor != "."; ancestor = path.Dir(ancestor) {
			if _, collision := files[ancestor]; collision {
				return errors.New("plan file is an ancestor of another file")
			}
		}
	}
	if !record.RootExists && len(directories) != 0 {
		return errors.New("absent-target publication records directories")
	}
	return nil
}

func publishIntoExisting(ctx context.Context, plan *bootstrap.Plan, record publicationPlan) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := os.Lstat(filepath.Join(plan.Root, ".git")); err == nil {
		return errors.New("target acquired Git administration concurrently")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	root, err := os.OpenRoot(plan.Root)
	if err != nil {
		return err
	}
	defer root.Close()
	for _, directory := range record.Directories {
		if err := root.Mkdir(filepath.FromSlash(directory), 0o755); err != nil && !errors.Is(err, os.ErrExist) {
			return &publicationError{started: true, err: err}
		}
		info, err := root.Lstat(filepath.FromSlash(directory))
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return &publicationError{started: true, err: errors.Join(err, errors.New("publication directory changed"))}
		}
	}
	for _, planned := range record.Files {
		if err := ctx.Err(); err != nil {
			return &publicationError{started: true, err: err}
		}
		file, err := root.OpenFile(filepath.FromSlash(planned.Path), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			return &publicationError{started: true, err: err}
		}
		if _, err := file.Write(planned.Data); err != nil {
			_ = file.Close()
			return &publicationError{started: true, err: err}
		}
		if err := file.Sync(); err != nil {
			_ = file.Close()
			return &publicationError{started: true, err: err}
		}
		if err := file.Close(); err != nil {
			return &publicationError{started: true, err: err}
		}
	}
	return syncDirectory(plan.Root)
}

type publicationError struct {
	started bool
	err     error
}

func (e *publicationError) Error() string { return e.err.Error() }
func (e *publicationError) Unwrap() error { return e.err }

func publicationMayHaveStarted(err error) bool {
	var published *publicationError
	return errors.As(err, &published) && published.started
}

func publishGitDirectory(stageStore, target string) error {
	source := filepath.Join(stageStore, ".git")
	destination := filepath.Join(target, ".git")
	if _, err := os.Lstat(destination); err == nil {
		return errors.New("target Git administration appeared concurrently")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(source, destination); err != nil {
		return err
	}
	return syncDirectory(target)
}

func verifyPublished(ctx context.Context, root, commit string) error {
	store, err := managedread.Open(ctx, root)
	if err != nil {
		return err
	}
	if store.Repository().HeadRef != "refs/heads/main" || store.Repository().Head == nil || store.Repository().Head.String() != commit {
		return errors.New("published initialization names a different accepted state")
	}
	audit, err := store.AuditAccepted(ctx)
	if err != nil || audit.Tip != commit || audit.Validation.Status != checker.StatusComplete || audit.Validation.HasErrors() {
		return errors.Join(err, errors.New("published initialization audit failed"))
	}
	state, err := guard.Inspect(ctx, store.Repository())
	if err != nil || state != guard.Unchanged {
		return errors.Join(err, errors.New("published initialization guard differs"))
	}
	return nil
}

func initializeGit(ctx context.Context, root string, identity Identity, environment []string) error {
	if err := runGit(ctx, root, environment, true, "init", "--initial-branch=main"); err != nil {
		return typed(ErrorCapability, "initialize Git repository", err)
	}
	for _, pair := range [][2]string{{"user.name", identity.Name}, {"user.email", identity.Email}, {"commit.gpgsign", "false"}} {
		if err := runGit(ctx, root, environment, true, "config", "--local", pair[0], pair[1]); err != nil {
			return typed(ErrorRepository, "configure initialization repository", err)
		}
	}
	return nil
}

func stageCandidate(ctx context.Context, root string, environment []string) error {
	if err := runGit(ctx, root, environment, true, "add", "--all", "--"); err != nil {
		return typed(ErrorRepository, "stage initialization candidate", err)
	}
	return nil
}

func configuredIdentity(ctx context.Context, environment []string) (Identity, error) {
	read := func(key string) (string, error) {
		git, err := exec.LookPath("git")
		if err != nil {
			return "", err
		}
		command := exec.CommandContext(ctx, git, "--no-pager", "config", "--get", key)
		command.Env = gitEnvironment(environment, false)
		output, err := command.Output()
		if err != nil {
			return "", err
		}
		return strings.TrimSuffix(string(output), "\n"), nil
	}
	name, err := read("user.name")
	if err != nil {
		return Identity{}, err
	}
	email, err := read("user.email")
	if err != nil {
		return Identity{}, err
	}
	return Identity{Name: name, Email: email}, nil
}

func runGit(ctx context.Context, root string, environment []string, isolateGlobal bool, arguments ...string) error {
	git, err := exec.LookPath("git")
	if err != nil {
		return err
	}
	global := []string{"--no-pager", "--no-optional-locks", "--no-replace-objects", "-c", "core.hooksPath=" + os.DevNull, "-C", root}
	command := exec.CommandContext(ctx, git, append(global, arguments...)...)
	command.Env = gitEnvironment(environment, isolateGlobal)
	var stderr bytes.Buffer
	command.Stdout = io.Discard
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("git %s: %w: %s", arguments[0], err, bounded(stderr.String()))
	}
	return nil
}

func gitEnvironment(environment []string, isolateGlobal bool) []string {
	if environment == nil {
		environment = os.Environ()
	}
	result := make([]string, 0, len(environment)+8)
	for _, item := range environment {
		name, _, _ := strings.Cut(item, "=")
		upper := strings.ToUpper(name)
		if strings.HasPrefix(upper, "GIT_") || strings.HasPrefix(upper, "ENGRAM_") || upper == "LC_ALL" {
			continue
		}
		result = append(result, item)
	}
	result = append(result, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_COUNT=0", "GIT_NO_LAZY_FETCH=1", "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0", "GIT_PAGER=cat", "LC_ALL=C")
	if isolateGlobal {
		result = append(result, "GIT_CONFIG_GLOBAL="+os.DevNull)
	}
	return result
}

func validIdentity(identity Identity) bool {
	return validIdentityPart(identity.Name, false) && validIdentityPart(identity.Email, true)
}

func validIdentityPart(value string, email bool) bool {
	if value == "" || !utf8.ValidString(value) || strings.ContainsAny(value, "\x00\r\n<>") {
		return false
	}
	return !email || strings.TrimSpace(value) == value
}

func cleanupPrivateStage(stage string) error {
	info, err := os.Lstat(stage)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.Join(err, errors.New("initialization staging is not one real directory"))
	}
	if err := os.RemoveAll(stage); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(stage))
}

func cloneValidation(value checker.Result) checker.Result {
	value.Findings = append([]checker.Finding{}, value.Findings...)
	return value
}

func cloneChanges(value []changeset.Change) []changeset.Change {
	if value == nil {
		return nil
	}
	result := make([]changeset.Change, len(value))
	copy(result, value)
	return result
}

func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
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

func bounded(value string) string {
	if len(value) > 16<<10 {
		value = value[:16<<10]
	}
	return strings.TrimSpace(value)
}

func recoveryError(operation, commit string, checkout bool, err error) error {
	return &Error{Kind: ErrorRecovery, Operation: operation, Durable: true, Commit: &commit, CheckoutChanged: checkout, RecoveryRequired: true, Underlying: err}
}

func typed(kind ErrorKind, operation string, err error) error {
	if err == nil {
		err = errors.New("unknown initialization failure")
	}
	return &Error{Kind: kind, Operation: operation, Underlying: err}
}

func classify(operation string, err error) error {
	if err == nil {
		return nil
	}
	var typedError *Error
	if errors.As(err, &typedError) {
		return err
	}
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return typed(ErrorCancelled, operation, err)
	case errors.Is(err, bootstrap.ErrTarget):
		return typed(ErrorUsage, operation, err)
	case errors.Is(err, bootstrap.ErrConflict), errors.Is(err, lifecycle.ErrExists):
		return typed(ErrorConflict, operation, err)
	case errors.Is(err, lifecycle.ErrChanged):
		return typed(ErrorConcurrency, operation, err)
	case errors.Is(err, os.ErrPermission):
		return typed(ErrorIO, operation, err)
	default:
		return typed(ErrorIO, operation, err)
	}
}

func classifyManagedWrite(operation string, err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return typed(ErrorCancelled, operation, err)
	}
	kind := ErrorRepository
	switch managedwrite.KindOf(err) {
	case managedwrite.FailureUsage:
		kind = ErrorUsage
	case managedwrite.FailureCapability:
		kind = ErrorCapability
	case managedwrite.FailureTrust, managedwrite.FailureHook, managedwrite.FailureGuard:
		kind = ErrorIntegration
	case managedwrite.FailureConcurrency:
		kind = ErrorConcurrency
	case managedwrite.FailureRecovery:
		kind = ErrorRecovery
	case managedwrite.FailureIO:
		kind = ErrorIO
	}
	return typed(kind, operation, err)
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
