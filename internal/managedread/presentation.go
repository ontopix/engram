package managedread

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/ontopix/engram/internal/checker"
	"github.com/ontopix/engram/internal/gitpath"
	"github.com/ontopix/engram/internal/gitraw"
)

// presentationFingerprint is a comparable digest of every presentation
// observation used by auditPresentation. Repeating the audit and comparing
// fingerprints detects changes without retaining mutable repository data.
type presentationFingerprint struct {
	sum [sha256.Size]byte
}

type presentationObservation struct {
	accepted    *checker.Snapshot
	fingerprint presentationFingerprint
}

func (f presentationFingerprint) Equal(other presentationFingerprint) bool { return f == other }
func (f presentationFingerprint) String() string                           { return hex.EncodeToString(f.sum[:]) }

// auditPresentation checks the current read-only evidence for annex-git
// section 2.3. It does not run filters, check out files, refresh the index, or
// write a filesystem probe. Consequently it proves exact round trips for
// observed paths, while a writer still has to prove the host's ability to
// create any future pathname as part of its write-side safety capture.
//
// accepted is the already checked snapshot for repository.Head. It may be nil
// for an unborn branch or when raw-history causality suppressed that snapshot.
func (s *Store) auditPresentation(ctx context.Context, repository *gitraw.Repository, accepted *checker.Snapshot) ([]checker.Finding, presentationFingerprint, error) {
	if s == nil || repository == nil {
		return nil, presentationFingerprint{}, fmt.Errorf("managedread: nil presentation input")
	}
	builder := newPresentationFingerprint()
	builder.add("root", []byte(repository.Root))
	issues := make([]string, 0)
	rootInfo, err := os.Lstat(repository.Root)
	if err != nil {
		return nil, presentationFingerprint{}, err
	}
	builder.addInt("root.mode", int(rootInfo.Mode()))
	if rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() || !filepath.IsAbs(repository.Root) || filepath.Clean(repository.Root) != repository.Root {
		issues = append(issues, "managed target is not the exact root of a real worktree directory")
	}
	topLevel, status, stderr, err := s.presentationGit(ctx, repository, nil,
		"rev-parse", "--path-format=absolute", "--show-toplevel")
	if err != nil {
		return nil, presentationFingerprint{}, err
	}
	builder.add("root.git.stdout", topLevel)
	builder.add("root.git.stderr", stderr)
	builder.addInt("root.git.status", status)
	gitRoot, rootPathErr := gitpath.Absolute(strings.TrimSuffix(string(topLevel), "\n"))
	if status != 0 || rootPathErr != nil || gitRoot != repository.Root {
		issues = append(issues, "managed target does not exactly match Git's worktree root")
	}

	config, status, stderr, err := s.presentationGit(ctx, repository, nil,
		"config", "--null", "--show-origin", "--show-scope", "--includes", "--list")
	if err != nil {
		return nil, presentationFingerprint{}, err
	}
	builder.add("config.stdout", config)
	builder.add("config.stderr", stderr)
	builder.addInt("config.status", status)
	if status != 0 {
		return nil, presentationFingerprint{}, presentationGitError("read-presentation-config", status, stderr)
	}
	for _, key := range []string{"core.autocrlf", "core.sparsecheckout", "index.sparse"} {
		value, present, valid, err := s.presentationBool(ctx, repository, key, builder)
		if err != nil {
			return nil, presentationFingerprint{}, err
		}
		if !valid {
			issues = append(issues, key+" is not a valid boolean")
			continue
		}
		if key == "core.autocrlf" {
			if present && value {
				issues = append(issues, "effective core.autocrlf is not false")
			}
			continue
		}
		if present && value {
			issues = append(issues, key+" enables sparse presentation")
		}
	}

	indexOutput, status, stderr, err := s.presentationGit(ctx, repository, nil,
		"ls-files", "--stage", "--debug", "--full-name", "-z", "--sparse",
		"--abbrev="+fmt.Sprint(repository.Format.HexWidth()))
	if err != nil {
		return nil, presentationFingerprint{}, err
	}
	builder.add("index.stdout", indexOutput)
	builder.add("index.stderr", stderr)
	builder.addInt("index.status", status)
	var indexEntries []IndexEntry
	if status != 0 {
		issues = append(issues, "Git cannot present the real index")
	} else {
		indexEntries, err = parseIndexListing(repository.Format, indexOutput)
		if err != nil {
			issues = append(issues, "real index has no reversible pathname projection")
		} else {
			for _, entry := range indexEntries {
				if entry.Mode == gitraw.TreeMode("040000") {
					issues = append(issues, "real index contains a sparse directory entry")
					break
				}
			}
			for _, entry := range indexEntries {
				if entry.SkipWorktree {
					issues = append(issues, "real index contains skip-worktree entries")
					break
				}
			}
		}
	}

	paths := make(map[string]struct{})
	addSnapshotPaths(paths, accepted)
	for _, entry := range indexEntries {
		if entry.Mode.IsRegular() && validIndexPath(entry.Path) {
			paths[entry.Path] = struct{}{}
		}
	}
	working, err := checker.CheckFS(repository.Root)
	if err != nil {
		return nil, presentationFingerprint{}, err
	}
	addSnapshotPaths(paths, working)
	orderedPaths := sortedStringKeys(paths)
	for _, name := range orderedPaths {
		builder.add("logical-path", []byte(name))
	}

	attributeIssues, err := s.auditAttributes(ctx, repository, orderedPaths, builder)
	if err != nil {
		return nil, presentationFingerprint{}, err
	}
	issues = append(issues, attributeIssues...)

	pathIssues, err := auditObservedPaths(repository.Root, orderedPaths, builder)
	if err != nil {
		return nil, presentationFingerprint{}, err
	}
	issues = append(issues, pathIssues...)
	sort.Strings(issues)
	issues = compactStrings(issues)

	fingerprint := builder.finish()
	if len(issues) == 0 {
		return nil, fingerprint, nil
	}
	return []checker.Finding{{Code: "E601", Path: ".", Detail: strings.Join(issues, "; ")}}, fingerprint, nil
}

