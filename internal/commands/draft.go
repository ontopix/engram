package commands

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/ontopix/engram/internal/checker"
	"github.com/ontopix/engram/internal/cli"
	"github.com/ontopix/engram/internal/draft"
	"github.com/ontopix/engram/internal/managedread"
	"github.com/ontopix/engram/internal/rendezvous"
)

// RegisterDraft installs the working-draft helpers. These commands mutate
// only portable store bytes and coordinate through the managed worktree
// rendezvous; they never stage paths or move the accepted ref.
func RegisterDraft(app *cli.App) {
	if app == nil {
		return
	}
	if app.Handlers == nil {
		app.Handlers = make(map[cli.CommandName]cli.Handler)
	}
	app.Handlers[cli.CommandFmt] = cli.HandlerFunc(runFmt)
	app.Handlers[cli.CommandNew] = cli.HandlerFunc(func(ctx context.Context, invocation *cli.Invocation) cli.Result {
		return runNew(ctx, invocation, app.Stdin)
	})
	app.Handlers[cli.CommandMove] = cli.HandlerFunc(runMove)
	app.Handlers[cli.CommandSchemaCopy] = cli.HandlerFunc(runSchemaCopy)
}

func runFmt(ctx context.Context, invocation *cli.Invocation) cli.Result {
	root, locker, result := openDraft(ctx, invocation)
	if result != nil {
		return *result
	}
	formatted, err := draft.Fmt(ctx, root, draft.FmtOptions{
		Paths: invocation.Arguments, Check: invocation.Options.Has("check"),
		DryRun: invocation.Options.Has("dry-run"), Rendezvous: locker,
	})
	if err != nil {
		return draftFailure(err, "format catalogs")
	}
	outcome := cli.OutcomeOK
	if formatted.Check && formatted.Changed {
		outcome = cli.OutcomeIssues
	}
	return cli.Result{Outcome: outcome, Value: formatted}
}

func runNew(ctx context.Context, invocation *cli.Invocation, stdin io.Reader) cli.Result {
	if invocation == nil || len(invocation.Arguments) != 2 {
		return commandError(cli.ErrorInternal, "new invocation has invalid arguments")
	}
	root, locker, result := openDraft(ctx, invocation)
	if result != nil {
		return *result
	}
	var fields []byte
	if name, present := invocation.Options.One("fields"); present {
		var err error
		fields, err = readDraftInput(name, nil)
		if err != nil {
			return commandError(cli.ErrorIO, fmt.Sprintf("read fields file: %v", err))
		}
	}
	var body []byte
	bodyProvided := false
	if name, present := invocation.Options.One("body"); present {
		bodyProvided = true
		var err error
		body, err = readDraftInput(name, stdin)
		if err != nil {
			return commandError(cli.ErrorIO, fmt.Sprintf("read body: %v", err))
		}
	}
	description, _ := invocation.Options.One("description")
	title, titleProvided := invocation.Options.One("title")
	created, err := draft.New(ctx, root, invocation.Arguments[0], invocation.Arguments[1], draft.NewOptions{
		Description: description, Fields: fields, Body: body, BodyProvided: bodyProvided,
		Title: title, TitleProvided: titleProvided, DryRun: invocation.Options.Has("dry-run"),
		Rendezvous: locker,
	})
	if err != nil {
		return draftFailure(err, "create record")
	}
	return cli.Result{Outcome: cli.OutcomeOK, Value: created}
}

func runSchemaCopy(ctx context.Context, invocation *cli.Invocation) cli.Result {
	if invocation == nil || len(invocation.Arguments) != 1 {
		return commandError(cli.ErrorInternal, "schema.copy invocation has invalid arguments")
	}
	root, locker, result := openDraft(ctx, invocation)
	if result != nil {
		return *result
	}
	scope, scopeProvided := invocation.Options.One("to")
	copied, err := draft.SchemaCopy(ctx, root, invocation.Arguments[0], draft.SchemaCopyOptions{
		Scope: scope, ScopeProvided: scopeProvided, DryRun: invocation.Options.Has("dry-run"), Rendezvous: locker,
	})
	if err != nil {
		return draftFailure(err, "copy schema")
	}
	return cli.Result{Outcome: cli.OutcomeOK, Value: copied}
}

