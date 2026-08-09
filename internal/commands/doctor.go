package commands

import (
	"context"
	"fmt"

	"github.com/ontopix/engram/internal/cli"
	"github.com/ontopix/engram/internal/doctor"
)

// RegisterDoctor installs the read-only diagnostic flow and the built-in
// stale pre-journal cleanup. Journaled transaction recovery remains disabled
// until a managed transaction engine is supplied explicitly.
func RegisterDoctor(app *cli.App) {
	RegisterDoctorWithRecovery(app, nil)
}

// RegisterDoctorWithRecovery is the composition point for the managed-write
// and synchronization engines. Doctor invokes recovery only after recognizing
// CLI state and proving every participating owner dead.
func RegisterDoctorWithRecovery(app *cli.App, recovery doctor.RecoveryAdapter) {
	if app == nil {
		return
	}
	if app.Handlers == nil {
		app.Handlers = make(map[cli.CommandName]cli.Handler)
	}
	app.Handlers[cli.CommandDoctor] = cli.HandlerFunc(func(ctx context.Context, invocation *cli.Invocation) cli.Result {
		return runDoctor(ctx, invocation, recovery)
	})
}

func runDoctor(ctx context.Context, invocation *cli.Invocation, recovery doctor.RecoveryAdapter) cli.Result {
	if invocation == nil || len(invocation.Arguments) > 1 {
		return commandError(cli.ErrorInternal, "doctor invocation has invalid arguments")
	}
	var target string
	var err error
	if len(invocation.Arguments) == 1 {
		target = invocation.Arguments[0]
		if target == "" {
			return commandError(cli.ErrorUsage, "doctor PATH must not be empty")
		}
	} else {
		target, err = selectedStore(invocation)
		if err != nil {
			return failure(err, cli.ErrorRepository, "select doctor target")
		}
	}
	result, err := doctor.Inspect(ctx, target, doctor.Options{
		Recover:  invocation.Options.Has("recover"),
		Recovery: recovery,
	})
	if err != nil {
		return doctorFailure(err, "inspect managed integration")
	}
	if result.HasRequiredErrors() {
		return cli.Result{Outcome: cli.OutcomeIssues, Value: result}
	}
	return cli.Result{Outcome: cli.OutcomeOK, Value: result}
}

func doctorFailure(err error, action string) cli.Result {
	kind := cli.ErrorOperational
	switch doctor.KindOf(err) {
	case doctor.FailureCancelled:
		kind = cli.ErrorCancelled
	case doctor.FailureCapability:
		kind = cli.ErrorCapability
	case doctor.FailureConcurrency:
		kind = cli.ErrorConcurrency
	case doctor.FailureRepository:
		kind = cli.ErrorRepository
	case doctor.FailureIO:
		kind = cli.ErrorIO
	case doctor.FailureOperational:
		kind = cli.ErrorOperational
	}
	protocolError := &cli.ProtocolError{Kind: kind, Message: fmt.Sprintf("%s: %v", action, err)}
	if mutation, present := doctor.MutationOf(err); present {
		result := cli.NewMutationResult()
		result.Durable = mutation.Durable
		result.RecoveryRequired = mutation.RecoveryRequired
		return cli.Result{Outcome: cli.OutcomeError, Value: result, Error: protocolError}
	}
	return cli.Result{Outcome: cli.OutcomeError, Error: protocolError}
}
