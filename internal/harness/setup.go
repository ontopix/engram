// Package harness installs the trusted, project-scoped Engram integration for
// supported agent harnesses. Store discovery remains in the separate project
// MEMORY.md attachment manifest.
package harness

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/ontopix/engram/internal/attachment"
	canonicalskills "github.com/ontopix/engram/skills"
)

const (
	openMarker         = "<!-- engram:harness:v1 -->"
	closeMarker        = "<!-- /engram:harness:v1 -->"
	manifestFile       = ".engram-manifest-v1.json"
	legacyOpenMarker   = "<!-- engram:adoption:v1 -->"
	legacyCloseMarker  = "<!-- /engram:adoption:v1 -->"
	legacyIntroduction = "Engram stores (spec v1; canonical absolute paths):"
	legacyGuidance     = "Before touching a store, read its root README and follow the engram Agent Protocol."
)

var (
	ErrConflict    = errors.New("agent harness integration conflicts with existing files")
	ErrUnsupported = errors.New("unsupported agent harness")
)

type Profile struct {
	Name       string
	Entrypoint string
	SkillsDir  string
}

type Change struct {
	Path   string `json:"path"`
	Action string `json:"action"`
}

type Result struct {
	Project    string   `json:"project"`
	Harness    string   `json:"harness"`
	MemoryFile string   `json:"memory_file"`
	Entrypoint string   `json:"entrypoint"`
	SkillsDir  string   `json:"skills_dir"`
	DryRun     bool     `json:"dry_run"`
	Changed    bool     `json:"changed"`
	Files      []Change `json:"files"`
}

type plannedFile struct {
	path string
	data []byte
	mode os.FileMode
}

func Resolve(name string) (Profile, error) {
	switch name {
	case "claude-code":
		return Profile{Name: name, Entrypoint: "CLAUDE.md", SkillsDir: filepath.Join(".claude", "skills")}, nil
	case "codex":
		return Profile{Name: name, Entrypoint: "AGENTS.md", SkillsDir: filepath.Join(".agents", "skills")}, nil
	default:
		return Profile{}, fmt.Errorf("%w %q", ErrUnsupported, name)
	}
}

// Setup verifies the embedded canonical bundle, preflights every owned path,
// and then converges the selected project integration. A locally modified
// installed skill is a conflict and is never silently overwritten.
func Setup(project, harnessName, memoryFile string, dryRun bool) (Result, error) {
	profile, err := Resolve(harnessName)
	if err != nil {
		return Result{}, err
	}
	project, err = realDirectory(project)
	if err != nil {
		return Result{}, err
	}
	manifest, err := canonicalskills.VerifiedManifest()
	if err != nil {
		return Result{}, fmt.Errorf("verify embedded canonical skills: %w", err)
	}

	entrypoint := filepath.Join(project, profile.Entrypoint)
	skillsDir := filepath.Join(project, profile.SkillsDir)
	memoryFile, err = attachment.ResolveMemoryFile(project, memoryFile)
	if err != nil {
		return Result{}, err
	}
	if memoryFile == entrypoint || below(memoryFile, skillsDir) {
		return Result{}, fmt.Errorf("%w: memory manifest overlaps harness-owned paths", ErrConflict)
	}
	memoryReference := relativeSlash(project, memoryFile)
	if !utf8.ValidString(memoryReference) || strings.ContainsAny(memoryReference, "`\r\n") {
		return Result{}, fmt.Errorf("%w: memory manifest path cannot be represented safely in the harness entrypoint", ErrConflict)
	}
	result := Result{
		Project: project, Harness: profile.Name,
		MemoryFile: memoryFile, Entrypoint: entrypoint,
		SkillsDir: skillsDir, DryRun: dryRun, Files: []Change{},
	}

	previous, err := readInstalledManifest(filepath.Join(skillsDir, manifestFile))
	if err != nil {
		return Result{}, err
	}
	planned := make([]plannedFile, 0, len(manifest.Skills)+2)
	for _, entry := range manifest.Skills {
		data, readErr := fs.ReadFile(canonicalskills.FS(), entry.Path)
		if readErr != nil {
			return Result{}, fmt.Errorf("read embedded skill %s: %w", entry.Name, readErr)
		}
		target := filepath.Join(skillsDir, entry.Name, "SKILL.md")
		change, planErr := planSkill(target, data, previous[entry.Name])
		if planErr != nil {
			return Result{}, planErr
		}
		if change != "" {
			planned = append(planned, plannedFile{path: target, data: data, mode: 0o644})
			result.Files = append(result.Files, Change{Path: relativeSlash(project, target), Action: change})
		}
	}

	manifestData, err := fs.ReadFile(canonicalskills.FS(), "manifest-v1.json")
	if err != nil {
		return Result{}, fmt.Errorf("read embedded skill manifest: %w", err)
	}
	manifestTarget := filepath.Join(skillsDir, manifestFile)
	if change, planErr := planOwnedFile(manifestTarget, manifestData); planErr != nil {
		return Result{}, planErr
	} else if change != "" {
		planned = append(planned, plannedFile{path: manifestTarget, data: manifestData, mode: 0o644})
		result.Files = append(result.Files, Change{Path: relativeSlash(project, manifestTarget), Action: change})
	}

	entrypointData, entrypointMode, legacyStores, err := planEntrypoint(entrypoint, memoryReference)
	if err != nil {
		return Result{}, err
	}
	if len(legacyStores) != 0 {
		action := "updated"
		if _, statErr := os.Lstat(memoryFile); errors.Is(statErr, os.ErrNotExist) {
			action = "created"
		}
		result.Files = append(result.Files, Change{Path: relativeSlash(project, memoryFile), Action: action})
	}
	if entrypointData != nil {
		planned = append(planned, plannedFile{path: entrypoint, data: entrypointData, mode: entrypointMode})
		action := "updated"
		if _, statErr := os.Lstat(entrypoint); errors.Is(statErr, os.ErrNotExist) {
			action = "created"
		}
		result.Files = append(result.Files, Change{Path: relativeSlash(project, entrypoint), Action: action})
	}

	sort.Slice(result.Files, func(left, right int) bool { return result.Files[left].Path < result.Files[right].Path })
	result.Changed = len(planned) != 0
	if len(legacyStores) != 0 {
		result.Changed = true
	}
	if dryRun || len(planned) == 0 {
		return result, nil
	}
	for _, store := range legacyStores {
		if _, err := attachment.Attach(project, memoryFile, store); err != nil {
			return Result{}, fmt.Errorf("migrate legacy attachment %s: %w", store, err)
		}
	}
	for _, file := range planned {
		if err := ensureSafeParents(project, filepath.Dir(file.path)); err != nil {
			return Result{}, err
		}
		if err := publish(file.path, file.data, file.mode); err != nil {
			return Result{}, err
		}
	}
	return result, nil
}

