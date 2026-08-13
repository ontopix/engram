package cli

import "strings"

// CommandName is the stable dot-separated command name used by protocol v1.
type CommandName string

const (
	CommandInit                   CommandName = "init"
	CommandClone                  CommandName = "clone"
	CommandAttach                 CommandName = "attach"
	CommandDetach                 CommandName = "detach"
	CommandSetup                  CommandName = "setup"
	CommandConfigAttachmentAdd    CommandName = "config.attachment.add"
	CommandConfigAttachmentRemove CommandName = "config.attachment.remove"
	CommandConfigHarness          CommandName = "config.harness"
	CommandConfigShow             CommandName = "config.show"
	CommandStatus                 CommandName = "status"
	CommandDiff                   CommandName = "diff"
	CommandLog                    CommandName = "log"
	CommandAdd                    CommandName = "add"
	CommandCheck                  CommandName = "check"
	CommandFmt                    CommandName = "fmt"
	CommandNew                    CommandName = "new"
	CommandMove                   CommandName = "mv"
	CommandSchemaInventory        CommandName = "schema.inventory"
	CommandSchemaList             CommandName = "schema.list"
	CommandSchemaShow             CommandName = "schema.show"
	CommandSchemaCopy             CommandName = "schema.copy"
	CommandCommit                 CommandName = "commit"
	CommandRevert                 CommandName = "revert"
	CommandHooksList              CommandName = "hooks.list"
	CommandHooksTrust             CommandName = "hooks.trust"
	CommandHooksRevoke            CommandName = "hooks.revoke"
	CommandDoctor                 CommandName = "doctor"
	CommandPull                   CommandName = "pull"
	CommandPush                   CommandName = "push"
	CommandVersion                CommandName = "version"
)

type StorePolicy uint8

const (
	StoreAllowed StorePolicy = iota
	StoreForbidden
	StoreConditional
)

type OptionSpec struct {
	Name       string
	Aliases    []string
	ValueName  string
	Repeatable bool
	Allowed    []string
}

func (o OptionSpec) TakesValue() bool { return o.ValueName != "" }

func (o OptionSpec) accepts(value string) bool {
	if len(o.Allowed) == 0 {
		return true
	}
	for _, allowed := range o.Allowed {
		if value == allowed {
			return true
		}
	}
	return false
}

type PositionalSpec struct {
	Names []string
	Min   int
	Max   int // -1 means unbounded.
}

type CommandSpec struct {
	Name        CommandName
	Path        []string
	Usage       string
	Summary     string
	Positionals PositionalSpec
	Options     []OptionSpec
	Store       StorePolicy
	Validate    func(*Invocation) *ProtocolError
}

type CommandGroupSpec struct {
	Name    string
	Summary string
}

type HelpCategorySpec struct {
	Title    string
	Commands []string
}

type Model struct {
	Globals        []OptionSpec
	Commands       []CommandSpec
	CommandGroups  []CommandGroupSpec
	HelpCategories []HelpCategorySpec
}

func flag(name string, aliases ...string) OptionSpec {
	return OptionSpec{Name: name, Aliases: aliases}
}

func value(name, valueName string, aliases ...string) OptionSpec {
	return OptionSpec{Name: name, Aliases: aliases, ValueName: valueName}
}

func repeatedValue(name, valueName string, aliases ...string) OptionSpec {
	return OptionSpec{Name: name, Aliases: aliases, ValueName: valueName, Repeatable: true}
}

func enumValue(name, valueName string, allowed []string, aliases ...string) OptionSpec {
	return OptionSpec{Name: name, Aliases: aliases, ValueName: valueName, Allowed: allowed}
}

func positionals(minimum, maximum int, names ...string) PositionalSpec {
	return PositionalSpec{Names: names, Min: minimum, Max: maximum}
}

