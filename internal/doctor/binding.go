package doctor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"

	"github.com/ontopix/engram/internal/lifecycle"
	"github.com/ontopix/engram/internal/rendezvous"
)

type recoveryFileProof struct {
	base     string
	path     string
	raw      []byte
	owner    rendezvous.Owner
	hasOwner bool
}

type recoveryApproval struct {
	binding  RecoveryBinding
	files    []recoveryFileProof
	pullRefs []string
}

type recoveryProof struct {
	approval recoveryApproval
}

func lifecycleRecoveryApproval(observation lifecycleObservation) (recoveryApproval, error) {
	controller := RecoveryController(observation.state.Operation)
	closed, err := lifecycle.ObserveRecovery(observation.state.Target, lifecycle.Operation(observation.state.Operation))
	if err != nil {
		return recoveryApproval{}, err
	}
	if !bytes.Equal(closed.StateRaw, observation.raw) || closed.State.Owner != observation.state.Owner ||
		string(closed.State.Operation) != observation.state.Operation || closed.State.Target != observation.state.Target ||
		string(closed.State.Phase) != string(observation.state.Phase) {
		return recoveryApproval{}, lifecycle.ErrChanged
	}
	files := []recoveryFileProof{{
		base: filepath.Dir(observation.path), path: observation.path,
		raw: append([]byte(nil), observation.raw...), owner: observation.state.Owner, hasOwner: true,
	}}
	if closed.PlanPresent {
		files = append(files, recoveryFileProof{
			base: filepath.Dir(closed.PlanPath), path: closed.PlanPath,
			raw: append([]byte(nil), closed.PlanRaw...),
		})
	}
	return recoveryApproval{
		binding: RecoveryBinding{
			Controller: controller, OwnerToken: observation.state.Owner.Token,
			StateSHA256: closed.Expectation.StateSHA256,
		},
		files: files,
	}, nil
}

func managedRecoveryApproval(ownerToken, journalPath string, raw []byte, locks []lockObservation) recoveryApproval {
	files := []recoveryFileProof{{
		base: filepath.Dir(filepath.Dir(filepath.Dir(journalPath))), path: journalPath,
		raw: append([]byte(nil), raw...),
	}}
	files = append(files, lockProofs(locks)...)
	return recoveryApproval{
		binding: RecoveryBinding{Controller: RecoveryManagedWrite, OwnerToken: ownerToken, StateSHA256: stateDigest(raw)},
		files:   files,
	}
}

func lockProofs(locks []lockObservation) []recoveryFileProof {
	result := make([]recoveryFileProof, len(locks))
	for index, lock := range locks {
		result[index] = recoveryFileProof{
			base: lock.base, path: lock.path, raw: append([]byte(nil), lock.raw...),
			owner: lock.owner, hasOwner: true,
		}
	}
	return result
}

func stateDigest(raw []byte) string {
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

func newRecoveryRequest(current inspection) (RecoveryRequest, bool) {
	if !current.recoveryPlan.needed || !current.recoveryPlan.safe || len(current.recoveryPlan.approvals) != 1 {
		return RecoveryRequest{}, false
	}
	approval := cloneRecoveryApproval(current.recoveryPlan.approvals[0])
	return RecoveryRequest{
		Target: current.target, Repository: current.repository, Binding: approval.binding,
		proof: &recoveryProof{approval: approval},
	}, true
}

// Revalidate repeats the closed recovery inspection and requires the same
// controller, owner, canonical state bytes, and rendezvous records. It also
// rejects any newly appeared competing controller before mutation starts.
func (request RecoveryRequest) Revalidate(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return fail(FailureCancelled, "revalidate doctor recovery approval", err)
	}
	if request.proof == nil || request.Target == "" || request.Binding != request.proof.approval.binding || !validRecoveryBinding(request.Binding) {
		return fail(FailureOperational, "revalidate doctor recovery approval", errors.New("recovery request lacks an authentic closed approval"))
	}
	current, err := inspectOnce(ctx, request.Target, true)
	if err != nil {
		return err
	}
	if !current.recoveryPlan.needed || !current.recoveryPlan.safe || len(current.recoveryPlan.approvals) != 1 ||
		!sameRecoveryApproval(request.proof.approval, current.recoveryPlan.approvals[0]) {
		return fail(FailureConcurrency, "revalidate doctor recovery approval", errors.New("approved controller state changed or became ambiguous"))
	}
	return nil
}

func validRecoveryBinding(binding RecoveryBinding) bool {
	switch binding.Controller {
	case RecoveryInitialization, RecoveryAcquisition, RecoverySynchronization, RecoveryManagedWrite:
	default:
		return false
	}
	if len(binding.OwnerToken) != 64 || len(binding.StateSHA256) != 64 {
		return false
	}
	for _, value := range []string{binding.OwnerToken, binding.StateSHA256} {
		for _, character := range value {
			if character < '0' || character > '9' && character < 'a' || character > 'f' {
				return false
			}
		}
	}
	return true
}

func sameRecoveryApproval(left, right recoveryApproval) bool {
	if left.binding != right.binding || len(left.files) != len(right.files) || len(left.pullRefs) != len(right.pullRefs) {
		return false
	}
	for index := range left.pullRefs {
		if left.pullRefs[index] != right.pullRefs[index] {
			return false
		}
	}
	for index := range left.files {
		leftFile, rightFile := left.files[index], right.files[index]
		if leftFile.base != rightFile.base || leftFile.path != rightFile.path || leftFile.owner != rightFile.owner ||
			leftFile.hasOwner != rightFile.hasOwner || !bytes.Equal(leftFile.raw, rightFile.raw) {
			return false
		}
	}
	return true
}

func cloneRecoveryApproval(value recoveryApproval) recoveryApproval {
	result := recoveryApproval{binding: value.binding, pullRefs: append([]string(nil), value.pullRefs...)}
	result.files = make([]recoveryFileProof, len(value.files))
	for index, file := range value.files {
		result.files[index] = file
		result.files[index].raw = append([]byte(nil), file.raw...)
	}
	return result
}
