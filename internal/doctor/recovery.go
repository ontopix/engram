package doctor

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ontopix/engram/internal/gitraw"
	"github.com/ontopix/engram/internal/journal"
	"github.com/ontopix/engram/internal/pullflow"
	"github.com/ontopix/engram/internal/rendezvous"
)

type lockObservation struct {
	path  string
	base  string
	owner rendezvous.Owner
	raw   []byte
	alive ownerCondition
}

type recoveryPlan struct {
	needed         bool
	safe           bool
	blocked        bool
	preJournalOnly bool
	worktreeGitDir string
	localCleanup   bool
	locks          []lockObservation
	lifecycle      []lifecycleObservation
	approvals      []recoveryApproval
}

func pullRecoveryApproval(inspection pullflow.RecoveryInspection, locks []lockObservation) recoveryApproval {
	approval := recoveryApproval{
		binding: RecoveryBinding{
			Controller: RecoverySynchronization, OwnerToken: inspection.OwnerToken,
			StateSHA256: inspection.StateSHA256,
		},
		files:    lockProofs(locks),
		pullRefs: append([]string(nil), inspection.RefNames...),
	}
	if inspection.ControllerPath != "" && len(inspection.ControllerRaw) != 0 {
		approval.files = append([]recoveryFileProof{{
			base: filepath.Dir(inspection.ControllerPath), path: inspection.ControllerPath,
			raw: append([]byte(nil), inspection.ControllerRaw...),
		}}, approval.files...)
	}
	return approval
}

func combineRecoveryPlans(plans ...recoveryPlan) recoveryPlan {
	combined := recoveryPlan{safe: true, preJournalOnly: true}
	for _, plan := range plans {
		if plan.blocked {
			combined.safe = false
			combined.blocked = true
		}
		if !plan.needed {
			continue
		}
		combined.needed = true
		combined.safe = combined.safe && plan.safe
		combined.preJournalOnly = combined.preJournalOnly && plan.preJournalOnly
		combined.locks = append(combined.locks, plan.locks...)
		combined.localCleanup = combined.localCleanup || plan.localCleanup
		combined.lifecycle = append(combined.lifecycle, plan.lifecycle...)
		combined.approvals = append(combined.approvals, plan.approvals...)
		if plan.worktreeGitDir != "" {
			combined.worktreeGitDir = plan.worktreeGitDir
		}
	}
	if !combined.needed {
		combined.safe = false
		combined.preJournalOnly = false
	}
	if len(combined.lifecycle) != 0 {
		combined.preJournalOnly = false
	}
	if len(combined.approvals) > 1 || len(combined.approvals) != 0 && combined.localCleanup {
		combined.safe = false
	}
	return combined
}

