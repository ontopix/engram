package main

import (
	"reflect"
	"testing"
)

func TestGitRevisionArgumentsEnableLongPathsBeforeSubcommand(t *testing.T) {
	want := []string{
		"-c", "core.longpaths=true",
		"-C", "/exact/repository",
		"rev-parse", "--verify", "HEAD^{commit}",
	}
	if got := gitRevisionArguments("/exact/repository"); !reflect.DeepEqual(got, want) {
		t.Fatalf("git revision arguments = %#v, want %#v", got, want)
	}
}
