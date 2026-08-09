package pullflow

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"github.com/ontopix/engram/internal/gitraw"
	"github.com/ontopix/engram/internal/rendezvous"
)

// RecoveryDisposition is the closed read-only classification of pullflow's
// implementation-private transition journal. Consumers never parse that
// journal themselves.
type RecoveryDisposition string

const (
	RecoveryAbsent       RecoveryDisposition = "absent"
	RecoveryActive       RecoveryDisposition = "active"
	RecoveryRecoverable  RecoveryDisposition = "recoverable"
	RecoveryInconsistent RecoveryDisposition = "inconsistent"
)

// RecoveryInspection contains only the rendezvous identity doctor needs to
// correlate the recognized journal with annex-defined locks. RefNames is
// sorted, unique, and non-nil. Detail is diagnostic text, not protocol state.
type RecoveryInspection struct {
	Disposition RecoveryDisposition
	OwnerToken  string
	RefNames    []string
	Detail      string
}

// InspectRecovery recognizes, structurally validates, binds, and checks owner
// liveness for exactly one pull transition journal without changing state.
func InspectRecovery(ctx context.Context, repository *gitraw.Repository) (RecoveryInspection, error) {
	absent := RecoveryInspection{Disposition: RecoveryAbsent, RefNames: []string{}}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return absent, err
	}
	if repository == nil {
		return inconsistentInspection(nil, "managed repository is unavailable"), nil
	}
	raw, present, err := readControllerFile(transitionPath(repository))
	if err != nil {
		return inconsistentInspection(nil, "cannot read pull transition journal: "+err.Error()), nil
	}
	if !present {
		return absent, nil
	}
	var record transitionRecord
	if err := decodeCanonical(raw, &record); err != nil {
		return inconsistentInspection(nil, "cannot decode pull transition journal: "+err.Error()), nil
	}
	if err := validateTransition(record); err != nil {
		return inconsistentInspection(nil, "unsupported pull transition journal: "+err.Error()), nil
	}
	refs := make([]string, len(record.Refs))
	for index, update := range record.Refs {
		refs[index] = update.Ref
	}
	if record.ObjectFormat != repository.Format {
		return inconsistentInspection(refs, "pull transition object format does not match the repository"), nil
	}

	paths := make([]string, 0, len(refs)+1)
	for _, ref := range refs {
		paths = append(paths, rendezvous.RefPath(repository.CommonGitDir, ref))
	}
	paths = append(paths, rendezvous.WorktreePath(repository.GitDir))
	var owner rendezvous.Owner
	for _, name := range paths {
		observed, err := inspectTransitionOwner(name)
		if err != nil {
			inspection := inconsistentInspection(refs, fmt.Sprintf("pull transition lacks exact lock %q: %v", name, err))
			inspection.OwnerToken = record.OwnerToken
			return inspection, nil
		}
		if owner.Version == 0 {
			owner = observed
		} else if observed != owner {
			inspection := inconsistentInspection(refs, "pull transition locks have inconsistent owner metadata")
			inspection.OwnerToken = record.OwnerToken
			return inspection, nil
		}
	}
	if owner.Token != record.OwnerToken {
		inspection := inconsistentInspection(refs, "pull transition journal and locks have different owner tokens")
		inspection.OwnerToken = record.OwnerToken
		return inspection, nil
	}
	if owner.Phase != rendezvous.JournalRequired && !(record.Phase == transitionPrepared && owner.Phase == rendezvous.PreJournal) {
		inspection := inconsistentInspection(refs, "pull transition journal is incompatible with its lock phase")
		inspection.OwnerToken = record.OwnerToken
		return inspection, nil
	}
	inspection := RecoveryInspection{OwnerToken: record.OwnerToken, RefNames: refs}
	if _, active := activeTransitionTokens.Load(record.OwnerToken); active {
		inspection.Disposition = RecoveryActive
		inspection.Detail = "coherent live pull transition"
		return inspection, nil
	}
	dead, err := ownerIsDead(owner)
	if err != nil {
		inspection.Disposition = RecoveryInconsistent
		inspection.Detail = "pull transition owner liveness cannot be disproved: " + err.Error()
		return inspection, nil
	}
	if !dead {
		inspection.Disposition = RecoveryActive
		inspection.Detail = "coherent live pull transition"
		return inspection, nil
	}
	inspection.Disposition = RecoveryRecoverable
	inspection.Detail = "recognized stale pull transition requires bounded recovery"
	return inspection, nil
}

func inspectTransitionOwner(name string) (rendezvous.Owner, error) {
	first, present, err := readControllerFile(name)
	if err != nil || !present {
		if err == nil {
			err = errors.New("lock is absent")
		}
		return rendezvous.Owner{}, err
	}
	owner, err := rendezvous.Read(name)
	if err != nil {
		return rendezvous.Owner{}, err
	}
	second, present, err := readControllerFile(name)
	if err != nil || !present || !bytes.Equal(first, second) {
		if err == nil {
			err = errors.New("lock changed during inspection")
		}
		return rendezvous.Owner{}, err
	}
	return owner, nil
}

func inconsistentInspection(refs []string, detail string) RecoveryInspection {
	copyRefs := make([]string, len(refs))
	copy(copyRefs, refs)
	return RecoveryInspection{Disposition: RecoveryInconsistent, RefNames: copyRefs, Detail: detail}
}
