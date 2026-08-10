package gitcap

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestProbe(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("system Git is unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	report, err := Probe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if report.Executable == "" || report.Version == "" {
		t.Fatalf("incomplete report: %+v", report)
	}
	if !report.Supported {
		t.Fatalf("required Git capabilities unavailable: %+v", report)
	}
}

func TestProbeIgnoresGlobalInitTemplateAndReferenceTransactionHook(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the sentinel uses a POSIX hook program")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("system Git is unavailable")
	}
	root := t.TempDir()
	template := filepath.Join(root, "template")
	hooks := filepath.Join(template, "hooks")
	if err := os.MkdirAll(hooks, 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(root, "reference-transaction-ran")
	program := fmt.Sprintf("#!/bin/sh\nprintf invoked > %q\n", sentinel)
	if err := os.WriteFile(filepath.Join(hooks, "reference-transaction"), []byte(program), 0o700); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(root, "home")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	configuration := fmt.Sprintf("[init]\n\ttemplateDir = %q\n", template)
	if err := os.WriteFile(filepath.Join(home, ".gitconfig"), []byte(configuration), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "xdg"))

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	report, err := Probe(ctx)
	if err != nil || !report.Supported {
		t.Fatalf("Probe = %+v, %v", report, err)
	}
	if _, err := os.Lstat(sentinel); !os.IsNotExist(err) {
		t.Fatalf("capability probe executed a global-template hook: %v", err)
	}
}

func TestIsLowerHex(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"0", "0123456789abcdef"} {
		if !isLowerHex(value) {
			t.Errorf("isLowerHex(%q) = false", value)
		}
	}
	for _, value := range []string{"", "A", "g", "-"} {
		if isLowerHex(value) {
			t.Errorf("isLowerHex(%q) = true", value)
		}
	}
}
