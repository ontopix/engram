package hookexec

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ontopix/engram/internal/changeset"
	"github.com/ontopix/engram/internal/checker"
	"github.com/ontopix/engram/internal/gitraw"
	"github.com/ontopix/engram/internal/hookprotocol"
	"github.com/ontopix/engram/internal/hooks"
	"github.com/ontopix/engram/internal/snapshot"
	"github.com/ontopix/engram/internal/treeimage"
)

type candidateState struct {
	image    treeimage.Image
	snapshot *checker.Snapshot
}

func (e *Executor) prepare(ctx context.Context, request Request) (*Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, typed(ErrorCapability, "", nil, nil, err)
	}
	worktreeRoot, err := validateRoots(request.StoreRoot, request.WorktreeRoot)
	if err != nil {
		return nil, typed(ErrorCapability, "", nil, nil, fmt.Errorf("%w: %v", ErrCapability, err))
	}
	if request.Initial == nil || request.Initial.Tree == nil {
		return nil, typed(ErrorCapability, "", nil, nil, fmt.Errorf("%w: initial candidate snapshot is unavailable", ErrCapability))
	}
	if request.Initialization {
		if request.Base != nil {
			return nil, typed(ErrorCapability, "", nil, nil, fmt.Errorf("%w: initialization must have an absent base", ErrCapability))
		}
	} else if request.Base == nil || request.Base.Tree == nil {
		return nil, typed(ErrorCapability, "", nil, nil, fmt.Errorf("%w: non-initialization base is unavailable", ErrCapability))
	}
	// Check the caller's original projections before constructing logical
	// images; FromSnapshot intentionally omits pruned wrong-kind boundaries.
	if !changeset.PreflightOK(request.Initial.Tree) {
		return nil, typed(ErrorHook, "", nil, nil, fmt.Errorf("%w: initial candidate fails changeset preflight", ErrRejected))
	}
	if request.Base != nil && !changeset.PreflightOK(request.Base.Tree) {
		return nil, typed(ErrorHook, "", nil, nil, fmt.Errorf("%w: base fails changeset preflight", ErrRejected))
	}
	if err := validateModes(request.Base, request.Initial, request.BaseModes, request.InitialModes, request.Initialization); err != nil {
		return nil, typed(ErrorCapability, "", nil, nil, fmt.Errorf("%w: %v", ErrCapability, err))
	}

	var baseImage treeimage.Image
	var baseSnapshot *checker.Snapshot
	if request.Base != nil {
		baseImage, err = treeimage.FromSnapshot(request.Base.Tree, request.BaseModes)
		if err != nil {
			return nil, typed(ErrorCapability, "", nil, nil, fmt.Errorf("%w: base image: %v", ErrCapability, err))
		}
		baseSnapshot, err = analyzeImage(baseImage)
		if err != nil {
			return nil, typed(ErrorHook, "", nil, nil, fmt.Errorf("%w: base analysis: %v", ErrRejected, err))
		}
		if baseSnapshot.Validation.Status != checker.StatusComplete || baseSnapshot.Validation.HasErrors() {
			validation := baseSnapshot.Validation
			return nil, typed(ErrorHook, "", nil, &validation, fmt.Errorf("%w: base snapshot is not conforming", ErrRejected))
		}
		if !changeset.PreflightOK(baseSnapshot.Tree) {
			return nil, typed(ErrorHook, "", nil, nil, fmt.Errorf("%w: base fails changeset preflight", ErrRejected))
		}
	} else {
		baseImage = make(treeimage.Image)
	}

	initialImage, err := treeimage.FromSnapshot(request.Initial.Tree, request.BaseModes)
	if err != nil {
		return nil, typed(ErrorCapability, "", nil, nil, fmt.Errorf("%w: initial candidate image: %v", ErrCapability, err))
	}
	currentSnapshot, err := analyzeImage(initialImage)
	if err != nil {
		return nil, typed(ErrorHook, "", nil, nil, fmt.Errorf("%w: initial candidate analysis: %v", ErrRejected, err))
	}
	if !changeset.PreflightOK(currentSnapshot.Tree) {
		return nil, typed(ErrorHook, "", nil, nil, fmt.Errorf("%w: initial candidate fails changeset preflight", ErrRejected))
	}
	current := candidateState{image: initialImage, snapshot: currentSnapshot}

	initialChanges := changeset.Diff(snapshotTree(baseSnapshot), current.snapshot.Tree)
	selected := hooks.EmptySet()
	if !request.Initialization && len(initialChanges) != 0 {
		selected, err = hooks.SelectTree(baseSnapshot.Tree)
		if err != nil {
			return nil, typed(ErrorHook, "", nil, nil, fmt.Errorf("%w: select base hooks: %v", ErrRejected, err))
		}
	}
	if err := e.establishTrust(request.StoreRoot, selected); err != nil {
		return nil, err
	}

	diagnostics := make([]Diagnostic, 0, len(selected.Hooks))
	for _, hook := range selected.Hooks {
		if err := ctx.Err(); err != nil {
			return nil, typed(ErrorHook, hook.Path, nil, nil, fmt.Errorf("%w: %v", ErrRejected, err))
		}
		currentChanges := changeset.Diff(snapshotTree(baseSnapshot), current.snapshot.Tree)
		input, err := hookprotocol.MarshalInput(currentChanges)
		if err != nil {
			return nil, typed(ErrorCapability, hook.Path, nil, nil, fmt.Errorf("%w: serialize hook input: %v", ErrCapability, err))
		}
		next, diagnostic, err := e.runOne(ctx, worktreeRoot, baseImage, request.BaseModes, current.image, hook, input)
		if err != nil {
			return nil, err
		}
		diagnostics = append(diagnostics, diagnostic)
		current = next
	}
	if err := ctx.Err(); err != nil {
		return nil, typed(ErrorHook, "", nil, nil, fmt.Errorf("%w: %v", ErrRejected, err))
	}

	// Definitive changes and validation are recomputed only from the latest
	// controller-private image, never from a hook-exposed directory.
	finalSnapshot, err := analyzeImage(current.image)
	if err != nil {
		return nil, typed(ErrorHook, "", nil, nil, fmt.Errorf("%w: final private capture: %v", ErrRejected, err))
	}
	validation, definitiveChanges := checker.CheckTransition(baseSnapshot, finalSnapshot, request.Initialization)
	if validation.Status != checker.StatusComplete || validation.HasErrors() {
		copy := validation
		return nil, typed(ErrorHook, "", nil, &copy, fmt.Errorf("%w: final candidate validation failed", ErrRejected))
	}
	finalImage, err := treeimage.FromSnapshot(finalSnapshot.Tree, request.BaseModes)
	if err != nil {
		return nil, typed(ErrorCapability, "", nil, nil, fmt.Errorf("%w: derive final candidate: %v", ErrCapability, err))
	}
	if !treeimage.Equal(current.image, finalImage) {
		return nil, typed(ErrorConcurrency, "", nil, nil, fmt.Errorf("%w: final derivation differs from private capture", ErrConcurrent))
	}
	return &Result{
		Capture:     cloneImage(finalImage),
		Final:       finalSnapshot,
		Modes:       modesFromImage(finalSnapshot, finalImage),
		Changes:     append([]changeset.Change(nil), definitiveChanges...),
		Validation:  validation,
		SetSHA256:   selected.SHA256,
		Diagnostics: append([]Diagnostic(nil), diagnostics...),
	}, nil
}

