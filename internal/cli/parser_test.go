package cli

import (
	"strings"
	"testing"
)

func TestParseCompleteCommandSurface(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		args []string
		want CommandName
	}{
		{"init", []string{"init", "memory", "--schema", "person", "--schema", "project", "--dry-run"}, CommandInit},
		{"clone", []string{"clone", "https://example.test/memory.git", "memory"}, CommandClone},
		{"attach", []string{"attach", "/memory", "--project", "/project", "--entrypoint", "AGENTS.md"}, CommandAttach},
		{"detach", []string{"detach", "/memory"}, CommandDetach},
		{"status", []string{"status"}, CommandStatus},
		{"diff", []string{"diff", "--cached", "--stat"}, CommandDiff},
		{"log", []string{"log", "-n", "10", "--oneline"}, CommandLog},
		{"add paths", []string{"add", "one.md", "topics"}, CommandAdd},
		{"add all", []string{"add", "--all"}, CommandAdd},
		{"check snapshot", []string{"check", "snapshot"}, CommandCheck},
		{"check accepted", []string{"check", "--accepted"}, CommandCheck},
		{"check staged", []string{"check", "--staged"}, CommandCheck},
		{"check pair", []string{"check", "--base", "base", "--candidate", "candidate"}, CommandCheck},
		{"fmt", []string{"fmt", "topics", "--check", "--dry-run"}, CommandFmt},
		{"new", []string{"new", "note", "topics/new.md", "--description", "A note.", "--title", "New", "--dry-run"}, CommandNew},
		{"move", []string{"mv", "old.md", "new.md", "--dry-run"}, CommandMove},
		{"schema inventory", []string{"schema", "inventory"}, CommandSchemaInventory},
		{"schema list", []string{"schema", "list", "--at", "topics"}, CommandSchemaList},
		{"schema show", []string{"schema", "show", "note", "--at", "topics"}, CommandSchemaShow},
		{"schema copy", []string{"schema", "copy", "person", "--to", "topics", "--dry-run"}, CommandSchemaCopy},
		{"commit", []string{"commit", "-m", "Update memory", "--dry-run"}, CommandCommit},
		{"commit dry-run", []string{"commit", "--dry-run"}, CommandCommit},
		{"revert", []string{"revert", "HEAD", "-m", "Undo"}, CommandRevert},
		{"hooks list", []string{"hooks", "list", "--state", "working"}, CommandHooksList},
		{"hooks trust", []string{"hooks", "trust"}, CommandHooksTrust},
		{"hooks revoke", []string{"hooks", "revoke", "20-catalog.py"}, CommandHooksRevoke},
		{"doctor", []string{"doctor", "--recover"}, CommandDoctor},
		{"pull", []string{"pull", "origin", "main"}, CommandPull},
		{"pull continue", []string{"pull", "--continue"}, CommandPull},
		{"pull abort", []string{"pull", "--abort"}, CommandPull},
		{"push", []string{"push", "origin", "main"}, CommandPush},
		{"version", []string{"version", "--format", "json"}, CommandVersion},
		{"version alias", []string{"-V"}, CommandVersion},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			invocation, failure := Parse(DefaultModel(), test.args)
			if failure != nil {
				t.Fatalf("Parse() failure: %v", failure.Error)
			}
			if invocation.Command.Name != test.want {
				t.Fatalf("command = %q, want %q", invocation.Command.Name, test.want)
			}
		})
	}
}

func TestParseGlobalOptionsBeforeAndAfterCommand(t *testing.T) {
	t.Parallel()
	invocation, failure := Parse(DefaultModel(), []string{
		"--format", "json", "schema", "--store", "/memory", "list", "--at", "topics", "--quiet",
	})
	if failure != nil {
		t.Fatalf("Parse() failure: %v", failure.Error)
	}
	if invocation.Command.Name != CommandSchemaList {
		t.Fatalf("command = %q", invocation.Command.Name)
	}
	if invocation.Globals.Format != FormatJSON || invocation.Globals.Store != "/memory" || !invocation.Globals.Quiet {
		t.Fatalf("globals = %#v", invocation.Globals)
	}
	if at, ok := invocation.Options.One("at"); !ok || at != "topics" {
		t.Fatalf("--at = %q, %v", at, ok)
	}
}

