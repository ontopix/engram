package cli

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	engramversion "github.com/ontopix/engram/internal/version"
)

type commandGolden struct {
	name  CommandName
	path  []string
	usage string
	valid []string
	store StorePolicy
}

// protocolCommandGoldens is deliberately independent of DefaultModel. It is
// the interface gate which makes a command addition, removal, rename, usage
// change, or store-policy change an explicit protocol review.
var protocolCommandGoldens = []commandGolden{
	{CommandInit, []string{"init"}, "engram init [PATH] [--schema TYPE]... [--dry-run]", []string{"init", "--dry-run"}, StoreForbidden},
	{CommandClone, []string{"clone"}, "engram clone URL [PATH]", []string{"clone", "https://example.test/memory.git", "memory"}, StoreForbidden},
	{CommandAttach, []string{"attach"}, "engram attach STORE [--project PATH] [--memory-file FILE]", []string{"attach", "memory"}, StoreForbidden},
	{CommandDetach, []string{"detach"}, "engram detach STORE [--project PATH] [--memory-file FILE]", []string{"detach", "memory"}, StoreForbidden},
	{CommandSetup, []string{"setup"}, "engram setup [--harness HARNESS] [--project PATH] [--memory-file FILE] [--check-history] [--dry-run]", []string{"setup", "--dry-run"}, StoreForbidden},
	{CommandConfigAttachmentAdd, []string{"config", "attachment", "add"}, "engram config attachment add NAME URL [--project PATH]", []string{"config", "attachment", "add", "project-memory", "git@github.com:ontopix/memory.git"}, StoreForbidden},
	{CommandConfigAttachmentRemove, []string{"config", "attachment", "remove"}, "engram config attachment remove NAME [--project PATH]", []string{"config", "attachment", "remove", "project-memory"}, StoreForbidden},
	{CommandConfigHarness, []string{"config", "harness"}, "engram config harness HARNESS [--project PATH]", []string{"config", "harness", "codex"}, StoreForbidden},
	{CommandConfigShow, []string{"config", "show"}, "engram config show [--project PATH]", []string{"config", "show"}, StoreForbidden},
	{CommandStatus, []string{"status"}, "engram status", []string{"status"}, StoreAllowed},
	{CommandDiff, []string{"diff"}, "engram diff [REV-A [REV-B]] [--staged|--cached] [--stat|--name-only]", []string{"diff", "--name-only"}, StoreAllowed},
	{CommandLog, []string{"log"}, "engram log [-n COUNT] [--oneline]", []string{"log", "-n", "1"}, StoreAllowed},
	{CommandAdd, []string{"add"}, "engram add PATH...\nengram add --all", []string{"add", "--all"}, StoreAllowed},
	{CommandCheck, []string{"check"}, "engram check [PATH]\nengram check --accepted\nengram check --history\nengram check --staged\nengram check --base BASE --candidate CANDIDATE", []string{"check"}, StoreConditional},
	{CommandFmt, []string{"fmt"}, "engram fmt [PATH...] [--check] [--dry-run]", []string{"fmt", "--dry-run"}, StoreAllowed},
	{CommandNew, []string{"new"}, "engram new TYPE PATH --description TEXT [--fields FILE] [--body FILE|-] [--title TEXT] [--dry-run]", []string{"new", "note", "topics/new.md", "--description", "A note.", "--dry-run"}, StoreAllowed},
	{CommandMove, []string{"mv"}, "engram mv FROM TO [--dry-run]", []string{"mv", "old.md", "new.md", "--dry-run"}, StoreAllowed},
	{CommandSchemaInventory, []string{"schema", "inventory"}, "engram schema inventory", []string{"schema", "inventory"}, StoreForbidden},
	{CommandSchemaList, []string{"schema", "list"}, "engram schema list [--at PATH]", []string{"schema", "list"}, StoreAllowed},
	{CommandSchemaShow, []string{"schema", "show"}, "engram schema show TYPE [--at PATH]", []string{"schema", "show", "note"}, StoreAllowed},
	{CommandSchemaCopy, []string{"schema", "copy"}, "engram schema copy TYPE [--to SCOPE] [--dry-run]", []string{"schema", "copy", "note", "--dry-run"}, StoreAllowed},
	{CommandCommit, []string{"commit"}, "engram commit -m MESSAGE [--dry-run]\nengram commit --dry-run", []string{"commit", "--dry-run"}, StoreAllowed},
	{CommandRevert, []string{"revert"}, "engram revert COMMIT [-m MESSAGE] [--dry-run]", []string{"revert", "HEAD", "--dry-run"}, StoreAllowed},
	{CommandHooksList, []string{"hooks", "list"}, "engram hooks list [--state accepted|working|staged]", []string{"hooks", "list"}, StoreAllowed},
	{CommandHooksTrust, []string{"hooks", "trust"}, "engram hooks trust [--state accepted|working|staged]", []string{"hooks", "trust"}, StoreAllowed},
	{CommandHooksRevoke, []string{"hooks", "revoke"}, "engram hooks revoke [HOOK...]", []string{"hooks", "revoke"}, StoreAllowed},
	{CommandDoctor, []string{"doctor"}, "engram doctor [PATH] [--recover] [--format text|json]", []string{"doctor"}, StoreConditional},
	{CommandPull, []string{"pull"}, "engram pull [REMOTE [BRANCH]]\nengram pull --continue\nengram pull --abort", []string{"pull", "--abort"}, StoreAllowed},
	{CommandPush, []string{"push"}, "engram push [REMOTE [BRANCH]]", []string{"push"}, StoreAllowed},
	{CommandVersion, []string{"version"}, "engram version [--format text|json]", []string{"version"}, StoreForbidden},
}

