package commands

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ontopix/engram/internal/checker"
	"github.com/ontopix/engram/internal/cli"
	"github.com/ontopix/engram/internal/hookexec"
	"github.com/ontopix/engram/internal/hooks"
	"github.com/ontopix/engram/internal/managedread"
	"github.com/ontopix/engram/internal/managedwrite"
	"github.com/ontopix/engram/internal/pullflow"
)

// RegisterPull installs pull and synchronization-aware guards. It is safe to
// call repeatedly. Applications register it after managed reads and managed
// acceptance so the wrappers retain those concrete handlers.
func RegisterPull(app *cli.App) {
	root, err := os.UserConfigDir()
	if err != nil {
		registerPullFailure(app, err)
		return
	}
	RegisterPullAt(app, filepath.Join(root, "engram", "hook-trust-v1.json"))
}

// RegisterPullAt is the deterministic embedding/test variant.
func RegisterPullAt(app *cli.App, registryPath string) {
	registry, err := hooks.NewRegistry(registryPath)
	if err != nil {
		registerPullFailure(app, err)
		return
	}
	registerPullWith(app, pullflow.New(managedwrite.New(hookexec.New(registry))))
}

func registerPullWith(app *cli.App, puller *pullflow.Puller) {
	if app == nil {
		return
	}
	if app.Handlers == nil {
		app.Handlers = make(map[cli.CommandName]cli.Handler)
	}
	if puller == nil {
		registerPullFailure(app, errors.New("pull controller is unavailable"))
		return
	}
	app.Handlers[cli.CommandPull] = cli.HandlerFunc(func(ctx context.Context, invocation *cli.Invocation) cli.Result {
		return runPull(ctx, invocation, puller)
	})
	RegisterReplayGuards(app)
}

func registerPullFailure(app *cli.App, err error) {
	if app == nil {
		return
	}
	if app.Handlers == nil {
		app.Handlers = make(map[cli.CommandName]cli.Handler)
	}
	app.Handlers[cli.CommandPull] = cli.HandlerFunc(func(context.Context, *cli.Invocation) cli.Result {
		return commandError(cli.ErrorCapability, fmt.Sprintf("configure pull: %v", err))
	})
}

// RegisterReplayGuards wraps status and every ordinary managed writer. It is
// exported so embedders that replace handlers after RegisterPull can reapply
// the guard without reconstructing the pull controller.
func RegisterReplayGuards(app *cli.App) {
	if app == nil || app.Handlers == nil {
		return
	}
	wrapStatusForReplay(app)
	for _, name := range []cli.CommandName{cli.CommandCommit, cli.CommandRevert} {
		wrapWriteForReplay(app, name)
	}
}

type replayWrapped interface{ replayWrapped() }

type replayGuardHandler struct {
	inner cli.Handler
	name  cli.CommandName
}

func (replayGuardHandler) replayWrapped() {}

func (h replayGuardHandler) Run(ctx context.Context, invocation *cli.Invocation) cli.Result {
	store, result := openManaged(ctx, invocation)
	if result != nil {
		return *result
	}
	active, err := pullflow.Active(store.Repository())
	if err != nil {
		return commandError(cli.ErrorConflict, fmt.Sprintf("inspect active pull replay: %v", err))
	}
	if active != nil {
		return commandError(cli.ErrorConflict, fmt.Sprintf("%s is unavailable while a pull replay is active", h.name))
	}
	return h.inner.Run(ctx, invocation)
}

func wrapWriteForReplay(app *cli.App, name cli.CommandName) {
	inner := app.Handlers[name]
	if inner == nil {
		return
	}
	if _, wrapped := inner.(replayWrapped); wrapped {
		return
	}
	app.Handlers[name] = replayGuardHandler{inner: inner, name: name}
}

type replayStatusHandler struct{ inner cli.Handler }

func (replayStatusHandler) replayWrapped() {}

func (h replayStatusHandler) Run(ctx context.Context, invocation *cli.Invocation) cli.Result {
	result := h.inner.Run(ctx, invocation)
	if result.Outcome == cli.OutcomeError || result.Value == nil {
		return result
	}
	status, ok := result.Value.(*managedread.StatusResult)
	if !ok {
		return commandError(cli.ErrorInternal, "status handler returned an unexpected result type")
	}
	store, failed := openManaged(ctx, invocation)
	if failed != nil {
		return *failed
	}
	active, err := pullflow.Active(store.Repository())
	if err != nil {
		return commandError(cli.ErrorConflict, fmt.Sprintf("inspect active pull replay: %v", err))
	}
	if active != nil {
		status.Mode = managedread.StatusPullReplay
		status.Replay = active
		status.CandidateBase = active.Private
	}
	return result
}

