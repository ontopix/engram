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

## Unattended capture

Setup installs discovery and canonical skills; it does not authorize recurring
writes. To enable unattended capture, the owner can adopt the following policy
in trusted project `AGENTS.md`, `CLAUDE.md`, or host instructions, outside the
setup-owned marker block. Replace `project-memory` with one actual attachment
and its intended content scope before adopting it. This example assumes one
automated editor per checkout; concurrent runs use separate worktrees or clones.

```markdown
For this project's authorized work, you may read the attached project-memory
store and persist project decisions, corrections, and reusable lessons there,
including their managed local commits, without asking again for each write.
At completed work steps, capture only new information useful to future work.
Check existing memory and update or link it instead of duplicating it; preserve
observed sources and distinguish facts from inferences. If nothing useful is
new, leave memory unchanged. Use the canonical write workflow and stage only
the intended operation. Other stores remain read-only under this policy.
This grant does not include hook execution, network synchronization, or new
credentials. Preserve unrelated drafts and report conflicts or failures in the
run result. If acceptance may already have occurred, inspect acceptance and
recovery evidence before any retry; do not repeat a possibly accepted write.
```

This is an example task authorization, not an additional Agent Protocol. The
host retains control over its scope and can revoke it. Recovery can be
separately authorized by the host; otherwise a recovery-required result is
reported to the controller. Scheduled execution, when needed, remains the
existing host's responsibility.
