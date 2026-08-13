package cli

import (
	"fmt"
	"io"
	"strings"
)

func WriteHelp(writer io.Writer, model *Model, invocation *Invocation) error {
	if invocation != nil && invocation.Command != nil {
		_, err := fmt.Fprintf(writer, "Usage:\n  %s\n", strings.ReplaceAll(invocation.Command.Usage, "\n", "\n  "))
		return err
	}
	if invocation != nil && invocation.Group != "" {
		prefix := strings.Fields(invocation.Group)
		if _, err := fmt.Fprintf(writer, "Usage:\n  engram %s COMMAND [ARGS]\n\nCommands:\n", invocation.Group); err != nil {
			return err
		}
		var entries []helpEntry
		seen := make(map[string]bool)
		for _, command := range model.Commands {
			if len(command.Path) <= len(prefix) || !equalPathPrefix(command.Path, prefix) {
				continue
			}
			name := command.Path[len(prefix)]
			if seen[name] {
				continue
			}
			seen[name] = true
			summary := command.Summary
			if len(command.Path) > len(prefix)+1 {
				summary = nestedGroupSummary(invocation.Group, name)
			}
			entries = append(entries, helpEntry{Name: name, Summary: summary})
		}
		if err := writeHelpEntries(writer, entries, 0); err != nil {
			return err
		}
		_, err := fmt.Fprintf(writer, "\nRun 'engram %s COMMAND --help' for more information on a command.\n", invocation.Group)
		return err
	}

	if _, err := fmt.Fprintln(writer, "Usage:\n  engram [GLOBAL-OPTIONS] COMMAND [ARGS]\n\nThese are the engram commands grouped by workflow:"); err != nil {
		return err
	}
	rootWidth := 0
	for _, category := range model.HelpCategories {
		for _, name := range category.Commands {
			rootWidth = max(rootWidth, len(name))
		}
	}
	for _, category := range model.HelpCategories {
		if _, err := fmt.Fprintf(writer, "\n%s:\n", category.Title); err != nil {
			return err
		}
		entries := make([]helpEntry, 0, len(category.Commands))
		for _, name := range category.Commands {
			entries = append(entries, helpEntry{Name: name, Summary: topLevelSummary(model, name)})
		}
		if err := writeHelpEntries(writer, entries, rootWidth); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintln(writer, "\nGlobal options:\n  -s, --store PATH       Select a store root\n      --format FORMAT    Set output format: text or json\n      --no-color         Disable ANSI styling\n  -q, --quiet            Suppress ordinary successful text\n  -h, --help             Show help\n  -V, --version          Show CLI version\n\nRun 'engram COMMAND --help' for more information on a command.")
	return err
}

func equalPathPrefix(path, prefix []string) bool {
	if len(path) < len(prefix) {
		return false
	}
	for index := range prefix {
		if path[index] != prefix[index] {
			return false
		}
	}
	return true
}

func nestedGroupSummary(group, name string) string {
	if group == "config" && name == "attachment" {
		return "Manage declared memory repositories"
	}
	return "Manage " + name
}

type helpEntry struct {
	Name    string
	Summary string
}

func writeHelpEntries(writer io.Writer, entries []helpEntry, width int) error {
	for _, entry := range entries {
		width = max(width, len(entry.Name))
	}
	for _, entry := range entries {
		if _, err := fmt.Fprintf(writer, "  %-*s  %s\n", width, entry.Name, entry.Summary); err != nil {
			return err
		}
	}
	return nil
}

func topLevelSummary(model *Model, name string) string {
	for _, group := range model.CommandGroups {
		if group.Name == name {
			return group.Summary
		}
	}
	for _, command := range model.Commands {
		if len(command.Path) == 1 && command.Path[0] == name {
			return command.Summary
		}
	}
	return ""
}
