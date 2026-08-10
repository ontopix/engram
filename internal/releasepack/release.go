// Package releasepack builds reproducible cross-platform release archives for
// the reference CLI using only the Go toolchain and standard library.
package releasepack

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	buildversion "github.com/ontopix/engram/internal/version"
	canonicalskills "github.com/ontopix/engram/skills"
)

type Target struct {
	OS   string `json:"os"`
	Arch string `json:"arch"`
}

var DefaultTargets = []Target{
	{OS: "darwin", Arch: "amd64"},
	{OS: "darwin", Arch: "arm64"},
	{OS: "linux", Arch: "amd64"},
	{OS: "linux", Arch: "arm64"},
	{OS: "windows", Arch: "amd64"},
	{OS: "windows", Arch: "arm64"},
}

type Options struct {
	Version         string
	Revision        string
	Output          string
	SourceDateEpoch int64
	Targets         []Target
}

type Artifact struct {
	Name   string `json:"name"`
	OS     string `json:"os"`
	Arch   string `json:"arch"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

type Provenance struct {
	Version         int        `json:"version"`
	CLI             string     `json:"cli"`
	Revision        string     `json:"revision"`
	Builder         string     `json:"builder"`
	SourceDateEpoch int64      `json:"source_date_epoch"`
	Artifacts       []Artifact `json:"artifacts"`
}

type asset struct {
	name string
	data []byte
	mode fs.FileMode
}

const maxPortableSourceDateEpoch int64 = 1<<32 - 1

// Build cross-compiles every requested target and publishes archives,
// provenance-v1.json, and SHA256SUMS into an existing or newly created output
// directory. Existing artifact names are never overwritten.
func Build(ctx context.Context, repository string, options Options) ([]Artifact, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	root, err := canonicalRepository(repository)
	if err != nil {
		return nil, err
	}
	version, label, err := normalizeVersion(options.Version)
	if err != nil {
		return nil, err
	}
	if version != buildversion.CLIVersion {
		return nil, fmt.Errorf("release version %q differs from the exact source CLI version %q", version, buildversion.CLIVersion)
	}
	if !validRevision(options.Revision) {
		return nil, errors.New("release revision must be one full lowercase SHA-1 or SHA-256 object ID")
	}
	if options.SourceDateEpoch < 0 || options.SourceDateEpoch > maxPortableSourceDateEpoch {
		return nil, errors.New("release source-date epoch must fit the portable ZIP timestamp range")
	}
	if err := verifyReleaseIdentity(ctx, root, options.Revision); err != nil {
		return nil, err
	}
	source, tracked, err := materializeReleaseSource(ctx, root, options.Revision)
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(source)
	if err := verifyCleanReleaseWorktree(root, source, tracked); err != nil {
		return nil, err
	}
	if err := verifyReleaseToolchain(ctx, source); err != nil {
		return nil, err
	}
	if err := verifyModules(ctx, source); err != nil {
		return nil, err
	}
	output := options.Output
	if output == "" {
		output = filepath.Join(root, "dist")
	} else if !filepath.IsAbs(output) {
		output = filepath.Join(root, output)
	}
	output = filepath.Clean(output)
	if err := prepareOutputDirectory(output); err != nil {
		return nil, err
	}
	output, err = filepath.EvalSymlinks(output)
	if err != nil {
		return nil, err
	}
	targets, err := normalizeTargets(options.Targets)
	if err != nil {
		return nil, err
	}
	static, err := releaseAssets(ctx, source, tracked, targets)
	if err != nil {
		return nil, err
	}
	artifactNames := make([]string, 0, len(targets)+2)
	for _, target := range targets {
		extension := ".tar.gz"
		if target.OS == "windows" {
			extension = ".zip"
		}
		artifactNames = append(artifactNames, "engram-"+label+"-"+target.OS+"-"+target.Arch+extension)
	}
	artifactNames = append(artifactNames, "provenance-v1.json", "SHA256SUMS")
	for _, name := range artifactNames {
		if err := requireAbsent(filepath.Join(output, name)); err != nil {
			return nil, err
		}
	}
	temporary, err := os.MkdirTemp(output, ".engram-release-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(temporary)

	artifacts := make([]Artifact, 0, len(targets))
	for _, target := range targets {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		binaryName := "engram"
		if target.OS == "windows" {
			binaryName += ".exe"
		}
		binary := filepath.Join(temporary, target.OS+"-"+target.Arch+"-"+binaryName)
		if err := buildBinary(ctx, source, binary, version, options.Revision, target); err != nil {
			return nil, err
		}
		binaryBytes, err := os.ReadFile(binary)
		if err != nil {
			return nil, err
		}
		contents := append([]asset{{name: binaryName, data: binaryBytes, mode: 0o755}}, cloneAssets(static)...)
		base := "engram-" + label + "-" + target.OS + "-" + target.Arch
		extension := ".tar.gz"
		if target.OS == "windows" {
			extension = ".zip"
		}
		name := base + extension
		staged := filepath.Join(temporary, name)
		if target.OS == "windows" {
			err = writeZIP(staged, base, contents, options.SourceDateEpoch)
		} else {
			err = writeTarGzip(staged, base, contents, options.SourceDateEpoch)
		}
		if err != nil {
			return nil, err
		}
		artifact, err := inspectArtifact(staged, name, target)
		if err != nil {
			return nil, err
		}
		artifacts = append(artifacts, artifact)
	}

	provenance := Provenance{
		Version: 1, CLI: version, Revision: options.Revision, Builder: runtime.Version(),
		SourceDateEpoch: options.SourceDateEpoch, Artifacts: append([]Artifact(nil), artifacts...),
	}
	provenanceBytes, err := encodeJSON(provenance)
	if err != nil {
		return nil, err
	}
	provenanceName := "provenance-v1.json"
	if _, err := stageBytes(temporary, provenanceName, provenanceBytes); err != nil {
		return nil, err
	}
	provenanceHash := sha256.Sum256(provenanceBytes)
	checksums := make([]string, 0, len(artifacts)+1)
	for _, artifact := range artifacts {
		checksums = append(checksums, artifact.SHA256+"  "+artifact.Name)
	}
	checksums = append(checksums, hex.EncodeToString(provenanceHash[:])+"  "+provenanceName)
	sort.Strings(checksums)
	if _, err := stageBytes(temporary, "SHA256SUMS", []byte(strings.Join(checksums, "\n")+"\n")); err != nil {
		return nil, err
	}
	published := make([]publishedFile, 0, len(artifactNames))
	for _, name := range artifactNames {
		item := publishedFile{staged: filepath.Join(temporary, name), destination: filepath.Join(output, name)}
		published = append(published, item)
		if err := publishFile(item.staged, item.destination); err != nil {
			return nil, errors.Join(err, rollbackPublished(published))
		}
	}
	if err := syncDirectory(output); err != nil {
		return nil, errors.Join(err, rollbackPublished(published))
	}
	return artifacts, nil
}

// verifyReleaseSource is the complete source gate used by tests and
// embedders. Build splits the identity and worktree checks around its one
// exact-tree materialization so release inputs are never materialized twice.
func verifyReleaseSource(ctx context.Context, root, revision string) error {
	if err := verifyReleaseIdentity(ctx, root, revision); err != nil {
		return err
	}
	source, tracked, err := materializeReleaseSource(ctx, root, revision)
	if err != nil {
		return err
	}
	defer os.RemoveAll(source)
	return verifyCleanReleaseWorktree(root, source, tracked)
}

func verifyReleaseIdentity(ctx context.Context, root, revision string) error {
	run := func(arguments ...string) ([]byte, error) {
		command := exec.CommandContext(ctx, "git", append(releaseGitPrefix(root), arguments...)...)
		command.Env = isolatedReleaseGitEnvironment(os.Environ())
		var stdout, stderr bytes.Buffer
		command.Stdout = &stdout
		command.Stderr = &stderr
		if err := command.Run(); err != nil {
			return nil, fmt.Errorf("verify release source with git %s: %w: %s", strings.Join(arguments, " "), err, bounded(stderr.String()))
		}
		return stdout.Bytes(), nil
	}
	head, err := run("rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return err
	}
	if strings.TrimSuffix(string(head), "\n") != revision {
		return errors.New("release revision does not equal the checked-out HEAD commit")
	}
	worktree, err := run("rev-parse", "--show-toplevel")
	if err != nil {
		return err
	}
	worktreeRoot, err := filepath.Abs(strings.TrimSpace(string(worktree)))
	if err != nil {
		return err
	}
	worktreeRoot, err = filepath.EvalSymlinks(filepath.Clean(worktreeRoot))
	if err != nil {
		return err
	}
	requestedInfo, requestedErr := os.Lstat(root)
	worktreeInfo, worktreeErr := os.Lstat(worktreeRoot)
	if requestedErr != nil || worktreeErr != nil || !os.SameFile(requestedInfo, worktreeInfo) {
		return errors.Join(requestedErr, worktreeErr, errors.New("release Git worktree differs from the requested repository root"))
	}
	return nil
}

// materializeReleaseSource reconstructs the exact committed tree from Git
// objects. Release inputs therefore never depend on checkout filters, ignored
// files, line-ending settings, or worktree changes racing the build.
func materializeReleaseSource(ctx context.Context, root, revision string) (source string, tracked map[string]struct{}, resultErr error) {
	listing, err := releaseGitOutput(ctx, root, "ls-tree", "-r", "-z", "--full-tree", revision)
	if err != nil {
		return "", nil, err
	}
	source, err = os.MkdirTemp("", "engram-release-source-")
	if err != nil {
		return "", nil, err
	}
	defer func() {
		if resultErr != nil {
			resultErr = errors.Join(resultErr, os.RemoveAll(source))
		}
	}()

	tracked = make(map[string]struct{})
	for _, record := range bytes.Split(listing, []byte{0}) {
		if len(record) == 0 {
			continue
		}
		metadata, rawName, found := bytes.Cut(record, []byte{'\t'})
		fields := bytes.Fields(metadata)
		if !found || len(fields) != 3 {
			return "", nil, errors.New("exact release tree contains a malformed entry")
		}
		mode, objectType, objectID := string(fields[0]), string(fields[1]), string(fields[2])
		name := string(rawName)
		if !validReleaseSourcePath(name) || objectType != "blob" || mode != "100644" && mode != "100755" || !validRevision(objectID) {
			return "", nil, fmt.Errorf("exact release tree contains unsupported entry %q (%s %s)", name, mode, objectType)
		}
		if _, duplicate := tracked[name]; duplicate {
			return "", nil, fmt.Errorf("exact release tree repeats path %q", name)
		}
		data, err := releaseGitOutput(ctx, root, "cat-file", "blob", objectID)
		if err != nil {
			return "", nil, fmt.Errorf("materialize exact release source %s: %w", name, err)
		}
		destination := filepath.Join(source, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
			return "", nil, err
		}
		permissions := fs.FileMode(0o644)
		if mode == "100755" {
			permissions = 0o755
		}
		file, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, permissions)
		if err != nil {
			return "", nil, err
		}
		_, writeErr := file.Write(data)
		closeErr := file.Close()
		if writeErr != nil || closeErr != nil {
			return "", nil, errors.Join(writeErr, closeErr)
		}
		tracked[name] = struct{}{}
	}
	if len(tracked) == 0 {
		return "", nil, errors.New("exact release tree is empty")
	}
	return source, tracked, nil
}

// verifyCleanReleaseWorktree compares ordinary filesystem bytes with the
// already materialized commit and rejects every other worktree entry. It does
// not ask Git to inspect worktree content: `git status` and `diff-files` may
// execute repository-configured clean filters selected by attributes, which
// is outside release verification authority.
func verifyCleanReleaseWorktree(root, source string, tracked map[string]struct{}) error {
	seen := make(map[string]struct{}, len(tracked))
	err := filepath.WalkDir(root, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, name)
		if err != nil {
			return err
		}
		if relative == "." {
			return nil
		}
		logical := filepath.ToSlash(relative)
		if logical == ".git" {
			info, err := os.Lstat(name)
			if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() && !info.Mode().IsRegular() {
				return errors.Join(err, errors.New("release Git administration entry is unsafe"))
			}
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			if _, exists := tracked[logical]; exists {
				return fmt.Errorf("release tracked path %s is a directory", logical)
			}
			return nil
		}
		if _, exists := tracked[logical]; !exists {
			return fmt.Errorf("release source has untracked or ignored entry %s", logical)
		}

		worktreeInfo, err := os.Lstat(name)
		if err != nil || worktreeInfo.Mode()&os.ModeSymlink != 0 || !worktreeInfo.Mode().IsRegular() {
			return errors.Join(err, fmt.Errorf("release tracked path %s is not one real regular file", logical))
		}
		exactName := filepath.Join(source, filepath.FromSlash(logical))
		exactInfo, err := os.Lstat(exactName)
		if err != nil || exactInfo.Mode()&os.ModeSymlink != 0 || !exactInfo.Mode().IsRegular() {
			return errors.Join(err, fmt.Errorf("exact release source path %s is not one real regular file", logical))
		}
		if runtime.GOOS != "windows" && worktreeInfo.Mode().Perm()&0o111 != exactInfo.Mode().Perm()&0o111 {
			return fmt.Errorf("release tracked path %s has a different executable mode", logical)
		}
		worktreeBytes, err := readReleaseFile(root, logical)
		if err != nil {
			return err
		}
		exactBytes, err := readReleaseFile(source, logical)
		if err != nil {
			return err
		}
		if !bytes.Equal(worktreeBytes, exactBytes) {
			return fmt.Errorf("release tracked path %s differs from HEAD", logical)
		}
		seen[logical] = struct{}{}
		return nil
	})
	if err != nil {
		return err
	}
	if len(seen) != len(tracked) {
		missing := make([]string, 0, len(tracked)-len(seen))
		for logical := range tracked {
			if _, exists := seen[logical]; !exists {
				missing = append(missing, logical)
			}
		}
		sort.Strings(missing)
		return fmt.Errorf("release source is missing tracked path %s", missing[0])
	}
	return nil
}

func validReleaseSourcePath(name string) bool {
	if name == "" || !utf8.ValidString(name) || strings.Contains(name, "\\") || filepath.IsAbs(name) || filepath.Clean(filepath.FromSlash(name)) != filepath.FromSlash(name) {
		return false
	}
	for _, component := range strings.Split(name, "/") {
		if component == "" || component == "." || component == ".." || strings.EqualFold(component, ".git") {
			return false
		}
	}
	return true
}

func releaseGitOutput(ctx context.Context, root string, arguments ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, "git", append(releaseGitPrefix(root), arguments...)...)
	command.Env = isolatedReleaseGitEnvironment(os.Environ())
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(arguments, " "), err, bounded(stderr.String()))
	}
	return stdout.Bytes(), nil
}

func releaseGitPrefix(root string) []string {
	return []string{
		"--no-pager", "--no-optional-locks", "--no-replace-objects",
		"-c", "core.hooksPath=" + os.DevNull,
		"-c", "core.fsmonitor=false",
		"-c", "core.untrackedCache=false",
		"-c", "maintenance.auto=false",
		"-c", "gc.auto=0",
		"-C", root,
	}
}

func verifyModules(ctx context.Context, root string) error {
	command := exec.CommandContext(ctx, "go", "mod", "verify")
	command.Dir = root
	command.Env = isolatedGoEnvironment(os.Environ())
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Run(); err != nil {
		return fmt.Errorf("verify pinned module cache: %w: %s", err, bounded(output.String()))
	}
	return nil
}

// verifyReleaseToolchain binds the toolchain recorded in provenance to the
// toolchain that every child build will actually invoke. Without this check a
// release helper built by one Go version could silently find another `go` on
// PATH and misidentify the compiler used for the published binaries.
func verifyReleaseToolchain(ctx context.Context, root string) error {
	command := exec.CommandContext(ctx, "go", "env", "GOVERSION")
	command.Dir = root
	command.Env = isolatedGoEnvironment(os.Environ())
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("identify release Go toolchain: %w: %s", err, bounded(stderr.String()))
	}
	return requireMatchingToolchain(strings.TrimSpace(stdout.String()), runtime.Version())
}

func requireMatchingToolchain(observed, builder string) error {
	if observed != builder {
		return fmt.Errorf("release Go toolchain %q differs from invoking builder %q", observed, builder)
	}
	return nil
}

func isolatedReleaseGitEnvironment(environment []string) []string {
	result := make([]string, 0, len(environment)+8)
	for _, item := range environment {
		name, _, _ := strings.Cut(item, "=")
		upper := strings.ToUpper(name)
		if strings.HasPrefix(upper, "GIT_") || strings.HasPrefix(upper, "ENGRAM_") || upper == "LC_ALL" {
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
		"LC_ALL=C",
	)
}

func prepareOutputDirectory(name string) error {
	info, err := os.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(name, 0o755); err != nil {
			return err
		}
		info, err = os.Lstat(name)
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("release output is not one real directory")
	}
	return nil
}

func canonicalRepository(value string) (string, error) {
	if value == "" {
		value = "."
	}
	absolute, err := filepath.Abs(value)
	if err != nil {
		return "", err
	}
	root, err := filepath.EvalSymlinks(filepath.Clean(absolute))
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(root)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", errors.Join(err, errors.New("release repository is not one real directory"))
	}
	module, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil || !bytes.HasPrefix(module, []byte("module github.com/ontopix/engram\n")) {
		return "", errors.Join(err, errors.New("release root is not the engram module"))
	}
	return root, nil
}

func normalizeVersion(value string) (version, label string, err error) {
	label = value
	value = strings.TrimPrefix(value, "v")
	if value == "" || !validVersionPart(value) {
		return "", "", errors.New("release version must be vMAJOR.MINOR.PATCH with an optional ASCII prerelease")
	}
	if !strings.HasPrefix(label, "v") {
		label = "v" + label
	}
	return value, label, nil
}

func validVersionPart(value string) bool {
	core, suffix, _ := strings.Cut(value, "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return false
	}
	for _, part := range parts {
		if part == "" || len(part) > 1 && part[0] == '0' {
			return false
		}
		if _, err := strconv.ParseUint(part, 10, 64); err != nil {
			return false
		}
	}
	if suffix == "" {
		return !strings.Contains(value, "-")
	}
	for _, identifier := range strings.Split(suffix, ".") {
		if identifier == "" {
			return false
		}
		numeric := true
		for _, character := range identifier {
			if character < '0' || character > '9' {
				numeric = false
			}
			if character != '-' && (character < '0' || character > '9') && (character < 'A' || character > 'Z') && (character < 'a' || character > 'z') {
				return false
			}
		}
		if numeric && len(identifier) > 1 && identifier[0] == '0' {
			return false
		}
	}
	return true
}

func validRevision(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' && character < 'a' || character > 'f' {
			return false
		}
	}
	return true
}

func normalizeTargets(input []Target) ([]Target, error) {
	if len(input) == 0 {
		input = DefaultTargets
	}
	seen := make(map[string]struct{}, len(input))
	result := append([]Target(nil), input...)
	for _, target := range result {
		key := target.OS + "/" + target.Arch
		if !validTargetPart(target.OS) || !validTargetPart(target.Arch) {
			return nil, fmt.Errorf("invalid release target %q", key)
		}
		if _, exists := seen[key]; exists {
			return nil, fmt.Errorf("duplicate release target %q", key)
		}
		seen[key] = struct{}{}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].OS != result[j].OS {
			return result[i].OS < result[j].OS
		}
		return result[i].Arch < result[j].Arch
	})
	return result, nil
}

func validTargetPart(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range []byte(value) {
		if character < 'a' || character > 'z' {
			if character < '0' || character > '9' {
				return false
			}
		}
	}
	return true
}

func releaseAssets(ctx context.Context, root string, tracked map[string]struct{}, targets []Target) ([]asset, error) {
	manifest, err := canonicalskills.VerifiedManifest()
	if err != nil {
		return nil, err
	}
	names := []string{"LICENSE", "README.md", "THIRD_PARTY_NOTICES.md", "licenses/go/LICENSE.txt", "licenses/go/PATENTS.txt", "skills/manifest-v1.json"}
	for _, tree := range []struct {
		root       string
		extensions map[string]bool
	}{
		{root: "docs", extensions: map[string]bool{".md": true}},
		{root: "schemas", extensions: map[string]bool{".md": true}},
		{root: "examples/minimal", extensions: map[string]bool{".md": true, ".yaml": true}},
	} {
		treeNames := releaseTreeNames(tracked, tree.root, tree.extensions)
		names = append(names, treeNames...)
	}
	expectedSkills := make(map[string]struct{}, len(manifest.Skills))
	for _, entry := range manifest.Skills {
		name := "skills/" + entry.Path
		names = append(names, name)
		expectedSkills[name] = struct{}{}
	}
	for logical := range tracked {
		if strings.HasPrefix(logical, "skills/") && strings.HasSuffix(logical, "/SKILL.md") {
			if _, expected := expectedSkills[logical]; !expected {
				return nil, fmt.Errorf("unmanifested canonical skill artifact %s", logical)
			}
			delete(expectedSkills, logical)
		}
	}
	if len(expectedSkills) != 0 {
		return nil, errors.New("canonical skill source set is incomplete")
	}
	sort.Strings(names)
	result := make([]asset, 0, len(names))
	for _, name := range names {
		if _, exists := tracked[name]; !exists {
			return nil, fmt.Errorf("release asset %s is not tracked by the exact source revision", name)
		}
		data, err := readReleaseFile(root, name)
		if err != nil {
			return nil, err
		}
		embeddedName := strings.TrimPrefix(name, "skills/")
		if name == "skills/manifest-v1.json" || strings.HasSuffix(name, "/SKILL.md") {
			embedded, readErr := fs.ReadFile(canonicalskills.FS(), embeddedName)
			if readErr != nil || !bytes.Equal(data, embedded) {
				return nil, errors.Join(readErr, fmt.Errorf("release skill asset %s differs from the verified embedded bundle", name))
			}
		}
		result = append(result, asset{name: name, data: data, mode: 0o644})
	}
	for _, extra := range []struct {
		host, name string
	}{
		{host: "internal/unicode17/data/LICENSE.txt", name: "provenance/unicode17/LICENSE.txt"},
		{host: "internal/unicode17/data/README.md", name: "provenance/unicode17/README.md"},
	} {
		if _, exists := tracked[extra.host]; !exists {
			return nil, fmt.Errorf("release asset %s is not tracked by the exact source revision", extra.host)
		}
		data, err := readReleaseFile(root, extra.host)
		if err != nil {
			return nil, err
		}
		result = append(result, asset{name: extra.name, data: data, mode: 0o644})
	}
	licenses, err := dependencyLicenseAssets(ctx, root, targets)
	if err != nil {
		return nil, err
	}
	result = append(result, licenses...)
	sort.Slice(result, func(i, j int) bool { return result[i].name < result[j].name })
	return result, nil
}

func releaseTreeNames(tracked map[string]struct{}, logicalRoot string, extensions map[string]bool) []string {
	var result []string
	prefix := strings.TrimSuffix(logicalRoot, "/") + "/"
	for name := range tracked {
		if strings.HasPrefix(name, prefix) && extensions[strings.ToLower(filepath.Ext(name))] {
			result = append(result, name)
		}
	}
	sort.Strings(result)
	return result
}

func readReleaseFile(root, logical string) ([]byte, error) {
	host := filepath.Join(root, filepath.FromSlash(logical))
	info, err := os.Lstat(host)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, errors.Join(err, fmt.Errorf("release asset %s is not one real regular file", logical))
	}
	data, err := os.ReadFile(host)
	if err != nil {
		return nil, err
	}
	after, err := os.Lstat(host)
	if err != nil || !os.SameFile(info, after) || after.Mode() != info.Mode() || after.Size() != info.Size() || after.ModTime() != info.ModTime() {
		return nil, errors.Join(err, fmt.Errorf("release asset %s changed while being read", logical))
	}
	return data, nil
}

func dependencyLicenseAssets(ctx context.Context, root string, targets []Target) ([]asset, error) {
	command := exec.CommandContext(ctx, "go", "env", "GOMODCACHE")
	command.Dir = root
	command.Env = isolatedGoEnvironment(os.Environ())
	output, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("locate module cache for licenses: %w", err)
	}
	cache := strings.TrimSpace(string(output))
	modules := []struct {
		path, version, source, name string
	}{
		{"github.com/santhosh-tekuri/jsonschema/v6", "v6.0.2", "LICENSE", "jsonschema-v6.txt"},
		{"github.com/yuin/goldmark", "v1.8.5", "LICENSE", "goldmark.txt"},
		{"go.yaml.in/yaml/v3", "v3.0.5", "LICENSE", "yaml-v3.txt"},
		{"go.yaml.in/yaml/v3", "v3.0.5", "NOTICE", "yaml-v3-NOTICE.txt"},
		{"golang.org/x/text", "v0.14.0", "LICENSE", "golang-x-text.txt"},
		{"golang.org/x/text", "v0.14.0", "PATENTS", "golang-x-text-PATENTS.txt"},
	}
	expectedClosure := make(map[string]struct{})
	for _, module := range modules {
		expectedClosure[module.path+"@"+module.version] = struct{}{}
	}
	actualClosure := make(map[string]struct{})
	for _, target := range targets {
		closureCommand := exec.CommandContext(ctx, "go", "list", "-deps", "-f", "{{with .Module}}{{.Path}}@{{.Version}}{{end}}", "./cmd/engram")
		closureCommand.Dir = root
		closureCommand.Env = buildEnvironment(target)
		closureOutput, err := closureCommand.CombinedOutput()
		if err != nil {
			return nil, fmt.Errorf("resolve compiled dependency closure for licenses on %s/%s: %w: %s", target.OS, target.Arch, err, bounded(string(closureOutput)))
		}
		for _, line := range strings.Split(strings.TrimSpace(string(closureOutput)), "\n") {
			line = strings.TrimSpace(line)
			if line != "" && line != "github.com/ontopix/engram@" {
				actualClosure[line] = struct{}{}
			}
		}
	}
	if !sameStringSet(actualClosure, expectedClosure) {
		return nil, fmt.Errorf("compiled module closure %v differs from release license inventory %v", sortedSet(actualClosure), sortedSet(expectedClosure))
	}
	result := make([]asset, 0, len(modules))
	for _, module := range modules {
		host := filepath.Join(cache, filepath.FromSlash(module.path+"@"+module.version), module.source)
		info, err := os.Lstat(host)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return nil, errors.Join(err, fmt.Errorf("license for %s@%s is unavailable after module acquisition", module.path, module.version))
		}
		data, err := os.ReadFile(host)
		if err != nil {
			return nil, err
		}
		result = append(result, asset{name: "licenses/third-party/" + module.name, data: data, mode: 0o644})
	}
	return result, nil
}

func sameStringSet(left, right map[string]struct{}) bool {
	if len(left) != len(right) {
		return false
	}
	for value := range left {
		if _, exists := right[value]; !exists {
			return false
		}
	}
	return true
}

func sortedSet(values map[string]struct{}) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func buildBinary(ctx context.Context, root, destination, version, revision string, target Target) error {
	arguments := []string{
		"build", "-trimpath", "-buildvcs=false",
		"-ldflags", "-s -w -X github.com/ontopix/engram/internal/version.CLIVersion=" + version + " -X github.com/ontopix/engram/internal/version.BuildRevision=" + revision,
		"-o", destination, "./cmd/engram",
	}
	command := exec.CommandContext(ctx, "go", arguments...)
	command.Dir = root
	command.Env = buildEnvironment(target)
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Run(); err != nil {
		return fmt.Errorf("build %s/%s: %w: %s", target.OS, target.Arch, err, bounded(output.String()))
	}
	return nil
}

func buildEnvironment(target Target) []string {
	result := isolatedGoEnvironment(os.Environ())
	result = append(result, "GOOS="+target.OS, "GOARCH="+target.Arch, "CGO_ENABLED=0")
	switch target.Arch {
	case "amd64":
		result = append(result, "GOAMD64=v1")
	case "arm64":
		result = append(result, "GOARM64=v8.0")
	}
	return result
}

func isolatedGoEnvironment(environment []string) []string {
	result := make([]string, 0, len(environment)+9)
	allowed := map[string]bool{
		"PATH": true, "HOME": true, "XDG_CACHE_HOME": true,
		"TMPDIR": true, "TMP": true, "TEMP": true,
		"SYSTEMROOT": true, "WINDIR": true, "USERPROFILE": true,
		"HOMEDRIVE": true, "HOMEPATH": true, "LOCALAPPDATA": true,
		"GOCACHE": true, "GOMODCACHE": true,
	}
	for _, item := range environment {
		name, _, _ := strings.Cut(item, "=")
		if !allowed[strings.ToUpper(name)] {
			continue
		}
		result = append(result, item)
	}
	return append(result,
		"GOENV=off",
		"GOEXPERIMENT=",
		"GOFLAGS=-mod=readonly",
		"GOPROXY=off",
		"GOSUMDB=off",
		"GOTOOLCHAIN=local",
		"GOTELEMETRY=off",
		"GOWORK=off",
		"LC_ALL=C",
	)
}

func writeTarGzip(name, root string, assets []asset, epoch int64) (resultErr error) {
	file, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := file.Close(); resultErr == nil {
			resultErr = closeErr
		}
	}()
	compressed, err := gzip.NewWriterLevel(file, gzip.BestCompression)
	if err != nil {
		return err
	}
	stamp := time.Unix(epoch, 0).UTC()
	compressed.Header.ModTime = stamp
	compressed.Header.OS = 255
	tarWriter := tar.NewWriter(compressed)
	for _, current := range assets {
		header := &tar.Header{
			Name: root + "/" + current.name, Mode: int64(current.mode.Perm()), Size: int64(len(current.data)),
			ModTime: stamp, AccessTime: time.Time{}, ChangeTime: time.Time{}, Typeflag: tar.TypeReg, Format: tar.FormatPAX,
		}
		if err := tarWriter.WriteHeader(header); err != nil {
			return err
		}
		if _, err := tarWriter.Write(current.data); err != nil {
			return err
		}
	}
	if err := tarWriter.Close(); err != nil {
		return err
	}
	if err := compressed.Close(); err != nil {
		return err
	}
	return file.Sync()
}

func writeZIP(name, root string, assets []asset, epoch int64) (resultErr error) {
	file, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := file.Close(); resultErr == nil {
			resultErr = closeErr
		}
	}()
	writer := zip.NewWriter(file)
	stamp := time.Unix(epoch, 0).UTC()
	if stamp.Year() < 1980 {
		stamp = time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	for _, current := range assets {
		header := &zip.FileHeader{Name: root + "/" + current.name, Method: zip.Deflate}
		header.SetMode(current.mode)
		header.SetModTime(stamp)
		entry, err := writer.CreateHeader(header)
		if err != nil {
			return err
		}
		if _, err := entry.Write(current.data); err != nil {
			return err
		}
	}
	if err := writer.Close(); err != nil {
		return err
	}
	return file.Sync()
}

func inspectArtifact(name, logical string, target Target) (Artifact, error) {
	file, err := os.Open(name)
	if err != nil {
		return Artifact{}, err
	}
	hash := sha256.New()
	size, copyErr := io.Copy(hash, bufio.NewReader(file))
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil {
		return Artifact{}, errors.Join(copyErr, closeErr)
	}
	return Artifact{Name: logical, OS: target.OS, Arch: target.Arch, SHA256: hex.EncodeToString(hash.Sum(nil)), Size: size}, nil
}

func encodeJSON(value any) ([]byte, error) {
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func stageBytes(temporary, name string, data []byte) (string, error) {
	staged := filepath.Join(temporary, name)
	file, err := os.OpenFile(staged, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return "", err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return "", err
	}
	err = errors.Join(file.Sync(), file.Close())
	if err != nil {
		return "", err
	}
	return staged, nil
}

func publishFile(staged, destination string) error {
	before, err := os.Lstat(staged)
	if err != nil || before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() {
		return errors.Join(err, errors.New("staged release artifact is not one real regular file"))
	}
	if err := os.Link(staged, destination); err != nil {
		return err
	}
	after, err := os.Lstat(destination)
	if err != nil || after.Mode()&os.ModeSymlink != 0 || !after.Mode().IsRegular() || !os.SameFile(before, after) {
		return errors.Join(err, errors.New("published release artifact identity differs"))
	}
	return syncDirectory(filepath.Dir(destination))
}

type publishedFile struct {
	staged      string
	destination string
}

func rollbackPublished(values []publishedFile) error {
	var result error
	removed := false
	for index := len(values) - 1; index >= 0; index-- {
		stagedInfo, stagedErr := os.Lstat(values[index].staged)
		destinationInfo, destinationErr := os.Lstat(values[index].destination)
		if errors.Is(destinationErr, os.ErrNotExist) {
			continue
		}
		if stagedErr != nil || destinationErr != nil || !os.SameFile(stagedInfo, destinationInfo) {
			result = errors.Join(result, stagedErr, destinationErr)
			continue
		}
		if err := os.Remove(values[index].destination); err != nil {
			result = errors.Join(result, err)
		} else {
			removed = true
		}
	}
	if removed && len(values) != 0 {
		result = errors.Join(result, syncDirectory(filepath.Dir(values[0].destination)))
	}
	return result
}

func requireAbsent(name string) error {
	if _, err := os.Lstat(name); err == nil {
		return fmt.Errorf("release artifact already exists: %s", name)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func cloneAssets(input []asset) []asset {
	result := make([]asset, len(input))
	for index, current := range input {
		result[index] = asset{name: current.name, data: append([]byte(nil), current.data...), mode: current.mode}
	}
	return result
}

func bounded(value string) string {
	if len(value) > 32<<10 {
		value = value[:32<<10]
	}
	return strings.TrimSpace(value)
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
