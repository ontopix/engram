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
		if _, err := fmt.Fprintf(writer, "Usage:\n  engram %s COMMAND [ARGS]\n\nCommands:\n", invocation.Group); err != nil {
			return err
		}
		for _, command := range model.Commands {
			if len(command.Path) == 2 && command.Path[0] == invocation.Group {
				if _, err := fmt.Fprintf(writer, "  %s\n", command.Path[1]); err != nil {
					return err
				}
			}
		}
		return nil
	}

	if _, err := fmt.Fprintln(writer, "Usage:\n  engram [GLOBAL-OPTIONS] COMMAND [ARGS]\n\nCommands:"); err != nil {
		return err
	}
	seen := make(map[string]bool)
	for _, command := range model.Commands {
		name := command.Path[0]
		if seen[name] {
			continue
		}
		seen[name] = true
		if _, err := fmt.Fprintf(writer, "  %s\n", name); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintln(writer, "\nGlobal options:\n  -s, --store PATH\n      --format text|json\n      --no-color\n  -q, --quiet\n  -h, --help\n  -V, --version")
	return err
}
