package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
)

func TestAppReturnsTypedCapabilityErrorForUnimplementedCommand(t *testing.T) {
	t.Parallel()
	app := NewApp()
	var stdout, stderr bytes.Buffer
	status := app.Run(context.Background(), []string{"--format", "json", "status"}, &stdout, &stderr)
	if status != 2 {
		t.Fatalf("status = %d, want 2", status)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q", stderr.String())
	}
	var envelope Envelope
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Command == nil || *envelope.Command != CommandStatus {
		t.Fatalf("command = %#v", envelope.Command)
	}
	if envelope.Error == nil || envelope.Error.Kind != ErrorCapability {
		t.Fatalf("error = %#v", envelope.Error)
	}
}

func TestAppReturnsJSONUsageErrorWithKnownCommand(t *testing.T) {
	t.Parallel()
	app := NewApp()
	var stdout, stderr bytes.Buffer
	status := app.Run(context.Background(), []string{"status", "extra", "--format", "json"}, &stdout, &stderr)
	if status != 2 || stderr.Len() != 0 {
		t.Fatalf("status = %d, stderr = %q", status, stderr.String())
	}
	var envelope Envelope
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Command == nil || *envelope.Command != CommandStatus || envelope.Error == nil || envelope.Error.Kind != ErrorUsage {
		t.Fatalf("envelope = %#v", envelope)
	}
}

func TestAppVersionTextIgnoresQuiet(t *testing.T) {
	t.Parallel()
	app := NewApp()
	var stdout, stderr bytes.Buffer
	status := app.Run(context.Background(), []string{"--quiet", "version"}, &stdout, &stderr)
	if status != 0 || stderr.Len() != 0 {
		t.Fatalf("status = %d, stderr = %q", status, stderr.String())
	}
	if !strings.HasPrefix(stdout.String(), "engram ") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestAppVersionJSON(t *testing.T) {
	t.Parallel()
	app := NewApp()
	var stdout, stderr bytes.Buffer
	status := app.Run(context.Background(), []string{"version", "--format", "json"}, &stdout, &stderr)
	if status != 0 || stderr.Len() != 0 {
		t.Fatalf("status = %d, stderr = %q", status, stderr.String())
	}
	var document map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	result := document["result"].(map[string]any)
	if result["cli_version"] == "" || len(result["core_versions"].([]any)) != 1 || len(result["annex_versions"].([]any)) != 2 {
		t.Fatalf("result = %#v", result)
	}
}

func TestAppRootHelp(t *testing.T) {
	t.Parallel()
	app := NewApp()
	var stdout, stderr bytes.Buffer
	status := app.Run(context.Background(), []string{"--help"}, &stdout, &stderr)
	if status != 0 || stderr.Len() != 0 || !strings.Contains(stdout.String(), "commands grouped by workflow") {
		t.Fatalf("status = %d, stdout = %q, stderr = %q", status, stdout.String(), stderr.String())
	}
}

func TestAppRendersSuccessfulTextAndQuietSuppressesIt(t *testing.T) {
	t.Parallel()
	app := NewApp()
	app.Handlers[CommandStatus] = HandlerFunc(func(context.Context, *Invocation) Result {
		return Result{Outcome: OutcomeOK, Value: struct {
			Changed bool `json:"changed"`
		}{Changed: true}}
	})
	var stdout, stderr bytes.Buffer
	if status := app.Run(context.Background(), []string{"status"}, &stdout, &stderr); status != 0 {
		t.Fatalf("status = %d", status)
	}
	if !strings.Contains(stdout.String(), `"changed": true`) || stderr.Len() != 0 {
		t.Fatalf("stdout = %q, stderr = %q", stdout.String(), stderr.String())
	}
	stdout.Reset()
	if status := app.Run(context.Background(), []string{"--quiet", "status"}, &stdout, &stderr); status != 0 || stdout.Len() != 0 {
		t.Fatalf("quiet status = %d, stdout = %q", status, stdout.String())
	}
}

func TestAppUsesOptionalTextRendererOnlyForHumanOutput(t *testing.T) {
	t.Parallel()
	app := NewApp()
	app.Handlers[CommandStatus] = HandlerFunc(func(context.Context, *Invocation) Result {
		return Result{
			Outcome: OutcomeOK,
			Value: struct {
				Changed bool `json:"changed"`
			}{Changed: true},
			Text: TextRendererFunc(func(output io.Writer) error {
				_, err := io.WriteString(output, "specialized\n")
				return err
			}),
		}
	})
	var stdout, stderr bytes.Buffer
	if status := app.Run(context.Background(), []string{"status"}, &stdout, &stderr); status != 0 || stdout.String() != "specialized\n" || stderr.Len() != 0 {
		t.Fatalf("text: status=%d stdout=%q stderr=%q", status, stdout.String(), stderr.String())
	}
	stdout.Reset()
	if status := app.Run(context.Background(), []string{"status", "--format", "json"}, &stdout, &stderr); status != 0 || stderr.Len() != 0 {
		t.Fatalf("JSON: status=%d stdout=%q stderr=%q", status, stdout.String(), stderr.String())
	}
	var envelope Envelope
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	value, ok := envelope.Result.(map[string]any)
	if !ok || value["changed"] != true {
		t.Fatalf("result=%#v", envelope.Result)
	}
}
