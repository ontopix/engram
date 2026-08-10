package commands

import (
	"path/filepath"
	"testing"

	"github.com/ontopix/engram/internal/cli"
)

func TestRegisterReferenceAtInstallsCompleteCommandSurface(t *testing.T) {
	app := cli.NewApp()
	version := app.Handlers[cli.CommandVersion]
	app.Handlers = map[cli.CommandName]cli.Handler{cli.CommandVersion: version}
	RegisterReferenceAt(app, filepath.Join(t.TempDir(), "config", "hook-trust-v1.json"))
	for _, command := range app.Model.Commands {
		if app.Handlers[command.Name] == nil {
			t.Errorf("handler %q is not installed", command.Name)
		}
	}
}
