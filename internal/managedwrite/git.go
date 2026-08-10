package managedwrite

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/ontopix/engram/internal/gitraw"
	"github.com/ontopix/engram/internal/managedread"
	"github.com/ontopix/engram/internal/treeimage"
)

type repositoryObservation struct {
	repository  *gitraw.Repository
	headBytes   []byte
	indexBytes  []byte
	indexExists bool
	indexMode   fs.FileMode
}

func stableRepository(ctx context.Context, root string) (*repositoryObservation, error) {
	first, err := observeRepository(ctx, root)
	if err != nil {
		return nil, err
	}
	second, err := observeRepository(ctx, root)
	if err != nil {
		return nil, err
	}
	if !sameObservation(first, second) {
		return nil, typed(FailureConcurrency, PhaseCaptured, ErrConcurrent)
	}
	return first, nil
}

func observeRepository(ctx context.Context, root string) (*repositoryObservation, error) {
	repository, err := gitraw.Discover(ctx, root)
	if err != nil {
		return nil, err
	}
	headBytes, err := readRealFile(filepath.Join(repository.GitDir, "HEAD"))
	if err != nil {
		return nil, fmt.Errorf("read symbolic HEAD: %w", err)
	}
	indexBytes, exists, mode, err := readOptionalRealFile(filepath.Join(repository.GitDir, "index"))
	if err != nil {
		return nil, fmt.Errorf("read exact Git index: %w", err)
	}
	return &repositoryObservation{
		repository: repository, headBytes: headBytes, indexBytes: indexBytes,
		indexExists: exists, indexMode: mode,
	}, nil
}

func sameObservation(left, right *repositoryObservation) bool {
	if left == nil || right == nil || !sameRepository(left.repository, right.repository) {
		return false
	}
	return bytes.Equal(left.headBytes, right.headBytes) &&
		left.indexExists == right.indexExists &&
		left.indexMode == right.indexMode &&
		bytes.Equal(left.indexBytes, right.indexBytes)
}

func sameRepository(left, right *gitraw.Repository) bool {
	if left == nil || right == nil || left.Root != right.Root || left.GitDir != right.GitDir ||
		left.CommonGitDir != right.CommonGitDir || left.HeadRef != right.HeadRef || left.Format != right.Format {
		return false
	}
	if left.Head == nil || right.Head == nil {
		return left.Head == nil && right.Head == nil
	}
	return left.Head.Equal(*right.Head)
}

func readRealFile(name string) ([]byte, error) {
	data, exists, _, err := readOptionalRealFile(name)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, os.ErrNotExist
	}
	return data, nil
}

func readOptionalRealFile(name string) ([]byte, bool, fs.FileMode, error) {
	before, err := os.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, 0, nil
	}
	if err != nil {
		return nil, false, 0, err
	}
	if before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() {
		return nil, false, 0, fmt.Errorf("%s is not a real regular file", name)
	}
	data, err := os.ReadFile(name)
	if err != nil {
		return nil, false, 0, err
	}
	after, err := os.Lstat(name)
	if err != nil || after.Mode()&os.ModeSymlink != 0 || !after.Mode().IsRegular() || !os.SameFile(before, after) {
		return nil, false, 0, fmt.Errorf("%s changed while being read", name)
	}
	return data, true, before.Mode().Perm(), nil
}

type gitClient struct {
	executable string
	root       string
}

func newGitClient(root string) (*gitClient, error) {
	executable, err := exec.LookPath("git")
	if err != nil {
		return nil, err
	}
	return &gitClient{executable: executable, root: root}, nil
}

type gitResult struct {
	stdout []byte
	stderr []byte
	status int
}

type synchronizedBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (b *synchronizedBuffer) Write(value []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.Write(value)
}

func (b *synchronizedBuffer) bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.buffer.Bytes()...)
}