func addSnapshotPaths(paths map[string]struct{}, value *checker.Snapshot) {
	if value == nil || value.Tree == nil {
		return
	}
	for name := range value.Tree.Files {
		paths[name] = struct{}{}
	}
}

func (s *Store) presentationBool(ctx context.Context, repository *gitraw.Repository, key string, builder *presentationFingerprintBuilder) (value, present, valid bool, err error) {
	output, status, stderr, err := s.presentationGit(ctx, repository, nil,
		"config", "--type=bool", "--includes", "--get", key)
	if err != nil {
		return false, false, false, err
	}
	builder.add(key+".stdout", output)
	builder.add(key+".stderr", stderr)
	builder.addInt(key+".status", status)
	switch status {
	case 0:
		value := strings.TrimSuffix(string(output), "\n")
		if value == "true" {
			return true, true, true, nil
		}
		if value == "false" {
			return false, true, true, nil
		}
		return false, true, false, nil
	case 1:
		return false, false, true, nil
	default:
		// Git uses a failing status for a present value that cannot be parsed as
		// bool. A raw query distinguishes that E601 input from an unavailable
		// Git/configuration capability.
		raw, rawStatus, rawStderr, rawErr := s.presentationGit(ctx, repository, nil,
			"config", "--includes", "--get", key)
		if rawErr != nil {
			return false, false, false, rawErr
		}
		builder.add(key+".raw.stdout", raw)
		builder.add(key+".raw.stderr", rawStderr)
		builder.addInt(key+".raw.status", rawStatus)
		if rawStatus == 0 {
			return false, true, false, nil
		}
		return false, false, false, presentationGitError("read-presentation-config", status, stderr)
	}
}

var presentationAttributes = []string{"text", "eol", "filter", "ident", "working-tree-encoding"}

func (s *Store) auditAttributes(ctx context.Context, repository *gitraw.Repository, paths []string, builder *presentationFingerprintBuilder) ([]string, error) {
	if len(paths) == 0 {
		builder.add("attributes", nil)
		return nil, nil
	}
	var input bytes.Buffer
	for _, name := range paths {
		input.WriteString(name)
		input.WriteByte(0)
	}
	arguments := append([]string{"check-attr", "-z", "--stdin"}, presentationAttributes...)
	output, status, stderr, err := s.presentationGit(ctx, repository, input.Bytes(), arguments...)
	if err != nil {
		return nil, err
	}
	builder.add("attributes.stdout", output)
	builder.add("attributes.stderr", stderr)
	builder.addInt("attributes.status", status)
	if status != 0 {
		return nil, presentationGitError("check-presentation-attributes", status, stderr)
	}
	fields := bytes.Split(output, []byte{0})
	if len(fields) != 0 && len(fields[len(fields)-1]) == 0 {
		fields = fields[:len(fields)-1]
	}
	if len(fields)%3 != 0 {
		return nil, &gitraw.Error{Kind: gitraw.FailureRepository, Op: "check-presentation-attributes", Detail: "malformed check-attr output"}
	}
	issues := make([]string, 0)
	seen := make(map[string]map[string]bool, len(paths))
	for offset := 0; offset < len(fields); offset += 3 {
		name, attribute, value := string(fields[offset]), string(fields[offset+1]), string(fields[offset+2])
		if !utf8.Valid(fields[offset]) || !utf8.Valid(fields[offset+1]) || !utf8.Valid(fields[offset+2]) {
			return nil, &gitraw.Error{Kind: gitraw.FailureRepository, Op: "check-presentation-attributes", Detail: "non-UTF-8 check-attr output"}
		}
		if _, ok := pathsLookup(paths, name); !ok || !containsPresentationAttribute(attribute) {
			return nil, &gitraw.Error{Kind: gitraw.FailureRepository, Op: "check-presentation-attributes", Detail: "unexpected check-attr record"}
		}
		if seen[name] == nil {
			seen[name] = make(map[string]bool)
		}
		if seen[name][attribute] {
			return nil, &gitraw.Error{Kind: gitraw.FailureRepository, Op: "check-presentation-attributes", Detail: "duplicate check-attr record"}
		}
		seen[name][attribute] = true
		allowed := value == "unspecified"
		if attribute == "text" {
			allowed = allowed || value == "unset"
		}
		if !allowed {
			issues = append(issues, fmt.Sprintf("attribute %s=%s transforms %s", attribute, value, name))
		}
	}
	if len(fields)/3 != len(paths)*len(presentationAttributes) {
		return nil, &gitraw.Error{Kind: gitraw.FailureRepository, Op: "check-presentation-attributes", Detail: "incomplete check-attr output"}
	}
	return issues, nil
}

