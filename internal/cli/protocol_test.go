package cli

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestOutcomeExitStatus(t *testing.T) {
	t.Parallel()
	tests := []struct {
		outcome Outcome
		status  int
	}{
		{OutcomeOK, 0},
		{OutcomeIssues, 1},
		{OutcomeError, 2},
		{OutcomeIndeterminate, 3},
	}
	for _, test := range tests {
		if status := test.outcome.ExitStatus(); status != test.status {
			t.Errorf("%s status = %d, want %d", test.outcome, status, test.status)
		}
	}
}

func TestWriteEnvelopeStableShape(t *testing.T) {
	t.Parallel()
	command := CommandCheck
	envelope := NewEnvelope(&command, OutcomeIssues, map[string]any{}, nil)
	var output bytes.Buffer
	if err := WriteEnvelope(&output, envelope); err != nil {
		t.Fatal(err)
	}
	want := "{\"version\":1,\"command\":\"check\",\"outcome\":\"issues\",\"exit_status\":1,\"result\":{},\"error\":null}\n"
	if output.String() != want {
		t.Fatalf("envelope = %q, want %q", output.String(), want)
	}
}

func TestWriteErrorEnvelope(t *testing.T) {
	t.Parallel()
	envelope := NewEnvelope(nil, OutcomeError, nil, &ProtocolError{Kind: ErrorUsage, Message: "bad input"})
	var output bytes.Buffer
	if err := WriteEnvelope(&output, envelope); err != nil {
		t.Fatal(err)
	}
	want := "{\"version\":1,\"command\":null,\"outcome\":\"error\",\"exit_status\":2,\"result\":{},\"error\":{\"kind\":\"usage\",\"message\":\"bad input\"}}\n"
	if output.String() != want {
		t.Fatalf("envelope = %q, want %q", output.String(), want)
	}
}

func TestMutationResultHasClosedNonNullShape(t *testing.T) {
	t.Parallel()
	result := NewMutationResult()
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"durable":false,"local_refs":[],"head":null,"checkout_changed":false,"remote":null,"recovery_required":false}`
	if string(data) != want {
		t.Fatalf("mutation result = %s, want %s", data, want)
	}
}
