# engram — Agent Entrypoint

**Purpose:** this repository hosts the engram standard: the normative
spec (`docs/spec/`), the canonical base-profile schemas
(`profiles/base/`), the example stores (`examples/`), and — later — the
`engram` reference CLI (Go).

**Boundaries:** the spec is the authority; the CLI is a non-normative
reference implementation. Never add references to a specific CLI
command or implementation to the normative sections of `docs/spec/`.
Runtime-integration material (CLI bindings, plugins, MCP) lives in
`docs/spec/annex-adapters.md`, which is explicitly non-normative.

## Layout

- `docs/spec/README.md` — core specification (normative, RFC 2119)
- `docs/spec/annex-*.md` — annexes, independently versioned
- `docs/rationale.md` — non-normative reasoning and evidence
- `profiles/base/schemas/*.md` — canonical schema files; the annex
  declares them normative artifacts
- `examples/minimal/` — smallest conforming store; must stay conforming

## Constraints

- Spec text uses RFC 2119 keywords and references no implementation.
- Normative changes update the revision date and `CHANGELOG.md`
  (see `CONTRIBUTING.md`).
- `profiles/base/schemas/` and the formats defined in the core spec
  must not drift apart: a change to the schema-file format lands in the
  same commit as the updated canonical schemas and examples.
- Future CLI: Go, dependencies limited to the stdlib,
  `gopkg.in/yaml.v3`, and `github.com/santhosh-tekuri/jsonschema`.
- `examples/` must always conform to the spec at HEAD. Until the CLI
  exists this is enforced by review; afterwards by `engram check` in CI.