func containsPresentationAttribute(want string) bool {
	for _, attribute := range presentationAttributes {
		if attribute == want {
			return true
		}
	}
	return false
}

func pathsLookup(paths []string, want string) (int, bool) {
	index := sort.SearchStrings(paths, want)
	return index, index < len(paths) && paths[index] == want
}

func auditObservedPaths(root string, paths []string, builder *presentationFingerprintBuilder) ([]string, error) {
	issues := make([]string, 0)
	for _, logicalPath := range paths {
		directory := root
		for _, component := range strings.Split(logicalPath, "/") {
			entries, err := os.ReadDir(directory)
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					builder.add("path.absent-parent", []byte(logicalPath))
					break
				}
				return nil, err
			}
			var exact os.DirEntry
			for _, entry := range entries {
				if entry.Name() == component {
					exact = entry
					break
				}
			}
			next := filepath.Join(directory, component)
			if exact == nil {
				if _, err := os.Lstat(next); err == nil {
					issues = append(issues, "filesystem changes pathname bytes for "+logicalPath)
					builder.add("path.aliased", []byte(logicalPath))
				} else if errors.Is(err, os.ErrNotExist) {
					builder.add("path.absent", []byte(logicalPath))
				} else {
					return nil, err
				}
				break
			}
			builder.add("path.component", []byte(exact.Name()))
			builder.addInt("path.kind", int(exact.Type()))
			info, err := exact.Info()
			if err != nil {
				return nil, err
			}
			if info.Mode().IsRegular() && next == filepath.Join(root, filepath.FromSlash(logicalPath)) {
				file, err := os.Open(next)
				if err != nil {
					return nil, err
				}
				_, copyErr := io.Copy(io.Discard, file)
				closeErr := file.Close()
				if copyErr != nil {
					return nil, copyErr
				}
				if closeErr != nil {
					return nil, closeErr
				}
			}
			if !info.IsDir() && next != filepath.Join(root, filepath.FromSlash(logicalPath)) {
				break
			}
			directory = next
		}
	}
	return issues, nil
}

func (s *Store) presentationGit(ctx context.Context, repository *gitraw.Repository, input []byte, arguments ...string) ([]byte, int, []byte, error) {
	global := []string{
		"--no-pager", "--no-optional-locks", "--no-replace-objects",
		"-c", "core.hooksPath=" + os.DevNull, "-c", "core.fsmonitor=false", "-c", "core.untrackedCache=false",
		"-c", "maintenance.auto=false", "-c", "gc.auto=0", "-C", repository.Root,
	}
	command := exec.CommandContext(ctx, s.git, append(global, arguments...)...)
	command.Env = isolatedGitEnvironment(os.Environ())
	command.Stdin = bytes.NewReader(input)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	if err == nil {
		return stdout.Bytes(), 0, stderr.Bytes(), nil
	}
	if ctx.Err() != nil {
		return nil, -1, nil, ctx.Err()
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		return stdout.Bytes(), exitError.ExitCode(), stderr.Bytes(), nil
	}
	return nil, -1, nil, &gitraw.Error{Kind: gitraw.FailureCapability, Op: "execute-presentation-git", Err: err}
}

func presentationGitError(op string, status int, stderr []byte) error {
	detail := fmt.Sprintf("Git exited with status %d", status)
	if message := strings.TrimSpace(string(stderr)); message != "" {
		detail += ": " + message
	}
	return &gitraw.Error{Kind: gitraw.FailureRepository, Op: op, Detail: detail}
}

func compactStrings(values []string) []string {
	if len(values) < 2 {
		return values
	}
	result := values[:1]
	for _, value := range values[1:] {
		if value != result[len(result)-1] {
			result = append(result, value)
		}
	}
	return result
}

type presentationFingerprintBuilder struct{ hash hash.Hash }

func newPresentationFingerprint() *presentationFingerprintBuilder {
	return &presentationFingerprintBuilder{hash: sha256.New()}
}

func (b *presentationFingerprintBuilder) add(label string, value []byte) {
	b.writeFrame([]byte(label))
	b.writeFrame(value)
}

func (b *presentationFingerprintBuilder) addInt(label string, value int) {
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], uint64(value))
	b.add(label, encoded[:])
}

func (b *presentationFingerprintBuilder) writeFrame(value []byte) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	_, _ = b.hash.Write(length[:])
	_, _ = b.hash.Write(value)
}

func (b *presentationFingerprintBuilder) finish() presentationFingerprint {
	var fingerprint presentationFingerprint
	copy(fingerprint.sum[:], b.hash.Sum(nil))
	return fingerprint
}
