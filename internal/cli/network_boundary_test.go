package cli

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const modulePath = "github.com/ontopix/engram"

// TestInterfaceGateRepositoryNetworkBoundary is a source-level authority
// audit, not a mock which merely proves that one happy path stayed local. It
// makes both sides of the boundary explicit:
//
//   - every literal capable of selecting a Git repository-network operation
//     is confined to the clone, declarative setup, pull, and push controllers
//     (apart from command grammar and remote-selection metadata); and
//   - those controllers can enter the command layer only through their named
//     adapters. Doctor may inspect/reconcile exact recovery state, but does not
//     get an unreviewed network entry point.
func TestInterfaceGateRepositoryNetworkBoundary(t *testing.T) {
	root := interfaceGateRepositoryRoot(t)
	files := parseProductionFiles(t, filepath.Join(root, "internal"))

	wantNetworkCommands := map[CommandName]bool{
		CommandClone: true,
		CommandSetup: true,
		CommandPull:  true,
		CommandPush:  true,
	}
	for _, golden := range protocolCommandGoldens {
		_, network := wantNetworkCommands[golden.name]
		if network != (golden.name == CommandClone || golden.name == CommandSetup || golden.name == CommandPull || golden.name == CommandPush) {
			t.Fatalf("invalid network command classification for %q", golden.name)
		}
	}
	if len(wantNetworkCommands) != 4 {
		t.Fatalf("network command count = %d, want 4", len(wantNetworkCommands))
	}

	networkVerbs := map[string]bool{
		"clone": true, "fetch": true, "pull": true, "push": true,
		"ls-remote": true, "request-pull": true, "submodule": true,
		"upload-pack": true, "receive-pack": true,
	}
	foundRequired := map[string]bool{}
	for relative, parsed := range files {
		for _, use := range findNetworkLiterals(parsed, networkVerbs) {
			if networkLiteralAllowed(relative, use.function, use.value) {
				if relative != "internal/cli/model.go" && relative != "internal/remoteselect/select.go" {
					foundRequired[use.value] = true
				}
				continue
			}
			t.Errorf("Git repository-network selector %q escaped the reviewed controller function in %s:%s", use.value, relative, use.function)
		}
	}
	for _, verb := range []string{"clone", "fetch", "ls-remote", "push"} {
		if !foundRequired[verb] {
			t.Errorf("reviewed network controller no longer contains required Git operation %q", verb)
		}
	}

	allowedImporters := map[string]map[string]bool{
		modulePath + "/internal/acquire": {
			"internal/commands/acquire.go":   true,
			"internal/commands/setup.go":     true,
			"internal/projectsetup/setup.go": true,
			// Acquisition recovery is exact-target cleanup and verification;
			// it is specified to be network-silent.
			"internal/commands/doctor.go": true,
			"internal/doctor/recovery.go": true,
		},
		modulePath + "/internal/pullflow": {
			"internal/commands/pull.go":   true,
			"internal/commands/doctor.go": true,
			"internal/doctor/recovery.go": true,
		},
		modulePath + "/internal/syncflow": {
			"internal/commands/sync.go": true,
		},
		modulePath + "/internal/remoteselect": {
			"internal/pullflow/network.go": true,
			"internal/syncflow/push.go":    true,
		},
		modulePath + "/internal/projectsetup": {
			"internal/commands/setup.go": true,
		},
	}
	seenImports := make(map[string][]string)
	for relative, parsed := range files {
		for _, imported := range parsed.Imports {
			path, err := strconv.Unquote(imported.Path.Value)
			if err != nil {
				t.Fatalf("unquote import in %s: %v", relative, err)
			}
			allowed, guarded := allowedImporters[path]
			if !guarded {
				continue
			}
			seenImports[path] = append(seenImports[path], relative)
			if !allowed[relative] {
				t.Errorf("network-capable package %s imported outside its reviewed adapter/recovery boundary by %s", path, relative)
			}
		}
	}
	for path, importers := range seenImports {
		sort.Strings(importers)
		seenImports[path] = importers
	}
	wantImports := map[string][]string{
		modulePath + "/internal/acquire":      {"internal/commands/acquire.go", "internal/commands/setup.go", "internal/projectsetup/setup.go"},
		modulePath + "/internal/pullflow":     {"internal/commands/pull.go", "internal/doctor/recovery.go"},
		modulePath + "/internal/syncflow":     {"internal/commands/sync.go"},
		modulePath + "/internal/remoteselect": {"internal/pullflow/network.go", "internal/syncflow/push.go"},
		modulePath + "/internal/projectsetup": {"internal/commands/setup.go"},
	}
	for path, required := range wantImports {
		for _, importer := range required {
			if !containsString(seenImports[path], importer) {
				t.Errorf("reviewed boundary import %s -> %s is missing", importer, path)
			}
		}
	}

	assertRecoveryOnlySelectors(t, files["internal/doctor/recovery.go"], "pullflow", map[string]bool{
		"InspectRecovery": true, "RecoveryDisposition": true, "RecoveryInspection": true, "RecoveryResult": true,
		"RecoveryAbsent": true, "RecoveryActive": true, "RecoveryRecoverable": true, "RecoveryInconsistent": true,
	})
}

func interfaceGateRepositoryRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate interface gate source")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func parseProductionFiles(t *testing.T, root string) map[string]*ast.File {
	t.Helper()
	result := make(map[string]*ast.File)
	set := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		parsed, err := parser.ParseFile(set, path, nil, 0)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(filepath.Dir(root), path)
		if err != nil {
			return err
		}
		result[filepath.ToSlash(relative)] = parsed
		return nil
	})
	if err != nil {
		t.Fatalf("parse production source: %v", err)
	}
	return result
}

type networkLiteralUse struct {
	function string
	value    string
}

func findNetworkLiterals(parsed *ast.File, verbs map[string]bool) []networkLiteralUse {
	var result []networkLiteralUse
	inspect := func(node ast.Node, function string) {
		ast.Inspect(node, func(child ast.Node) bool {
			literal, ok := child.(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				return true
			}
			value, err := strconv.Unquote(literal.Value)
			if err == nil && verbs[value] {
				result = append(result, networkLiteralUse{function: function, value: value})
			}
			return true
		})
	}
	for _, declaration := range parsed.Decls {
		if function, ok := declaration.(*ast.FuncDecl); ok {
			inspect(function, function.Name.Name)
			continue
		}
		inspect(declaration, "<declaration>")
	}
	return result
}

func networkLiteralAllowed(relative, function, value string) bool {
	allowed := map[string]map[string]map[string]bool{
		"internal/cli/model.go": {
			"<declaration>": {"clone": true, "pull": true, "push": true},
			"DefaultModel":  {"clone": true, "pull": true, "push": true},
		},
		"internal/remoteselect/select.go": {
			"<declaration>": {"fetch": true, "push": true},
		},
		"internal/acquire/clone.go": {
			"Run":      {"clone": true},
			"runClone": {"clone": true},
		},
		"internal/projectsetup/setup.go": {
			"Run":             {"clone": true},
			"acquireOne":      {"clone": true},
			"planAcquisition": {"clone": true},
		},
		"internal/pullflow/network.go": {
			"acquireTip": {"fetch": true},
			"observe":    {"ls-remote": true},
		},
		"internal/pullflow/pull.go": {
			"Pull": {"pull": true},
		},
		"internal/syncflow/push.go": {
			"Push":          {"push": true},
			"observe":       {"ls-remote": true},
			"pushArguments": {"push": true},
		},
		"internal/syncflow/process.go": {
			"runGitCommand": {"push": true},
		},
	}
	return allowed[relative][function][value]
}

func assertRecoveryOnlySelectors(t *testing.T, parsed *ast.File, alias string, allowed map[string]bool) {
	t.Helper()
	if parsed == nil {
		t.Fatal("doctor recovery source is unavailable")
	}
	ast.Inspect(parsed, func(node ast.Node) bool {
		selector, ok := node.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		identifier, ok := selector.X.(*ast.Ident)
		if !ok || identifier.Name != alias {
			return true
		}
		if !allowed[selector.Sel.Name] {
			t.Errorf("doctor recovery uses pullflow.%s outside the network-silent recovery API", selector.Sel.Name)
		}
		return true
	})
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

// Keep the source scanner honest if its repository-root calculation changes.
func TestInterfaceGateSourceAuditRoot(t *testing.T) {
	root := interfaceGateRepositoryRoot(t)
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("source audit repository root %q: %v", root, err)
	}
}
