package main

import (
	"context"
	"os"

	"github.com/ontopix/engram/internal/cli"
)

func main() {
	os.Exit(cli.NewApp().Run(context.Background(), os.Args[1:], os.Stdout, os.Stderr))
}