func runMove(ctx context.Context, invocation *cli.Invocation) cli.Result {
	if invocation == nil || len(invocation.Arguments) != 2 {
		return commandError(cli.ErrorInternal, "mv invocation has invalid arguments")
	}
	root, locker, result := openDraft(ctx, invocation)
	if result != nil {
		return *result
	}
	moved, err := draft.Move(ctx, root, invocation.Arguments[0], invocation.Arguments[1], draft.MoveOptions{
		DryRun: invocation.Options.Has("dry-run"), Rendezvous: locker,
	})
	if err != nil {
		return draftFailure(err, "move record")
	}
	return cli.Result{Outcome: cli.OutcomeOK, Value: moved}
}

type draftLocker struct {
	gitDirectory string
	root         string
}

func (l draftLocker) LockDraft(ctx context.Context, _ string) (draft.Unlock, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	handle, err := rendezvous.AcquireWorktree(l.gitDirectory)
	if err != nil {
		return nil, err
	}
	store, err := managedread.Open(ctx, l.root)
	if err == nil {
		var audit *managedread.AcceptedAudit
		audit, err = store.AuditAccepted(ctx)
		if err == nil && (audit.Validation.Status != checker.StatusComplete || audit.Validation.HasErrors()) {
			err = errors.New("selected store ceased to be a conforming managed store")
		}
	}
	if err != nil {
		return nil, errors.Join(err, handle.Release())
	}
	return handle.Release, nil
}

func openDraft(ctx context.Context, invocation *cli.Invocation) (string, draft.Locker, *cli.Result) {
	root, err := selectedStore(invocation)
	if err != nil {
		result := failure(err, cli.ErrorRepository, "select store")
		return "", nil, &result
	}
	store, err := managedread.Open(ctx, root)
	if err != nil {
		result := managedFailure(err, "open managed store")
		return "", nil, &result
	}
	audit, err := store.AuditAccepted(ctx)
	if err != nil {
		result := managedFailure(err, "audit managed store")
		return "", nil, &result
	}
	if audit.Validation.Status != checker.StatusComplete || audit.Validation.HasErrors() {
		result := commandError(cli.ErrorRepository, "selected store is not a conforming managed store")
		return "", nil, &result
	}
	return store.Repository().Root, draftLocker{gitDirectory: store.Repository().GitDir, root: store.Repository().Root}, nil
}

func readDraftInput(name string, stdin io.Reader) ([]byte, error) {
	if name != "-" {
		return os.ReadFile(name)
	}
	if stdin == nil {
		return nil, errors.New("standard input is unavailable")
	}
	return io.ReadAll(stdin)
}

func draftFailure(err error, action string) cli.Result {
	if err == nil {
		return commandError(cli.ErrorInternal, action+": unknown failure")
	}
	kind := cli.ErrorRepository
	switch draft.KindOf(err) {
	case draft.ErrorUsage:
		kind = cli.ErrorUsage
	case draft.ErrorCancelled:
		kind = cli.ErrorCancelled
	case draft.ErrorInternal:
		kind = cli.ErrorInternal
	case draft.ErrorCapability:
		kind = cli.ErrorCapability
	case draft.ErrorConflict:
		kind = cli.ErrorConflict
	case draft.ErrorConcurrency:
		kind = cli.ErrorConcurrency
	case draft.ErrorRepository:
		kind = cli.ErrorRepository
	case draft.ErrorIO:
		kind = cli.ErrorIO
	default:
		switch {
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			kind = cli.ErrorCancelled
		case errors.Is(err, rendezvous.ErrBusy), errors.Is(err, rendezvous.ErrOwnership):
			kind = cli.ErrorConcurrency
		}
	}
	return commandError(kind, fmt.Sprintf("%s: %v", action, err))
}
