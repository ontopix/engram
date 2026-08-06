---
type: project
version: 1
description: "Living record of a line of work: it has people, decisions, and a state that changes."
schema:
  type: object
  required: [type, description, name, status]
  additionalProperties: false
  properties:
    type: {const: project}
    description: {type: string, minLength: 1, maxLength: 200}
    pinned: {type: boolean}
    name:
      type: string
      pattern: "^[a-z0-9]+(-[a-z0-9]+)*$"
    status: {enum: [active, paused, closed]}
    people:
      type: array
      items:
        type: string
        x-engram-link: {types: [person], must-exist: true}
body:
  required-sections: [Status, Decisions]
---
# project

A line of work with its own state. Not everything with a name is a
project: a loose task, a conversation topic, an organization, a goal
nobody is working toward — none of these earn one.

## When to create one — and when not

**Records are earned by recurrence.** Work that shows up once is a
journal line; create the record when the work recurs or materially
starts. The first version must already carry a Status worth reading.

## Fields

- `name` — kebab-case, recommended to equal the filename: it is the inbound
  wikilink target, so renaming it means rewriting links (core §7.4).
- `status` — set on evidence, never on elapsed time. A project silent
  for three months is still `active` until something proves otherwise;
  activity is not progress, and silence is not closure.

## Body

- `## Status` — rewritten in place: present tense, WITH its date, so a
  reader knows both the state and its freshness.
- `## Decisions` — append-only, each line dated and carrying its
  source: `- 2026-08-04 — <decision> ← <source | [[journal/...]]>`.

## Template

```markdown
---
type: project
description: "<what is at stake, one line>"
name: <kebab-case>
status: active
people: []
---
# <Name>

<1–3 self-contained lines: what this is and why it matters.>

## Status

<Present tense, dated: as of 2026-08-04, ...>

## Decisions

- 2026-08-04 — <decision> ← [[journal/daily/2026-08-04]]
```
