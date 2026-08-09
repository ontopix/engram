// Package acquire implements verified, publish-after-audit managed-store
// acquisition. Clone is the only operation here which may initiate network or
// credential effects; reuse inspection is strictly local.
package acquire

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/ontopix/engram/internal/checker"
	"github.com/ontopix/engram/internal/gitraw"
	"github.com/ontopix/engram/internal/guard"
	"github.com/ontopix/engram/internal/hooks"
	"github.com/ontopix/engram/internal/managedread"
	"github.com/ontopix/engram/internal/transport"
)

type ErrorKind string

const (
	ErrorUsage       ErrorKind = "usage"
	ErrorCancelled   ErrorKind = "cancelled"
	ErrorCapability  ErrorKind = "capability"
	ErrorNetwork     ErrorKind = "network"
	ErrorConflict    ErrorKind = "conflict"
	ErrorConcurrency ErrorKind = "concurrency"
	ErrorIntegration ErrorKind = "integration"
	ErrorRepository  ErrorKind = "repository"
	ErrorIO          ErrorKind = "io"
)

type Error struct {
	Kind ErrorKind
	Op   string
	Err  error
}

func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Err == nil {
		return e.Op
	}
	return e.Op + ": " + e.Err.Error()
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func KindOf(err error) ErrorKind {
	var typed *Error
	if errors.As(err, &typed) {
		return typed.Kind
	}
	return ""
}

type Options struct {
	Destination         string
	DestinationProvided bool
}

type Result struct {
	Root            string                     `json:"root"`
	Remote          string                     `json:"remote"`
	Accepted        managedread.GitState       `json:"accepted"`
	Published       bool                       `json:"published"`
	Reused          bool                       `json:"reused"`
	VerifiedCommits int                        `json:"verified_commits"`
	Launcher        guard.State                `json:"launcher"`
	Validation      checker.Result             `json:"validation"`
	Audits          []managedread.HistoryAudit `json:"audits"`
}

// Clone obtains location into an unpublished sibling staging directory,
// configures byte-transparent presentation, audits the complete accepted
// lineage, installs the owned raw-Git guard, and only then atomically publishes
// the checkout at its final path.
func Clone(ctx context.Context, location string, options Options) (Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return Result{}, typed(ErrorCancelled, "clone", err)
	}
	if err := transport.ValidateLocation(location); err != nil {
		return Result{}, typed(ErrorUsage, "validate clone location", err)
	}
	destination, err := cloneDestination(location, options)
	if err != nil {
		return Result{}, err
	}
	if _, statErr := os.Lstat(destination); statErr == nil {
		if options.DestinationProvided {
			return Result{}, typed(ErrorConflict, "select clone destination", errors.New("explicit destination already exists"))
		}
		return reuse(ctx, location, destination)
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return Result{}, typed(ErrorIO, "inspect clone destination", statErr)
	}

	parent := filepath.Dir(destination)
	if !options.DestinationProvided {
		if err := os.MkdirAll(parent, 0o700); err != nil {
			return Result{}, typed(ErrorIO, "create default clone parent", err)
		}
	}
	parentInfo, err := os.Lstat(parent)
	if err != nil || parentInfo.Mode()&os.ModeSymlink != 0 || !parentInfo.IsDir() {
		return Result{}, typed(ErrorUsage, "select clone destination", errors.New("destination parent is not an existing real directory"))
	}
	staging, err := os.MkdirTemp(parent, ".engram-clone-")
	if err != nil {
		return Result{}, typed(ErrorIO, "create clone staging directory", err)
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(staging)
		}
	}()
	checkout := filepath.Join(staging, "store")
	if err := runClone(ctx, location, checkout); err != nil {
		return Result{}, err
	}
	if err := configurePresentation(ctx, checkout); err != nil {
		return Result{}, err
	}
	result, repository, err := verify(ctx, checkout)
	if err != nil {
		return Result{}, err
	}
	result.Root = destination
	result.Remote = "origin"
	result.Launcher = guard.Planned
	if result.Validation.Status != checker.StatusComplete || result.Validation.HasErrors() {
		return result, nil
	}
	launcher, err := guard.Install(ctx, repository)
	if err != nil {
		return Result{}, typed(ErrorIntegration, "install managed Git guard", err)
	}
	result.Launcher = launcher
	if err := verifyOriginAndUpstream(ctx, checkout, location, repository.HeadRef); err != nil {
		return Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return Result{}, typed(ErrorCancelled, "publish clone", err)
	}
	if _, err := os.Lstat(destination); err == nil {
		return Result{}, typed(ErrorConcurrency, "publish clone", errors.New("destination appeared concurrently"))
	} else if !errors.Is(err, os.ErrNotExist) {
		return Result{}, typed(ErrorIO, "publish clone", err)
	}
	if err := os.Rename(checkout, destination); err != nil {
		return Result{}, typed(ErrorIO, "publish clone", err)
	}
	result.Published = true
	if err := os.Remove(staging); err != nil {
		// The checkout is already durably visible. Do not report a failed clone
		// merely because its now-empty transaction directory could not be swept.
		cleanup = false
		return result, nil
	}
	cleanup = false
	return result, nil
}