func (g *gitClient) run(ctx context.Context, input []byte, extraEnvironment []string, arguments ...string) (gitResult, error) {
	global := []string{
		"--no-pager", "--no-optional-locks", "--no-replace-objects",
		"-c", "core.hooksPath=" + os.DevNull, "-c", "core.fsmonitor=false", "-c", "core.untrackedCache=false",
		"-c", "maintenance.auto=false", "-c", "gc.auto=0", "-c", "core.fsync=loose-object", "-C", g.root,
	}
	command := exec.CommandContext(ctx, g.executable, append(global, arguments...)...)
	command.Env = append(isolatedGitEnvironment(os.Environ()), extraEnvironment...)
	command.Stdin = bytes.NewReader(input)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	if err == nil {
		return gitResult{stdout: stdout.Bytes(), stderr: stderr.Bytes(), status: 0}, nil
	}
	if ctx.Err() != nil {
		return gitResult{stdout: stdout.Bytes(), stderr: stderr.Bytes(), status: -1}, ctx.Err()
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		return gitResult{stdout: stdout.Bytes(), stderr: stderr.Bytes(), status: exitError.ExitCode()}, nil
	}
	return gitResult{stdout: stdout.Bytes(), stderr: stderr.Bytes(), status: -1}, err
}

func (g *gitClient) require(ctx context.Context, input []byte, environment []string, arguments ...string) ([]byte, error) {
	result, err := g.run(ctx, input, environment, arguments...)
	if err != nil {
		return nil, err
	}
	if result.status != 0 {
		return nil, fmt.Errorf("git %s exited %d: %s", strings.Join(arguments, " "), result.status, strings.TrimSpace(string(result.stderr)))
	}
	return result.stdout, nil
}

func isolatedGitEnvironment(environment []string) []string {
	// Git receives an explicit capability-oriented allowlist. In particular,
	// credentials, agent sockets, identity overrides, tracing, and every GIT_ /
	// ENGRAM_ overlay are absent rather than merely ignored by this package.
	allowed := map[string]bool{
		"PATH": true, "HOME": true, "XDG_CONFIG_HOME": true,
		"TMPDIR": true, "TMP": true, "TEMP": true,
		"SYSTEMROOT": true, "WINDIR": true,
		"LANG": true, "LC_ALL": true, "LC_CTYPE": true,
	}
	result := make([]string, 0, len(environment)+9)
	for _, item := range environment {
		name, _, _ := strings.Cut(item, "=")
		upper := strings.ToUpper(name)
		if strings.HasPrefix(upper, "GIT_") || strings.HasPrefix(upper, "ENGRAM_") || !allowed[upper] {
			continue
		}
		result = append(result, item)
	}
	sort.Strings(result)
	return append(result,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_CONFIG_COUNT=0",
		"GIT_NO_LAZY_FETCH=1",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_OPTIONAL_LOCKS=0",
		"GIT_PAGER=cat",
		"LC_ALL=C",
	)
}

func presentationEnvironment() []byte {
	values := make([]string, 0, 2)
	for _, name := range []string{"HOME", "XDG_CONFIG_HOME"} {
		if value, present := os.LookupEnv(name); present {
			values = append(values, name+"="+value)
		}
	}
	return []byte(strings.Join(values, "\x00"))
}