func TestInterfaceGateCanonicalCommandSurface(t *testing.T) {
	model := DefaultModel()
	if len(model.Commands) != len(protocolCommandGoldens) {
		t.Fatalf("command count = %d, want %d", len(model.Commands), len(protocolCommandGoldens))
	}
	for index, golden := range protocolCommandGoldens {
		golden := golden
		t.Run(string(golden.name), func(t *testing.T) {
			actual := model.Commands[index]
			if actual.Name != golden.name || !equalStrings(actual.Path, golden.path) || actual.Usage != golden.usage || actual.Store != golden.store {
				t.Fatalf("command[%d] = {name:%q path:%q usage:%q store:%d}, want {name:%q path:%q usage:%q store:%d}",
					index, actual.Name, actual.Path, actual.Usage, actual.Store, golden.name, golden.path, golden.usage, golden.store)
			}
			invocation, failure := Parse(model, golden.valid)
			if failure != nil {
				t.Fatalf("representative invocation %q: %v", golden.valid, failure.Error)
			}
			if invocation.Command.Name != golden.name {
				t.Fatalf("representative invocation selected %q, want %q", invocation.Command.Name, golden.name)
			}
		})
	}
}

func TestInterfaceGateCommandHelpExactBeforeAndAfter(t *testing.T) {
	app := protocolGateApp()
	for _, golden := range protocolCommandGoldens {
		golden := golden
		want := "Usage:\n  " + strings.ReplaceAll(golden.usage, "\n", "\n  ") + "\n"
		for _, test := range []struct {
			name string
			args []string
		}{
			{"before", append([]string{"--help", "--no-color"}, golden.path...)},
			{"after", append(append([]string(nil), golden.path...), "--no-color", "-h")},
		} {
			t.Run(string(golden.name)+"/"+test.name, func(t *testing.T) {
				status, stdout, stderr := runGateApp(app, test.args)
				assertGateIO(t, status, stdout, stderr, 0, want, "")
			})
		}
	}
}

