package main

import (
	"context"
	"os"

	"github.com/ontopix/engram/internal/cli"
	"github.com/ontopix/engram/internal/commands"
)

func main() {
	app := cli.NewApp()
	commands.RegisterPortable(app)
	os.Exit(app.Run(context.Background(), os.Args[1:], os.Stdout, os.Stderr))
}
