package syncflow

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"unicode/utf8"

	"github.com/ontopix/engram/internal/checker"
	"github.com/ontopix/engram/internal/gitraw"
	"github.com/ontopix/engram/internal/managedread"
	"github.com/ontopix/engram/internal/networkgit"
	"github.com/ontopix/engram/internal/remoteselect"
)

// Pusher owns the process environment used for remote observation and the
// one conditional publication. A nil Environment inherits the host
// environment after removing every GIT_* and ENGRAM_* variable.
type Pusher struct {
	Environment []string
	LookPath    func(string) (string, error)

	// afterObserve is a deterministic race/cancellation seam. Production
	// pushers leave it nil. The operation rechecks cancellation after it runs.
	afterObserve func(selection remoteselect.Selection, before *string)
	// afterRewriteCheck is the final local-config race seam. Network Git uses a
	// private repository even when a rewrite appears after this point.
	afterRewriteCheck func()

	run func(context.Context, string, string, []string, ...string) commandResult
}

// NewPusher constructs a pusher using the system Git executable.
func NewPusher() *Pusher { return &Pusher{} }

// Push is the convenient one-shot form of NewPusher().Push.
func Push(ctx context.Context, store *managedread.Store, remote, branch string) (*PushResult, error) {
	return NewPusher().Push(ctx, store, remote, branch)
}

// Push completely audits the local accepted lineage before its first network
// operation. Empty remote and branch select the configured upstream; a remote
// with an empty branch selects the accepted branch's short name there.
func (p *Pusher) Push(ctx context.Context, store *managedread.Store, remote, branch string) (*PushResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, typed(ErrorCancelled, "push", err)
	}
	if store == nil || store.Repository() == nil {
		return nil, typed(ErrorRepository, "select managed store", errors.New("nil managed store"))
	}
	arguments, err := selectionArguments(remote, branch)
	if err != nil {
		return nil, err
	}
	repository, err := captureRepository(ctx, store.Repository())
	if err != nil {
		return nil, err
	}
	selection, err := remoteselect.Select(ctx, repository.Root, repository.HeadRef, arguments, remoteselect.Push)
	if err != nil {
		if ctx.Err() != nil {
			return nil, typed(ErrorCancelled, "select remote branch", ctx.Err())
		}
		return nil, typed(ErrorRepository, "select remote branch", err)
	}

	audit, err := store.AuditAccepted(ctx)
	if err != nil {
		return nil, classifyAuditError(ctx, err)
	}
	if err := verifyAuditedRepository(ctx, repository, audit); err != nil {
		return nil, err
	}
	result, err := resultFromAudit(selection, audit)
	if err != nil {
		return nil, err
	}
	if audit.Validation.Status != checker.StatusComplete || audit.Validation.HasErrors() {
		result.State = PushRejected
		result.Changed = boolPointer(false)
		return result, nil
	}
	if err := requireCompleteAudit(audit); err != nil {
		return nil, typed(ErrorRepository, "audit local accepted lineage", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, typed(ErrorCancelled, "observe remote ref", err)
	}

	git, err := p.gitPath()
	if err != nil {
		return nil, typed(ErrorCapability, "locate git", err)
	}
	if err := p.rejectURLRewrites(ctx, git, repository.Root); err != nil {
		return nil, err
	}
	private, err := networkgit.New(repository.CommonGitDir, repository.Format)
	if err != nil {
		return nil, typed(ErrorCapability, "create private network context", err)
	}
	defer private.Close()
	before, observeErr := p.observe(ctx, git, private.Root(), repository.Format, selection)
	if observeErr != nil {
		return nil, observeErr
	}
	result.RemoteObserved = true
	result.Before = cloneString(before)

	ahead, found := commitsAfter(audit, before)
	if before != nil && !found {
		result.State = PushRejected
		result.Changed = boolPointer(false)
		return result, nil
	}
	result.Commits = ahead
	if ahead == 0 {
		result.State = PushUpToDate
		result.Changed = boolPointer(false)
		return result, nil
	}

	if p != nil && p.afterObserve != nil {
		p.afterObserve(selection, cloneString(before))
	}
	if err := ctx.Err(); err != nil {
		return nil, typed(ErrorCancelled, "conditionally publish remote ref", err)
	}
	if err := verifyAuditedRepository(ctx, repository, audit); err != nil {
		return nil, err
	}
	if err := p.rejectURLRewrites(ctx, git, repository.Root); err != nil {
		return nil, err
	}
	if p != nil && p.afterRewriteCheck != nil {
		p.afterRewriteCheck()
	}
	if err := ctx.Err(); err != nil {
		return nil, typed(ErrorCancelled, "conditionally publish remote ref", err)
	}

	publication := p.command(ctx, git, private.Root(), pushArguments(selection, audit.Tip, before)...)
	if !publication.started {
		if ctx.Err() != nil || errors.Is(publication.err, context.Canceled) || errors.Is(publication.err, context.DeadlineExceeded) {
			if ctx.Err() != nil {
				publication.err = ctx.Err()
			}
			return nil, typed(ErrorCancelled, "conditionally publish remote ref", publication.err)
		}
		return nil, typed(ErrorCapability, "start conditional remote update", publication.err)
	}
	report := parsePushReport(publication.stdout, audit.Tip, selection.RemoteRef)
	if report.casRace || report.upToDate {
		return nil, casFailure(report.summary)
	}
	if report.rejected {
		result.State = PushRejected
		result.Changed = boolPointer(false)
		return result, nil
	}
	dispatched := pushWasDispatched(publication.stderr, before, audit.Tip, selection.RemoteRef, repository.Format)
	if (publication.err != nil || publication.status != 0) && !dispatched {
		if ctx.Err() != nil || errors.Is(publication.err, context.Canceled) || errors.Is(publication.err, context.DeadlineExceeded) {
			if ctx.Err() != nil {
				publication.err = ctx.Err()
			}
			return nil, typed(ErrorCancelled, "conditionally publish remote ref", publication.err)
		}
		detail := commandDiagnostic(publication)
		if localPushCapabilityFailure(detail) {
			return nil, typed(ErrorCapability, "conditionally publish remote ref", errors.New(detail))
		}
		return nil, typed(ErrorNetwork, "conditionally publish remote ref", fmt.Errorf("%w: %s", ErrNetwork, detail))
	}
	if publication.err != nil || publication.status != 0 || !report.published {
		// Once receive-pack has been dispatched, an unclassified transport or
		// protocol failure is not safe to report as an ordinary network error:
		// the server may have committed the update before the response was lost.
		result.State = PushIndeterminate
		result.Changed = nil
		return result, nil
	}
	result.State = PushPushed
	result.Changed = boolPointer(true)
	return result, nil
}

