package commands

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/ontopix/engram/internal/changeset"
	"github.com/ontopix/engram/internal/checker"
	"github.com/ontopix/engram/internal/cli"
	"github.com/ontopix/engram/internal/hooks"
	"github.com/ontopix/engram/internal/managedread"
)

// RegisterHooks installs complete-set hook inspection and trust operations
// using controller-owned configuration outside the selected store.
func RegisterHooks(app *cli.App) {
	root, err := os.UserConfigDir()
	if err != nil {
		registerHookConfigurationFailure(app, err)
		return
	}
	RegisterHooksAt(app, filepath.Join(root, "engram", "hook-trust-v1.json"))
}

// RegisterHooksAt is the deterministic embedding/test variant of
// RegisterHooks. registryPath is still checked by hooks.Registry to ensure it
// cannot be controlled from inside the selected store.
func RegisterHooksAt(app *cli.App, registryPath string) {
	registry, err := hooks.NewRegistry(registryPath)
	if err != nil {
		registerHookConfigurationFailure(app, err)
		return
	}
	registerHooksWith(app, registry)
}

type hookRegistry interface {
	List(string, hooks.Set) (hooks.Selection, error)
	Trust(string, hooks.Set) (hooks.Selection, error)
	Revoke(string, ...string) (hooks.RevokeResult, error)
}

func registerHooksWith(app *cli.App, registry hookRegistry) {
	if app == nil {
		return
	}
	if app.Handlers == nil {
		app.Handlers = make(map[cli.CommandName]cli.Handler)
	}
	if registry == nil {
		registerHookConfigurationFailure(app, errors.New("hook trust registry is unavailable"))
		return
	}
	app.Handlers[cli.CommandHooksList] = cli.HandlerFunc(func(ctx context.Context, invocation *cli.Invocation) cli.Result {
		return runHooksList(ctx, invocation, registry)
	})
	app.Handlers[cli.CommandHooksTrust] = cli.HandlerFunc(func(ctx context.Context, invocation *cli.Invocation) cli.Result {
		return runHooksTrust(ctx, invocation, registry)
	})
	app.Handlers[cli.CommandHooksRevoke] = cli.HandlerFunc(func(ctx context.Context, invocation *cli.Invocation) cli.Result {
		return runHooksRevoke(ctx, invocation, registry)
	})
}

func registerHookConfigurationFailure(app *cli.App, err error) {
	if app == nil {
		return
	}
	if app.Handlers == nil {
		app.Handlers = make(map[cli.CommandName]cli.Handler)
	}
	for _, name := range []cli.CommandName{cli.CommandHooksList, cli.CommandHooksTrust, cli.CommandHooksRevoke} {
		captured := err
		app.Handlers[name] = cli.HandlerFunc(func(context.Context, *cli.Invocation) cli.Result {
			return commandError(cli.ErrorCapability, fmt.Sprintf("locate hook trust configuration: %v", captured))
		})
	}
}

type hookSelectionResult struct {
	State   string       `json:"state"`
	Changed bool         `json:"changed"`
	SHA256  string       `json:"sha256"`
	Trusted bool         `json:"trusted"`
	Hooks   []hooks.Hook `json:"hooks"`
}

func runHooksList(ctx context.Context, invocation *cli.Invocation, registry hookRegistry) cli.Result {
	root, state, set, result := selectHooks(ctx, invocation)
	if result != nil {
		return *result
	}
	selection, err := registry.List(root, set)
	if err != nil {
		return hookFailure(err, "inspect hook trust")
	}
	return cli.Result{Outcome: cli.OutcomeOK, Value: hookResult(state, selection)}
}

func runHooksTrust(ctx context.Context, invocation *cli.Invocation, registry hookRegistry) cli.Result {
	root, state, set, result := selectHooks(ctx, invocation)
	if result != nil {
		return *result
	}
	selection, err := registry.Trust(root, set)
	if err != nil {
		return hookFailure(err, "trust hook set")
	}
	return cli.Result{Outcome: cli.OutcomeOK, Value: hookResult(state, selection)}
}

func runHooksRevoke(ctx context.Context, invocation *cli.Invocation, registry hookRegistry) cli.Result {
	store, result := openManaged(ctx, invocation)
	if result != nil {
		return *result
	}
	if result := requireAcceptedAudit(ctx, store); result != nil {
		return *result
	}
	revoked, err := registry.Revoke(store.Repository().Root, invocation.Arguments...)
	if err != nil {
		return hookFailure(err, "revoke hook trust")
	}
	return cli.Result{Outcome: cli.OutcomeOK, Value: revoked}
}