func (e *Executor) establishTrust(storeRoot string, set hooks.Set) error {
	if e.Trust == nil {
		return typed(ErrorCapability, "", nil, nil, fmt.Errorf("%w: hook trust state is unavailable", ErrCapability))
	}
	selection, err := e.Trust.List(storeRoot, set)
	if err != nil {
		switch {
		case errors.Is(err, hooks.ErrConcurrent):
			return typed(ErrorConcurrency, "", nil, nil, errors.Join(ErrConcurrent, err))
		case errors.Is(err, hooks.ErrPhysicalIdentity):
			return typed(ErrorCapability, "", nil, nil, errors.Join(ErrCapability, err))
		default:
			return typed(ErrorTrust, "", nil, nil, err)
		}
	}
	if selection.SHA256 != set.SHA256 || !sameHookDescriptions(selection.Hooks, set.Hooks) {
		return typed(ErrorTrust, "", nil, nil, fmt.Errorf("%w: trust lookup returned a different complete set", ErrUntrusted))
	}
	if !selection.Trusted {
		return typed(ErrorTrust, "", nil, nil, ErrUntrusted)
	}
	return nil
}

func (e *Executor) runOne(ctx context.Context, worktreeRoot string, baseImage treeimage.Image, baseModes map[string]gitraw.TreeMode, current treeimage.Image, hook hooks.Hook, input []byte) (result candidateState, diagnostic Diagnostic, resultErr error) {
	exposedParent, err := e.temporaryDirectory(worktreeRoot, "engram-hook-exposed-")
	if err != nil {
		return result, diagnostic, typed(ErrorCapability, hook.Path, nil, nil, fmt.Errorf("%w: create disposable trees: %v", ErrCapability, err))
	}
	defer func() {
		if exposedParent != "" {
			if cleanupErr := cleanupTree(exposedParent); cleanupErr != nil {
				cleanupErr = fmt.Errorf("abandon exposed trees: %w", cleanupErr)
				if resultErr == nil {
					resultErr = typed(ErrorCapability, hook.Path, &diagnostic, nil, errors.Join(ErrCapability, cleanupErr))
				} else {
					resultErr = errors.Join(resultErr, cleanupErr)
				}
			}
		}
	}()
	baseRoot := filepath.Join(exposedParent, "base")
	candidateRoot := filepath.Join(exposedParent, "candidate")
	if err := treeimage.Materialize(baseRoot, baseImage, true); err != nil {
		return result, diagnostic, typed(ErrorCapability, hook.Path, nil, nil, fmt.Errorf("%w: materialize immutable base: %v", ErrCapability, err))
	}
	if err := treeimage.Materialize(candidateRoot, current, false); err != nil {
		return result, diagnostic, typed(ErrorCapability, hook.Path, nil, nil, fmt.Errorf("%w: materialize candidate: %v", ErrCapability, err))
	}
	if err := verifyImageRoot(baseRoot, baseImage); err != nil {
		return result, diagnostic, typed(ErrorConcurrency, hook.Path, nil, nil, fmt.Errorf("%w: base materialization: %v", ErrConcurrent, err))
	}
	if err := verifyImageRoot(candidateRoot, current); err != nil {
		return result, diagnostic, typed(ErrorConcurrency, hook.Path, nil, nil, fmt.Errorf("%w: candidate materialization: %v", ErrConcurrent, err))
	}

	diagnostic, err = e.invoke(ctx, hook, input, baseRoot, candidateRoot)
	if err != nil {
		return result, diagnostic, err
	}
	if err := verifyImageRoot(baseRoot, baseImage); err != nil {
		return result, diagnostic, typed(ErrorHook, hook.Path, &diagnostic, nil, fmt.Errorf("%w: exposed base changed: %v", ErrRejected, err))
	}
	sourceImage, err := treeimage.Capture(candidateRoot, true)
	if err != nil {
		return result, diagnostic, typed(ErrorConcurrency, hook.Path, &diagnostic, nil, fmt.Errorf("%w: candidate capture: %v", ErrConcurrent, err))
	}
	if e.afterSourceCapture != nil {
		e.afterSourceCapture(candidateRoot)
	}
	projected, err := analyzeImage(sourceImage)
	if err != nil {
		return result, diagnostic, typed(ErrorHook, hook.Path, &diagnostic, nil, fmt.Errorf("%w: hook-produced candidate: %v", ErrRejected, err))
	}
	if err := treeimage.LogicalOnly(sourceImage, projected.Tree); err != nil {
		return result, diagnostic, typed(ErrorHook, hook.Path, &diagnostic, nil, fmt.Errorf("%w: %v", ErrRejected, err))
	}
	if !changeset.PreflightOK(projected.Tree) {
		return result, diagnostic, typed(ErrorHook, hook.Path, &diagnostic, nil, fmt.Errorf("%w: hook-produced candidate fails boundary/layout preflight", ErrRejected))
	}
	normalized, err := treeimage.FromSnapshot(projected.Tree, baseModes)
	if err != nil {
		return result, diagnostic, typed(ErrorHook, hook.Path, &diagnostic, nil, fmt.Errorf("%w: normalize hook-produced candidate: %v", ErrRejected, err))
	}
	if !treeimage.Equal(sourceImage, normalized) {
		return result, diagnostic, typed(ErrorHook, hook.Path, &diagnostic, nil, fmt.Errorf("%w: hook-produced candidate is not wholly logical", ErrRejected))
	}

	privateParent, err := e.temporaryDirectory(worktreeRoot, "engram-hook-capture-")
	if err != nil {
		return result, diagnostic, typed(ErrorCapability, hook.Path, &diagnostic, nil, fmt.Errorf("%w: create private capture: %v", ErrCapability, err))
	}
	defer func() {
		if privateParent != "" {
			if cleanupErr := cleanupTree(privateParent); cleanupErr != nil {
				cleanupErr = fmt.Errorf("release private filesystem copy: %w", cleanupErr)
				if resultErr == nil {
					resultErr = typed(ErrorCapability, hook.Path, &diagnostic, nil, errors.Join(ErrCapability, cleanupErr))
				} else {
					resultErr = errors.Join(resultErr, cleanupErr)
				}
			}
		}
	}()
	privateRoot := filepath.Join(privateParent, "candidate")
	if err := treeimage.Materialize(privateRoot, normalized, false); err != nil {
		return result, diagnostic, typed(ErrorCapability, hook.Path, &diagnostic, nil, fmt.Errorf("%w: copy private capture: %v", ErrCapability, err))
	}
	privateImage, err := treeimage.Capture(privateRoot, true)
	if err != nil {
		return result, diagnostic, typed(ErrorConcurrency, hook.Path, &diagnostic, nil, fmt.Errorf("%w: observe private capture: %v", ErrConcurrent, err))
	}
	sourceAgain, err := treeimage.Capture(candidateRoot, true)
	if err != nil || !treeimage.Equal(sourceImage, sourceAgain) {
		return result, diagnostic, typed(ErrorConcurrency, hook.Path, &diagnostic, nil, fmt.Errorf("%w: exposed candidate changed during private capture", ErrConcurrent))
	}
	if !treeimage.Equal(sourceImage, privateImage) {
		return result, diagnostic, typed(ErrorConcurrency, hook.Path, &diagnostic, nil, fmt.Errorf("%w: private copy differs from exposed candidate", ErrConcurrent))
	}
	if err := verifyImageRoot(baseRoot, baseImage); err != nil {
		return result, diagnostic, typed(ErrorHook, hook.Path, &diagnostic, nil, fmt.Errorf("%w: exposed base changed during capture: %v", ErrRejected, err))
	}
	privateSnapshot, err := analyzeImage(privateImage)
	if err != nil {
		return result, diagnostic, typed(ErrorHook, hook.Path, &diagnostic, nil, fmt.Errorf("%w: analyze private capture: %v", ErrRejected, err))
	}

	if err := cleanupTree(exposedParent); err != nil {
		return result, diagnostic, typed(ErrorCapability, hook.Path, &diagnostic, nil, fmt.Errorf("%w: abandon exposed trees: %v", ErrCapability, err))
	}
	exposedParent = ""
	if err := cleanupTree(privateParent); err != nil {
		return result, diagnostic, typed(ErrorCapability, hook.Path, &diagnostic, nil, fmt.Errorf("%w: release private filesystem copy: %v", ErrCapability, err))
	}
	privateParent = ""
	return candidateState{image: cloneImage(privateImage), snapshot: privateSnapshot}, diagnostic, nil
}

