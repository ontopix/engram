package gitraw

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"
)

type Repository struct {
	Root         string
	GitDir       string
	CommonGitDir string
	HeadRef      string
	Head         *OID
	Format       ObjectFormat

	runner gitRunner
}

// Discover asks Git only for repository topology, then validates the accepted
// ref shape itself. All later history and tree traversal uses raw objects.
func Discover(ctx context.Context, selectedPath string) (*Repository, error) {
	git, err := exec.LookPath("git")
	if err != nil {
		return nil, &Error{Kind: FailureCapability, Op: "locate-git", Err: err}
	}
	runner := gitRunner{executable: git, directory: selectedPath}

	inside, err := runner.output(ctx, nil, "rev-parse", "--is-inside-work-tree")
	if err != nil || stringLine(inside) != "true" {
		return nil, &Error{Kind: FailureRepository, Op: "discover", Detail: "target is not a non-bare worktree", Err: err}
	}
	root, err := runner.pathOutput(ctx, "--show-toplevel")
	if err != nil {
		return nil, err
	}
	gitDir, err := runner.pathOutput(ctx, "--git-dir")
	if err != nil {
		return nil, err
	}
	commonDir, err := runner.pathOutput(ctx, "--git-common-dir")
	if err != nil {
		return nil, err
	}
	formatBytes, err := runner.output(ctx, nil, "rev-parse", "--show-object-format=storage")
	if err != nil {
		return nil, &Error{Kind: FailureCapability, Op: "discover-object-format", Err: err}
	}
	format, err := ParseObjectFormat(stringLine(formatBytes))
	if err != nil {
		return nil, err
	}

	headBytes, headStatus, err := runner.outputStatus(ctx, nil, "symbolic-ref", "--quiet", "HEAD")
	if err != nil {
		return nil, err
	}
	if headStatus != 0 {
		return nil, &Error{Kind: FailureRepository, Op: "discover-head", Detail: "HEAD is not symbolic"}
	}
	headRef := stringLine(headBytes)
	if !utf8.ValidString(headRef) || !strings.HasPrefix(headRef, "refs/heads/") || len(headRef) == len("refs/heads/") || strings.ContainsAny(headRef, "\x00\r\n") {
		return nil, &Error{Kind: FailureRepository, Op: "discover-head", Detail: "HEAD does not name a valid UTF-8 local branch"}
	}

	_, symbolicStatus, err := runner.outputStatus(ctx, nil, "symbolic-ref", "--quiet", headRef)
	if err != nil {
		return nil, err
	}
	if symbolicStatus == 0 {
		return nil, &Error{Kind: FailureRepository, Op: "discover-head", Detail: "accepted ref is symbolic"}
	}
	if symbolicStatus != 1 {
		return nil, &Error{Kind: FailureGit, Op: "discover-head", Detail: "cannot inspect accepted ref"}
	}

	var head *OID
	oidBytes, status, err := runner.outputStatus(ctx, nil, "show-ref", "--verify", "--hash", headRef)
	if err != nil {
		return nil, err
	}
	if status == 0 {
		parsed, parseErr := ParseOID(format, stringLine(oidBytes))
		if parseErr != nil {
			return nil, &Error{Kind: FailureRepository, Op: "discover-head", Detail: "accepted ref does not contain a canonical object ID", Err: parseErr}
		}
		head = &parsed
	} else if status != 1 {
		return nil, &Error{Kind: FailureGit, Op: "discover-head", Detail: "cannot resolve accepted ref"}
	}

	runner.directory = root
	return &Repository{
		Root:         root,
		GitDir:       gitDir,
		CommonGitDir: commonDir,
		HeadRef:      headRef,
		Head:         head,
		Format:       format,
		runner:       runner,
	}, nil
}

