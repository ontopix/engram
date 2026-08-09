package cli

import (
	"encoding/json"
	"fmt"
	"io"
)

const ProtocolVersion = 1

type Outcome string

const (
	OutcomeOK            Outcome = "ok"
	OutcomeIssues        Outcome = "issues"
	OutcomeError         Outcome = "error"
	OutcomeIndeterminate Outcome = "indeterminate"
)

func (o Outcome) ExitStatus() int {
	switch o {
	case OutcomeOK:
		return 0
	case OutcomeIssues:
		return 1
	case OutcomeError:
		return 2
	case OutcomeIndeterminate:
		return 3
	default:
		return 2
	}
}

type ErrorKind string

const (
	ErrorUsage       ErrorKind = "usage"
	ErrorCancelled   ErrorKind = "cancelled"
	ErrorInternal    ErrorKind = "internal"
	ErrorCapability  ErrorKind = "capability"
	ErrorTrust       ErrorKind = "trust"
	ErrorHook        ErrorKind = "hook"
	ErrorNetwork     ErrorKind = "network"
	ErrorConflict    ErrorKind = "conflict"
	ErrorConcurrency ErrorKind = "concurrency"
	ErrorIntegration ErrorKind = "integration"
	ErrorRepository  ErrorKind = "repository"
	ErrorIO          ErrorKind = "io"
	ErrorOperational ErrorKind = "operational"
)

type ProtocolError struct {
	Kind    ErrorKind `json:"kind"`
	Message string    `json:"message"`
}

func (e *ProtocolError) Error() string { return e.Message }

func usageError(format string, arguments ...any) *ProtocolError {
	return &ProtocolError{Kind: ErrorUsage, Message: fmt.Sprintf(format, arguments...)}
}

type Envelope struct {
	Version    int            `json:"version"`
	Command    *CommandName   `json:"command"`
	Outcome    Outcome        `json:"outcome"`
	ExitStatus int            `json:"exit_status"`
	Result     any            `json:"result"`
	Error      *ProtocolError `json:"error"`
}

func NewEnvelope(command *CommandName, outcome Outcome, result any, protocolError *ProtocolError) Envelope {
	if result == nil {
		result = map[string]any{}
	}
	return Envelope{
		Version:    ProtocolVersion,
		Command:    command,
		Outcome:    outcome,
		ExitStatus: outcome.ExitStatus(),
		Result:     result,
		Error:      protocolError,
	}
}

func WriteEnvelope(writer io.Writer, envelope Envelope) error {
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(envelope)
}