func analyzeImage(image treeimage.Image) (*checker.Snapshot, error) {
	source, err := newImageSource(image)
	if err != nil {
		return nil, err
	}
	return checker.CheckSource(source)
}

func snapshotTree(value *checker.Snapshot) *snapshot.Tree {
	if value == nil {
		return nil
	}
	return value.Tree
}

func validateRoots(storeRoot, worktreeRoot string) (string, error) {
	store, err := realDirectory(storeRoot)
	if err != nil {
		return "", fmt.Errorf("store root: %w", err)
	}
	if worktreeRoot == "" {
		return store, nil
	}
	worktree, err := realDirectory(worktreeRoot)
	if err != nil {
		return "", fmt.Errorf("worktree root: %w", err)
	}
	storeInfo, err := os.Stat(store)
	if err != nil {
		return "", err
	}
	worktreeInfo, err := os.Stat(worktree)
	if err != nil {
		return "", err
	}
	if !os.SameFile(storeInfo, worktreeInfo) {
		return "", fmt.Errorf("store and worktree roots identify different directories")
	}
	return worktree, nil
}

func validateModes(base, initial *checker.Snapshot, baseModes, initialModes map[string]gitraw.TreeMode, initialization bool) error {
	if initialization {
		if len(baseModes) != 0 {
			return fmt.Errorf("initialization has base modes")
		}
	} else if err := exactModes(base, baseModes, nil); err != nil {
		return fmt.Errorf("base modes: %w", err)
	}
	wantInitial := make(map[string]gitraw.TreeMode, len(initial.Tree.Files))
	for name := range initial.Tree.Files {
		want := gitraw.ModeRegular
		if baseMode, survives := baseModes[name]; survives {
			want = baseMode
		}
		wantInitial[name] = want
	}
	return exactModes(initial, initialModes, wantInitial)
}

