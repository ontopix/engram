---
type: fact
version: 1
description: "One atomic durable statement, with temporal validity, provenance, and claim level."
schema:
  type: object
  required: [type, description, source, claim]
  additionalProperties: false
  properties:
    type: {const: fact}
    description: {type: string, minLength: 1, maxLength: 200}
    pinned: {type: boolean}
    valid_from: {type: string, format: date}
    valid_until:
      anyOf:
        - {type: string, format: date}
        - {type: "null"}
    supersedes:
      type: array
      minItems: 1
      items:
        type: string
        x-engram-link: {types: [fact], must-exist: true}
    source: {type: string, minLength: 1}
    claim: {enum: [confirmed, inferred, hypothesis]}
    tags:
      type: array
      items: {type: string}
---
# fact

One atomic statement the store vouches for. The `description` is the
claim, expressed as one assertable sentence; the body holds its context
and links, not a second claim.

## When to create one — and when not

A fact is durable enough to matter outside the episode that produced
it. Raw activity stays in `journal-entry`; a claim worth acting on is
distilled here and links back to its anchor.

## Temporal contract

- `valid_from` and `valid_until` record when the claim held; absent or
  null `valid_until` means it is current.
- A contradiction closes and supersedes the old fact; it never
  overwrites the old claim. Set `valid_until`, create the replacement
  with `supersedes: ["[[<old>]]"]`, and preserve the former statement.
- Store anchors, never elapsed time: "unanswered since 2026-07-17",
  never "~2 weeks without answer". Computed quantities rot on their
  own; elapsed time is computed at read time.

## Provenance contract

`source` identifies a source actually observed by the writer: for
example a tool-returned permalink, a document path, or a person's
statement. Use `no-reference` when none is available. Never construct,
guess, or complete a reference, and preserve the source when promoting
the claim.

`claim` states its standing: `confirmed` (verified or first-hand),
`inferred` (derived from evidence), or `hypothesis` (not yet supported).

## Naming

Kebab-case slug of the statement's subject:
`jane-doe-left-acme.md`, `prod-db-is-postgres-16.md`.

## Template

```markdown
---
type: fact
description: "<the statement, one assertable sentence>"
source: "<tool-returned permalink | document path | who said it | no-reference>"
claim: confirmed
valid_from: 2026-01-15
valid_until: null
---
# <Short title>

<Context and nuance. Link the anchor: [[journal/daily/2026-01-15]].>
```