func TestInterfaceGateRootAndGroupHelpExact(t *testing.T) {
	app := protocolGateApp()
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"root", []string{"--quiet", "--no-color", "--help"}, rootHelpGolden},
		{"schema before", []string{"--help", "schema"}, schemaHelpGolden},
		{"schema after", []string{"schema", "--help"}, schemaHelpGolden},
		{"hooks before", []string{"--help", "hooks"}, hooksHelpGolden},
		{"hooks after", []string{"hooks", "--help"}, hooksHelpGolden},
		{"config before", []string{"--help", "config"}, configHelpGolden},
		{"config after", []string{"config", "--help"}, configHelpGolden},
		{"config attachment", []string{"config", "attachment", "--help"}, configAttachmentHelpGolden},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			status, stdout, stderr := runGateApp(app, test.args)
			assertGateIO(t, status, stdout, stderr, 0, test.want, "")
		})
	}
}

const rootHelpGolden = "Usage:\n" +
	"  engram [GLOBAL-OPTIONS] COMMAND [ARGS]\n\n" +
	"These are the engram commands grouped by workflow:\n\n" +
	"Create, obtain, and connect stores:\n" +
	"  init     Create or initialize a managed store\n" +
	"  clone    Clone a managed store into a new directory\n" +
	"  attach   Attach a store through a project memory manifest\n" +
	"  detach   Detach a store from a project memory manifest\n" +
	"  setup    Converge project memories and agent-harness integration\n" +
	"  config   Inspect and edit declarative project setup\n\n" +
	"Inspect state:\n" +
	"  status   Show working draft and initial candidate status\n" +
	"  diff     Show changes between store states\n" +
	"  log      Show accepted store history\n" +
	"  check    Validate a snapshot, candidate, or transition\n\n" +
	"Work on the current draft:\n" +
	"  add      Add working draft changes to the initial candidate\n" +
	"  fmt      Regenerate catalog marker regions\n" +
	"  new      Create one typed record and update its catalog\n" +
	"  mv       Move one record and preserve link meaning\n" +
	"  schema   Inspect and copy schemas\n\n" +
	"Accept and undo changes:\n" +
	"  commit   Prepare, validate, and accept the initial candidate\n" +
	"  revert   Revert an accepted commit through a new transaction\n\n" +
	"Manage hooks and trust:\n" +
	"  hooks    Inspect and manage preparation-hook trust\n\n" +
	"Synchronize repositories:\n" +
	"  pull     Synchronize accepted history from a remote\n" +
	"  push     Publish accepted history to a remote\n\n" +
	"Diagnose and inspect runtime:\n" +
	"  doctor   Diagnose integration and recovery state\n" +
	"  version  Show CLI and specification compatibility\n\n" +
	"Global options:\n" +
	"  -s, --store PATH       Select a store root\n" +
	"      --format FORMAT    Set output format: text or json\n" +
	"      --no-color         Disable ANSI styling\n" +
	"  -q, --quiet            Suppress ordinary successful text\n" +
	"  -h, --help             Show help\n" +
	"  -V, --version          Show CLI version\n\n" +
	"Run 'engram COMMAND --help' for more information on a command.\n"

const schemaHelpGolden = "Usage:\n  engram schema COMMAND [ARGS]\n\n" +
	"Commands:\n" +
	"  inventory  List schemas bundled with the CLI\n" +
	"  list       List schemas visible at a content path\n" +
	"  show       Show one resolved local schema\n" +
	"  copy       Copy one bundled schema into a store\n\n" +
	"Run 'engram schema COMMAND --help' for more information on a command.\n"

const hooksHelpGolden = "Usage:\n  engram hooks COMMAND [ARGS]\n\n" +
	"Commands:\n" +
	"  list    List hooks and their local trust state\n" +
	"  trust   Trust one complete selected hook set\n" +
	"  revoke  Revoke hook trust grants\n\n" +
	"Run 'engram hooks COMMAND --help' for more information on a command.\n"