func (r *Repository) ReadObject(ctx context.Context, oid OID) (Object, error) {
	if r == nil || oid.Format() != r.Format || !oid.Valid() {
		return Object{}, &Error{Kind: FailureMalformed, Op: "read-object", OID: oid, Detail: "object ID does not match repository format"}
	}
	output, err := r.runner.output(ctx, []byte(oid.String()+"\n"), "cat-file", "--batch")
	if err != nil {
		return Object{}, &Error{Kind: FailureMalformed, Op: "read-object", OID: oid, Detail: "Git could not decode a present raw object", Err: err}
	}
	lineEnd := bytes.IndexByte(output, '\n')
	if lineEnd < 0 {
		return Object{}, &Error{Kind: FailureMalformed, Op: "read-object", OID: oid, Detail: "cat-file response has no header terminator"}
	}
	header := string(output[:lineEnd])
	if header == oid.String()+" missing" {
		return Object{}, &Error{Kind: FailureMissing, Op: "read-object", OID: oid}
	}
	fields := strings.Split(header, " ")
	if len(fields) != 3 || fields[0] != oid.String() {
		return Object{}, &Error{Kind: FailureMalformed, Op: "read-object", OID: oid, Detail: "invalid cat-file response header"}
	}
	size, conversionErr := strconv.ParseUint(fields[2], 10, 63)
	if conversionErr != nil || size > uint64(len(output)) {
		return Object{}, &Error{Kind: FailureMalformed, Op: "read-object", OID: oid, Detail: "invalid cat-file object size"}
	}
	payload := output[lineEnd+1:]
	if uint64(len(payload)) != size+1 || payload[len(payload)-1] != '\n' {
		return Object{}, &Error{Kind: FailureMalformed, Op: "read-object", OID: oid, Detail: "cat-file response does not match declared size"}
	}
	return Object{OID: oid, Type: ObjectType(fields[1]), Data: append([]byte(nil), payload[:len(payload)-1]...)}, nil
}

type commandOutput struct {
	stdout bytes.Buffer
	stderr bytes.Buffer
	status int
}

type gitRunner struct {
	executable string
	directory  string
}

func (r gitRunner) pathOutput(ctx context.Context, selector string) (string, error) {
	output, err := r.output(ctx, nil, "rev-parse", "--path-format=absolute", selector)
	if err != nil {
		return "", &Error{Kind: FailureRepository, Op: "discover-path", Err: err}
	}
	value := stringLine(output)
	if value == "" || !utf8.ValidString(value) || !filepath.IsAbs(value) {
		return "", &Error{Kind: FailureRepository, Op: "discover-path", Detail: fmt.Sprintf("Git returned invalid absolute path %q", value)}
	}
	return filepath.Clean(value), nil
}

func (r gitRunner) output(ctx context.Context, input []byte, arguments ...string) ([]byte, error) {
	output, status, err := r.outputStatus(ctx, input, arguments...)
	if err != nil {
		return nil, err
	}
	if status != 0 {
		return nil, &Error{Kind: FailureGit, Op: "git", Detail: strings.Join(arguments, " ") + " exited unsuccessfully"}
	}
	return output, nil
}

func (r gitRunner) outputStatus(ctx context.Context, input []byte, arguments ...string) ([]byte, int, error) {
	global := []string{"--no-pager", "--no-optional-locks", "--no-replace-objects", "-c", "core.hooksPath=" + os.DevNull}
	if r.directory != "" {
		global = append(global, "-C", r.directory)
	}
	command := exec.CommandContext(ctx, r.executable, append(global, arguments...)...)
	command.Env = isolatedGitEnvironment(os.Environ())
	command.Stdin = bytes.NewReader(input)
	var output commandOutput
	command.Stdout = &output.stdout
	command.Stderr = &output.stderr
	err := command.Run()
	if err == nil {
		return output.stdout.Bytes(), 0, nil
	}
	if ctx.Err() != nil {
		return nil, -1, ctx.Err()
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		return output.stdout.Bytes(), exitError.ExitCode(), nil
	}
	return nil, -1, &Error{Kind: FailureCapability, Op: "execute-git", Err: err}
}

func isolatedGitEnvironment(environment []string) []string {
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
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_CONFIG_COUNT=0",
		"GIT_NO_LAZY_FETCH=1",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_OPTIONAL_LOCKS=0",
		"GIT_PAGER=cat",
	)
}

func stringLine(output []byte) string {
	return strings.TrimSuffix(string(output), "\n")
}
