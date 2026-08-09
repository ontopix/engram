package doctor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ontopix/engram/internal/checker"
	"github.com/ontopix/engram/internal/gitraw"
	"github.com/ontopix/engram/internal/guard"
	"github.com/ontopix/engram/internal/hooks"
	"github.com/ontopix/engram/internal/managedread"
)

type inspection struct {
	result       Result
	target       string
	repository   *gitraw.Repository
	audit        *managedread.AcceptedAudit
	accepted     *checker.Snapshot
	working      *checker.Snapshot
	recoveryPlan recoveryPlan
}

// Inspect evaluates one exact target. The target may be absent only when an
// exact target-derived initialization or acquisition state file is recognized.
func Inspect(ctx context.Context, target string, options Options) (Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return Result{}, fail(FailureCancelled, "inspect doctor target", err)
	}
	if target == "" {
		return Result{}, fail(FailureRepository, "resolve doctor target", errors.New("target path is empty"))
	}
	absolute, err := filepath.Abs(target)
	if err != nil {
		return Result{}, fail(FailureRepository, "resolve doctor target", err)
	}
	absolute = filepath.Clean(absolute)
	stateEvidence, evidenceErr := targetStateEvidence(absolute)
	if evidenceErr != nil {
		return Result{}, fail(FailureIO, "inspect target controller state", evidenceErr)
	}
	info, statErr := os.Lstat(absolute)
	if errors.Is(statErr, os.ErrNotExist) && !stateEvidence {
		return Result{}, fail(FailureRepository, "inspect doctor target", errors.New("target is missing and has no recognized controller state"))
	}
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return Result{}, fail(FailureIO, "inspect doctor target", statErr)
	}
	if statErr == nil && (info.Mode()&os.ModeSymlink != 0 || !info.IsDir()) {
		return Result{}, fail(FailureRepository, "inspect doctor target", errors.New("target is not a real directory"))
	}

	first, err := inspectOnce(ctx, absolute, options.Recover)
	if err != nil {
		return Result{}, err
	}
	if !options.Recover || !first.recoveryPlan.needed || !first.recoveryPlan.safe {
		return first.result, nil
	}

	performed := false
	var adapterAccepted *managedread.GitState
	switch {
	case first.recoveryPlan.preJournalOnly:
		if err := cleanStalePreJournal(first.recoveryPlan); err != nil {
			var cleanup *cleanupError
			durable := errors.As(err, &cleanup) && cleanup.durable
			return Result{}, failMutation(FailureConcurrency, "clean stale pre-journal locks", err, Mutation{Durable: durable, RecoveryRequired: true})
		}
		performed = true
	case options.Recovery != nil:
		response, err := options.Recovery.Recover(ctx, RecoveryRequest{Target: absolute, Repository: first.repository})
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return Result{}, failMutation(FailureCancelled, "recover recognized controller state", err, Mutation{Durable: response.Durable, RecoveryRequired: true})
			}
			return Result{}, failMutation(FailureOperational, "recover recognized controller state", err, Mutation{Durable: response.Durable, RecoveryRequired: true})
		}
		adapterAccepted = response.Accepted
		performed = true
	default:
		return first.result, nil
	}

	if err := ctx.Err(); err != nil {
		return Result{}, fail(FailureCancelled, "recheck recovered target", err)
	}
	after, err := inspectOnce(ctx, absolute, true)
	if err != nil {
		// A successful acquisition/init cleanup is allowed to leave no store.
		if performed && KindOf(err) == FailureRepository {
			result := first.result
			result.Recovery.Performed = true
			result.Recovery.Accepted = nil
			return result, nil
		}
		return Result{}, err
	}
	after.result.Recovery.Requested = true
	after.result.Recovery.Needed = first.result.Recovery.Needed
	after.result.Recovery.Performed = performed && !after.recoveryPlan.needed
	if adapterAccepted != nil && after.result.Recovery.Accepted == nil {
		after.result.Recovery.Accepted = adapterAccepted
	}
	return after.result, nil
}

