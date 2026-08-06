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

The raw layer of episodic memory: what entered the system, when, from
where. Everything durable — facts, people, projects — is distilled
*from* this layer and links *back* to it, which is what keeps every
claim in the store traceable to the moment it arrived.

## Discipline

- One entry per day (or per bounded session), filename the date:
  `2026-08-04.md`. The `date` field equals the filename.
- **Append-only by policy** — the store's one mechanically-frozen
  type. Within the open day, new material is appended at the end;
  nothing already written is edited. A mistake is corrected by a new
  dated line stating the correction, never by rewriting history.
- Entries record signals distilled at capture: what happened and its
  source reference, not pasted transcripts. Third-party content arrives
  summarized, with its identifier — never verbatim walls.
- No `pinned` here: an anchor layer is read through the records that
  cite it, not preloaded.

## Directory setup

The README of a journal directory is recommended to set `catalog: dirs` — dated
records are addressed by name, and a 365-line catalog is churn without
navigation value.

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
