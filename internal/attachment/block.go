// Package attachment owns the versioned project adoption block used by the
// attach and detach workflows. It deliberately knows nothing about Git or
// managed-store validation; callers must audit a store before Attach.
package attachment

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	OpenMarker  = "<!-- engram:adoption:v1 -->"
	CloseMarker = "<!-- /engram:adoption:v1 -->"

	introduction = "Engram stores (spec v1; canonical absolute paths):"
	guidance     = "Before touching a store, read its root README and follow the engram Agent Protocol."
)

// ErrMalformedBlock identifies entrypoint bytes which look owned by engram
// but cannot be replaced without guessing.
var ErrMalformedBlock = errors.New("malformed or duplicate engram adoption block")

// ErrBusy identifies another cooperating attachment update in progress.
var ErrBusy = errors.New("entrypoint is busy")

type document struct {
	Stores []string `json:"stores"`
}

// Result is the published local attachment change.
type Result struct {
	Project    string `json:"project"`
	Store      string `json:"store"`
	Entrypoint string `json:"entrypoint"`
	Changed    bool   `json:"changed"`
}

type parsedBlock struct {
	start  int
	end    int
	stores []string
}

// ResolveProject returns an explicit real directory, or the containing Git
// worktree root when Git recognizes one, otherwise the current directory.
// Git is invoked with network- and hook-irrelevant ambient overlays removed.
func ResolveProject(ctx context.Context, explicit string) (string, error) {
	if explicit != "" {
		return realDirectory(explicit)
	}
	working, err := os.Getwd()
	if err != nil {
		return "", err
	}
	git, err := exec.LookPath("git")
	if err == nil {
		command := exec.CommandContext(ctx, git, "--no-pager", "--no-optional-locks", "-c", "core.hooksPath="+os.DevNull, "-C", working, "rev-parse", "--path-format=absolute", "--show-toplevel")
		command.Env = isolatedEnvironment(os.Environ())
		output, commandErr := command.Output()
		if commandErr == nil {
			candidate := strings.TrimSuffix(string(output), "\n")
			if utf8.ValidString(candidate) && filepath.IsAbs(candidate) {
				return realDirectory(candidate)
			}
		}
	}
	return realDirectory(working)
}

// ResolveEntrypoint resolves explicit relative to project and proves the
// resulting lexical path remains below the project root.
func ResolveEntrypoint(project, explicit string) (string, error) {
	project, err := realDirectory(project)
	if err != nil {
		return "", err
	}
	if explicit == "" {
		explicit = "AGENTS.md"
	}
	candidate := explicit
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(project, candidate)
	}
	candidate, err = filepath.Abs(candidate)
	if err != nil {
		return "", err
	}
	candidate = filepath.Clean(candidate)
	if resolved, resolveErr := filepath.EvalSymlinks(candidate); resolveErr == nil {
		candidate = resolved
	} else if errors.Is(resolveErr, os.ErrNotExist) {
		resolvedParent, parentErr := filepath.EvalSymlinks(filepath.Dir(candidate))
		if parentErr != nil {
			return "", parentErr
		}
		candidate = filepath.Join(resolvedParent, filepath.Base(candidate))
	} else {
		return "", resolveErr
	}
	relative, err := filepath.Rel(project, candidate)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("entrypoint must stay below project root")
	}
	if info, statErr := os.Lstat(candidate); statErr == nil && info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("entrypoint is a symbolic link")
	} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return "", statErr
	}
	return candidate, nil
}

// CanonicalStore resolves a store to its canonical physical directory path.
func CanonicalStore(store string) (string, error) {
	return realDirectory(store)
}

// Attach adds store to the owned block and atomically publishes the result.
func Attach(project, entrypoint, store string) (Result, error) {
	return update(project, entrypoint, store, true)
}

// Detach removes store from the owned block and atomically publishes the
// result. A missing block or entry is an unchanged success.
func Detach(project, entrypoint, store string) (Result, error) {
	return update(project, entrypoint, store, false)
}

