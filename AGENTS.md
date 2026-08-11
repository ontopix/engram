# engram — Agent Entrypoint

**Purpose:** this repository hosts the engram standard: the normative
core spec and its independently statused annexes (`docs/spec/`), the
reference CLI contract (`docs/cli/`), the curated schema inventory
(`schemas/`), the example snapshots (`examples/`), and the `engram` Go
reference CLI.

**Boundaries:** the spec is the authority; the CLI is a non-normative
reference implementation. Never add references to a specific CLI
command or implementation to normative `docs/spec/` sections. The
normative Git annex defines managed-store semantics without prescribing
CLI commands. The core Agent Protocol is the sole source of agent
obligations; canonical skills only teach that protocol. Filesystem
runtime bindings and plugin guidance live in
`docs/spec/annex-adapters.md`, which is explicitly non-normative; the
reference command surface lives in `docs/cli/README.md`.

## Layout

- `docs/spec/README.md` — core specification (normative, RFC 2119)
- `docs/spec/annex-*.md` — independently versioned and statused annexes
- `docs/spec/annex-git.md` — normative managed-store Git binding
- `docs/spec/annex-skills.md` — non-normative canonical agent workflows
- `docs/spec/annex-adapters.md` — non-normative runtime integration
- `docs/cli/README.md` — non-normative reference CLI contract
- `docs/implementation-plan.md` — non-normative phased implementation
  roadmap and release gates
- `docs/rationale.md` — non-normative reasoning and evidence
- `schemas/*.md` — non-normative curated schemas; `note.md` mirrors the
  normative core skeleton
- `skills/*/SKILL.md` — canonical runtime-neutral skill artifacts
- `examples/minimal/` — small conforming snapshot; must stay conforming

## Constraints

- Normative text uses RFC 2119 keywords. The Git annex may norm Git
  semantics, but normative documents never depend on reference CLI
  commands, Go packages, or one product implementation.
- Keep managed-write terminology exact: worktree edits are a **working
  draft**; the staged/index tree is the **initial candidate**;
  hook output is the **final candidate**; a **transaction** is the
  one-shot acceptance attempt, never an editing session or CLI handle.
- Normative changes update the revision date and `CHANGELOG.md`
  (see `CONTRIBUTING.md`).
- `schemas/` and the formats defined in the core spec must not drift
  apart: a change to the schema-file format lands in the same commit as
  the updated curated schemas and examples. `schemas/note.md` remains
  byte-identical to core Appendix A.3.
- Reference CLI: Go, direct package dependencies limited to the stdlib,
  `go.yaml.in/yaml/v3`,
  `github.com/santhosh-tekuri/jsonschema/v6`, and
  `github.com/yuin/goldmark`; versions and transitive modules are pinned
  by `go.mod`/`go.sum`. Git integration invokes the system Git executable
  rather than embedding another Git implementation.
- `examples/` must always conform to the spec at HEAD, enforced by
  `engram check` in CI and by review of integration-only examples.
- Commits in this repo are unsigned (`commit.gpgsign false`, local
  config): the owner's global git signs via 1Password, whose agent does
  not respond from unattended agent sessions. Do not flip it back;
  Albert re-signs by rebase if it ever matters.
