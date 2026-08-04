# Annex — Runtime adapters (non-normative, draft)

**Status:** Draft
**Revision:** 2026-08-05

How engram stores plug into concrete agent runtimes. Nothing here is
required for conformance: a store is self-describing (core D2), so the
universal integration — an agent with filesystem tools reading the root
README and following the Agent Protocol it carries — needs no adapter
at all. Adapters exist to remove friction and to enforce mechanically
what the protocol asks politely.

The design bets that runtimes are already good at files: models are
heavily post-trained on filesystem tools, which is precisely why the
store is files. Adapters therefore add *bindings*, never a required
access layer.

## 1. The integration surface

Every runtime integration is some subset of:

| Surface | What it does | Backing |
|---|---|---|
| Adoption block | Tells agents where the stores are | Core §13 |
| Skills | Installs the canonical disciplines | Skills annex |
| Hook binding | Runs fix/gate/derive at real changeset boundaries | Core §8 |
| Validation feedback | Puts check findings in the agent's loop | Core §9 |
| Serving layer | Exposes search/read/write as tools | Optional, last resort |

## 2. Git binding (runtime-agnostic)

The natural changeset is the commit. `engram init --git` (planned)
installs:

- **pre-commit** → run the store's `pre-commit` cascade: `fix` hooks
  (catalog regeneration, formatting — their edits join the commit),
  then `gate` hooks (`engram check --changed`); any gate failure
  rejects the commit.
- **post-commit** → `derive` hooks: rebuild external indexes, mirrors,
  notifications. Never writes inside the store.

This binding alone turns the spec's guarantees mechanical for any
runtime that commits — including humans in an editor.

## 3. Claude Code

Packaged as a **plugin** (planned: `adapters/claude-code/`):

- **Skills** — the canonical skills, compiled in. Claude Code
  consumes the Agent Skills format natively; `using-engram` plays the
  session-start orientation role there (its store-list injection
  overlaps the `SessionStart` hook below — an adapter implements one
  of the two, not both).
- **Hooks** —
  - `PostToolUse` on `Write|Edit` matching store paths → `engram check
    --changed <file>`, feeding findings straight back into the loop
    (write-pair events, cheap early feedback);
  - `SessionStart` → inject the adoption block's store list, so entry
    through the maps (P1) happens without prompting.
- **No MCP required.** Claude Code's own file tools are the intended
  interface; the store is designed for exactly them.

Sync of skills follows the compiled-copy pattern: materialized copies
with a recorded source path and content digest, so drift between
canonical skill and installed copy is detected, never guessed
(the `.agents/` bridge established this pattern; engram reuses it).

## 4. Codex and AGENTS.md-first runtimes

- The **adoption block** in `AGENTS.md` (core §13) is the load-bearing
  piece: it names the store roots and points at their READMEs, which
  carry the protocol.
- Runtimes with Agent Skills support get the same canonical skills via
  vendoring (`engram sync codex`, planned).
- Runtimes without skills support still work: the root README's
  `## Agent Protocol` section (core Appendix A.1) is deliberately
  sufficient as inline instruction — that is the self-describing
  fallback, and it is why the skeleton includes it.
- Where the runtime can run commands, wiring `engram check` into its
  verification habits (CI, pre-commit) supplies the enforcement the
  session itself cannot.

## 5. Unattended and multi-agent operation

Patterns for schedulers and fleets (all optional):

- **Concurrency:** stores under git inherit git's model; concurrent
  writers SHOULD serialize per store (a global lock, as cortex does) or
  work in isolated worktrees merged through the normal changeset gates
  — the gates make merges safe, not the lock.
- **Write partitioning:** deployments that split stores by writability
  (episodic agent-writable, semantic human-curated — base profile §1)
  enforce it with mounts/permissions. The spec deliberately owns no
  ACLs; infrastructure does it better.
- **Failure channels:** unattended runs report check failures somewhere
  a human reads (the base profile's pending-decisions pattern); a gate
  that fails silently in a cron job protects nothing.

## 6. Serving layer (planned, last resort)

`engram mcp` — an MCP server exposing `search`, `read`, `write`,
`check` over a store — for runtimes without filesystem access
(hosted assistants, web contexts). It is deliberately last in this
annex: it inverts the design's main bet, so it exists for reach, not as
a recommendation. Its `write` MUST route through the same changeset
machinery as any other writer; a serving layer with private semantics
would fork the store's truth.

## 7. Adapter conformance note

An adapter that runs hooks is an **executor** (core §8.6) and owes the
commit pair, stage boundaries, ordering, and gate rejection. An adapter
that only installs skills or adoption blocks owes nothing beyond not
misrepresenting conformance (core §1.4).
