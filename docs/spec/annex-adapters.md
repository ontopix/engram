# Annex — Runtime adapters (non-normative, draft)

**Version:** v1
**Status:** Draft
**Revision:** 2026-08-06
**Normative status:** Non-normative

How engram stores plug into concrete agent runtimes. Nothing here adds
conformance rules: a snapshot is self-describing (core D2), so the
universal read integration — an agent with filesystem tools reading the
root README and following the Agent Protocol it carries — needs no
adapter. Persistent writes use the normative Git annex; adapters remove
friction around attachment, working drafts, staging, commits, and
feedback. Discovery is not a trust decision: without an independently
trusted `using-engram` skill or an equivalent host decision, core P0
leaves the store read-only.

The design bets that runtimes are already good at files: models are
heavily post-trained on filesystem tools, which is precisely why the
store is files. Adapters therefore add *bindings*, never a required
access layer.

## 1. The integration surface

Every runtime integration is some subset of:

| Surface | What it does | Backing |
|---|---|---|
| Attachment/adoption block | Tells a project where independent stores are | Core §12, Git annex §6 |
| Skills | Installs trusted copies of the canonical disciplines | Skills annex |
| Working-draft binding | Coordinates one editor and stages its initial candidate | Git annex §5 |
| Commit binding | Materializes, prepares, validates, and accepts one commit | Git annex §4 |
| Hook binding | Runs `prepare-changeset` once inside the commit attempt | Core §8 |
| Validation feedback | Puts check findings in the agent's loop | Core §9 |

## 2. Git binding (runtime-agnostic)

A managed store owns its Git worktree; a consumer project merely
attaches it. The normal agent-write path deliberately follows familiar
Git staging while keeping acceptance stronger than a raw commit:

1. when separately authorized, synchronize the clean store before
   editing;
2. coordinate one automated editor for the checkout and let it assemble
   the bounded operation in the working tree;
3. stage only the authorized logical paths, making the index the
   initial candidate and leaving unrelated unstaged bytes outside it;
4. at commit time, resolve the accepted branch, acquire the shared ref
   and worktree locks, then capture `HEAD` and the resolved index;
5. verify the accepted lineage, materialize the initial candidate away
   from the live worktree, and complete the boundary preflight;
6. obtain the applicable `prepare-changeset` programs from base and
   check local trust for their exact bytes;
7. run the core §8 protocol and complete validation; and
8. create one commit, compare-and-swap the accepted ref, and reconcile
   the index and visible checkout only when safe.

A local commit does not fetch or push. One physical store attached to
several projects still has one accepted branch and repository worktree,
so projects coordinate their complete edit-and-commit operations.
Independent concurrent writers use separate clones or worktrees and
integrate their commits later.

A local `pre-commit` launcher inside the memory repository can guard the
managed boundary, but it cannot own the whole transaction: it returns
before Git creates the commit and therefore cannot retain the writer
lock through compare-and-swap or perform post-update reconciliation.
The launcher should consequently be a minimal POSIX `sh` wrapper that
delegates to a private entrypoint of the managed-store engine and rejects
an ordinary raw Git commit with guidance to use that engine. It does not
prepare hooks or modify the index or worktree. Git hook installation is
a convenience guard rather than a security boundary: bypassed hooks,
amend, merge, and lower-level ref manipulation remain outside the
supported write path. Because installation is repository-local state,
store creation and clone tooling can install the launcher automatically
and diagnostic tooling can report drift; no logical store file declares
it.

There is no core post-commit event. Rebuilding indexes or publishing
mirrors is runtime automation outside the preparation protocol, because
a program run before acceptance must not publish a candidate that may
still be rejected.

The managed commit engine uses repository plumbing without invoking the
`pre-commit` guard. It alone owns preparation, commit creation,
compare-and-swap, and reconciliation for the attempt. Hook programs
remain stored with, and specific to, the memory repository.

