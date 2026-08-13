# Annex — Runtime adapters (non-normative, draft)

**Version:** v1
**Status:** Draft
**Revision:** 2026-08-13
**Normative status:** Non-normative

This annex describes optional bindings between engram and agent runtimes. An
agent with filesystem tools can read a snapshot directly by entering through
its root README; no adapter or memory-serving layer is required. Persistent
writes delegate to the normative managed-writer contract in the Git annex.
Adapters add discovery, trusted skill packaging, lifecycle coordination, and
feedback, but no store semantics.

Discovery never establishes trust or authority. An adapter that packages the
canonical skills obtains them independently of the store, verifies a digest
from an independently trusted distribution channel (for example, a published
release), and installs byte-identical artifacts. In particular, the loaded
`using-engram` bootstrap must be trusted before it interprets store prose;
without that decision, core P0 leaves the discovered store read-only.

## 1. Integration surfaces

A runtime integration may provide any subset of these surfaces:

| Surface | Purpose | Authority |
|---|---|---|
| Attachment/adoption | Locate independent store roots through project `MEMORY.md` | Core §12; Git annex §6 |
| Canonical skills | Package the runtime-neutral operating disciplines | Skills annex |
| Managed-write binding | Coordinate a working draft, declare its initial candidate, and delegate preparation and acceptance | Git annex §§4–5 |
| Hook execution | Act as the one executor for `prepare-changeset` | Core §8 |
| Validation feedback | Return deterministic findings during editing and acceptance | Core §9 |

Each surface is optional. Installing skills or attachments does not make the
adapter a writer or executor.

## 2. Declarative project setup

An adapter MAY recognize a tracked project-root `engram.yaml` as portable setup
intent distinct from runtime attachment state. Version 1 has this strict shape:

```yaml
version: 1
harness: codex
attachments:
  - name: project
    url: git@github.com:example/project-memory.git
```

`harness` selects an adapter default and may be overridden by the invoking
host. Each unique portable `name` identifies one local materialization; the
single exact `url` supplies its fetch and push location. Credentials never
belong in the manifest.

The manifest describes desired repository sources, while core §12 `MEMORY.md`
describes resolved local store paths. An adapter may materialize declared
stores below project `.memory/<name>`, exclude the complete `.memory/` tree
from the consumer repository, and reconcile those paths into `MEMORY.md`.
Attachments outside that reserved namespace remain independently managed.

A manifest editor should be a separate, explicit, network-silent operation.
It should preserve unrelated document presentation where practical, validate
the complete result before atomic publication, and leave materialization and
runtime reconciliation to a subsequent explicit setup invocation. This keeps
versioned intent reviewable without turning a small configuration edit into an
implicit credential, network, deletion, or harness-installation request.

A setup invocation may grant bounded network and credential authority to
acquire a missing declared repository. Merely opening the project, loading the
manifest, or exposing `MEMORY.md` to an agent grants no such authority. A
conforming managed-store acquisition audits accepted history before making a
checkout visible and never executes store hooks.

Repeated setup should verify and reuse an exact existing checkout without
fetching. Synchronization remains explicit. Removing a declaration should
remove its attachment without deleting the independent checkout; destructive
cleanup needs separate authority. These boundaries keep setup convergent and
recoverable without turning repository configuration into an implicit delete,
pull, push, hook-trust, or memory-write request.

## 3. Managed-write binding

The managed store owns its repository worktree; an attached project only
discovers it. A write-capable adapter coordinates one bounded editor, stages
only the authorized logical operation, and passes the resulting index-declared
initial candidate to a conforming managed writer. That writer alone owns the stable
capture, disposable preparation, complete validation, commit creation,
compare-and-swap, reconciliation, and recovery protocol of the
[Git annex](annex-git.md#4-managed-transactions).

Normal file tools need no transaction handle. Early validation is useful while
the working draft is being assembled, but only validation of the prepared final
candidate inside the managed transaction decides acceptance. Rejection
discards disposable preparation while leaving the original draft available for
explicit correction and restaging.

A local commit neither fetches nor pushes. Network synchronization is a
separately authorized operation under the Git annex's synchronizer profile.

## 4. Runtime profiles

These profiles differ only in packaging and lifecycle hooks; they use the same
store and managed-write contracts:

| Runtime profile | Discovery | Skills | Write binding |
|---|---|---|---|
| Claude Code | A bounded `CLAUDE.md` block points to project `MEMORY.md` | Native Agent Skills through a plugin or project-scoped `.claude/skills/<slug>/SKILL.md` | Ordinary file tools assemble the working draft; the adapter stages its owned paths and delegates one managed commit |
| Codex / AGENTS.md-first | A bounded `AGENTS.md` block points to project `MEMORY.md` | Skills-only plugin or digest-checked `.agents/skills/<slug>/SKILL.md` vendoring | A lifecycle-aware harness records its owned edits, stages them, and delegates one managed commit |
| Generic filesystem runtime | Host-supplied attachment or direct discovery of `.engram/root.yaml` | Plain Markdown skills when supported; otherwise the core Agent Protocol, optionally copied into the root README | Filesystem editing is sufficient only when a conforming managed writer owns acceptance |

An adapter exposes the project attachment registry through one runtime-native
entrypoint; it does not duplicate the store list there. The independently
trusted adapter may mention and install `using-engram`, which routes to the
other canonical skills, but the registry cannot bootstrap trust in that copy.
A store may serve several unrelated projects, but they still refer to the
independently owned store checkout.

## 5. Operational guidance

- Filesystem tools remain the primary data interface; adapters need not proxy
  ordinary reading, search, or editing through another protocol.
- Runtime automation after acceptance—such as rebuilding an external index or
  publishing a mirror—is outside `prepare-changeset`. Pre-acceptance code must
  not publish a candidate that may still be rejected.
- Deployments that partition stores by writability enforce that policy with
  host permissions or mounts. Engram defines no ACL system.
- Unattended integrations surface validation, hook, and recovery failures on a
  channel a human or controller observes.

## 6. Normative roles an adapter may assume

An adapter that accepts writes assumes every managed-writer obligation in Git
annex §4; one that runs hooks also assumes every executor obligation in core
§8.5. An adapter that only installs skills or attachments assumes neither role
and must only avoid misrepresenting conformance (core §1.4).
