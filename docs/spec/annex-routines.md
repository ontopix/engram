# Annex — Routine declarations, v1 (draft)

**Version:** v1
**Status:** Draft
**Revision:** 2026-08-13
**Normative status:** Normative

This annex defines optional, portable declarations of scheduled routines. A
declaration names a UTC schedule and carries Markdown instructions for work
that an externally authorized agent may perform. It defines neither a
scheduler nor an agent runtime.

The requirements language and BCP 14 interpretation of core §1.2 apply.

## 1. Scope and boundaries

A **routine declaration** is normed configuration in a snapshot. It is not a
record, asset, map, schema, hook program, cache, or source of runtime state.
Its presence does not make an execution occur, select a model or harness,
grant access to a source, grant credentials, authorize a write, or authorize
publication.

The declaration's schedule identifies UTC instants at which work becomes
**eligible**. Eligibility is not a guarantee of punctual, exactly-once, or
eventual execution. This annex defines no recovery, retry, queuing,
concurrency, notification, or delivery behavior. Those operational choices
remain outside the snapshot.

Static validation checks only this declaration's layout, text profile,
frontmatter, instructions body, and `cron` profile. It MUST NOT read a clock,
infer whether a controller is bound, or emit a finding because an eligible
instant is past, present, or future.

This annex defines only scheduled eligibility. API, repository-event, manual,
and other trigger kinds are outside v1.

## 2. Layout and identity

Any logical content directory `<D>`, including the snapshot root, MAY contain
a `.engram/routines/` directory. That directory MUST be a real directory and
MUST contain only direct regular routine-declaration files. It MUST NOT contain
subdirectories or any other entry. Violations produce E309; traversal,
pruning, and precedence follow core §2.4.

Each declaration filename MUST be exactly `<slug>.md`, where `<slug>` uses the
type-slug grammar: 1–64 lowercase ASCII characters from `[a-z0-9-]`, with no
leading, trailing, or doubled hyphen. The routine's identity within a snapshot
is its complete logical path. This annex defines no opaque identifier and no
identity continuity across a rename or move.

Routine declarations are configuration, not content. They are excluded from
record resolution, link targets, directory catalogs, and the requirement for
content-directory `README.md` files. The containing `<D>` supplies only
organizational context; it neither grants authority nor implies an input or
output scope.

For example, a routine local to the `journal/` subtree may live at:

```text
journal/.engram/routines/daily-journal.md
```

## 3. Declaration format

A routine declaration is normed UTF-8/LF Markdown under core §2.6. It MUST
begin with frontmatter using the exact delimiter grammar of core §4.1. Its
frontmatter MUST be a mapping containing exactly these fields:

| Field | Requirement | Meaning |
|---|---|---|
| `engram` | REQUIRED string, exactly `routine/v1` | Declares this routine format and version |
| `cron` | REQUIRED string conforming to §4 | The recurring UTC eligibility schedule |

The Markdown body starts immediately after the closing frontmatter delimiter.
It MUST contain at least one character other than ASCII space, horizontal tab,
LF, or CR. The complete body is the routine's instructions. It is intentionally
not split into declared inputs, outputs, tools, model settings, or permissions.
Version 1 defines no `timezone`, `missed`, `enabled`, `paused`, or `suspended`
frontmatter field.

Any unknown, missing, duplicated, wrongly typed, or otherwise invalid
frontmatter field, invalid frontmatter delimiter, or empty instructions body
produces E309 at the declaration's path.

Example (non-normative):

```markdown
---
engram: routine/v1
cron: "17 2 * * *"
---

# Daily journal

Review the externally authorized sources and propose a journal update that is
grounded in the evidence available to this run. Do not promote material to
facts automatically.
```

## 4. `cron` profile

The `cron` string uses this closed five-field profile:

```text
minute hour day-of-month month day-of-week
```

The five fields MUST be separated by exactly one ASCII space, with no leading
or trailing whitespace. Every field is a comma-separated list of one or more
items and matches when at least one item matches. A decimal integer is unsigned
ASCII decimal notation; leading zeroes have no special meaning. An item is one
of:

- `*`;
- one decimal integer in the field's domain;
- an inclusive `<first>-<last>` range in the field's domain, where `first` is
  not greater than `last`;
- `*/<step>`; or
- `<first>-<last>/<step>`.

`step` is a non-zero ASCII decimal integer no greater than the cardinality of
the wildcard or range it qualifies. No aliases, names, seconds, year field,
question mark, or other cron extension is admitted.

The domains are:

| Field | Domain |
|---|---|
| `minute` | 0–59 |
| `hour` | 0–23 |
| `day-of-month` | 1–31 |
| `month` | 1–12 |
| `day-of-week` | 0–6, where 0 is Sunday |

The expression is evaluated against Gregorian civil time in UTC. A minute
matches when its minute, hour, and month values match their fields and its
date satisfies the day fields:

- if both `day-of-month` and `day-of-week` are exactly `*`, every date
  satisfies them;
- if exactly one is other than `*`, that field must match; and
- both fields MUST NOT be other than `*` in one expression.

For a stepped item, the matched values start at the wildcard's domain minimum
or the range's `first` value and advance by `step`. A day-of-month that does
not exist in a matching month has no matching minute. A controller that cannot
preserve this UTC profile MUST decline to bind the declaration; it MUST NOT
silently reinterpret the expression in a local time zone or another cron
dialect.

An invalid `cron` value produces E309 at the declaration's path.

## 5. External execution and authority

A routine declaration is never a core `prepare-changeset` hook. A controller
MAY bind an accepted declaration to an external scheduler and agent runtime,
but that binding is external state and requires authority independent of the
snapshot. The declaration's Markdown body may guide only an already authorized
operation; it does not establish its own trust or authority under core P0.

A controller that binds a routine MUST bind the physical store identity, the
declaration's complete logical path, and its exact accepted bytes. Before a
run, it MUST verify that those bytes remain present at that path in the
accepted state selected for the run. A change, removal, or move therefore
requires a new external authorization decision before a controller may run the
routine again. Runtime, model, tools, sources, credentials, execution state,
and observations remain controller-owned external state.

If a routine materializes a persistent store change, the controller MUST treat
that work as an ordinary authorized write: it starts from accepted state and
uses one managed transaction under the Git annex. Its resulting candidate is
therefore subject to the applicable preparation hooks and final validation.
This annex neither determines the resulting files nor grants any authority to
accept, commit, push, or otherwise publish them.
