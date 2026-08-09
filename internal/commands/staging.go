package commands

import (
	"context"
	"errors"
	"fmt"

	"github.com/ontopix/engram/internal/cli"
	"github.com/ontopix/engram/internal/rendezvous"
	"github.com/ontopix/engram/internal/staging"
)

func RegisterStaging(app *cli.App) {
	if app == nil {
		return
	}
	if app.Handlers == nil {
		app.Handlers = make(map[cli.CommandName]cli.Handler)
	}
	app.Handlers[cli.CommandAdd] = cli.HandlerFunc(runAdd)
}

func runAdd(ctx context.Context, invocation *cli.Invocation) cli.Result {
	store, result := openManaged(ctx, invocation)
	if result != nil {
		return *result
	}
	added, err := staging.Add(ctx, store, invocation.Arguments, invocation.Options.Has("all"))
	if err != nil {
		switch {
		case errors.Is(err, staging.ErrSelection):
			return commandError(cli.ErrorUsage, fmt.Sprintf("stage logical selection: %v", err))
		case errors.Is(err, staging.ErrConcurrent), errors.Is(err, rendezvous.ErrBusy), errors.Is(err, rendezvous.ErrOwnership):
			return commandError(cli.ErrorConcurrency, fmt.Sprintf("stage logical selection: %v", err))
		default:
			return managedFailure(err, "stage logical selection")
		}
	}
	return cli.Result{Outcome: cli.OutcomeOK, Value: added}
}
