---
engram: routine/v1
cron: "0 20 * * *"
---
# Daily activity journal

Maintain the daily activity record for the configured scope, following the
journaling contract at `journal/README.md` relative to the containing store root.
The default schedule is once a day at 20:00 UTC. Gather and review the evidence
before writing one consolidated daily log. Run it only when the
host's assignment names this exact routine; it grants no additional authority.

## Establish the run

- Verify that this declaration and `journal/README.md` are committed and unchanged.
  Read the contract, applicable maps and schemas; resolve the required host settings.
- Work as the designated writer of the journal. Preserve unrelated edits; stop if
  they cannot be separated safely.
- Establish the day in the active time zone and source-specific review windows.
  Record the chosen time zone and windows in the host's run record for recovery.
  Include missed windows and late evidence from any source, not only missing agent
  journals. Read previous coverage and runtime cursors; state gaps when unavailable.
  Review the remainder of the preceding day and record that coverage even when no
  relevant activity is found. Split windows by local date as the contract requires.

## Collect and reconcile

1. Read configured contributor journals, relevant task activity and the writer's
   own work where it falls within scope. Use them as source contributions, not the
   full extent of the day. No particular agent roster or store layout is required.
2. Review the authorized email, chat, meeting minutes/notes, documents and
   project/task updates identified by the contract. Use existing sourced agent
   contributions where sufficient; consult original evidence when available.
   Record the scope and actual coverage of each source, including inaccessible ones.
3. Collect changes in the review window regardless of whether a task is closed.
   Read existing daily entries and any linked meeting records before writing.
   Reconcile signals about the same event across sources, retain their references
   and separate distinct decisions, hypotheses, commitments and changes of state.
4. Attribute claims, label interpretations and preserve unresolved contradictions.
   Check event dates independently of consolidation dates; never invent missing times,
   outcomes, attendees, decisions or source identifiers.

## Write

5. Prepare `journal/daily/<YYYY>/<YYYY-MM-DD>.md` from the store root for each local
   date reviewed, using `journal-entry@v1`. Write a flat log ordered by event time with one
   meaningful signal per item: id, time, kind, keywords, description and sources. Use
   the contract's exact entry convention, including unknown or approximate times.
   Reuse the keywords the store already knows before introducing a new one.
   Number a new log from `#01` in written order, and cite the id of any earlier
   entry that an item corrects or resolves.
   Do not create capture blocks or thematic sections. Create the year directory
   and its README together if missing. If a record was already accepted, skip
   duplicates, correct existing entries in place and insert new evidence into the
   chronological log. Preserve existing ids; assign each new entry the next unused
   id from the record's history, even when its event time places it earlier in the
   log. Follow the contract's correction and reference rules.
6. End a new daily document with one `Coverage` section: consolidation timestamp,
   active time zone, source scopes, review windows, actual cutoffs and gaps. Create
   the document even when the log is empty, stating that no relevant activity was
   found within the reviewed sources and window. Distinguish partial or unavailable
   coverage from a verified absence of relevant activity. Update that same final
   section when windows extend or gaps change, including the prior day's remaining
   hours. Refresh its consolidation timestamp when the document changes. Keep one
   log and one coverage section; no supplementary sections are needed. A no-op
   applies only when the evidence and coverage were already accepted unchanged.
   Review the complete draft before managed acceptance.
7. If a meeting collection is configured, create or update sourced meeting records
   using its location and schema, then link daily entries to them. Otherwise retain
   distilled outcomes and original source references in the log. Minutes or notes
   can support outcomes even without a transcript; identify the evidence used. A calendar event alone supports
   scheduling, not attendance or outcomes. Leave missing information explicit.
8. If durable knowledge destinations are configured, promote only relevant sourced
   knowledge, preserving its claim level and links to daily evidence. Apply only
   authorized, validated update requests within scope. Respect the host's data exclusions.

## Accept and report

Regenerate affected catalogs, stage only this operation and commit through the
managed writer after `engram check`. Explain material corrections and their sources
in the commit message; accepted history retains the earlier versions. Advance collection cursors only after the
corresponding writes have been accepted, or a retry verifies those same windows
were already accepted; preserve unreviewed windows. Record the accepted commit,
daily paths, source windows and coverage limits in the host's run record. If
interrupted after commit, reconcile accepted entries and coverage before retrying.
Flag affected weekly/monthly periods for revision when evidence or coverage changes
their assessment; keep this operational queue outside the memory records.

After acceptance, notify the host that deferred weekly/monthly work waiting for
this consolidation can resume, retaining each review's original period. A verified
retry may recover this notification if the earlier run stopped after acceptance.
Do not signal readiness after a failed or unaccepted write. This resumes review
work only; it does not schedule another daily capture.

Notify the configured recipient when validation fails, a source is missing or a
decision requires attention. Use the configured delivery channel and language.
Explain coverage limits without claiming a complete account of the day. Record
completion in the host's run record, including when no journal change is needed.
