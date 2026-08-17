// Package projectsetup converges a project-scoped Engram installation from an
// optional declarative engram.yaml manifest. The manifest declares desired
// repository locations; MEMORY.md remains the runtime-facing local registry.
package projectsetup

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/ontopix/engram/internal/acquire"
	"github.com/ontopix/engram/internal/attachment"
	"github.com/ontopix/engram/internal/checker"
	"github.com/ontopix/engram/internal/fileidentity"
	"github.com/ontopix/engram/internal/harness"
	"github.com/ontopix/engram/internal/managedread"
	"github.com/ontopix/engram/internal/transport"
	"go.yaml.in/yaml/v3"
)

const (
	ConfigFileName = "engram.yaml"
	MemoryDirName  = ".memory"

	gitignoreOpen  = "# engram:managed-stores:v1"
	gitignoreClose = "# /engram:managed-stores:v1"
	maxConfigBytes = 1 << 20
)

var (
	ErrConfig        = errors.New("invalid project setup manifest")
	ErrConflict      = errors.New("project setup conflicts with existing files")
	ErrUsage         = errors.New("project setup requires additional input")
	ErrValidation    = errors.New("configured memory has validation issues")
	ErrIndeterminate = errors.New("configured memory validation is indeterminate")
)

type Config struct {
	Version     int                `yaml:"version" json:"version"`
	Harness     string             `yaml:"harness,omitempty" json:"harness,omitempty"`
	Attachments []ConfigAttachment `yaml:"attachments" json:"attachments"`
}

type ConfigAttachment struct {
	Name string `yaml:"name" json:"name"`
	URL  string `yaml:"url" json:"url"`
}

type Options struct {
	Project         string
	Harness         string
	MemoryFile      string
	DryRun          bool
	ValidationScope acquire.ValidationScope
	ConfigLoader    func(string) (*Config, string, error)
	AcquireClone    func(context.Context, string, acquire.Options) (acquire.Result, error)
	AcquireReuse    func(context.Context, string, string, acquire.Options) (acquire.Result, error)
	HarnessSetup    func(string, string, string, bool) (harness.Result, error)
	PlanManaged     func(string, string, string, []string) (attachment.ManagedResult, error)
	ApplyManaged    func(string, string, string, []string) (attachment.ManagedResult, error)
}

type FileChange struct {
	Path   string `json:"path"`
	Action string `json:"action"`
}

type AttachmentResult struct {
	Name            string                     `json:"name"`
	URL             string                     `json:"url"`
	Store           string                     `json:"store"`
	Action          string                     `json:"action"`
	ValidationScope acquire.ValidationScope    `json:"validation_scope"`
	Validation      *checker.Result            `json:"validation"`
	Audits          []managedread.HistoryAudit `json:"audits"`
}

type Result struct {
	Project     string             `json:"project"`
	ConfigFile  *string            `json:"config_file"`
	Harness     string             `json:"harness"`
	MemoryDir   *string            `json:"memory_dir"`
	MemoryFile  string             `json:"memory_file"`
	Entrypoint  string             `json:"entrypoint"`
	SkillsDir   string             `json:"skills_dir"`
	DryRun      bool               `json:"dry_run"`
	Changed     bool               `json:"changed"`
	Attachments []AttachmentResult `json:"attachments"`
	Files       []FileChange       `json:"files"`
}

type filePlan struct {
	path         string
	original     []byte
	originalInfo os.FileInfo
	updated      []byte
	mode         os.FileMode
	action       string
}