func cloneDestination(location string, options Options) (string, error) {
	if options.DestinationProvided {
		if options.Destination == "" || !utf8.ValidString(options.Destination) {
			return "", typed(ErrorUsage, "select clone destination", errors.New("destination is empty or not UTF-8"))
		}
		absolute, err := filepath.Abs(options.Destination)
		if err != nil {
			return "", typed(ErrorUsage, "select clone destination", err)
		}
		return filepath.Clean(absolute), nil
	}
	if options.Destination != "" {
		if !filepath.IsAbs(options.Destination) || filepath.Clean(options.Destination) != options.Destination {
			return "", typed(ErrorUsage, "select clone destination", errors.New("injected default destination is not absolute and clean"))
		}
		return options.Destination, nil
	}
	value, err := transport.DefaultDestination(location)
	if err != nil {
		return "", typed(ErrorCapability, "select default clone destination", err)
	}
	return value, nil
}

func reuse(ctx context.Context, location, destination string) (Result, error) {
	result, repository, err := verify(ctx, destination)
	if err != nil {
		return Result{}, typed(ErrorConflict, "reuse default clone", err)
	}
	if result.Validation.Status != checker.StatusComplete || result.Validation.HasErrors() {
		return Result{}, typed(ErrorConflict, "reuse default clone", errors.New("existing clone no longer has a conforming accepted lineage"))
	}
	if err := verifyOriginAndUpstream(ctx, destination, location, repository.HeadRef); err != nil {
		return Result{}, typed(ErrorConflict, "reuse default clone", err)
	}
	if _, err := hooks.ResolveStoreIdentity(destination); err != nil {
		return Result{}, typed(ErrorConflict, "reuse default clone identity", err)
	}
	launcher, err := guard.Inspect(ctx, repository)
	if err != nil || launcher != guard.Unchanged {
		return Result{}, typed(ErrorConflict, "reuse default clone guard", err)
	}
	if ok, err := hasCacheExclusion(repository.GitDir); err != nil || !ok {
		return Result{}, typed(ErrorConflict, "reuse default clone cache exclusion", err)
	}
	result.Root = destination
	result.Remote = "origin"
	result.Launcher = launcher
	result.Reused = true
	return result, nil
}

func verify(ctx context.Context, root string) (Result, *gitraw.Repository, error) {
	store, err := managedread.Open(ctx, root)
	if err != nil {
		return Result{}, nil, typed(ErrorRepository, "open cloned managed store", err)
	}
	audit, err := store.AuditAccepted(ctx)
	if err != nil {
		return Result{}, nil, typed(ErrorRepository, "audit cloned managed store", err)
	}
	repository := store.Repository()
	accepted := managedread.GitState{Ref: stringPtr(repository.HeadRef)}
	if repository.Head != nil {
		accepted.Commit = stringPtr(repository.Head.String())
	}
	result := Result{
		Root: repository.Root, Remote: "origin", Accepted: accepted,
		VerifiedCommits: len(audit.Audits), Validation: audit.Validation,
		Audits: append([]managedread.HistoryAudit(nil), audit.Audits...),
	}
	return result, repository, nil
}

func runClone(ctx context.Context, location, destination string) error {
	git, err := exec.LookPath("git")
	if err != nil {
		return typed(ErrorCapability, "locate Git", err)
	}
	arguments := []string{
		"--no-pager", "--no-optional-locks", "--no-replace-objects",
		"-c", "core.hooksPath=" + os.DevNull,
		"clone", "--origin", "origin", "--no-tags", "--no-recurse-submodules", "--single-branch", "--template=", "--", location, destination,
	}
	command := exec.CommandContext(ctx, git, arguments...)
	command.Env = isolatedEnvironment(os.Environ())
	var stderr bytes.Buffer
	command.Stdout = &stderr
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return typed(ErrorCancelled, "clone repository", ctx.Err())
		}
		return typed(ErrorNetwork, "clone repository", fmt.Errorf("%w: %s", err, bounded(stderr.Bytes())))
	}
	return nil
}

func configurePresentation(ctx context.Context, root string) error {
	for _, pair := range [][2]string{
		{"core.autocrlf", "false"},
		{"core.sparseCheckout", "false"},
		{"core.sparseCheckoutCone", "false"},
		{"index.sparse", "false"},
	} {
		if _, err := gitOutput(ctx, root, "config", "--local", pair[0], pair[1]); err != nil {
			return typed(ErrorRepository, "configure byte-transparent presentation", err)
		}
	}
	if err := installCacheExclusion(filepath.Join(root, ".git")); err != nil {
		return typed(ErrorIntegration, "install cache exclusion", err)
	}
	return nil
}

