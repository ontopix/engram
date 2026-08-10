// Command release builds the complete deterministic engram release set. It is
// an operator tool, not part of the public engram command surface.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/ontopix/engram/internal/releasepack"
)

func main() {
	var version, revision, output, repository string
	var epoch int64
	flag.StringVar(&version, "version", "", "release version (vMAJOR.MINOR.PATCH)")
	flag.StringVar(&revision, "revision", "", "full source Git object ID (default: repository HEAD)")
	flag.StringVar(&output, "output", "dist", "artifact output directory")
	flag.StringVar(&repository, "repository", ".", "engram repository root")
	flag.Int64Var(&epoch, "source-date-epoch", environmentEpoch(), "deterministic archive timestamp")
	flag.Parse()
	if flag.NArg() != 0 || version == "" {
		fmt.Fprintln(os.Stderr, "usage: go run ./tools/release -version vMAJOR.MINOR.PATCH [-revision FULL_OID] [-output DIR]")
		os.Exit(2)
	}
	if revision == "" {
		value, err := gitRevision(repository)
		if err != nil {
			fmt.Fprintf(os.Stderr, "release: %v\n", err)
			os.Exit(2)
		}
		revision = value
	}
	artifacts, err := releasepack.Build(context.Background(), repository, releasepack.Options{
		Version: version, Revision: revision, Output: output, SourceDateEpoch: epoch,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "release: %v\n", err)
		os.Exit(1)
	}
	for _, artifact := range artifacts {
		fmt.Printf("%s  %s\n", artifact.SHA256, artifact.Name)
	}
}

func gitRevision(repository string) (string, error) {
	command := exec.Command("git", "-C", repository, "rev-parse", "--verify", "HEAD^{commit}")
	output, err := command.Output()
	if err != nil {
		return "", fmt.Errorf("resolve source revision: %w", err)
	}
	return strings.TrimSpace(string(output)), nil
}

func environmentEpoch() int64 {
	value := os.Getenv("SOURCE_DATE_EPOCH")
	if value == "" {
		return 0
	}
	epoch, err := strconv.ParseInt(value, 10, 64)
	if err != nil || epoch < 0 {
		fmt.Fprintln(os.Stderr, "release: SOURCE_DATE_EPOCH must be a non-negative integer")
		os.Exit(2)
	}
	return epoch
}
