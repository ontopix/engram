package commands

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"

	"github.com/ontopix/engram/internal/changeset"
	"github.com/ontopix/engram/internal/checker"
	"github.com/ontopix/engram/internal/cli"
	"github.com/ontopix/engram/internal/gitraw"
	"github.com/ontopix/engram/internal/hookexec"
	"github.com/ontopix/engram/internal/hooks"
	"github.com/ontopix/engram/internal/managedread"
	"github.com/ontopix/engram/internal/managedwrite"
	"github.com/ontopix/engram/internal/replay"
	"github.com/ontopix/engram/internal/snapshot"
)

// acceptanceWriter is deliberately the complete one-shot mutation surface.
// Command handlers never receive or expose an editable transaction handle.
type acceptanceWriter interface {
	Commit(context.Context, managedwrite.Request) (*managedwrite.Result, error)
	CommitImage(context.Context, managedwrite.ImageRequest) (*managedwrite.Result, error)
}

// RegisterAcceptance installs commit and revert with the controller-owned
// hook-trust registry used by the hooks commands.
func RegisterAcceptance(app *cli.App) {
	root, err := os.UserConfigDir()
	if err != nil {
		registerAcceptanceFailure(app, err)
		return
	}
	RegisterAcceptanceAt(app, filepath.Join(root, "engram", "hook-trust-v1.json"))
}

// RegisterAcceptanceAt is the deterministic embedding and test variant.
func RegisterAcceptanceAt(app *cli.App, registryPath string) {
	registry, err := hooks.NewRegistry(registryPath)
	if err != nil {
		registerAcceptanceFailure(app, err)
		return
	}
	registerAcceptanceWith(app, managedwrite.New(hookexec.New(registry)))
}

func registerAcceptanceWith(app *cli.App, writer acceptanceWriter) {
	if app == nil {
		return
	}
	if app.Handlers == nil {
		app.Handlers = make(map[cli.CommandName]cli.Handler)
	}
	if writer == nil {
		registerAcceptanceFailure(app, errors.New("managed acceptance engine is unavailable"))
		return
	}
	app.Handlers[cli.CommandCommit] = cli.HandlerFunc(func(ctx context.Context, invocation *cli.Invocation) cli.Result {
		return runCommit(ctx, invocation, writer)
	})
	app.Handlers[cli.CommandRevert] = cli.HandlerFunc(func(ctx context.Context, invocation *cli.Invocation) cli.Result {
		return runRevert(ctx, invocation, writer)
	})
}

func registerAcceptanceFailure(app *cli.App, err error) {
	if app == nil {
		return
	}
	if app.Handlers == nil {
		app.Handlers = make(map[cli.CommandName]cli.Handler)
	}
	for _, name := range []cli.CommandName{cli.CommandCommit, cli.CommandRevert} {
		captured := err
		app.Handlers[name] = cli.HandlerFunc(func(context.Context, *cli.Invocation) cli.Result {
			return commandError(cli.ErrorCapability, fmt.Sprintf("configure managed acceptance: %v", captured))
		})
	}
}

type commitCommandResult struct {
	DryRun     bool               `json:"dry_run"`
	Created    bool               `json:"created"`
	Commit     *string            `json:"commit"`
	Changes    []changeset.Change `json:"changes"`
	Validation *checker.Result    `json:"validation"`
}

type revertCommandResult struct {
	DryRun     bool               `json:"dry_run"`
	Created    bool               `json:"created"`
	Commit     *string            `json:"commit"`
	Changes    []changeset.Change `json:"changes"`
	Validation *checker.Result    `json:"validation"`
	Reverted   string             `json:"reverted"`
	Conflicts  []string           `json:"conflicts"`
}

func runCommit(ctx context.Context, invocation *cli.Invocation, writer acceptanceWriter) cli.Result {
	if invocation == nil || len(invocation.Arguments) != 0 {
		return commandError(cli.ErrorInternal, "commit invocation has invalid arguments")
	}
	store, err := selectedStore(invocation)
	if err != nil {
		return failure(err, cli.ErrorRepository, "select store")
	}
	message, _ := invocation.Options.One("message")
	dryRun := invocation.Options.Has("dry-run")
	accepted, err := writer.Commit(ctx, managedwrite.Request{Store: store, Message: message, DryRun: dryRun})
	result := commitResult(accepted, dryRun)
	if validation, handled := acceptanceValidationOutcome(accepted, result); handled {
		return validation
	}
	if err == nil {
		return cli.Result{Outcome: cli.OutcomeOK, Value: result}
	}
	return managedWriteFailure(err, accepted, "accept staged candidate")
}