// DefaultModel is the single source of truth for the v1 command grammar.
func DefaultModel() *Model {
	dryRun := flag("dry-run", "--dry-run")
	message := value("message", "MESSAGE", "-m")
	state := enumValue("state", "STATE", []string{"accepted", "working", "staged"}, "--state")

	return &Model{
		CommandGroups: []CommandGroupSpec{
			{Name: "config", Summary: "Inspect and edit declarative project setup"},
			{Name: "schema", Summary: "Inspect and copy schemas"},
			{Name: "hooks", Summary: "Inspect and manage preparation-hook trust"},
		},
		HelpCategories: []HelpCategorySpec{
			{Title: "Create, obtain, and connect stores", Commands: []string{"init", "clone", "attach", "detach", "setup", "config"}},
			{Title: "Inspect state", Commands: []string{"status", "diff", "log", "check"}},
			{Title: "Work on the current draft", Commands: []string{"add", "fmt", "new", "mv", "schema"}},
			{Title: "Accept and undo changes", Commands: []string{"commit", "revert"}},
			{Title: "Manage hooks and trust", Commands: []string{"hooks"}},
			{Title: "Synchronize repositories", Commands: []string{"pull", "push"}},
			{Title: "Diagnose and inspect runtime", Commands: []string{"doctor", "version"}},
		},
		Globals: []OptionSpec{
			value("store", "PATH", "-s", "--store"),
			enumValue("format", "FORMAT", []string{"text", "json"}, "--format"),
			flag("no-color", "--no-color"),
			flag("quiet", "-q", "--quiet"),
			flag("help", "-h", "--help"),
			flag("version", "-V", "--version"),
		},
		Commands: []CommandSpec{
			{Name: CommandInit, Path: []string{"init"}, Usage: "engram init [PATH] [--schema TYPE]... [--dry-run]", Summary: "Create or initialize a managed store", Positionals: positionals(0, 1, "PATH"), Options: []OptionSpec{repeatedValue("schema", "TYPE", "--schema"), dryRun}, Store: StoreForbidden},
			{Name: CommandClone, Path: []string{"clone"}, Usage: "engram clone URL [PATH]", Summary: "Clone a managed store into a new directory", Positionals: positionals(1, 2, "URL", "PATH"), Store: StoreForbidden},
			{Name: CommandAttach, Path: []string{"attach"}, Usage: "engram attach STORE [--project PATH] [--memory-file FILE]", Summary: "Attach a store through a project memory manifest", Positionals: positionals(1, 1, "STORE"), Options: []OptionSpec{value("project", "PATH", "--project"), value("memory-file", "FILE", "--memory-file")}, Store: StoreForbidden},
			{Name: CommandDetach, Path: []string{"detach"}, Usage: "engram detach STORE [--project PATH] [--memory-file FILE]", Summary: "Detach a store from a project memory manifest", Positionals: positionals(1, 1, "STORE"), Options: []OptionSpec{value("project", "PATH", "--project"), value("memory-file", "FILE", "--memory-file")}, Store: StoreForbidden},
			{Name: CommandSetup, Path: []string{"setup"}, Usage: "engram setup [--harness HARNESS] [--project PATH] [--memory-file FILE] [--dry-run]", Summary: "Converge project memories and agent-harness integration", Positionals: positionals(0, 0), Options: []OptionSpec{enumValue("harness", "HARNESS", []string{"claude-code", "codex"}, "--harness"), value("project", "PATH", "--project"), value("memory-file", "FILE", "--memory-file"), dryRun}, Store: StoreForbidden},
			{Name: CommandConfigAttachmentAdd, Path: []string{"config", "attachment", "add"}, Usage: "engram config attachment add NAME URL [--project PATH]", Summary: "Declare a project memory repository", Positionals: positionals(2, 2, "NAME", "URL"), Options: []OptionSpec{value("project", "PATH", "--project")}, Store: StoreForbidden},
			{Name: CommandConfigAttachmentRemove, Path: []string{"config", "attachment", "remove"}, Usage: "engram config attachment remove NAME [--project PATH]", Summary: "Remove a declared memory repository", Positionals: positionals(1, 1, "NAME"), Options: []OptionSpec{value("project", "PATH", "--project")}, Store: StoreForbidden},
			{Name: CommandConfigHarness, Path: []string{"config", "harness"}, Usage: "engram config harness HARNESS [--project PATH]", Summary: "Set the default project harness", Positionals: positionals(1, 1, "HARNESS"), Options: []OptionSpec{value("project", "PATH", "--project")}, Store: StoreForbidden},
			{Name: CommandConfigShow, Path: []string{"config", "show"}, Usage: "engram config show [--project PATH]", Summary: "Show declarative project setup", Positionals: positionals(0, 0), Options: []OptionSpec{value("project", "PATH", "--project")}, Store: StoreForbidden},
			{Name: CommandStatus, Path: []string{"status"}, Usage: "engram status", Summary: "Show working draft and initial candidate status", Positionals: positionals(0, 0), Store: StoreAllowed},
			{Name: CommandDiff, Path: []string{"diff"}, Usage: "engram diff [REV-A [REV-B]] [--staged|--cached] [--stat|--name-only]", Summary: "Show changes between store states", Positionals: positionals(0, 2, "REV-A", "REV-B"), Options: []OptionSpec{flag("staged", "--staged", "--cached"), flag("stat", "--stat"), flag("name-only", "--name-only")}, Store: StoreAllowed, Validate: validateDiff},
			{Name: CommandLog, Path: []string{"log"}, Usage: "engram log [-n COUNT] [--oneline]", Summary: "Show accepted store history", Positionals: positionals(0, 0), Options: []OptionSpec{value("count", "COUNT", "-n"), flag("oneline", "--oneline")}, Store: StoreAllowed, Validate: validateLog},
			{Name: CommandAdd, Path: []string{"add"}, Usage: "engram add PATH...\nengram add --all", Summary: "Add working draft changes to the initial candidate", Positionals: positionals(0, -1, "PATH"), Options: []OptionSpec{flag("all", "--all")}, Store: StoreAllowed, Validate: validateAdd},
			{Name: CommandCheck, Path: []string{"check"}, Usage: "engram check [PATH]\nengram check --accepted\nengram check --staged\nengram check --base BASE --candidate CANDIDATE", Summary: "Validate a snapshot, candidate, or transition", Positionals: positionals(0, 1, "PATH"), Options: []OptionSpec{flag("accepted", "--accepted"), flag("staged", "--staged"), value("base", "BASE", "--base"), value("candidate", "CANDIDATE", "--candidate")}, Store: StoreConditional, Validate: validateCheck},
			{Name: CommandFmt, Path: []string{"fmt"}, Usage: "engram fmt [PATH...] [--check] [--dry-run]", Summary: "Regenerate catalog marker regions", Positionals: positionals(0, -1, "PATH"), Options: []OptionSpec{flag("check", "--check"), dryRun}, Store: StoreAllowed},
			{Name: CommandNew, Path: []string{"new"}, Usage: "engram new TYPE PATH --description TEXT [--fields FILE] [--body FILE|-] [--title TEXT] [--dry-run]", Summary: "Create one typed record and update its catalog", Positionals: positionals(2, 2, "TYPE", "PATH"), Options: []OptionSpec{value("description", "TEXT", "--description"), value("fields", "FILE", "--fields"), value("body", "FILE|-", "--body"), value("title", "TEXT", "--title"), dryRun}, Store: StoreAllowed, Validate: validateNew},
			{Name: CommandMove, Path: []string{"mv"}, Usage: "engram mv FROM TO [--dry-run]", Summary: "Move one record and preserve link meaning", Positionals: positionals(2, 2, "FROM", "TO"), Options: []OptionSpec{dryRun}, Store: StoreAllowed},
			{Name: CommandSchemaInventory, Path: []string{"schema", "inventory"}, Usage: "engram schema inventory", Summary: "List schemas bundled with the CLI", Positionals: positionals(0, 0), Store: StoreForbidden},
			{Name: CommandSchemaList, Path: []string{"schema", "list"}, Usage: "engram schema list [--at PATH]", Summary: "List schemas visible at a content path", Positionals: positionals(0, 0), Options: []OptionSpec{value("at", "PATH", "--at")}, Store: StoreAllowed},
			{Name: CommandSchemaShow, Path: []string{"schema", "show"}, Usage: "engram schema show TYPE [--at PATH]", Summary: "Show one resolved local schema", Positionals: positionals(1, 1, "TYPE"), Options: []OptionSpec{value("at", "PATH", "--at")}, Store: StoreAllowed},
			{Name: CommandSchemaCopy, Path: []string{"schema", "copy"}, Usage: "engram schema copy TYPE [--to SCOPE] [--dry-run]", Summary: "Copy one bundled schema into a store", Positionals: positionals(1, 1, "TYPE"), Options: []OptionSpec{value("to", "SCOPE", "--to"), dryRun}, Store: StoreAllowed},
			{Name: CommandCommit, Path: []string{"commit"}, Usage: "engram commit -m MESSAGE [--dry-run]\nengram commit --dry-run", Summary: "Prepare, validate, and accept the initial candidate", Positionals: positionals(0, 0), Options: []OptionSpec{message, dryRun}, Store: StoreAllowed, Validate: validateCommit},
			{Name: CommandRevert, Path: []string{"revert"}, Usage: "engram revert COMMIT [-m MESSAGE] [--dry-run]", Summary: "Revert an accepted commit through a new transaction", Positionals: positionals(1, 1, "COMMIT"), Options: []OptionSpec{message, dryRun}, Store: StoreAllowed, Validate: validateOptionalMessage},
			{Name: CommandHooksList, Path: []string{"hooks", "list"}, Usage: "engram hooks list [--state accepted|working|staged]", Summary: "List hooks and their local trust state", Positionals: positionals(0, 0), Options: []OptionSpec{state}, Store: StoreAllowed},
			{Name: CommandHooksTrust, Path: []string{"hooks", "trust"}, Usage: "engram hooks trust [--state accepted|working|staged]", Summary: "Trust one complete selected hook set", Positionals: positionals(0, 0), Options: []OptionSpec{state}, Store: StoreAllowed},
			{Name: CommandHooksRevoke, Path: []string{"hooks", "revoke"}, Usage: "engram hooks revoke [HOOK...]", Summary: "Revoke hook trust grants", Positionals: positionals(0, -1, "HOOK"), Store: StoreAllowed},
			{Name: CommandDoctor, Path: []string{"doctor"}, Usage: "engram doctor [PATH] [--recover] [--format text|json]", Summary: "Diagnose integration and recovery state", Positionals: positionals(0, 1, "PATH"), Options: []OptionSpec{flag("recover", "--recover")}, Store: StoreConditional, Validate: validateDoctor},
			{Name: CommandPull, Path: []string{"pull"}, Usage: "engram pull [REMOTE [BRANCH]]\nengram pull --continue\nengram pull --abort", Summary: "Synchronize accepted history from a remote", Positionals: positionals(0, 2, "REMOTE", "BRANCH"), Options: []OptionSpec{flag("continue", "--continue"), flag("abort", "--abort")}, Store: StoreAllowed, Validate: validatePull},
			{Name: CommandPush, Path: []string{"push"}, Usage: "engram push [REMOTE [BRANCH]]", Summary: "Publish accepted history to a remote", Positionals: positionals(0, 2, "REMOTE", "BRANCH"), Store: StoreAllowed},
			{Name: CommandVersion, Path: []string{"version"}, Usage: "engram version [--format text|json]", Summary: "Show CLI and specification compatibility", Positionals: positionals(0, 0), Store: StoreForbidden},
		},
	}
}

