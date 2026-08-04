# Annex — The base profile, v1 (draft)

**Profile name:** `base`
**Version:** 1 (declared as `base@1` in `root.yaml`)
**Status:** Draft
**Revision:** 2026-08-04

This annex norms the **base profile**: a small vocabulary of entity
types for agent memory, plus the store patterns those types assume.
It is OPTIONAL — the core standard is complete without it — and it
versions independently of the core.

The normative artifacts of this profile are the five canonical schema
files under [`profiles/base/schemas/`](../../profiles/base/schemas/):

| Type | One line |
|---|---|
| `note` | Free-form baseline; identical to the core's Appendix A.3 |
| `fact` | One atomic durable statement, with temporal validity and provenance |
| `person` | Living record of a person |
| `project` | Living record of a line of work |
| `journal-entry` | One dated, append-only unit of raw activity |

Installing `base@1` copies exactly those five files into the store's
root `.engram/schemas/`. This annex explains the semantics they encode;
where prose here and a schema file disagree, the schema file governs.

## 1. The two-store pattern (RECOMMENDED)

Memory splits along one axis that experience keeps rediscovering:

- **episodic** — what happened and who/what exists. Dated, anchored,
  accumulating. Journal entries, facts, people, projects.
- **semantic** — how things work: conventions, schemas of behavior,
  operating rules. Corrected in place so it stays true; history lives
  in version control, not in dated supersessions.

The RECOMMENDED deployment is two sibling stores — for example
`memory/` (episodic, agent-writable) and `knowledge/` (semantic,
human-curated, read-only to agents where infrastructure allows). The
test when placing content: *if it has an occurrence date, it is
episodic; if it is true until someone changes it, it is semantic.*

This is a pattern, not a mechanism: the core neither knows nor cares
which role a store plays. Write policies (who may write which store)
belong to the deployment — mounts, permissions, hosts — never to this
profile.

## 2. Temporal validity

Version control answers *when was this file edited*. It cannot answer
*until when was this claim true* — those are different clocks, and
conflating them is how memories rot. The `fact` type carries the second
clock explicitly:

- `valid_from` — date the claim became true (OPTIONAL; defaults to
  unknown-but-past).
- `valid_until` — date the claim stopped holding, or `null` while it
  holds. A closed fact is not deleted — it is history that was once
  trusted.
- `supersedes` — typed links to the fact(s) this one replaces.

The contract: **contradiction supersedes; it never edits.** On learning
that a fact no longer holds, an agent closes the old fact's
`valid_until`, creates the new fact with `supersedes` pointing back,
and touches nothing else. Cross-record consistency (every superseded
fact is closed) is doctor-class in v1 — flagged heuristically, enforced
in a future profile version once cross-record checks are normed.

## 3. Provenance and claim level

Memory an agent will later act on is only as good as its sourcing.
Two `fact` fields carry that weight:

- `source` — where the claim came from: a permalink or identifier
  returned by a tool **in the session that wrote the fact**, a
  document path, a person's stated word — or the explicit string
  `no-reference`. Inventing, reconstructing, or completing a reference
  is forbidden (core Protocol P6); laundering provenance during
  promotion (copying a claim forward without its source) is the same
  sin at one remove.
- `claim` — `confirmed` | `inferred` | `hypothesis`. What kind of
  standing the statement has. A fact promoted from someone's assertion
  stays marked as their assertion until independently confirmed;
  presenting one level as another is the memory-store equivalent of
  lying to your future self.

## 4. The traction rule

Records are earned, not pre-created. An entity seen once is one line —
a `fact`, or an entry in a collection note — never a fresh `person` or
`project` record. The record is created on the second meaningful
encounter or when actual work begins, and its first version already
says something (D2: a stub describes nothing).

This rule is prose, not schema — no validator can measure "traction" —
but it is the difference between a store of living records and a
graveyard of empty scaffolds, so every base-profile schema restates it
where it applies.

## 5. Journal discipline

`journal-entry` records are the raw layer: one per day (or per bounded
session), named by date, `append-only` by policy — the store's one
mechanically-frozen type. Corrections are new entries that say the old
one was wrong; nothing is ever rewritten. Everything else in the
episodic store is distilled from this layer and links back to it, so
every durable claim stays traceable to the moment it entered the
system.

Directories of journal entries SHOULD set `catalog: dirs` in their
README ([core §5.2](README.md#52-the-catalog)): dated records are
addressed by name, and a 365-line catalog is churn without navigation
value.

## 6. What the base profile is not

No task tracker, no meeting-note type, no bookmark manager, no CRM.
Adopters add scoped types for those the moment they actually recur
(core §6, and the traction rule applies to types too: the third
same-shaped `note` is the signal). The base profile's job is to make
five well-understood shapes portable across stores — not to guess
every shape a memory will ever need.
