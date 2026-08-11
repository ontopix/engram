package commands

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strconv"

	"github.com/ontopix/engram/internal/cli"
	"github.com/ontopix/engram/internal/gitraw"
	"github.com/ontopix/engram/internal/managedread"
)

// RegisterManagedReads installs the M2 read-only managed-store handlers. It
// preserves the already registered portable check forms and adds only the
// accepted and staged variants.
func RegisterManagedReads(app *cli.App) {
	if app == nil {
		return
	}
	if app.Handlers == nil {
		app.Handlers = make(map[cli.CommandName]cli.Handler)
	}
	portableCheck := app.Handlers[cli.CommandCheck]
	app.Handlers[cli.CommandCheck] = cli.HandlerFunc(func(ctx context.Context, invocation *cli.Invocation) cli.Result {
		if invocation != nil && (invocation.Options.Has("accepted") || invocation.Options.Has("staged")) {
			return runManagedCheck(ctx, invocation)
		}
		if portableCheck == nil {
			return commandError(cli.ErrorInternal, "portable check handler is not registered")
		}
		return portableCheck.Run(ctx, invocation)
	})
	app.Handlers[cli.CommandStatus] = cli.HandlerFunc(runStatus)
	app.Handlers[cli.CommandDiff] = cli.HandlerFunc(runDiff)
	app.Handlers[cli.CommandLog] = cli.HandlerFunc(runLog)
}

func runManagedCheck(ctx context.Context, invocation *cli.Invocation) cli.Result {
	if invocation == nil {
		return commandError(cli.ErrorInternal, "managed check invocation is nil")
	}
	if invocation.Options.Has("accepted") {
		target, err := selectedAcceptedCheckTarget(invocation)
		if err != nil {
			return failure(err, cli.ErrorRepository, "select managed check target")
		}
		validation, err := managedread.CheckAccepted(ctx, target)
		if err != nil {
			return managedFailure(err, "audit accepted history")
		}
		return validationResult(validation)
	}
	store, result := openManaged(ctx, invocation)
	if result != nil {
		return *result
	}
	validation, _, err := store.CheckStaged(ctx)
	if err != nil {
		return managedFailure(err, "check staged candidate")
	}
	return validationResult(validation)
}

func selectedAcceptedCheckTarget(invocation *cli.Invocation) (string, error) {
	if invocation != nil && invocation.Globals.StoreSet {
		if invocation.Globals.Store == "" {
			return "", fmt.Errorf("store path is empty")
		}
		return filepath.Abs(invocation.Globals.Store)
	}
	return selectedStore(invocation)
}

func runStatus(ctx context.Context, invocation *cli.Invocation) cli.Result {
	store, result := openManaged(ctx, invocation)
	if result != nil {
		return *result
	}
	status, err := store.Status(ctx)
	if err != nil {
		return managedFailure(err, "inspect managed status")
	}
	return cli.Result{Outcome: cli.OutcomeOK, Value: status}
}

func runDiff(ctx context.Context, invocation *cli.Invocation) cli.Result {
	store, result := openManaged(ctx, invocation)
	if result != nil {
		return *result
	}
	var diff *managedread.DiffResult
	var err error
	switch {
	case invocation.Options.Has("staged"):
		diff, err = store.DiffStaged(ctx)
	case len(invocation.Arguments) == 0:
		diff, err = store.DiffWorking(ctx)
	case len(invocation.Arguments) == 1:
		diff, err = store.Diff(ctx, managedread.RevisionSelector(invocation.Arguments[0]), managedread.WorkingSelector())
	case len(invocation.Arguments) == 2:
		diff, err = store.Diff(ctx, managedread.RevisionSelector(invocation.Arguments[0]), managedread.RevisionSelector(invocation.Arguments[1]))
	default:
		return commandError(cli.ErrorInternal, "diff invocation has invalid arguments")
	}
	if err != nil {
		return managedFailure(err, "compare managed states")
	}
	mode := managedread.DiffTextContent
	if invocation.Options.Has("stat") {
		mode = managedread.DiffTextStat
	} else if invocation.Options.Has("name-only") {
		mode = managedread.DiffTextNames
	}
	return cli.Result{
		Outcome: cli.OutcomeOK,
		Value:   diff,
		Text: cli.TextRendererFunc(func(output io.Writer) error {
			return managedread.WriteDiffText(output, diff, mode)
		}),
	}
}

func runLog(ctx context.Context, invocation *cli.Invocation) cli.Result {
	store, result := openManaged(ctx, invocation)
	if result != nil {
		return *result
	}
	count := int(^uint(0) >> 1)
	if count > 2147483647 {
		count = 2147483647
	}
	if value, present := invocation.Options.One("count"); present {
		parsed, err := strconv.ParseInt(value, 10, 32)
		if err != nil {
			return commandError(cli.ErrorInternal, "validated log count cannot be parsed")
		}
		count = int(parsed)
	}
	log, err := store.Log(ctx, count)
	if err != nil {
		return managedFailure(err, "read accepted log")
	}
	outcome := cli.OutcomeOK
	if log.MergeBoundary != nil {
		outcome = cli.OutcomeIssues
	}
	mode := managedread.LogTextFull
	if invocation.Options.Has("oneline") {
		mode = managedread.LogTextOneline
	}
	return cli.Result{
		Outcome: outcome,
		Value:   log,
		Text: cli.TextRendererFunc(func(output io.Writer) error {
			return managedread.WriteLogText(output, log, mode)
		}),
	}
}

func openManaged(ctx context.Context, invocation *cli.Invocation) (*managedread.Store, *cli.Result) {
	if result := cancellation(ctx); result != nil {
		return nil, result
	}
	root, err := selectedStore(invocation)
	if err != nil {
		result := failure(err, cli.ErrorRepository, "select store")
		return nil, &result
	}
	store, err := managedread.Open(ctx, root)
	if err != nil {
		result := managedFailure(err, "open managed store")
		return nil, &result
	}
	return store, nil
}

func managedFailure(err error, action string) cli.Result {
	if err == nil {
		return commandError(cli.ErrorInternal, action+": unknown failure")
	}
	kind := cli.ErrorRepository
	var raw *gitraw.Error
	var revision *managedread.RevisionError
	var index *managedread.IndexError
	var boundary *managedread.BoundaryError
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		kind = cli.ErrorCancelled
	case errors.Is(err, managedread.ErrConcurrent):
		kind = cli.ErrorConcurrency
	case errors.As(err, &raw):
		switch raw.Kind {
		case gitraw.FailureCapability, gitraw.FailureMissing:
			kind = cli.ErrorCapability
		case gitraw.FailureIO:
			kind = cli.ErrorIO
		default:
			kind = cli.ErrorRepository
		}
	case errors.As(err, &revision):
		kind = cli.ErrorUsage
	case errors.As(err, &index), errors.As(err, &boundary):
		kind = cli.ErrorRepository
	}
	return commandError(kind, fmt.Sprintf("%s: %v", action, err))
}