func createCandidateIndex(ctx context.Context, git *gitClient, repository *gitraw.Repository, image treeimage.Image, temporaryRoot string) (indexPath string, indexBytes []byte, treeID string, cleanup func() error, err error) {
	parent, err := privateTempDir(temporaryRoot, repository.Root, "engram-managed-index-")
	if err != nil {
		return "", nil, "", nil, err
	}
	cleanup = func() error { return os.RemoveAll(parent) }
	failed := true
	defer func() {
		if failed {
			_ = cleanup()
		}
	}()
	indexPath = filepath.Join(parent, "index")
	environment := []string{"GIT_INDEX_FILE=" + indexPath}
	if _, err := git.require(ctx, nil, environment, "read-tree", "--empty"); err != nil {
		return "", nil, "", nil, fmt.Errorf("initialize disposable index: %w", err)
	}

	paths := treeimage.SortedPaths(image)
	var updates bytes.Buffer
	for _, name := range paths {
		entry := image[name]
		if entry.Kind == treeimage.Directory {
			continue
		}
		if entry.Kind != treeimage.Regular {
			return "", nil, "", nil, fmt.Errorf("final candidate contains non-regular path %q", name)
		}
		output, err := git.require(ctx, entry.Data, nil, "hash-object", "-w", "--no-filters", "--stdin")
		if err != nil {
			return "", nil, "", nil, fmt.Errorf("write candidate blob %q: %w", name, err)
		}
		oid := strings.TrimSuffix(string(output), "\n")
		if _, err := gitraw.ParseOID(repository.Format, oid); err != nil {
			return "", nil, "", nil, fmt.Errorf("invalid candidate blob ID for %q: %w", name, err)
		}
		mode := gitraw.ModeRegular
		if entry.Mode&0o111 != 0 {
			mode = gitraw.ModeExecutable
		}
		fmt.Fprintf(&updates, "%s %s\t%s%c", mode, oid, name, byte(0))
	}
	if _, err := git.require(ctx, updates.Bytes(), environment, "update-index", "-z", "--index-info"); err != nil {
		return "", nil, "", nil, fmt.Errorf("construct disposable index: %w", err)
	}
	output, err := git.require(ctx, nil, environment, "write-tree")
	if err != nil {
		return "", nil, "", nil, fmt.Errorf("write candidate tree: %w", err)
	}
	treeID = strings.TrimSuffix(string(output), "\n")
	if _, err := gitraw.ParseOID(repository.Format, treeID); err != nil {
		return "", nil, "", nil, fmt.Errorf("invalid candidate tree ID: %w", err)
	}
	indexBytes, err = readRealFile(indexPath)
	if err != nil {
		return "", nil, "", nil, err
	}
	failed = false
	return indexPath, indexBytes, treeID, cleanup, nil
}

func initializeCapturedIndex(ctx context.Context, git *gitClient, repository *gitraw.Repository, observation *repositoryObservation, temporaryRoot string) (string, func() error, error) {
	parent, err := privateTempDir(temporaryRoot, repository.Root, "engram-managed-capture-")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() error { return os.RemoveAll(parent) }
	indexPath := filepath.Join(parent, "index")
	if observation.indexExists {
		mode := observation.indexMode
		if mode == 0 {
			mode = 0o600
		}
		if err := os.WriteFile(indexPath, observation.indexBytes, mode); err != nil {
			_ = cleanup()
			return "", nil, err
		}
	} else {
		environment := []string{"GIT_INDEX_FILE=" + indexPath}
		if _, err := git.require(ctx, nil, environment, "read-tree", "--empty"); err != nil {
			_ = cleanup()
			return "", nil, fmt.Errorf("materialize absent captured index: %w", err)
		}
	}
	return filepath.Clean(indexPath), cleanup, nil
}

func privateTempDir(base, worktree, prefix string) (string, error) {
	if base == "" {
		base = os.TempDir()
	}
	canonicalBase, err := filepath.EvalSymlinks(base)
	if err != nil {
		return "", err
	}
	canonicalBase, err = filepath.Abs(canonicalBase)
	if err != nil {
		return "", err
	}
	canonicalWorktree, err := filepath.EvalSymlinks(worktree)
	if err != nil {
		return "", err
	}
	canonicalWorktree, err = filepath.Abs(canonicalWorktree)
	if err != nil {
		return "", err
	}
	if within(canonicalWorktree, canonicalBase) {
		return "", fmt.Errorf("temporary root is inside the live worktree")
	}
	directory, err := os.MkdirTemp(canonicalBase, prefix)
	if err != nil {
		return "", err
	}
	if within(canonicalWorktree, directory) {
		_ = os.RemoveAll(directory)
		return "", fmt.Errorf("temporary directory is inside the live worktree")
	}
	return directory, nil
}