func verifyOriginAndUpstream(ctx context.Context, root, location, headRef string) error {
	urls, err := gitConfigAll(ctx, root, "remote.origin.url")
	if err != nil || len(urls) != 1 || urls[0] != location {
		return typed(ErrorRepository, "verify origin URL", errors.New("origin does not contain the exact requested URL"))
	}
	short, ok := strings.CutPrefix(headRef, "refs/heads/")
	if !ok {
		return typed(ErrorRepository, "verify clone upstream", errors.New("HEAD is not a local branch"))
	}
	remote, err := gitConfigAll(ctx, root, "branch."+short+".remote")
	if err != nil || len(remote) != 1 || remote[0] != "origin" {
		return typed(ErrorRepository, "verify clone upstream", errors.New("accepted branch does not track origin"))
	}
	merge, err := gitConfigAll(ctx, root, "branch."+short+".merge")
	if err != nil || len(merge) != 1 || merge[0] != headRef {
		return typed(ErrorRepository, "verify clone upstream", errors.New("accepted branch upstream differs"))
	}
	return nil
}

func installCacheExclusion(gitDirectory string) error {
	name := filepath.Join(gitDirectory, "info", "exclude")
	if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
		return err
	}
	var content []byte
	info, err := os.Lstat(name)
	switch {
	case err == nil:
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return errors.New("Git exclude file is not a real regular file")
		}
		content, err = os.ReadFile(name)
		if err != nil {
			return err
		}
	case errors.Is(err, os.ErrNotExist):
	default:
		return err
	}
	if exclusionPresent(content) {
		return nil
	}
	if len(content) != 0 && content[len(content)-1] != '\n' {
		content = append(content, '\n')
	}
	content = append(content, []byte(".engram/cache/\n")...)
	return os.WriteFile(name, content, 0o600)
}

func hasCacheExclusion(gitDirectory string) (bool, error) {
	content, err := os.ReadFile(filepath.Join(gitDirectory, "info", "exclude"))
	if err != nil {
		return false, err
	}
	return exclusionPresent(content), nil
}

func exclusionPresent(content []byte) bool {
	for _, line := range bytes.Split(content, []byte("\n")) {
		if string(line) == ".engram/cache/" {
			return true
		}
	}
	return false
}

func gitConfigAll(ctx context.Context, root, key string) ([]string, error) {
	output, status, err := gitOutputStatus(ctx, root, "config", "--local", "--get-all", key)
	if err != nil {
		return nil, err
	}
	if status == 1 {
		return []string{}, nil
	}
	if status != 0 {
		return nil, fmt.Errorf("git config exited %d", status)
	}
	if len(output) == 0 {
		return []string{""}, nil
	}
	lines := bytes.Split(bytes.TrimSuffix(output, []byte("\n")), []byte("\n"))
	values := make([]string, len(lines))
	for index, line := range lines {
		values[index] = string(line)
	}
	return values, nil
}

func gitOutput(ctx context.Context, root string, arguments ...string) ([]byte, error) {
	output, status, err := gitOutputStatus(ctx, root, arguments...)
	if err != nil {
		return nil, err
	}
	if status != 0 {
		return nil, fmt.Errorf("git exited %d", status)
	}
	return output, nil
}

func gitOutputStatus(ctx context.Context, root string, arguments ...string) ([]byte, int, error) {
	git, err := exec.LookPath("git")
	if err != nil {
		return nil, -1, err
	}
	global := []string{"--no-pager", "--no-optional-locks", "--no-replace-objects", "-c", "core.hooksPath=" + os.DevNull, "-C", root}
	command := exec.CommandContext(ctx, git, append(global, arguments...)...)
	command.Env = isolatedEnvironment(os.Environ())
	output, err := command.Output()
	if err == nil {
		return output, 0, nil
	}
	if ctx.Err() != nil {
		return nil, -1, ctx.Err()
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return output, exit.ExitCode(), nil
	}
	return nil, -1, err
}

func isolatedEnvironment(environment []string) []string {
	result := make([]string, 0, len(environment)+8)
	for _, item := range environment {
		name, _, _ := strings.Cut(item, "=")
		upper := strings.ToUpper(name)
		if strings.HasPrefix(upper, "GIT_") || strings.HasPrefix(upper, "ENGRAM_") {
			continue
		}
		result = append(result, item)
	}
	return append(result,
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_COUNT=0",
		"GIT_NO_LAZY_FETCH=1", "GIT_OPTIONAL_LOCKS=0", "GIT_PAGER=cat",
	)
}

func bounded(value []byte) string {
	const limit = 16 << 10
	if len(value) > limit {
		value = value[:limit]
	}
	return strings.TrimSpace(string(value))
}

func typed(kind ErrorKind, operation string, err error) error {
	if err == nil {
		err = errors.New("unknown acquisition failure")
	}
	return &Error{Kind: kind, Op: operation, Err: err}
}

func stringPtr(value string) *string {
	copy := value
	return &copy
}
