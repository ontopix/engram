// Package checker implements the portable engram snapshot and changeset
// validation functions. It has no dependency on Git, the CLI, processes, or
// network access.
package checker

import (
	"bytes"
	"sort"
)

type Target string

const (
	TargetSnapshot     Target = "snapshot"
	TargetChangeset    Target = "changeset"
	TargetManagedState Target = "managed-state"
	TargetManagedStore Target = "managed-store"
)

type Status string

const (
	StatusComplete      Status = "complete"
	StatusIndeterminate Status = "indeterminate"
)

// Finding is the protocol representation of a normative finding. Detail is
// intentionally excluded from identity and can vary between implementations.
type Finding struct {
	Code   string `json:"code"`
	Path   string `json:"path"`
	Detail string `json:"detail,omitempty"`
}

// Result is the portable validation result shared by the CLI protocol.
type Result struct {
	Target   Target    `json:"target"`
	Status   Status    `json:"status"`
	Findings []Finding `json:"findings"`
}

// HasErrors reports whether at least one E-class finding is present. Warnings
// alone do not make a completed operation an issues outcome.
func (r Result) HasErrors() bool {
	for _, finding := range r.Findings {
		if len(finding.Code) != 0 && finding.Code[0] == 'E' {
			return true
		}
	}
	return false
}

type findingSet map[[2]string]Finding

func (s findingSet) add(code, path, detail string) {
	key := [2]string{code, path}
	if existing, ok := s[key]; ok {
		if existing.Detail == "" {
			existing.Detail = detail
			s[key] = existing
		}
		return
	}
	s[key] = Finding{Code: code, Path: path, Detail: detail}
}

func (s findingSet) sorted() []Finding {
	result := make([]Finding, 0, len(s))
	for _, finding := range s {
		result = append(result, finding)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Path != result[j].Path {
			return bytes.Compare([]byte(result[i].Path), []byte(result[j].Path)) < 0
		}
		return result[i].Code < result[j].Code
	})
	return result
}

// CapabilityError means the requested conformance result cannot honestly be
// produced by this build or for the declared specification version.
type CapabilityError struct {
	Message string
}

func (e *CapabilityError) Error() string { return e.Message }
