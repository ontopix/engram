package cli

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

type OutputFormat string

const (
	FormatText OutputFormat = "text"
	FormatJSON OutputFormat = "json"
)

type GlobalOptions struct {
	Store        string
	StoreSet     bool
	Format       OutputFormat
	NoColor      bool
	Quiet        bool
	Help         bool
	VersionAlias bool
}

type OptionValues map[string][]string

func (v OptionValues) Has(name string) bool {
	_, ok := v[name]
	return ok
}

func (v OptionValues) One(name string) (string, bool) {
	values, ok := v[name]
	if !ok || len(values) == 0 {
		return "", false
	}
	return values[len(values)-1], true
}

func (v OptionValues) All(name string) []string {
	return append([]string(nil), v[name]...)
}

type Invocation struct {
	Command   *CommandSpec
	Group     string
	Globals   GlobalOptions
	Options   OptionValues
	Arguments []string
}

func (i *Invocation) CommandName() *CommandName {
	if i == nil || i.Command == nil {
		return nil
	}
	name := i.Command.Name
	return &name
}

type ParseFailure struct {
	Command *CommandName
	Globals GlobalOptions
	Error   *ProtocolError
}

func Parse(model *Model, arguments []string) (*Invocation, *ParseFailure) {
	if model == nil {
		model = DefaultModel()
	}
	invocation := &Invocation{
		Globals: GlobalOptions{Format: FormatText},
		Options: make(OptionValues),
	}
	seenGlobals := make(map[string]bool)
	optionsEnabled := true
	index := 0
	var commandPath []string
	explicitCommand := false

	for index < len(arguments) && invocation.Command == nil {
		token := arguments[index]
		if optionsEnabled && token == "--" {
			optionsEnabled = false
			index++
			continue
		}
		if optionsEnabled {
			if option, ok := model.globalOption(token); ok {
				next, parseError := consumeGlobal(invocation, seenGlobals, option, arguments, index)
				if parseError != nil {
					return nil, failure(invocation, parseError)
				}
				index = next
				continue
			}
			if strings.HasPrefix(token, "-") && token != "-" {
				return nil, failure(invocation, usageError("unknown global option %q", token))
			}
		}

		if len(commandPath) == 0 {
			if !model.isTopLevel(token) {
				return nil, failure(invocation, usageError("unknown command %q", token))
			}
			commandPath = append(commandPath, token)
			explicitCommand = true
			index++
			if model.isGroup(token) {
				invocation.Group = token
				continue
			}
			invocation.Command = model.command(commandPath)
			continue
		}

		commandPath = append(commandPath, token)
		index++
		invocation.Command = model.command(commandPath)
		if invocation.Command == nil {
			return nil, failure(invocation, usageError("unknown %s subcommand %q", commandPath[0], token))
		}
	}

	if invocation.Command == nil {
		switch {
		case invocation.Globals.VersionAlias && invocation.Group == "":
			invocation.Command = model.command([]string{"version"})
		case invocation.Globals.Help:
			return invocation, validateHelp(invocation)
		case invocation.Group != "":
			return nil, failure(invocation, usageError("%s requires a subcommand", invocation.Group))
		default:
			return nil, failure(invocation, usageError("a command is required"))
		}
	}

	for index < len(arguments) {
		token := arguments[index]
		if optionsEnabled && token == "--" {
			optionsEnabled = false
			index++
			continue
		}
		if optionsEnabled {
			if option, ok := invocation.Command.option(token); ok {
				next, parseError := consumeCommandOption(invocation, option, arguments, index)
				if parseError != nil {
					return nil, failure(invocation, parseError)
				}
				index = next
				continue
			}
			if option, ok := model.globalOption(token); ok {
				next, parseError := consumeGlobal(invocation, seenGlobals, option, arguments, index)
				if parseError != nil {
					return nil, failure(invocation, parseError)
				}
				index = next
				continue
			}
			if strings.HasPrefix(token, "-") && token != "-" {
				return nil, failure(invocation, usageError("unknown option %q for %s", token, invocation.Command.Name))
			}
		}
		invocation.Arguments = append(invocation.Arguments, token)
		index++
	}

	if invocation.Globals.VersionAlias && explicitCommand {
		return nil, failure(invocation, usageError("--version cannot be combined with another command"))
	}
	if invocation.Globals.VersionAlias && invocation.Globals.Help {
		return nil, failure(invocation, usageError("--version cannot be combined with --help"))
	}
	if invocation.Globals.Help {
		return invocation, validateHelp(invocation)
	}
	if parseError := validateInvocation(invocation); parseError != nil {
		return nil, failure(invocation, parseError)
	}
	return invocation, nil
}