func wrapStatusForReplay(app *cli.App) {
	inner := app.Handlers[cli.CommandStatus]
	if inner == nil {
		return
	}
	if _, wrapped := inner.(replayWrapped); wrapped {
		return
	}
	app.Handlers[cli.CommandStatus] = replayStatusHandler{inner: inner}
}

func runPull(ctx context.Context, invocation *cli.Invocation, puller *pullflow.Puller) cli.Result {
	if invocation == nil || len(invocation.Arguments) > 2 {
		return commandError(cli.ErrorInternal, "pull invocation has invalid arguments")
	}
	store, failed := openManaged(ctx, invocation)
	if failed != nil {
		return *failed
	}
	var result *pullflow.Result
	var err error
	switch {
	case invocation.Options.Has("continue"):
		result, err = puller.Continue(ctx, store)
	case invocation.Options.Has("abort"):
		result, err = puller.Abort(ctx, store)
	default:
		remote, branch := "", ""
		if len(invocation.Arguments) >= 1 {
			remote = invocation.Arguments[0]
		}
		if len(invocation.Arguments) == 2 {
			branch = invocation.Arguments[1]
		}
		result, err = puller.Pull(ctx, store, remote, branch)
	}
	if err != nil {
		return pullFailure(err)
	}
	if result == nil {
		return commandError(cli.ErrorInternal, "pull returned no result")
	}
	if result.Validation.Status == checker.StatusIndeterminate || result.CandidateValidation != nil && result.CandidateValidation.Status == checker.StatusIndeterminate {
		return cli.Result{Outcome: cli.OutcomeIndeterminate, Value: result}
	}
	switch result.State {
	case pullflow.Conflict, pullflow.Rejected:
		return cli.Result{Outcome: cli.OutcomeIssues, Value: result}
	case pullflow.UpToDate, pullflow.FastForwarded, pullflow.Replayed, pullflow.Aborted:
		return cli.Result{Outcome: cli.OutcomeOK, Value: result}
	default:
		return commandError(cli.ErrorInternal, fmt.Sprintf("pull returned unknown state %q", result.State))
	}
}

func pullFailure(err error) cli.Result {
	kind := cli.ErrorOperational
	switch pullflow.KindOf(err) {
	case pullflow.ErrorUsage:
		kind = cli.ErrorUsage
	case pullflow.ErrorCancelled:
		kind = cli.ErrorCancelled
	case pullflow.ErrorRepository:
		kind = cli.ErrorRepository
	case pullflow.ErrorCapability:
		kind = cli.ErrorCapability
	case pullflow.ErrorTrust:
		kind = cli.ErrorTrust
	case pullflow.ErrorHook:
		kind = cli.ErrorHook
	case pullflow.ErrorNetwork:
		kind = cli.ErrorNetwork
	case pullflow.ErrorConflict:
		kind = cli.ErrorConflict
	case pullflow.ErrorConcurrency:
		kind = cli.ErrorConcurrency
	case pullflow.ErrorIntegration:
		kind = cli.ErrorIntegration
	case pullflow.ErrorIO:
		kind = cli.ErrorIO
	}
	result := cli.Result{Outcome: cli.OutcomeError, Error: &cli.ProtocolError{Kind: kind, Message: fmt.Sprintf("pull: %v", err)}}
	if mutation := pullflow.MutationOf(err); mutation != nil {
		protocol := cli.NewMutationResult()
		protocol.Durable = mutation.Durable
		protocol.CheckoutChanged = mutation.CheckoutChanged
		protocol.RecoveryRequired = mutation.RecoveryRequired
		for _, update := range mutation.LocalRefs {
			protocol.LocalRefs = append(protocol.LocalRefs, cli.RefMutation{Ref: update.Ref, Before: cloneStringForCommand(update.Before), After: cloneStringForCommand(update.After)})
		}
		if mutation.Head != nil {
			protocol.Head = &cli.HeadMutation{
				Before: cli.MutationGitState{Ref: cloneStringForCommand(mutation.Head.Before.Ref), Commit: cloneStringForCommand(mutation.Head.Before.Commit)},
				After:  cli.MutationGitState{Ref: cloneStringForCommand(mutation.Head.After.Ref), Commit: cloneStringForCommand(mutation.Head.After.Commit)},
			}
		}
		result.Value = protocol
	}
	return result
}
