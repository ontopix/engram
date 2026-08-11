package commands

import (
	"context"
	"errors"
	"fmt"

	"github.com/ontopix/engram/internal/acquire"
	"github.com/ontopix/engram/internal/cli"
	"github.com/ontopix/engram/internal/doctor"
	"github.com/ontopix/engram/internal/gitraw"
	"github.com/ontopix/engram/internal/hookexec"
	"github.com/ontopix/engram/internal/hooks"
	"github.com/ontopix/engram/internal/initialize"
	"github.com/ontopix/engram/internal/lifecycle"
	"github.com/ontopix/engram/internal/managedread"
	"github.com/ontopix/engram/internal/managedwrite"
	"github.com/ontopix/engram/internal/pullflow"
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
	case doctor.FailureTrust:
		kind = cli.ErrorTrust
	case doctor.FailureHook:
		kind = cli.ErrorHook
	case doctor.FailureNetwork:
		kind = cli.ErrorNetwork
	case doctor.FailureConflict:
		kind = cli.ErrorConflict
	case doctor.FailureIntegration:
		kind = cli.ErrorIntegration
	case doctor.FailureOperational:
		kind = cli.ErrorOperational
	}
	protocolError := &cli.ProtocolError{Kind: kind, Message: fmt.Sprintf("%s: %v", action, err)}
	if mutation, present := doctor.MutationOf(err); present {
		result := cli.NewMutationResult()
		result.Durable = mutation.Durable
		for _, update := range mutation.LocalRefs {
			result.LocalRefs = append(result.LocalRefs, cli.RefMutation{
				Ref: update.Ref, Before: cloneStringForCommand(update.Before), After: cloneStringForCommand(update.After),
			})
		}
		if mutation.Head != nil {
			result.Head = &cli.HeadMutation{
				Before: cli.MutationGitState{
					Ref: cloneStringForCommand(mutation.Head.Before.Ref), Commit: cloneStringForCommand(mutation.Head.Before.Commit),
				},
				After: cli.MutationGitState{
					Ref: cloneStringForCommand(mutation.Head.After.Ref), Commit: cloneStringForCommand(mutation.Head.After.Commit),
				},
			}
		}
		result.CheckoutChanged = mutation.CheckoutChanged
		result.RecoveryRequired = mutation.RecoveryRequired
		return cli.Result{Outcome: cli.OutcomeError, Value: result, Error: protocolError}
	}
	return cli.Result{Outcome: cli.OutcomeError, Error: protocolError}
}

type referenceRecovery struct {
	writer *managedwrite.Engine
	puller *pullflow.Puller
}

func newReferenceRecovery(registryPath string) (doctor.RecoveryAdapter, error) {
	registry, err := hooks.NewRegistry(registryPath)
	if err != nil {
		return nil, err
	}
	writer := managedwrite.New(hookexec.New(registry))
	return &referenceRecovery{writer: writer, puller: pullflow.New(writer)}, nil
}

// Recover routes one doctor-approved state to exactly one network-silent
// controller. Doctor has already recognized the state and proved owner death;
// this adapter re-reads that evidence so a raced or ambiguous state is never
// guessed into an operation.
func (r *referenceRecovery) Recover(ctx context.Context, request doctor.RecoveryRequest) (doctor.RecoveryResponse, error) {
	if r == nil || r.writer == nil || r.puller == nil {
		err := errors.New("reference recovery controllers are unavailable")
		return doctor.RecoveryResponse{Failure: doctor.FailureCapability, RecoveryRequired: true}, err
	}
	if err := request.Revalidate(ctx); err != nil {
		return doctor.RecoveryResponse{Failure: recoveryFailureKind(err), RecoveryRequired: true}, err
	}
	expectedLifecycle := lifecycle.RecoveryExpectation{
		OwnerToken: request.Binding.OwnerToken, StateSHA256: request.Binding.StateSHA256,
	}
	switch request.Binding.Controller {
	case doctor.RecoveryInitialization:
		result, recoverErr := initialize.RecoverExpected(ctx, request.Target, expectedLifecycle)
		return initializationRecoveryResponse(result, recoverErr), recoverErr
	case doctor.RecoveryAcquisition:
		result, recoverErr := acquire.RecoverExpected(ctx, request.Target, expectedLifecycle)
		return acquisitionRecoveryResponse(result, recoverErr), recoverErr
	case doctor.RecoverySynchronization:
		result, recoverErr := r.puller.RecoverExpected(ctx, request.Target, pullflow.RecoveryExpectation{
			OwnerToken: request.Binding.OwnerToken, StateSHA256: request.Binding.StateSHA256,
		})
		response := pullRecoveryResponse(result, recoverErr)
		if recoverErr == nil {
			response.Accepted, recoverErr = currentAcceptedState(ctx, request.Target)
			if recoverErr != nil {
				response.Failure = recoveryFailureKind(recoverErr)
			}
		}
		return response, recoverErr
	case doctor.RecoveryManagedWrite:
		result, recoverErr := r.writer.RecoverExpected(ctx, request.Target, managedwrite.RecoveryExpectation{
			OwnerToken: request.Binding.OwnerToken, StateSHA256: request.Binding.StateSHA256,
		})
		response := managedRecoveryResponse(result, recoverErr)
		if recoverErr == nil {
			response.Accepted, recoverErr = currentAcceptedState(ctx, request.Target)
			if recoverErr != nil {
				response.Failure = recoveryFailureKind(recoverErr)
			}
		}
		return response, recoverErr
	default:
		err := fmt.Errorf("unsupported recovery controller %q", request.Binding.Controller)
		return doctor.RecoveryResponse{Failure: doctor.FailureOperational, RecoveryRequired: true}, err
	}
}

