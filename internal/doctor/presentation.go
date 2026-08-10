package doctor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

type gitResult struct {
	stdout []byte
	stderr []byte
	status int
}

func inspectPresentation(ctx context.Context, current *inspection) {
	if current.repository == nil {
		for _, name := range []string{"presentation.sparse", "presentation.transforms", "presentation.roundtrip"} {
			setRequired(&current.result, name, Error, nil, "managed repository is unavailable")
		}
		return
	}
	paths, pathIssues := presentationPaths(ctx, current)

	sparseIssues := inspectSparse(ctx, current.target)
	if len(sparseIssues) != 0 {
		setRequired(&current.result, "presentation.sparse", Error, nil, strings.Join(sortedUnique(sparseIssues), "; "))
	}
	transformIssues := inspectTransforms(ctx, current.target, paths)
	if len(transformIssues) != 0 {
		setRequired(&current.result, "presentation.transforms", Error, nil, strings.Join(sortedUnique(transformIssues), "; "))
	}
	roundtripIssues := append(pathIssues, inspectRoundtrip(current, paths)...)
	if len(roundtripIssues) != 0 {
		setRequired(&current.result, "presentation.roundtrip", Error, nil, strings.Join(sortedUnique(roundtripIssues), "; "))
	}
}

func presentationPaths(ctx context.Context, current *inspection) ([]string, []string) {
	set := make(map[string]struct{})
	if current.accepted != nil && current.accepted.Tree != nil {
		for name := range current.accepted.Tree.Files {
			set[name] = struct{}{}
		}
	}
	if current.working != nil && current.working.Tree != nil {
		for name := range current.working.Tree.Files {
			set[name] = struct{}{}
		}
	}
	result, err := runGit(ctx, current.target, nil, "ls-files", "--stage", "--sparse", "--full-name", "-z")
	issues := make([]string, 0)
	if err != nil || result.status != 0 {
		issues = append(issues, "Git cannot enumerate the real index")
	} else {
		for _, record := range nulRecords(result.stdout) {
			tab := bytes.IndexByte(record, '\t')
			if tab < 0 || tab+1 == len(record) || !utf8.Valid(record[tab+1:]) {
				issues = append(issues, "index has no reversible pathname projection")
				continue
			}
			name := string(record[tab+1:])
			if !validLogicalPath(name) {
				issues = append(issues, "index contains an unsafe logical pathname")
				continue
			}
			set[name] = struct{}{}
		}
	}
	paths := make([]string, 0, len(set))
	for name := range set {
		paths = append(paths, name)
	}
	sort.Slice(paths, func(left, right int) bool { return bytes.Compare([]byte(paths[left]), []byte(paths[right])) < 0 })
	return paths, issues
}

func inspectSparse(ctx context.Context, root string) []string {
	issues := make([]string, 0)
	for _, key := range []string{"core.sparseCheckout", "index.sparse"} {
		value, present, valid := gitBool(ctx, root, key)
		if !valid {
			issues = append(issues, key+" cannot be read as a boolean")
		} else if present && value {
			issues = append(issues, key+" enables sparse presentation")
		}
	}
	staged, err := runGit(ctx, root, nil, "ls-files", "--stage", "--sparse", "--full-name", "-z")
	if err != nil || staged.status != 0 {
		issues = append(issues, "Git cannot inspect sparse index entries")
	} else {
		for _, record := range nulRecords(staged.stdout) {
			tab := bytes.IndexByte(record, '\t')
			if tab < 0 {
				issues = append(issues, "Git returned a malformed index entry")
				break
			}
			header := bytes.Fields(record[:tab])
			if len(header) != 3 {
				issues = append(issues, "Git returned a malformed index entry")
				break
			}
			if bytes.Equal(header[0], []byte("040000")) {
				issues = append(issues, "real index contains a sparse directory entry")
				break
			}
		}
	}
	tags, err := runGit(ctx, root, nil, "ls-files", "-t", "--full-name", "-z")
	if err != nil || tags.status != 0 {
		issues = append(issues, "Git cannot inspect skip-worktree entries")
	} else {
		for _, record := range nulRecords(tags.stdout) {
			if len(record) < 3 || record[1] != ' ' {
				issues = append(issues, "Git returned malformed index tags")
				break
			}
			if record[0] == 'S' {
				issues = append(issues, "real index contains skip-worktree entries")
				break
			}
		}
	}
	return issues
}

func inspectTransforms(ctx context.Context, root string, paths []string) []string {
	issues := make([]string, 0)
	value, present, valid := gitBool(ctx, root, "core.autocrlf")
	if !valid {
		issues = append(issues, "core.autocrlf cannot be read as a boolean")
	} else if present && value {
		issues = append(issues, "effective core.autocrlf is not false")
	}
	if len(paths) == 0 {
		return issues
	}
	var input bytes.Buffer
	for _, name := range paths {
		input.WriteString(name)
		input.WriteByte(0)
	}
	attributes := []string{"text", "eol", "filter", "ident", "working-tree-encoding"}
	arguments := append([]string{"check-attr", "-z", "--stdin"}, attributes...)
	result, err := runGit(ctx, root, input.Bytes(), arguments...)
	if err != nil || result.status != 0 {
		return append(issues, "Git cannot inspect effective attributes")
	}
	fields := bytes.Split(result.stdout, []byte{0})
	if len(fields) != 0 && len(fields[len(fields)-1]) == 0 {
		fields = fields[:len(fields)-1]
	}
	if len(fields) != len(paths)*len(attributes)*3 {
		return append(issues, "Git returned incomplete attribute observations")
	}
	seen := make(map[string]struct{}, len(fields)/3)
	for offset := 0; offset < len(fields); offset += 3 {
		if !utf8.Valid(fields[offset]) || !utf8.Valid(fields[offset+1]) || !utf8.Valid(fields[offset+2]) {
			issues = append(issues, "Git returned non-UTF-8 attribute observations")
			continue
		}
		name, attribute, attributeValue := string(fields[offset]), string(fields[offset+1]), string(fields[offset+2])
		key := name + "\x00" + attribute
		if _, duplicate := seen[key]; duplicate {
			issues = append(issues, "Git returned duplicate attribute observations")
			continue
		}
		seen[key] = struct{}{}
		allowed := attributeValue == "unspecified" || attribute == "text" && attributeValue == "unset"
		if !allowed {
			issues = append(issues, fmt.Sprintf("attribute %s=%s transforms %s", attribute, attributeValue, name))
		}
	}
	return issues
}

