---
type: journal-entry
version: 1
description: "One dated, editable activity record with sourced entries and explicit coverage."
schema:
  type: object
  required: [type, description, date]
  additionalProperties: false
  properties:
    type: {const: journal-entry@v1}
    description: {type: string, minLength: 1, maxLength: 200}
    date: {type: string, format: date}
---
# journal-entry

The evidence layer of episodic memory: what happened, when, and from where the
information came. Durable records can link back to these anchors.

## Discipline

- One document per day, named by date: `YYYY-MM-DD.md`. The `date` field
  equals the filename stem.
- A reviewed day receives a document even if no relevant activity was found.
  Use a brief statement and source coverage instead of an invented log entry;
  distinguish reviewed inactivity from partial or unavailable coverage.
- The record is editable. Correct entries in place, insert newly discovered
  activity into the log and update the existing Coverage section. Preserve
  accepted entry ids and source references supporting the resulting account.
  Managed Git history retains earlier accepted versions and correction rationale.
- Entries record signals distilled at capture: what happened and its
  source reference, not pasted transcripts. Third-party content arrives
  summarized, with its identifier — never verbatim walls.
- No `pinned` here: an anchor layer is read through the records that
  cite it, not preloaded.

## Directory setup

A journal directory can use `catalog: dirs`: dated records are addressed
by name, so a long generated list may add little navigation value.

## Template

The journal's body convention is defined in `journal/README.md` from the
store root: one daily consolidation, a flat structured log of entries with stable
ids and a separate coverage section. This example illustrates that convention;
schema v1 validates metadata, not individual Markdown log fields.

```markdown
---
type: journal-entry@v1
description: "Daily journal, 2026-08-04."
date: 2026-08-04
---
# 2026-08-04

- **#<NN> · <HH:MM / ~HH:MM / Time unknown> · <KIND> · <keyword, keyword>**
  <Distilled signal, actor and relevant context, rationale or uncertainty.>
  Sources: <observed reference>; <additional reference if relevant>.

## Coverage

- Consolidated at: <timestamp with UTC offset>.
- Time zone: <active time zone used for this run>.
- <Source/account/channel>: <review window and cutoff>; <complete / partial / unavailable>; <relevant gaps>.
```
