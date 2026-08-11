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
	"github.com/ontopix/engram/internal/initialize"
	"github.com/ontopix/engram/internal/managedwrite"
)

type initializationRunner interface {
	Run(context.Context, string, initialize.Options) (initialize.Result, error)
}

// RegisterInitialization installs init with the same controller-owned trust
// location used by managed acceptance. Initialization intrinsically selects
// the empty base hook set, but still resolves the physical store binding.
func RegisterInitialization(app *cli.App) {
	root, err := os.UserConfigDir()
	if err != nil {
		registerInitializationFailure(app, err)
		return
	}
	RegisterInitializationAt(app, filepath.Join(root, "engram", "hook-trust-v1.json"))
}

func RegisterInitializationAt(app *cli.App, registryPath string) {
	registry, err := hooks.NewRegistry(registryPath)
	if err != nil {
		registerInitializationFailure(app, err)
		return
	}
	registerInitializationWith(app, initialize.New(managedwrite.New(hookexec.New(registry))))
}

func registerInitializationWith(app *cli.App, runner initializationRunner) {
	if app == nil {
		return
	}
	if app.Handlers == nil {
		app.Handlers = make(map[cli.CommandName]cli.Handler)
	}
	if runner == nil {
		registerInitializationFailure(app, errors.New("initialization engine is unavailable"))
		return
	}
	app.Handlers[cli.CommandInit] = cli.HandlerFunc(func(ctx context.Context, invocation *cli.Invocation) cli.Result {
		return runInitialize(ctx, invocation, runner)
	})
}

func registerInitializationFailure(app *cli.App, err error) {
	if app == nil {
		return
	}
	if app.Handlers == nil {
		app.Handlers = make(map[cli.CommandName]cli.Handler)
	}
	app.Handlers[cli.CommandInit] = cli.HandlerFunc(func(context.Context, *cli.Invocation) cli.Result {
		return commandError(cli.ErrorCapability, fmt.Sprintf("configure initialization: %v", err))
	})
}

func runInitialize(ctx context.Context, invocation *cli.Invocation, runner initializationRunner) cli.Result {
	if invocation == nil || len(invocation.Arguments) > 1 {
		return commandError(cli.ErrorInternal, "init invocation has invalid arguments")
	}
	target := "."
	if len(invocation.Arguments) == 1 {
		target = invocation.Arguments[0]
	}
	result, err := runner.Run(ctx, target, initialize.Options{
		Schemas: invocation.Options.All("schema"),
		DryRun:  invocation.Options.Has("dry-run"),
	})
	if err != nil {
		return initializationFailure(err, "initialize managed store")
	}
	switch result.Validation.Status {
	case checker.StatusIndeterminate:
		return cli.Result{Outcome: cli.OutcomeIndeterminate, Value: result}
	case checker.StatusComplete:
		if result.Validation.HasErrors() {
			return cli.Result{Outcome: cli.OutcomeIssues, Value: result}
		}
		return cli.Result{Outcome: cli.OutcomeOK, Value: result}
	default:
		return commandError(cli.ErrorInternal, fmt.Sprintf("initialization returned unknown validation status %q", result.Validation.Status))
	}
}

func initializationFailure(err error, action string) cli.Result {
	kind := cli.ErrorOperational
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		kind = cli.ErrorCancelled
	} else {
		switch initialize.KindOf(err) {
		case initialize.ErrorUsage:
			kind = cli.ErrorUsage
		case initialize.ErrorCancelled:
			kind = cli.ErrorCancelled
		case initialize.ErrorCapability:
			kind = cli.ErrorCapability
		case initialize.ErrorConflict:
			kind = cli.ErrorConflict
		case initialize.ErrorConcurrency:
			kind = cli.ErrorConcurrency
		case initialize.ErrorIntegration:
			kind = cli.ErrorIntegration
		case initialize.ErrorRepository:
			kind = cli.ErrorRepository
		case initialize.ErrorIO:
			kind = cli.ErrorIO
		case initialize.ErrorRecovery:
			kind = cli.ErrorOperational
		}
	}
	protocolError := &cli.ProtocolError{Kind: kind, Message: fmt.Sprintf("%s: %v", action, err)}
	mutationEvidence, present := initialize.MutationOf(err)
	if !present {
		return cli.Result{Outcome: cli.OutcomeError, Error: protocolError}
	}
	mutation := cli.NewMutationResult()
	mutation.Durable = mutationEvidence.Durable
	mutation.CheckoutChanged = mutationEvidence.CheckoutChanged
	mutation.RecoveryRequired = mutationEvidence.RecoveryRequired
	if mutationEvidence.Commit != nil {
		mutation.LocalRefs = append(mutation.LocalRefs, cli.RefMutation{Ref: "refs/heads/main", After: cloneStringForCommand(mutationEvidence.Commit)})
	}
	return cli.Result{Outcome: cli.OutcomeError, Value: mutation, Error: protocolError}
}