func (m *Model) globalOption(alias string) (OptionSpec, bool) {
	return findOption(m.Globals, alias)
}

func (c *CommandSpec) option(alias string) (OptionSpec, bool) {
	return findOption(c.Options, alias)
}

func findOption(options []OptionSpec, alias string) (OptionSpec, bool) {
	for _, option := range options {
		for _, candidate := range option.Aliases {
			if alias == candidate {
				return option, true
			}
		}
	}
	return OptionSpec{}, false
}

func (m *Model) command(path []string) *CommandSpec {
	for i := range m.Commands {
		candidate := &m.Commands[i]
		if len(candidate.Path) != len(path) {
			continue
		}
		if strings.Join(candidate.Path, "\x00") == strings.Join(path, "\x00") {
			return candidate
		}
	}
	return nil
}

func (m *Model) isGroup(name string) bool {
	for _, group := range m.CommandGroups {
		if group.Name == name {
			return true
		}
	}
	for _, command := range m.Commands {
		if len(command.Path) > 1 && command.Path[0] == name {
			return true
		}
	}
	return false
}

func (m *Model) isTopLevel(name string) bool {
	for _, command := range m.Commands {
		if command.Path[0] == name {
			return true
		}
	}
	return false
}

func (m *Model) pathPrefix(path []string) bool {
	for _, command := range m.Commands {
		if len(command.Path) <= len(path) {
			continue
		}
		matched := true
		for index := range path {
			if command.Path[index] != path[index] {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}