func (p *Pusher) gitPath() (string, error) {
	if p != nil && p.LookPath != nil {
		return p.LookPath("git")
	}
	return exec.LookPath("git")
}

func (p *Pusher) command(ctx context.Context, executable, root string, arguments ...string) commandResult {
	environment := []string(nil)
	if p != nil {
		environment = p.Environment
		if p.run != nil {
			return p.run(ctx, executable, root, environment, arguments...)
		}
	}
	return runGitCommand(ctx, executable, root, environment, arguments...)
}

func selectionArguments(remote, branch string) ([]string, error) {
	if remote == "" {
		if branch != "" {
			return nil, typed(ErrorUsage, "select remote branch", errors.New("branch requires an explicit remote"))
		}
		return nil, nil
	}
	if !validRemoteArgument(remote) {
		return nil, typed(ErrorUsage, "select remote branch", fmt.Errorf("invalid remote name %q", remote))
	}
	if branch != "" && !remoteselect.ValidBranch(branch) {
		return nil, typed(ErrorUsage, "select remote branch", fmt.Errorf("invalid branch name %q", branch))
	}
	if branch == "" {
		return []string{remote}, nil
	}
	return []string{remote, branch}, nil
}

func validRemoteArgument(value string) bool {
	return value != "" && utf8.ValidString(value) && !strings.HasPrefix(value, "-") && !strings.ContainsAny(value, "\x00\r\n")
}

