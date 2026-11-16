package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMemoryPackagesInstallIndependently(t *testing.T) {
	binary, root := buildExecutable(t)
	environment := executableTestEnvironment(t)
	for _, name := range []string{"journal", "people", "project"} {
		t.Run(name, func(t *testing.T) {
			store := filepath.Join(t.TempDir(), "memory")
			if err := os.CopyFS(store, os.DirFS(filepath.Join(root, "examples", "minimal"))); err != nil {
				t.Fatal(err)
			}
			run := func(args ...string) {
				t.Helper()
				status, stdout, stderr := runExecutableWithEnvironment(t, binary, root, environment, args...)
				if status != 0 {
					t.Fatalf("engram %q: status=%d stdout=%s stderr=%s", args, status, stdout, stderr)
				}
			}
			run("init", store)
			source := os.DirFS(filepath.Join(root, "examples", "packages", name))
			if err := os.CopyFS(filepath.Join(store, name), source); err != nil {
				t.Fatal(err)
			}
			run("--store", store, "fmt")
			run("--store", store, "add", "--all")
			run("--store", store, "commit", "-m", "Install the "+name+" package", "--format", "json")
			run("--store", store, "check", "--history", "--format", "json")
		})
	}
}