func inspectRecoveryState(ctx context.Context, current *inspection) recoveryPlan {
	if current.repository == nil {
		setRequired(&current.result, "recovery.state", Error, nil, "managed repository is unavailable")
		return recoveryPlan{}
	}
	journalPath := journal.Path(current.repository.GitDir)
	record, rawJournal, journalPresent, journalProblem := inspectJournal(journalPath)
	pullInspection, pullErr := pullflow.InspectRecovery(ctx, current.repository)
	if pullErr != nil {
		setRequired(&current.result, "recovery.state", Error, nil, "cannot inspect pull recovery state: "+pullErr.Error())
		return recoveryPlan{needed: true}
	}
	pullPresent := pullInspection.Disposition != pullflow.RecoveryAbsent
	refnames := []string{current.repository.HeadRef}
	if journalPresent && journalProblem == "" {
		refnames = append(refnames, record.Ref.Ref)
	}
	refnames = append(refnames, pullInspection.RefNames...)
	locks, problem := inspectRendezvousLocks(current.repository.CommonGitDir, current.repository.GitDir, refnames)
	native := nativeLockPathsForRefs(current.repository, refnames)
	if len(native) != 0 {
		problem = append(problem, "unowned native Git locks are present: "+strings.Join(native, ", "))
	}
	if journalProblem != "" {
		problem = append(problem, journalProblem)
	}
	if pullInspection.Disposition == pullflow.RecoveryInconsistent {
		message := pullInspection.Detail
		if message == "" {
			message = "pull transition recovery state is inconsistent"
		}
		problem = append(problem, message)
	}

	plan := recoveryPlan{safe: true, preJournalOnly: true, worktreeGitDir: current.repository.GitDir}
	if len(problem) != 0 {
		setRequired(&current.result, "recovery.state", Error, nil, strings.Join(problem, "; "))
		plan.needed = true
		plan.safe = false
		return plan
	}
	if pullInspection.CleanupOnly && pullPresent {
		plan.preJournalOnly = false
		if journalPresent || len(locks) != 0 {
			plan.needed, plan.safe = true, false
			setRequired(&current.result, "recovery.state", Error, nil, "terminal replay cleanup overlaps another journal or rendezvous owner")
			return plan
		}
		switch pullInspection.Disposition {
		case pullflow.RecoveryActive:
			plan.blocked = true
			setRequired(&current.result, "recovery.state", Error, pathPointer(pullInspection.ControllerPath), "coherent live replay terminal operation")
		case pullflow.RecoveryRecoverable:
			plan.needed = true
			plan.approvals = append(plan.approvals, pullRecoveryApproval(pullInspection, nil))
			setRequired(&current.result, "recovery.state", Error, pathPointer(pullInspection.ControllerPath), "recognized terminal replay requires bounded cleanup")
		default:
			plan.needed, plan.safe = true, false
			setRequired(&current.result, "recovery.state", Error, pathPointer(pullInspection.ControllerPath), "terminal replay recovery state is inconsistent")
		}
		return plan
	}
	if len(locks) == 0 && !journalPresent && !pullPresent {
		return recoveryPlan{}
	}
	if len(locks) == 0 {
		path := journalPath
		if pullPresent {
			path = ""
		}
		var pointer *string
		if path != "" {
			pointer = pathPointer(path)
		}
		setRequired(&current.result, "recovery.state", Error, pointer, "recognized journal exists without its rendezvous locks")
		return recoveryPlan{needed: true}
	}

	groups := make(map[string][]lockObservation)
	for _, lock := range locks {
		groups[lock.owner.Token] = append(groups[lock.owner.Token], lock)
	}
	tokens := make([]string, 0, len(groups))
	for token := range groups {
		tokens = append(tokens, token)
	}
	sort.Strings(tokens)
	details := make([]string, 0)
	journalMatched := false
	pullMatched := false
	for _, token := range tokens {
		group := groups[token]
		owner := group[0].owner
		condition := group[0].alive
		consistent := true
		for _, lock := range group[1:] {
			if lock.owner.Phase != owner.Phase {
				consistent = false
			}
			if lock.alive != condition {
				condition = ownerUnknown
			}
		}
		if !consistent {
			plan.needed, plan.safe = true, false
			details = append(details, "one lock owner token has inconsistent phases")
			continue
		}
		hasJournal := journalPresent && record.OwnerToken == token
		hasPull := (pullInspection.Disposition == pullflow.RecoveryActive || pullInspection.Disposition == pullflow.RecoveryRecoverable) && pullInspection.OwnerToken == token
		if hasJournal && hasPull {
			plan.needed, plan.safe = true, false
			details = append(details, "one lock owner token is claimed by multiple recovery journals")
			continue
		}
		if hasPull {
			pullMatched = true
			if problem := validatePullBinding(pullInspection, current, group); problem != "" {
				plan.needed, plan.safe = true, false
				details = append(details, problem)
				continue
			}
			switch pullInspection.Disposition {
			case pullflow.RecoveryActive:
				plan.blocked = true
				details = append(details, "coherent live pull transition")
			case pullflow.RecoveryRecoverable:
				plan.needed = true
				plan.preJournalOnly = false
				plan.locks = append(plan.locks, group...)
				plan.approvals = append(plan.approvals, pullRecoveryApproval(pullInspection, group))
				details = append(details, "recognized stale pull transition requires bounded recovery")
			}
			continue
		}
		if hasJournal {
			journalMatched = true
			if owner.Phase == rendezvous.PreJournal {
				if problem := validateJournalBinding(record, current, group); problem != "" {
					plan.needed, plan.safe = true, false
					details = append(details, problem)
					continue
				}
				if condition != ownerDead {
					plan.blocked = true
					details = append(details, "coherent live pre-CAS journal publication")
					continue
				}
				// The durable pre-journal phase proves CAS was forbidden. The
				// transaction adapter may cancel and clean this exact journal; the
				// generic stale-lock cleanup must not remove its ownership signal.
				plan.needed = true
				plan.preJournalOnly = false
				plan.locks = append(plan.locks, group...)
				plan.approvals = append(plan.approvals, managedRecoveryApproval(record.OwnerToken, journalPath, rawJournal, group))
				details = append(details, "recognized stale pre-CAS journal requires bounded cleanup")
				continue
			}
			if owner.Phase != rendezvous.JournalRequired {
				plan.needed, plan.safe = true, false
				details = append(details, "journal owner locks are not journal-required")
				continue
			}
			if problem := validateJournalBinding(record, current, group); problem != "" {
				plan.needed, plan.safe = true, false
				details = append(details, problem)
				continue
			}
			if condition != ownerDead {
				plan.blocked = true
				details = append(details, "coherent live journaled operation")
				continue
			}
			plan.needed = true
			plan.preJournalOnly = false
			plan.locks = append(plan.locks, group...)
			plan.approvals = append(plan.approvals, managedRecoveryApproval(record.OwnerToken, journalPath, rawJournal, group))
			details = append(details, "recognized stale journaled operation requires bounded recovery")
			continue
		}

		switch owner.Phase {
		case rendezvous.PreJournal:
			if condition != ownerDead {
				plan.blocked = true
				message := "coherent live pre-journal operation"
				if condition == ownerUnknown {
					message = "pre-journal owner liveness cannot be disproved"
				}
				details = append(details, message)
				continue
			}
			plan.needed = true
			plan.localCleanup = true
			plan.locks = append(plan.locks, group...)
			details = append(details, "recognized stale pre-journal locks require cleanup")
		case rendezvous.JournalRequired:
			if condition != ownerDead && !groupHasPath(group, rendezvous.WorktreePath(current.repository.GitDir)) {
				// The exact accepted-ref lock can be owned by another worktree;
				// its journal is deliberately outside this target's state.
				plan.blocked = true
				details = append(details, "shared accepted ref is controlled by a live journaled operation")
				continue
			}
			plan.needed, plan.safe = true, false
			details = append(details, "journal-required locks have no recognized local journal")
		default:
			plan.needed, plan.safe = true, false
			details = append(details, "unsupported rendezvous phase")
		}
	}
	if journalPresent && !journalMatched {
		plan.needed, plan.safe = true, false
		details = append(details, "journal owner does not match any exact rendezvous lock")
	}
	if (pullInspection.Disposition == pullflow.RecoveryActive || pullInspection.Disposition == pullflow.RecoveryRecoverable) && !pullMatched {
		plan.needed, plan.safe = true, false
		details = append(details, "pull transition owner does not match every exact rendezvous lock")
	}
	if len(plan.approvals) > 1 {
		plan.safe = false
		details = append(details, "multiple recovery controllers claim the target")
	}
	if !plan.needed {
		plan.safe = false
		plan.preJournalOnly = false
		if len(details) != 0 {
			setRequired(&current.result, "recovery.state", OK, nil, strings.Join(sortedUnique(details), "; "))
		}
		return plan
	}
	setRequired(&current.result, "recovery.state", Error, nil, strings.Join(sortedUnique(details), "; "))
	return plan
}