func update(project, entrypoint, store string, attach bool) (Result, error) {
	var err error
	project, err = realDirectory(project)
	if err != nil {
		return Result{}, err
	}
	entrypoint, err = ResolveEntrypoint(project, entrypoint)
	if err != nil {
		return Result{}, err
	}
	if attach {
		store, err = CanonicalStore(store)
	} else {
		store, err = detachStorePath(store)
	}
	if err != nil {
		return Result{}, err
	}
	result := Result{Project: project, Store: store, Entrypoint: entrypoint}

	lock, err := acquireLock(entrypoint + ".engram.lock")
	if err != nil {
		return Result{}, err
	}
	defer lock.release()

	original, originalInfo, err := readOptional(entrypoint)
	if err != nil {
		return Result{}, err
	}
	block, present, err := parse(original)
	if err != nil {
		return Result{}, err
	}
	stores := []string(nil)
	if present {
		stores = append(stores, block.stores...)
	}
	if err := validatePhysicalDuplicates(stores); err != nil {
		return Result{}, err
	}

	matching := matchingStores(stores, store)
	if attach {
		if len(matching) == 0 {
			stores = append(stores, store)
		}
	} else if len(matching) != 0 {
		kept := stores[:0]
		for index, existing := range stores {
			if !matching[index] {
				kept = append(kept, existing)
			}
		}
		stores = kept
	}
	sort.Strings(stores)

	updated, err := replace(original, block, present, stores)
	if err != nil {
		return Result{}, err
	}
	if bytes.Equal(original, updated) {
		return result, nil
	}
	if err := publish(entrypoint, original, originalInfo, updated); err != nil {
		return Result{}, err
	}
	result.Changed = true
	return result, nil
}

func detachStorePath(store string) (string, error) {
	absolute, err := filepath.Abs(store)
	if err != nil {
		return "", err
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err == nil {
		info, statErr := os.Stat(canonical)
		if statErr != nil {
			return "", statErr
		}
		if !info.IsDir() {
			return "", fmt.Errorf("%s is not a directory", store)
		}
		return filepath.Clean(canonical), nil
	}
	if errors.Is(err, os.ErrNotExist) {
		resolvedParent, parentErr := filepath.EvalSymlinks(filepath.Dir(absolute))
		if parentErr == nil {
			return filepath.Join(resolvedParent, filepath.Base(absolute)), nil
		}
		if errors.Is(parentErr, os.ErrNotExist) {
			return filepath.Clean(absolute), nil
		}
		return "", parentErr
	}
	return "", err
}

func parse(source []byte) (parsedBlock, bool, error) {
	openLine := []byte(OpenMarker + "\n")
	closeLine := []byte(CloseMarker + "\n")
	opens := wholeLineOffsets(source, openLine)
	closes := wholeLineOffsets(source, closeLine)
	if len(opens) == 0 && len(closes) == 0 {
		return parsedBlock{}, false, nil
	}
	if len(opens) != 1 || len(closes) != 1 || opens[0] >= closes[0] {
		return parsedBlock{}, false, ErrMalformedBlock
	}
	end := closes[0] + len(closeLine)
	blockBytes := source[opens[0]:end]
	prefix := OpenMarker + "\n" + introduction + "\n```json\n"
	suffix := "\n```\n" + guidance + "\n" + CloseMarker + "\n"
	if !bytes.HasPrefix(blockBytes, []byte(prefix)) || !bytes.HasSuffix(blockBytes, []byte(suffix)) {
		return parsedBlock{}, false, ErrMalformedBlock
	}
	payload := blockBytes[len(prefix) : len(blockBytes)-len(suffix)]
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var decoded document
	if err := decoder.Decode(&decoded); err != nil || decoder.Decode(&struct{}{}) != io.EOF || decoded.Stores == nil {
		return parsedBlock{}, false, ErrMalformedBlock
	}
	seen := make(map[string]struct{}, len(decoded.Stores))
	for _, store := range decoded.Stores {
		if store == "" || !utf8.ValidString(store) || !filepath.IsAbs(store) || filepath.Clean(store) != store {
			return parsedBlock{}, false, ErrMalformedBlock
		}
		if _, exists := seen[store]; exists {
			return parsedBlock{}, false, ErrMalformedBlock
		}
		seen[store] = struct{}{}
	}
	return parsedBlock{start: opens[0], end: end, stores: decoded.Stores}, true, nil
}

func replace(original []byte, block parsedBlock, present bool, stores []string) ([]byte, error) {
	if len(stores) == 0 {
		if !present {
			return append([]byte(nil), original...), nil
		}
		result := append([]byte(nil), original[:block.start]...)
		result = append(result, original[block.end:]...)
		return result, nil
	}
	encoded, err := encode(stores)
	if err != nil {
		return nil, err
	}
	if present {
		result := make([]byte, 0, len(original)-(block.end-block.start)+len(encoded))
		result = append(result, original[:block.start]...)
		result = append(result, encoded...)
		result = append(result, original[block.end:]...)
		return result, nil
	}
	result := append([]byte(nil), original...)
	if len(result) != 0 && result[len(result)-1] != '\n' {
		result = append(result, '\n')
	}
	if len(result) != 0 && !bytes.HasSuffix(result, []byte("\n\n")) {
		result = append(result, '\n')
	}
	return append(result, encoded...), nil
}

func encode(stores []string) ([]byte, error) {
	var payload bytes.Buffer
	encoder := json.NewEncoder(&payload)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(document{Stores: stores}); err != nil {
		return nil, err
	}
	payloadBytes := bytes.TrimSuffix(payload.Bytes(), []byte("\n"))
	var result bytes.Buffer
	fmt.Fprintf(&result, "%s\n%s\n```json\n", OpenMarker, introduction)
	result.Write(payloadBytes)
	fmt.Fprintf(&result, "\n```\n%s\n%s\n", guidance, CloseMarker)
	return result.Bytes(), nil
}

func wholeLineOffsets(source, line []byte) []int {
	var result []int
	for start := 0; start < len(source); {
		endRelative := bytes.IndexByte(source[start:], '\n')
		if endRelative < 0 {
			break
		}
		end := start + endRelative + 1
		if bytes.Equal(source[start:end], line) {
			result = append(result, start)
		}
		start = end
	}
	return result
}

func matchingStores(stores []string, wanted string) map[int]bool {
	result := make(map[int]bool)
	wantedInfo, wantedErr := os.Stat(wanted)
	for index, existing := range stores {
		if existing == wanted {
			result[index] = true
			continue
		}
		existingInfo, err := os.Stat(existing)
		if wantedErr == nil && err == nil && os.SameFile(wantedInfo, existingInfo) {
			result[index] = true
		}
	}
	return result
}

func validatePhysicalDuplicates(stores []string) error {
	for left := range stores {
		leftInfo, leftErr := os.Stat(stores[left])
		if leftErr != nil {
			continue
		}
		for right := left + 1; right < len(stores); right++ {
			rightInfo, rightErr := os.Stat(stores[right])
			if rightErr == nil && os.SameFile(leftInfo, rightInfo) {
				return ErrMalformedBlock
			}
		}
	}
	return nil
}

func realDirectory(name string) (string, error) {
	absolute, err := filepath.Abs(name)
	if err != nil {
		return "", err
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory", name)
	}
	return filepath.Clean(canonical), nil
}

func readOptional(name string) ([]byte, os.FileInfo, error) {
	info, err := os.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, nil, fmt.Errorf("entrypoint is not a real regular file")
	}
	data, err := os.ReadFile(name)
	return data, info, err
}

