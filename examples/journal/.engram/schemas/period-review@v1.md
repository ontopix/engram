---
type: period-review
version: 1
description: "A dated editorial assessment of a closed week or month, with evidence links and explicit coverage."
schema:
  type: object
  required: [type, description, period_start, period_end, sources]
  additionalProperties: false
  properties:
    type: {const: period-review@v1}
    description: {type: string, minLength: 1, maxLength: 200}
    period_start: {type: string, format: date}
    period_end: {type: string, format: date}
    sources:
      type: array
      minItems: 1
      uniqueItems: true
      items:
        type: string
        x-engram-link: {types: [journal-entry, period-review], must-exist: true}
body:
  required-sections: [Summary, Decisions and rationale, Lessons, Open at period end, Coverage]
---
# period-review

A deliberately small local type shared by weekly and monthly reviews. It
records an editorial judgment about the period: which events mattered, their
consequences, and what the evidence supports. It is not an additional built-in
Engram type and does not change the curated inventory.

## When to create one

Create one after a week or month has closed and there is evidence worth
reviewing. An authorized reviewer can be a human or an agent. Record the dated
assessment and its rationale, distinguishing interpretation from observations.
Do not fill a quota of highlights or fabricate a review when no evidence exists;
the host records a completed no-op outside the store.

## Period and naming

- Use `weekly/<ISO-week-year>/<ISO-week-year>-w<two-digit-week>.md` for a full
  Monday-to-Sunday week. The directory and filename use the ISO week-year, which
  can differ from the calendar year near New Year.
- Use `monthly/<calendar-year>/<calendar-year>-<two-digit-month>.md` for a full
  calendar month. Do not create daily instances of this type.
- `period_start` and `period_end` are inclusive dates in `Europe/Madrid`.
  Their boundaries describe the intended period even when its evidence is
  incomplete. Never shrink the dates to hide missing coverage.
- Use a stable description of the period, not a changing status or headline.

## Evidence and assessment

Weekly `sources` lists the daily entries reviewed. Monthly `sources` lists all
available daily entries in the month and may also list weekly reviews used for
orientation. An out-of-period daily entry may document a correction or coverage
gap; identify that use explicitly without treating it as an in-period event.
Weekly reviews crossing a month boundary supply context only; events outside
the month do not become that month's events. Reviews never source themselves
or create cycles.

Each important factual statement also links to the daily record that supports
it, with an event time or source identifier when needed to locate the entry.
Read a monthly period's daily evidence as well as its weekly assessments so an
earlier selection of highlights does not become a permanent filter.

Prioritize consequential decisions, verified outcomes, changed direction,
material blockers, and reusable lessons. Repetition alone is not importance.
Label an interpretation or uncertain claim; do not promote it to a fact merely
because it appeared in a prior review.

## Body

- `## Summary` — a dated assessment, usually three to five useful points.
  Fewer are appropriate when the evidence supports fewer.
- `## Decisions and rationale` — decisions made in the period, their reasons,
  and explicit reversals. Do not backdate a later decision into this period.
- `## Lessons` — reasoned interpretations with supporting evidence and limits.
- `## Open at period end` — what was unresolved at the closing date. Later
  resolution may be noted separately without rewriting what was known then.
- `## Coverage` — available dates and sources, missing evidence, and later
  additions or corrections used in this version. Absence of a record never
  proves absence of activity.

## Revision

This type is editable. Refresh the existing path when relevant evidence or an
explicit correction changes the assessment, preserving the rationale and
identifying late evidence. Managed Git history retains earlier accepted
versions; no second file or manual revision counter is needed. Skip a write
when there is no material change.

Static validation checks the fields, dates, section headings, and link targets.
Calendar alignment, evidence completeness, factual accuracy, and the quality
of the assessment remain the writer's responsibility.

## Template

```markdown
---
type: period-review@v1
description: "Weekly review for <ISO-week label>."
period_start: <YYYY-MM-DD>
period_end: <YYYY-MM-DD>
sources:
  - "[[daily/<year>/<date>]]"
---
# <ISO-week label or month>

## Summary

Assessment recorded <date>: <what mattered, why, and its evidence>.

## Decisions and rationale

<Decisions made in the period, their reasons, and daily references.>

## Lessons

<Supported interpretation, with uncertainty and daily references.>

## Open at period end

<Historical open questions as of period_end, with evidence.>

## Coverage

<Dates and sources reviewed, gaps, and late evidence used.>
```
