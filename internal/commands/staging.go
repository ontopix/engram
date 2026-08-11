package commands

import (
	"context"
	"errors"
	"fmt"

	"github.com/ontopix/engram/internal/cli"
	"github.com/ontopix/engram/internal/managedread"
	"github.com/ontopix/engram/internal/rendezvous"
	"github.com/ontopix/engram/internal/staging"
)

type stagingRunner interface {
	Add(context.Context, *managedread.Store, []string, bool) (staging.Result, error)
}

func RegisterStaging(app *cli.App) {
	registerStagingWith(app, staging.New())
}

func registerStagingWith(app *cli.App, runner stagingRunner) {
	if app == nil {
		return
	}
	if app.Handlers == nil {
		app.Handlers = make(map[cli.CommandName]cli.Handler)
	}
	if runner == nil {
		app.Handlers[cli.CommandAdd] = cli.HandlerFunc(func(context.Context, *cli.Invocation) cli.Result {
			return commandError(cli.ErrorCapability, "configure staging: staging engine is unavailable")
		})
		return
	}
	app.Handlers[cli.CommandAdd] = cli.HandlerFunc(func(ctx context.Context, invocation *cli.Invocation) cli.Result {
		return runAddWith(ctx, invocation, runner)
	})
}

func runAdd(ctx context.Context, invocation *cli.Invocation) cli.Result {
	return runAddWith(ctx, invocation, staging.New())
}

func runAddWith(ctx context.Context, invocation *cli.Invocation, runner stagingRunner) cli.Result {
	store, result := openManaged(ctx, invocation)
	if result != nil {
		return *result
	}
	added, err := runner.Add(ctx, store, invocation.Arguments, invocation.Options.Has("all"))
	if err != nil {
		return stagingFailure(err, "stage logical selection")
	}
	return cli.Result{Outcome: cli.OutcomeOK, Value: added}
}

func stagingFailure(err error, action string) cli.Result {
	kind := cli.ErrorRepository
	switch {
	case errors.Is(err, staging.ErrSelection):
		kind = cli.ErrorUsage
	case errors.Is(err, staging.ErrConcurrent), errors.Is(err, rendezvous.ErrBusy), errors.Is(err, rendezvous.ErrOwnership):
		kind = cli.ErrorConcurrency
	}
	protocolError := &cli.ProtocolError{Kind: kind, Message: fmt.Sprintf("%s: %v", action, err)}
	mutationEvidence, present := staging.MutationOf(err)
	if !present {
		if kind == cli.ErrorRepository {
			return managedFailure(err, action)
		}
		return cli.Result{Outcome: cli.OutcomeError, Error: protocolError}
	}
	mutation := cli.NewMutationResult()
	mutation.Durable = mutationEvidence.Durable
	mutation.CheckoutChanged = mutationEvidence.CheckoutChanged
	mutation.RecoveryRequired = mutationEvidence.RecoveryRequired
	return cli.Result{Outcome: cli.OutcomeError, Value: mutation, Error: protocolError}
}