const configHelpGolden = "Usage:\n  engram config COMMAND [ARGS]\n\n" +
	"Commands:\n" +
	"  attachment  Manage declared memory repositories\n" +
	"  harness     Set the default project harness\n" +
	"  show        Show declarative project setup\n\n" +
	"Run 'engram config COMMAND --help' for more information on a command.\n"

const configAttachmentHelpGolden = "Usage:\n  engram config attachment COMMAND [ARGS]\n\n" +
	"Commands:\n" +
	"  add     Declare a project memory repository\n" +
	"  remove  Remove a declared memory repository\n\n" +
	"Run 'engram config attachment COMMAND --help' for more information on a command.\n"

func TestInterfaceGateGlobalOptionsBeforeAndAfterEveryCommand(t *testing.T) {
	model := DefaultModel()
	for _, golden := range protocolCommandGoldens {
		golden := golden
		t.Run(string(golden.name), func(t *testing.T) {
			variants := []struct {
				name string
				args []string
			}{
				{"before", append([]string{"--format", "json", "--quiet", "--no-color"}, golden.valid...)},
				{"after", append(append([]string(nil), golden.valid...), "--format", "json", "--quiet", "--no-color")},
				{"text before", append([]string{"--format", "text", "-q"}, golden.valid...)},
				{"text after", append(append([]string(nil), golden.valid...), "--format", "text", "-q")},
			}
			if golden.store != StoreForbidden {
				variants = append(variants,
					struct {
						name string
						args []string
					}{"store before", append([]string{"--store", "memory"}, golden.valid...)},
					struct {
						name string
						args []string
					}{"store after", append(append([]string(nil), golden.valid...), "-s", "memory")},
				)
			}
			for _, variant := range variants {
				invocation, failure := Parse(model, variant.args)
				if failure != nil {
					t.Errorf("%s %q: %v", variant.name, variant.args, failure.Error)
					continue
				}
				if strings.HasPrefix(variant.name, "store") {
					if !invocation.Globals.StoreSet || invocation.Globals.Store != "memory" {
						t.Errorf("%s globals = %#v", variant.name, invocation.Globals)
					}
					continue
				}
				wantFormat := FormatJSON
				wantNoColor := true
				if strings.HasPrefix(variant.name, "text") {
					wantFormat = FormatText
					wantNoColor = false
				}
				if invocation.Globals.Format != wantFormat || !invocation.Globals.Quiet || invocation.Globals.NoColor != wantNoColor {
					t.Errorf("%s globals = %#v", variant.name, invocation.Globals)
				}
			}

			if golden.store == StoreForbidden {
				for _, args := range [][]string{
					append([]string{"--store", "memory", "--format", "json"}, golden.valid...),
					append(append([]string(nil), golden.valid...), "--store", "memory", "--format", "json"),
				} {
					_, failure := Parse(model, args)
					if failure == nil || failure.Error.Kind != ErrorUsage || failure.Error.Message != "--store is not accepted by "+string(golden.name) {
						t.Errorf("forbidden store %q failure = %#v", args, failure)
					}
				}
			}
		})
	}
}

