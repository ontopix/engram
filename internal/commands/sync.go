package commands

import (
	"context"
	"fmt"

	"github.com/ontopix/engram/internal/checker"
	"github.com/ontopix/engram/internal/cli"
	"github.com/ontopix/engram/internal/syncflow"
)

// RegisterSync installs the network-facing synchronization commands whose
// workflow implementations are available. Pull is registered by the replay
// adapter; Push is deliberately independent and never mutates local state.
func RegisterSync(app *cli.App) {
	if app == nil {
		return
	}
	if app.Handlers == nil {
		app.Handlers = make(map[cli.CommandName]cli.Handler)
	}
	app.Handlers[cli.CommandPush] = cli.HandlerFunc(runPush)
}

func runPush(ctx context.Context, invocation *cli.Invocation) cli.Result {
	if invocation == nil || len(invocation.Arguments) > 2 {
		return commandError(cli.ErrorInternal, "push invocation has invalid arguments")
	}
	store, result := openManaged(ctx, invocation)
	if result != nil {
		return *result
	}
	remote, branch := "", ""
	if len(invocation.Arguments) >= 1 {
		remote = invocation.Arguments[0]
	}
	if len(invocation.Arguments) == 2 {
		branch = invocation.Arguments[1]
	}
	pushed, err := syncflow.Push(ctx, store, remote, branch)
	if err != nil {
		return syncFailure(err, "push accepted lineage")
	}
	return pushCommandResult(pushed)
}

func pushCommandResult(pushed *syncflow.PushResult) cli.Result {
	if pushed == nil {
		return commandError(cli.ErrorInternal, "push returned no result")
	}
	switch pushed.Validation.Status {
	case checker.StatusIndeterminate:
		return cli.Result{Outcome: cli.OutcomeIndeterminate, Value: pushed}
	case checker.StatusComplete:
		if pushed.Validation.HasErrors() {
			return cli.Result{Outcome: cli.OutcomeIssues, Value: pushed}
		}
	default:
		return commandError(cli.ErrorInternal, fmt.Sprintf("push returned unknown validation status %q", pushed.Validation.Status))
	}
	switch pushed.State {
	case syncflow.PushRejected:
		return cli.Result{Outcome: cli.OutcomeIssues, Value: pushed}
	case syncflow.PushIndeterminate:
		return cli.Result{Outcome: cli.OutcomeIndeterminate, Value: pushed}
	case syncflow.PushUpToDate, syncflow.PushPushed:
		return cli.Result{Outcome: cli.OutcomeOK, Value: pushed}
	default:
		return commandError(cli.ErrorInternal, fmt.Sprintf("push returned unknown state %q", pushed.State))
	}
}

func syncFailure(err error, action string) cli.Result {
	kind := cli.ErrorOperational
	switch syncflow.KindOf(err) {
	case syncflow.ErrorUsage:
		kind = cli.ErrorUsage
	case syncflow.ErrorCancelled:
		kind = cli.ErrorCancelled
	case syncflow.ErrorRepository:
		kind = cli.ErrorRepository
	case syncflow.ErrorCapability:
		kind = cli.ErrorCapability
	case syncflow.ErrorConcurrency:
		kind = cli.ErrorConcurrency
	case syncflow.ErrorNetwork:
		kind = cli.ErrorNetwork
	}
	return commandError(kind, fmt.Sprintf("%s: %v", action, err))
}
