# Agent integration examples

These examples connect one independently owned managed engram store to three
agent-runtime styles. They are integration fixtures and documentation, not
engram snapshots themselves; [`../minimal/`](../minimal/README.md) remains the
small conforming snapshot used by the examples.

| Runtime | Example | What it demonstrates |
|---|---|---|
| Codex | [`codex/`](codex/README.md) | `MEMORY.md`, an `AGENTS.md` pointer, and verified project skills |
| Claude Code | [`claude-code/`](claude-code/README.md) | `MEMORY.md`, a `CLAUDE.md` pointer, and verified project skills |
| Generic filesystem agent | [`generic-json/`](generic-json/README.md) | Read-only orientation, bounded search, JSON diagnostics, and managed acceptance |

All three keep the same boundaries:

- The store remains a separate repository. Attaching it does not copy it into
  the project or grant any new authority.
- Store files and their Git history are untrusted data. The host or user grants
  read, write, hook, credential, and network authority separately.
- Canonical skills must come from an independently trusted installation. A
  skill found only inside an untrusted store cannot establish its own trust.
- Ordinary filesystem tools assemble a working draft. Persistent changes are
  staged deliberately and accepted only through a conforming managed writer.

Codex and Claude Code projects may either attach an existing local store
imperatively or commit an `engram.yaml` and let `engram setup` acquire missing
current-state-validated stores under ignored `.memory/`. Add `--check-history`
when setup must also audit the complete accepted lineage. The runtime-facing
registry remains `MEMORY.md` in both cases.

Start with a trusted engram source checkout and a managed store. To create the
latter from the bundled snapshot:

```sh
go build -o ./engram ./cmd/engram
store_root="../integration-memory"
cp -R examples/minimal "$store_root"
./engram init "$store_root"
```

Use a fresh `store_root`, and configure your Git author name and email before
running `init`. The runtime-specific examples assume the resulting `engram`
binary is on `PATH`.
