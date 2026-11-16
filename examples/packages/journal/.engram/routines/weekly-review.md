---
engram: routine/v1
cron: "15 20 * * 1"
---
# Weekly activity review

Assess activity within the configured scope for the most recent fully closed ISO
week. The default schedule is 20:15 UTC on Mondays. Run only when the host's assignment
names this exact routine; follow the contract at `journal/README.md` from the store root.

## Establish the review

Verify that this declaration and the contract are committed and unchanged. Read
the contract, maps and `period-review@v1` schema; resolve the required host settings.
Use the active time zone for week boundaries: Monday 00:00 through the following
Monday 00:00, with `period_end` set to Sunday. If today is Sunday, the current week
is still open. Select the latest week whose closing boundary has passed. Also
consider earlier closed weeks whose reviews are missing or affected by late evidence;
honor previously completed no-op reviews. Review pending earlier weeks oldest
first, so each review can carry open items from its predecessor. Preserve
unrelated edits.

Apply the contract's `Review readiness` checks before reading for synthesis or
writing a review. Record the target week and time zone on first execution; keep
them on retries. Verify acceptance of the daily consolidation through the closing
boundary, using its accepted coverage and host result. A partial accepted daily
result allows an explicitly incomplete review. If the prerequisite is missing,
running, failed or unaccepted, leave this review pending with its reason and target
week. Resume it after daily acceptance, without waiting for the next weekly trigger
or launching an extra daily capture. The 15-minute schedule gap is not a dependency.

## Review

1. Read all available daily records for the week in `journal/daily/` and their
   linked meeting records when available. Journal paths are relative to the
   containing store root. Read the latest accepted log entries and inspect
   each day's current `Coverage`, not just the
   presence of a daily file.
   Queue newly discovered evidence for the next daily consolidation and revisit
   the review after acceptance; do not launch another daily capture or cite an
   unwritten daily anchor. Also read `Open at period end` in the latest earlier
   accepted weekly review, if one exists.
2. Group log entries by subject/project using their keywords: consequential events,
   decisions and their rationale, hypotheses and their validation, fulfilled or
   pending commitments, open questions and corrections. Distinguish reported information and inference
   from direct observations. Repetition across sources is not confirmation.
   Account for every open item carried from the earlier review: resolved by an
   entry of this week, still open, or dropped with its justification.
3. Write or revise `journal/weekly/<ISO-year>/<ISO-year>-w<NN>.md` as
   `period-review@v1`, using the contract's mapping to the existing schema sections.
   Explain the editorial selection and cite the daily anchors for material claims
   by entry id (`<YYYY-MM-DD>#NN`).
   Keep the week's boundaries and what was known at its end explicit; label later
   evidence and resolutions separately rather than backdating them.
   Apply the contract's carry-forward rules to carried open items and list the
   earlier weekly review in `sources`.
4. If durable knowledge destinations are configured, identify relevant knowledge
   to promote and apply only validated changes, preserving uncertainty and source links.
5. If there was no relevant activity and coverage supports that conclusion, write
   no review and record completion against the accepted daily coverage. Empty logs
   with complete coverage support that conclusion; missing files do not. If records
   only reveal coverage gaps, retain an explicitly incomplete
   assessment rather than claiming inactivity. Without any daily evidence, report
   the gap and do not fabricate a review or its required source references.
   Open items carried from an earlier review do not by themselves require a
   review; the next one carries them from the latest review written.

## Accept and report

Regenerate affected catalogs and commit through the managed writer after
`engram check`. Reuse the existing period's path on retry and leave it unchanged
when the assessment has not materially changed. Report its path and a four-to-six
line summary through the host's configured delivery channel and language, including
material coverage limits. Record the outcome in the host's run record.
