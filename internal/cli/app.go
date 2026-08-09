package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	engramversion "github.com/ontopix/engram/internal/version"
)

type Result struct {
	Outcome Outcome
	Value   any
	Error   *ProtocolError
}

type Handler interface {
	Run(context.Context, *Invocation) Result
}

type HandlerFunc func(context.Context, *Invocation) Result

func (f HandlerFunc) Run(ctx context.Context, invocation *Invocation) Result {
	return f(ctx, invocation)
}

type App struct {
	Model    *Model
	Handlers map[CommandName]Handler
	Version  engramversion.Provider
	// Stdin is the byte source for commands whose explicit grammar accepts
	// standard input (currently `new --body -`). Keeping it on the application
	// makes parser invocations pure while allowing embedders and tests to supply
	// a bounded reader instead of inheriting process-global state.
	Stdin io.Reader
}

func NewApp() *App {
	model := DefaultModel()
	app := &App{
		Model:    model,
		Handlers: make(map[CommandName]Handler, len(model.Commands)),
		Version:  engramversion.NewProvider(),
		Stdin:    io.LimitReader(emptyReader{}, 0),
	}
	for _, command := range model.Commands {
		name := command.Name
		app.Handlers[name] = HandlerFunc(func(context.Context, *Invocation) Result {
			return Result{
				Outcome: OutcomeError,
				Error: &ProtocolError{
					Kind:    ErrorCapability,
					Message: fmt.Sprintf("%s is not implemented in this foundation build", name),
				},
			}
		})
	}
	app.Handlers[CommandVersion] = HandlerFunc(func(ctx context.Context, _ *Invocation) Result {
		return Result{Outcome: OutcomeOK, Value: app.Version.Info(ctx)}
	})
	return app
}

// emptyReader is the allocation-free default input for library callers. The
// executable replaces it with os.Stdin; commands never read it unless their
// explicit option selects standard input.
type emptyReader struct{}

func (emptyReader) Read([]byte) (int, error) { return 0, io.EOF }

func (a *App) Run(ctx context.Context, arguments []string, stdout, stderr io.Writer) int {
	invocation, parseFailure := Parse(a.Model, arguments)
	if parseFailure != nil {
		return writeFailure(stdout, stderr, parseFailure.Globals.Format, parseFailure.Command, parseFailure.Error)
	}
	if invocation.Globals.Help {
		if writeError := WriteHelp(stdout, a.Model, invocation); writeError != nil {
			return writeFailure(stdout, stderr, invocation.Globals.Format, invocation.CommandName(), &ProtocolError{Kind: ErrorIO, Message: writeError.Error()})
		}
		return OutcomeOK.ExitStatus()
	}

	handler, ok := a.Handlers[invocation.Command.Name]
	if !ok {
		return writeFailure(stdout, stderr, invocation.Globals.Format, invocation.CommandName(), &ProtocolError{Kind: ErrorInternal, Message: "missing command handler"})
	}
	result := handler.Run(ctx, invocation)
	if result.Outcome == "" {
		result.Outcome = OutcomeError
	}
	if result.Outcome == OutcomeError && result.Error == nil {
		result.Error = &ProtocolError{Kind: ErrorInternal, Message: "error result has no error payload"}
	}
	if invocation.Globals.Format == FormatJSON {
		envelope := NewEnvelope(invocation.CommandName(), result.Outcome, result.Value, result.Error)
		if writeError := WriteEnvelope(stdout, envelope); writeError != nil {
			fmt.Fprintf(stderr, "engram: %s\n", writeError)
			return OutcomeError.ExitStatus()
		}
		return result.Outcome.ExitStatus()
	}
	if result.Outcome == OutcomeError {
		fmt.Fprintf(stderr, "engram: %s\n", result.Error.Message)
		return result.Outcome.ExitStatus()
	}
	if invocation.Command.Name == CommandVersion {
		info := result.Value.(engramversion.Info)
		fmt.Fprintf(stdout, "engram %s\n", info.CLIVersion)
		return result.Outcome.ExitStatus()
	}
	if invocation.Globals.Quiet && result.Outcome == OutcomeOK {
		return result.Outcome.ExitStatus()
	}
	if result.Value != nil {
		// Protocol v1 leaves the exact human prose non-normative. A stable,
		// indented rendering keeps every closed result field visible until a
		// command elects a more specialized human presentation, while JSON mode
		// above remains the machine contract.
		encoder := json.NewEncoder(stdout)
		encoder.SetEscapeHTML(false)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(result.Value); err != nil {
			fmt.Fprintf(stderr, "engram: %s\n", err)
			return OutcomeError.ExitStatus()
		}
	}
	return result.Outcome.ExitStatus()
}

func writeFailure(stdout, stderr io.Writer, format OutputFormat, command *CommandName, protocolError *ProtocolError) int {
	if format == FormatJSON {
		envelope := NewEnvelope(command, OutcomeError, nil, protocolError)
		if writeError := WriteEnvelope(stdout, envelope); writeError != nil {
			fmt.Fprintf(stderr, "engram: %s\n", writeError)
		}
		return OutcomeError.ExitStatus()
	}
	fmt.Fprintf(stderr, "engram: %s\n", protocolError.Message)
	return OutcomeError.ExitStatus()
}
