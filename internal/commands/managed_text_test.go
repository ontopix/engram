package commands

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/ontopix/engram/internal/cli"
)

func TestManagedDiffTextPresentationsGoldenAndJSONInvariant(t *testing.T) {
	root := managedFixture(t)
	appendFile(t, filepath.Join(root, "topics", "why-files.md"), "\nA text-rendered line.\n")

	assertManagedTextGolden(t, "diff-content.golden", root, "diff")
	assertManagedTextGolden(t, "diff-stat.golden", root, "diff", "--stat")
	assertManagedTextGolden(t, "diff-names.golden", root, "diff", "--name-only")

	plain := runManagedOutput(t, "--store", root, "diff", "--format", "json")
	for _, option := range []string{"--stat", "--name-only"} {
		presented := runManagedOutput(t, "--store", root, "diff", option, "--format", "json")
		if !bytes.Equal(presented, plain) {
			t.Fatalf("diff %s changed JSON\nplain: %s\nflag:  %s", option, plain, presented)
		}
	}
}

func TestManagedLogTextPresentationsGoldenAndJSONInvariant(t *testing.T) {
	root := managedFixture(t)
	full := normalizeLogGolden(runManagedOutput(t, "--store", root, "log", "-n", "1"))
	assertGolden(t, "log-full.golden", full)
	oneline := normalizeLogGolden(runManagedOutput(t, "--store", root, "log", "-n", "1", "--oneline"))
	assertGolden(t, "log-oneline.golden", oneline)

	plainJSON := runManagedOutput(t, "--store", root, "log", "-n", "1", "--format", "json")
	onelineJSON := runManagedOutput(t, "--store", root, "log", "-n", "1", "--oneline", "--format", "json")
	if !bytes.Equal(onelineJSON, plainJSON) {
		t.Fatalf("log --oneline changed JSON\nplain: %s\nflag:  %s", plainJSON, onelineJSON)
	}
}

func assertManagedTextGolden(t *testing.T, name, root string, arguments ...string) {
	t.Helper()
	command := []string{"--store", root}
	command = append(command, arguments...)
	assertGolden(t, name, runManagedOutput(t, command...))
}

func runManagedOutput(t *testing.T, arguments ...string) []byte {
	t.Helper()
	app := cli.NewApp()
	RegisterPortable(app)
	RegisterManagedReads(app)
	var stdout, stderr bytes.Buffer
	status := app.Run(context.Background(), arguments, &stdout, &stderr)
	if status != 0 || stderr.Len() != 0 {
		t.Fatalf("engram %v: status=%d stderr=%q stdout=%q", arguments, status, stderr.String(), stdout.String())
	}
	return append([]byte(nil), stdout.Bytes()...)
}

func assertGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	want, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s mismatch\nwant:\n%s\ngot:\n%s", name, want, got)
	}
}

var (
	logOID  = regexp.MustCompile(`\b[0-9a-f]{40}(?:[0-9a-f]{24})?\b`)
	logTime = regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}[+-]\d{2}:\d{2}`)
)

func normalizeLogGolden(value []byte) []byte {
	value = logOID.ReplaceAll(value, []byte("<oid>"))
	return logTime.ReplaceAll(value, []byte("<time>"))
}
