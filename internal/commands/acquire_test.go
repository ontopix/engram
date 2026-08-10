package commands

import (
	"errors"
	"testing"

	"github.com/ontopix/engram/internal/acquire"
	"github.com/ontopix/engram/internal/cli"
	"github.com/ontopix/engram/internal/managedread"
)

func TestAcquireRecoveryFailureCarriesClosedMutationResult(t *testing.T) {
	ref, commit := "refs/heads/main", "0123456789012345678901234567890123456789"
	result := acquireFailure(&acquire.Error{
		Kind: acquire.ErrorRecovery, Op: "publish clone", Err: errors.New("fault"),
		Mutation: &acquire.Mutation{
			Durable: true, CheckoutChanged: true, RecoveryRequired: true,
			Accepted: &managedread.GitState{Ref: &ref, Commit: &commit},
		},
	}, "clone managed store")
	if result.Outcome != cli.OutcomeError || result.Error == nil || result.Error.Kind != cli.ErrorOperational {
		t.Fatalf("result = %#v", result)
	}
	mutation, ok := result.Value.(cli.MutationResult)
	if !ok || !mutation.Durable || !mutation.CheckoutChanged || !mutation.RecoveryRequired || len(mutation.LocalRefs) != 1 || mutation.LocalRefs[0].Ref != ref || mutation.LocalRefs[0].After == nil || *mutation.LocalRefs[0].After != commit || mutation.Head == nil || mutation.Head.After.Ref == nil || mutation.Head.After.Commit == nil || mutation.Remote != nil {
		t.Fatalf("mutation = %#v", result.Value)
	}
}

func TestAcquisitionRecoveryDoesNotTurnObservedPublicationIntoANewCheckoutEffect(t *testing.T) {
	response := acquisitionRecoveryResponse(&acquire.RecoveryResult{
		Needed: true, Published: true, Durable: true, RecoveryRequired: true,
	}, nil)
	if response.CheckoutChanged || !response.Durable || !response.RecoveryRequired {
		t.Fatalf("recovery response = %#v", response)
	}
}