func runRevert(ctx context.Context, invocation *cli.Invocation, writer acceptanceWriter) cli.Result {
	if invocation == nil || len(invocation.Arguments) != 1 {
		return commandError(cli.ErrorInternal, "revert invocation has invalid arguments")
	}
	store, opened := openManaged(ctx, invocation)
	if opened != nil {
		return *opened
	}
	audit, err := store.AuditAccepted(ctx)
	if err != nil {
		return managedFailure(err, "audit accepted history for revert")
	}
	dryRun := invocation.Options.Has("dry-run")
	reverted := invocation.Arguments[0]
	if reverted == "HEAD" {
		reverted = audit.Tip
	}
	if audit.Validation.Status == checker.StatusIndeterminate {
		validation := audit.Validation
		return cli.Result{Outcome: cli.OutcomeIndeterminate, Value: revertCommandResult{
			DryRun: dryRun, Reverted: reverted, Conflicts: []string{}, Validation: cloneValidationForCommand(&validation),
		}}
	}
	if audit.Validation.Status != checker.StatusComplete {
		return commandError(cli.ErrorInternal, fmt.Sprintf("accepted audit returned unknown validation status %q", audit.Validation.Status))
	}
	if audit.Validation.HasErrors() {
		validation := audit.Validation
		return cli.Result{Outcome: cli.OutcomeIssues, Value: revertCommandResult{
			DryRun: dryRun, Reverted: reverted, Conflicts: []string{}, Validation: cloneValidationForCommand(&validation),
		}}
	}
	sourceID, source, parentID, err := selectRevertSource(store, audit, invocation.Arguments[0])
	if err != nil {
		return managedFailure(err, "select revert source")
	}
	current := audit.Snapshots[audit.Tip]
	parent := audit.Snapshots[parentID]
	if current == nil || current.Tree == nil || parent == nil || parent.Tree == nil || source == nil || source.Tree == nil {
		return commandError(cli.ErrorRepository, "revert source lineage has an unavailable snapshot")
	}
	inverse := replay.Apply(snapshotFiles(source), snapshotFiles(parent), snapshotFiles(current))
	if len(inverse.Conflicts) != 0 {
		return cli.Result{Outcome: cli.OutcomeIssues, Value: revertCommandResult{
			DryRun: dryRun, Conflicts: append([]string(nil), inverse.Conflicts...), Reverted: sourceID,
		}}
	}
	candidate, err := checkReplayFiles(inverse.Files)
	if err != nil {
		return failure(err, cli.ErrorRepository, "construct inverse candidate")
	}
	accepted, err := store.Accepted(ctx)
	if err != nil {
		return managedFailure(err, "capture current modes for revert")
	}
	if accepted.State.Commit == nil || *accepted.State.Commit != audit.Tip {
		return commandError(cli.ErrorConcurrency, "accepted state changed while constructing revert")
	}
	modes := candidateModes(candidate, accepted.Modes)
	message, supplied := invocation.Options.One("message")
	if !supplied {
		message = "Revert " + sourceID
	}
	request := managedwrite.ImageRequest{
		Store: store.Repository().Root, Message: message, DryRun: dryRun,
		Candidate: candidate, Modes: modes,
		RequireClean: true, RequireBase: true, ExpectedBase: stringPointerForCommand(audit.Tip),
	}
	written, writeErr := writer.CommitImage(ctx, request)
	result := revertResult(written, dryRun, sourceID)
	if validation, handled := acceptanceValidationOutcome(written, result); handled {
		return validation
	}
	if writeErr == nil {
		return cli.Result{Outcome: cli.OutcomeOK, Value: result}
	}
	return managedWriteFailure(writeErr, written, "accept inverse candidate")
}