func initializationRecoveryResponse(result initialize.RecoveryResult, err error) doctor.RecoveryResponse {
	response := doctor.RecoveryResponse{
		Accepted: result.Accepted, Durable: result.Durable, CheckoutChanged: result.CheckoutChanged,
		RecoveryRequired: result.RecoveryRequired,
	}
	var detail *initialize.Error
	if errors.As(err, &detail) {
		response.Durable = response.Durable || detail.Durable
		response.CheckoutChanged = response.CheckoutChanged || detail.CheckoutChanged
		response.RecoveryRequired = response.RecoveryRequired || detail.RecoveryRequired
	}
	return finishRecoveryResponse(response, result.Needed, result.Performed, err)
}

func acquisitionRecoveryResponse(result *acquire.RecoveryResult, err error) doctor.RecoveryResponse {
	response := doctor.RecoveryResponse{}
	needed, performed := false, false
	if result != nil {
		needed, performed = result.Needed, result.Performed
		response.Accepted = result.Accepted
		response.Durable = result.Durable
		response.CheckoutChanged = result.CheckoutChanged
		response.RecoveryRequired = result.RecoveryRequired
	}
	if mutation, present := acquire.MutationOf(err); present {
		response.Durable = response.Durable || mutation.Durable
		response.CheckoutChanged = response.CheckoutChanged || mutation.CheckoutChanged
		response.RecoveryRequired = response.RecoveryRequired || mutation.RecoveryRequired
		if response.Accepted == nil {
			response.Accepted = mutation.Accepted
		}
	}
	return finishRecoveryResponse(response, needed, performed, err)
}

func managedRecoveryResponse(result *managedwrite.RecoveryResult, err error) doctor.RecoveryResponse {
	response := doctor.RecoveryResponse{}
	needed, performed := false, false
	if result != nil {
		needed, performed = result.Needed, result.Performed
		response.Durable = result.Durable
		response.CheckoutChanged = result.CheckoutChanged
		response.RecoveryRequired = result.RecoveryRequired
	}
	return finishRecoveryResponse(response, needed, performed, err)
}

func pullRecoveryResponse(result *pullflow.RecoveryResult, err error) doctor.RecoveryResponse {
	response := doctor.RecoveryResponse{}
	needed, performed := false, false
	if result != nil {
		needed, performed = result.Needed, result.Performed
		response.Durable = result.Durable
		response.CheckoutChanged = result.CheckoutChanged
		response.RecoveryRequired = result.RecoveryRequired
		if result.Mutation != nil {
			mergePullRecoveryMutation(&response, result.Mutation)
		}
	}
	if mutation := pullflow.MutationOf(err); mutation != nil {
		mergePullRecoveryMutation(&response, mutation)
	}
	return finishRecoveryResponse(response, needed, performed, err)
}

func mergePullRecoveryMutation(response *doctor.RecoveryResponse, mutation *pullflow.Mutation) {
	if response == nil || mutation == nil {
		return
	}
	response.Durable = response.Durable || mutation.Durable
	response.CheckoutChanged = response.CheckoutChanged || mutation.CheckoutChanged
	response.RecoveryRequired = response.RecoveryRequired || mutation.RecoveryRequired
	for _, update := range mutation.LocalRefs {
		candidate := doctor.RefMutation{
			Ref: update.Ref, Before: cloneStringForCommand(update.Before), After: cloneStringForCommand(update.After),
		}
		duplicate := false
		for _, existing := range response.LocalRefs {
			if existing.Ref == candidate.Ref && equalOptionalString(existing.Before, candidate.Before) && equalOptionalString(existing.After, candidate.After) {
				duplicate = true
				break
			}
		}
		if !duplicate {
			response.LocalRefs = append(response.LocalRefs, candidate)
		}
	}
	if response.Head == nil && mutation.Head != nil {
		response.Head = &doctor.HeadMutation{
			Before: cloneDoctorGitState(mutation.Head.Before),
			After:  cloneDoctorGitState(mutation.Head.After),
		}
	}
}