func TestParseRejectsInvalidGrammar(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"missing command", nil, "command is required"},
		{"unknown command", []string{"wat"}, "unknown command"},
		{"missing subcommand", []string{"schema"}, "requires a subcommand"},
		{"unknown option", []string{"status", "--wat"}, "unknown option"},
		{"duplicate global", []string{"--quiet", "status", "--quiet"}, "cannot be repeated"},
		{"bad format", []string{"--format", "yaml", "status"}, "invalid FORMAT"},
		{"store forbidden", []string{"--store", "/memory", "version"}, "--store is not accepted"},
		{"check store path", []string{"--store", "/memory", "check", "snapshot"}, "explicit-path"},
		{"doctor store path", []string{"doctor", "target", "--store", "/memory"}, "cannot be combined"},
		{"diff staged revision", []string{"diff", "HEAD", "--staged"}, "cannot be combined"},
		{"diff presentation", []string{"diff", "--stat", "--name-only"}, "mutually exclusive"},
		{"bad count", []string{"log", "-n", "0"}, "COUNT must"},
		{"add empty", []string{"add"}, "requires one or more"},
		{"add all paths", []string{"add", "--all", "note.md"}, "cannot be combined"},
		{"check mixed", []string{"check", "--accepted", "--staged"}, "mutually exclusive"},
		{"check half pair", []string{"check", "--base", "base"}, "provided together"},
		{"new description", []string{"new", "note", "new.md"}, "requires --description"},
		{"new body title", []string{"new", "note", "new.md", "--description", "A note.", "--body", "body.md", "--title", "Title"}, "mutually exclusive"},
		{"commit message", []string{"commit"}, "requires -m"},
		{"commit newline", []string{"commit", "-m", "bad\n"}, "MESSAGE must"},
		{"hooks state", []string{"hooks", "list", "--state", "candidate"}, "invalid STATE"},
		{"pull controls", []string{"pull", "--continue", "--abort"}, "mutually exclusive"},
		{"pull control args", []string{"pull", "origin", "--continue"}, "cannot be combined"},
		{"version and command", []string{"--version", "version"}, "cannot be combined"},
		{"json help", []string{"status", "--format", "json", "--help"}, "JSON help"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, failure := Parse(DefaultModel(), test.args)
			if failure == nil {
				t.Fatal("Parse() unexpectedly succeeded")
			}
			if !strings.Contains(failure.Error.Message, test.want) {
				t.Fatalf("error = %q, want substring %q", failure.Error.Message, test.want)
			}
		})
	}
}

func TestParseDelimiterMakesFollowingOptionsPositional(t *testing.T) {
	t.Parallel()
	invocation, failure := Parse(DefaultModel(), []string{"add", "--", "--all"})
	if failure != nil {
		t.Fatalf("Parse() failure: %v", failure.Error)
	}
	if invocation.Options.Has("all") || len(invocation.Arguments) != 1 || invocation.Arguments[0] != "--all" {
		t.Fatalf("invocation = %#v", invocation)
	}
}

func TestModelHasUniqueCanonicalCommandsAndAliases(t *testing.T) {
	t.Parallel()
	model := DefaultModel()
	commands := make(map[CommandName]bool)
	paths := make(map[string]bool)
	for _, command := range model.Commands {
		if commands[command.Name] {
			t.Fatalf("duplicate command name %q", command.Name)
		}
		commands[command.Name] = true
		path := strings.Join(command.Path, " ")
		if paths[path] {
			t.Fatalf("duplicate command path %q", path)
		}
		paths[path] = true
		aliases := make(map[string]bool)
		for _, option := range command.Options {
			for _, alias := range option.Aliases {
				if aliases[alias] {
					t.Fatalf("duplicate option alias %q in %s", alias, command.Name)
				}
				aliases[alias] = true
			}
		}
	}
	if len(commands) != 25 {
		t.Fatalf("command count = %d, want 25", len(commands))
	}
}
