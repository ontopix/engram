// Package commands registers concrete command handlers on the protocol-facing
// CLI application. This file contains only portable snapshot operations; it
// has no Git or network dependency.
package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"strconv"
	"strings"

	"github.com/ontopix/engram/internal/checker"
	"github.com/ontopix/engram/internal/cli"
	"github.com/ontopix/engram/internal/discovery"
	"github.com/ontopix/engram/internal/snapshot"
	"github.com/ontopix/engram/schemas"
)

// RegisterPortable installs the M1 handlers on app. It is safe to call more
// than once; the portable handlers simply replace the same command entries.
func RegisterPortable(app *cli.App) {
	if app == nil {
		return
	}
	if app.Handlers == nil {
		app.Handlers = make(map[cli.CommandName]cli.Handler)
	}
	app.Handlers[cli.CommandCheck] = cli.HandlerFunc(runCheck)
	app.Handlers[cli.CommandSchemaInventory] = cli.HandlerFunc(runSchemaInventory)
	app.Handlers[cli.CommandSchemaList] = cli.HandlerFunc(runSchemaList)
	app.Handlers[cli.CommandSchemaShow] = cli.HandlerFunc(runSchemaShow)
}

func runCheck(ctx context.Context, invocation *cli.Invocation) cli.Result {
	if result := cancellation(ctx); result != nil {
		return *result
	}
	if invocation == nil {
		return commandError(cli.ErrorInternal, "check invocation is nil")
	}
	if invocation.Options.Has("accepted") {
		return commandError(cli.ErrorCapability, "accepted managed-state checking requires the managed-read adapter")
	}
	if invocation.Options.Has("history") {
		return commandError(cli.ErrorCapability, "managed history checking requires the managed-read adapter")
	}
	if invocation.Options.Has("staged") {
		return commandError(cli.ErrorCapability, "staged candidate checking requires the managed-read adapter")
	}
	if baseName, pair := invocation.Options.One("base"); pair {
		candidateName, _ := invocation.Options.One("candidate")
		if baseName == "" || candidateName == "" {
			return commandError(cli.ErrorUsage, "--base and --candidate require non-empty paths")
		}
		baseRoot, err := discovery.Exact(baseName)
		if err != nil {
			return failure(err, cli.ErrorRepository, "select base snapshot")
		}
		candidateRoot, err := discovery.Exact(candidateName)
		if err != nil {
			return failure(err, cli.ErrorRepository, "select candidate snapshot")
		}
		base, err := checker.CheckFS(baseRoot)
		if err != nil {
			return failure(err, cli.ErrorIO, "check base snapshot")
		}
		if result := cancellation(ctx); result != nil {
			return *result
		}
		candidate, err := checker.CheckFS(candidateRoot)
		if err != nil {
			return failure(err, cli.ErrorIO, "check candidate snapshot")
		}
		validation, _ := checker.CheckTransition(base, candidate, false)
		return validationResult(validation)
	}

	var root string
	var err error
	if len(invocation.Arguments) == 1 {
		if invocation.Arguments[0] == "" {
			return commandError(cli.ErrorUsage, "check PATH must not be empty")
		}
		root, err = discovery.Exact(invocation.Arguments[0])
	} else {
		root, err = selectedStore(invocation)
	}
	if err != nil {
		return failure(err, cli.ErrorRepository, "select snapshot")
	}
	checked, err := checker.CheckFS(root)
	if err != nil {
		return failure(err, cli.ErrorIO, "check snapshot")
	}
	return validationResult(checked.Validation)
}

type schemaListResult struct {
	Schemas []checker.SchemaDescription `json:"schemas"`
}

type schemaShowResult struct {
	Schema  checker.SchemaDescription `json:"schema"`
	Content string                    `json:"content"`
}

func runSchemaInventory(ctx context.Context, _ *cli.Invocation) cli.Result {
	if result := cancellation(ctx); result != nil {
		return *result
	}
	entries, err := schemas.Inventory()
	if err != nil {
		return failure(err, cli.ErrorInternal, "load embedded schema inventory")
	}
	descriptions := make([]checker.SchemaDescription, len(entries))
	for index, entry := range entries {
		descriptions[index] = checker.SchemaDescription{
			Type: entry.Type, Source: "inventory", Path: nil,
			Version: json.Number(strconv.FormatInt(entry.Version, 10)), Description: entry.Description,
		}
	}
	return cli.Result{Outcome: cli.OutcomeOK, Value: schemaListResult{Schemas: descriptions}}
}

