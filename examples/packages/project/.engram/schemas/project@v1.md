---
type: project
version: 1
description: "Living record of a line of work: it has people, decisions, and a state that changes."
schema:
  type: object
  required: [type, description, name, status]
  additionalProperties: false
  properties:
    type: {const: project@v1}
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

A line of work with people, decisions, and changing state. A loose task,
topic, organization, or inactive aspiration is not a project record.

## When to create one — and when not

Create the record when work materially starts or recurs. Its first
version must already contain a useful status.

## Fields

- `name` — kebab-case. For directory-based projects, match the enclosing project
  directory name; the record is always named `project.md`. The record path, not
  this field, is link identity.
- `status` — set on evidence, never on elapsed time. A project silent
  for three months is still `active` until something proves otherwise;
  activity is not progress, and silence is not closure.

## Body

Use the common sections defined by `project/README.md` from the store root:
Purpose, Scope, People, Status, Decisions and Resources. These conventions do not
depend on the name of the containing scope. Only the following two sections are
mechanically required by this schema revision; the others organize useful context.

- `## Status` — rewritten in place: present tense, WITH its date, so a
  reader knows both the state and its freshness.
- `## Decisions` — append-only, each line dated and carrying its
  source: `- <YYYY-MM-DD> — <decision and evidenced rationale> ← <source reference>`.

## Template

```markdown
---
type: project@v1
description: "<what is at stake, one line>"
name: <project-name>
status: active
people: []
---
# <Name>

## Purpose

<Intended outcome and why the work matters, with its source.>

## Scope

<Included work and boundaries; this is independent of the grouping directory.>

## People

<Known responsibilities and links to existing people records, or an explicit unknown.>

## Status

<Present tense, dated: as of YYYY-MM-DD, ...; include evidence and material blockers.>

## Decisions

- <YYYY-MM-DD> — <decision and evidenced rationale> ← <observed source reference>

## Resources

<Exact observed links to repositories, documents, tools and other useful resources.>
```
