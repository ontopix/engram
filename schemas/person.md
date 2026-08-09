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

A living record updated as a person's situation changes. `description`
says who they are and why they matter here, not their latest status.

## When to create one — and when not

Create a person record after meaningful recurrence or when real work
together begins. A one-off mention belongs in its source record.

## Identity

`emails` and `aliases` are matching keys; `name` is the human-facing
display name and is not assumed unique.

## Body

- `## Facts` (required) — dated history. Promote weighty claims to
  `fact` records and link them.
- `## Interactions` (recommended) — append-only dated log:
  `- 2026-08-04 — <what> → [[journal/daily/2026-08-04]]`.

`org` and `role` hold the current values; `## Facts` keeps the dated
history when either changes.

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