// Run resolves the optional project manifest, preflights every project-owned
// file, acquires missing configured stores, reconciles MEMORY.md, and installs
// the selected harness. Existing stores are verified locally and never pulled.
func Run(ctx context.Context, options Options) (Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	validationScope, err := setupValidationScope(options.ValidationScope)
	if err != nil {
		return Result{}, err
	}
	options.ValidationScope = validationScope
	project, err := realDirectory(options.Project)
	if err != nil {
		return Result{}, err
	}
	options = withDefaults(options)
	config, configFile, err := options.ConfigLoader(project)
	if err != nil {
		return Result{}, err
	}
	memoryFile, err := attachment.ResolveMemoryFile(project, options.MemoryFile)
	if err != nil {
		return Result{}, err
	}

	harnessName := options.Harness
	if harnessName == "" && config != nil {
		harnessName = config.Harness
	}
	if harnessName == "" {
		return Result{}, fmt.Errorf("%w: harness must be set in %s or with --harness", ErrUsage, ConfigFileName)
	}
	if _, err := harness.Resolve(harnessName); err != nil {
		return Result{}, err
	}

	harnessPlan, err := options.HarnessSetup(project, harnessName, memoryFile, true)
	if err != nil {
		return Result{}, err
	}
	result := Result{
		Project: project, Harness: harnessName, MemoryFile: memoryFile,
		Entrypoint: harnessPlan.Entrypoint, SkillsDir: harnessPlan.SkillsDir,
		DryRun: options.DryRun, Attachments: []AttachmentResult{}, Files: []FileChange{},
	}
	if config == nil {
		installed := harnessPlan
		if !options.DryRun {
			installed, err = options.HarnessSetup(project, harnessName, memoryFile, false)
			if err != nil {
				return result, err
			}
		}
		result.Changed = installed.Changed
		result.Files = harnessChanges(installed.Files)
		return result, nil
	}
	result.ConfigFile = stringPointer(configFile)
	memoryDir := filepath.Join(project, MemoryDirName)
	result.MemoryDir = stringPointer(memoryDir)
	if err := inspectMemoryDir(memoryDir); err != nil {
		return result, err
	}

	ignorePlan, err := planGitignore(project)
	if err != nil {
		return result, err
	}
	destinations := make([]string, len(config.Attachments))
	for index, configured := range config.Attachments {
		destinations[index] = filepath.Join(memoryDir, configured.Name)
	}
	memoryPlan, err := options.PlanManaged(project, memoryFile, memoryDir, destinations)
	if err != nil {
		return result, err
	}
	memoryChange := fileChange(project, memoryFile)

	if options.DryRun {
		for index, configured := range config.Attachments {
			planned, planErr := planAcquisition(ctx, configured, destinations[index], validationScope, options.AcquireReuse)
			if planErr != nil {
				return result, planErr
			}
			result.Attachments = append(result.Attachments, planned)
			if planned.Action == "clone" {
				result.Changed = true
			}
		}
		if ignorePlan != nil {
			result.Files = append(result.Files, FileChange{Path: ".gitignore", Action: ignorePlan.action})
		}
		if memoryPlan.Changed {
			result.Files = append(result.Files, memoryChange)
		}
		result.Files = mergeChanges(result.Files, harnessChanges(harnessPlan.Files))
		result.Changed = result.Changed || len(result.Files) != 0
		return result, nil
	}

	if ignorePlan != nil {
		if err := publishFile(ignorePlan); err != nil {
			return result, err
		}
		result.Files = append(result.Files, FileChange{Path: ".gitignore", Action: ignorePlan.action})
		result.Changed = true
	}
	if len(config.Attachments) != 0 {
		created, ensureErr := ensureMemoryDir(memoryDir)
		if ensureErr != nil {
			return result, ensureErr
		}
		result.Changed = result.Changed || created
	}
	for index, configured := range config.Attachments {
		acquired, acquireErr := acquireOne(ctx, configured, destinations[index], validationScope, options.AcquireClone, options.AcquireReuse)
		if acquireErr != nil {
			return result, acquireErr
		}
		result.Attachments = append(result.Attachments, acquired)
		if acquired.Action == "cloned" {
			result.Changed = true
		}
		if acquired.Validation != nil {
			switch {
			case acquired.Validation.Status == checker.StatusIndeterminate:
				return result, ErrIndeterminate
			case acquired.Validation.HasErrors():
				return result, ErrValidation
			}
		}
	}

	memoryResult, err := options.ApplyManaged(project, memoryFile, memoryDir, destinations)
	if err != nil {
		return result, err
	}
	if memoryResult.Changed {
		result.Files = append(result.Files, memoryChange)
		result.Changed = true
	}
	installed, err := options.HarnessSetup(project, harnessName, memoryFile, false)
	if err != nil {
		return result, err
	}
	result.Files = mergeChanges(result.Files, harnessChanges(installed.Files))
	result.Changed = result.Changed || installed.Changed
	return result, nil
}

