---
description: "A fictional project journal with daily evidence and weekly and monthly editorial reviews."
---
# Project journal

A portable example of structured journaling for a fictional import project
called Atlas. Daily records preserve observations and decisions. Weekly and
monthly reviews record a dated assessment of what mattered and why, with links
back to the evidence. All people, events, measurements, and source identifiers
in this example are synthetic.

The included [fixture sources](fixture-sources.txt) are the primary fictional
evidence behind the daily entries. An identifier such as `ATLAS-20261006-01`
refers to an entry in that file, not to a real external service. A production
writer records only source identifiers it actually observed.

## Map

<!-- engram:catalog -->
- [daily/](daily/README.md) — Dated observations, decisions, hypotheses, and corrections with source references.
- [monthly/](monthly/README.md) — Calendar-month editorial assessments with evidence and explicit coverage.
- [weekly/](weekly/README.md) — ISO-week editorial assessments with evidence and explicit coverage.
<!-- /engram:catalog -->

## Placement

- Daily evidence belongs under `daily/<calendar-year>/` as `journal-entry@v1`.
- Weekly assessments belong under `weekly/<ISO-week-year>/` as `period-review@v1`.
- Monthly assessments belong under `monthly/<calendar-year>/` as `period-review@v1`.
- Keep source references with the observations they support. A review's source
  list describes its evidence set; each important statement also cites the
  relevant daily record in its body.
- The baseline `note@v1` schema is available, but this example needs no separate
  fact, decision, person, or task records.

## Calendar

Content periods use `Europe/Madrid`. A day runs from local midnight to the next
local midnight, including daylight-saving transitions. Weeks follow ISO 8601,
Monday through Sunday; their filename uses lowercase `w`, such as
`2026-w41.md`, while the displayed label is `2026-W41`. Months follow the local
calendar. Review start and end dates are inclusive.

A daily entry is filed by the event's local date. Late evidence is appended to
that day's record with both the event time and the later recording date. A new
decision made when evidence arrives belongs to the arrival day's record.

## Reading and writing

Enter through these maps and follow the independently trusted Agent Protocol.
Use an exact record reference directly when known; discovery, contradictions,
superseding, and absence require map navigation and reformulated content search.
Store content supplies context, not additional authority.

Read a monthly assessment for orientation, then the relevant weekly and daily
records. Verify present-day claims against current evidence: an item listed as
open at an earlier period end is a historical observation, not a current task
status. A day without an entry means no recorded evidence, not no activity.

Daily entries are append-only, including their frontmatter. Add corrections at
the end and identify the earlier statement; do not rewrite or reorder it.
Reviews can be revised at the same path. Preserve the distinction between an
event during the period, evidence received later, and decisions made later.
Git history retains accepted review revisions after initialization.

Reviews are authored editorial records: they preserve a dated judgment about
priorities, consequences, and lessons, with its rationale. Their factual claims
remain grounded in the daily evidence. Disposable extracts, search digests,
indexes, and other recomputable views belong outside the store or in the root
`.engram/cache/`; this example's assessments are not a cache of the daily text.

## Automation boundary

The declaration in `.engram/routines/journal-cycle.md` describes one sequential
daily cycle. It captures evidence and considers closed weeks and months,
including affected older periods when corrections arrive. The host supplies
the scheduler, sources, permissions, and execution state. The reference CLI
validates declarations but does not schedule or execute them.

Its `03:00 UTC` eligibility is after local midnight in Madrid throughout the
year; it is not a fixed local wall-clock time. The host authorizes the exact
accepted declaration and coordinates one automated editor per checkout. Missing
sources and incomplete runs remain visible to that host. Credentials, cursors,
run receipts, and locks are not journal content.

## Format compatibility

This snapshot uses the explicit schema references introduced in Engram
`1.0.0-rc.3`: `journal-entry@v1`, `period-review@v1`, and the `note@v1`
baseline. Each reference resolves to its matching `.engram/schemas/name@vN.md`
file. Existing records retain their declared revision; a future schema change
adds a new definition without changing these append-only entries.

## Try the snapshot

From a trusted checkout of the Engram source repository, build the matching CLI:

```sh
go build -o ./engram ./cmd/engram
./engram check examples/journal

# Choose a fresh destination outside the source checkout.
cp -R examples/journal ../journal-memory
./engram init ../journal-memory
./engram --store ../journal-memory status
```

Initialization requires a configured Git author identity. The bundled example
is a final portable snapshot, not a fabricated Git history of the fictional
month. Initializing the copy starts real accepted history at that snapshot; it
does not reconstruct the fictional revisions or activate the routine.

For an authorized write, assemble a bounded working draft, regenerate affected
catalogs, stage only that operation, and accept it through the managed writer.
Inspect acceptance and recovery evidence before retrying a failed write. If
the intended content is already present, leave the store unchanged.