func runSchemaList(ctx context.Context, invocation *cli.Invocation) cli.Result {
	return withLocalSchemas(ctx, invocation, func(checked *checker.Snapshot, at string) cli.Result {
		descriptions, err := checked.VisibleSchemas(at)
		if err != nil {
			return failure(err, cli.ErrorRepository, "list visible schemas")
		}
		if descriptions == nil {
			descriptions = []checker.SchemaDescription{}
		}
		return cli.Result{Outcome: cli.OutcomeOK, Value: schemaListResult{Schemas: descriptions}}
	})
}

func runSchemaShow(ctx context.Context, invocation *cli.Invocation) cli.Result {
	if result := cancellation(ctx); result != nil {
		return *result
	}
	if invocation == nil || len(invocation.Arguments) != 1 {
		return commandError(cli.ErrorInternal, "schema.show invocation has invalid arguments")
	}
	typeName := invocation.Arguments[0]
	if !snapshot.ValidTypeSlug(typeName) {
		return commandError(cli.ErrorUsage, fmt.Sprintf("invalid schema type %q", typeName))
	}
	return withLocalSchemas(ctx, invocation, func(checked *checker.Snapshot, at string) cli.Result {
		description, content, err := checked.ShowSchema(at, typeName)
		if err != nil {
			return failure(err, cli.ErrorRepository, "show visible schema")
		}
		return cli.Result{Outcome: cli.OutcomeOK, Value: schemaShowResult{Schema: description, Content: content}}
	})
}

func withLocalSchemas(ctx context.Context, invocation *cli.Invocation, query func(*checker.Snapshot, string) cli.Result) cli.Result {
	if result := cancellation(ctx); result != nil {
		return *result
	}
	if invocation == nil || query == nil {
		return commandError(cli.ErrorInternal, "schema invocation is invalid")
	}
	at := "."
	if value, present := invocation.Options.One("at"); present {
		if !validLogicalDirectory(value) {
			return commandError(cli.ErrorUsage, fmt.Sprintf("invalid logical content directory %q", value))
		}
		at = value
	}
	root, err := selectedStore(invocation)
	if err != nil {
		return failure(err, cli.ErrorRepository, "select store")
	}
	checked, err := checker.CheckFS(root)
	if err != nil {
		return failure(err, cli.ErrorIO, "inspect store")
	}
	if result := cancellation(ctx); result != nil {
		return *result
	}
	return query(checked, at)
}

func selectedStore(invocation *cli.Invocation) (string, error) {
	if invocation != nil && invocation.Globals.StoreSet {
		if invocation.Globals.Store == "" {
			return "", fmt.Errorf("store path is empty")
		}
		return discovery.Exact(invocation.Globals.Store)
	}
	workingDirectory, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return discovery.From(workingDirectory)
}

func validLogicalDirectory(value string) bool {
	if value == "." {
		return true
	}
	if value == "" || strings.HasPrefix(value, "/") || strings.Contains(value, "\\") || path.Clean(value) != value {
		return false
	}
	for _, segment := range strings.Split(value, "/") {
		if !snapshot.ValidContentName(segment) {
			return false
		}
	}
	return true
}

func validationResult(validation checker.Result) cli.Result {
	switch validation.Status {
	case checker.StatusIndeterminate:
		return cli.Result{Outcome: cli.OutcomeIndeterminate, Value: validation}
	case checker.StatusComplete:
		if validation.HasErrors() {
			return cli.Result{Outcome: cli.OutcomeIssues, Value: validation}
		}
		return cli.Result{Outcome: cli.OutcomeOK, Value: validation}
	default:
		return commandError(cli.ErrorInternal, fmt.Sprintf("checker returned unknown status %q", validation.Status))
	}
}

func cancellation(ctx context.Context) *cli.Result {
	if ctx == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		result := commandError(cli.ErrorCancelled, "operation cancelled")
		return &result
	default:
		return nil
	}
}

func failure(err error, fallback cli.ErrorKind, action string) cli.Result {
	if err == nil {
		return commandError(cli.ErrorInternal, action+": unknown failure")
	}
	kind := fallback
	var capability *checker.CapabilityError
	switch {
	case errors.As(err, &capability):
		kind = cli.ErrorCapability
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		kind = cli.ErrorCancelled
	case errors.Is(err, discovery.ErrNotFound):
		kind = cli.ErrorRepository
	case errors.Is(err, fs.ErrPermission):
		kind = cli.ErrorIO
	}
	return commandError(kind, fmt.Sprintf("%s: %v", action, err))
}

func commandError(kind cli.ErrorKind, message string) cli.Result {
	return cli.Result{Outcome: cli.OutcomeError, Error: &cli.ProtocolError{Kind: kind, Message: message}}
}
