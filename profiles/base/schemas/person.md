---
type: person
version: 1
description: "Living record of a person with real traction in this store's world."
schema:
  type: object
  required: [type, description, name]
  additionalProperties: false
  properties:
    type: {const: person}
    description: {type: string, minLength: 1, maxLength: 200}
    pinned: {type: boolean}
    name: {type: string, minLength: 1}
    aliases:
      type: array
      items: {type: string}
    emails:
      type: array
      items: {type: string}
    org: {type: string}
    role: {type: string}
    projects:
      type: array
      items:
        type: string
        x-engram-link: {types: [project], must-exist: true}
body:
  required-sections: [Facts]
---
# person

A living record: rewritten as the person's situation changes, with the
dated trail kept in its sections. `description` says who they are and
why they matter here — stable, not their latest status.

## When to create one — and when not

**Records are earned by recurrence.** A person seen once is one line in
a collection note or a `fact` — never a fresh record. Create the record
on the second meaningful encounter, or when actual work together
begins; its first version must already say something.

## Identity

`emails` (and `aliases`) are the matching keys: automation links people
by identifier, never by display name — display names collide and
change. `name` is the human-facing full name.

## Body

- `## Facts` (required) — one dated line per durable claim, newest
  context on top. A contradiction supersedes the old line; prefer
  promoting weighty claims to `fact` records and linking them.
- `## Interactions` (recommended) — append-only dated log:
  `- 2026-08-04 — <what> → [[journal/daily/2026-08-04]]`.

`org` and `role` state the current claim; when they change, the change
is dated in Facts and the field is updated — one truth per fact, in one
place.

## Template

```markdown
---
type: person
description: "<who they are and why they matter here>"
name: <Full Name>
emails: [<address>]
org: <org>
role: <role>
projects: []
---
# <Full Name>

<1–3 self-contained lines.>

## Facts

- <claim> (2026-08-04 ← [[journal/daily/2026-08-04]])

## Interactions

- 2026-08-04 — <what> → [[journal/daily/2026-08-04]]
```
