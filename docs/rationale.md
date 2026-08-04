# Rationale

Non-normative. Why engram is shaped the way it is, and what the
evidence behind each bet looks like. Where this document and the spec
disagree, the spec governs.

## Why files

The 2024–2026 wave of agent-memory systems split into families:
memory-as-OS (Letta/MemGPT), extraction pipelines (Mem0), temporal
knowledge graphs (Zep/Graphiti, Cognee), linked-notes stores (A-MEM),
multi-strategy retrieval stacks (Hindsight). Against all of them, one
result kept repeating: **plain files operated with plain file tools are
embarrassingly competitive.**

The cleanest datapoint is Letta's own benchmark: an agent given only
`grep`/`search_files`/`open` over flat files scored 74.0% on LoCoMo
with gpt-4o-mini — above Mem0's graph variant at 68.5%. Letta — the
company whose founding metaphor was memory-as-OS — subsequently
rebuilt its memory layer as MemFS: markdown files in a git repo. The
mechanism behind the result matters more than the number: models are
post-trained massively on filesystem tools, so they wield them well;
and an agent that can *reformulate its own query and iterate* beats a
theoretically-superior single-hop retrieval it operates poorly.

Two further properties tipped the design, both usually unmeasured:

- **Auditability.** A human can read, diff, and correct what the agent
  believes. Every write is reviewable; the memory is a repo, not a
  black box behind an API.
- **No silent failure.** An extraction pipeline that decides badly what
  to store is an invisible bug. A file that says the wrong thing is a
  visible one.

## Where bare files break — and engram's answer to each

Honest accounting: structured-filesystem memory has three known failure
modes, and the standard addresses two of them by construction, one by
subordinated infrastructure.

1. **Placement and retrieval degrade as the tree grows.** Grep needs
   the right term; navigation needs a true map. engram's answer is the
   README contract with generated catalogs (maps that cannot silently
   drift), lazy descent by one-line descriptions (MemFS's proven
   catalog pattern, made mandatory), and the find discipline's forced
   reformulation.
2. **Facts change truth-value over time.** A file gives you current
   state or a log; "this was true until March" lives nowhere. The
   graph systems (Graphiti/Zep) win benchmarks on exactly this. engram
   takes the lesson without the graph database: bi-temporal fields and
   supersede-never-edit as *schema-level* contract (base profile
   `fact`), enforced socially by protocol and mechanically where
   changeset tooling exists.
3. **Query-free recall.** When the agent doesn't know what to search
   for, embeddings beat grep. engram's position: build any index you
   want — as *derived state*, rebuilt from files, never authoritative
   (core §10). The failure of most memory products is making the index
   the truth; the failure of purist file systems is refusing the index
   entirely. Subordination resolves both.

## Why a standard, not a product

Every system surveyed couples its memory format to its runtime: Letta
memory needs Letta, Mem0 memories live in Mem0's pipeline, Zep's graph
speaks Zep's API. The store outlives any runtime choice — ten years of
notes must not be hostage to this year's framework. Hence a *format*
standard: markdown + conventions any agent with file tools can operate,
with conformance defined mechanically (check), not by which software is
installed.

The direct ancestors, and what was taken from each:

- **MemFS (Letta):** catalog descriptions as lazy-loading contract; git
  as the write log; worktrees for concurrent background consolidation;
  the audited insight that file tools beat bespoke retrieval.
- **The `.agents/` standard:** the meta-shape — RFC-2119 declarative
  spec, non-normative reference CLI, conformance targets kept separate,
  canonical skeletons, dogfooding.
- **Obsidian-culture vault practice:** typed frontmatter + wikilinks as
  the relational layer; type-discriminated notes; index files as maps —
  formalized here with validation those vaults never had.
- **Graphiti/Zep:** bi-temporal validity and invalidate-don't-overwrite,
  ported from graph edges to frontmatter fields.
- **cortex (first adopter):** the writing rules a real second brain
  needed in practice — anchors-not-elapsed-time, provenance that never
  launders, records earned by recurrence, append-only journal, maps
  carrying no state. Generalized into protocol and profile; the
  cortex-specific parts stayed home.

## Deliberate absences

- **No graph database.** The relational layer is typed links in
  frontmatter, validated by check. When temporal-graph queries are
  truly needed, build the graph *from* the store as derived state.
- **No opaque IDs.** Path-as-identity plus mechanical rename-rewrite.
  Stable IDs require a resolver index to mean anything; an index the
  store can't be read without violates D1. Revisit only if rename pain
  proves real (v2 question).
- **No embedded execution model.** Hooks are declarations; executors
  are environment. The spec that defines *who runs things* has coupled
  itself to a runtime — the exact mistake the standard exists to avoid.
- **No timestamps in frontmatter.** Version control is the bookkeeping
  clock; frontmatter carries only *world* clocks (`valid_until`), never
  copies of what git already knows. Two clocks in one file always
  diverge.
- **No mandated retrieval stack.** The standard norms the substrate and
  its integrity. Retrieval sophistication — FTS, vectors, rerankers —
  is a consumer concern, downstream of truth.

## The bet, in one line

Structure in the files, discipline in the protocol, enforcement at the
changeset, intelligence in the agent — and every layer above the files
rebuildable, so none of them can hold the memory hostage.