func captureRepository(ctx context.Context, opened *gitraw.Repository) (*gitraw.Repository, error) {
	if opened == nil {
		return nil, typed(ErrorRepository, "capture managed repository", errors.New("nil repository"))
	}
	captured, err := gitraw.Discover(ctx, opened.Root)
	if err != nil {
		return nil, classifyRepositoryRead(ctx, "capture managed repository", err)
	}
	if !sameRepositoryTopology(opened, captured) {
		return nil, typed(ErrorConcurrency, "capture managed repository", fmt.Errorf("%w: repository identity changed", managedread.ErrConcurrent))
	}
	return captured, nil
}

func verifyAuditedRepository(ctx context.Context, captured *gitraw.Repository, audit *managedread.AcceptedAudit) error {
	current, err := gitraw.Discover(ctx, captured.Root)
	if err != nil {
		return classifyRepositoryRead(ctx, "verify audited HEAD/ref", err)
	}
	if !sameRepositoryTopology(captured, current) || captured.HeadRef != current.HeadRef || !sameOID(captured.Head, current.Head) || captured.Head == nil || audit == nil || captured.Head.String() != audit.Tip {
		return typed(ErrorConcurrency, "verify audited HEAD/ref", fmt.Errorf("%w: selected HEAD/ref does not match the audited lineage", managedread.ErrConcurrent))
	}
	return nil
}

func sameRepositoryTopology(left, right *gitraw.Repository) bool {
	return left != nil && right != nil && left.Root == right.Root && left.GitDir == right.GitDir && left.CommonGitDir == right.CommonGitDir && left.Format == right.Format
}

func sameOID(left, right *gitraw.OID) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Equal(*right)
}

func classifyRepositoryRead(ctx context.Context, operation string, err error) error {
	if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return typed(ErrorCancelled, operation, err)
	}
	var raw *gitraw.Error
	if errors.As(err, &raw) && (raw.Kind == gitraw.FailureCapability || raw.Kind == gitraw.FailureMissing) {
		return typed(ErrorCapability, operation, err)
	}
	return typed(ErrorRepository, operation, err)
}

func classifyAuditError(ctx context.Context, err error) error {
	if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return typed(ErrorCancelled, "audit local accepted lineage", err)
	}
	if errors.Is(err, managedread.ErrConcurrent) {
		return typed(ErrorConcurrency, "audit local accepted lineage", err)
	}
	var raw *gitraw.Error
	if errors.As(err, &raw) && (raw.Kind == gitraw.FailureCapability || raw.Kind == gitraw.FailureMissing) {
		return typed(ErrorCapability, "audit local accepted lineage", err)
	}
	return typed(ErrorRepository, "audit local accepted lineage", err)
}

func resultFromAudit(selection remoteselect.Selection, audit *managedread.AcceptedAudit) (*PushResult, error) {
	if audit == nil || audit.Tip == "" {
		return nil, typed(ErrorRepository, "audit local accepted lineage", errors.New("audit has no accepted tip"))
	}
	return &PushResult{
		Remote:     selection.Remote,
		RemoteRef:  selection.RemoteRef,
		After:      audit.Tip,
		Validation: cloneValidation(audit.Validation),
		Audits:     cloneAudits(audit.Audits),
		Commits:    0,
	}, nil
}

func requireCompleteAudit(audit *managedread.AcceptedAudit) error {
	if audit == nil || audit.Raw == nil || !audit.Raw.Complete {
		return errors.New("raw lineage audit is incomplete")
	}
	if len(audit.Raw.Commits) == 0 || len(audit.Audits) != len(audit.Raw.Commits) {
		return errors.New("snapshot or transition audit is incomplete")
	}
	if audit.Raw.Tip.String() != audit.Tip || audit.Raw.Commits[len(audit.Raw.Commits)-1].ID.String() != audit.Tip {
		return errors.New("raw lineage tip does not match the accepted audit tip")
	}
	for index, commit := range audit.Raw.Commits {
		if commit.Commit == nil || commit.Snapshot == nil || audit.Audits[index].Candidate != commit.ID.String() || audit.Audits[index].Validation.Status != checker.StatusComplete {
			return errors.New("raw commit lacks a complete matching transition audit")
		}
	}
	return nil
}