func inspectRoundtrip(current *inspection, paths []string) []string {
	issues := make([]string, 0)
	rootInfo, err := os.Lstat(current.repository.Root)
	if err != nil || rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() || !filepath.IsAbs(current.repository.Root) || filepath.Clean(current.repository.Root) != current.repository.Root {
		return []string{"managed worktree root is not one exact real directory"}
	}
	for _, logical := range paths {
		name := current.repository.Root
		present := true
		for _, component := range strings.Split(logical, "/") {
			entries, err := os.ReadDir(name)
			if errors.Is(err, os.ErrNotExist) {
				present = false
				break
			}
			if err != nil {
				issues = append(issues, "cannot enumerate exact host pathname for "+logical)
				present = false
				break
			}
			found := false
			for _, entry := range entries {
				if entry.Name() == component {
					found = true
					break
				}
			}
			if !found {
				if _, err := os.Lstat(filepath.Join(name, component)); err == nil {
					issues = append(issues, "host filesystem changes pathname bytes for "+logical)
				}
				present = false
				break
			}
			name = filepath.Join(name, component)
		}
		if !present {
			continue
		}
		if current.working == nil || current.working.Tree == nil {
			issues = append(issues, "working bytes cannot be projected for round-trip proof")
			break
		}
		file, logicalPresent := current.working.Tree.Files[logical]
		if !logicalPresent {
			// The path may be an index-only deletion/addition observation.
			continue
		}
		info, err := os.Lstat(name)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			issues = append(issues, "observed logical file is not one real regular host file: "+logical)
			continue
		}
		data, err := os.ReadFile(name)
		if err != nil || !bytes.Equal(data, file.Data) {
			issues = append(issues, "host file bytes do not round-trip for "+logical)
		}
	}
	return issues
}

func inspectCacheExclusion(current *inspection) {
	if current.repository == nil {
		setRequired(&current.result, "cache.exclusion", Error, nil, "managed repository is unavailable")
		return
	}
	name := filepath.Join(current.repository.CommonGitDir, "info", "exclude")
	data, present, err := readStableControllerFile(current.repository.CommonGitDir, name)
	if err != nil || !present {
		message := "repository-local exclude file is unavailable"
		if err != nil {
			message += ": " + err.Error()
		}
		setRequired(&current.result, "cache.exclusion", Error, pathPointer(name), message)
		return
	}
	for _, line := range bytes.Split(data, []byte("\n")) {
		if bytes.Equal(line, []byte(".engram/cache/")) {
			return
		}
	}
	setRequired(&current.result, "cache.exclusion", Error, pathPointer(name), "exact .engram/cache/ exclusion is absent")
}

func gitBool(ctx context.Context, root, key string) (value, present, valid bool) {
	result, err := runGit(ctx, root, nil, "config", "--type=bool", "--includes", "--get", key)
	if err != nil {
		return false, false, false
	}
	switch result.status {
	case 0:
		value := strings.TrimSuffix(string(result.stdout), "\n")
		return value == "true", true, value == "true" || value == "false"
	case 1:
		return false, false, true
	default:
		return false, false, false
	}
}

func nulRecords(data []byte) [][]byte {
	if len(data) == 0 {
		return nil
	}
	records := bytes.Split(data, []byte{0})
	if len(records[len(records)-1]) == 0 {
		records = records[:len(records)-1]
	}
	return records
}

func runGit(ctx context.Context, root string, input []byte, arguments ...string) (gitResult, error) {
	git, err := exec.LookPath("git")
	if err != nil {
		return gitResult{}, err
	}
	global := []string{
		"-c", "core.longpaths=true",
		"--no-pager", "--no-optional-locks", "--no-replace-objects",
		"-c", "core.hooksPath=" + os.DevNull, "-c", "core.fsmonitor=false", "-c", "core.untrackedCache=false",
		"-c", "maintenance.auto=false", "-c", "gc.auto=0", "-C", root,
	}
	command := exec.CommandContext(ctx, git, append(global, arguments...)...)
	command.Env = isolatedEnvironment(os.Environ())
	command.Stdin = bytes.NewReader(input)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err = command.Run()
	if err == nil {
		return gitResult{stdout: stdout.Bytes(), stderr: stderr.Bytes()}, nil
	}
	if ctx.Err() != nil {
		return gitResult{}, ctx.Err()
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return gitResult{stdout: stdout.Bytes(), stderr: stderr.Bytes(), status: exit.ExitCode()}, nil
	}
	return gitResult{}, err
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
	sort.Strings(result)
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