func inspectOnce(ctx context.Context, target string, requested bool) (inspection, error) {
	current := inspection{target: target, result: Result{Checks: initialChecks(), Recovery: Recovery{Requested: requested}}}
	info, statErr := os.Lstat(target)
	targetExists := statErr == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0
	if targetExists {
		store, openErr := managedread.Open(ctx, target)
		if openErr == nil {
			current.repository = store.Repository()
			current.result.Recovery.Accepted = acceptedState(current.repository)
			audit, auditErr := store.AuditAccepted(ctx)
			if auditErr != nil {
				setRequired(&current.result, "repository.shape", Error, nil, "accepted history or required raw objects are unavailable: "+auditErr.Error())
			} else {
				current.audit = audit
				current.accepted = audit.Snapshots[audit.Tip]
				if shapeProblems(audit) {
					setRequired(&current.result, "repository.shape", Error, nil, "accepted history or snapshot shape is not conforming")
				}
			}
			working, workingErr := checker.CheckFS(current.repository.Root)
			if workingErr == nil {
				current.working = working
			}
		} else {
			setRequired(&current.result, "repository.shape", Error, nil, "target is not an eligible managed repository: "+openErr.Error())
		}
	} else {
		setRequired(&current.result, "repository.shape", Error, nil, "target has not been published as a managed store")
	}

	inspectIdentity(&current, targetExists)
	inspectGuard(ctx, &current)
	lifecycle := inspectLifecycleStates(&current)
	recovery := inspectRecoveryState(ctx, &current)
	inspectReplayState(&current)
	inspectPresentation(ctx, &current)
	inspectCacheExclusion(&current)
	appendHeuristics(&current)

	current.recoveryPlan = combineRecoveryPlans(lifecycle, recovery)
	current.result.Recovery.Needed = current.recoveryPlan.needed
	return current, nil
}

func acceptedState(repository *gitraw.Repository) *managedread.GitState {
	if repository == nil {
		return nil
	}
	state := managedread.GitState{Ref: pathPointer(repository.HeadRef)}
	if repository.Head != nil {
		state.Commit = pathPointer(repository.Head.String())
	}
	return &state
}

func shapeProblems(audit *managedread.AcceptedAudit) bool {
	if audit == nil || audit.Validation.Status != checker.StatusComplete || audit.Raw == nil || !audit.Raw.Complete {
		return true
	}
	rawE601 := false
	for _, finding := range audit.Raw.Findings {
		if strings.HasPrefix(finding.Code, "E") {
			if finding.Code == "E601" {
				rawE601 = true
			}
			return true
		}
	}
	for _, finding := range audit.Validation.Findings {
		if !strings.HasPrefix(finding.Code, "E") {
			continue
		}
		// AuditAccepted also attributes presentation failure to E601. The
		// dedicated presentation rows below own that failure unless raw history
		// independently established an E601 boundary.
		if finding.Code == "E601" && !rawE601 {
			continue
		}
		return true
	}
	return false
}

func inspectIdentity(current *inspection, targetExists bool) {
	if !targetExists {
		setRequired(&current.result, "identity.binding", Error, nil, "physical store identity is unavailable before publication")
		return
	}
	identity, err := hooks.ResolveStoreIdentity(current.target)
	if err != nil {
		setRequired(&current.result, "identity.binding", Error, nil, "physical store identity cannot be proven: "+err.Error())
		return
	}
	if current.repository != nil && identity.Path != current.repository.Root {
		setRequired(&current.result, "identity.binding", Error, nil, "physical binding does not name the exact repository root")
	}
}

func inspectGuard(ctx context.Context, current *inspection) {
	if current.repository == nil {
		setRequired(&current.result, "guard.ownership", Error, nil, "managed repository is unavailable")
		return
	}
	state, err := guard.Inspect(ctx, current.repository)
	if err != nil {
		setRequired(&current.result, "guard.ownership", Error, nil, "owned guard cannot be verified: "+err.Error())
		return
	}
	if state != guard.Unchanged {
		setRequired(&current.result, "guard.ownership", Error, nil, "owned guard bytes, executable mode, or version are not installed")
	}
}

func setRequired(result *Result, name string, status Status, path *string, message string) {
	for index := range result.Checks {
		if result.Checks[index].Name != name {
			continue
		}
		result.Checks[index].Status = status
		result.Checks[index].Path = path
		if message != "" {
			result.Checks[index].Detail = detail(message)
		} else {
			result.Checks[index].Detail = nil
		}
		return
	}
	panic(fmt.Sprintf("doctor: unknown required check %q", name))
}

// HasRequiredErrors reports the protocol outcome boundary. Heuristic warnings
// never turn a complete doctor result into issues.
func (r Result) HasRequiredErrors() bool {
	for _, check := range r.Checks {
		if check.Class == Required && check.Status == Error {
			return true
		}
	}
	return false
}

func sortedUnique(values []string) []string {
	sort.Strings(values)
	result := values[:0]
	for _, value := range values {
		if len(result) == 0 || result[len(result)-1] != value {
			result = append(result, value)
		}
	}
	return result
}