func TestInterfaceGateExactTextJSONQuietAndNoColorForEveryCommand(t *testing.T) {
	app := protocolGateApp()
	for _, golden := range protocolCommandGoldens {
		golden := golden
		t.Run(string(golden.name), func(t *testing.T) {
			wantText := "{\n  \"token\": \"" + string(golden.name) + "\"\n}\n"
			wantResult := "{\"token\":\"" + string(golden.name) + "\"}"
			if golden.name == CommandVersion {
				wantText = "engram 1.2.3-test\n"
				wantResult = `{"cli_version":"1.2.3-test","core_versions":[],"annex_versions":[],"git":{"version":null,"supported":false},"build":{"go":"test-go","os":"test-os","arch":"test-arch","revision":null}}`
			}
			wantJSON := fmt.Sprintf("{\"version\":1,\"command\":%q,\"outcome\":\"ok\",\"exit_status\":0,\"result\":%s,\"error\":null}\n", golden.name, wantResult)

			status, stdout, stderr := runGateApp(app, append(append([]string(nil), golden.valid...), "--no-color"))
			assertGateIO(t, status, stdout, stderr, 0, wantText, "")
			if strings.Contains(stdout, "\x1b") {
				t.Fatalf("--no-color output contains ANSI escape: %q", stdout)
			}
			for _, textArgs := range [][]string{
				append([]string{"--format", "text", "--no-color"}, golden.valid...),
				append(append([]string(nil), golden.valid...), "--format", "text", "--no-color"),
			} {
				status, stdout, stderr = runGateApp(app, textArgs)
				assertGateIO(t, status, stdout, stderr, 0, wantText, "")
			}

			quietArgs := append([]string{"--quiet", "--no-color"}, golden.valid...)
			status, stdout, stderr = runGateApp(app, quietArgs)
			quietWant := ""
			if golden.name == CommandVersion {
				quietWant = wantText
			}
			assertGateIO(t, status, stdout, stderr, 0, quietWant, "")

			for _, jsonArgs := range [][]string{
				append([]string{"--format", "json", "--quiet", "--no-color"}, golden.valid...),
				append(append([]string(nil), golden.valid...), "--format", "json", "--quiet", "--no-color"),
			} {
				status, stdout, stderr = runGateApp(app, jsonArgs)
				assertGateIO(t, status, stdout, stderr, 0, wantJSON, "")
				if strings.Contains(stdout, "\x1b") {
					t.Fatalf("JSON contains ANSI escape: %q", stdout)
				}
			}
			if golden.store != StoreForbidden {
				for _, storedArgs := range [][]string{
					append([]string{"--store", "memory", "--format", "json"}, golden.valid...),
					append(append([]string(nil), golden.valid...), "-s", "memory", "--format", "json"),
				} {
					status, stdout, stderr = runGateApp(app, storedArgs)
					assertGateIO(t, status, stdout, stderr, 0, wantJSON, "")
				}
			}
		})
	}
}

func TestInterfaceGateExactEnvelopesAndExitStatuses(t *testing.T) {
	tests := []struct {
		outcome    Outcome
		exit       int
		result     Result
		wantJSON   string
		wantText   string
		wantError  string
		quietText  string
		quietError string
	}{
		{OutcomeOK, 0, Result{Outcome: OutcomeOK, Value: gateToken{Token: "ok"}},
			"{\"version\":1,\"command\":\"status\",\"outcome\":\"ok\",\"exit_status\":0,\"result\":{\"token\":\"ok\"},\"error\":null}\n",
			"{\n  \"token\": \"ok\"\n}\n", "", "", ""},
		{OutcomeIssues, 1, Result{Outcome: OutcomeIssues, Value: gateToken{Token: "issues"}},
			"{\"version\":1,\"command\":\"status\",\"outcome\":\"issues\",\"exit_status\":1,\"result\":{\"token\":\"issues\"},\"error\":null}\n",
			"{\n  \"token\": \"issues\"\n}\n", "", "{\n  \"token\": \"issues\"\n}\n", ""},
		{OutcomeError, 2, Result{Outcome: OutcomeError, Error: &ProtocolError{Kind: ErrorRepository, Message: "store is unavailable"}},
			"{\"version\":1,\"command\":\"status\",\"outcome\":\"error\",\"exit_status\":2,\"result\":{},\"error\":{\"kind\":\"repository\",\"message\":\"store is unavailable\"}}\n",
			"", "engram: store is unavailable\n", "", "engram: store is unavailable\n"},
		{OutcomeIndeterminate, 3, Result{Outcome: OutcomeIndeterminate, Value: gateToken{Token: "indeterminate"}},
			"{\"version\":1,\"command\":\"status\",\"outcome\":\"indeterminate\",\"exit_status\":3,\"result\":{\"token\":\"indeterminate\"},\"error\":null}\n",
			"{\n  \"token\": \"indeterminate\"\n}\n", "", "{\n  \"token\": \"indeterminate\"\n}\n", ""},
	}
	for _, test := range tests {
		t.Run(string(test.outcome), func(t *testing.T) {
			app := NewApp()
			app.Handlers[CommandStatus] = HandlerFunc(func(context.Context, *Invocation) Result { return test.result })

			status, stdout, stderr := runGateApp(app, []string{"status", "--format", "json", "--quiet", "--no-color"})
			assertGateIO(t, status, stdout, stderr, test.exit, test.wantJSON, "")

			status, stdout, stderr = runGateApp(app, []string{"status", "--no-color"})
			assertGateIO(t, status, stdout, stderr, test.exit, test.wantText, test.wantError)

			status, stdout, stderr = runGateApp(app, []string{"--quiet", "status"})
			assertGateIO(t, status, stdout, stderr, test.exit, test.quietText, test.quietError)
		})
	}
}