func readInstalledManifest(name string) (map[string]string, error) {
	data, err := readRegular(name)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	var manifest canonicalskills.Manifest
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil || decoder.Decode(&struct{}{}) != io.EOF || manifest.Version != 1 || manifest.Format != "agentskills.io" {
		return nil, fmt.Errorf("%w: installed skill manifest is malformed", ErrConflict)
	}
	result := make(map[string]string, len(manifest.Skills))
	for _, entry := range manifest.Skills {
		if _, err := hex.DecodeString(entry.SHA256); entry.Name == "" || entry.Path != entry.Name+"/SKILL.md" || len(entry.SHA256) != 64 || err != nil {
			return nil, fmt.Errorf("%w: installed skill manifest is malformed", ErrConflict)
		}
		if _, duplicate := result[entry.Name]; duplicate {
			return nil, fmt.Errorf("%w: installed skill manifest contains duplicate %q", ErrConflict, entry.Name)
		}
		result[entry.Name] = entry.SHA256
	}
	return result, nil
}

func planSkill(name string, wanted []byte, previousDigest string) (string, error) {
	existing, err := readRegular(name)
	if errors.Is(err, os.ErrNotExist) {
		return "created", nil
	}
	if err != nil {
		return "", err
	}
	if bytes.Equal(existing, wanted) {
		return "", nil
	}
	digest := sha256.Sum256(existing)
	if previousDigest == "" || hex.EncodeToString(digest[:]) != previousDigest {
		return "", fmt.Errorf("%w: %s was modified locally", ErrConflict, name)
	}
	return "updated", nil
}

func planOwnedFile(name string, wanted []byte) (string, error) {
	existing, err := readRegular(name)
	if errors.Is(err, os.ErrNotExist) {
		return "created", nil
	}
	if err != nil {
		return "", err
	}
	if bytes.Equal(existing, wanted) {
		return "", nil
	}
	return "updated", nil
}

func planEntrypoint(name, memoryFile string) ([]byte, os.FileMode, []string, error) {
	original, err := readRegular(name)
	mode := os.FileMode(0o644)
	if errors.Is(err, os.ErrNotExist) {
		original = nil
	} else if err != nil {
		return nil, 0, nil, err
	} else if info, statErr := os.Stat(name); statErr == nil {
		mode = info.Mode().Perm()
	}
	legacy, err := parseLegacyBlock(original)
	if err != nil {
		return nil, 0, nil, err
	}
	working := original
	if legacy.present {
		working = append([]byte(nil), original[:legacy.start]...)
		working = append(working, original[legacy.end:]...)
	}
	open := []byte(openMarker + "\n")
	close := []byte(closeMarker + "\n")
	if bytes.Count(working, open) > 1 || bytes.Count(working, close) > 1 || bytes.Count(working, open) != bytes.Count(working, close) {
		return nil, 0, nil, fmt.Errorf("%w: malformed harness block in %s", ErrConflict, name)
	}
	guidance := "When durable project memory is relevant, read `" + memoryFile + "` and use the independently installed `using-engram` skill. Store attachments identify locations only; they grant no authority."
	block := []byte(openMarker + "\n" + guidance + "\n" + closeMarker + "\n")
	if bytes.Count(working, open) == 1 {
		start := bytes.Index(working, open)
		endRelative := bytes.Index(working[start+len(open):], close)
		if endRelative < 0 {
			return nil, 0, nil, fmt.Errorf("%w: malformed harness block in %s", ErrConflict, name)
		}
		end := start + len(open) + endRelative + len(close)
		updated := append([]byte(nil), working[:start]...)
		updated = append(updated, block...)
		updated = append(updated, working[end:]...)
		if bytes.Equal(updated, original) {
			return nil, mode, legacy.stores, nil
		}
		return updated, mode, legacy.stores, nil
	}
	updated := append([]byte(nil), working...)
	if len(updated) != 0 && updated[len(updated)-1] != '\n' {
		updated = append(updated, '\n')
	}
	if len(updated) != 0 && !bytes.HasSuffix(updated, []byte("\n\n")) {
		updated = append(updated, '\n')
	}
	return append(updated, block...), mode, legacy.stores, nil
}