func consumeGlobal(invocation *Invocation, seen map[string]bool, option OptionSpec, arguments []string, index int) (int, *ProtocolError) {
	if seen[option.Name] {
		return index, usageError("global option %s cannot be repeated", option.Aliases[len(option.Aliases)-1])
	}
	seen[option.Name] = true
	valueText := ""
	if option.TakesValue() {
		if index+1 >= len(arguments) {
			return index, usageError("option %s requires %s", arguments[index], option.ValueName)
		}
		valueText = arguments[index+1]
		if !option.accepts(valueText) {
			return index, usageError("invalid %s value %q", option.ValueName, valueText)
		}
	}

	switch option.Name {
	case "store":
		invocation.Globals.Store = valueText
		invocation.Globals.StoreSet = true
	case "format":
		invocation.Globals.Format = OutputFormat(valueText)
	case "no-color":
		invocation.Globals.NoColor = true
	case "quiet":
		invocation.Globals.Quiet = true
	case "help":
		invocation.Globals.Help = true
	case "version":
		invocation.Globals.VersionAlias = true
	}
	if option.TakesValue() {
		return index + 2, nil
	}
	return index + 1, nil
}

func consumeCommandOption(invocation *Invocation, option OptionSpec, arguments []string, index int) (int, *ProtocolError) {
	if invocation.Options.Has(option.Name) && !option.Repeatable {
		return index, usageError("option %s cannot be repeated", arguments[index])
	}
	valueText := ""
	if option.TakesValue() {
		if index+1 >= len(arguments) {
			return index, usageError("option %s requires %s", arguments[index], option.ValueName)
		}
		valueText = arguments[index+1]
		if !option.accepts(valueText) {
			return index, usageError("invalid %s value %q", option.ValueName, valueText)
		}
	}
	invocation.Options[option.Name] = append(invocation.Options[option.Name], valueText)
	if option.TakesValue() {
		return index + 2, nil
	}
	return index + 1, nil
}

func validateHelp(invocation *Invocation) *ParseFailure {
	if invocation.Globals.Format == FormatJSON {
		return failure(invocation, usageError("JSON help is not part of protocol v1"))
	}
	if invocation.Globals.StoreSet && (invocation.Command == nil || invocation.Command.Store == StoreForbidden) {
		return failure(invocation, usageError("--store is not accepted by this command"))
	}
	return nil
}

func validateInvocation(invocation *Invocation) *ProtocolError {
	positional := invocation.Command.Positionals
	count := len(invocation.Arguments)
	if count < positional.Min {
		return usageError("%s requires %s", invocation.Command.Name, positionalDescription(positional))
	}
	if positional.Max >= 0 && count > positional.Max {
		return usageError("too many arguments for %s", invocation.Command.Name)
	}
	if invocation.Globals.StoreSet && invocation.Command.Store == StoreForbidden {
		return usageError("--store is not accepted by %s", invocation.Command.Name)
	}
	if invocation.Command.Validate != nil {
		return invocation.Command.Validate(invocation)
	}
	return nil
}

func positionalDescription(specification PositionalSpec) string {
	if len(specification.Names) == 0 {
		return "no positional arguments"
	}
	return strings.Join(specification.Names[:min(specification.Min, len(specification.Names))], " and ")
}