func (p *Pusher) rejectURLRewrites(ctx context.Context, git, root string) error {
	inspected := p.command(ctx, git, root, "config", "--includes", "--get-regexp", `^url\..*\.(insteadof|pushinsteadof)$`)
	if !inspected.started {
		if ctx.Err() != nil || errors.Is(inspected.err, context.Canceled) || errors.Is(inspected.err, context.DeadlineExceeded) {
			if ctx.Err() != nil {
				inspected.err = ctx.Err()
			}
			return typed(ErrorCancelled, "inspect remote URL rewrites", inspected.err)
		}
		return typed(ErrorCapability, "inspect remote URL rewrites", inspected.err)
	}
	if ctx.Err() != nil || errors.Is(inspected.err, context.Canceled) || errors.Is(inspected.err, context.DeadlineExceeded) {
		if ctx.Err() != nil {
			inspected.err = ctx.Err()
		}
		return typed(ErrorCancelled, "inspect remote URL rewrites", inspected.err)
	}
	if inspected.err != nil || (inspected.status != 0 && inspected.status != 1) {
		return typed(ErrorRepository, "inspect remote URL rewrites", errors.New(commandDiagnostic(inspected)))
	}
	if inspected.status == 0 || len(inspected.stdout) != 0 {
		return typed(ErrorRepository, "inspect remote URL rewrites", errors.New("repository URL rewrite configuration is not admitted"))
	}
	return nil
}

func (p *Pusher) observe(ctx context.Context, git, root string, format gitraw.ObjectFormat, selection remoteselect.Selection) (*string, error) {
	observed := p.command(ctx, git, root, "ls-remote", "--refs", selection.URL, selection.RemoteRef)
	if !observed.started {
		if ctx.Err() != nil || errors.Is(observed.err, context.Canceled) || errors.Is(observed.err, context.DeadlineExceeded) {
			if ctx.Err() != nil {
				observed.err = ctx.Err()
			}
			return nil, typed(ErrorCancelled, "observe remote ref", observed.err)
		}
		return nil, typed(ErrorCapability, "start remote ref observation", observed.err)
	}
	if ctx.Err() != nil || errors.Is(observed.err, context.Canceled) || errors.Is(observed.err, context.DeadlineExceeded) {
		if ctx.Err() != nil {
			observed.err = ctx.Err()
		}
		return nil, typed(ErrorCancelled, "observe remote ref", observed.err)
	}
	if observed.err != nil || observed.status != 0 {
		return nil, typed(ErrorNetwork, "observe remote ref", fmt.Errorf("%w: %s", ErrNetwork, commandDiagnostic(observed)))
	}
	before, err := parseObservedRef(observed.stdout, selection.RemoteRef, format)
	if err != nil {
		return nil, typed(ErrorNetwork, "observe remote ref", fmt.Errorf("%w: %v", ErrNetwork, err))
	}
	return before, nil
}

func parseObservedRef(output []byte, remoteRef string, format gitraw.ObjectFormat) (*string, error) {
	if len(output) == 0 {
		return nil, nil
	}
	if bytes.ContainsAny(output, "\x00\r") || output[len(output)-1] != '\n' {
		return nil, errors.New("remote returned an invalid ref advertisement")
	}
	var exact *string
	for _, line := range bytes.Split(output[:len(output)-1], []byte{'\n'}) {
		fields := bytes.Split(line, []byte{'\t'})
		if len(fields) != 2 {
			return nil, errors.New("remote returned an invalid ref advertisement")
		}
		if string(fields[1]) != remoteRef {
			// ls-remote patterns use tail matching. Non-exact suffix collisions
			// are deliberately ignored rather than mistaken for the branch.
			continue
		}
		if exact != nil {
			return nil, errors.New("remote returned the exact selected ref more than once")
		}
		oid := string(fields[0])
		if _, err := gitraw.ParseOID(format, oid); err != nil {
			return nil, errors.New("remote returned an object ID outside the local repository format")
		}
		exact = &oid
	}
	return exact, nil
}

