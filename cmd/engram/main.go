package main

import (
	"context"
	"os"

	"github.com/ontopix/engram/internal/cli"
	"github.com/ontopix/engram/internal/commands"
)

func main() {
	app := cli.NewApp()
	app.Stdin = os.Stdin
	commands.RegisterPortable(app)
	commands.RegisterManagedReads(app)
	commands.RegisterAttachments(app)
	commands.RegisterStaging(app)
	commands.RegisterDraft(app)
	commands.RegisterHooks(app)
	commands.RegisterAcquisition(app)
	os.Exit(app.Run(context.Background(), os.Args[1:], os.Stdout, os.Stderr))
}
