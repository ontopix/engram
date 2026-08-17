package commands

import (
	"context"
	"errors"
	"fmt"

	"github.com/ontopix/engram/internal/acquire"
	"github.com/ontopix/engram/internal/attachment"
	"github.com/ontopix/engram/internal/cli"
	"github.com/ontopix/engram/internal/harness"
	"github.com/ontopix/engram/internal/projectsetup"
)

// RegisterSetup installs the project-scoped agent harness workflow.
func RegisterSetup(app *cli.App) {
	if app == nil {
		return
	}
	if app.Handlers == nil {
		app.Handlers = make(map[cli.CommandName]cli.Handler)
	}
	app.Handlers[cli.CommandSetup] = cli.HandlerFunc(runSetup)
}

func runSetup(ctx context.Context, invocation *cli.Invocation) cli.Result {
	if result := cancellation(ctx); result != nil {
		return *result
	}
	if invocation == nil {
		return commandError(cli.ErrorInternal, "setup invocation is nil")
	}
	projectOption, _ := invocation.Options.One("project")
	project, err := attachment.ResolveProject(ctx, projectOption)
	if err != nil {
		return failure(err, cli.ErrorIO, "select setup project")
	}
	harnessName, _ := invocation.Options.One("harness")
	memoryFileOption, _ := invocation.Options.One("memory-file")
	validationScope := acquire.ValidationScopeCurrent
	if invocation.Options.Has("check-history") {
		validationScope = acquire.ValidationScopeHistory
	}
	result, err := projectsetup.Run(ctx, projectsetup.Options{
		Project: project, Harness: harnessName, MemoryFile: memoryFileOption,
		DryRun: invocation.Options.Has("dry-run"), ValidationScope: validationScope,
	})
	if err != nil {
		switch {
		case errors.Is(err, projectsetup.ErrValidation):
			return cli.Result{Outcome: cli.OutcomeIssues, Value: result}
		case errors.Is(err, projectsetup.ErrIndeterminate):
			return cli.Result{Outcome: cli.OutcomeIndeterminate, Value: result}
		}
		if _, present := acquire.MutationOf(err); present {
			return acquireFailure(err, "setup configured memory")
		}
		if _, present := attachment.EffectOf(err); present {
			return attachmentFailure(err, "setup project attachments")
		}
		kind := cli.ErrorIO
		switch {
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			kind = cli.ErrorCancelled
		case errors.Is(err, projectsetup.ErrUsage), errors.Is(err, harness.ErrUnsupported), acquire.KindOf(err) == acquire.ErrorUsage:
			kind = cli.ErrorUsage
		case errors.Is(err, projectsetup.ErrConfig), errors.Is(err, projectsetup.ErrConflict), errors.Is(err, harness.ErrConflict):
			kind = cli.ErrorIntegration
		case errors.Is(err, attachment.ErrMalformedBlock):
			kind = cli.ErrorIntegration
		case errors.Is(err, attachment.ErrBusy), acquire.KindOf(err) == acquire.ErrorConcurrency:
			kind = cli.ErrorConcurrency
		case acquire.KindOf(err) == acquire.ErrorCancelled:
			kind = cli.ErrorCancelled
		case acquire.KindOf(err) == acquire.ErrorCapability:
			kind = cli.ErrorCapability
		case acquire.KindOf(err) == acquire.ErrorNetwork:
			kind = cli.ErrorNetwork
		case acquire.KindOf(err) == acquire.ErrorConflict:
			kind = cli.ErrorConflict
		case acquire.KindOf(err) == acquire.ErrorIntegration:
			kind = cli.ErrorIntegration
		case acquire.KindOf(err) == acquire.ErrorRepository:
			kind = cli.ErrorRepository
		case acquire.KindOf(err) == acquire.ErrorIO:
			kind = cli.ErrorIO
		case acquire.KindOf(err) == acquire.ErrorRecovery:
			kind = cli.ErrorOperational
		}
		protocolError := &cli.ProtocolError{Kind: kind, Message: fmt.Sprintf("setup project: %v", err)}
		if result.Changed {
			return cli.Result{Outcome: cli.OutcomeError, Value: result, Error: protocolError}
		}
		return cli.Result{Outcome: cli.OutcomeError, Error: protocolError}
	}
	return cli.Result{Outcome: cli.OutcomeOK, Value: result}
}