func withDefaults(options Options) Options {
	if options.ConfigLoader == nil {
		options.ConfigLoader = LoadConfig
	}
	if options.AcquireClone == nil {
		options.AcquireClone = acquire.Clone
	}
	if options.AcquireReuse == nil {
		options.AcquireReuse = acquire.ReuseWithOptions
	}
	if options.HarnessSetup == nil {
		options.HarnessSetup = harness.Setup
	}
	if options.PlanManaged == nil {
		options.PlanManaged = attachment.PlanManaged
	}
	if options.ApplyManaged == nil {
		options.ApplyManaged = attachment.ReconcileManaged
	}
	return options
}

func setupValidationScope(scope acquire.ValidationScope) (acquire.ValidationScope, error) {
	switch scope {
	case "", acquire.ValidationScopeCurrent:
		return acquire.ValidationScopeCurrent, nil
	case acquire.ValidationScopeHistory:
		return acquire.ValidationScopeHistory, nil
	default:
		return "", fmt.Errorf("%w: unsupported validation scope %q", ErrUsage, scope)
	}
}

// LoadConfig reads the project-root engram.yaml without following a symlink.
// Absence is not an error and preserves the imperative setup workflow.
func LoadConfig(project string) (*Config, string, error) {
	document, err := readConfigDocument(project)
	if err != nil {
		return nil, document.path, err
	}
	if !document.exists {
		return nil, document.path, nil
	}
	config := document.config
	return &config, document.path, nil
}

