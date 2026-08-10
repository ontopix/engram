package commands

import (
	"context"
	"errors"
	"fmt"

	"github.com/ontopix/engram/internal/attachment"
	"github.com/ontopix/engram/internal/cli"
	"github.com/ontopix/engram/internal/managedread"
)

type attachResult struct {
	Project    string                     `json:"project"`
	Store      string                     `json:"store"`
	Entrypoint string                     `json:"entrypoint"`
	Changed    bool                       `json:"changed"`
	Validation any                        `json:"validation"`
	Audits     []managedread.HistoryAudit `json:"audits"`
}

type attachmentUpdater interface {
	Attach(string, string, string) (attachment.Result, error)
	Detach(string, string, string) (attachment.Result, error)
}

// RegisterAttachments installs the local attach and detach workflows.
func RegisterAttachments(app *cli.App) {
	registerAttachmentsWith(app, attachment.NewUpdater())
}

func registerAttachmentsWith(app *cli.App, updater attachmentUpdater) {
	if app == nil {
		return
	}
	if app.Handlers == nil {
		app.Handlers = make(map[cli.CommandName]cli.Handler)
	}
	if updater == nil {
		for _, name := range []cli.CommandName{cli.CommandAttach, cli.CommandDetach} {
			app.Handlers[name] = cli.HandlerFunc(func(context.Context, *cli.Invocation) cli.Result {
				return commandError(cli.ErrorCapability, "attachment updater is unavailable")
			})
		}
		return
	}
	app.Handlers[cli.CommandAttach] = cli.HandlerFunc(func(ctx context.Context, invocation *cli.Invocation) cli.Result {
		return runAttach(ctx, invocation, updater)
	})
	app.Handlers[cli.CommandDetach] = cli.HandlerFunc(func(ctx context.Context, invocation *cli.Invocation) cli.Result {
		return runDetach(ctx, invocation, updater)
	})
}

func runAttach(ctx context.Context, invocation *cli.Invocation, updater attachmentUpdater) cli.Result {
	if invocation == nil || len(invocation.Arguments) != 1 {
		return commandError(cli.ErrorInternal, "attach invocation has invalid arguments")
	}
	storePath, err := attachment.CanonicalStore(invocation.Arguments[0])
	if err != nil {
		return commandError(cli.ErrorRepository, fmt.Sprintf("select attached store: %v", err))
	}
	store, err := managedread.Open(ctx, storePath)
	if err != nil {
		return managedFailure(err, "open attached managed store")
	}
	audit, err := store.AuditAccepted(ctx)
	if err != nil {
		return managedFailure(err, "audit attached managed store")
	}
	projectOption, _ := invocation.Options.One("project")
	project, err := attachment.ResolveProject(ctx, projectOption)
	if err != nil {
		return attachmentFailure(err, "select attachment project")
	}
	entrypointOption, _ := invocation.Options.One("entrypoint")
	entrypoint, err := attachment.ResolveEntrypoint(project, entrypointOption)
	if err != nil {
		return attachmentFailure(err, "select attachment entrypoint")
	}
	value := attachResult{
		Project: project, Store: storePath, Entrypoint: entrypoint,
		Validation: audit.Validation, Audits: audit.Audits,
	}
	if audit.Validation.Status == "indeterminate" {
		return cli.Result{Outcome: cli.OutcomeIndeterminate, Value: value}
	}
	if audit.Validation.HasErrors() {
		return cli.Result{Outcome: cli.OutcomeIssues, Value: value}
	}
	published, err := updater.Attach(project, entrypoint, storePath)
	if err != nil {
		return attachmentFailure(err, "publish attachment")
	}
	value.Changed = published.Changed
	return cli.Result{Outcome: cli.OutcomeOK, Value: value}
}

func runDetach(ctx context.Context, invocation *cli.Invocation, updater attachmentUpdater) cli.Result {
	if invocation == nil || len(invocation.Arguments) != 1 {
		return commandError(cli.ErrorInternal, "detach invocation has invalid arguments")
	}
	if result := cancellation(ctx); result != nil {
		return *result
	}
	projectOption, _ := invocation.Options.One("project")
	project, err := attachment.ResolveProject(ctx, projectOption)
	if err != nil {
		return attachmentFailure(err, "select attachment project")
	}
	entrypointOption, _ := invocation.Options.One("entrypoint")
	entrypoint, err := attachment.ResolveEntrypoint(project, entrypointOption)
	if err != nil {
		return attachmentFailure(err, "select attachment entrypoint")
	}
	published, err := updater.Detach(project, entrypoint, invocation.Arguments[0])
	if err != nil {
		return attachmentFailure(err, "publish detachment")
	}
	return cli.Result{Outcome: cli.OutcomeOK, Value: published}
}

func attachmentFailure(err error, action string) cli.Result {
	kind := cli.ErrorIO
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		kind = cli.ErrorCancelled
	case errors.Is(err, attachment.ErrMalformedBlock):
		kind = cli.ErrorIntegration
	case errors.Is(err, attachment.ErrBusy):
		kind = cli.ErrorConcurrency
	}
	result := commandError(kind, fmt.Sprintf("%s: %v", action, err))
	if effect, ok := attachment.EffectOf(err); ok {
		mutation := cli.NewMutationResult()
		mutation.Durable = effect.Durable
		mutation.RecoveryRequired = effect.RecoveryRequired
		result.Value = mutation
	}
	return result
}
