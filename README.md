# engram

A filesystem-native memory standard for AI agents.

An **engram store** is a directory tree of markdown records that an
agent can read, write, navigate, and validate — with no database, no
embedding pipeline, and no runtime lock-in. The tree is self-describing:
every directory documents itself, every record declares its type, every
type is defined by a schema that lives in the tree, and a deterministic
validator can prove the whole store consistent.

- **Spec:** [docs/spec/README.md](docs/spec/README.md)
- **Rationale:** [docs/rationale.md](docs/rationale.md) — why files, why
  no graph database, what the evidence says
- **Base profile:** [docs/spec/annex-base-profile.md](docs/spec/annex-base-profile.md)
  — optional, versioned vocabulary of common entity types
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
forbidden. Integrity is enforced at changeset boundaries by declarative
hooks (`fix` → `gate` → `derive`) and a deterministic `check` with
stable error codes. Schema-level policies (`immutable`, `append-only`)
make guarantees like an append-only journal mechanical rather than
aspirational.

## What this repository contains

| Path | Content |
|---|---|
| `docs/spec/` | The normative specification (core + annexes) |
| `docs/rationale.md` | Non-normative: the reasoning and the evidence |
| `profiles/base/` | Canonical schema files of the base profile |
| `examples/minimal/` | The smallest conforming store |
| `cmd/`, `internal/` | Reference CLI (`engram`), Go — not yet started |

## Status

Pre-release draft. The specification is being written against its first
adopter ([cortex](https://github.com/apuigsech/cortex), a personal
second-brain system); nothing is stable until v1 is declared.

Planned publication: `engram.ontopix.ai`.

## Roadmap

1. **Spec v1 draft** — this repository, now.
2. **Reference CLI, read-only core** — `engram check` (the full E/W
   catalog), `engram init`.
3. **Write tooling** — `engram new`, `engram mv` (inbound-link rewrite),
   `engram fmt` (catalog regeneration).
4. **Changeset machinery** — `engram hooks run`, the git binding
   (pre-commit / post-commit).
5. **Profiles** — `engram profile add|upgrade`, base profile installable.
6. **Skills + adapters** — canonical skills shipped, `engram sync claude`
   / `engram sync codex` materialization.
7. **Optional MCP server** — `engram mcp` for runtimes that prefer tools
   over filesystem access.

## Relationship to the `.agents/` standard

[`.agents/`](https://github.com/apuigsech/dot-agents) organizes a
*repository's* agent-facing content (skills, tasks, volatile work
memory, stable knowledge). engram norms a *memory store* — the thing
`.agents/` §3.1 explicitly excludes from its own `memory/` pillar. They
compose: an engram store can live inside a repository that adopts
`.agents/`, or stand entirely alone.

## License

MIT — see [LICENSE](LICENSE).