func exactModes(value *checker.Snapshot, modes map[string]gitraw.TreeMode, expected map[string]gitraw.TreeMode) error {
	if value == nil || value.Tree == nil {
		return fmt.Errorf("snapshot is unavailable")
	}
	if len(modes) != len(value.Tree.Files) {
		return fmt.Errorf("mode map does not cover exactly every logical file")
	}
	for name := range value.Tree.Files {
		mode, exists := modes[name]
		if !exists || !mode.IsRegular() {
			return fmt.Errorf("%q has no admitted regular mode", name)
		}
		if expected != nil && mode != expected[name] {
			return fmt.Errorf("%q mode %s, want %s", name, mode, expected[name])
		}
	}
	for name := range modes {
		if _, exists := value.Tree.Files[name]; !exists {
			return fmt.Errorf("mode map contains non-logical path %q", name)
		}
	}
	return nil
}

func sameHookDescriptions(left, right []hooks.Hook) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].Path != right[index].Path || left[index].Interpreter != right[index].Interpreter || left[index].SHA256 != right[index].SHA256 {
			return false
		}
	}
	return true
}

func modesFromImage(final *checker.Snapshot, image treeimage.Image) map[string]gitraw.TreeMode {
	result := make(map[string]gitraw.TreeMode, len(final.Tree.Files))
	for name := range final.Tree.Files {
		mode := gitraw.ModeRegular
		if image[name].Mode&0o111 != 0 {
			mode = gitraw.ModeExecutable
		}
		result[name] = mode
	}
	return result
}
