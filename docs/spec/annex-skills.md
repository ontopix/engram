# Annex — Canonical skills, v1 (draft)

**Status:** Draft
**Revision:** 2026-08-04

The Agent Protocol (core §11) states *what* an agent owes a store; the
canonical skills teach *how*, in the packaging agents actually consume
([Agent Skills](https://agentskills.io) format: `<slug>/SKILL.md`).
This annex specifies the four skills; the shipped `SKILL.md` artifacts
will live under `skills/` in this repository and follow this annex.

Skills mirror **operations, not structure**: an agent doesn't need a
skill per directory — it needs the write discipline, the find
discipline, the maintenance duties, and the evolution procedure. Four
skills, no more.

They are runtime-neutral by construction and reach runtimes through
the adapters annex (sync/vendoring), or simply by being read — each
skill is plain markdown an agent can follow with filesystem tools
alone.

---

## engram-write

**Trigger:** anything that adds to or edits a store — capturing new
information, updating a living record, promoting distilled claims.

**Discipline:**

1. **Resolve the type first.** Ascend `.engram/schemas/` from the
   target area; read the winning schema file *including its prose* —
   "when not to create one" is half its value. No fitting type →
   `note` (never force structure), and note the recurrence signal: a
   third same-shaped note means a missing type.
2. **Place by the maps.** Read the target directory's README and unread
   ancestors' (Protocol P2); follow `## Placement`. If placement is
   genuinely ambiguous, the READMEs are defective — fix the map or
   surface the gap; don't guess silently.
3. **Respect earned existence.** Living-record types (person, project)
   are created on recurrence, not first sight (base profile §4). First
   sight is a line, not a file.
4. **Write from the template.** Extract the schema's `## Template`
   block; fill every field deliberately — `description` is the catalog
   line, written for a reader who doesn't know the file exists.
5. **Link, typed where it counts.** Relations the schema models go in
   frontmatter link fields; looser references are body wikilinks. Never
   restate a linked record's content — link it.
6. **Close the changeset whole** (Protocol P4, P7): new directory ⇒ its
   README, same changeset; affected catalogs regenerated (tooling if
   present, by hand otherwise); validate — check when available,
   read-back against the schema when not. A write that leaves the
   store non-conforming is not done.
7. **Contradictions supersede** (Protocol P5): follow the type's
   superseding semantics; never silently overwrite; surface what has no
   defined resolution.

## engram-find

**Trigger:** any retrieval — a direct question, background gathering
before writing, verification of a claim's existence.

**Discipline:**

1. **Descend the maps.** Root README → catalog descriptions → child
   READMEs, opening only what the descriptions justify (D4). The
   catalog line is the contract; trust it over guessed paths.
2. **Search content in parallel**, not as fallback: grep-class search
   over the store with the query's own terms, then **at least one
   reformulation** (synonyms, the entity's other names, the date's
   neighborhood). Frontmatter is greppable by design — `type:`,
   link targets, `description` lines.
3. **Follow the graph.** From any hit, walk its typed links and body
   wikilinks; from an entity record, its journal anchors. The link
   structure is retrieval infrastructure, not decoration.
4. **Mind the clocks.** A `fact` with closed `valid_until` was true,
   isn't now; elapsed time is computed at read time from stored
   anchors. Never present superseded claims as current.
5. **Absence has a bar** (Protocol P3): claimed only after catalog
   descent *and* reformulated content search. Until then: "not found so
   far", with where you looked.

## engram-maintain

**Trigger:** scheduled upkeep, or drift noticed in passing (stale
catalog, orphan, duplicate suspicion).

**Discipline:**

1. **Deterministic pass first:** run check (or manually verify the
   E-catalog basics); fix findings — stale catalogs, broken links,
   missing READMEs — before any judgment work.
2. **Judgment pass second**, clearly separated: duplicate candidates
   merge via superseding (never delete-and-lose-provenance); orphans
   get linked from where they should have been reachable, or promoted
   into records they support, or flagged for the human; descriptions
   that no longer describe are rewritten in place.
3. **Promote along the pipeline:** journal signals that proved durable
   become facts/records linking back to their anchors; promotion moves
   claims, it never launders sources (base profile §3).
4. **Never violate policies while cleaning:** append-only and immutable
   records are corrected by new entries, not edits — maintenance has no
   special license.
5. **Report what changed** and what needs a human decision; a
   maintenance run that silently reshapes memory is worse than none.

## engram-evolve

**Trigger:** a schema needs to change — new field, narrowed constraint,
new type, deprecation.

**Discipline:**

1. **Classify the change.** Widening (new OPTIONAL field, relaxed
   constraint): edit schema, bump `version`, done. Narrowing (new
   required field, tightened enum, removed field): migration required.
2. **Migration is one changeset:** updated schema (+`version` bump)
   *and* every affected record brought conforming, together — the store
   never points at itself broken (core §6.9). Enumerate affected
   records by `type:` search before editing anything.
3. **New types respect scope:** define at the deepest directory that
   covers all intended use; check shadowing (E304) before naming.
   Update the scope's README placement rules in the same changeset —
   a type nobody can find is a type nobody uses.
4. **Deprecation is closure, not deletion:** stop referencing the type
   in placement rules; migrate or close its records; remove the schema
   only when no record carries the type. History stays in version
   control.
5. **Profile-owned schemas fork consciously:** editing an installed
   profile's schema makes the store a fork (core §12); prefer a scoped
   new type unless the divergence is deliberate — then record why in
   the schema's prose.
