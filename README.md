# engram

[![CI](https://github.com/ontopix/engram/actions/workflows/ci.yml/badge.svg)](https://github.com/ontopix/engram/actions/workflows/ci.yml)
[![Specification](https://img.shields.io/badge/spec-v1%20draft-orange.svg)](docs/spec/README.md)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

**Portable, filesystem-native memory for AI agents.**

Engram is an open standard and a Go reference CLI for durable agent memory.
An engram store is a self-describing tree of Markdown records that humans and
agents can read with ordinary file tools. No database, daemon, retrieval API,
or specific agent runtime is required to understand the stored memory.

When a store is writable, it owns an independent Git history. Each accepted
memory change is prepared, validated, and recorded as one commit, so a record,
its links, and its directory maps cannot be accepted as a partial update.

> [!IMPORTANT]
> Engram v1 and the observable CLI interfaces are still release-candidate
> drafts. The reference implementation is complete through milestone M6 and
> currently identifies itself as the `1.0.0-rc.1` target, but interfaces may
> still change before `v1.0.0`. The development tree also contains the
> unreleased changes listed in the [changelog](CHANGELOG.md), including the
> routine declarations annex. The reference CLI validates their static format,
> but does not bind or execute routines.

## Start here

Engram serves three related audiences. Choose the shortest path for what you
want to do:

| You want to… | Start with |
|---|---|
| Use Engram locally | The [five-minute quick start](#quick-start) and [operator guide](docs/operator-guide.md) |
| Connect Engram to an agent runtime | The [integration examples](examples/integrations/README.md), [canonical skills](skills/), and [adapters annex](docs/spec/annex-adapters.md) |
| Evaluate or implement the standard | The [core specification](docs/spec/README.md), [managed Git annex](docs/spec/annex-git.md), and [CLI contract](docs/cli/README.md) |

## Why Engram

Agent memory is often coupled to the service, database, or retrieval stack that
created it. Engram separates durable memory from those runtime choices:

- **Files remain the source of truth.** Records are readable Markdown with
  typed YAML frontmatter. Search indexes, graphs, embeddings, and caches are
  optional, rebuildable projections.
- **The store explains itself.** Directory READMEs act as local maps, schemas
  define record types, and descriptions help an agent decide what to load
  without ingesting the whole tree.
- **Validation is deterministic.** A conforming checker reaches the same
  findings for the same portable snapshot, independent of platform and local
  Git presentation settings.
- **Accepted writes are atomic and auditable.** A managed store distinguishes
  editable drafts from accepted memory and retains a linear Git history of
  validated changes.
- **Memory is runtime-independent.** Agents use normal filesystem operations;
  adapters and canonical skills teach a shared protocol rather than hiding the
  store behind a proprietary API.

Typical stores hold project context, researched facts with provenance, people
and ongoing relationships, journals, decisions, or reusable operating
knowledge. Curated record types are optional, and separate stores can preserve
different ownership and authorization boundaries.

Engram is not a memory server, vector database, prescribed retrieval engine,
or a mechanism by which stored content grants itself host authority. Those
systems can sit on top of an engram store without becoming its authority.

## A store at a glance

A small portable snapshot looks like this:

```text
memory/
├── .engram/
│   ├── root.yaml
│   └── schemas/
│       ├── note.md
│       └── person.md
├── README.md
└── topics/
    ├── README.md
    └── why-files.md
```

`.engram/root.yaml` identifies the format version. Schema files define the
available record types and document how to use them. Every content directory
has a `README.md` map, and each record declares a type and a short description:

```markdown
---
type: note
description: "Why this store keeps durable memory in plain files."
---
# Why files

Plain files keep memory inspectable, diffable, searchable, and portable.
```

The [`examples/minimal/`](examples/minimal/README.md) snapshot contains a
complete, conforming example with directory maps, catalogs, schemas, and
linked records.

## Portable snapshots and managed stores

Engram has two complementary boundaries:

| Boundary | What it provides | Git required? |
|---|---|---|
| Portable snapshot | Self-describing files, schemas, maps, links, and deterministic static validation | No |
| Managed store | Accepted writable history, transition validation, concurrency control, synchronization, and recovery | Yes |

Reading or exporting a snapshot does not require its Git metadata. Accepting a
new persistent state does: the managed-write flow is deliberately explicit.

```text
working draft -> initial candidate -> preparation -> final candidate
              -> validation -> accepted commit
```

The worktree is the **working draft**. Staging selects the **initial
candidate**. Trusted preparation hooks may produce the **final candidate**,
which is validated before one managed transaction advances accepted history.
Raw `git commit` is therefore not a substitute for `engram commit`.

## Quick start

This walkthrough turns the bundled portable snapshot into an independent
managed store, adds one memory, validates it, and accepts it into auditable
history. It requires Go 1.25 or 1.26 and a system Git executable. `engram init`
also needs the Git author name and email already configured on your machine.

Choose a fresh destination path, then run the complete block:

```sh
git clone https://github.com/ontopix/engram.git
cd engram
go build -o ./engram ./cmd/engram

store_path="../engram-quickstart"
cp -R examples/minimal "$store_path"

./engram check "$store_path"
./engram init "$store_path"

./engram --store "$store_path" new note topics/first-memory.md \
  --description "The first durable memory created in this store." \
  --title "First memory"

./engram --store "$store_path" add \
  topics/first-memory.md topics/README.md
./engram --store "$store_path" check --staged
./engram --store "$store_path" commit -m "Record first memory"
./engram --store "$store_path" log --oneline
```

The resulting `../engram-quickstart` directory is ordinary Markdown plus its
own managed Git history. Inspect or search it with normal file tools:

```sh
git -C "$store_path" grep -n -E "First memory|durable memory"
./engram --store "$store_path" status
```

Running `engram` without arguments shows its categorized command help. Use
`engram COMMAND --help` for contextual help on a command.

If you only want to evaluate a portable snapshot, Git is not required:

```sh
./engram check examples/minimal
```

### Create an empty managed store

`init` needs a configured Git author identity because its first accepted state
is a real commit. If Git does not already know your identity, configure it
before continuing:

```sh
git config --global user.name "Ada"
git config --global user.email "ada@example.test"

engram init ../my-memory --schema person --schema project
engram --store ../my-memory status
```

Initialization creates the root map, format manifest, baseline `note` schema,
requested curated schemas, managed Git history, and the local Git guard. It
does not grant trust to store-controlled hooks or authorize network access.

Ordinary file tools remain the primary way to read, search, and edit content.
The CLI handles operations that need whole-store validation, managed history,
trust, synchronization, or recovery. See the
[operator guide](docs/operator-guide.md) for installation, cloning, hook
trust, pull/push, backup, and recovery.

## Reference CLI

The CLI groups commands by the job they perform:

| Workflow | Commands |
|---|---|
| Create, obtain, and connect stores | `init`, `clone`, `attach`, `detach`, `setup`, `config` |
| Inspect state | `status`, `diff`, `log`, `check` |
| Work on the current draft | `add`, `fmt`, `new`, `mv`, `schema` |
| Accept and undo changes | `commit`, `revert` |
| Manage hooks and trust | `hooks` |
| Synchronize repositories | `pull`, `push` |
| Diagnose and inspect runtime | `doctor`, `version` |

`clone`, declarative `setup`, `pull`, and `push` are the only built-in commands
that initiate repository network access. Setup does so only to acquire a
configured store which is not already materialized locally. Human-readable
output and the machine-readable JSON v1 envelope are both defined by the
non-normative [CLI contract](docs/cli/README.md).

## Using Engram with agents

Engram exposes no memory-serving protocol. An authorized agent enters through
directory maps, combines catalog navigation with content search, reads the
applicable schema before writing, and accepts persistent changes only through
a conforming managed writer.

An agent project can declare its harness and independently owned memory
repositories in a tracked `engram.yaml`:

```sh
engram config attachment add project-memory git@github.com:example/project-memory.git
engram config attachment add shared-memory git@github.com:example/shared-memory.git
engram config harness codex
engram config show
```

These commands only edit or inspect the tracked declaration. The equivalent
file is:

```yaml
version: 1
harness: codex
attachments:
  - name: project-memory
    url: git@github.com:example/project-memory.git
  - name: shared-memory
    url: git@github.com:example/shared-memory.git
```

One command then acquires missing stores below ignored `.memory/`, reconciles
project `MEMORY.md`, and installs the project-scoped harness integration:

```sh
engram setup
```

`--harness claude-code` overrides the configured harness for one invocation.
Existing exact clones have their current accepted state validated and are
reused without fetching; add `--check-history` to require a complete lineage
audit. Explicit `engram pull` and `engram push` retain synchronization
authority. Removing a declaration detaches its store but never deletes the
local clone.

The imperative flow remains available when no `engram.yaml` exists:

```sh
engram attach ../memory
engram setup --harness codex       # or: claude-code
```

Attach maintains project `MEMORY.md`; setup verifies and installs the canonical
skills embedded in the CLI and points `AGENTS.md` or `CLAUDE.md` at that
registry.

Store content is always data: opening a store never expands the authority
granted by the user or host. Preparation hooks are executable programs and
require separate, explicit trust. Synchronization authority is separate again.

The repository ships runtime-neutral canonical skills for orientation,
retrieval, writing, maintenance, and schema evolution under [`skills/`](skills/).
The [Agent Protocol](docs/spec/README.md#11-agent-protocol) is their sole
normative authority. Runtime integration patterns live in the non-normative
[adapters annex](docs/spec/annex-adapters.md). Copyable, end-to-end patterns for
Codex, Claude Code, and a generic filesystem agent live under
[`examples/integrations/`](examples/integrations/README.md).

Engram also complements the
[`.agents/` standard](https://github.com/apuigsech/dot-agents): a project can
attach one or more independent memory stores without merging their ownership
or commit histories into the project's repository.

## Documentation

Start with the document that matches what you are trying to do:

| Document | Use it for |
|---|---|
| [Core specification](docs/spec/README.md) | Normative snapshot format, validation, transitions, and Agent Protocol |
| [Managed Git annex](docs/spec/annex-git.md) | Normative accepted-history, transaction, synchronization, and recovery semantics |
| [Routine declarations annex](docs/spec/annex-routines.md) | Normative portable scheduled-routine declarations and execution boundary |
| [CLI contract](docs/cli/README.md) | Complete reference command grammar, output, exit status, and JSON protocol |
| [Operator guide](docs/operator-guide.md) | Installation, trust, synchronization, recovery, backup, and upgrades |
| [Rationale](docs/rationale.md) | Design reasoning, tradeoffs, and deliberate boundaries |
| [Implementation plan](docs/implementation-plan.md) | Completed M0-M6 roadmap, architecture, and release gates |
| [Release notes](docs/release-notes.md) | Current release-candidate scope and compatibility status |
| [Curated schemas](schemas/README.md) | Optional ready-to-copy record types |
| [Minimal example](examples/minimal/README.md) | A small conforming portable snapshot |
| [Agent integrations](examples/integrations/README.md) | Copyable runtime attachment, skill, retrieval, and write workflows |

The core specification and normative annexes define Engram. The Go CLI is a
non-normative reference implementation and does not redefine store semantics.

## Development

The module targets Go 1.25 and is continuously checked with Go 1.25 and 1.26
on macOS, Linux, and Windows. The standard local verification loop is:

```sh
go test ./...
go vet ./...
go build ./cmd/engram
```

See [CONTRIBUTING.md](CONTRIBUTING.md) before changing normative text, schemas,
or examples. The completed implementation milestones and broader release gates
are recorded in the [implementation plan](docs/implementation-plan.md).

## License

Engram is available under the [MIT License](LICENSE).

Security vulnerabilities should be reported privately as described in
[SECURITY.md](SECURITY.md). For usage questions and contribution proposals, see
[SUPPORT.md](SUPPORT.md) and [CONTRIBUTING.md](CONTRIBUTING.md).