func decodeConfig(data []byte) (Config, error) {
	if len(data) == 0 || !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return Config{}, errors.New("manifest is empty or not safe UTF-8")
	}
	var node yaml.Node
	nodeDecoder := yaml.NewDecoder(bytes.NewReader(data))
	if err := nodeDecoder.Decode(&node); err != nil {
		return Config{}, fmt.Errorf("decode YAML: %w", err)
	}
	var extra yaml.Node
	if err := nodeDecoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return Config{}, errors.New("manifest contains more than one YAML document")
		}
		return Config{}, fmt.Errorf("decode trailing YAML: %w", err)
	}
	if err := validateYAMLNode(&node); err != nil {
		return Config{}, err
	}
	if err := validateConfigShape(&node); err != nil {
		return Config{}, err
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	var config Config
	if err := decoder.Decode(&config); err != nil {
		return Config{}, fmt.Errorf("decode fields: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return Config{}, errors.New("manifest contains more than one YAML document")
	}
	if config.Version != 1 {
		return Config{}, errors.New("version must be integer 1")
	}
	if config.Harness != "" {
		if _, err := harness.Resolve(config.Harness); err != nil {
			return Config{}, err
		}
	}
	if config.Attachments == nil {
		config.Attachments = []ConfigAttachment{}
	}
	names := make(map[string]struct{}, len(config.Attachments))
	locations := make(map[string]struct{}, len(config.Attachments))
	for index, attachment := range config.Attachments {
		if !validName(attachment.Name) {
			return Config{}, fmt.Errorf("attachments[%d].name must be a portable lowercase slug", index)
		}
		if _, duplicate := names[attachment.Name]; duplicate {
			return Config{}, fmt.Errorf("duplicate attachment name %q", attachment.Name)
		}
		names[attachment.Name] = struct{}{}
		if err := transport.ValidateLocation(attachment.URL); err != nil {
			return Config{}, fmt.Errorf("attachments[%d].url: %w", index, err)
		}
		if hasEmbeddedPassword(attachment.URL) {
			return Config{}, fmt.Errorf("attachments[%d].url must not contain an embedded password", index)
		}
		if _, duplicate := locations[attachment.URL]; duplicate {
			return Config{}, fmt.Errorf("duplicate attachment URL %q", attachment.URL)
		}
		locations[attachment.URL] = struct{}{}
	}
	return config, nil
}

func validateYAMLNode(node *yaml.Node) error {
	if node == nil {
		return errors.New("manifest has no YAML document")
	}
	if node.Kind == yaml.AliasNode {
		return errors.New("YAML aliases are not supported")
	}
	if node.Kind == yaml.MappingNode {
		seen := make(map[string]struct{}, len(node.Content)/2)
		for index := 0; index < len(node.Content); index += 2 {
			if index+1 >= len(node.Content) || node.Content[index].Kind != yaml.ScalarNode {
				return errors.New("mapping keys must be scalars")
			}
			key := node.Content[index].Value
			if key == "<<" {
				return errors.New("YAML merge keys are not supported")
			}
			if _, duplicate := seen[key]; duplicate {
				return fmt.Errorf("duplicate YAML key %q", key)
			}
			seen[key] = struct{}{}
		}
	}
	for _, child := range node.Content {
		if err := validateYAMLNode(child); err != nil {
			return err
		}
	}
	return nil
}

func validateConfigShape(document *yaml.Node) error {
	if document == nil || document.Kind != yaml.DocumentNode || len(document.Content) != 1 {
		return errors.New("manifest must contain one mapping document")
	}
	root := document.Content[0]
	if root.Kind != yaml.MappingNode || root.Tag != "!!map" {
		return errors.New("manifest root must be a mapping")
	}
	versionPresent := false
	for index := 0; index < len(root.Content); index += 2 {
		key, value := root.Content[index], root.Content[index+1]
		if key.Kind != yaml.ScalarNode || key.Tag != "!!str" {
			return errors.New("manifest field names must be strings")
		}
		switch key.Value {
		case "version":
			versionPresent = true
			if value.Kind != yaml.ScalarNode || value.Tag != "!!int" {
				return errors.New("version must be an integer")
			}
		case "harness":
			if value.Kind != yaml.ScalarNode || value.Tag != "!!str" || value.Value == "" {
				return errors.New("harness must be a non-empty string when present")
			}
		case "attachments":
			if value.Kind != yaml.SequenceNode || value.Tag != "!!seq" {
				return errors.New("attachments must be a sequence when present")
			}
			for attachmentIndex, item := range value.Content {
				if err := validateConfigAttachmentShape(item); err != nil {
					return fmt.Errorf("attachments[%d]: %w", attachmentIndex, err)
				}
			}
		default:
			return fmt.Errorf("unknown manifest field %q", key.Value)
		}
	}
	if !versionPresent {
		return errors.New("version is required")
	}
	return nil
}

func validateConfigAttachmentShape(node *yaml.Node) error {
	if node == nil || node.Kind != yaml.MappingNode || node.Tag != "!!map" {
		return errors.New("entry must be a mapping")
	}
	present := map[string]bool{"name": false, "url": false}
	for index := 0; index < len(node.Content); index += 2 {
		key, value := node.Content[index], node.Content[index+1]
		if key.Kind != yaml.ScalarNode || key.Tag != "!!str" {
			return errors.New("field names must be strings")
		}
		if _, known := present[key.Value]; !known {
			return fmt.Errorf("unknown field %q", key.Value)
		}
		if value.Kind != yaml.ScalarNode || value.Tag != "!!str" || value.Value == "" {
			return fmt.Errorf("%s must be a non-empty string", key.Value)
		}
		present[key.Value] = true
	}
	for _, field := range []string{"name", "url"} {
		if !present[field] {
			return fmt.Errorf("%s is required", field)
		}
	}
	return nil
}

func validName(value string) bool {
	if len(value) == 0 || len(value) > 63 || (value[0] < 'a' || value[0] > 'z') && (value[0] < '0' || value[0] > '9') {
		return false
	}
	if value[len(value)-1] == '-' {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '-' {
			continue
		}
		return false
	}
	return !reservedWindowsName(value)
}

func reservedWindowsName(value string) bool {
	switch value {
	case "con", "prn", "aux", "nul",
		"com1", "com2", "com3", "com4", "com5", "com6", "com7", "com8", "com9",
		"lpt1", "lpt2", "lpt3", "lpt4", "lpt5", "lpt6", "lpt7", "lpt8", "lpt9":
		return true
	default:
		return false
	}
}

func hasEmbeddedPassword(value string) bool {
	if !strings.Contains(value, "://") {
		return false
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.User == nil {
		return false
	}
	_, present := parsed.User.Password()
	return present
}

func planAcquisition(ctx context.Context, configured ConfigAttachment, destination string, validationScope acquire.ValidationScope, reuse func(context.Context, string, string, acquire.Options) (acquire.Result, error)) (AttachmentResult, error) {
	result := AttachmentResult{Name: configured.Name, URL: configured.URL, Store: destination, Action: "clone", ValidationScope: validationScope, Audits: []managedread.HistoryAudit{}}
	if _, err := os.Lstat(destination); errors.Is(err, os.ErrNotExist) {
		return result, nil
	} else if err != nil {
		return result, err
	}
	result.Action = "reuse"
	reused, err := reuse(ctx, configured.URL, destination, acquire.Options{ValidationScope: validationScope})
	if err != nil {
		return result, err
	}
	result.Validation = checkerPointer(reused.Validation)
	result.Audits = append(result.Audits, reused.Audits...)
	return result, nil
}

func acquireOne(ctx context.Context, configured ConfigAttachment, destination string, validationScope acquire.ValidationScope, clone func(context.Context, string, acquire.Options) (acquire.Result, error), reuse func(context.Context, string, string, acquire.Options) (acquire.Result, error)) (AttachmentResult, error) {
	result := AttachmentResult{Name: configured.Name, URL: configured.URL, Store: destination, ValidationScope: validationScope, Audits: []managedread.HistoryAudit{}}
	_, statErr := os.Lstat(destination)
	var acquired acquire.Result
	var err error
	if errors.Is(statErr, os.ErrNotExist) {
		result.Action = "clone"
		acquired, err = clone(ctx, configured.URL, acquire.Options{Destination: destination, DestinationProvided: true, ValidationScope: validationScope})
	} else if statErr == nil {
		result.Action = "reuse"
		acquired, err = reuse(ctx, configured.URL, destination, acquire.Options{ValidationScope: validationScope})
	} else {
		return result, statErr
	}
	result.Validation = checkerPointer(acquired.Validation)
	result.Audits = append(result.Audits, acquired.Audits...)
	if err != nil {
		return result, err
	}
	if result.Action == "clone" {
		if acquired.Published {
			result.Action = "cloned"
		} else {
			result.Action = "rejected"
		}
	} else {
		result.Action = "reused"
	}
	return result, nil
}

func inspectMemoryDir(name string) error {
	info, err := os.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("%w: %s is not a real directory", ErrConflict, MemoryDirName)
	}
	return nil
}

func ensureMemoryDir(name string) (bool, error) {
	if err := inspectMemoryDir(name); err != nil {
		return false, err
	}
	created := false
	if err := os.Mkdir(name, 0o700); err == nil {
		created = true
	} else if !errors.Is(err, os.ErrExist) {
		return false, err
	}
	return created, inspectMemoryDir(name)
}

func planGitignore(project string) (*filePlan, error) {
	name := filepath.Join(project, ".gitignore")
	original, info, err := readOptionalRegular(name)
	if err != nil {
		return nil, err
	}
	openOffsets := wholeLineOffsets(original, gitignoreOpen)
	closeOffsets := wholeLineOffsets(original, gitignoreClose)
	wantedLF := []byte(gitignoreOpen + "\n# Project-local Engram stores.\n/" + MemoryDirName + "/\n" + gitignoreClose + "\n")
	lineBreak := []byte("\n")
	if bytes.Contains(original, []byte("\r\n")) {
		lineBreak = []byte("\r\n")
	}
	wanted := bytes.ReplaceAll(wantedLF, []byte("\n"), lineBreak)
	if len(openOffsets) != 0 || len(closeOffsets) != 0 {
		if len(openOffsets) != 1 || len(closeOffsets) != 1 || openOffsets[0] >= closeOffsets[0] {
			return nil, fmt.Errorf("%w: malformed Engram block in .gitignore", ErrConflict)
		}
		end := lineEnd(original, closeOffsets[0])
		if end < 0 {
			return nil, fmt.Errorf("%w: modified Engram block in .gitignore", ErrConflict)
		}
		normalized := bytes.ReplaceAll(original[openOffsets[0]:end], []byte("\r\n"), []byte("\n"))
		if !bytes.Equal(normalized, wantedLF) {
			return nil, fmt.Errorf("%w: modified Engram block in .gitignore", ErrConflict)
		}
		return nil, nil
	}
	if containsWholeLine(original, "/"+MemoryDirName+"/") {
		return nil, nil
	}
	updated := append([]byte(nil), original...)
	if len(updated) != 0 && !bytes.HasSuffix(updated, lineBreak) {
		updated = append(updated, lineBreak...)
	}
	if len(updated) != 0 && !bytes.HasSuffix(updated, append(append([]byte(nil), lineBreak...), lineBreak...)) {
		updated = append(updated, lineBreak...)
	}
	updated = append(updated, wanted...)
	mode := os.FileMode(0o644)
	action := "created"
	if info != nil {
		mode = info.Mode().Perm()
		action = "updated"
	}
	return &filePlan{path: name, original: original, originalInfo: info, updated: updated, mode: mode, action: action}, nil
}

func readOptionalRegular(name string) ([]byte, os.FileInfo, error) {
	info, err := os.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, nil, fmt.Errorf("%w: %s is not a real regular file", ErrConflict, filepath.Base(name))
	}
	if err := fileidentity.Pin(info); err != nil {
		return nil, nil, fmt.Errorf("%w: capture %s identity: %v", ErrConflict, filepath.Base(name), err)
	}
	data, err := os.ReadFile(name)
	if err != nil {
		return nil, nil, err
	}
	after, err := os.Lstat(name)
	if err != nil || after.Mode()&os.ModeSymlink != 0 || !after.Mode().IsRegular() || !os.SameFile(info, after) ||
		after.Mode() != info.Mode() || after.Size() != info.Size() || after.ModTime() != info.ModTime() {
		return nil, nil, fmt.Errorf("%w: %s changed while being read", ErrConflict, filepath.Base(name))
	}
	return data, info, nil
}