func publish(name string, original []byte, originalInfo os.FileInfo, updated []byte) error {
	current, currentInfo, err := readOptional(name)
	if err != nil {
		return err
	}
	if !bytes.Equal(current, original) || originalInfo == nil != (currentInfo == nil) || originalInfo != nil && !os.SameFile(originalInfo, currentInfo) {
		return fmt.Errorf("%w: entrypoint changed concurrently", ErrBusy)
	}
	directory := filepath.Dir(name)
	temporary, err := os.CreateTemp(directory, ".engram-entrypoint-*")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	mode := os.FileMode(0o644)
	if originalInfo != nil {
		mode = originalInfo.Mode().Perm()
	}
	if err := temporary.Chmod(mode); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(updated); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryName, name); err != nil {
		return err
	}
	if directoryHandle, err := os.Open(directory); err == nil {
		syncErr := directoryHandle.Sync()
		closeErr := directoryHandle.Close()
		if syncErr != nil {
			return syncErr
		}
		return closeErr
	}
	return nil
}

type lockFile struct {
	name string
	file *os.File
}

func acquireLock(name string) (*lockFile, error) {
	file, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return nil, ErrBusy
	}
	if err != nil {
		return nil, err
	}
	return &lockFile{name: name, file: file}, nil
}

func (l *lockFile) release() {
	if l == nil {
		return
	}
	if l.file != nil {
		_ = l.file.Close()
	}
	_ = os.Remove(l.name)
}

func isolatedEnvironment(environment []string) []string {
	result := make([]string, 0, len(environment)+7)
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