func validateDiff(invocation *Invocation) *ProtocolError {
	if invocation.Options.Has("staged") && len(invocation.Arguments) != 0 {
		return usageError("--staged cannot be combined with revisions")
	}
	if invocation.Options.Has("stat") && invocation.Options.Has("name-only") {
		return usageError("--stat and --name-only are mutually exclusive")
	}
	return nil
}

func validateLog(invocation *Invocation) *ProtocolError {
	valueText, ok := invocation.Options.One("count")
	if !ok {
		return nil
	}
	count, conversionError := strconv.ParseInt(valueText, 10, 32)
	if conversionError != nil || count < 1 || count > 2147483647 {
		return usageError("COUNT must be an integer from 1 through 2147483647")
	}
	return nil
}

func validateAdd(invocation *Invocation) *ProtocolError {
	all := invocation.Options.Has("all")
	if all && len(invocation.Arguments) != 0 {
		return usageError("--all cannot be combined with paths")
	}
	if !all && len(invocation.Arguments) == 0 {
		return usageError("add requires one or more paths or --all")
	}
	return nil
}

func validateCheck(invocation *Invocation) *ProtocolError {
	accepted := invocation.Options.Has("accepted")
	staged := invocation.Options.Has("staged")
	base := invocation.Options.Has("base")
	candidate := invocation.Options.Has("candidate")

	selected := 0
	if accepted {
		selected++
	}
	if staged {
		selected++
	}
	if base || candidate {
		selected++
	}
	if selected > 1 {
		return usageError("check forms are mutually exclusive")
	}
	if base != candidate {
		return usageError("--base and --candidate must be provided together")
	}
	if selected != 0 && len(invocation.Arguments) != 0 {
		return usageError("explicit check modes cannot be combined with PATH")
	}
	if invocation.Globals.StoreSet && (len(invocation.Arguments) != 0 || base || candidate) {
		return usageError("--store is not accepted by explicit-path or snapshot-pair check")
	}
	return nil
}

func validateNew(invocation *Invocation) *ProtocolError {
	if !invocation.Options.Has("description") {
		return usageError("new requires --description TEXT")
	}
	if invocation.Options.Has("body") && invocation.Options.Has("title") {
		return usageError("--body and --title are mutually exclusive")
	}
	return nil
}

func validateCommit(invocation *Invocation) *ProtocolError {
	message, hasMessage := invocation.Options.One("message")
	if !hasMessage && !invocation.Options.Has("dry-run") {
		return usageError("commit requires -m MESSAGE unless --dry-run is used")
	}
	if hasMessage {
		return validateMessage(message)
	}
	return nil
}

func validateOptionalMessage(invocation *Invocation) *ProtocolError {
	message, ok := invocation.Options.One("message")
	if !ok {
		return nil
	}
	return validateMessage(message)
}

func validateMessage(message string) *ProtocolError {
	if message == "" || !utf8.ValidString(message) || strings.ContainsAny(message, "\x00\r") || strings.HasSuffix(message, "\n") {
		return usageError("MESSAGE must be non-empty UTF-8 with no NUL, CR, or final LF")
	}
	return nil
}

func validateDoctor(invocation *Invocation) *ProtocolError {
	if invocation.Globals.StoreSet && len(invocation.Arguments) != 0 {
		return usageError("--store cannot be combined with positional doctor PATH")
	}
	return nil
}

func validatePull(invocation *Invocation) *ProtocolError {
	continueReplay := invocation.Options.Has("continue")
	abortReplay := invocation.Options.Has("abort")
	if continueReplay && abortReplay {
		return usageError("--continue and --abort are mutually exclusive")
	}
	if (continueReplay || abortReplay) && len(invocation.Arguments) != 0 {
		return usageError("replay control cannot be combined with REMOTE or BRANCH")
	}
	return nil
}

func failure(invocation *Invocation, protocolError *ProtocolError) *ParseFailure {
	return &ParseFailure{Command: invocation.CommandName(), Globals: invocation.Globals, Error: protocolError}
}

func (i Invocation) String() string {
	if i.Command == nil {
		return ""
	}
	return fmt.Sprintf("%s %s", i.Command.Name, strings.Join(i.Arguments, " "))
}
