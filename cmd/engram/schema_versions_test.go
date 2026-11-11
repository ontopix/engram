package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManagedSchemaPublicationPreservesHistoricalRecords(t *testing.T) {
	binary, root := buildExecutable(t)
	environment := executableTestEnvironment(t)
	store := filepath.Join(t.TempDir(), "memory")
	if err := os.CopyFS(store, os.DirFS(filepath.Join(root, "examples", "schema-versions"))); err != nil {
		t.Fatal(err)
	}
	read := func(name string) []byte {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(store, name))
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	write := func(name string, data []byte) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(store, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run := func(want int, finding string, args ...string) string {
		t.Helper()
		status, stdout, stderr := runExecutableWithEnvironment(t, binary, root, environment, args...)
		if status != want || finding != "" && !strings.Contains(stdout, finding) {
			t.Fatalf("engram %q: status=%d stdout=%s stderr=%s", args, status, stdout, stderr)
		}
		return stdout
	}
	secondSchema, thirdSchema := ".engram/schemas/journal-entry@v2.md", ".engram/schemas/journal-entry@v3.md"
	saved := make(map[string][]byte)
	for _, name := range []string{secondSchema, thirdSchema, "2026-11-02.md", "2026-11-03.md"} {
		saved[name] = read(name)
		if err := os.Remove(filepath.Join(store, name)); err != nil {
			t.Fatal(err)
		}
	}
	write("README.md", []byte("---\ndescription: Schema publication integration test.\ncatalog: none\n---\n# Memory\n"))
	original := read("2026-11-01.md")
	run(0, "", "init", store)
	for _, pair := range [][2]string{{secondSchema, "2026-11-02.md"}, {thirdSchema, "2026-11-03.md"}} {
		for _, name := range pair {
			write(name, saved[name])
		}
		run(0, "", "--store", store, "add", "--all")
		run(0, "", "--store", store, "commit", "-m", "Publish another journal contract", "--format", "json")
	}
	run(0, "", "--store", store, "check", "--history", "--format", "json")
	if !bytes.Equal(original, read("2026-11-01.md")) {
		t.Fatal("publication changed the historical record")
	}
	// Inverse replay may not remove a published revision, even with its records.
	run(1, "E505", "--store", store, "revert", "HEAD", "--dry-run", "--format", "json")
	// A fully valid destination record still cannot waive the base append-only policy.
	write("2026-11-01.md", bytes.ReplaceAll(saved["2026-11-03.md"], []byte("2026-11-03"), []byte("2026-11-01")))
	run(0, "", "--store", store, "add", "2026-11-01.md")
	run(1, "E502", "--store", store, "commit", "--dry-run", "--format", "json")
	write("2026-11-01.md", append(append([]byte(nil), original...), []byte("\n- Later evidence.\n")...))
	run(0, "", "--store", store, "add", "2026-11-01.md")
	run(0, "", "--store", store, "commit", "-m", "Append evidence under the original contract")
	run(0, "", "--store", store, "check", "--history", "--format", "json")
}
