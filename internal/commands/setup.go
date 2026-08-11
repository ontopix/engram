package commands

import (
	"context"
	"errors"
	"fmt"

	"github.com/ontopix/engram/internal/attachment"
	"github.com/ontopix/engram/internal/cli"
	"github.com/ontopix/engram/internal/harness"
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
	memoryFile, err := attachment.ResolveMemoryFile(project, memoryFileOption)
	if err != nil {
		return failure(err, cli.ErrorIntegration, "select setup memory manifest")
	}
	result, err := harness.Setup(project, harnessName, memoryFile, invocation.Options.Has("dry-run"))
	if err != nil {
		kind := cli.ErrorIO
		if errors.Is(err, harness.ErrConflict) {
			kind = cli.ErrorIntegration
		} else if errors.Is(err, harness.ErrUnsupported) {
			kind = cli.ErrorUsage
		} else if errors.Is(err, attachment.ErrMalformedBlock) {
			kind = cli.ErrorIntegration
		} else if errors.Is(err, attachment.ErrBusy) {
			kind = cli.ErrorConcurrency
		}
		return commandError(kind, fmt.Sprintf("setup agent harness: %v", err))
	}
	return cli.Result{Outcome: cli.OutcomeOK, Value: result}
}