func validatePullBinding(inspection pullflow.RecoveryInspection, current *inspection, locks []lockObservation) string {
	if current.repository == nil || inspection.OwnerToken == "" || len(inspection.RefNames) == 0 {
		return "pull transition inspection lacks its exact owner or refs"
	}
	want := make(map[string]struct{}, len(inspection.RefNames)+1)
	for _, ref := range inspection.RefNames {
		want[rendezvous.RefPath(current.repository.CommonGitDir, ref)] = struct{}{}
	}
	want[rendezvous.WorktreePath(current.repository.GitDir)] = struct{}{}
	for _, lock := range locks {
		delete(want, lock.path)
	}
	if len(want) != 0 {
		return "pull transition lacks an exact ref or worktree rendezvous lock"
	}
	return ""
}

func inspectRendezvousLocks(commonGitDir, worktreeGitDir string, refnames []string) ([]lockObservation, []string) {
	paths := make([]string, 0)
	refDirectory := filepath.Join(commonGitDir, "engram", "locks", "refs")
	if err := realDirectoryChain(commonGitDir, refDirectory); err != nil {
		return nil, []string{"controller ref-lock directory is unsafe: " + err.Error()}
	}
	for _, refname := range sortedUnique(refnames) {
		name := rendezvous.RefPath(commonGitDir, refname)
		if _, err := os.Lstat(name); err == nil {
			paths = append(paths, name)
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, []string{"cannot inspect exact controller ref lock: " + err.Error()}
		}
	}
	worktree := rendezvous.WorktreePath(worktreeGitDir)
	if err := realDirectoryChain(worktreeGitDir, filepath.Dir(worktree)); err != nil {
		return nil, []string{"controller worktree-lock directory is unsafe: " + err.Error()}
	}
	if _, err := os.Lstat(worktree); err == nil {
		paths = append(paths, worktree)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, []string{"cannot inspect controller worktree lock: " + err.Error()}
	}
	sort.Strings(paths)
	locks := make([]lockObservation, 0, len(paths))
	for _, name := range paths {
		base := commonGitDir
		if name == worktree {
			base = worktreeGitDir
		}
		raw, present, err := readStableControllerFile(base, name)
		if err != nil || !present {
			if err == nil {
				err = errors.New("lock disappeared during inspection")
			}
			return nil, []string{"cannot recognize controller lock " + name + ": " + err.Error()}
		}
		owner, err := rendezvous.Read(name)
		if err != nil || !validOwner(owner) {
			if err == nil {
				err = errors.New("invalid owner metadata")
			}
			return nil, []string{"cannot recognize controller lock " + name + ": " + err.Error()}
		}
		rawAfter, present, err := readStableControllerFile(base, name)
		if err != nil || !present || !bytes.Equal(raw, rawAfter) {
			return nil, []string{"controller lock changed during inspection"}
		}
		locks = append(locks, lockObservation{path: name, base: base, owner: owner, raw: raw, alive: ownerLiveness(owner)})
	}
	return locks, nil
}