func commitsAfter(audit *managedread.AcceptedAudit, before *string) (int, bool) {
	if audit == nil || audit.Raw == nil {
		return 0, false
	}
	if before == nil {
		return len(audit.Raw.Commits), true
	}
	for index, commit := range audit.Raw.Commits {
		if commit.ID.String() == *before {
			return len(audit.Raw.Commits) - index - 1, true
		}
	}
	return 0, false
}

func pushArguments(selection remoteselect.Selection, tip string, before *string) []string {
	expected := ""
	if before != nil {
		expected = *before
	}
	return []string{
		"push",
		"--porcelain",
		"--no-verify",
		"--no-follow-tags",
		"--no-recurse-submodules",
		"--no-signed",
		"--no-force-if-includes",
		"--force-with-lease=" + selection.RemoteRef + ":" + expected,
		selection.URL,
		tip + ":" + selection.RemoteRef,
	}
}

type pushReport struct {
	published bool
	rejected  bool
	casRace   bool
	upToDate  bool
	summary   string
}

func parsePushReport(output []byte, tip, remoteRef string) pushReport {
	var result pushReport
	statusLines := 0
	for _, line := range bytes.Split(output, []byte{'\n'}) {
		fields := bytes.Split(line, []byte{'\t'})
		if len(fields) != 3 || len(fields[0]) != 1 {
			continue
		}
		statusLines++
		if string(fields[1]) != tip+":"+remoteRef {
			return pushReport{}
		}
		result.summary = string(fields[2])
		switch fields[0][0] {
		case ' ', '*':
			result.published = true
		case '=':
			// Push is invoked only after an older or absent remote ref was
			// observed. A now-up-to-date destination therefore changed at the
			// observation/update boundary even if Git optimized away the write.
			result.upToDate = true
		case '!':
			if bytes.HasPrefix(fields[2], []byte("[remote rejected]")) {
				if remoteCASReason(string(fields[2])) {
					result.casRace = true
				} else {
					result.rejected = true
				}
			} else if bytes.HasPrefix(fields[2], []byte("[rejected]")) {
				result.casRace = true
			}
		}
	}
	if statusLines != 1 {
		return pushReport{}
	}
	return result
}

func remoteCASReason(summary string) bool {
	normalized := strings.ToLower(summary)
	return strings.Contains(normalized, "failed to update ref") ||
		strings.Contains(normalized, "cannot lock ref") ||
		strings.Contains(normalized, "stale info")
}

func pushWasDispatched(stderr []byte, before *string, tip, remoteRef string, format gitraw.ObjectFormat) bool {
	old := strings.Repeat("0", format.HexWidth())
	if before != nil {
		old = *before
	}
	needle := "push> " + old + " " + tip + " " + remoteRef
	for _, line := range strings.Split(string(stderr), "\n") {
		if strings.Contains(line, "packet:") && strings.Contains(line, needle) {
			return true
		}
	}
	return false
}

func localPushCapabilityFailure(detail string) bool {
	normalized := strings.ToLower(detail)
	for _, fragment := range []string{
		"unknown option", "unknown switch", "usage: git push",
		"src refspec", "does not match any", "bad object", "unable to read",
	} {
		if strings.Contains(normalized, fragment) {
			return true
		}
	}
	return false
}

func commandDiagnostic(result commandResult) string {
	detail := strings.TrimSpace(string(result.stderr))
	if detail == "" {
		detail = strings.TrimSpace(string(result.stdout))
	}
	if detail == "" && result.err != nil {
		detail = result.err.Error()
	}
	if detail == "" {
		detail = fmt.Sprintf("git exited with status %d", result.status)
	}
	return detail
}

func boolPointer(value bool) *bool {
	copy := value
	return &copy
}

func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneValidation(value checker.Result) checker.Result {
	findings := make([]checker.Finding, len(value.Findings))
	copy(findings, value.Findings)
	value.Findings = findings
	return value
}

func cloneAudits(values []managedread.HistoryAudit) []managedread.HistoryAudit {
	result := make([]managedread.HistoryAudit, len(values))
	for index, value := range values {
		result[index] = value
		result[index].Base = cloneString(value.Base)
		result[index].Validation = cloneValidation(value.Validation)
	}
	return result
}
