package gitcap

import (
	"context"
	"os/exec"
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
