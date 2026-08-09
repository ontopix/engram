package commands

import (
	"context"
	"fmt"

	"github.com/ontopix/engram/internal/acquire"
	"github.com/ontopix/engram/internal/checker"
	"github.com/ontopix/engram/internal/cli"
)

func RegisterAcquisition(app *cli.App) {
	if app == nil {
		return
	}
	if app.Handlers == nil {
		app.Handlers = make(map[cli.CommandName]cli.Handler)
	}
	app.Handlers[cli.CommandClone] = cli.HandlerFunc(runClone)
}

func runClone(ctx context.Context, invocation *cli.Invocation) cli.Result {
	if invocation == nil || len(invocation.Arguments) < 1 || len(invocation.Arguments) > 2 {
		return commandError(cli.ErrorInternal, "clone invocation has invalid arguments")
	}
	options := acquire.Options{}
	if len(invocation.Arguments) == 2 {
		options.Destination = invocation.Arguments[1]
		options.DestinationProvided = true
	}
	cloned, err := acquire.Clone(ctx, invocation.Arguments[0], options)
	if err != nil {
		return acquireFailure(err, "clone managed store")
	}
	switch {
	case cloned.Validation.Status == checker.StatusIndeterminate:
		return cli.Result{Outcome: cli.OutcomeIndeterminate, Value: cloned}
	case cloned.Validation.HasErrors():
		return cli.Result{Outcome: cli.OutcomeIssues, Value: cloned}
	default:
		return cli.Result{Outcome: cli.OutcomeOK, Value: cloned}
	}
}

func acquireFailure(err error, action string) cli.Result {
	kind := cli.ErrorOperational
	switch acquire.KindOf(err) {
	case acquire.ErrorUsage:
		kind = cli.ErrorUsage
	case acquire.ErrorCancelled:
		kind = cli.ErrorCancelled
	case acquire.ErrorCapability:
		kind = cli.ErrorCapability
	case acquire.ErrorNetwork:
		kind = cli.ErrorNetwork
	case acquire.ErrorConflict:
		kind = cli.ErrorConflict
	case acquire.ErrorConcurrency:
		kind = cli.ErrorConcurrency
	case acquire.ErrorIntegration:
		kind = cli.ErrorIntegration
	case acquire.ErrorRepository:
		kind = cli.ErrorRepository
	case acquire.ErrorIO:
		kind = cli.ErrorIO
	}
	return commandError(kind, fmt.Sprintf("%s: %v", action, err))
}