func selectRevertSource(store *managedread.Store, audit *managedread.AcceptedAudit, value string) (string, *checker.Snapshot, string, error) {
	if store == nil || store.Repository() == nil || audit == nil || audit.Raw == nil {
		return "", nil, "", errors.New("accepted lineage is unavailable")
	}
	id := value
	if value == "HEAD" {
		id = audit.Tip
	} else if _, err := gitraw.ParseOID(store.Repository().Format, value); err != nil {
		return "", nil, "", &managedread.RevisionError{Value: value, Detail: "expected HEAD or one full lowercase object ID at repository width"}
	}
	for _, commit := range audit.Raw.Commits {
		if commit.ID.String() != id {
			continue
		}
		if commit.Commit == nil || len(commit.Commit.Parents) != 1 {
			return "", nil, "", &managedread.RevisionError{Value: value, Detail: "revert source must be a non-root, single-parent commit"}
		}
		return id, audit.Snapshots[id], commit.Commit.Parents[0].String(), nil
	}
	return "", nil, "", &managedread.RevisionError{Value: value, Detail: "object is not in the current accepted lineage"}
}

func snapshotFiles(value *checker.Snapshot) replay.Files {
	result := make(replay.Files)
	if value == nil || value.Tree == nil {
		return result
	}
	for name, file := range value.Tree.Files {
		result[name] = append([]byte(nil), file.Data...)
	}
	return result
}

func candidateModes(candidate *checker.Snapshot, current map[string]gitraw.TreeMode) map[string]gitraw.TreeMode {
	result := make(map[string]gitraw.TreeMode)
	if candidate == nil || candidate.Tree == nil {
		return result
	}
	for name := range candidate.Tree.Files {
		if current[name] == gitraw.ModeExecutable {
			result[name] = gitraw.ModeExecutable
		} else {
			result[name] = gitraw.ModeRegular
		}
	}
	return result
}

func commitResult(result *managedwrite.Result, dryRun bool) commitCommandResult {
	if result == nil {
		return commitCommandResult{DryRun: dryRun}
	}
	return commitCommandResult{
		DryRun: result.DryRun, Created: result.Created, Commit: cloneStringForCommand(result.Commit),
		Changes: cloneChangesForCommand(result.Changes), Validation: cloneValidationForCommand(result.Validation),
	}
}

func revertResult(result *managedwrite.Result, dryRun bool, source string) revertCommandResult {
	commit := commitResult(result, dryRun)
	return revertCommandResult{
		DryRun: commit.DryRun, Created: commit.Created, Commit: commit.Commit,
		Changes: commit.Changes, Validation: commit.Validation,
		Reverted: source, Conflicts: []string{},
	}
}

func acceptanceValidationOutcome(result *managedwrite.Result, value any) (cli.Result, bool) {
	if result == nil || result.Validation == nil {
		return cli.Result{}, false
	}
	switch result.Validation.Status {
	case checker.StatusIndeterminate:
		return cli.Result{Outcome: cli.OutcomeIndeterminate, Value: value}, true
	case checker.StatusComplete:
		if result.Validation.HasErrors() {
			return cli.Result{Outcome: cli.OutcomeIssues, Value: value}, true
		}
		return cli.Result{}, false
	default:
		return commandError(cli.ErrorInternal, fmt.Sprintf("managed writer returned unknown validation status %q", result.Validation.Status)), true
	}
}