func groupHasPath(group []lockObservation, name string) bool {
	for _, lock := range group {
		if lock.path == name {
			return true
		}
	}
	return false
}

func inspectJournal(name string) (journal.Record, []byte, bool, string) {
	base := filepath.Dir(filepath.Dir(filepath.Dir(name)))
	first, present, err := readStableControllerFile(base, name)
	if err != nil {
		return journal.Record{}, nil, true, "cannot recognize managed recovery journal: " + err.Error()
	}
	if !present {
		return journal.Record{}, nil, false, ""
	}
	record, raw, err := journal.Read(name)
	if err != nil {
		return journal.Record{}, nil, true, "cannot recognize managed recovery journal: " + err.Error()
	}
	if !bytes.Equal(first, raw) {
		return journal.Record{}, nil, true, "managed recovery journal changed during inspection"
	}
	return record, raw, true, ""
}

func validateJournalBinding(record journal.Record, current *inspection, locks []lockObservation) string {
	if current.repository == nil || !strings.HasPrefix(record.Ref.Ref, "refs/heads/") || len(record.Ref.Ref) == len("refs/heads/") {
		return "journal does not name one eligible local accepted ref"
	}
	if _, err := gitraw.ParseOID(current.repository.Format, record.Ref.After); err != nil {
		return "journal new object ID does not match repository format"
	}
	if record.Ref.Before != nil {
		if _, err := gitraw.ParseOID(current.repository.Format, *record.Ref.Before); err != nil {
			return "journal old object ID does not match repository format"
		}
	}
	wantRef := rendezvous.RefPath(current.repository.CommonGitDir, record.Ref.Ref)
	wantWorktree := rendezvous.WorktreePath(current.repository.GitDir)
	refFound, worktreeFound := false, false
	for _, lock := range locks {
		refFound = refFound || lock.path == wantRef
		worktreeFound = worktreeFound || lock.path == wantWorktree
	}
	if !refFound || !worktreeFound {
		return "journal-required state lacks its exact ref or worktree rendezvous lock"
	}
	return ""
}