func within(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

type identity struct {
	name  string
	email string
}

func configuredIdentity(ctx context.Context, git *gitClient) (identity, error) {
	read := func(key string) (string, error) {
		result, err := git.run(ctx, nil, nil, "config", "--local", "--get", key)
		if err != nil {
			return "", err
		}
		if result.status == 1 {
			return "", fmt.Errorf("%s is not configured locally", key)
		}
		if result.status != 0 {
			return "", fmt.Errorf("read %s: %s", key, strings.TrimSpace(string(result.stderr)))
		}
		return strings.TrimSuffix(string(result.stdout), "\n"), nil
	}
	name, err := read("user.name")
	if err != nil {
		return identity{}, err
	}
	email, err := read("user.email")
	if err != nil {
		return identity{}, err
	}
	if !validIdentityPart(name, false) || !validIdentityPart(email, true) {
		return identity{}, fmt.Errorf("configured Git identity is not representable in one raw commit header")
	}
	return identity{name: name, email: email}, nil
}

func validIdentityPart(value string, email bool) bool {
	if value == "" || !utf8.ValidString(value) || strings.ContainsAny(value, "\x00\r\n<>") {
		return false
	}
	if email && strings.TrimSpace(value) != value {
		return false
	}
	return true
}

func validateMessage(message string) error {
	if message == "" || !utf8.ValidString(message) || strings.ContainsAny(message, "\x00\r") || strings.HasSuffix(message, "\n") {
		return fmt.Errorf("message must be non-empty UTF-8 without NUL, CR, or final LF")
	}
	return nil
}

func createCommit(ctx context.Context, git *gitClient, repository *gitraw.Repository, treeID string, parent *gitraw.OID, who identity, when time.Time, message string) (string, error) {
	zone := when.Format("-0700")
	seconds := strconv.FormatInt(when.Unix(), 10)
	ident := who.name + " <" + who.email + "> " + seconds + " " + zone
	var raw bytes.Buffer
	fmt.Fprintf(&raw, "tree %s\n", treeID)
	if parent != nil {
		fmt.Fprintf(&raw, "parent %s\n", parent.String())
	}
	fmt.Fprintf(&raw, "author %s\ncommitter %s\n\n%s\n", ident, ident, message)
	output, err := git.require(ctx, raw.Bytes(), nil, "hash-object", "-t", "commit", "-w", "--no-filters", "--stdin")
	if err != nil {
		return "", err
	}
	commitID := strings.TrimSuffix(string(output), "\n")
	if _, err := gitraw.ParseOID(repository.Format, commitID); err != nil {
		return "", fmt.Errorf("Git returned invalid commit ID: %w", err)
	}
	return commitID, nil
}

type casOutcome uint8

const (
	casUpdated casOutcome = iota + 1
	casRejected
	casUnknown
)

func updateRefCAS(ctx context.Context, git *gitClient, repository *gitraw.Repository, newID string, old *gitraw.OID) (casOutcome, error) {
	return updateRefPreparedCAS(ctx, git, repository, newID, old, func() error { return nil })
}

// updateRefPreparedCAS holds Git's native ref transaction in prepared state
// while recheck runs. Before commit is sent every failure is definitively
// non-updating; after it is sent only an exact commit acknowledgement and
// successful process exit establish a known update.
func updateRefPreparedCAS(ctx context.Context, git *gitClient, repository *gitraw.Repository, newID string, old *gitraw.OID, recheck func() error) (outcome casOutcome, resultErr error) {
	global := []string{
		"--no-pager", "--no-optional-locks", "--no-replace-objects",
		"-c", "core.hooksPath=" + os.DevNull, "-c", "core.fsmonitor=false", "-c", "core.untrackedCache=false",
		"-c", "maintenance.auto=false", "-c", "gc.auto=0", "-c", "core.fsync=reference", "-C", git.root,
	}
	command := exec.CommandContext(ctx, git.executable, append(global, "update-ref", "--no-deref", "--stdin", "-m", "engram managed acceptance")...)
	command.Env = isolatedGitEnvironment(os.Environ())
	stdin, err := command.StdinPipe()
	if err != nil {
		return casRejected, err
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return casRejected, err
	}
	var stderr synchronizedBuffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		return casRejected, err
	}
	writer := bufio.NewWriter(stdin)
	reader := bufio.NewReader(stdout)
	waited := false
	defer func() {
		if waited {
			return
		}
		_ = stdin.Close()
		_ = command.Process.Kill()
		_ = command.Wait()
	}()
	send := func(value string) error {
		if _, err := writer.WriteString(value); err != nil {
			return err
		}
		return writer.Flush()
	}
	expect := func(want string) error {
		line, err := reader.ReadString('\n')
		if err != nil {
			return errors.Join(err, stderrErrorCopy(stderr.bytes()))
		}
		if line != want+"\n" {
			return fmt.Errorf("update-ref response %q, want %q", strings.TrimSuffix(line, "\n"), want)
		}
		return nil
	}
	if err := send("start\n"); err != nil {
		return casRejected, err
	}
	if err := expect("start: ok"); err != nil {
		return casRejected, err
	}
	if old == nil {
		if err := send("create " + repository.HeadRef + " " + newID + "\nprepare\n"); err != nil {
			return casRejected, err
		}
	} else if err := send("update " + repository.HeadRef + " " + newID + " " + old.String() + "\nprepare\n"); err != nil {
		return casRejected, err
	}
	if err := expect("prepare: ok"); err != nil {
		// The command and transaction protocol were already accepted. With the
		// new object and refname prevalidated, prepare rejection is the native
		// compare-and-swap reporting that the expected old value no longer
		// matches (or that another ref transaction holds the ref).
		return casRejected, errors.Join(ErrConcurrent, err)
	}
	if err := recheck(); err != nil {
		abortErr := send("abort\n")
		if abortErr == nil {
			abortErr = expect("abort: ok")
		}
		_ = stdin.Close()
		waitErr := command.Wait()
		waited = true
		return casRejected, errors.Join(err, abortErr, waitErr, stderrErrorCopy(stderr.bytes()))
	}

	// The outcome becomes unknown before attempting the write: a short write,
	// signal, or EOF can occur after Git consumed the commit verb.
	outcome = casUnknown
	if err := send("commit\n"); err != nil {
		return outcome, err
	}
	if err := expect("commit: ok"); err != nil {
		return outcome, err
	}
	if err := stdin.Close(); err != nil {
		return outcome, err
	}
	if err := command.Wait(); err != nil {
		waited = true
		return outcome, errors.Join(err, stderrErrorCopy(stderr.bytes()))
	}
	waited = true
	return casUpdated, nil
}

func stderrErrorCopy(value []byte) error {
	value = bytes.TrimSpace(value)
	if len(value) == 0 {
		return nil
	}
	return errors.New(string(value))
}

func currentRef(ctx context.Context, root, expectedRef string) (*gitraw.Repository, error) {
	repository, err := gitraw.Discover(ctx, root)
	if err != nil {
		return nil, err
	}
	if repository.HeadRef != expectedRef {
		return nil, fmt.Errorf("symbolic HEAD names %q, want %q", repository.HeadRef, expectedRef)
	}
	return repository, nil
}

func syncDirectory(name string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	directory, err := os.Open(name)
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}

func sameProjectedImage(view *managedread.SnapshotView, image treeimage.Image) bool {
	if view == nil || view.Snapshot == nil || view.Snapshot.Tree == nil {
		return false
	}
	projected, err := treeimage.FromSnapshot(view.Snapshot.Tree, view.Modes)
	return err == nil && treeimage.Equal(projected, image)
}