func publishFile(plan *filePlan) error {
	if plan == nil {
		return nil
	}
	current, currentInfo, err := readOptionalRegular(plan.path)
	if err != nil {
		return err
	}
	if !bytes.Equal(current, plan.original) || plan.originalInfo == nil != (currentInfo == nil) || plan.originalInfo != nil && !os.SameFile(plan.originalInfo, currentInfo) {
		return fmt.Errorf("%w: %s changed concurrently", ErrConflict, filepath.Base(plan.path))
	}
	temporary, err := os.CreateTemp(filepath.Dir(plan.path), ".engram-project-*")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(plan.mode); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(plan.updated); err != nil {
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
	if err := os.Rename(temporaryName, plan.path); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(plan.path))
}

func syncDirectory(directory string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	handle, err := os.Open(directory)
	if err != nil {
		return err
	}
	return errors.Join(handle.Sync(), handle.Close())
}

func wholeLineOffsets(source []byte, wanted string) []int {
	var result []int
	for start := 0; start < len(source); {
		end := lineEnd(source, start)
		if end < 0 {
			end = len(source)
		}
		line := bytes.TrimSuffix(source[start:end], []byte("\n"))
		line = bytes.TrimSuffix(line, []byte("\r"))
		if string(line) == wanted {
			result = append(result, start)
		}
		if end == len(source) {
			break
		}
		start = end
	}
	return result
}