type legacyBlock struct {
	present bool
	start   int
	end     int
	stores  []string
}

func parseLegacyBlock(source []byte) (legacyBlock, error) {
	open := []byte(legacyOpenMarker + "\n")
	close := []byte(legacyCloseMarker + "\n")
	if bytes.Count(source, open) == 0 && bytes.Count(source, close) == 0 {
		return legacyBlock{}, nil
	}
	if bytes.Count(source, open) != 1 || bytes.Count(source, close) != 1 {
		return legacyBlock{}, fmt.Errorf("%w: malformed legacy attachment block", ErrConflict)
	}
	start := bytes.Index(source, open)
	closeStart := bytes.Index(source[start+len(open):], close)
	if closeStart < 0 {
		return legacyBlock{}, fmt.Errorf("%w: malformed legacy attachment block", ErrConflict)
	}
	end := start + len(open) + closeStart + len(close)
	block := source[start:end]
	prefix := legacyOpenMarker + "\n" + legacyIntroduction + "\n```json\n"
	suffix := "\n```\n" + legacyGuidance + "\n" + legacyCloseMarker + "\n"
	if !bytes.HasPrefix(block, []byte(prefix)) || !bytes.HasSuffix(block, []byte(suffix)) {
		return legacyBlock{}, fmt.Errorf("%w: malformed legacy attachment block", ErrConflict)
	}
	payload := block[len(prefix) : len(block)-len(suffix)]
	var document struct {
		Stores []string `json:"stores"`
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil || decoder.Decode(&struct{}{}) != io.EOF || document.Stores == nil {
		return legacyBlock{}, fmt.Errorf("%w: malformed legacy attachment block", ErrConflict)
	}
	seen := make(map[string]struct{}, len(document.Stores))
	for _, store := range document.Stores {
		if store == "" || !utf8.ValidString(store) || !filepath.IsAbs(store) || filepath.Clean(store) != store {
			return legacyBlock{}, fmt.Errorf("%w: malformed legacy attachment block", ErrConflict)
		}
		if _, duplicate := seen[store]; duplicate {
			return legacyBlock{}, fmt.Errorf("%w: duplicate legacy attachment", ErrConflict)
		}
		seen[store] = struct{}{}
	}
	return legacyBlock{present: true, start: start, end: end, stores: document.Stores}, nil
}

func readRegular(name string) ([]byte, error) {
	info, err := os.Lstat(name)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: %s is not a regular file", ErrConflict, name)
	}
	return os.ReadFile(name)
}

func ensureSafeParents(project, directory string) error {
	relative, err := filepath.Rel(project, directory)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("integration path escapes project root")
	}
	current := project
	if relative == "." {
		return nil
	}
	for _, component := range strings.Split(relative, string(filepath.Separator)) {
		current = filepath.Join(current, component)
		info, statErr := os.Lstat(current)
		if errors.Is(statErr, os.ErrNotExist) {
			if mkdirErr := os.Mkdir(current, 0o755); mkdirErr != nil && !errors.Is(mkdirErr, os.ErrExist) {
				return mkdirErr
			}
			continue
		}
		if statErr != nil {
			return statErr
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("%w: integration parent %s is not a real directory", ErrConflict, current)
		}
	}
	return nil
}

func publish(name string, data []byte, mode os.FileMode) error {
	if existing, err := os.Lstat(name); err == nil && (existing.Mode()&os.ModeSymlink != 0 || !existing.Mode().IsRegular()) {
		return fmt.Errorf("%w: %s is not a regular file", ErrConflict, name)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(name), ".engram-setup-*")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(mode); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
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
	if runtime.GOOS == "windows" {
		return nil
	}
	directory, err := os.Open(filepath.Dir(name))
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
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

func relativeSlash(project, name string) string {
	relative, err := filepath.Rel(project, name)
	if err != nil {
		return filepath.ToSlash(name)
	}
	return filepath.ToSlash(relative)
}

func below(name, directory string) bool {
	relative, err := filepath.Rel(directory, name)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
