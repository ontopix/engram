# engram

A filesystem-native memory standard for AI agents.

An engram store is a self-describing tree of markdown records. Each
portable snapshot can be read and validated with ordinary file tools; a
writable store uses an independent Git history so each accepted memory
change is one validated commit.

## Repository map

| Path | Purpose |
|---|---|
| [`docs/spec/README.md`](docs/spec/README.md) | Normative core: snapshot format, validation, transitions, and Agent Protocol |
| [`docs/spec/annex-git.md`](docs/spec/annex-git.md) | Normative Git binding for writable managed stores |
| [`docs/cli/README.md`](docs/cli/README.md) | Non-normative reference CLI contract |
| [`docs/implementation-plan.md`](docs/implementation-plan.md) | Phased implementation roadmap |
| [`docs/rationale.md`](docs/rationale.md) | Design reasoning and tradeoffs |
| [`schemas/`](schemas/README.md) | Optional curated record types |
| [`docs/spec/annex-skills.md`](docs/spec/annex-skills.md) | Canonical agent workflows |
| [`docs/spec/annex-adapters.md`](docs/spec/annex-adapters.md) | Runtime integration guidance |
| [`examples/minimal/`](examples/minimal/README.md) | Small conforming snapshot |

## Design

- Files are authoritative; indexes and caches are rebuildable.
- Every directory has a README map, every record declares a type, and
  every type is defined by a schema in the tree.
- Agents navigate by descriptions and load record content only when it
  is relevant.
- Deterministic checks protect snapshot and transition integrity.
- Git is required only for accepted writable history, not for reading a
  portable snapshot.

The standard exposes no memory-serving protocol. Agents and adapters use
normal filesystem tools.

## Status

The v1 design is ready for implementation but remains a pre-release
draft; no interface is stable until v1 is declared. The executable
sequence and release gates live in the
[implementation plan](docs/implementation-plan.md).

Engram complements the [`.agents/` standard](https://github.com/apuigsech/dot-agents):
projects can attach independent engram stores without merging their Git
ownership or histories.

Planned publication: `engram.ontopix.ai`.

## License

MIT — see [LICENSE](LICENSE).
