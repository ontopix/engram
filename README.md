# engram

A filesystem-native memory standard for AI agents.

An **engram snapshot** is a portable directory tree of markdown records
that an agent can read, navigate, and validate — with no database or
embedding pipeline. A writable **managed store** gives those snapshots
an independent Git history and accepts each logical memory operation as
one validated commit. The tree is self-describing: every directory
documents itself, every record declares its type, every type is defined
by a schema in the tree, and a deterministic validator can prove it
consistent.

- **Spec:** [docs/spec/README.md](docs/spec/README.md)
- **Managed-store Git binding:**
  [docs/spec/annex-git.md](docs/spec/annex-git.md) — accepted states,
  working drafts, staged candidates, commits, concurrency, and attachment
- **Reference CLI contract:** [docs/cli/README.md](docs/cli/README.md)
- **Implementation plan:**
  [docs/implementation-plan.md](docs/implementation-plan.md)
- **Rationale:** [docs/rationale.md](docs/rationale.md) — why files, why
  no graph database, what the evidence says
- **Curated schemas:** [schemas/README.md](schemas/README.md) — optional,
  ready-to-copy vocabulary of common entity types
- **Skills:** [docs/spec/annex-skills.md](docs/spec/annex-skills.md) —
  the operating discipline, packaged for agents
- **Runtime adapters:** [docs/spec/annex-adapters.md](docs/spec/annex-adapters.md)
  — Claude Code, Codex, and generic integration

## Design in one paragraph

Files are the source of truth; every index is derived and rebuildable.
The store is navigated top-down through per-directory `README.md` files
whose one-line descriptions form a lazy-loading catalog — an agent reads
the map, not the territory. Types are defined by schema files (JSON
Schema in frontmatter + placement criteria in prose) scoped lexically:
a type defined in a directory is valid in that subtree, shadowing is
forbidden. Portable snapshots remain plain files; writable managed
stores use their own Git worktree, with accepted memory at `HEAD` and
each logical write accepted as one validated commit. Integrity is
enforced at changeset boundaries by declarative transition rules and a
deterministic `check` with cataloged error codes. Optional
`prepare-changeset` scripts transform disposable candidates before
acceptance. Schema-level policies (`immutable`, `append-only`) make
guarantees like an append-only journal mechanical rather than
aspirational.

## What this repository contains

| Path | Content |
|---|---|
| `docs/spec/` | Core specification and independently versioned annexes; each document declares its normative status |
| `docs/cli/` | Non-normative reference CLI contract |
| `docs/implementation-plan.md` | Phased Go CLI implementation and release gates |
| `docs/rationale.md` | Non-normative: the reasoning and the evidence |
| `schemas/` | Non-normative curated schema inventory |
| `examples/minimal/` | A small conforming portable snapshot |
| `skills/` | Canonical runtime-neutral Agent Skills artifacts (planned in implementation milestone M0) |
| `cmd/`, `internal/` | Reference CLI (`engram`), Go — not yet started |

## Status

Pre-release draft. The v1 specification is functionally closed and ready
to implement against its first adopter
([cortex](https://github.com/apuigsech/cortex), a personal second-brain
system); nothing is stable until v1 is declared.

Planned publication: `engram.ontopix.ai`.

## Roadmap

- **Now:** build the conformance harness, portable snapshot checker,
  changeset engine, and read-only managed-history commands.
- **Next:** add draft/staging helpers and the locked Go acceptance engine,
  with a minimal POSIX `sh` guard for raw Git commits.
- **Later:** add linear pull/push replay, operational diagnostics,
  skills-only adapter packaging, and the v1 release gates.

The executable milestones, dependencies, tests, and exit criteria are
maintained in the [implementation plan](docs/implementation-plan.md).

Engram exposes no memory-serving protocol: agents and adapters interact
with stores directly through filesystem tools.

## Relationship to the `.agents/` standard

[`.agents/`](https://github.com/apuigsech/dot-agents) organizes a
*repository's* agent-facing content (skills, tasks, volatile work
memory, stable knowledge). engram norms a *memory store* — the thing
`.agents/` §3.1 explicitly excludes from its own `memory/` pillar. They
compose: a project adopting `.agents/` attaches one or more independent
engram stores by path. A deliberately nested checkout is ignored by the
outer repository or represented explicitly as a submodule; attachment
does not merge repository ownership.

## License

MIT — see [LICENSE](LICENSE).