## 3. Claude Code

Packaged as a **plugin** (planned: `adapters/claude-code/`):

- **Skills** — the canonical skills, compiled in. Claude Code
  consumes the Agent Skills format natively; `using-engram` plays the
  session-start orientation and trust-bootstrap role there. Its source
  is verified independently of any store it evaluates. Store-list
  injection overlaps the session-start hook below — an adapter
  implements one of the two, not both.
- **Writes** — an authorized write skill coordinates one editor for the
  checkout, performs the bounded filesystem operation in its working
  draft, stages only its paths, and requests one managed commit.
  Per-write callbacks may provide early validation feedback but are not
  acceptance boundaries. A session-start hook may inject the attachment
  list, so entry through the maps (P1) happens without prompting.
- **Filesystem only.** Claude Code's own file tools are the intended
  interface; the store is designed for exactly them and exposes no
  separate memory-serving protocol.

The adapter packages the canonical `skills/<slug>/SKILL.md` artifacts
without changing their bytes. A project-scoped direct installation uses
`.claude/skills/<slug>/SKILL.md`; distribution may instead use Claude
Code's plugin mechanism. Ownership and updates belong to that adapter or
package manager, not to a store or its content.

## 4. Codex and AGENTS.md-first runtimes

- The **adoption block** in `AGENTS.md` (core §12) is the load-bearing
  discovery piece: it names the store roots and points at their READMEs,
  but does not transfer repository ownership or establish trust or
  authorization. A store may be shared by several unrelated projects.
- Runtimes with Agent Skills support get the same canonical skills by
  digest-checked vendoring or a skills-only plugin. A project-scoped
  Codex installation uses `.agents/skills/<slug>/SKILL.md`. The trusted
  `using-engram` copy establishes the operating mode before store prose
  is followed.
- Runtimes without skills support still work: the root README's
  `## Agent Protocol` section (core Appendix A.1) is deliberately
  sufficient as an operational fallback, and it is why the skeleton
  includes it. Because that reminder comes from the store itself, it
  cannot establish trust in its own source; absent an equivalent host
  decision, the fallback remains read-only under P0.
- A harness with multi-call lifecycle support coordinates one bounded
  working draft, records which logical edits it owns, stages those edits,
  and then submits the initial candidate to the managed-store engine for
  preparation and acceptance. The normal file tools need no transaction
  handle.
- Validation feedback remains useful while editing, but the definitive
  state and transition check runs against the final candidate
  immediately before the commit.

Each attempt designates one executor. The managed commit engine owns hook
execution and commit creation; the local Git guard does not prepare the
candidate. A rejected disposable preparation is discarded while the
original working draft remains available for explicit correction and
restaging.

## 5. Unattended and multi-agent operation

Patterns for schedulers and fleets (all optional):

- **Concurrency:** one visible checkout has one automated draft writer,
  and acceptance against its ref uses one writer lock. Distinct stores,
  clones, or worktrees can draft and prepare independently. Divergent
  clones replay and revalidate changes instead of introducing merge
  commits into the accepted lineage. The executor is per attempt and
  candidate, not a global singleton.
- **Write partitioning:** deployments that split stores by writability
  (for example, episodic agent-writable and semantic human-curated)
  enforce it with mounts or permissions. The spec deliberately owns no
  ACLs; infrastructure does it better.
- **Failure channels:** unattended runs report check failures somewhere
  a human reads; a hook or validation failure that disappears inside a
  cron job protects nothing.

## 6. Adapter conformance note

An adapter that accepts writes is a managed writer (Git annex §4), and
an adapter that runs hooks is an **executor** (core §8.5). It therefore
owes working-draft ownership, declared-change isolation, ref-race
rejection, base/candidate mapping, trust checking, invocation and
ordering boundaries, candidate import, single execution, and final
validation. An adapter that only installs skills or attachments owes
nothing beyond not misrepresenting conformance (core §1.4).
