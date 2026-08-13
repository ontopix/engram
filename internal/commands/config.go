package commands

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/ontopix/engram/internal/attachment"
	"github.com/ontopix/engram/internal/cli"
	"github.com/ontopix/engram/internal/harness"
	"github.com/ontopix/engram/internal/projectsetup"
	"go.yaml.in/yaml/v3"
)

func RegisterConfig(app *cli.App) {
	if app == nil {
		return
	}
	if app.Handlers == nil {
		app.Handlers = make(map[cli.CommandName]cli.Handler)
	}
	app.Handlers[cli.CommandConfigAttachmentAdd] = cli.HandlerFunc(runConfigAttachmentAdd)
	app.Handlers[cli.CommandConfigAttachmentRemove] = cli.HandlerFunc(runConfigAttachmentRemove)
	app.Handlers[cli.CommandConfigHarness] = cli.HandlerFunc(runConfigHarness)
	app.Handlers[cli.CommandConfigShow] = cli.HandlerFunc(runConfigShow)
}

func runConfigAttachmentAdd(ctx context.Context, invocation *cli.Invocation) cli.Result {
	if invocation == nil || len(invocation.Arguments) != 2 {
		return commandError(cli.ErrorInternal, "config.attachment.add invocation has invalid arguments")
	}
	project, result := configProject(ctx, invocation)
	if result != nil {
		return *result
	}
	configured, err := projectsetup.AddConfigAttachment(project, invocation.Arguments[0], invocation.Arguments[1])
	return configCommandResult(configured, err, "add configured attachment")
}

func runConfigAttachmentRemove(ctx context.Context, invocation *cli.Invocation) cli.Result {
	if invocation == nil || len(invocation.Arguments) != 1 {
		return commandError(cli.ErrorInternal, "config.attachment.remove invocation has invalid arguments")
	}
	project, result := configProject(ctx, invocation)
	if result != nil {
		return *result
	}
	configured, err := projectsetup.RemoveConfigAttachment(project, invocation.Arguments[0])
	return configCommandResult(configured, err, "remove configured attachment")
}

func runConfigHarness(ctx context.Context, invocation *cli.Invocation) cli.Result {
	if invocation == nil || len(invocation.Arguments) != 1 {
		return commandError(cli.ErrorInternal, "config.harness invocation has invalid arguments")
	}
	project, result := configProject(ctx, invocation)
	if result != nil {
		return *result
	}
	configured, err := projectsetup.SetConfigHarness(project, invocation.Arguments[0])
	return configCommandResult(configured, err, "set configured harness")
}

func runConfigShow(ctx context.Context, invocation *cli.Invocation) cli.Result {
	if invocation == nil || len(invocation.Arguments) != 0 {
		return commandError(cli.ErrorInternal, "config.show invocation has invalid arguments")
	}
	project, result := configProject(ctx, invocation)
	if result != nil {
		return *result
	}
	configured, err := projectsetup.ShowConfig(project)
	if err != nil {
		return configCommandResult(configured, err, "show project configuration")
	}
	return cli.Result{
		Outcome: cli.OutcomeOK,
		Value:   configured,
		Text: cli.TextRendererFunc(func(output io.Writer) error {
			data, encodeErr := yaml.Marshal(configured.Config)
			if encodeErr != nil {
				return encodeErr
			}
			_, writeErr := output.Write(data)
			return writeErr
		}),
	}
}

func configProject(ctx context.Context, invocation *cli.Invocation) (string, *cli.Result) {
	if result := cancellation(ctx); result != nil {
		return "", result
	}
	projectOption, _ := invocation.Options.One("project")
	project, err := attachment.ResolveProject(ctx, projectOption)
	if err != nil {
		result := configCommandResult(projectsetup.ConfigResult{}, err, "select configuration project")
		return "", &result
	}
	return project, nil
}

func configCommandResult(result projectsetup.ConfigResult, err error, action string) cli.Result {
	if err == nil {
		return cli.Result{Outcome: cli.OutcomeOK, Value: result}
	}
	kind := cli.ErrorIO
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		kind = cli.ErrorCancelled
	case errors.Is(err, projectsetup.ErrConfigBusy):
		kind = cli.ErrorConcurrency
	case errors.Is(err, projectsetup.ErrConfigArgument):
		kind = cli.ErrorUsage
	case errors.Is(err, projectsetup.ErrConflict):
		kind = cli.ErrorConflict
	case errors.Is(err, projectsetup.ErrConfig):
		kind = cli.ErrorIntegration
	case errors.Is(err, harness.ErrUnsupported):
		kind = cli.ErrorUsage
	}
	failure := commandError(kind, fmt.Sprintf("%s: %v", action, err))
	if effect, present := projectsetup.ConfigEffectOf(err); present {
		mutation := cli.NewMutationResult()
		mutation.Durable = effect.Durable
		mutation.RecoveryRequired = effect.RecoveryRequired
		failure.Value = mutation
	}
	return failure
}