func managedWriteFailure(err error, prospective *managedwrite.Result, action string) cli.Result {
	if err == nil {
		return commandError(cli.ErrorInternal, action+": unknown failure")
	}
	kind := cli.ErrorOperational
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		kind = cli.ErrorCancelled
	} else {
		switch managedwrite.KindOf(err) {
		case managedwrite.FailureUsage:
			kind = cli.ErrorUsage
		case managedwrite.FailureRepository:
			kind = cli.ErrorRepository
		case managedwrite.FailureCapability:
			kind = cli.ErrorCapability
		case managedwrite.FailureValidation:
			kind = cli.ErrorRepository
		case managedwrite.FailureTrust:
			kind = cli.ErrorTrust
		case managedwrite.FailureHook:
			kind = cli.ErrorHook
		case managedwrite.FailureGuard:
			kind = cli.ErrorIntegration
		case managedwrite.FailureConcurrency:
			kind = cli.ErrorConcurrency
		case managedwrite.FailureRecovery:
			kind = cli.ErrorConflict
		case managedwrite.FailureIO:
			kind = cli.ErrorIO
		}
	}
	result := cli.Result{Outcome: cli.OutcomeError, Error: &cli.ProtocolError{Kind: kind, Message: fmt.Sprintf("%s: %v", action, err)}}
	var typed *managedwrite.Error
	if errors.As(err, &typed) && (typed.Durable || typed.Accepted || typed.UnknownCAS || typed.RecoveryRequired || typed.Kind == managedwrite.FailureRecovery) {
		mutation := cli.NewMutationResult()
		mutation.Durable = typed.Durable || typed.Accepted
		mutation.RecoveryRequired = typed.RecoveryRequired
		if !typed.Durable {
			mutation.RecoveryRequired = (typed.Accepted || typed.UnknownCAS || typed.Kind == managedwrite.FailureRecovery) && typed.Phase != managedwrite.PhaseJournalRemoved
		}
		if typed.Accepted && prospective != nil && prospective.Ref != "" && typed.Commit != "" {
			after := typed.Commit
			mutation.LocalRefs = append(mutation.LocalRefs, cli.RefMutation{Ref: prospective.Ref, Before: cloneStringForCommand(prospective.Base), After: &after})
			mutation.Head = &cli.HeadMutation{
				Before: cli.MutationGitState{Ref: cloneStringForCommand(&prospective.Ref), Commit: cloneStringForCommand(prospective.Base)},
				After:  cli.MutationGitState{Ref: cloneStringForCommand(&prospective.Ref), Commit: cloneStringForCommand(&after)},
			}
		}
		mutation.CheckoutChanged = typed.CheckoutChanged
		result.Value = mutation
	}
	return result
}

func cloneChangesForCommand(value []changeset.Change) []changeset.Change {
	if value == nil {
		return nil
	}
	cloned := make([]changeset.Change, len(value))
	copy(cloned, value)
	return cloned
}

func cloneValidationForCommand(value *checker.Result) *checker.Result {
	if value == nil {
		return nil
	}
	cloned := *value
	if value.Findings != nil {
		cloned.Findings = make([]checker.Finding, len(value.Findings))
		copy(cloned.Findings, value.Findings)
	}
	return &cloned
}

func cloneStringForCommand(value *string) *string {
	if value == nil {
		return nil
	}
	return stringPointerForCommand(*value)
}

func stringPointerForCommand(value string) *string {
	copy := value
	return &copy
}

// replaySource turns a logical regular-file image back into the same bounded
// snapshot.Source model used by accepted-history audits.
type replaySource struct {
	children map[string][]snapshot.Entry
	files    map[string][]byte
}

func checkReplayFiles(files replay.Files) (*checker.Snapshot, error) {
	source := &replaySource{children: map[string][]snapshot.Entry{".": {}}, files: make(map[string][]byte, len(files))}
	directories := map[string]struct{}{".": {}}
	for name, data := range files {
		for directory := path.Dir(name); directory != "."; directory = path.Dir(directory) {
			directories[directory] = struct{}{}
		}
		source.files[name] = append([]byte(nil), data...)
	}
	for directory := range directories {
		if _, ok := source.children[directory]; !ok {
			source.children[directory] = []snapshot.Entry{}
		}
		if directory == "." {
			continue
		}
		parent := path.Dir(directory)
		source.children[parent] = append(source.children[parent], snapshot.Entry{Name: path.Base(directory), Kind: snapshot.KindDirectory})
	}
	for name := range files {
		directory := path.Dir(name)
		source.children[directory] = append(source.children[directory], snapshot.Entry{Name: path.Base(name), Kind: snapshot.KindRegular})
	}
	for directory := range source.children {
		sort.Slice(source.children[directory], func(i, j int) bool {
			return bytes.Compare([]byte(source.children[directory][i].Name), []byte(source.children[directory][j].Name)) < 0
		})
	}
	return checker.CheckSource(source)
}

func (s *replaySource) ReadDir(logical string) ([]snapshot.Entry, error) {
	entries, ok := s.children[logical]
	if !ok {
		return nil, os.ErrNotExist
	}
	return append([]snapshot.Entry(nil), entries...), nil
}

func (s *replaySource) ReadFile(logical string) ([]byte, error) {
	data, ok := s.files[logical]
	if !ok {
		return nil, os.ErrNotExist
	}
	return append([]byte(nil), data...), nil
}
