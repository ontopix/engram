package schemaprofile

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

// Problem is one schema-profile violation at an RFC 6901 location. The empty
// location denotes the schema root.
type Problem struct {
	Location string
	Message  string
}

// ProfileError contains all profile violations found by the finite syntactic
// analysis. Standard JSON Schema vocabulary-shape errors are represented in
// the same type after metaschema validation.
type ProfileError struct {
	Problems []Problem
	cause    error
}

func (e *ProfileError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if len(e.Problems) == 0 {
		if e.cause != nil {
			return e.cause.Error()
		}
		return "invalid engram schema profile"
	}
	parts := make([]string, len(e.Problems))
	for i, problem := range e.Problems {
		location := problem.Location
		if location == "" {
			location = "<root>"
		}
		parts[i] = fmt.Sprintf("%s: %s", location, problem.Message)
	}
	return "invalid engram schema profile: " + strings.Join(parts, "; ")
}

func (e *ProfileError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func newProfileError(problems ...Problem) *ProfileError {
	return &ProfileError{Problems: sortedProblems(problems)}
}

func sortedProblems(problems []Problem) []Problem {
	result := append([]Problem(nil), problems...)
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].Location != result[j].Location {
			return result[i].Location < result[j].Location
		}
		return result[i].Message < result[j].Message
	})
	return result
}

// ValidationProblem is one assertion failure with stable RFC 6901 locations.
type ValidationProblem struct {
	InstanceLocation string
	SchemaLocation   string
	Message          string
}

// ValidationError is returned when an instance is outside the exact JSON data
// model or does not satisfy a compiled schema.
type ValidationError struct {
	Problems []ValidationProblem
	cause    error
}

func (e *ValidationError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if len(e.Problems) == 0 {
		if e.cause != nil {
			return e.cause.Error()
		}
		return "schema validation failed"
	}
	parts := make([]string, len(e.Problems))
	for i, problem := range e.Problems {
		location := problem.InstanceLocation
		if location == "" {
			location = "<root>"
		}
		parts[i] = fmt.Sprintf("%s: %s", location, problem.Message)
	}
	return "schema validation failed: " + strings.Join(parts, "; ")
}

func (e *ValidationError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func validationError(err error) error {
	var source *jsonschema.ValidationError
	if !errors.As(err, &source) {
		return &ValidationError{cause: err}
	}
	problems := make([]ValidationProblem, 0, 4)
	collectValidationProblems(source, &problems)
	sort.SliceStable(problems, func(i, j int) bool {
		if problems[i].InstanceLocation != problems[j].InstanceLocation {
			return problems[i].InstanceLocation < problems[j].InstanceLocation
		}
		if problems[i].SchemaLocation != problems[j].SchemaLocation {
			return problems[i].SchemaLocation < problems[j].SchemaLocation
		}
		return problems[i].Message < problems[j].Message
	})
	return &ValidationError{Problems: problems, cause: err}
}

func collectValidationProblems(current *jsonschema.ValidationError, result *[]ValidationProblem) {
	if len(current.Causes) != 0 {
		for _, cause := range current.Causes {
			collectValidationProblems(cause, result)
		}
		return
	}
	schemaLocation := pointerFromURL(current.SchemaURL)
	if keywordPath := current.ErrorKind.KeywordPath(); len(keywordPath) != 0 {
		base, _ := decodePointer(schemaLocation)
		schemaLocation = pointer(append(base, keywordPath...))
	}
	*result = append(*result, ValidationProblem{
		InstanceLocation: pointer(current.InstanceLocation),
		SchemaLocation:   schemaLocation,
		Message:          current.Error(),
	})
}

func compileProfileError(err error) error {
	problem := Problem{Message: err.Error()}
	var schemaError *jsonschema.SchemaValidationError
	if errors.As(err, &schemaError) {
		base, _ := decodePointer(pointerFromURL(schemaError.URL))
		var validation *jsonschema.ValidationError
		if errors.As(schemaError.Err, &validation) {
			deepest := deepestInstanceLocation(validation)
			problem.Location = pointer(append(base, deepest...))
		} else {
			problem.Location = pointer(base)
		}
	}
	return &ProfileError{Problems: []Problem{problem}, cause: err}
}

func deepestInstanceLocation(root *jsonschema.ValidationError) []string {
	best := append([]string(nil), root.InstanceLocation...)
	for _, cause := range root.Causes {
		candidate := deepestInstanceLocation(cause)
		if len(candidate) > len(best) || len(candidate) == len(best) && pointer(candidate) < pointer(best) {
			best = candidate
		}
	}
	return best
}