func cloneDoctorGitState(state managedread.GitState) managedread.GitState {
	return managedread.GitState{
		Ref: cloneStringForCommand(state.Ref), Commit: cloneStringForCommand(state.Commit),
	}
}

func equalOptionalString(left, right *string) bool {
	if left == nil || right == nil {
		return left == right
	}
	return *left == *right
}

func finishRecoveryResponse(response doctor.RecoveryResponse, needed, performed bool, err error) doctor.RecoveryResponse {
	if err == nil {
		return response
	}
	response.Failure = recoveryFailureKind(err)
	response.RecoveryRequired = response.RecoveryRequired || needed && !performed
	return response
}

func recoveryFailureKind(err error) doctor.ErrorKind {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return doctor.FailureCancelled
	}
	if kind := doctor.KindOf(err); kind != "" {
		return kind
	}
	if kind := initialize.KindOf(err); kind != "" {
		switch kind {
		case initialize.ErrorCancelled:
			return doctor.FailureCancelled
		case initialize.ErrorCapability:
			return doctor.FailureCapability
		case initialize.ErrorConflict:
			return doctor.FailureConflict
		case initialize.ErrorConcurrency:
			return doctor.FailureConcurrency
		case initialize.ErrorIntegration:
			return doctor.FailureIntegration
		case initialize.ErrorRepository:
			return doctor.FailureRepository
		case initialize.ErrorIO:
			return doctor.FailureIO
		}
	}
	if kind := acquire.KindOf(err); kind != "" {
		switch kind {
		case acquire.ErrorCancelled:
			return doctor.FailureCancelled
		case acquire.ErrorCapability:
			return doctor.FailureCapability
		case acquire.ErrorNetwork:
			return doctor.FailureNetwork
		case acquire.ErrorConflict:
			return doctor.FailureConflict
		case acquire.ErrorConcurrency:
			return doctor.FailureConcurrency
		case acquire.ErrorIntegration:
			return doctor.FailureIntegration
		case acquire.ErrorRepository:
			return doctor.FailureRepository
		case acquire.ErrorIO:
			return doctor.FailureIO
		}
	}
	if kind := pullflow.KindOf(err); kind != "" {
		switch kind {
		case pullflow.ErrorCancelled:
			return doctor.FailureCancelled
		case pullflow.ErrorRepository:
			return doctor.FailureRepository
		case pullflow.ErrorCapability:
			return doctor.FailureCapability
		case pullflow.ErrorTrust:
			return doctor.FailureTrust
		case pullflow.ErrorHook:
			return doctor.FailureHook
		case pullflow.ErrorNetwork:
			return doctor.FailureNetwork
		case pullflow.ErrorConflict:
			return doctor.FailureConflict
		case pullflow.ErrorConcurrency:
			return doctor.FailureConcurrency
		case pullflow.ErrorIntegration:
			return doctor.FailureIntegration
		case pullflow.ErrorIO:
			return doctor.FailureIO
		}
	}
	if kind := managedwrite.KindOf(err); kind != "" {
		switch kind {
		case managedwrite.FailureRepository, managedwrite.FailureValidation:
			return doctor.FailureRepository
		case managedwrite.FailureCapability:
			return doctor.FailureCapability
		case managedwrite.FailureTrust:
			return doctor.FailureTrust
		case managedwrite.FailureHook:
			return doctor.FailureHook
		case managedwrite.FailureGuard:
			return doctor.FailureIntegration
		case managedwrite.FailureConcurrency:
			return doctor.FailureConcurrency
		case managedwrite.FailureRecovery:
			return doctor.FailureConflict
		case managedwrite.FailureIO:
			return doctor.FailureIO
		}
	}
	return doctor.FailureOperational
}

func currentAcceptedState(ctx context.Context, target string) (*managedread.GitState, error) {
	repository, err := gitraw.Discover(ctx, target)
	if err != nil {
		return nil, err
	}
	state := &managedread.GitState{}
	if repository.HeadRef != "" {
		ref := repository.HeadRef
		state.Ref = &ref
	}
	if repository.Head != nil {
		commit := repository.Head.String()
		state.Commit = &commit
	}
	return state, nil
}