func lineEnd(source []byte, start int) int {
	if start < 0 || start > len(source) {
		return -1
	}
	if offset := bytes.IndexByte(source[start:], '\n'); offset >= 0 {
		return start + offset + 1
	}
	if start < len(source) {
		return len(source)
	}
	return -1
}

func containsWholeLine(source []byte, wanted string) bool {
	return len(wholeLineOffsets(source, wanted)) != 0
}

func harnessChanges(changes []harness.Change) []FileChange {
	result := make([]FileChange, len(changes))
	for index, change := range changes {
		result[index] = FileChange{Path: change.Path, Action: change.Action}
	}
	return result
}

func mergeChanges(left, right []FileChange) []FileChange {
	merged := make(map[string]string, len(left)+len(right))
	for _, change := range append(append([]FileChange(nil), left...), right...) {
		if previous, exists := merged[change.Path]; exists && previous == "created" {
			continue
		}
		merged[change.Path] = change.Action
	}
	result := make([]FileChange, 0, len(merged))
	for path, action := range merged {
		result = append(result, FileChange{Path: path, Action: action})
	}
	sort.Slice(result, func(left, right int) bool { return result[left].Path < result[right].Path })
	return result
}

func fileChange(project, name string) FileChange {
	action := "updated"
	if _, err := os.Lstat(name); errors.Is(err, os.ErrNotExist) {
		action = "created"
	}
	return FileChange{Path: relativeSlash(project, name), Action: action}
}

func relativeSlash(project, name string) string {
	relative, err := filepath.Rel(project, name)
	if err != nil {
		return filepath.ToSlash(name)
	}
	return filepath.ToSlash(relative)
}

func checkerPointer(value checker.Result) *checker.Result {
	copy := value
	return &copy
}

func stringPointer(value string) *string {
	copy := value
	return &copy
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