func nativeLockPathsFor(gitDir, commonGitDir, headRef string) []string {
	type candidate struct{ base, path string }
	candidates := []candidate{
		{gitDir, filepath.Join(gitDir, "HEAD.lock")},
		{gitDir, filepath.Join(gitDir, "index.lock")},
		{commonGitDir, filepath.Join(commonGitDir, "packed-refs.lock")},
		{commonGitDir, filepath.Join(commonGitDir, filepath.FromSlash(headRef)+".lock")},
	}
	var present []string
	for _, candidate := range candidates {
		if err := realDirectoryChain(candidate.base, filepath.Dir(candidate.path)); err != nil {
			present = append(present, candidate.path+" (unsafe ancestor)")
			continue
		}
		if _, err := os.Lstat(candidate.path); err == nil {
			present = append(present, candidate.path)
		}
	}
	return sortedUnique(present)
}

func cleanStalePreJournal(plan recoveryPlan) (result error) {
	if !plan.needed || !plan.safe || !plan.preJournalOnly || !plan.localCleanup || plan.worktreeGitDir == "" || len(plan.locks) == 0 || len(plan.lifecycle) != 0 || len(plan.approvals) != 0 {
		return errors.New("recovery plan is not a stale pre-journal cleanup")
	}
	lease, err := rendezvous.AcquireRecovery(plan.worktreeGitDir)
	if err != nil {
		return &cleanupError{err: err}
	}
	removed := false
	defer func() {
		if releaseErr := lease.Release(); releaseErr != nil {
			result = &cleanupError{durable: removed, err: errors.Join(result, releaseErr)}
		}
	}()
	locks := append([]lockObservation(nil), plan.locks...)
	// Worktree locks are acquired last and therefore released first. The ref
	// paths were already observed in deterministic order.
	sort.Slice(locks, func(left, right int) bool {
		leftWorktree := strings.HasSuffix(locks[left].path, string(filepath.Separator)+"worktree.lock")
		rightWorktree := strings.HasSuffix(locks[right].path, string(filepath.Separator)+"worktree.lock")
		if leftWorktree != rightWorktree {
			return leftWorktree
		}
		return locks[left].path > locks[right].path
	})
	for _, lock := range locks {
		if ownerLiveness(lock.owner) != ownerDead {
			return &cleanupError{durable: removed, err: errors.New("lock owner death is no longer proven")}
		}
		current, present, err := readStableControllerFile(lock.base, lock.path)
		if err != nil || !present || !bytes.Equal(current, lock.raw) {
			return &cleanupError{durable: removed, err: errors.New("lock changed before stale cleanup")}
		}
		if err := os.Remove(lock.path); err != nil {
			return &cleanupError{durable: removed, err: err}
		}
		removed = true
	}
	return nil
}

type cleanupError struct {
	durable bool
	err     error
}

func (e *cleanupError) Error() string { return e.err.Error() }
func (e *cleanupError) Unwrap() error { return e.err }

func nativeLockPaths(currentRepository *gitraw.Repository) []string {
	if currentRepository == nil {
		return nil
	}
	return nativeLockPathsFor(currentRepository.GitDir, currentRepository.CommonGitDir, currentRepository.HeadRef)
}

func nativeLockPathsForRefs(repository *gitraw.Repository, refnames []string) []string {
	if repository == nil {
		return nil
	}
	paths := make([]string, 0)
	for _, refname := range sortedUnique(refnames) {
		paths = append(paths, nativeLockPathsFor(repository.GitDir, repository.CommonGitDir, refname)...)
	}
	return sortedUnique(paths)
}
