---
type: journal-entry
version: 1
description: "One dated, append-only unit of raw activity; the anchor layer everything durable links back to."
schema:
  type: object
  required: [type, description, date]
  additionalProperties: false
  properties:
    type: {const: journal-entry}
    description: {type: string, minLength: 1, maxLength: 200}
    date: {type: string, format: date}
policy:
  append-only: true
---
# journal-entry

The raw layer of episodic memory: what entered the system, when, and
from where. Durable records can link back to these anchors.

## Discipline

- One entry per day, named by date: `2026-08-04.md`. The `date` field
  equals the filename stem.
- The type is mechanically append-only. Add new material at the end;
  correct a mistake with a later line rather than rewriting history.
- Entries record signals distilled at capture: what happened and its
  source reference, not pasted transcripts. Third-party content arrives
  summarized, with its identifier — never verbatim walls.
- No `pinned` here: an anchor layer is read through the records that
  cite it, not preloaded.

## Directory setup

A journal directory can use `catalog: dirs`: dated records are addressed
by name, so a long generated list may add little navigation value.

## Template

```markdown
---
type: journal-entry
description: "Daily journal, 2026-08-04."
date: 2026-08-04
---
# 2026-08-04

- <HH:MM> — <what, distilled> ← <source id | no-reference>
```
