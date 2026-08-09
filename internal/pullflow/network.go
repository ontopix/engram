package pullflow

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"unicode/utf8"

	"github.com/ontopix/engram/internal/gitraw"
	"github.com/ontopix/engram/internal/remoteselect"
)

func (p *Puller) gitPath() (string, error) {
	if p != nil && p.LookPath != nil {
		return p.LookPath("git")
	}
	return exec.LookPath("git")
}

func selectRemote(ctx context.Context, repository *gitraw.Repository, remote, branch string) (remoteselect.Selection, error) {
	arguments := make([]string, 0, 2)
	if remote == "" {
		if branch != "" {
			return remoteselect.Selection{}, typed(ErrorUsage, "select remote branch", errors.New("branch requires an explicit remote"))
		}
	} else {
		if !validRemoteArgument(remote) {
			return remoteselect.Selection{}, typed(ErrorUsage, "select remote branch", fmt.Errorf("invalid remote name %q", remote))
		}
		arguments = append(arguments, remote)
		if branch != "" {
			if !remoteselect.ValidBranch(branch) {
				return remoteselect.Selection{}, typed(ErrorUsage, "select remote branch", fmt.Errorf("invalid branch name %q", branch))
			}
			arguments = append(arguments, branch)
		}
	}
	selection, err := remoteselect.Select(ctx, repository.Root, repository.HeadRef, arguments, remoteselect.Fetch)
	if err != nil {
		if ctx.Err() != nil {
			return remoteselect.Selection{}, typed(ErrorCancelled, "select remote branch", ctx.Err())
		}
		return remoteselect.Selection{}, typed(ErrorRepository, "select remote branch", err)
	}
	return selection, nil
}

func validRemoteArgument(value string) bool {
	return value != "" && utf8.ValidString(value) && !strings.HasPrefix(value, "-") && !strings.ContainsAny(value, "\x00\r\n")
}

func (p *Puller) acquireTip(ctx context.Context, git string, repository *gitraw.Repository, selection remoteselect.Selection) (gitraw.OID, error) {
	if err := p.rejectURLRewrites(ctx, git, repository.Root); err != nil {
		return gitraw.OID{}, err
	}
	before, err := p.observe(ctx, git, repository, selection)
	if err != nil {
		return gitraw.OID{}, err
	}
	if before == nil {
		return gitraw.OID{}, typed(ErrorNetwork, "observe incoming branch", errors.New("selected remote branch is absent"))
	}
	fetched := p.command(ctx, git, repository.Root, nil,
		"fetch", "--no-tags", "--no-write-fetch-head", "--no-recurse-submodules", selection.URL, selection.RemoteRef)
	if !fetched.started {
		if ctx.Err() != nil {
			return gitraw.OID{}, typed(ErrorCancelled, "start fetch", ctx.Err())
		}
		return gitraw.OID{}, typed(ErrorCapability, "start fetch", fetched.err)
	}
	if ctx.Err() != nil || errors.Is(fetched.err, context.Canceled) || errors.Is(fetched.err, context.DeadlineExceeded) {
		return gitraw.OID{}, typed(ErrorCancelled, "fetch incoming branch", ctx.Err())
	}
	if fetched.err != nil || fetched.status != 0 {
		detail := commandDetail(fetched)
		if strings.Contains(strings.ToLower(detail), "unknown option") {
			return gitraw.OID{}, typed(ErrorCapability, "fetch incoming branch", errors.New(detail))
		}
		return gitraw.OID{}, typed(ErrorNetwork, "fetch incoming branch", errors.New(detail))
	}
	if err := p.rejectURLRewrites(ctx, git, repository.Root); err != nil {
		return gitraw.OID{}, err
	}
	after, err := p.observe(ctx, git, repository, selection)
	if err != nil {
		return gitraw.OID{}, err
	}
	if after == nil || *after != *before {
		return gitraw.OID{}, typed(ErrorConcurrency, "stabilize incoming branch", errors.New("remote ref changed while it was fetched"))
	}
	oid, err := gitraw.ParseOID(repository.Format, *before)
	if err != nil {
		return gitraw.OID{}, typed(ErrorNetwork, "parse incoming branch", err)
	}
	if _, err := repository.ReadObject(ctx, oid); err != nil {
		return gitraw.OID{}, classifyReadError(ctx, "verify fetched tip", err)
	}
	return oid, nil
}