func TestInterfaceGateRepresentativeErrorsHaveExactBytes(t *testing.T) {
	app := protocolGateApp()
	tests := []struct {
		name       string
		args       []string
		wantStdout string
		wantStderr string
	}{
		{"missing command text", nil, "", rootHelpGolden},
		{"missing group subcommand text", []string{"schema"}, "", "engram: schema requires a subcommand\n\n" + schemaHelpGolden},
		{"missing command arguments text", []string{"clone"}, "", "engram: clone requires URL\n\nUsage:\n  engram clone URL [PATH]\n"},
		{"missing command arguments JSON", []string{"clone", "--format", "json"}, "{\"version\":1,\"command\":\"clone\",\"outcome\":\"error\",\"exit_status\":2,\"result\":{},\"error\":{\"kind\":\"usage\",\"message\":\"clone requires URL\"}}\n", ""},
		{"unknown command text suggestion", []string{"statsu"}, "", "engram: unknown command \"statsu\"\n\nDid you mean 'status'?\n"},
		{"unknown command text without suggestion", []string{"wat"}, "", "engram: unknown command \"wat\"\n"},
		{"unknown group subcommand text", []string{"schema", "shwo"}, "", "engram: unknown schema subcommand \"shwo\"\n\nDid you mean 'show'?\n\n" + schemaHelpGolden},
		{"unknown nested subcommand text", []string{"config", "attachment", "ad"}, "", "engram: unknown config attachment subcommand \"ad\"\n\nDid you mean 'add'?\n\n" + configAttachmentHelpGolden},
		{"unknown command JSON", []string{"--format", "json", "wat"}, "{\"version\":1,\"command\":null,\"outcome\":\"error\",\"exit_status\":2,\"result\":{},\"error\":{\"kind\":\"usage\",\"message\":\"unknown command \\\"wat\\\"\"}}\n", ""},
		{"known command JSON", []string{"status", "extra", "--format", "json"}, "{\"version\":1,\"command\":\"status\",\"outcome\":\"error\",\"exit_status\":2,\"result\":{},\"error\":{\"kind\":\"usage\",\"message\":\"too many arguments for status\"}}\n", ""},
		{"JSON help", []string{"status", "--help", "--format", "json"}, "{\"version\":1,\"command\":\"status\",\"outcome\":\"error\",\"exit_status\":2,\"result\":{},\"error\":{\"kind\":\"usage\",\"message\":\"JSON help is not part of protocol v1\"}}\n", ""},
		{"forbidden store JSON", []string{"version", "--store", "memory", "--format", "json"}, "{\"version\":1,\"command\":\"version\",\"outcome\":\"error\",\"exit_status\":2,\"result\":{},\"error\":{\"kind\":\"usage\",\"message\":\"--store is not accepted by version\"}}\n", ""},
		{"duplicate global text", []string{"--quiet", "status", "--quiet"}, "", "engram: global option --quiet cannot be repeated\n\nUsage:\n  engram status\n"},
		{"unknown option text", []string{"status", "--wat"}, "", "engram: unknown option \"--wat\" for status\n\nUsage:\n  engram status\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			status, stdout, stderr := runGateApp(app, test.args)
			assertGateIO(t, status, stdout, stderr, 2, test.wantStdout, test.wantStderr)
		})
	}
}

