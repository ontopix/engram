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

One atomic statement the store vouches for — with the two clocks that
keep memory honest: when the claim held (`valid_from` / `valid_until`)
and where it came from (`source`, `claim`). Version control remembers
when the *file* changed; these fields remember when the *world* did.

**The statement is the `description`.** One assertable sentence — that
is what makes facts greppable from catalogs alone. The body carries
nuance, context, and links; it never carries a second claim (that is
another fact).

## When to create one — and when not

A fact is durable and cross-cutting: it will matter outside the episode
that produced it. Raw activity stays in `journal-entry`; a claim worth
trusting months from now is distilled into a fact that links back to
its journal anchor. One-off trivia is not a fact; a claim you would act
on is.

## Temporal contract

- Absent `valid_until` (or `null`) means the claim currently holds.
- **Contradiction supersedes; it never edits.** On learning the claim
  no longer holds: set the old fact's `valid_until`, create the new
  fact with `supersedes: ["[[<old>]]"]`, change nothing else. A closed
  fact is history that was once trusted — it is not deleted.
- Store anchors, never elapsed time: "unanswered since 2026-07-17",
  never "~2 weeks without answer". Computed quantities rot on their
  own; elapsed time is computed at read time.

## Provenance contract

`source` holds a permalink or identifier returned by a tool in the
session that wrote the fact, a document path, or a person's stated
word — or the explicit string `no-reference`. Never construct, guess,
or complete a reference. When a fact is promoted or copied forward, its
source travels with it: promotion moves a fact, it never launders where
it came from.

`claim` states the standing: `confirmed` (verified or first-hand),
`inferred` (follows from evidence, not stated), `hypothesis` (worth
recording, not yet supported). Presenting one level as another is how a
store starts lying to its future reader.

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