func (p *Puller) rejectURLRewrites(ctx context.Context, git, root string) error {
	result := p.command(ctx, git, root, nil, "config", "--includes", "--get-regexp", `^url\..*\.(insteadof|pushinsteadof)$`)
	if !result.started {
		if ctx.Err() != nil {
			return typed(ErrorCancelled, "inspect remote URL rewrites", ctx.Err())
		}
		return typed(ErrorCapability, "inspect remote URL rewrites", result.err)
	}
	if ctx.Err() != nil {
		return typed(ErrorCancelled, "inspect remote URL rewrites", ctx.Err())
	}
	if result.err != nil || result.status != 0 && result.status != 1 {
		return typed(ErrorRepository, "inspect remote URL rewrites", errors.New(commandDetail(result)))
	}
	if result.status == 0 || len(result.stdout) != 0 {
		return typed(ErrorRepository, "inspect remote URL rewrites", errors.New("repository URL rewrite configuration is not admitted"))
	}
	return nil
}

func (p *Puller) observe(ctx context.Context, git string, repository *gitraw.Repository, selection remoteselect.Selection) (*string, error) {
	result := p.command(ctx, git, repository.Root, nil, "ls-remote", "--refs", selection.URL, selection.RemoteRef)
	if !result.started {
		if ctx.Err() != nil {
			return nil, typed(ErrorCancelled, "observe incoming branch", ctx.Err())
		}
		return nil, typed(ErrorCapability, "start incoming observation", result.err)
	}
	if ctx.Err() != nil {
		return nil, typed(ErrorCancelled, "observe incoming branch", ctx.Err())
	}
	if result.err != nil || result.status != 0 {
		return nil, typed(ErrorNetwork, "observe incoming branch", errors.New(commandDetail(result)))
	}
	return parseObservedRef(result.stdout, selection.RemoteRef, repository.Format)
}

func parseObservedRef(output []byte, ref string, format gitraw.ObjectFormat) (*string, error) {
	if len(output) == 0 {
		return nil, nil
	}
	if bytes.ContainsAny(output, "\x00\r") || output[len(output)-1] != '\n' {
		return nil, typed(ErrorNetwork, "parse incoming advertisement", errors.New("remote returned invalid ref bytes"))
	}
	var exact *string
	for _, line := range bytes.Split(output[:len(output)-1], []byte{'\n'}) {
		fields := bytes.Split(line, []byte{'\t'})
		if len(fields) != 2 {
			return nil, typed(ErrorNetwork, "parse incoming advertisement", errors.New("remote returned an invalid ref line"))
		}
		if string(fields[1]) != ref {
			continue
		}
		if exact != nil {
			return nil, typed(ErrorNetwork, "parse incoming advertisement", errors.New("remote returned the selected ref more than once"))
		}
		value := string(fields[0])
		if _, err := gitraw.ParseOID(format, value); err != nil {
			return nil, typed(ErrorNetwork, "parse incoming advertisement", errors.New("remote returned an object ID at the wrong width"))
		}
		exact = stringPointer(value)
	}
	return exact, nil
}

func classifyReadError(ctx context.Context, operation string, err error) error {
	if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return typed(ErrorCancelled, operation, ctx.Err())
	}
	var raw *gitraw.Error
	if errors.As(err, &raw) && (raw.Kind == gitraw.FailureCapability || raw.Kind == gitraw.FailureMissing) {
		return typed(ErrorCapability, operation, err)
	}
	return typed(ErrorRepository, operation, err)
}