func selectHooks(ctx context.Context, invocation *cli.Invocation) (string, string, hooks.Set, *cli.Result) {
	store, result := openManaged(ctx, invocation)
	if result != nil {
		return "", "", hooks.Set{}, result
	}
	audit, err := store.AuditAccepted(ctx)
	if err != nil {
		failed := managedFailure(err, "audit accepted history")
		return "", "", hooks.Set{}, &failed
	}
	if audit.Validation.Status != checker.StatusComplete || audit.Validation.HasErrors() {
		failed := commandError(cli.ErrorRepository, "accepted history is not a conforming managed store")
		return "", "", hooks.Set{}, &failed
	}
	state := "accepted"
	if selected, present := invocation.Options.One("state"); present {
		state = selected
	}
	accepted := audit.Snapshots[audit.Tip]
	if accepted == nil || accepted.Tree == nil {
		failed := commandError(cli.ErrorInternal, "accepted audit has no tip snapshot")
		return "", "", hooks.Set{}, &failed
	}
	var tree = accepted.Tree
	if state == "working" {
		working, err := store.Working(ctx)
		if err != nil {
			failed := managedFailure(err, "inspect working hook state")
			return "", "", hooks.Set{}, &failed
		}
		tree = working.Snapshot.Tree
	} else if state == "staged" {
		staged, err := store.Staged(ctx)
		if err != nil {
			failed := managedFailure(err, "inspect staged initial candidate")
			return "", "", hooks.Set{}, &failed
		}
		if staged.Snapshot == nil || staged.Snapshot.Tree == nil {
			failed := commandError(cli.ErrorRepository, "staged initial candidate is unavailable")
			return "", "", hooks.Set{}, &failed
		}
		if !changeset.PreflightOK(staged.Snapshot.Tree) {
			failed := commandError(cli.ErrorRepository, "staged initial candidate fails changeset preflight")
			return "", "", hooks.Set{}, &failed
		}
		if _, err := hooks.SelectTree(staged.Snapshot.Tree); err != nil {
			failed := hookFailure(err, "validate staged hook tree")
			return "", "", hooks.Set{}, &failed
		}
		initial := changeset.Diff(accepted.Tree, staged.Snapshot.Tree)
		set, err := hooks.SelectTreeForChanges(accepted.Tree, initial)
		if err != nil {
			failed := hookFailure(err, "select applicable base hook set")
			return "", "", hooks.Set{}, &failed
		}
		return store.Repository().Root, state, set, nil
	}
	set, err := hooks.SelectTree(tree)
	if err != nil {
		failed := hookFailure(err, "select hook set")
		return "", "", hooks.Set{}, &failed
	}
	return store.Repository().Root, state, set, nil
}

func requireAcceptedAudit(ctx context.Context, store *managedread.Store) *cli.Result {
	audit, err := store.AuditAccepted(ctx)
	if err != nil {
		result := managedFailure(err, "audit accepted history")
		return &result
	}
	if audit.Validation.Status != checker.StatusComplete || audit.Validation.HasErrors() {
		result := commandError(cli.ErrorRepository, "accepted history is not a conforming managed store")
		return &result
	}
	return nil
}

func hookResult(state string, selection hooks.Selection) hookSelectionResult {
	return hookSelectionResult{
		State: state, Changed: selection.Changed, SHA256: selection.SHA256,
		Trusted: selection.Trusted, Hooks: selection.Hooks,
	}
}

func hookFailure(err error, action string) cli.Result {
	if err == nil {
		return commandError(cli.ErrorInternal, action+": unknown failure")
	}
	kind := cli.ErrorRepository
	effect, hasEffect := hooks.EffectOf(err)
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		kind = cli.ErrorCancelled
	case errors.Is(err, hooks.ErrConcurrent):
		kind = cli.ErrorConcurrency
	case errors.Is(err, hooks.ErrInvalidName):
		kind = cli.ErrorUsage
	case errors.Is(err, hooks.ErrCorruptRegistry), errors.Is(err, hooks.ErrUnsafePermissions),
		errors.Is(err, hooks.ErrConfigInsideStore), errors.Is(err, hooks.ErrPhysicalIdentity):
		kind = cli.ErrorIntegration
	case errors.Is(err, hooks.ErrInvalidSelection):
		kind = cli.ErrorRepository
	case errors.Is(err, fs.ErrPermission):
		kind = cli.ErrorIO
	}
	if hasEffect && kind == cli.ErrorRepository {
		kind = cli.ErrorIO
	}
	result := commandError(kind, fmt.Sprintf("%s: %v", action, err))
	if hasEffect {
		mutation := cli.NewMutationResult()
		mutation.Durable = effect.Durable
		mutation.RecoveryRequired = effect.RecoveryRequired
		result.Value = mutation
	}
	return result
}
