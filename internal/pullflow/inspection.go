package pullflow

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	Disposition    RecoveryDisposition
	OwnerToken     string
	StateSHA256    string
	RefNames       []string
	CleanupOnly    bool
	ControllerPath string
	ControllerRaw  []byte
	Detail         string
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
		return inspectReplayTerminalRecovery(ctx, repository, absent)
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
	digest := sha256.Sum256(raw)
	inspection := RecoveryInspection{
		OwnerToken: record.OwnerToken, StateSHA256: hex.EncodeToString(digest[:]), RefNames: refs,
		ControllerPath: transitionPath(repository), ControllerRaw: append([]byte(nil), raw...),
	}
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

func inspectReplayTerminalRecovery(ctx context.Context, repository *gitraw.Repository, absent RecoveryInspection) (RecoveryInspection, error) {
	record, raw, present, err := readReplayTerminal(repository)
	if err != nil {
		return inconsistentInspection(nil, "cannot read pull replay terminal state: "+err.Error()), nil
	}
	if !present {
		return inspectReplayPairRecovery(ctx, repository, absent)
	}
	refs := make([]string, len(record.Refs))
	for index, update := range record.Refs {
		refs[index] = update.Ref
	}
	digest := sha256.Sum256(raw)
	inspection := RecoveryInspection{
		OwnerToken: record.Owner.Token, StateSHA256: hex.EncodeToString(digest[:]), RefNames: refs,
		CleanupOnly: true, ControllerPath: replayTerminalPath(repository), ControllerRaw: append([]byte(nil), raw...),
	}
	if _, active := activeReplayTerminals.Load(record.Owner.Token); active {
		inspection.Disposition = RecoveryActive
		inspection.Detail = "coherent live replay terminal operation"
		return inspection, nil
	}
	dead, err := ownerIsDead(record.Owner)
	if err != nil {
		inspection.Disposition = RecoveryInconsistent
		inspection.Detail = "replay terminal owner liveness cannot be disproved: " + err.Error()
		return inspection, nil
	}
	if !dead {
		inspection.Disposition = RecoveryActive
		inspection.Detail = "coherent live replay terminal operation"
		return inspection, nil
	}
	stage, _, err := observeReplayTerminal(ctx, repository, record)
	if err != nil {
		inspection.Disposition = RecoveryInconsistent
		inspection.Detail = "replay terminal state is not exact: " + err.Error()
		return inspection, nil
	}
	if stage != replayTerminalAfter {
		inspection.Disposition = RecoveryInconsistent
		inspection.Detail = "replay terminal intent is recorded before its local transition; resume the matching explicit pull action"
		return inspection, nil
	}
	inspection.Disposition = RecoveryRecoverable
	inspection.Detail = "recognized terminal replay requires bounded cleanup"
	return inspection, nil
}

func inspectReplayPairRecovery(ctx context.Context, repository *gitraw.Repository, absent RecoveryInspection) (RecoveryInspection, error) {
	record, raw, present, err := readReplayPairJournal(repository)
	if err != nil {
		return inconsistentInspection(nil, "cannot read pull replay pair journal: "+err.Error()), nil
	}
	if !present {
		return absent, nil
	}
	digest := sha256.Sum256(raw)
	inspection := RecoveryInspection{
		OwnerToken: record.Owner.Token, StateSHA256: hex.EncodeToString(digest[:]), RefNames: []string{},
		CleanupOnly: true, ControllerPath: replayPairJournalPath(repository), ControllerRaw: append([]byte(nil), raw...),
	}
	if _, active := activeReplayPairs.Load(record.Owner.Token); active {
		inspection.Disposition = RecoveryActive
		inspection.Detail = "coherent live replay pair publication"
		return inspection, nil
	}
	dead, err := ownerIsDead(record.Owner)
	if err != nil {
		inspection.Disposition = RecoveryInconsistent
		inspection.Detail = "replay pair owner liveness cannot be disproved: " + err.Error()
		return inspection, nil
	}
	if !dead {
		inspection.Disposition = RecoveryActive
		inspection.Detail = "coherent live replay pair publication"
		return inspection, nil
	}
	stage, err := observeReplayPairFiles(repository, record)
	if err != nil || stage == replayPairInconsistent {
		inspection.Disposition = RecoveryInconsistent
		inspection.Detail = "replay pair images are not exact: " + errorText(err)
		return inspection, nil
	}
	if record.Operation == replayPairPublish {
		if _, transitionPresent, transitionErr := readControllerFile(transitionPath(repository)); transitionErr != nil || transitionPresent {
			inspection.Disposition = RecoveryInconsistent
			inspection.Detail = "replay publication overlaps a local transition journal"
			return inspection, nil
		}
		publication, _, publicationErr := observeReplayPublication(ctx, repository, record)
		if publicationErr != nil || publication == replayPublishInconsistent || publication == replayPublishActivated && stage != replayPairAfter {
			inspection.Disposition = RecoveryInconsistent
			inspection.Detail = "replay publication is not exact: " + errorText(publicationErr)
			return inspection, nil
		}
	} else if stage != replayPairBefore {
		current, discoverErr := gitraw.Discover(ctx, repository.Root)
		if discoverErr != nil || !sameTopology(repository, current) || validateReplayPair(current, record.AfterState, record.AfterPlan) != nil {
			inspection.Disposition = RecoveryInconsistent
			inspection.Detail = "replay pair update does not match the repository"
			return inspection, nil
		}
	}
	inspection.Disposition = RecoveryRecoverable
	inspection.Detail = "recognized replay pair journal requires bounded recovery"
	return inspection, nil
}

func errorText(err error) string {
	if err == nil {
		return "unexpected controller state"
	}
	return err.Error()
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