func TestInterfaceGateClosedErrorKindsHaveExactJSON(t *testing.T) {
	kinds := []ErrorKind{
		ErrorUsage,
		ErrorCancelled,
		ErrorInternal,
		ErrorCapability,
		ErrorTrust,
		ErrorHook,
		ErrorNetwork,
		ErrorConflict,
		ErrorConcurrency,
		ErrorIntegration,
		ErrorRepository,
		ErrorIO,
		ErrorOperational,
	}
	wantNames := []string{
		"usage", "cancelled", "internal", "capability", "trust", "hook", "network",
		"conflict", "concurrency", "integration", "repository", "io", "operational",
	}
	if len(kinds) != len(wantNames) {
		t.Fatal("error-kind golden is internally inconsistent")
	}
	for index, kind := range kinds {
		kind, wantName := kind, wantNames[index]
		t.Run(wantName, func(t *testing.T) {
			if string(kind) != wantName {
				t.Fatalf("error kind = %q, want %q", kind, wantName)
			}
			message := "representative " + wantName
			app := NewApp()
			app.Handlers[CommandStatus] = HandlerFunc(func(context.Context, *Invocation) Result {
				return Result{Outcome: OutcomeError, Error: &ProtocolError{Kind: kind, Message: message}}
			})
			want := fmt.Sprintf("{\"version\":1,\"command\":\"status\",\"outcome\":\"error\",\"exit_status\":2,\"result\":{},\"error\":{\"kind\":%q,\"message\":%q}}\n", wantName, message)
			status, stdout, stderr := runGateApp(app, []string{"--format", "json", "status"})
			assertGateIO(t, status, stdout, stderr, 2, want, "")
		})
	}
}

func TestInterfaceGateVersionAliasesIgnoreQuiet(t *testing.T) {
	app := protocolGateApp()
	for _, args := range [][]string{
		{"--quiet", "--no-color", "-V"},
		{"--version", "--quiet", "--no-color"},
	} {
		status, stdout, stderr := runGateApp(app, args)
		assertGateIO(t, status, stdout, stderr, 0, "engram 1.2.3-test\n", "")
	}
}

type gateToken struct {
	Token string `json:"token"`
}

func protocolGateApp() *App {
	app := NewApp()
	for _, golden := range protocolCommandGoldens {
		name := golden.name
		app.Handlers[name] = HandlerFunc(func(context.Context, *Invocation) Result {
			if name == CommandVersion {
				return Result{Outcome: OutcomeOK, Value: engramversion.Info{
					CLIVersion:    "1.2.3-test",
					CoreVersions:  []engramversion.Specification{},
					AnnexVersions: []engramversion.Specification{},
					Git:           engramversion.GitCapability{},
					Build:         engramversion.Build{Go: "test-go", OS: "test-os", Arch: "test-arch"},
				}}
			}
			return Result{Outcome: OutcomeOK, Value: gateToken{Token: string(name)}}
		})
	}
	return app
}

func runGateApp(app *App, args []string) (int, string, string) {
	var stdout, stderr bytes.Buffer
	status := app.Run(context.Background(), args, &stdout, &stderr)
	return status, stdout.String(), stderr.String()
}

func assertGateIO(t *testing.T, status int, stdout, stderr string, wantStatus int, wantStdout, wantStderr string) {
	t.Helper()
	if status != wantStatus || stdout != wantStdout || stderr != wantStderr {
		t.Fatalf("status/stdout/stderr = %d/%q/%q, want %d/%q/%q", status, stdout, stderr, wantStatus, wantStdout, wantStderr)
	}
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
